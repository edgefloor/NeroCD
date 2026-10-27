package domain

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHostingRegistryCredentialClosedContract(t *testing.T) {
	for _, raw := range []string{`{"registry":"ghcr.io","username":"user","password":"token"}`, `{"registry":"index.docker.io","username":"user","password":"token"}`} {
		c, err := DecodeHostingRegistryCredential([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(c)
		if string(b) != "{}" {
			t.Fatal("credential serialized")
		}
		c.Close()
	}
	for _, raw := range []string{`{"registry":"ghcr.io","username":"user","password":"token","other":1}`, `{"registry":"ghcr.io","username":"user","password":"token","password":"second"}`, `{"registry":"https://ghcr.io","username":"user","password":"token"}`, `{"registry":"other/path","username":"user","password":"token"}`, `{"registry":"ghcr.io","username":"user:other","password":"token"}`, `{"registry":"ghcr.io","username":"user","password":""}`} {
		if _, err := DecodeHostingRegistryCredential([]byte(raw)); err == nil {
			t.Fatal("malformed credential accepted")
		}
	}
	var ref HostingRegistryCredentialRef
	for _, raw := range []string{`{"secret_id":"secret","version":0}`, `{"secret_id":"secret","version":1,"password":"token"}`, `{"secret_id":"secret","version":1,"version":2}`} {
		if json.Unmarshal([]byte(raw), &ref) == nil {
			t.Fatal("invalid reference accepted")
		}
	}
	d := HostingRegistryCredentialDelivery{Credential: HostingRegistryCredential{Registry: "ghcr.io", Username: "user", Password: "token"}, UseUntil: time.Now().Add(time.Second)}
	var buf bytes.Buffer
	if WriteHostingRegistryDelivery(&buf, &d) != nil {
		t.Fatal("codec write")
	}
	read, err := ReadHostingRegistryDelivery(&buf)
	if err != nil || read.Credential.Password != "token" {
		t.Fatal("codec read")
	}
	read.Close()
}
func TestHostingRegistryAnonymousHistoricalSerialization(t *testing.T) {
	// Exact pre-feature anonymous snapshot encoding, including field order.
	const historic = `{"resource_id":"res","resource_generation":1,"image_reference":"nginx:1.27","binding_id":"binding","destination_id":"dest","server_id":"server","runner_id":"runner","runtime_namespace":"ncd_abc","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"}`
	var s HostingSnapshot
	if err := json.Unmarshal([]byte(historic), &s); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if string(raw) != historic || HostingDocumentHash(s) != HostingDocumentHash(json.RawMessage(historic)) {
		t.Fatal("anonymous history changed", string(raw))
	}
	s.RegistryCredential = HostingRegistryCredentialRef{SecretID: "secret", Version: 1}
	if ValidateHostingRegistrySnapshot(s) != nil {
		t.Fatal("ordinary image denied")
	}
	s.Stack = HostingStackRef{Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}
	if ValidateHostingRegistrySnapshot(s) == nil {
		t.Fatal("mixed stack and image source accepted")
	}
}

func TestHostingRegistryManagedStackDecoderAndSnapshot(t *testing.T) {
	const raw = `{"expected_generation":1,"idempotency_key":"private-stack","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5},"managed_stack":{"acknowledge_interruption":true},"registry_credential":{"secret_id":"secret","version":1}}`
	var in HostingDeploymentInput
	if json.Unmarshal([]byte(raw), &in) != nil || ValidateHostingDeployment(in) != nil {
		t.Fatal("valid private stack request rejected")
	}
	for _, selection := range []string{`null`, `{}`, `{"secret_id":"secret","version":0}`, `{"secret_id":"secret","version":1,"registry":"ghcr.io"}`} {
		body := strings.Replace(raw, `{"secret_id":"secret","version":1}`, selection, 1)
		if json.Unmarshal([]byte(body), &in) == nil {
			t.Fatal("invalid selected credential decoded")
		}
	}
	s := HostingSnapshot{ResourceGeneration: 1, Stack: HostingStackRef{Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}, RegistryCredential: HostingRegistryCredentialRef{SecretID: "secret", Version: 1}}
	if ValidateHostingRegistrySnapshot(s) != nil {
		t.Fatal("private stack snapshot rejected")
	}
	before := HostingDocumentHash(s)
	s.RegistryCredential.Version++
	if HostingDocumentHash(s) == before {
		t.Fatal("credential version not included in immutable snapshot")
	}
	s.Stack.Hash = "invalid"
	if ValidateHostingRegistrySnapshot(s) == nil {
		t.Fatal("invalid stack identity accepted")
	}
}

func TestHostingRegistryAnonymousStackHistoricalSerialization(t *testing.T) {
	const historic = `{"stack":{"generation":1,"hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"resource_id":"res","resource_generation":1,"image_reference":"","binding_id":"binding","destination_id":"dest","server_id":"server","runner_id":"runner","runtime_namespace":"ncd_abc","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"managed-compose.interrupting.v1"}`
	var snapshot HostingSnapshot
	if err := json.Unmarshal([]byte(historic), &snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || string(raw) != historic || HostingDocumentHash(snapshot) != HostingDocumentHash(json.RawMessage(historic)) {
		t.Fatal("anonymous saved-stack history changed", string(raw), err)
	}
}
