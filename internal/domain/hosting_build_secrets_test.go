package domain_test

import (
	"encoding/json"
	"nerocd/internal/domain"
	"testing"
)

func TestHostingBuildSecretsReferencesAndIdentity(t *testing.T) {
	base := domain.HostingBuildSettings{Strategy: "dockerfile"}
	original := domain.HostingDocumentHash(base)
	base.BuildSecrets = []domain.HostingBuildSecretReference{}
	if domain.HostingDocumentHash(base) != original {
		t.Fatal("empty secrets changed historical identity")
	}
	for _, raw := range []string{`{"name":"TOKEN","secret":{"secret_id":"secret_token","version":1}}`, `{"name":"MULTILINE","secret":{"secret_id":"secret_token","version":2}}`} {
		var ref domain.HostingBuildSecretReference
		if err := json.Unmarshal([]byte(raw), &ref); err != nil {
			t.Fatal(err)
		}
		base.BuildSecrets = append(base.BuildSecrets, ref)
	}
	if err := domain.ValidateHostingBuildSettings(base); err != nil {
		t.Fatal(err)
	}
	clone := base.Clone()
	clone.BuildSecrets[0].Secret.Version = 3
	if base.BuildSecrets[0].Secret.Version != 1 || domain.HostingDocumentHash(base) == domain.HostingDocumentHash(clone) {
		t.Fatal("reference mutation did not have owned identity")
	}
	for _, raw := range []string{`null`, `{}`, `{"name":"TOKEN","secret":null}`, `{"name":"TOKEN","secret":{"secret_id":"secret_token","version":0}}`, `{"name":"HTTP_PROXY","secret":{"secret_id":"secret_token","version":1}}`, `{"name":"TOKEN","secret":{"secret_id":"secret_token","version":1},"value":"unsafe"}`, `{"name":"TOKEN","name":"OTHER","secret":{"secret_id":"secret_token","version":1}}`} {
		var ref domain.HostingBuildSecretReference
		if json.Unmarshal([]byte(raw), &ref) == nil {
			t.Fatalf("accepted invalid reference %s", raw)
		}
	}
	clone = base.Clone()
	clone.BuildVariables = []domain.HostingBuildVariable{{Name: "TOKEN", Value: "public"}}
	if domain.ValidateHostingBuildSettings(clone) == nil {
		t.Fatal("accepted public/secret collision")
	}
	clone = base.Clone()
	clone.Strategy = "railpack"
	if domain.ValidateHostingBuildSettings(clone) == nil {
		t.Fatal("accepted railpack secret")
	}
	clone = base.Clone()
	clone.BuildSecrets = append(clone.BuildSecrets, clone.BuildSecrets[0])
	if domain.ValidateHostingBuildSettings(clone) == nil {
		t.Fatal("accepted duplicate secret")
	}
}
