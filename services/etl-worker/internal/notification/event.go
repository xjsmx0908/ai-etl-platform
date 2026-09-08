// Package notification provides a durable, content-safe outbox for governance
// events such as release-center approval tasks. Delivery is asynchronous and
// fail-open: a notification outage must not block review, approval, or publish.
package notification

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SourceReleaseCenter = "release_center"

	EventRequestOpened       = "release.request.opened"
	EventRequestStateChanged = "release.request.state_changed"
	EventDecisionRecorded    = "release.decision.recorded"

	maxPayloadValueRunes = 128
)

var allowedPayloadKeys = map[string]struct{}{
	"tenant_id":          {},
	"request_id":         {},
	"document_id":        {},
	"review_id":          {},
	"state":              {},
	"required_approvals": {},
	"approved_decisions": {},
	"approver_group_id":  {},
	"policy_id":          {},
	"recommendation":     {},
	"risk_level":         {},
	"decision":           {},
	"decision_id":        {},
	"decided_by":         {},
	"public_path":        {},
}

// Event is one durable notification. Payload values are identifiers and enums
// only; callers must not place findings, summaries, document text, or secrets.
type Event struct {
	ID          string            `json:"event_id"`
	DedupeKey   string            `json:"dedupe_key"`
	TenantID    string            `json:"tenant_id"`
	Source      string            `json:"source"`
	SourceID    string            `json:"source_id"`
	Type        string            `json:"event_type"`
	Payload     map[string]string `json:"payload"`
	Attempts    int               `json:"attempts,omitempty"`
	AvailableAt time.Time         `json:"available_at,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	PublishedAt *time.Time        `json:"published_at,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
}

// NewEvent builds a sanitized, idempotent event. Duplicate business facts reuse
// the same ID and dedupe key so retries do not fan out extra deliveries.
func NewEvent(source, sourceID, eventType, tenantID string, payload map[string]string) (Event, error) {
	source = strings.TrimSpace(source)
	sourceID = strings.TrimSpace(sourceID)
	eventType = strings.TrimSpace(eventType)
	tenantID = strings.TrimSpace(tenantID)
	if source == "" || sourceID == "" || eventType == "" || tenantID == "" {
		return Event{}, fmt.Errorf("notification event requires source, source id, type, and tenant")
	}
	clean := SanitizePayload(payload)
	if clean["tenant_id"] == "" {
		clean["tenant_id"] = tenantID
	}
	if clean["request_id"] == "" && source == SourceReleaseCenter {
		clean["request_id"] = sourceID
	}
	distinctive := strings.TrimSpace(clean["decision_id"])
	if distinctive == "" {
		distinctive = strings.TrimSpace(clean["state"])
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{tenantID, source, sourceID, eventType, distinctive}, "\x00")))
	key := hex.EncodeToString(sum[:])
	return Event{
		ID:        "ntf-" + key[:32],
		DedupeKey: key,
		TenantID:  tenantID,
		Source:    source,
		SourceID:  sourceID,
		Type:      eventType,
		Payload:   clean,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// SanitizePayload copies only allow-listed identifier fields and truncates
// values. Unknown keys, including findings and summaries, are dropped.
func SanitizePayload(payload map[string]string) map[string]string {
	clean := make(map[string]string, len(allowedPayloadKeys))
	for key, value := range payload {
		if _, ok := allowedPayloadKeys[strings.TrimSpace(key)]; !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if utf8.RuneCountInString(value) > maxPayloadValueRunes {
			runes := []rune(value)
			value = string(runes[:maxPayloadValueRunes])
		}
		clean[key] = value
	}
	return clean
}
