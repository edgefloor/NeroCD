package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestCaptureArtifactsFindsFilesAndDirectories(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "out.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}

	var events []ProcessEvent
	results, err := CaptureArtifacts(base, []domain.ArtifactSpec{
		{Name: "output", Path: "out.txt", Required: true},
		{Name: "reports", Path: "reports", Required: false},
	}, func(event ProcessEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Found || results[0].Size == 0 || !results[1].IsDir {
		t.Fatalf("unexpected artifact results: %#v", results)
	}
	joined := artifactEvents(events)
	if !strings.Contains(joined, `Captured artifact "output"`) || !strings.Contains(joined, `Captured artifact "reports"`) {
		t.Fatalf("expected artifact capture events, got:\n%s", joined)
	}
}

func TestCaptureArtifactsFailsMissingRequired(t *testing.T) {
	var events []ProcessEvent
	results, err := CaptureArtifacts(t.TempDir(), []domain.ArtifactSpec{{Name: "required", Path: "missing.txt", Required: true}}, func(event ProcessEvent) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatal("expected missing required artifact error")
	}
	if len(results) != 1 || results[0].Found {
		t.Fatalf("unexpected missing artifact result: %#v", results)
	}
	if !strings.Contains(artifactEvents(events), `Required artifact "required" missing`) {
		t.Fatalf("expected missing artifact event, got %#v", events)
	}
}

func TestCaptureArtifactsRejectsUnsafePath(t *testing.T) {
	_, err := CaptureArtifacts(t.TempDir(), []domain.ArtifactSpec{{Name: "bad", Path: "../secret.txt", Required: true}}, func(ProcessEvent) {})
	if err == nil {
		t.Fatal("expected unsafe artifact path error")
	}
}

func artifactEvents(events []ProcessEvent) string {
	var out strings.Builder
	for _, event := range events {
		out.WriteString(event.Stream)
		out.WriteString(":")
		out.WriteString(event.Message)
		out.WriteString("\n")
	}
	return out.String()
}
