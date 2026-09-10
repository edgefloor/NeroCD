package app

import (
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestOffboardUserValidationPreservesAuthorizationPrecedence(t *testing.T) {
	service := newTestService()

	if _, err := service.OffboardUser(t.Context(), OffboardUserInput{}); !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unauthenticated OffboardUser error = %v", err)
	}

	viewer := auth.WithPrincipal(t.Context(), auth.Principal{
		ID:       "usr_viewer",
		Roles:    []string{domain.RoleViewer},
		Provider: domain.PrincipalLocal,
	})
	if _, err := service.OffboardUser(viewer, OffboardUserInput{}); !errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("forbidden OffboardUser error = %v", err)
	}

	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if _, err := service.OffboardUser(admin, OffboardUserInput{UserID: "  "}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("administrator OffboardUser error = %v, want ErrInvalidInput", err)
	}
}
