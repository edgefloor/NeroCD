package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func storageDiagnosticWord(value string, allowed ...string) string {
	for _, word := range allowed {
		if value == word {
			return word
		}
	}
	return "unknown"
}

func storageDiagnosticResult(r *domain.HostingStorageArchiveResult) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"archive_outcome": storageDiagnosticWord(r.ArchiveOutcome, "succeeded", "failed", "not_attempted"),
		"resume_outcome":  storageDiagnosticWord(r.ResumeOutcome, "resumed", "not_needed", "uncertain"),
		"failure_code":    storageDiagnosticWord(r.FailureCode, "", "helper_failed", "archive_invalid", "ownership_mismatch", "interrupted", "resume_failed", "canceled"),
		"uncertain":       r.Uncertain, "restore_verified": r.RestoreVerified,
	}
}

func storageDiagnosticPhases(entries []runner.JournalStorageArchive, operation string) []string {
	var phases []string
	for _, entry := range entries {
		if entry.Plan.OperationID != operation || len(phases) == 32 {
			continue
		}
		phase := entry.Phase
		if entry.Progress != nil {
			phase = entry.Progress.Phase
		}
		phases = append(phases, storageDiagnosticWord(phase, "plan", "prepared", "apply_intent", "writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_unverified", "helper_identified", "archive_published", "restore_verified", "resume_intent", "resumed", "complete", "storage_create_intent", "storage_materialized", "recovery_settlement"))
	}
	return phases
}

// Decode only the archive family; never log a journal or decode application payloads.
func storageDiagnosticJournal(input io.Reader) ([]runner.JournalStorageArchive, error) {
	const limit = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || len(raw) > limit {
		return nil, fmt.Errorf("journal unavailable or exceeds diagnostic bound")
	}
	var snapshot struct {
		Entries []runner.JournalStorageArchive `json:"storage_archive"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Entries) > 128 {
		return nil, fmt.Errorf("journal snapshot invalid or exceeds diagnostic bound")
	}
	return snapshot.Entries, nil
}

func TestStorageRuntimeDiagnosticBoundaries(t *testing.T) {
	secret := "credential-or-application-payload"
	r := &domain.HostingStorageArchiveResult{ArchiveOutcome: secret, ResumeOutcome: "resumed", FailureCode: secret, Blob: &domain.HostingStorageArchiveBlob{SHA256: secret}}
	entries := []runner.JournalStorageArchive{{Plan: domain.HostingStorageArchivePlan{OperationID: "owned"}, Phase: secret}, {Plan: domain.HostingStorageArchivePlan{OperationID: "other"}, Phase: "complete"}}
	raw, err := json.Marshal(map[string]any{"result": storageDiagnosticResult(r), "phases": storageDiagnosticPhases(entries, "owned")})
	if err != nil || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "unknown") || strings.Contains(string(raw), "complete") {
		t.Fatal("diagnostic allowlist leaked unapproved values")
	}
	for _, input := range []string{"{", strings.Repeat(" ", (4<<20)+1), `{"storage_archive":[` + strings.Repeat(`{},`, 128) + `{}]}`} {
		if _, err := storageDiagnosticJournal(strings.NewReader(input)); err == nil {
			t.Fatal("unbounded or malformed diagnostic journal accepted")
		}
	}
	decoded, err := storageDiagnosticJournal(strings.NewReader(`{"events":[{"payload":"` + secret + `"}],"storage_archive":[]}`))
	if err != nil || len(decoded) != 0 {
		t.Fatal("unrelated journal payload influenced archive diagnostic")
	}
	entries = nil
	for range 40 {
		entries = append(entries, runner.JournalStorageArchive{Plan: domain.HostingStorageArchivePlan{OperationID: "owned"}, Progress: &domain.HostingStorageArchiveProgress{Phase: "helper_intent"}})
	}
	if phases := storageDiagnosticPhases(entries, "owned"); len(phases) != 32 || phases[0] != "helper_intent" {
		t.Fatal("diagnostic phase bound or progress selection changed")
	}
}

// Direct fixture starts must use a prerequisite image already verified by the
// gate. This covers Pebble and Silo without changing production pull behavior.
func storageRuntimeDockerArguments(args []string) []string {
	if len(args) > 0 && args[0] == "run" {
		return append([]string{"run", "--pull=never"}, args[1:]...)
	}
	return args
}

// Docker progress and warnings use stderr. They must never become a container
// ID, JSON document, or other stdout value consumed by the fixture.
func storageRuntimeDockerOutput(command *exec.Cmd) ([]byte, error) {
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, diagnostics.Bytes())
	}
	return output, nil
}

func TestStorageRuntimeDockerOutputSeparatesDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		failure    bool
	}{
		{name: "successful ID with pull-like diagnostics", mode: "success"},
		{name: "failed command keeps stderr", mode: "failure", failure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStorageRuntimeDockerOutputProcess$")
			command.Env = append(os.Environ(), "NEROCD_STORAGE_DOCKER_OUTPUT_TEST="+test.mode)
			output, err := storageRuntimeDockerOutput(command)
			if test.failure {
				if err == nil || !strings.Contains(err.Error(), "fixture Docker diagnostic") || len(output) != 0 {
					t.Fatalf("failure output=%q error=%v", output, err)
				}
				return
			}
			if err != nil || string(output) != strings.Repeat("a", 64)+"\n" {
				t.Fatalf("container ID contaminated: %q error=%v", output, err)
			}
		})
	}
}

func TestStorageRuntimeDockerOutputProcess(t *testing.T) {
	mode := os.Getenv("NEROCD_STORAGE_DOCKER_OUTPUT_TEST")
	if mode == "" {
		return
	}
	fmt.Fprintln(os.Stderr, "fixture Docker diagnostic")
	fmt.Fprintln(os.Stdout, strings.Repeat("a", 64))
	if mode == "failure" {
		os.Exit(17)
	}
	os.Exit(0)
}

func TestStorageRuntimeDockerDirectStartsNeverPull(t *testing.T) {
	original := []string{"run", "--detach", "pinned@sha256:" + strings.Repeat("a", 64)}
	before := append([]string(nil), original...)
	want := append([]string{"run", "--pull=never"}, original[1:]...)
	if actual := storageRuntimeDockerArguments(original); !reflect.DeepEqual(actual, want) {
		t.Fatalf("run arguments=%q want=%q", actual, want)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("input arguments modified")
	}
	inspect := []string{"image", "inspect", "pinned"}
	if actual := storageRuntimeDockerArguments(inspect); !reflect.DeepEqual(actual, inspect) {
		t.Fatal("non-start command changed")
	}
}
