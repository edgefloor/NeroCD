package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoredDockerfileSourceContract(t *testing.T) {
	var in ResourceSourceInput
	raw := `{"kind":"dockerfile","dockerfile":{"content":"FROM scratch\n","settings":{"strategy":"dockerfile"}}}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResourceSource(in.Source()); err != nil {
		t.Fatal(err)
	}
	if in.Dockerfile.Content != "FROM scratch\n" {
		t.Fatal("content changed")
	}
	for _, bad := range []string{
		strings.Replace(raw, `"settings":`, `"source_id":"forged","settings":`, 1),
		strings.Replace(raw, `"kind":"dockerfile"`, `"kind":"dockerfile","git":{}`, 1),
	} {
		if json.Unmarshal([]byte(bad), &in) == nil {
			t.Fatal("accepted mixed/server-owned source")
		}
	}
	for _, content := range []string{"", " \n", "FROM scratch\x00", strings.Repeat("x", HostingDockerfileContentLimit+1), string([]byte{0xff})} {
		if ValidateDockerfileResourceConfiguration(DockerfileResourceConfiguration{Content: content, Settings: HostingBuildSettings{Strategy: "dockerfile"}}) == nil {
			t.Fatal("accepted invalid content")
		}
	}
	if err := ValidateDockerfileResourceConfiguration(DockerfileResourceConfiguration{Content: strings.Repeat("x", HostingDockerfileContentLimit), Settings: HostingBuildSettings{Strategy: "dockerfile"}}); err != nil {
		t.Fatal(err)
	}
}

func TestStoredDockerfileSerializationPreservesGitIdentity(t *testing.T) {
	// The pre-extension Git field order and empty-value encoding are identity-bearing.
	git := HostingGitSource{ID: "source", ResourceID: "resource", ProjectID: "project", Generation: 1, RepositoryURL: "https://example.com/repo", RequestedRef: "main", CredentialKind: "public", Settings: HostingBuildSettings{Strategy: "dockerfile"}}
	type original struct {
		ID             string               `json:"id"`
		ResourceID     string               `json:"resource_id"`
		ProjectID      string               `json:"project_id"`
		Generation     int64                `json:"generation"`
		RepositoryURL  string               `json:"repository_url"`
		RequestedRef   string               `json:"requested_ref"`
		CredentialKind string               `json:"credential_kind"`
		Policy         any                  `json:"policy"`
		Settings       HostingBuildSettings `json:"settings"`
	}
	before, _ := json.Marshal(original{git.ID, git.ResourceID, git.ProjectID, git.Generation, git.RepositoryURL, git.RequestedRef, git.CredentialKind, git.Policy, git.Settings})
	after, err := json.Marshal(git)
	if err != nil || string(before) != string(after) {
		t.Fatalf("Git identity changed: %s != %s (%v)", before, after, err)
	}
	git.Kind = "dockerfile"
	git.Content = "FROM scratch\n"
	git.ContentHash = HostingDockerfileContentHash(git.Content)
	private, _ := json.Marshal(git)
	public, _ := json.Marshal(PublicBuild(HostingBuild{Snapshot: HostingBuildSnapshot{Source: git}}).Snapshot.Source)
	for _, raw := range [][]byte{private, public} {
		if strings.Contains(string(raw), "repository_url") || strings.Contains(string(raw), "requested_ref") || strings.Contains(string(raw), "policy") {
			t.Fatalf("Git metadata in stored source: %s", raw)
		}
	}
	if strings.Contains(string(public), "FROM scratch") {
		t.Fatal("public build duplicated source content")
	}
}
