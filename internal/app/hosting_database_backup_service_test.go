package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// Exercise Service.ClaimRun, rather than the store or registry directly. The
// repository owns full persisted plan validation; this seam checks that service
// dispatch preserves its exact current authority call and response identity.
type databaseBackupClaimRepository struct {
	store.RunnerRepository
	store.HostingRepository
	store.HostingDatabaseBackupRepository
	claim                 domain.ClaimedRun
	plan                  domain.HostingDatabaseBackupPlan
	want                  domain.HostingAuthority
	claimCalls, planCalls int
	planError             error
}

func (r *databaseBackupClaimRepository) ClaimRun(ctx context.Context, runner string, _ time.Time, _ time.Duration, _ ...store.MutationOption) (domain.ClaimedRun, error) {
	r.claimCalls++
	if err := ctx.Err(); err != nil {
		return domain.ClaimedRun{}, err
	}
	if runner != "backup_runner" {
		return domain.ClaimedRun{}, store.ErrConflict
	}
	return r.claim, nil
}

func (r *databaseBackupClaimRepository) DatabaseBackupPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error) {
	r.planCalls++
	if err := ctx.Err(); err != nil {
		return domain.HostingDatabaseBackupPlan{}, err
	}
	if runner != "backup_runner" || a != r.want {
		return domain.HostingDatabaseBackupPlan{}, store.ErrConflict
	}
	return r.plan, r.planError
}

func databaseBackupClaimService(t *testing.T, mode string) (*Service, *databaseBackupClaimRepository, context.Context) {
	t.Helper()
	s := newTestService()
	a := domain.HostingAuthority{OperationID: "backup_operation", RunID: "db_backup_backup_operation", LeaseID: "backup_lease", Attempt: 2, Fence: "backup_fence"}
	r := &databaseBackupClaimRepository{
		RunnerRepository: s.runners, HostingRepository: s.hosting, want: a,
		claim: domain.ClaimedRun{
			Run:   domain.TaskRun{ID: a.RunID, ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackup, HostingOperationID: a.OperationID, HostingMode: mode, Inputs: map[string]any{}}},
			Lease: domain.RunLease{ID: a.LeaseID, RunID: a.RunID, RunnerID: "backup_runner", Attempt: a.Attempt, Fence: a.Fence, Status: "active", ExpiresAt: time.Now().Add(time.Minute)},
		},
		plan: domain.HostingDatabaseBackupPlan{Operation: domain.HostingDatabaseBackupOperation{ID: a.OperationID, Mode: mode}, RunnerID: "backup_runner"},
	}
	s.runners, s.hosting = r, r
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "backup_runner", Provider: domain.PrincipalRunner})
	return s, r, ctx
}

func TestDatabaseBackupServiceClaimDispatch(t *testing.T) {
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			s, r, ctx := databaseBackupClaimService(t, mode)
			// No repository-provided generic primitive may survive this family.
			r.claim.PrimitivePlan = domain.RunnerPrimitivePlan{RunID: "foreign", Process: &domain.ProcessSpec{}}
			got, err := s.ClaimRun(ctx)
			if err != nil {
				t.Fatalf("typed %s claim rejected before execution: %v", mode, err)
			}
			if r.claimCalls != 1 || r.planCalls != 1 || !reflect.DeepEqual(got.Run, r.claim.Run) || got.Lease != r.claim.Lease || !reflect.DeepEqual(got.PrimitivePlan, domain.RunnerPrimitivePlan{RunID: r.claim.Run.ID}) {
				t.Fatal("typed claim lost its exact task/lease or acquired generic execution")
			}
		})
	}
}

func TestDatabaseBackupServiceClaimRejectsChangedIdentity(t *testing.T) {
	for name, change := range map[string]func(*databaseBackupClaimRepository){
		"generic family with hosting identity": func(r *databaseBackupClaimRepository) { r.claim.Run.RunSpec.Type = domain.RunTypeShell },
		"task run":                             func(r *databaseBackupClaimRepository) { r.claim.Run.ID = "foreign" },
		"invalid mode":                         func(r *databaseBackupClaimRepository) { r.claim.Run.RunSpec.HostingMode = "execute" },
		"task process":                         func(r *databaseBackupClaimRepository) { r.claim.Run.RunSpec.Process = &domain.ProcessSpec{} },
		"operation": func(r *databaseBackupClaimRepository) {
			r.claim.Run.RunSpec.HostingOperationID = "foreign"
			r.claim.Run.ID = "db_backup_foreign"
			r.claim.Lease.RunID = r.claim.Run.ID
		},
		"lease run":      func(r *databaseBackupClaimRepository) { r.claim.Lease.RunID = "foreign" },
		"lease runner":   func(r *databaseBackupClaimRepository) { r.claim.Lease.RunnerID = "foreign" },
		"lease ID":       func(r *databaseBackupClaimRepository) { r.claim.Lease.ID = "foreign" },
		"attempt":        func(r *databaseBackupClaimRepository) { r.claim.Lease.Attempt++ },
		"fence":          func(r *databaseBackupClaimRepository) { r.claim.Lease.Fence = "foreign" },
		"plan operation": func(r *databaseBackupClaimRepository) { r.plan.Operation.ID = "foreign" },
		"plan mode":      func(r *databaseBackupClaimRepository) { r.plan.Operation.Mode = "restore" },
		"plan runner":    func(r *databaseBackupClaimRepository) { r.plan.RunnerID = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			s, r, ctx := databaseBackupClaimService(t, "backup")
			change(r)
			got, err := s.ClaimRun(ctx)
			if !errors.Is(err, store.ErrConflict) || !reflect.DeepEqual(got, domain.ClaimedRun{}) {
				t.Fatalf("changed claim acquired executable response: err=%v", err)
			}
		})
	}
}

func TestDatabaseBackupServiceClaimKeepsPlanAuthorityDenials(t *testing.T) {
	for _, mode := range []string{"backup", "restore"} {
		t.Run(mode, func(t *testing.T) {
			for _, denied := range []error{store.ErrConflict, store.ErrHostingDatabaseUnavailable, auth.ErrForbidden} {
				s, r, ctx := databaseBackupClaimService(t, mode)
				r.planError = denied
				got, err := s.ClaimRun(ctx)
				if !errors.Is(err, denied) || !reflect.DeepEqual(got, domain.ClaimedRun{}) || r.planCalls != 1 {
					t.Fatal("current plan authority error was bypassed or changed")
				}
			}
		})
	}
	s, r, ctx := databaseBackupClaimService(t, "backup")
	for _, caller := range []context.Context{t.Context(), auth.WithPrincipal(t.Context(), testPrincipalContext(t))} {
		if got, err := s.ClaimRun(caller); err == nil || !reflect.DeepEqual(got, domain.ClaimedRun{}) {
			t.Fatal("non-runner acquired a typed task")
		}
	}
	if r.claimCalls != 0 || r.planCalls != 0 {
		t.Fatal("non-runner reached claim authority")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ClaimRun(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was replaced: %v", err)
	}
	s.hosting = r.HostingRepository
	if got, err := s.ClaimRun(ctx); !errors.Is(err, store.ErrHostingDatabaseUnavailable) || !reflect.DeepEqual(got, domain.ClaimedRun{}) || r.planCalls != 0 {
		t.Fatal("backend without backup authority manufactured an executable claim")
	}
}
