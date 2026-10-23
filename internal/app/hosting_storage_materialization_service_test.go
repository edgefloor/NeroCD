package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

type storageMaterializationServiceRepository struct {
	store.RunRepository
	runner string
	create domain.HostingStorageCreateRequest
	proof  domain.HostingStorageMaterializedRequest
	err    error
	calls  int
}

func (r *storageMaterializationServiceRepository) HostingStorageCreateBegin(_ context.Context, id string, in domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	r.runner, r.create, r.calls = id, in, r.calls+1
	return domain.HostingStorageCreateAck{}, r.err
}
func (r *storageMaterializationServiceRepository) HostingStorageMaterialized(_ context.Context, id string, in domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	r.runner, r.proof, r.calls = id, in, r.calls+1
	return domain.HostingStorageMaterializedAck{}, r.err
}

func TestStorageMaterializationServiceCurrentRunnerAndExactAuthority(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	r, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_storage", Name: "storage", Capabilities: []string{domain.RunTypeImageDeploy}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateRunnerToken(t.Context(), r.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), p)
	a := domain.HostingAuthority{OperationID: "operation", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	id, _ := domain.HostingStorageIdentity("binding")
	in := domain.HostingStorageCreateRequest{HostingAuthority: a, StorageRef: domain.HostingStorageRef{StorageID: id, Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}, PlanHash: "sha256:" + strings.Repeat("b", 64), CreationNonce: strings.Repeat("c", 32)}
	if _, err = s.RunnerHostingStorageCreateBegin(ctx, in); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("memory backend manufactured storage authority", err)
	}
	repo := &storageMaterializationServiceRepository{RunRepository: s.runs}
	s.runs = repo
	for _, invalid := range []context.Context{admin, auth.WithPrincipal(t.Context(), auth.Principal{ID: "absent", Provider: domain.PrincipalRunner})} {
		if _, err = s.RunnerHostingStorageCreateBegin(invalid, in); err == nil {
			t.Fatal("non-current runner reached storage repository")
		}
	}
	if repo.calls != 0 {
		t.Fatal("unauthorized request reached repository")
	}
	if _, err = s.RunnerHostingStorageCreateBegin(ctx, in); err != nil || repo.runner != r.Runner.ID || repo.create != in {
		t.Fatal("runner or exact tuple changed", err)
	}
	repo.err = store.ErrConflict
	if _, err = s.RunnerHostingStorageCreateBegin(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale authority conflict was hidden", err)
	}
	in.Attempt = 0
	if _, err = s.RunnerHostingStorageCreateBegin(ctx, in); err == nil || repo.calls != 2 {
		t.Fatal("invalid tuple reached repository", err)
	}
	in.Attempt = 1
	_, volume := domain.HostingStorageIdentity("binding")
	labels := domain.ExpectedHostingStorageLabels(domain.HostingStorage{ID: id, Hash: in.StorageRef.Hash, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination"}, domain.HostingStorageMaterialization{CreationOperationID: a.OperationID, CreationPlanHash: in.PlanHash, CreationNonce: in.CreationNonce, DaemonID: "daemon"})
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "receipt", PlanHash: in.PlanHash, StorageRef: in.StorageRef, CreationOperationID: a.OperationID, CreationPlanHash: in.PlanHash, CreationNonce: in.CreationNonce, DaemonID: "daemon", VolumeName: volume, Driver: "local", Scope: "local", Labels: labels}
	if _, err = s.RunnerHostingStorageMaterialized(ctx, proof); !errors.Is(err, store.ErrConflict) || repo.runner != r.Runner.ID || domain.HostingDocumentHash(repo.proof) != domain.HostingDocumentHash(proof) {
		t.Fatal("materialization authority or conflict changed", err)
	}
	if _, err = mem.UpdateRunnerToken(t.Context(), r.Runner.ID, "", domain.RunnerRevoked, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunnerHostingStorageMaterialized(ctx, proof); !errors.Is(err, auth.ErrUnauthenticated) || repo.calls != 3 {
		t.Fatal("revoked persisted identity reached repository", err)
	}
}
