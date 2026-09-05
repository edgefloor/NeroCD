package auth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	defaultOIDCTimeout = 10 * time.Second
	maxOIDCResponse    = 1 << 20
)

// OIDCIdentity is the minimal verified identity assertion used by the app.
type OIDCIdentity struct {
	Issuer  string
	Subject string
	Nonce   string
}

// OIDCProvider is the narrow protocol seam consumed by the application layer.
type OIDCProvider interface {
	Issuer() string
	ClientID() string
	AuthorizationURL(context.Context, string, string, string) (string, error)
	Exchange(context.Context, string, string) (OIDCIdentity, error)
}

// OIDCConfig contains fixed operator configuration. RedirectURL must be the
// server-derived callback and AllowLoopbackHTTP is development-fixture only.
type OIDCConfig struct {
	IssuerURL         string
	ClientID          string
	ClientSecret      string
	RedirectURL       string
	AllowLoopbackHTTP bool
	Timeout           time.Duration
}

// OIDCClient performs lazy discovery and verification so a transient IdP
// outage affects only SSO requests and never disables local recovery login.
type OIDCClient struct {
	issuer       string
	clientID     string
	clientSecret string
	redirectURL  string
	timeout      time.Duration
	httpClient   *http.Client
}

// NewOIDCClient validates fixed local configuration without contacting the IdP.
func NewOIDCClient(cfg OIDCConfig) (*OIDCClient, error) {
	issuer, err := ValidateOIDCIssuerURL(cfg.IssuerURL, cfg.AllowLoopbackHTTP)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(cfg.ClientID)
	clientSecret := strings.TrimSpace(cfg.ClientSecret)
	redirectURL := strings.TrimSpace(cfg.RedirectURL)
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		return nil, errors.New("oidc configuration is incomplete")
	}
	if err := validateOIDCURL(redirectURL, cfg.AllowLoopbackHTTP, true); err != nil {
		return nil, errors.New("oidc redirect URL is invalid")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultOIDCTimeout
	}
	if timeout < time.Second || timeout > 30*time.Second {
		return nil, errors.New("oidc timeout must be between 1s and 30s")
	}
	transport := boundedOIDCTransport{base: http.DefaultTransport, maxBytes: maxOIDCResponse}
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("oidc redirect limit exceeded")
			}
			if err := validateOIDCURL(req.URL.String(), cfg.AllowLoopbackHTTP, true); err != nil {
				return errors.New("oidc redirect is invalid")
			}
			return nil
		},
	}
	return &OIDCClient{issuer: issuer, clientID: clientID, clientSecret: clientSecret, redirectURL: redirectURL, timeout: timeout, httpClient: client}, nil
}

// ValidateOIDCIssuerURL validates and returns the exact trimmed issuer value.
func ValidateOIDCIssuerURL(value string, allowLoopbackHTTP bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("oidc issuer URL is invalid")
	}
	if err := validateOIDCURL(value, allowLoopbackHTTP, false); err != nil {
		return "", errors.New("oidc issuer URL is invalid")
	}
	return value, nil
}

// Issuer returns the exact configured issuer identifier.
func (c *OIDCClient) Issuer() string { return c.issuer }

// ClientID returns the configured public client identifier.
func (c *OIDCClient) ClientID() string { return c.clientID }

// String deliberately redacts all configured provider material.
func (c *OIDCClient) String() string { return "OIDCClient(configured)" }

// GoString deliberately redacts all configured provider material.
func (c *OIDCClient) GoString() string { return "OIDCClient(configured)" }

// AuthorizationURL discovers the provider under a bounded request and creates
// an authorization-code URL with nonce and S256 PKCE bindings.
func (c *OIDCClient) AuthorizationURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	provider, oauthConfig, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	_ = provider
	return oauthConfig.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange redeems one authorization code and verifies the signed ID token.
// Access and refresh tokens remain local to this call and are discarded.
func (c *OIDCClient) Exchange(ctx context.Context, code, verifier string) (OIDCIdentity, error) {
	provider, oauthConfig, err := c.discover(ctx)
	if err != nil {
		return OIDCIdentity{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestCtx = oidc.ClientContext(requestCtx, c.httpClient)
	token, err := oauthConfig.Exchange(requestCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return OIDCIdentity{}, errors.New("oidc code exchange failed")
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		return OIDCIdentity{}, errors.New("oidc response omitted id token")
	}
	verified, err := provider.Verifier(&oidc.Config{ClientID: c.clientID}).Verify(requestCtx, rawIDToken)
	if err != nil {
		return OIDCIdentity{}, errors.New("oidc id token verification failed")
	}
	if strings.TrimSpace(verified.Subject) == "" {
		return OIDCIdentity{}, errors.New("oidc id token omitted subject")
	}
	return OIDCIdentity{Issuer: verified.Issuer, Subject: verified.Subject, Nonce: verified.Nonce}, nil
}

func (c *OIDCClient) discover(ctx context.Context) (*oidc.Provider, oauth2.Config, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestCtx = oidc.ClientContext(requestCtx, c.httpClient)
	provider, err := oidc.NewProvider(requestCtx, c.issuer)
	if err != nil {
		return nil, oauth2.Config{}, errors.New("oidc discovery failed")
	}
	var metadata struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, oauth2.Config{}, errors.New("oidc discovery metadata is invalid")
	}
	for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.JWKSURI} {
		if err := validateOIDCURL(endpoint, strings.HasPrefix(c.issuer, "http://"), true); err != nil {
			return nil, oauth2.Config{}, errors.New("oidc discovery endpoint is invalid")
		}
	}
	return provider, oauth2.Config{
		ClientID:     c.clientID,
		ClientSecret: c.clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  c.redirectURL,
		Scopes:       []string{oidc.ScopeOpenID},
	}, nil
}

func validateOIDCURL(value string, allowLoopbackHTTP, allowPath bool) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid URL")
	}
	if !allowPath && parsed.RawQuery != "" {
		return errors.New("invalid issuer URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" || !allowLoopbackHTTP || !loopbackHost(parsed.Hostname()) {
		return errors.New("HTTPS is required")
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

type boundedOIDCTransport struct {
	base     http.RoundTripper
	maxBytes int64
}

func (t boundedOIDCTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	response.Body = &boundedReadCloser{Reader: io.LimitReader(response.Body, t.maxBytes), Closer: response.Body}
	return response, nil
}

type boundedReadCloser struct {
	io.Reader
	io.Closer
}

var _ OIDCProvider = (*OIDCClient)(nil)
var _ http.RoundTripper = boundedOIDCTransport{}
