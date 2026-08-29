package auth

import (
	"context"
	"errors"
)

var (
	// ErrUnauthenticated indicates that no acceptable authentication was supplied.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden indicates that the authenticated principal lacks permission.
	ErrForbidden = errors.New("forbidden")
	// ErrRateLimited indicates too many recent authentication attempts.
	ErrRateLimited = errors.New("rate limited")
	// ErrInvalidCredentials indicates rejected authentication credentials.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Principal is the authenticated identity and authorization context.
type Principal struct {
	ID       string   `json:"id"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
	Provider string   `json:"provider"`
}

// Provider retrieves the current request principal.
type Provider interface {
	CurrentPrincipal(context.Context) (Principal, error)
}

type principalContextKey struct{}

type credentialSourceContextKey struct{}

// CredentialSource identifies how a principal authenticated.
type CredentialSource string

const (
	// CredentialSourceBearer identifies bearer-token authentication.
	CredentialSourceBearer CredentialSource = "bearer"
	// CredentialSourceCookie identifies session-cookie authentication.
	CredentialSourceCookie CredentialSource = "cookie"
)

// WithPrincipal returns ctx carrying principal authentication context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// WithCredentialSource records the transport that authenticated the request.
// Authorization policy can use it without coupling to HTTP details.
func WithCredentialSource(ctx context.Context, source CredentialSource) context.Context {
	return context.WithValue(ctx, credentialSourceContextKey{}, source)
}

// CredentialSourceFromContext returns the credential source carried by ctx.
func CredentialSourceFromContext(ctx context.Context) CredentialSource {
	source, _ := ctx.Value(credentialSourceContextKey{}).(CredentialSource)
	return source
}

// ContextProvider retrieves principals placed in a context.
type ContextProvider struct{}

// CurrentPrincipal returns the principal carried by ctx.
func (ContextProvider) CurrentPrincipal(ctx context.Context) (Principal, error) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}
