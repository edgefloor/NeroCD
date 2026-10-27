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
		t.Fatal("stack credential accepted")
	}
}
