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
}

func (r *runtimeIdentity) CurrentRunnerIdentity(context.Context) (RunnerIdentity, error) {
	r.calls++
	if r.calls == 2 && r.after != nil {
		r.after()
	}
	return r.current, nil
}

type runtimeDeliveryRepository struct {
	value     store.HostingRuntimeEnvironmentAuthorization
	err       error
	calls     int
	committed bool
}

func (r *runtimeDeliveryRepository) AuthorizeHostingRuntimeEnvironment(context.Context, string, domain.HostingRuntimeEnvironmentRequest) (store.HostingRuntimeEnvironmentAuthorization, error) {
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
	identity := &runtimeIdentity{current: RunnerIdentity{RunnerID: "runner", Capabilities: []string{domain.HostingRuntimeEnvironmentCapability}}}
	s, err := NewHostingRuntimeEnvironmentDelivery(auth.ContextProvider{}, identity, repo, keys, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner", Provider: domain.PrincipalRunner})
	return s, repo, identity, ctx, in
}

func TestRuntimeEnvironmentDeliveryAuditBoundaryAndLiteral(t *testing.T) {
	s, repo, _, ctx, in := runtimeAppFixture(t)
	out, err := s.Deliver(ctx, in)
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
			got, err := s.Deliver(ctx, in)
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
