package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
)

type apiOIDCProvider struct {
	issuer, clientID string
	state, nonce     string
	subject          string
	authorizationErr error
	exchangeErr      error
}

func (p *apiOIDCProvider) Issuer() string   { return p.issuer }
func (p *apiOIDCProvider) ClientID() string { return p.clientID }
func (p *apiOIDCProvider) AuthorizationURL(_ context.Context, state, nonce, _ string) (string, error) {
	p.state, p.nonce = state, nonce
	return p.issuer + "/authorize?state=" + url.QueryEscape(state), p.authorizationErr
}
func (p *apiOIDCProvider) Exchange(_ context.Context, _, _ string) (auth.OIDCIdentity, error) {
	return auth.OIDCIdentity{Issuer: p.issuer, Subject: p.subject, Nonce: p.nonce}, p.exchangeErr
}

func configuredOIDCTestServer(t *testing.T) (*Server, *apiOIDCProvider) {
	t.Helper()
	server, _ := newTestServerWithConfig(ServerConfig{AllowInsecureCookies: true, PublicOrigin: "http://127.0.0.1:8080"})
	provider := &apiOIDCProvider{issuer: "https://idp.example/realms/ops", clientID: "client", subject: "subject-1"}
	if err := server.app.ConfigureOIDC(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := server.app.ProvisionOIDCIdentity(t.Context(), app.OIDCProvisionInput{Issuer: provider.issuer, Subject: provider.subject, UserID: "usr_bootstrap"}); err != nil {
		t.Fatal(err)
	}
	return server, provider
}

func beginOIDCTestFlow(t *testing.T, server *Server, redirect string) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/login?redirect="+url.QueryEscape(redirect), nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("OIDC login status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("OIDC login response is cacheable")
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == oidcFlowCookie {
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/api/v1/oidc" || cookie.Secure {
				t.Fatalf("flow cookie flags=%#v", cookie)
			}
			return cookie, rec.Header().Get("Location")
		}
	}
	t.Fatal("OIDC flow cookie missing")
	return nil, ""
}

func TestOIDCStatusAndDisabledEndpointsAreMinimalAndFailClosed(t *testing.T) {
	server, _ := newTestServer()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/oidc/status", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"enabled":false}` || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/oidc/login", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled login status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCCallbackCreatesExistingSessionAndPreservesRBACLogout(t *testing.T) {
	server, provider := configuredOIDCTestServer(t)
	flowCookie, location := beginOIDCTestFlow(t, server, "/runs?q=deploy")
	if !strings.HasPrefix(location, provider.issuer+"/authorize?") || !strings.Contains(location, url.QueryEscape(provider.state)) {
		t.Fatalf("provider redirect=%q", location)
	}

	wrong := *flowCookie
	wrong.Value = "wrong-verifier"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=code&state="+url.QueryEscape(provider.state), nil)
	req.AddCookie(&wrong)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || rec.Header().Values("Set-Cookie") != nil {
		t.Fatalf("mismatched browser status=%d cookies=%v", rec.Code, rec.Header().Values("Set-Cookie"))
	}

	callback := "/api/v1/oidc/callback?code=code&state=" + url.QueryEscape(provider.state) + "&iss=" + url.QueryEscape(provider.issuer) + "&session_state=ignored"
	req = httptest.NewRequest(http.MethodGet, callback, nil)
	req.AddCookie(flowCookie)
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/runs?q=deploy" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback status=%d location=%q headers=%v body=%s", rec.Code, rec.Header().Get("Location"), rec.Header(), rec.Body.String())
	}
	var sessionCookie *http.Cookie
	clearedFlow := false
	for _, cookie := range rec.Result().Cookies() {
		switch cookie.Name {
		case oidcFlowCookie:
			clearedFlow = cookie.MaxAge < 0
		case "nerocd_session":
			sessionCookie = cookie
		}
	}
	if !clearedFlow || sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Path != "/api/v1" {
		t.Fatalf("callback cookies=%v", rec.Result().Cookies())
	}

	for _, path := range []string{"/api/v1/me", "/api/v1/projects"} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(sessionCookie)
		rec = httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sessions", nil)
	req.Header.Set("X-NeroCD-CSRF", "1")
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCCallbackProviderDenialAndIssuerValidationAreBound(t *testing.T) {
	server, provider := configuredOIDCTestServer(t)
	flowCookie, _ := beginOIDCTestFlow(t, server, "/")
	wrongIssuer := "/api/v1/oidc/callback?error=access_denied&state=" + url.QueryEscape(provider.state) + "&iss=" + url.QueryEscape("https://other.example")
	req := httptest.NewRequest(http.MethodGet, wrongIssuer, nil)
	req.AddCookie(flowCookie)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("issuer mismatch consumed flow status=%d cookies=%v", rec.Code, rec.Header().Values("Set-Cookie"))
	}
	spaceIssuer := "/api/v1/oidc/callback?error=access_denied&state=" + url.QueryEscape(provider.state) + "&iss=" + url.QueryEscape(" "+provider.issuer)
	req = httptest.NewRequest(http.MethodGet, spaceIssuer, nil)
	req.AddCookie(flowCookie)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("issuer whitespace was normalized status=%d cookies=%v", rec.Code, rec.Header().Values("Set-Cookie"))
	}

	denial := "/api/v1/oidc/callback?error=access_denied&error_description=" + url.QueryEscape("sensitive provider detail") + "&state=" + url.QueryEscape(provider.state) + "&iss=" + url.QueryEscape(provider.issuer) + "&session_state=ignored"
	req = httptest.NewRequest(http.MethodGet, denial, nil)
	req.AddCookie(flowCookie)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sign-in?oidc_error=failed" || strings.Contains(rec.Body.String(), "sensitive provider detail") {
		t.Fatalf("provider denial status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=code&error=access_denied&state="+url.QueryEscape(provider.state), nil)
	req.AddCookie(flowCookie)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous code/error accepted: %d", rec.Code)
	}
}

func TestOIDCProviderFailureUsesOnlyGenericBoundError(t *testing.T) {
	server, provider := configuredOIDCTestServer(t)
	provider.exchangeErr = errors.New("provider returned sensitive diagnostic")
	flowCookie, _ := beginOIDCTestFlow(t, server, "/")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/callback?code=sensitive-code&state="+url.QueryEscape(provider.state), nil)
	req.AddCookie(flowCookie)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	raw, _ := json.Marshal(rec.Header())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sign-in?oidc_error=failed" || strings.Contains(string(raw)+rec.Body.String(), "sensitive") {
		t.Fatalf("provider failure leaked detail status=%d headers=%s body=%s", rec.Code, raw, rec.Body.String())
	}
}

func TestOIDCDiscoveryOutageReturnsBrowserToLocalRecovery(t *testing.T) {
	server, provider := configuredOIDCTestServer(t)
	provider.authorizationErr = errors.New("sensitive provider network diagnostic")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/oidc/login?redirect=%2Fruns", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	raw, _ := json.Marshal(rec.Header())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sign-in?oidc_error=failed" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" || strings.Contains(string(raw)+rec.Body.String(), "sensitive") {
		t.Fatalf("browser outage status=%d headers=%s body=%s", rec.Code, raw, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/oidc/login?redirect=%2Fruns", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"oidc_unavailable"`) || strings.Contains(rec.Body.String(), "sensitive") {
		t.Fatalf("API outage status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOIDCRedirectValidation(t *testing.T) {
	for _, valid := range []string{"/", "/runs", "/runs/run_1", "/deployments/deploy.1?q=ready"} {
		if got, ok := validOIDCRedirect(valid); !ok || got != valid {
			t.Fatalf("valid redirect %q rejected", valid)
		}
	}
	for _, invalid := range []string{"https://evil.example", "//evil.example", "/api/v1/me", "/sign-in", "/runs/%2f%2fevil", "/runs\\evil", "/runs#fragment", "/runs?other=value"} {
		if _, ok := validOIDCRedirect(invalid); ok {
			t.Fatalf("unsafe redirect %q accepted", invalid)
		}
	}
}
