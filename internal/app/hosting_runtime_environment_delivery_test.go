package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type runtimeIdentity struct {
	current RunnerIdentity
	calls   int
	after   func()
	token   string
	afterAt int
}

func (r *runtimeIdentity) AuthenticateRunnerToken(_ context.Context, token string) (auth.Principal, error) {
	if token == "" || token != r.token {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{ID: r.current.RunnerID, Provider: domain.PrincipalRunner}, nil
}

func (r *runtimeIdentity) CurrentRunnerIdentity(context.Context) (RunnerIdentity, error) {
	r.calls++
	afterAt := r.afterAt
	if afterAt == 0 {
		afterAt = 2
	}
	if r.calls == afterAt && r.after != nil {
		r.after()
	}
	return r.current, nil
}

func TestRuntimeEnvironmentDeliveryOriginalBearerRequired(t *testing.T) {
	for _, name := range []string{"missing", "incorrect", "rotation_before_decrypt", "rotation_before_return", "revocation_before_return"} {
		t.Run(name, func(t *testing.T) {
			s, repo, identity, ctx, in := runtimeAppFixture(t)
			token := "runtime-delivery-bearer"
			switch name {
			case "missing":
				token = ""
			case "incorrect":
				token = "incorrect-bearer"
			case "rotation_before_decrypt":
				identity.after = func() { identity.token = "replacement" }
			case "rotation_before_return":
				identity.afterAt = 3
				identity.after = func() { identity.token = "replacement" }
			case "revocation_before_return":
				identity.afterAt = 3
				identity.after = func() { identity.token = "" }
			}
			out, err := s.Deliver(ctx, in, token)
			if out != nil {
				out.Close()
			}
			if err != ErrHostingRuntimeEnvironmentUnavailable || out != nil {
				t.Fatal("original bearer no longer authorizes delivery", err)
			}
			if (name == "missing" || name == "incorrect") && repo.calls != 0 {
				t.Fatal("invalid bearer reached repository")
			}
			if strings.Contains(name, "before_return") && identity.calls != 3 {
				t.Fatal("final credential boundary not reached")
			}
		})
	}
}

type runtimeDeliveryRepository struct {
	value       store.HostingRuntimeEnvironmentAuthorization
	err         error
	calls       int
	committed   bool
	afterCommit func()
}

func (r *runtimeDeliveryRepository) AuthorizeHostingRuntimeEnvironment(context.Context, string, string, domain.HostingRuntimeEnvironmentRequest) (store.HostingRuntimeEnvironmentAuthorization, error) {
	r.calls++
	if r.err != nil {
		return store.HostingRuntimeEnvironmentAuthorization{}, r.err
	}
	r.committed = true
	v := r.value
	v.Records = map[domain.HostingRuntimeSecretReference]store.ManagedSecretRecord{}
	for ref, record := range r.value.Records {
		record.Envelope.Data = bytes.Clone(record.Envelope.Data)
		v.Records[ref] = record
	}
	if r.afterCommit != nil {
		r.afterCommit()
	}
	return v, nil
}

func runtimeAppFixture(t *testing.T) (*HostingRuntimeEnvironmentDelivery, *runtimeDeliveryRepository, *runtimeIdentity, context.Context, domain.HostingRuntimeEnvironmentRequest) {
	t.Helper()
	keys := sourceDeliveryRing(t, 1)
	ref := domain.HostingRuntimeSecretReference{SecretID: "secret", Version: 1}
	literal := "${NOT_EXPANDED}"
	doc, _, err := domain.CanonicalHostingRuntimeDocument([]domain.HostingRuntimeEntry{{Name: "LITERAL", Kind: "literal", Value: &literal}, {Name: "SECRET", Kind: "managed_secret", Secret: &ref}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingRuntimeEnvironmentRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: domain.HostingDocumentHash(doc)}}
	envelope, err := keys.Seal(secretcrypto.Binding{ProjectID: "project", SecretID: ref.SecretID, Version: 1}, []byte("private-value"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &runtimeDeliveryRepository{value: store.HostingRuntimeEnvironmentAuthorization{ProjectID: "project", Document: doc, Window: domain.HostingRuntimeEnvironmentWindow{DeliveryID: in.DeliveryID, RequestHash: hostingruntimeprotocol.RuntimeEnvironmentRequestHash("runner", in), UseUntil: time.Now().Add(time.Minute)}, Records: map[domain.HostingRuntimeSecretReference]store.ManagedSecretRecord{ref: {Metadata: domain.ManagedSecretVersion{SecretID: ref.SecretID, Version: 1}, Envelope: envelope}}}}
	identity := &runtimeIdentity{current: RunnerIdentity{RunnerID: "runner", Capabilities: []string{domain.HostingRuntimeEnvironmentCapability}}, token: "runtime-delivery-bearer"}
	s, err := NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, identity, repo, keys, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner", Provider: domain.PrincipalRunner})
	return s, repo, identity, ctx, in
}

func TestRuntimeEnvironmentDeliveryRejectsRotatedCredentialAfterCommit(t *testing.T) {
	s, repo, identity, ctx, in := runtimeAppFixture(t)
	oldToken := identity.token
	if _, err := identity.AuthenticateRunnerToken(ctx, oldToken); err != nil {
		t.Fatal(err)
	}
	repo.afterCommit = func() { identity.token = "replacement-bearer" }
	out, err := s.Deliver(ctx, in, "runtime-delivery-bearer")
	if out != nil {
		out.Close()
	}
	if err == nil || out != nil {
		t.Fatal("original credential delivered values after replacement")
	}
	if !repo.committed {
		t.Fatal("test did not cross the committed authorization boundary")
	}
	if _, err = identity.AuthenticateRunnerToken(ctx, oldToken); err == nil {
		t.Fatal("old fresh authentication succeeded")
	}
	if _, err = identity.AuthenticateRunnerToken(ctx, identity.token); err != nil {
		t.Fatal("replacement authentication failed", err)
	}
}

func TestRuntimeEnvironmentDeliveryAuditBoundaryAndLiteral(t *testing.T) {
	s, repo, _, ctx, in := runtimeAppFixture(t)
	out, err := s.Deliver(ctx, in, "runtime-delivery-bearer")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if !repo.committed {
		t.Fatal("delivered without audit authorization")
	}
	if err = out.WithValues(func(entries []hostingruntimeprotocol.Entry) error {
		if string(entries[0].Value) != "${NOT_EXPANDED}" || string(entries[1].Value) != "private-value" {
			t.Fatal("values changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEnvironmentDeliveryDenials(t *testing.T) {
	for _, name := range []string{"audit_failure", "nonrunner", "identity", "capability", "wrong_hash", "wrong_secret", "wrong_project", "expired", "expires_during_identity", "revoked_capability", "cancel", "invalid_plaintext", "missing_keys"} {
		t.Run(name, func(t *testing.T) {
			s, repo, identity, ctx, in := runtimeAppFixture(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			switch name {
			case "audit_failure":
				repo.err = errors.New("audit unavailable")
				s.keys = nil
			case "nonrunner":
				ctx = auth.WithPrincipal(ctx, auth.Principal{ID: "runner", Provider: domain.PrincipalAPIToken})
			case "identity":
				identity.current.RunnerID = "other"
			case "capability":
				identity.current.Capabilities = nil
			case "wrong_hash":
				repo.value.Window.RequestHash = "wrong"
			case "wrong_secret":
				for ref, r := range repo.value.Records {
					r.Metadata.Version = 2
					repo.value.Records[ref] = r
				}
			case "wrong_project":
				repo.value.ProjectID = "other"
			case "expired":
				s.clock = func() time.Time { return repo.value.Window.UseUntil }
			case "expires_during_identity":
				identity.after = func() { s.clock = func() time.Time { return repo.value.Window.UseUntil } }
			case "revoked_capability":
				identity.after = func() { identity.current.Capabilities = nil }
			case "cancel":
				identity.after = cancel
			case "invalid_plaintext":
				for ref, r := range repo.value.Records {
					r.Envelope, _ = s.keys.Seal(secretcrypto.Binding{ProjectID: "project", SecretID: ref.SecretID, Version: ref.Version}, []byte("private\nvalue"))
					repo.value.Records[ref] = r
				}
			case "missing_keys":
				s.keys = nil
			}
			got, err := s.Deliver(ctx, in, "runtime-delivery-bearer")
			if got != nil {
				got.Close()
			}
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("denial leaked or succeeded")
			}
			if (name == "nonrunner" || name == "identity" || name == "capability") && repo.calls != 0 {
				t.Fatal("unauthorized repository call")
			}
			if name == "missing_keys" && !repo.committed {
				t.Fatal("decrypt attempted before audit commit")
			}
		})
	}
}
