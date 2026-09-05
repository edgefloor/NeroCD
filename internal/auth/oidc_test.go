package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

type oidcTestProvider struct {
	server      *httptest.Server
	signer      jose.Signer
	otherSigner jose.Signer
	publicKey   jose.JSONWebKey
}

func newOIDCTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newSigner := func(key *rsa.PrivateKey, kid string) jose.Signer {
		t.Helper()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
		if err != nil {
			t.Fatal(err)
		}
		return signer
	}
	fixture := &oidcTestProvider{signer: newSigner(key, "fixture-key"), otherSigner: newSigner(other, "other-key"), publicKey: jose.JSONWebKey{Key: &key.PublicKey, KeyID: "fixture-key", Algorithm: string(jose.RS256), Use: "sig"}}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (p *oidcTestProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeOIDCTestJSON(w, map[string]any{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/jwks":
		writeOIDCTestJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{p.publicKey}})
	case "/token":
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") != "fixture-verifier" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		code := r.Form.Get("code")
		if code == "no-id-token" {
			writeOIDCTestJSON(w, map[string]any{"access_token": "discarded", "token_type": "Bearer"})
			return
		}
		now := time.Now().UTC()
		claims := map[string]any{"iss": p.server.URL, "sub": "subject-1", "aud": "client-1", "exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1"}
		signer := p.signer
		switch code {
		case "bad-signature":
			signer = p.otherSigner
		case "bad-issuer":
			claims["iss"] = "https://other.example.invalid"
		case "bad-audience":
			claims["aud"] = "other-client"
		case "expired":
			claims["exp"] = now.Add(-time.Minute).Unix()
		case "empty-subject":
			claims["sub"] = ""
		}
		raw, err := jwt.Signed(signer).Claims(claims).Serialize()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeOIDCTestJSON(w, map[string]any{"access_token": "discarded", "token_type": "Bearer", "expires_in": 60, "id_token": raw})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeOIDCTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestOIDCClientAuthorizationAndVerifiedExchange(t *testing.T) {
	fixture := newOIDCTestProvider(t)
	client, err := NewOIDCClient(OIDCConfig{IssuerURL: fixture.server.URL, ClientID: "client-1", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8080/api/v1/oidc/callback", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := client.AuthorizationURL(t.Context(), "state-1", "nonce-1", "fixture-verifier")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/authorize" || parsed.Query().Get("state") != "state-1" || parsed.Query().Get("nonce") != "nonce-1" || parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("code_challenge") != oauth2.S256ChallengeFromVerifier("fixture-verifier") {
		t.Fatalf("authorization URL missing required bindings: %s", authorizationURL)
	}
	identity, err := client.Exchange(t.Context(), "good", "fixture-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != fixture.server.URL || identity.Subject != "subject-1" || identity.Nonce != "nonce-1" {
		t.Fatalf("identity=%#v", identity)
	}
}

func TestOIDCClientRejectsTokenAndPKCEFailures(t *testing.T) {
	fixture := newOIDCTestProvider(t)
	client, err := NewOIDCClient(OIDCConfig{IssuerURL: fixture.server.URL, ClientID: "client-1", ClientSecret: "secret", RedirectURL: "http://localhost:8080/api/v1/oidc/callback", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"bad-signature", "bad-issuer", "bad-audience", "expired", "empty-subject", "no-id-token"} {
		t.Run(code, func(t *testing.T) {
			if _, err := client.Exchange(t.Context(), code, "fixture-verifier"); err == nil {
				t.Fatal("accepted invalid provider response")
			}
		})
	}
	if _, err := client.Exchange(t.Context(), "good", "wrong-verifier"); err == nil {
		t.Fatal("accepted invalid PKCE verifier")
	}
}

func TestOIDCConfigurationAndEndpointTrustFailClosed(t *testing.T) {
	for _, issuer := range []string{"http://idp.example.invalid", "https://user@idp.example.invalid", "https://idp.example.invalid#fragment"} {
		if _, err := ValidateOIDCIssuerURL(issuer, true); err == nil {
			t.Fatalf("accepted unsafe issuer %q", issuer)
		}
	}
	if _, err := ValidateOIDCIssuerURL("https://idp.example.invalid/realms/ops", false); err != nil {
		t.Fatalf("rejected HTTPS issuer: %v", err)
	}
	if value, err := ValidateOIDCIssuerURL("https://tenant.example.invalid/", false); err != nil || value != "https://tenant.example.invalid/" {
		t.Fatalf("trailing-slash issuer was not preserved: value=%q err=%v", value, err)
	}
	client, err := NewOIDCClient(OIDCConfig{IssuerURL: "http://127.0.0.1:8081", ClientID: "client", ClientSecret: "secret", RedirectURL: "http://localhost:8080/api/v1/oidc/callback", AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://example.invalid/discovery", "https://user@example.invalid/discovery", "https://example.invalid/discovery#fragment"} {
		parsed, parseErr := url.Parse(target)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		req := &http.Request{Method: http.MethodGet, URL: parsed}
		if err := client.httpClient.CheckRedirect(req, nil); err == nil {
			t.Fatalf("accepted unsafe redirect %q", target)
		}
	}
}

func TestOIDCProviderOutageIsSanitized(t *testing.T) {
	client, err := NewOIDCClient(OIDCConfig{IssuerURL: "http://127.0.0.1:1", ClientID: "client", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8080/api/v1/oidc/callback", AllowLoopbackHTTP: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.AuthorizationURL(t.Context(), "state", "nonce", "verifier")
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("provider outage error was not sanitized: %v", err)
	}
}
