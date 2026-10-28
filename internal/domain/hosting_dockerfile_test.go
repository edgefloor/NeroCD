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
	if _, err := json.Marshal(git); err == nil {
		t.Fatal("mixed stored/Git source serialized")
	}
	git.RepositoryURL = ""
	git.RequestedRef = ""
	git.CredentialKind = ""
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

func TestStoredDockerfileLegacyReceiptAndStoredIdentity(t *testing.T) {
	empty, _ := json.Marshal(HostingBuildSourceReceipt{})
	if string(empty) != `{"build_id":"","source_id":"","input_id":"","commit":"","policy_hash":"","tree_digest":""}` {
		t.Fatal("legacy empty receipt changed", string(empty))
	}
	s := HostingBuildSnapshot{BuildID: "build", InputID: "sha256:" + strings.Repeat("a", 64), Source: HostingGitSource{Kind: "dockerfile", ID: "source", ResourceID: "resource", ProjectID: "project", Generation: 1, Content: "FROM scratch", ContentHash: HostingDockerfileContentHash("FROM scratch"), Settings: HostingBuildSettings{Strategy: "dockerfile"}}}
	r := HostingBuildSourceReceipt{Kind: "dockerfile", ContentHash: s.Source.ContentHash, BuildID: s.BuildID, SourceID: s.Source.ID, InputID: s.InputID, TreeDigest: "sha256:" + strings.Repeat("b", 64)}
	if err := ValidateHostingBuildSourceReceipt(r, s); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*HostingBuildSnapshot){func(s *HostingBuildSnapshot) { s.Source.Content += "\n" }, func(s *HostingBuildSnapshot) { s.Source.RepositoryURL = "https://example.com/repo" }, func(s *HostingBuildSnapshot) {
		s.TriggerRevision = &HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("a", 40)}
	}, func(s *HostingBuildSnapshot) { s.Grant = &HostingSourceGrant{ID: "grant"} }} {
		copy := s
		change(&copy)
		if ValidateHostingBuildSourceReceipt(r, copy) == nil {
			t.Fatal("mismatched source/authority accepted")
		}
	}
}
