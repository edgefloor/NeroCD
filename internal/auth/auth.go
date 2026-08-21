package auth

import (
	"context"
	"errors"
)

var (
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrForbidden          = errors.New("forbidden")
	ErrRateLimited        = errors.New("rate limited")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

type Principal struct {
	ID       string   `json:"id"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Roles    []string `json:"roles"`
	Provider string   `json:"provider"`
}

type Provider interface {
	CurrentPrincipal(context.Context) (Principal, error)
}

type principalContextKey struct{}

type credentialSourceContextKey struct{}

type CredentialSource string

const (
	CredentialSourceBearer CredentialSource = "bearer"
	CredentialSourceCookie CredentialSource = "cookie"
)

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// WithCredentialSource records the transport that authenticated the request.
// Authorization policy can use it without coupling to HTTP details.
func WithCredentialSource(ctx context.Context, source CredentialSource) context.Context {
	return context.WithValue(ctx, credentialSourceContextKey{}, source)
}

func CredentialSourceFromContext(ctx context.Context) CredentialSource {
	source, _ := ctx.Value(credentialSourceContextKey{}).(CredentialSource)
	return source
}

type ContextProvider struct{}

func (ContextProvider) CurrentPrincipal(ctx context.Context) (Principal, error) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}
