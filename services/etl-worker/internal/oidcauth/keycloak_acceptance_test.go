package oidcauth_test

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"ai-etl-pipeline/internal/oidcauth"
)

var keycloakLoginForm = regexp.MustCompile(`(?s)<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)

// TestKeycloakAuthorizationCodeAcceptance is an explicit interoperability gate.
// It is skipped in ordinary unit runs and executes the public OIDC seam against
// a real Keycloak realm when the acceptance environment is supplied.
func TestKeycloakAuthorizationCodeAcceptance(t *testing.T) {
	issuer := os.Getenv("OIDC_KEYCLOAK_TEST_ISSUER")
	clientSecret := os.Getenv("OIDC_KEYCLOAK_TEST_CLIENT_SECRET")
	username := os.Getenv("OIDC_KEYCLOAK_TEST_USERNAME")
	password := os.Getenv("OIDC_KEYCLOAK_TEST_PASSWORD")
	if issuer == "" || clientSecret == "" || username == "" || password == "" {
		t.Skip("Keycloak acceptance environment is not configured")
	}
	const redirectURI = "https://rag.example.com/api/auth/oidc/callback"
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", ClientSecret: clientSecret,
		RedirectURI: redirectURI,
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("initialize against Keycloak: %v", err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), "/")
	if err != nil {
		t.Fatalf("start Keycloak flow: %v", err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create browser cookie jar: %v", err)
	}
	browser := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	loginPage, err := browser.Get(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("open Keycloak authorization endpoint: %v", err)
	}
	page, readErr := io.ReadAll(io.LimitReader(loginPage.Body, 1<<20))
	_ = loginPage.Body.Close()
	if readErr != nil || loginPage.StatusCode != http.StatusOK {
		t.Fatalf("read Keycloak login page: status=%d err=%v", loginPage.StatusCode, readErr)
	}
	match := keycloakLoginForm.FindSubmatch(page)
	if len(match) != 2 {
		t.Fatal("Keycloak login form was not found")
	}
	action := html.UnescapeString(string(match[1]))
	browser.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host == "rag.example.com" {
			return http.ErrUseLastResponse
		}
		return nil
	}
	loginResponse, err := browser.PostForm(action, url.Values{
		"username": {username}, "password": {password}, "credentialId": {""},
	})
	if err != nil {
		t.Fatalf("submit Keycloak login: %v", err)
	}
	defer loginResponse.Body.Close()
	callback, err := url.Parse(loginResponse.Header.Get("Location"))
	if err != nil || callback.Host != "rag.example.com" {
		failurePage, _ := io.ReadAll(io.LimitReader(loginResponse.Body, 1<<20))
		t.Fatalf("Keycloak did not return the registered callback: status=%d location=%q login_error=%q fields=%q", loginResponse.StatusCode, loginResponse.Header.Get("Location"), keycloakErrorMessage(failurePage), keycloakFormFields(failurePage))
	}
	if callback.Query().Get("state") != start.State || callback.Query().Get("code") == "" {
		t.Fatalf("Keycloak callback lacks the expected state/code: %s", callback.Redacted())
	}
	principal, _, err := flow.Complete(context.Background(), start.State, callback.Query().Get("state"), callback.Query().Get("code"))
	if err != nil {
		t.Fatalf("complete Keycloak authorization code flow: %v", err)
	}
	if principal.SubjectID != "user-42" || principal.TenantID != "tenant-a" {
		t.Fatalf("unexpected policy principal: %+v", principal)
	}
}

func keycloakFormFields(page []byte) []string {
	pattern := regexp.MustCompile(`<input[^>]*name="([^"]+)"[^>]*>`)
	matches := pattern.FindAllSubmatch(page, -1)
	fields := make([]string, 0, len(matches))
	for _, match := range matches {
		fields = append(fields, string(match[1]))
	}
	return fields
}

func keycloakErrorMessage(page []byte) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?s)<span[^>]*id="input-error"[^>]*>(.*?)</span>`),
		regexp.MustCompile(`(?s)<span[^>]*class="[^"]*kc-feedback-text[^"]*"[^>]*>(.*?)</span>`),
	}
	var message []byte
	for _, pattern := range patterns {
		match := pattern.FindSubmatch(page)
		if len(match) == 2 {
			message = match[1]
			break
		}
	}
	if message == nil {
		return "unknown"
	}
	tags := regexp.MustCompile(`<[^>]+>`)
	return html.UnescapeString(string(tags.ReplaceAll(message, nil)))
}
