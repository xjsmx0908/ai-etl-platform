// Package oidcauth exchanges and validates OIDC authorization-code results.
// Provider claims identify an external subject only; internal authorization is
// always resolved through the external identity directory.
package oidcauth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/session"
)

var ErrAuthentication = errors.New("oidc authentication failed")

const (
	demoACR               = "2"
	demoAssurance         = session.AssuranceDemoMFA
	demoEvidenceFreshness = 10 * time.Minute
)

type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	HTTPClient   *http.Client
	Clock        func() time.Time
}

type CodeExchange struct {
	Code         string
	CodeVerifier string
	Nonce        string
}

type Authenticator struct {
	config    Config
	directory externalidentity.Directory
	client    *http.Client
	discovery discoveryDocument
	now       func() time.Time

	keysMu sync.RWMutex
	keys   map[string]any
}

type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type tokenResponse struct {
	IDToken string `json:"id_token"`
}

type idTokenClaims struct {
	Nonce              string           `json:"nonce"`
	AuthorizedParty    string           `json:"azp"`
	ACR                string           `json:"acr"`
	AMR                []string         `json:"amr"`
	AuthenticationTime *jwt.NumericDate `json:"auth_time"`
	jwt.RegisteredClaims
}

type AuthenticationResult struct {
	Principal auth.Principal
	Evidence  session.AuthenticationEvidence
}

func New(ctx context.Context, cfg Config, directory externalidentity.Directory) (*Authenticator, error) {
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURI == "" || directory == nil {
		return nil, fmt.Errorf("%w: incomplete configuration", ErrAuthentication)
	}
	issuerURL, err := url.Parse(cfg.Issuer)
	if err != nil || issuerURL.Scheme != "https" || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return nil, fmt.Errorf("%w: issuer must be an absolute HTTPS URL", ErrAuthentication)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	a := &Authenticator{config: cfg, directory: directory, client: client, now: now}
	if err := a.loadDiscovery(ctx); err != nil {
		return nil, err
	}
	if err := a.refreshKeys(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Authenticator) Authenticate(ctx context.Context, exchange CodeExchange) (auth.Principal, error) {
	result, err := a.authenticate(ctx, exchange, false)
	return result.Principal, err
}

func (a *Authenticator) AuthenticateWithEvidence(ctx context.Context, exchange CodeExchange) (AuthenticationResult, error) {
	return a.authenticate(ctx, exchange, true)
}

func (a *Authenticator) authenticate(ctx context.Context, exchange CodeExchange, requireEvidence bool) (AuthenticationResult, error) {
	if strings.TrimSpace(exchange.Code) == "" || strings.TrimSpace(exchange.CodeVerifier) == "" || strings.TrimSpace(exchange.Nonce) == "" {
		return AuthenticationResult{}, fmt.Errorf("%w: incomplete code exchange", ErrAuthentication)
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {exchange.Code},
		"code_verifier": {exchange.CodeVerifier},
		"client_id":     {a.config.ClientID},
		"redirect_uri":  {a.config.RedirectURI},
	}
	if a.config.ClientSecret != "" {
		form.Set("client_secret", a.config.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return AuthenticationResult{}, fmt.Errorf("%w: build token request", ErrAuthentication)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return AuthenticationResult{}, fmt.Errorf("%w: token endpoint unavailable", ErrAuthentication)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AuthenticationResult{}, fmt.Errorf("%w: token endpoint rejected exchange", ErrAuthentication)
	}
	var tokenResult tokenResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&tokenResult); err != nil || tokenResult.IDToken == "" {
		return AuthenticationResult{}, fmt.Errorf("%w: invalid token response", ErrAuthentication)
	}
	refreshed, err := a.ensureSigningKey(ctx, tokenResult.IDToken)
	if err != nil {
		return AuthenticationResult{}, fmt.Errorf("%w: invalid ID token", ErrAuthentication)
	}

	claims := &idTokenClaims{}
	token, err := a.parseIDToken(tokenResult.IDToken, claims)
	if err != nil && !refreshed && errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		if refreshErr := a.refreshKeys(ctx); refreshErr != nil {
			return AuthenticationResult{}, fmt.Errorf("%w: invalid ID token", ErrAuthentication)
		}
		claims = &idTokenClaims{}
		token, err = a.parseIDToken(tokenResult.IDToken, claims)
	}
	if err != nil || !token.Valid || claims.Subject == "" || claims.Nonce != exchange.Nonce ||
		claims.IssuedAt == nil || claims.IssuedAt.Time.After(a.now().Add(time.Minute)) ||
		(len(claims.Audience) > 1 && claims.AuthorizedParty != a.config.ClientID) {
		return AuthenticationResult{}, fmt.Errorf("%w: invalid ID token", ErrAuthentication)
	}
	var evidence session.AuthenticationEvidence
	if requireEvidence {
		if claims.ACR != demoACR || !exactAMR(claims.AMR, "pwd", "otp") || claims.AuthenticationTime == nil ||
			claims.AuthenticationTime.Time.After(a.now()) || a.now().Sub(claims.AuthenticationTime.Time) >= demoEvidenceFreshness {
			return AuthenticationResult{}, fmt.Errorf("%w: authentication evidence is not approved", ErrAuthentication)
		}
		evidence = session.AuthenticationEvidence{Assurance: demoAssurance, AuthenticatedAt: claims.AuthenticationTime.Time.UTC()}
	}
	principal, err := a.directory.Resolve(ctx, externalidentity.ExternalIdentity{
		Issuer: claims.Issuer, Subject: claims.Subject,
	})
	if err != nil {
		return AuthenticationResult{}, fmt.Errorf("%w: external identity is not authorized", ErrAuthentication)
	}
	return AuthenticationResult{Principal: principal, Evidence: evidence}, nil
}

func exactAMR(actual []string, required ...string) bool {
	if len(actual) != len(required) {
		return false
	}
	want := make(map[string]bool, len(required))
	for _, value := range required {
		want[value] = true
	}
	for _, value := range actual {
		if !want[value] {
			return false
		}
		delete(want, value)
	}
	return len(want) == 0
}

func (a *Authenticator) parseIDToken(rawToken string, claims *idTokenClaims) (*jwt.Token, error) {
	return jwt.ParseWithClaims(rawToken, claims, a.keyForToken,
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(a.config.Issuer),
		jwt.WithAudience(a.config.ClientID),
		jwt.WithTimeFunc(a.now),
		jwt.WithExpirationRequired(),
	)
}

// ensureSigningKey reads only the untrusted JOSE header to select a key. A
// cache miss performs one bounded JWKS refresh before full token validation.
func (a *Authenticator) ensureSigningKey(ctx context.Context, rawToken string) (bool, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return false, fmt.Errorf("malformed token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false, fmt.Errorf("malformed token header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil ||
		header.Algorithm != jwt.SigningMethodRS256.Alg() || header.KeyID == "" {
		return false, fmt.Errorf("unsupported token header")
	}
	a.keysMu.RLock()
	_, found := a.keys[header.KeyID]
	a.keysMu.RUnlock()
	if found {
		return false, nil
	}
	if err := a.refreshKeys(ctx); err != nil {
		return true, err
	}
	a.keysMu.RLock()
	_, found = a.keys[header.KeyID]
	a.keysMu.RUnlock()
	if !found {
		return true, fmt.Errorf("unknown key id")
	}
	return true, nil
}

func (a *Authenticator) loadDiscovery(ctx context.Context) error {
	var doc discoveryDocument
	discoveryURL := strings.TrimSuffix(a.config.Issuer, "/") + "/.well-known/openid-configuration"
	if err := a.getJSON(ctx, discoveryURL, &doc); err != nil {
		return fmt.Errorf("%w: discovery unavailable", ErrAuthentication)
	}
	if doc.Issuer != a.config.Issuer || !sameHTTPSOrigin(doc.TokenEndpoint, a.config.Issuer) ||
		!sameHTTPSOrigin(doc.JWKSURI, a.config.Issuer) || !sameHTTPSOrigin(doc.AuthorizationEndpoint, a.config.Issuer) {
		return fmt.Errorf("%w: invalid discovery metadata", ErrAuthentication)
	}
	a.discovery = doc
	return nil
}

func (a *Authenticator) refreshKeys(ctx context.Context) error {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := a.getJSON(ctx, a.discovery.JWKSURI, &set); err != nil {
		return fmt.Errorf("%w: JWKS unavailable", ErrAuthentication)
	}
	keys := make(map[string]any)
	for _, raw := range set.Keys {
		if raw.Kty != "RSA" || raw.Use != "sig" || raw.Alg != jwt.SigningMethodRS256.Alg() || raw.Kid == "" {
			continue
		}
		nBytes, nErr := base64.RawURLEncoding.DecodeString(raw.N)
		eBytes, eErr := base64.RawURLEncoding.DecodeString(raw.E)
		if nErr != nil || eErr != nil || len(nBytes) == 0 || len(eBytes) == 0 {
			continue
		}
		e := new(big.Int).SetBytes(eBytes)
		if !e.IsInt64() || e.Int64() < 3 {
			continue
		}
		keys[raw.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e.Int64())}
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: JWKS contains no allowed signing keys", ErrAuthentication)
	}
	a.keysMu.Lock()
	a.keys = keys
	a.keysMu.Unlock()
	return nil
}

func (a *Authenticator) keyForToken(token *jwt.Token) (any, error) {
	if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
		return nil, fmt.Errorf("unexpected signing algorithm")
	}
	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("missing key id")
	}
	a.keysMu.RLock()
	key, found := a.keys[kid]
	a.keysMu.RUnlock()
	if !found {
		return nil, fmt.Errorf("unknown key id")
	}
	return key, nil
}

func (a *Authenticator) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(target)
}

func sameHTTPSOrigin(raw, issuer string) bool {
	u, err := url.Parse(raw)
	base, baseErr := url.Parse(issuer)
	return err == nil && baseErr == nil && u.Scheme == "https" && u.Host == base.Host && u.User == nil && u.Fragment == ""
}
