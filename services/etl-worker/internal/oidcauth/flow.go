package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/session"
)

var ErrInvalidTransaction = errors.New("oidc browser transaction is invalid or expired")

type BrowserStart struct {
	AuthorizationURL string
	State            string
	ExpiresIn        time.Duration
}

type transactionKind string

const (
	transactionKindLogin            transactionKind = "login"
	transactionKindReauthentication transactionKind = "reauthentication"
	transactionKindLogout           transactionKind = "logout"
)

type browserTransaction struct {
	Nonce            string
	CodeVerifier     string
	ReturnTo         string
	ExpiresAt        time.Time
	CredentialDigest string          `json:",omitempty"`
	TenantID         string          `json:",omitempty"`
	SubjectID        string          `json:",omitempty"`
	Action           string          `json:",omitempty"`
	Kind             transactionKind `json:"kind"`
}

type ReauthenticationStartCommand struct {
	Credential string
	Action     string
	ReturnTo   string
	Principal  auth.Principal
}

type ReauthenticationCompleteCommand struct {
	CookieState       string
	CallbackState     string
	Code              string
	CurrentCredential string
}

type ReauthenticationResult struct {
	Principal auth.Principal
	Evidence  session.AuthenticationEvidence
	Action    string
	ReturnTo  string
}

type TransactionStore interface {
	Save(context.Context, string, browserTransaction) error
	Consume(context.Context, string) (browserTransaction, error)
}

type MemoryTransactionStore struct {
	mu           sync.Mutex
	transactions map[string]browserTransaction
}

func NewMemoryTransactionStore() *MemoryTransactionStore {
	return &MemoryTransactionStore{transactions: make(map[string]browserTransaction)}
}

func (s *MemoryTransactionStore) Save(_ context.Context, state string, transaction browserTransaction) error {
	if s == nil || state == "" {
		return ErrInvalidTransaction
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions[state] = transaction
	return nil
}

func (s *MemoryTransactionStore) Consume(_ context.Context, state string) (browserTransaction, error) {
	if s == nil || state == "" {
		return browserTransaction{}, ErrInvalidTransaction
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction, found := s.transactions[state]
	delete(s.transactions, state)
	if !found || time.Now().After(transaction.ExpiresAt) {
		return browserTransaction{}, ErrInvalidTransaction
	}
	return transaction, nil
}

type Flow struct {
	authenticator *Authenticator
	transactions  TransactionStore
	ttl           time.Duration
}

func NewFlow(authenticator *Authenticator, transactions TransactionStore, ttl time.Duration) *Flow {
	return &Flow{authenticator: authenticator, transactions: transactions, ttl: ttl}
}

func (f *Flow) Start(ctx context.Context, returnTo string) (BrowserStart, error) {
	return f.startLogin(ctx, returnTo, nil)
}

func (f *Flow) StartLoginWithAssurance(ctx context.Context, returnTo string) (BrowserStart, error) {
	return f.startLogin(ctx, returnTo, url.Values{
		"prompt": {"login"}, "max_age": {"0"}, "acr_values": {demoACR},
	})
}

func (f *Flow) StartLogout(ctx context.Context, returnTo string) (BrowserStart, error) {
	if f == nil || f.authenticator == nil || f.transactions == nil || f.ttl <= 0 || f.ttl > 15*time.Minute ||
		f.authenticator.discovery.EndSessionEndpoint == "" || f.authenticator.config.LogoutRedirectURI == "" {
		return BrowserStart{}, ErrInvalidTransaction
	}
	state, err := randomURLToken(32)
	if err != nil {
		return BrowserStart{}, fmt.Errorf("%w: generate logout state", ErrInvalidTransaction)
	}
	transaction := browserTransaction{
		ReturnTo: safeReturnPath(returnTo), ExpiresAt: time.Now().Add(f.ttl), Kind: transactionKindLogout,
	}
	if err := f.transactions.Save(ctx, state, transaction); err != nil {
		return BrowserStart{}, fmt.Errorf("%w: save logout transaction", ErrInvalidTransaction)
	}
	endpoint, err := url.Parse(f.authenticator.discovery.EndSessionEndpoint)
	if err != nil {
		return BrowserStart{}, ErrInvalidTransaction
	}
	query := endpoint.Query()
	query.Set("client_id", f.authenticator.config.ClientID)
	query.Set("post_logout_redirect_uri", f.authenticator.config.LogoutRedirectURI)
	query.Set("state", state)
	endpoint.RawQuery = query.Encode()
	return BrowserStart{AuthorizationURL: endpoint.String(), State: state, ExpiresIn: f.ttl}, nil
}

func (f *Flow) CompleteLogout(ctx context.Context, cookieState, callbackState string) (string, error) {
	if f == nil || f.transactions == nil || cookieState == "" || callbackState == "" ||
		len(cookieState) != len(callbackState) ||
		subtle.ConstantTimeCompare([]byte(cookieState), []byte(callbackState)) != 1 {
		return "", ErrInvalidTransaction
	}
	transaction, err := f.transactions.Consume(ctx, cookieState)
	if err != nil || transaction.Kind != transactionKindLogout {
		return "", ErrInvalidTransaction
	}
	return safeReturnPath(transaction.ReturnTo), nil
}

func (f *Flow) startLogin(ctx context.Context, returnTo string, extraQuery url.Values) (BrowserStart, error) {
	if f == nil || f.authenticator == nil || f.transactions == nil || f.ttl <= 0 || f.ttl > 15*time.Minute {
		return BrowserStart{}, ErrInvalidTransaction
	}
	return f.startBrowserTransaction(ctx, browserTransaction{
		ReturnTo: safeReturnPath(returnTo), ExpiresAt: time.Now().Add(f.ttl), Kind: transactionKindLogin,
	}, extraQuery)
}

func (f *Flow) startBrowserTransaction(ctx context.Context, transaction browserTransaction, extraQuery url.Values) (BrowserStart, error) {
	state, err := randomURLToken(32)
	if err != nil {
		return BrowserStart{}, fmt.Errorf("%w: generate state", ErrInvalidTransaction)
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return BrowserStart{}, fmt.Errorf("%w: generate nonce", ErrInvalidTransaction)
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return BrowserStart{}, fmt.Errorf("%w: generate PKCE verifier", ErrInvalidTransaction)
	}
	transaction.Nonce = nonce
	transaction.CodeVerifier = verifier
	if err := f.transactions.Save(ctx, state, transaction); err != nil {
		return BrowserStart{}, fmt.Errorf("%w: save transaction", ErrInvalidTransaction)
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.authenticator.config.ClientID},
		"redirect_uri":          {f.authenticator.config.RedirectURI},
		"scope":                 {"openid"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	for key, values := range extraQuery {
		query[key] = values
	}
	authorizationURL, err := url.Parse(f.authenticator.discovery.AuthorizationEndpoint)
	if err != nil {
		return BrowserStart{}, fmt.Errorf("%w: invalid authorization endpoint", ErrInvalidTransaction)
	}
	mergedQuery := authorizationURL.Query()
	for key, values := range query {
		mergedQuery[key] = values
	}
	authorizationURL.RawQuery = mergedQuery.Encode()
	return BrowserStart{
		AuthorizationURL: authorizationURL.String(),
		State:            state,
		ExpiresIn:        f.ttl,
	}, nil
}

func (f *Flow) StartReauthentication(ctx context.Context, command ReauthenticationStartCommand) (BrowserStart, error) {
	if f == nil || f.authenticator == nil || f.transactions == nil || f.ttl <= 0 || f.ttl > 15*time.Minute ||
		strings.TrimSpace(command.Credential) == "" || strings.TrimSpace(command.Action) == "" ||
		strings.TrimSpace(command.Principal.TenantID) == "" || strings.TrimSpace(command.Principal.SubjectID) == "" ||
		command.Principal.AuthenticationMethod != auth.AuthenticationMethodFederated {
		return BrowserStart{}, ErrInvalidTransaction
	}
	digest := sha256.Sum256([]byte(command.Credential))
	transaction := browserTransaction{
		ReturnTo: safeReturnPath(command.ReturnTo), ExpiresAt: time.Now().Add(f.ttl),
		CredentialDigest: base64.RawURLEncoding.EncodeToString(digest[:]),
		TenantID:         command.Principal.TenantID, SubjectID: command.Principal.SubjectID,
		Action: command.Action, Kind: transactionKindReauthentication,
	}
	return f.startBrowserTransaction(ctx, transaction, url.Values{
		"prompt": {"login"}, "max_age": {"0"}, "acr_values": {demoACR},
	})
}

func (f *Flow) Complete(ctx context.Context, cookieState, callbackState, code string) (auth.Principal, string, error) {
	result, returnTo, err := f.completeLogin(ctx, cookieState, callbackState, code, false)
	return result.Principal, returnTo, err
}

func (f *Flow) CompleteLoginWithEvidence(ctx context.Context, cookieState, callbackState, code string) (AuthenticationResult, string, error) {
	return f.completeLogin(ctx, cookieState, callbackState, code, true)
}

func (f *Flow) completeLogin(ctx context.Context, cookieState, callbackState, code string, requireEvidence bool) (AuthenticationResult, string, error) {
	if f == nil || f.authenticator == nil || f.transactions == nil || cookieState == "" || callbackState == "" || code == "" ||
		len(cookieState) != len(callbackState) || subtle.ConstantTimeCompare([]byte(cookieState), []byte(callbackState)) != 1 {
		return AuthenticationResult{}, "", ErrInvalidTransaction
	}
	transaction, err := f.transactions.Consume(ctx, cookieState)
	if err != nil || transaction.Kind != transactionKindLogin {
		return AuthenticationResult{}, "", ErrInvalidTransaction
	}
	exchange := CodeExchange{
		Code: code, CodeVerifier: transaction.CodeVerifier, Nonce: transaction.Nonce,
	}
	var result AuthenticationResult
	if requireEvidence {
		result, err = f.authenticator.AuthenticateWithEvidence(ctx, exchange)
	} else {
		result.Principal, err = f.authenticator.Authenticate(ctx, exchange)
	}
	if err != nil {
		return AuthenticationResult{}, "", err
	}
	return result, transaction.ReturnTo, nil
}

func (f *Flow) CompleteReauthentication(ctx context.Context, command ReauthenticationCompleteCommand) (ReauthenticationResult, error) {
	if f == nil || f.authenticator == nil || f.transactions == nil ||
		strings.TrimSpace(command.CookieState) == "" || strings.TrimSpace(command.CallbackState) == "" ||
		strings.TrimSpace(command.Code) == "" || strings.TrimSpace(command.CurrentCredential) == "" ||
		len(command.CookieState) != len(command.CallbackState) ||
		subtle.ConstantTimeCompare([]byte(command.CookieState), []byte(command.CallbackState)) != 1 {
		return ReauthenticationResult{}, ErrInvalidTransaction
	}
	transaction, err := f.transactions.Consume(ctx, command.CookieState)
	if err != nil || transaction.Kind != transactionKindReauthentication || transaction.CredentialDigest == "" ||
		transaction.TenantID == "" || transaction.SubjectID == "" || transaction.Action == "" {
		return ReauthenticationResult{}, ErrInvalidTransaction
	}
	credentialDigest := sha256.Sum256([]byte(command.CurrentCredential))
	expectedDigest := base64.RawURLEncoding.EncodeToString(credentialDigest[:])
	if len(expectedDigest) != len(transaction.CredentialDigest) ||
		subtle.ConstantTimeCompare([]byte(expectedDigest), []byte(transaction.CredentialDigest)) != 1 {
		return ReauthenticationResult{}, ErrInvalidTransaction
	}
	result, err := f.authenticator.AuthenticateWithEvidence(ctx, CodeExchange{
		Code: command.Code, CodeVerifier: transaction.CodeVerifier, Nonce: transaction.Nonce,
	})
	if err != nil {
		return ReauthenticationResult{}, err
	}
	now := f.authenticator.now()
	if result.Principal.TenantID != transaction.TenantID || result.Principal.SubjectID != transaction.SubjectID ||
		result.Evidence.Assurance != demoAssurance || result.Evidence.AuthenticatedAt.IsZero() ||
		result.Evidence.AuthenticatedAt.After(now) || now.Sub(result.Evidence.AuthenticatedAt) >= demoEvidenceFreshness {
		return ReauthenticationResult{}, ErrAuthentication
	}
	return ReauthenticationResult{
		Principal: result.Principal, Evidence: result.Evidence,
		Action: transaction.Action, ReturnTo: transaction.ReturnTo,
	}, nil
}

func randomURLToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnPath(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.Contains(raw, `\`) || strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 ||
		parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || strings.Contains(parsed.Path, `\`) {
		return "/"
	}
	return parsed.RequestURI()
}

var _ TransactionStore = (*MemoryTransactionStore)(nil)
