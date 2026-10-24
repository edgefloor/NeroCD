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
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type databaseDeliveryRepository struct {
	value                        store.HostingDatabaseCredentialAuthorization
	err                          error
	finalErr                     error
	calls, finalCalls            int
	afterCommit, afterRevalidate func()
}

func (r *databaseDeliveryRepository) AuthorizeHostingDatabaseCredentials(_ context.Context, runner, hash string, in domain.HostingDatabaseCredentialRequest) (store.HostingDatabaseCredentialAuthorization, error) {
	r.calls++
	if r.err != nil {
		return store.HostingDatabaseCredentialAuthorization{}, r.err
	}
	if runner != "runner" || hash != runnerTokenHash("database-bearer") || in.Authority != r.value.Metadata.Authority {
		return store.HostingDatabaseCredentialAuthorization{}, errors.New("incorrect credential binding")
	}
	v := r.value
	v.Records = map[domain.HostingDatabaseCredentialRef]store.ManagedSecretRecord{}
	for ref, record := range r.value.Records {
		record.Envelope.Data = bytes.Clone(record.Envelope.Data)
		v.Records[ref] = record
	}
	if r.afterCommit != nil {
		r.afterCommit()
	}
	return v, nil
}

func (r *databaseDeliveryRepository) RevalidateHostingDatabaseCredentials(_ context.Context, runner, hash string, in domain.HostingDatabaseCredentialRequest, m hostingdatabaseprotocol.Metadata) error {
	r.finalCalls++
	if runner != "runner" || hash != runnerTokenHash("database-bearer") || m.RequestHash != hostingdatabaseprotocol.RequestHash(runner, in) || domain.HostingDocumentHash(m) != domain.HostingDocumentHash(r.value.Metadata) {
		return errors.New("changed authority")
	}
	if r.afterRevalidate != nil {
		r.afterRevalidate()
	}
	return r.finalErr
}

func databaseDeliveryAppFixture(t *testing.T) (*HostingDatabaseCredentialDelivery, *databaseDeliveryRepository, *runtimeIdentity, context.Context, domain.HostingDatabaseCredentialRequest) {
	t.Helper()
	keys := sourceDeliveryRing(t, 1)
	in := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), Mode: "deploy"}
	recipe, err := domain.HostingDatabaseRecipeFor("postgresql", domain.HostingDatabasePostgresRecipe)
	if err != nil {
		t.Fatal(err)
	}
	refs := recipe.Credentials
	records := map[domain.HostingDatabaseCredentialRef]store.ManagedSecretRecord{}
	for i := range refs {
		ref := &refs[i]
		ref.SecretID = domain.HostingDatabaseSecretID("resource", ref.Role)
		ref.Version = 1
		envelope, e := keys.Seal(secretcrypto.Binding{ProjectID: "project", SecretID: ref.SecretID, Version: 1}, []byte(strings.Repeat("A", 43)))
		if e != nil {
			t.Fatal(e)
		}
		records[*ref] = store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: ref.SecretID, Version: 1}, Envelope: envelope}
	}
	repo := &databaseDeliveryRepository{value: store.HostingDatabaseCredentialAuthorization{ProjectID: "project", Metadata: hostingdatabaseprotocol.Metadata{RunnerID: "runner", Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: domain.HostingDatabaseExecutionRef{Engine: recipe.Engine, RecipeVersion: recipe.Version, ConfigurationHash: in.PlanHash}, RequestHash: hostingdatabaseprotocol.RequestHash("runner", in), Purpose: hostingdatabaseprotocol.Purpose, UseUntil: time.Now().Add(30 * time.Second), References: refs}, Records: records}}
	identity := &runtimeIdentity{current: RunnerIdentity{RunnerID: "runner", Capabilities: []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}}, token: "database-bearer"}
	s, err := NewHostingDatabaseCredentialDelivery(auth.ContextProvider{}, identity, repo, keys, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner", Provider: domain.PrincipalRunner})
	return s, repo, identity, ctx, in
}

func TestDatabaseCredentialDeliveryAuditAndFreshAuthority(t *testing.T) {
	s, repo, _, ctx, in := databaseDeliveryAppFixture(t)
	out, err := s.Deliver(ctx, in, "database-bearer")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if repo.calls != 1 || repo.finalCalls != 1 {
		t.Fatal("missing audit or post-decrypt transaction")
	}
	if err = out.WithValues(func(v []hostingdatabaseprotocol.Credential) error {
		if len(v) != 2 || !bytes.Equal(v[0].Value, []byte(strings.Repeat("A", 43))) {
			t.Fatal("wrong scoped values")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseCredentialDeliveryFailClosed(t *testing.T) {
	for _, name := range []string{"audit", "wrong_key", "missing_key", "wrong_project", "wrong_version", "wrong_ref", "wrong_request", "expired", "nonrunner", "missing_bearer", "capability", "rotate_after_audit", "revoke_after_decrypt", "authority_after_decrypt", "expiry_after_decrypt", "capability_after_decrypt", "cancel"} {
		t.Run(name, func(t *testing.T) {
			s, repo, identity, ctx, in := databaseDeliveryAppFixture(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			token := "database-bearer"
			switch name {
			case "audit":
				repo.err = errors.New("private audit diagnostic")
				s.keys = nil
			case "wrong_key":
				s.keys = sourceDeliveryRing(t, 2)
			case "missing_key":
				s.keys = nil
			case "wrong_project":
				repo.value.ProjectID = "foreign"
			case "wrong_version":
				for ref, r := range repo.value.Records {
					r.Metadata.Version++
					repo.value.Records[ref] = r
				}
			case "wrong_ref":
				repo.value.Metadata.References[0].SecretID = "foreign"
			case "wrong_request":
				repo.value.Metadata.RequestHash = "wrong"
			case "expired":
				s.clock = func() time.Time { return repo.value.Metadata.UseUntil }
			case "nonrunner":
				ctx = auth.WithPrincipal(ctx, auth.Principal{ID: "runner", Provider: domain.PrincipalAPIToken})
			case "missing_bearer":
				token = ""
			case "capability":
				identity.current.Capabilities = nil
			case "rotate_after_audit":
				repo.afterCommit = func() { identity.token = "replacement" }
			case "revoke_after_decrypt":
				repo.finalErr = errors.New("revoked original bearer")
			case "authority_after_decrypt":
				repo.finalErr = errors.New("private revoked operation")
			case "expiry_after_decrypt":
				repo.afterRevalidate = func() { s.clock = func() time.Time { return repo.value.Metadata.UseUntil } }
			case "capability_after_decrypt":
				repo.finalErr = errors.New("revoked database capability")
			case "cancel":
				repo.afterRevalidate = cancel
			}
			out, err := s.Deliver(ctx, in, token)
			if out != nil {
				out.Close()
			}
			if err == nil || out != nil || strings.Contains(err.Error(), "private") {
				t.Fatal("denial leaked or succeeded")
			}
			if name == "audit" && repo.finalCalls != 0 {
				t.Fatal("decrypt phase followed failed audit")
			}
			if (name == "wrong_key" || name == "missing_key") && (repo.calls != 1 || repo.finalCalls != 0) {
				t.Fatal("decryption did not follow committed authorization")
			}
			if strings.Contains(name, "after_decrypt") && repo.finalCalls != 1 {
				t.Fatal("post-decrypt check was not exercised")
			}
		})
	}
}
