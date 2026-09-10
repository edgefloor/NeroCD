package app

import (
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

func TestHelperValidationErrorsAreTypedForDirectAndCallerPaths(t *testing.T) {
	service := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	tests := []struct {
		name string
		want string
		call func() error
	}{
		{
			name: "require project role",
			want: "project_id is required",
			call: func() error {
				return service.requireProjectRole(t.Context(), testPrincipalContext(t), " ", domain.RoleViewer)
			},
		},
		{
			name: "archive project",
			want: "project_id is required",
			call: func() error {
				_, err := service.ArchiveProject(admin, " ")
				return err
			},
		},
		{
			name: "list services",
			want: "project_id is required",
			call: func() error {
				_, err := service.ListServices(admin, " ")
				return err
			},
		},
		{
			name: "run by ID",
			want: "run_id is required",
			call: func() error {
				_, err := service.runByID(t.Context(), " ")
				return err
			},
		},
		{
			name: "approve run",
			want: "run_id is required",
			call: func() error {
				_, err := service.ApproveRun(admin, " ")
				return err
			},
		},
		{
			name: "reject run",
			want: "run_id is required",
			call: func() error {
				_, err := service.RejectRun(admin, " ")
				return err
			},
		},
		{
			name: "cancel run",
			want: "run_id is required",
			call: func() error {
				_, err := service.CancelRun(admin, " ")
				return err
			},
		},
		{
			name: "runner primitive plan",
			want: "run_id is required",
			call: func() error {
				_, err := service.RunnerPrimitivePlan(admin, " ")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
			}
			if got := err.Error(); got != test.want {
				t.Errorf("error text = %q, want %q", got, test.want)
			}
		})
	}
}
