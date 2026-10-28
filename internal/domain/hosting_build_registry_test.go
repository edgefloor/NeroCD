package domain

import (
	"encoding/json"
	"testing"
)

func TestHostingBuildRegistrySettings(t *testing.T) {
	for _, strategy := range []string{"dockerfile", "railpack", "static"} {
		s := HostingBuildSettings{Strategy: strategy, BuildRegistryCredential: &HostingRegistryCredentialRef{SecretID: "secret_build", Version: 1}}
		if got := ValidateHostingBuildSettings(s); (got == nil) != (strategy == "dockerfile") {
			t.Fatalf("strategy %s: %v", strategy, got)
		}
	}
	s := HostingBuildSettings{Strategy: "dockerfile", BuildRegistryCredential: &HostingRegistryCredentialRef{SecretID: "secret_build", Version: 1}}
	c := s.Clone()
	c.BuildRegistryCredential.Version = 2
	if s.BuildRegistryCredential.Version != 1 {
		t.Fatal("credential reference aliases source")
	}
	b, _ := json.Marshal(HostingBuildSettings{Strategy: "dockerfile"})
	if string(b) != `{"strategy":"dockerfile"}` {
		t.Fatalf("legacy settings changed: %s", b)
	}
	for _, raw := range []string{`{"strategy":"dockerfile","build_registry_credential":null}`, `{"strategy":"dockerfile"}`} {
		var decoded HostingBuildSettings
		if json.Unmarshal([]byte(raw), &decoded) != nil || decoded.BuildRegistryCredential != nil {
			t.Fatal("null/absent selection retained")
		}
	}
}
