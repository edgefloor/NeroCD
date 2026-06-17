package runner

import (
	"testing"

	"nerocd/internal/domain"
)

func TestExecuteCheckoutRejectsUnsafeDestPath(t *testing.T) {
	_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: "https://github.com/example/repo.git"}, DestPath: "../repo"}, t.TempDir(), func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsafe dest_path error")
	}
}

func TestExecuteCheckoutRejectsUnsafeRepositoryURL(t *testing.T) {
	tests := []string{
		t.TempDir(),
		"file:///tmp/repo",
		"https://127.0.0.1/repo.git",
	}
	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			_, err := ExecuteCheckout(t.Context(), domain.CheckoutPlan{Repository: domain.RepositoryRef{URL: test}, DestPath: "repo"}, t.TempDir(), func(ProcessEvent) {})
			if err == nil {
				t.Fatal("expected unsafe repository url error")
			}
		})
	}
}
