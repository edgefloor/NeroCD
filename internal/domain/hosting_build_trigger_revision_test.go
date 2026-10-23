package domain_test

import (
	"encoding/json"
	"nerocd/internal/domain"
	"strings"
	"testing"
)

func TestHostingBuildTriggerRevisionValidation(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name     string
		revision *domain.HostingBuildTriggerRevision
		ref      string
		valid    bool
	}{
		{"legacy", nil, "main", true},
		{"sha1", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: commit}, "main", true},
		{"sha256", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("b", 64)}, "main", true},
		{"configured commit", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: commit}, commit, true},
		{"different configured commit", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: commit}, strings.Repeat("b", 40), false},
		{"version", &domain.HostingBuildTriggerRevision{Version: 2, ExpectedCommit: commit}, "main", false},
		{"zero", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("0", 40)}, "main", false},
		{"uppercase", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: strings.Repeat("A", 40)}, "main", false},
		{"short", &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: "abcdef"}, "main", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := domain.HostingBuildSnapshot{TriggerRevision: tc.revision}
			s.Source.RequestedRef = tc.ref
			if (domain.ValidateHostingBuildTriggerRevision(s) == nil) != tc.valid {
				t.Fatal("revision validation mismatch")
			}
		})
	}
}

func TestHostingBuildTriggerRevisionNilSerializationAndReceipt(t *testing.T) {
	s := domain.HostingBuildSnapshot{}
	legacy, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "trigger_revision") {
		t.Fatal("legacy serialization changed")
	}
	// A typed historical shape proves nil does not change field order or the hash.
	type historicalSnapshot struct {
		BuildID            string                     `json:"build_id"`
		InputID            string                     `json:"input_id"`
		ResourceGeneration int64                      `json:"resource_generation"`
		Source             domain.HostingGitSource    `json:"source"`
		BindingID          string                     `json:"binding_id"`
		DestinationID      string                     `json:"destination_id"`
		ServerID           string                     `json:"server_id"`
		RunnerID           string                     `json:"runner_id"`
		ExpectedDaemonID   string                     `json:"expected_daemon_id,omitempty"`
		Policy             domain.HostingBuildPolicy  `json:"operator_policy"`
		Grant              *domain.HostingSourceGrant `json:"grant,omitempty"`
	}
	old := historicalSnapshot{}
	if domain.HostingDocumentHash(s) != domain.HostingDocumentHash(old) {
		t.Fatalf("nil revision hash differs: %s", legacy)
	}
	s.BuildID = "build"
	s.InputID = "input"
	s.Source.ID = "source"
	s.Source.RequestedRef = "main"
	receipt := domain.HostingBuildSourceReceipt{BuildID: s.BuildID, InputID: s.InputID, SourceID: s.Source.ID, Commit: strings.Repeat("a", 40), PolicyHash: domain.HostingDocumentHash(s.Source.Policy), TreeDigest: "sha256:" + strings.Repeat("b", 64)}
	if err := domain.ValidateHostingBuildSourceReceipt(receipt, s); err != nil {
		t.Fatal(err)
	}
	s.TriggerRevision = &domain.HostingBuildTriggerRevision{Version: 1, ExpectedCommit: receipt.Commit}
	if err := domain.ValidateHostingBuildSourceReceipt(receipt, s); err != nil {
		t.Fatal(err)
	}
	receipt.Commit = strings.Repeat("c", 40)
	if domain.ValidateHostingBuildSourceReceipt(receipt, s) == nil {
		t.Fatal("wrong trigger commit accepted")
	}
	b := domain.HostingBuild{Snapshot: s}
	public := domain.PublicBuild(b)
	if public.Snapshot.TriggerRevision == nil {
		t.Fatal("public revision omitted")
	}
	public.Snapshot.TriggerRevision.ExpectedCommit = "changed"
	if s.TriggerRevision.ExpectedCommit == "changed" {
		t.Fatal("public revision aliases immutable snapshot")
	}
}
