// Package scim translates a bounded SCIM 2.0 Users subset into the internal
// identity lifecycle seam. It never owns tenant, role, or capability policy.
package scim

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/identitylifecycle"
)

const (
	SCIMContentType   = "application/scim+json"
	userSchema        = "urn:ietf:params:scim:schemas:core:2.0:User"
	patchSchema       = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	errorSchema       = "urn:ietf:params:scim:api:messages:2.0:Error"
	maxUsernameLen    = 256
	maxExternalIDLen  = 512
	maxDisplayNameLen = 512
	maxEmailLen       = 320
	maxMetadataLen    = 256
)

var usernameFilter = regexp.MustCompile(`^userName eq "([^"\\]{1,256})"$`)

type Config struct {
	ConnectorID      string
	Issuer           string
	SubjectAttribute string
	BearerTokens     []string
	MaxBodyBytes     int64
}

type Handler struct {
	config       Config
	provisioner  identitylifecycle.Provisioner
	directory    identitylifecycle.ResourceDirectory
	tokenDigests [][32]byte
}

type User struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName,omitempty"`
	Active      bool     `json:"active"`
	Emails      []Email  `json:"emails,omitempty"`
	Roles       []any    `json:"roles,omitempty"`
	Groups      []any    `json:"groups,omitempty"`
	Meta        *Meta    `json:"meta,omitempty"`
}

type Email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}

type Meta struct {
	ResourceType string `json:"resourceType"`
	Version      string `json:"version,omitempty"`
}

type patchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []patchOperation `json:"Operations"`
}

type patchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func NewHandler(config Config, provisioner identitylifecycle.Provisioner, directory identitylifecycle.ResourceDirectory) (*Handler, error) {
	config.ConnectorID = strings.TrimSpace(config.ConnectorID)
	config.SubjectAttribute = strings.TrimSpace(config.SubjectAttribute)
	if config.ConnectorID == "" || config.SubjectAttribute != "externalId" || provisioner == nil || directory == nil ||
		config.MaxBodyBytes <= 0 || config.MaxBodyBytes > 1<<20 || len(config.BearerTokens) == 0 {
		return nil, fmt.Errorf("invalid SCIM configuration")
	}
	identity, err := externalidentity.Normalize(externalidentity.ExternalIdentity{Issuer: config.Issuer, Subject: "validation"})
	if err != nil {
		return nil, fmt.Errorf("invalid SCIM issuer")
	}
	config.Issuer = identity.Issuer
	handler := &Handler{config: config, provisioner: provisioner, directory: directory}
	for _, token := range config.BearerTokens {
		if token = strings.TrimSpace(token); token == "" {
			return nil, fmt.Errorf("invalid SCIM bearer token")
		}
		handler.tokenDigests = append(handler.tokenDigests, sha256.Sum256([]byte(token)))
	}
	handler.config.BearerTokens = nil
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", SCIMContentType)
	if !h.authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="scim"`)
		h.writeError(w, http.StatusUnauthorized, "invalid authentication")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/scim/v2/Users")
	if path == r.URL.Path || (path != "" && !strings.HasPrefix(path, "/")) {
		h.writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	resourceID := strings.Trim(path, "/")
	if resourceID != "" {
		parsed, err := uuid.Parse(resourceID)
		if err != nil || parsed.String() != resourceID {
			h.writeError(w, http.StatusBadRequest, "invalid resource identifier")
			return
		}
	}
	switch {
	case resourceID == "" && r.Method == http.MethodPost:
		h.create(w, r)
	case resourceID == "" && r.Method == http.MethodGet:
		h.lookup(w, r)
	case resourceID != "" && r.Method == http.MethodGet:
		h.get(w, r, resourceID)
	case resourceID != "" && (r.Method == http.MethodPut || r.Method == http.MethodPatch):
		h.update(w, r, resourceID)
	case resourceID != "" && r.Method == http.MethodDelete:
		h.remove(w, r, resourceID)
	default:
		w.Header().Set("Allow", "GET, POST, PUT, PATCH, DELETE")
		h.writeError(w, http.StatusMethodNotAllowed, "operation not supported")
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var user User
	if !h.decode(w, r, &user) {
		return
	}
	if !exactSchema(user.Schemas, userSchema) || !validUser(user, true) ||
		len(user.Roles) != 0 || len(user.Groups) != 0 {
		h.writeError(w, http.StatusBadRequest, "invalid user resource")
		return
	}
	command := identitylifecycle.LifecycleCommand{
		Operation: identitylifecycle.OperationCreate, ConnectorID: h.config.ConnectorID,
		ProviderResourceID: resourceID(h.config.ConnectorID, user.ExternalID), Identity: externalidentity.ExternalIdentity{Issuer: h.config.Issuer, Subject: user.ExternalID},
		Username: user.UserName, DisplayName: stringPointer(user.DisplayName), Email: stringPointer(primaryEmail(user.Emails)), Active: &user.Active,
		SourceVersion: sourceVersion(r), CorrelationID: correlationID(r),
	}
	var ok bool
	command.IdempotencyKey, ok = h.requestIdempotencyKey(w, r, command)
	if !ok {
		return
	}
	result, err := h.provisioner.Apply(r.Context(), command)
	if err != nil {
		h.writeLifecycleError(w, err)
		return
	}
	response := userFromResult(result, "")
	w.Header().Set("Location", "/scim/v2/Users/"+response.ID)
	h.writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) lookup(w http.ResponseWriter, r *http.Request) {
	match := usernameFilter.FindStringSubmatch(r.URL.Query().Get("filter"))
	if len(match) != 2 {
		h.writeError(w, http.StatusBadRequest, "unsupported filter")
		return
	}
	results, err := h.directory.FindByUsername(r.Context(), h.config.ConnectorID, match[1])
	if err != nil {
		h.writeLifecycleError(w, err)
		return
	}
	resources := make([]User, 0, len(results))
	for _, result := range results {
		if result.Deleted {
			continue
		}
		resources = append(resources, userFromResult(result, ""))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(resources), "startIndex": 1, "itemsPerPage": len(resources), "Resources": resources,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, id string) {
	result, err := h.directory.Get(r.Context(), h.config.ConnectorID, id)
	if err != nil {
		h.writeLifecycleError(w, err)
		return
	}
	if result.Deleted {
		h.writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	h.writeJSON(w, http.StatusOK, userFromResult(result, ""))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, id string) {
	command := identitylifecycle.LifecycleCommand{Operation: identitylifecycle.OperationUpdate,
		ConnectorID: h.config.ConnectorID, ProviderResourceID: id, SourceVersion: sourceVersion(r), CorrelationID: correlationID(r)}
	if r.Method == http.MethodPut {
		var user User
		if !h.decode(w, r, &user) {
			return
		}
		if !exactSchema(user.Schemas, userSchema) || !validUser(user, false) || len(user.Roles) != 0 || len(user.Groups) != 0 {
			h.writeError(w, http.StatusBadRequest, "invalid user resource")
			return
		}
		command.Username, command.DisplayName, command.Email, command.Active = user.UserName, stringPointer(user.DisplayName), stringPointer(primaryEmail(user.Emails)), &user.Active
		if user.ExternalID != "" {
			command.Identity = externalidentity.ExternalIdentity{Issuer: h.config.Issuer, Subject: user.ExternalID}
		}
	} else {
		var request patchRequest
		if !h.decode(w, r, &request) {
			return
		}
		if !exactSchema(request.Schemas, patchSchema) || len(request.Operations) == 0 || len(request.Operations) > 10 {
			h.writeError(w, http.StatusBadRequest, "invalid patch request")
			return
		}
		for _, operation := range request.Operations {
			if !strings.EqualFold(operation.Op, "replace") {
				h.writeError(w, http.StatusBadRequest, "unsupported patch operation")
				return
			}
			switch strings.ToLower(strings.TrimSpace(operation.Path)) {
			case "active":
				if err := json.Unmarshal(operation.Value, &command.Active); err != nil || command.Active == nil {
					h.writeError(w, http.StatusBadRequest, "invalid active value")
					return
				}
			case "displayname":
				var displayName string
				if err := json.Unmarshal(operation.Value, &displayName); err != nil || len(displayName) > maxDisplayNameLen {
					h.writeError(w, http.StatusBadRequest, "invalid displayName value")
					return
				}
				command.DisplayName = &displayName
			default:
				h.writeError(w, http.StatusBadRequest, "unsupported patch path")
				return
			}
		}
	}
	var ok bool
	command.IdempotencyKey, ok = h.requestIdempotencyKey(w, r, command)
	if !ok {
		return
	}
	result, err := h.provisioner.Apply(r.Context(), command)
	if err != nil {
		h.writeLifecycleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, userFromResult(result, ""))
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, id string) {
	command := identitylifecycle.LifecycleCommand{
		Operation: identitylifecycle.OperationDelete, ConnectorID: h.config.ConnectorID,
		ProviderResourceID: id, SourceVersion: sourceVersion(r), CorrelationID: correlationID(r),
	}
	var ok bool
	command.IdempotencyKey, ok = h.requestIdempotencyKey(w, r, command)
	if !ok {
		return
	}
	_, err := h.provisioner.Apply(r.Context(), command)
	if err != nil {
		h.writeLifecycleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	reader := http.MaxBytesReader(w, r.Body, h.config.MaxBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			h.writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func (h *Handler) authorized(header string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	accepted := 0
	for _, candidate := range h.tokenDigests {
		accepted |= subtle.ConstantTimeCompare(digest[:], candidate[:])
	}
	return accepted == 1
}

func (h *Handler) writeLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identitylifecycle.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, identitylifecycle.ErrConflict):
		h.writeError(w, http.StatusConflict, "resource conflict")
	case errors.Is(err, identitylifecycle.ErrInvalid):
		h.writeError(w, http.StatusBadRequest, "invalid resource")
	default:
		h.writeError(w, http.StatusServiceUnavailable, "provisioning unavailable")
	}
}

func (h *Handler) writeError(w http.ResponseWriter, status int, detail string) {
	h.writeJSON(w, status, map[string]any{"schemas": []string{errorSchema}, "status": fmt.Sprint(status), "detail": detail})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func exactSchema(schemas []string, expected string) bool {
	return len(schemas) == 1 && schemas[0] == expected
}

func primaryEmail(emails []Email) string {
	for _, email := range emails {
		if email.Primary {
			return email.Value
		}
	}
	if len(emails) > 0 {
		return emails[0].Value
	}
	return ""
}

func (h *Handler) requestIdempotencyKey(w http.ResponseWriter, r *http.Request, command identitylifecycle.LifecycleCommand) (string, bool) {
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" {
		if len(key) > maxMetadataLen {
			h.writeError(w, http.StatusBadRequest, "invalid idempotency key")
			return "", false
		}
		return key, true
	}
	if len(sourceVersion(r)) > maxMetadataLen {
		h.writeError(w, http.StatusBadRequest, "invalid source version")
		return "", false
	}
	command.IdempotencyKey = ""
	encoded, _ := json.Marshal(command)
	digest := sha256.Sum256(encoded)
	return "scim:" + fmt.Sprintf("%x", digest[:]), true
}

func sourceVersion(r *http.Request) string { return strings.TrimSpace(r.Header.Get("If-Match")) }

func correlationID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); len(value) <= maxMetadataLen {
		return value
	}
	return ""
}

func resourceID(connectorID, subject string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(connectorID+":"+subject)).String()
}

func stringPointer(value string) *string { return &value }

func validUser(user User, requireExternalID bool) bool {
	username := strings.TrimSpace(user.UserName)
	externalID := strings.TrimSpace(user.ExternalID)
	if username == "" || username != user.UserName || len(username) > maxUsernameLen ||
		len(user.DisplayName) > maxDisplayNameLen || len(primaryEmail(user.Emails)) > maxEmailLen || len(user.Emails) > 10 {
		return false
	}
	if requireExternalID && externalID == "" {
		return false
	}
	return externalID == user.ExternalID && len(externalID) <= maxExternalIDLen
}

func userFromResult(result identitylifecycle.LifecycleResult, externalID string) User {
	user := User{Schemas: []string{userSchema}, ID: result.ProviderResourceID, ExternalID: externalID,
		UserName: result.Username, DisplayName: result.DisplayName, Active: result.Active,
		Meta: &Meta{ResourceType: "User", Version: result.SourceVersion}}
	if result.Email != "" {
		user.Emails = []Email{{Value: result.Email, Primary: true}}
	}
	return user
}
