package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type sourceRunnerIdentity struct {
	ID  string
	Err error
}

func (r sourceRunnerIdentity) CurrentRunnerIdentity(context.Context) (RunnerIdentity, error) {
	return RunnerIdentity{RunnerID: r.ID}, r.Err
}

type sourceDeliveryRepository struct {
	Value store.HostingSourceCredentialAuthorization
	Err   error
	Calls int
}

func (r *sourceDeliveryRepository) AuthorizeHostingSourceCredential(_ context.Context, _ string, _ domain.HostingSourceCredentialRequest) (store.HostingSourceCredentialAuthorization, error) {
	r.Calls++
	v := r.Value
	v.Record.Envelope.Data = bytes.Clone(v.Record.Envelope.Data)
	return v, r.Err
}
func sourceDeliveryRing(t *testing.T, value byte) *secretcrypto.Keyring {
	t.Helper()
	k, e := secretcrypto.New("delivery", map[string][]byte{"delivery": bytes.Repeat([]byte{value}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func sourceDeliveryAppFixture(t *testing.T) (*HostingBuildCredentialDelivery, *sourceDeliveryRepository, context.Context, domain.HostingSourceCredentialRequest) {
	t.Helper()
	keys := sourceDeliveryRing(t, 1)
	in := domain.HostingSourceCredentialRequest{Authority: domain.HostingBuildAuthority{BuildID: "build", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32)}
	envelope, err := keys.Seal(secretcrypto.Binding{ProjectID: "project", SecretID: "secret", Version: 1}, []byte(`{"username":"fixture-user","password":"private-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	repo := &sourceDeliveryRepository{Value: store.HostingSourceCredentialAuthorization{ProjectID: "project", SecretID: "secret", SecretVersion: 1, Kind: "https_basic", Window: domain.HostingSourceCredentialWindow{DeliveryID: in.DeliveryID, RequestHash: domain.HostingDocumentHash(struct {
		Runner  string
		Request domain.HostingSourceCredentialRequest
	}{"runner", in}), UseUntil: time.Now().Add(time.Minute)}, Record: store.ManagedSecretRecord{Metadata: domain.ManagedSecretVersion{SecretID: "secret", Version: 1}, Envelope: envelope}}}
	component, err := NewHostingBuildCredentialDelivery(auth.ContextProvider{}, sourceRunnerIdentity{ID: "runner"}, repo, keys, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner", Provider: domain.PrincipalRunner})
	return component, repo, ctx, in
}
func TestHostingBuildCredentialDeliveryLifecycleAndNoSerialization(t *testing.T) {
	component, repo, ctx, in := sourceDeliveryAppFixture(t)
	delivery, err := component.Deliver(ctx, in)
	if err != nil || repo.Calls != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(delivery)
	formatted := fmt.Sprintf("%v %+v %#v", delivery, delivery, delivery)
	if string(raw) != "{}" || strings.Contains(formatted, "private-password") || strings.Contains(formatted, "fixture-user") {
		t.Fatal("delivery serialized credential")
	}
	material := delivery.Take()
	defer material.Close()
	if string(material.Bytes) != "private-password" || material.Username != "fixture-user" || material.Kind != "https_basic" {
		t.Fatal("decoded credential mismatch")
	}
	if next := delivery.Take(); len(next.Bytes) != 0 {
		t.Fatal("transferred credential twice")
	}
	alias := material.Bytes
	raw, _ = json.Marshal(material)
	if string(raw) != "{}" || strings.Contains(fmt.Sprintf("%#v", material), "private-password") {
		t.Fatal("material serialized credential")
	}
	material.Close()
	if !bytes.Equal(alias, make([]byte, len(alias))) {
		t.Fatal("owned credential bytes not cleared")
	}
}
func TestHostingBuildCredentialDeliveryFailClosed(t *testing.T) {
	for _, test := range []string{"audit", "missing_key", "wrong_key", "tamper", "binding", "malformed", "expired", "expires_after_decode", "window_request", "runner_mismatch", "non_runner"} {
		t.Run(test, func(t *testing.T) {
			component, repo, ctx, in := sourceDeliveryAppFixture(t)
			switch test {
			case "audit":
				repo.Err = errors.New("private-database-diagnostic")
			case "missing_key":
				component.keys = nil
			case "wrong_key":
				component.keys = sourceDeliveryRing(t, 2)
			case "tamper":
				repo.Value.Record.Envelope.Data[12] ^= 1
			case "binding":
				repo.Value.ProjectID = "other-project"
			case "malformed":
				var err error
				repo.Value.Record.Envelope, err = component.keys.Seal(secretcrypto.Binding{ProjectID: "project", SecretID: "secret", Version: 1}, []byte(`{"password":"private-password"}`))
				if err != nil {
					t.Fatal(err)
				}
			case "expired":
				repo.Value.Window.UseUntil = time.Now().Add(-time.Second)
			case "expires_after_decode":
				calls := 0
				component.clock = func() time.Time {
					calls++
					if calls == 1 {
						return repo.Value.Window.UseUntil.Add(-time.Second)
					}
					return repo.Value.Window.UseUntil
				}
			case "window_request":
				repo.Value.Window.RequestHash = "wrong"
			case "runner_mismatch":
				component.runners = sourceRunnerIdentity{ID: "other"}
			case "non_runner":
				ctx = auth.WithPrincipal(ctx, auth.Principal{ID: "runner", Provider: domain.PrincipalAPIToken, Roles: []string{domain.RoleSystemAdmin}})
			}
			got, err := component.Deliver(ctx, in)
			material := got.Take()
			defer material.Close()
			if err == nil || len(material.Bytes) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe failure: %v", err)
			}
			if (test == "non_runner" || test == "runner_mismatch") && repo.Calls != 0 {
				t.Fatal("untrusted identity reached repository")
			}
		})
	}
}
func TestHostingBuildCredentialStrictDecoding(t *testing.T) {
	for _, value := range []string{`{}`, `{"username":"u","password":""}`, `{"username":"u","password":"p","unknown":true}`, `{"username":"u","username":"v","password":"p"}`, `{"username":"u","user\u006eame":"v","password":"p"}`, `{"username":"u","password":"p"} {}`, `{"username":"u","password":"a\nb"}`, `{"username":null,"password":"p"}`, `{"username":"u","password":1}`} {
		material, err := decodeHostingSourceCredential("https_basic", []byte(value))
		material.Close()
		if err == nil {
			t.Fatal("accepted malformed credential")
		}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	defer clear(raw)
	material, err := decodeHostingSourceCredential("ssh_private_key", raw)
	if err != nil {
		t.Fatal(err)
	}
	material.Close()
	encrypted, err := ssh.MarshalPrivateKeyWithPassphrase(private, "", []byte("private-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	material, err = decodeHostingSourceCredential("ssh_private_key", pem.EncodeToMemory(encrypted))
	material.Close()
	if err == nil {
		t.Fatal("accepted interactive encrypted key")
	}
	for _, value := range [][]byte{[]byte("not a private key"), {0xff}} {
		if _, err = decodeHostingSourceCredential("ssh_private_key", value); err == nil {
			t.Fatal("accepted malformed SSH key")
		}
	}
}
