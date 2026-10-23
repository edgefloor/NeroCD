package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
)

type recoveryServiceRepository struct {
	*proxyViewRepository
	calls int
}

func (r *recoveryServiceRepository) GetHostingProxy(context.Context, string) (domain.HostingProxy, error) {
	return domain.HostingProxy{ID: "proxy_recovery", ControlProjectID: "proj_platform"}, nil
}
func (r *recoveryServiceRepository) HostingProxyRecovery(context.Context, string, string) (domain.HostingProxyRecoveryStatus, error) {
	r.calls++
	return domain.HostingProxyRecoveryStatus{Eligible: true}, nil
}
func (r *recoveryServiceRepository) PlanHostingProxyRecovery(context.Context, string, string, string, domain.HostingProxyRecoveryInput, domain.AuditEvent) (domain.HostingProxyRecoveryPlan, error) {
	r.calls++
	return domain.HostingProxyRecoveryPlan{}, nil
}
func (r *recoveryServiceRepository) ApplyHostingProxyRecovery(context.Context, string, string, string, string, domain.HostingProxyRecoveryApplyInput, domain.AuditEvent) (domain.HostingProxyRecoveryResolution, error) {
	r.calls++
	return domain.HostingProxyRecoveryResolution{}, nil
}

func TestHostingProxyRecoveryServiceAuthorityAndInput(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	r := &recoveryServiceRepository{proxyViewRepository: &proxyViewRepository{RunRepository: s.runs, op: domain.HostingProxyOperation{ID: "pop_recovery", ProxyID: "proxy_recovery"}}}
	s.runs = r
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err := s.HostingProxyRecovery(viewer, "pop_recovery"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer %v", err)
	}
	if r.calls != 0 {
		t.Fatal("viewer reached recovery repository")
	}
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if _, err := s.HostingProxyRecovery(admin, "pop_recovery"); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingProxyRecoveryInput{IdempotencyKey: "review", ExpectedLoadedGeneration: 1, ExpectedDesiredGeneration: 2, ExpectedCompleteReceiptID: "receipt_recovery", ExpectedCompleteReceiptHash: "sha256:" + strings.Repeat("a", 64)}
	if _, err := s.PlanHostingProxyRecovery(admin, "pop_recovery", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyHostingProxyRecovery(admin, "pop_recovery", "prp_recovery", domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "apply", PlanHash: in.ExpectedCompleteReceiptHash}); err != nil {
		t.Fatal(err)
	}
	before := r.calls
	in.ExpectedCompleteReceiptHash = ""
	if _, err := s.PlanHostingProxyRecovery(admin, "pop_recovery", in); err == nil {
		t.Fatal("missing receipt accepted")
	}
	if _, err := s.ApplyHostingProxyRecovery(admin, "pop_recovery", "prp_recovery", domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "apply"}); err == nil {
		t.Fatal("unlock without hash accepted")
	}
	if before != r.calls {
		t.Fatal("invalid input reached transaction")
	}
}
