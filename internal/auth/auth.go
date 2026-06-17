package auth

import (
	"context"
	"errors"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
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

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

type ContextProvider struct{}

func (ContextProvider) CurrentPrincipal(ctx context.Context) (Principal, error) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}
