package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProductionRunnerOperatingGuardrailsFailClosed(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	workspace := filepath.Join(t.TempDir(), "runner-workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	workspace = resolvedWorkspace
	for _, endpoint := range []string{"http://nerocd.example.invalid", "https://user@nerocd.example.invalid", "https://nerocd.example.invalid/api", "https://nerocd.example.invalid?token=x"} {
		if err := validateRunnerOperatingGuardrails(endpoint, workspace); err == nil {
			t.Fatalf("validateRunnerOperatingGuardrails(%q) accepted unsafe endpoint", endpoint)
		}
	}
	if err := os.Chmod(workspace, 0750); err != nil {
		t.Fatal(err)
	}
	if err := validateRunnerOperatingGuardrails("https://nerocd.example.invalid", workspace); err == nil {
		t.Fatal("validateRunnerOperatingGuardrails accepted group-readable workspace")
	}
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateRunnerOperatingGuardrails("https://nerocd.example.invalid", workspace); err != nil {
		t.Fatalf("validateRunnerOperatingGuardrails(valid) error = %v", err)
	}
}

func TestDevelopmentRunnerOperatingGuardrailsPermitExplicitDevelopmentMode(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	if err := validateRunnerOperatingGuardrails("http://127.0.0.1:8080", t.TempDir()); err != nil {
		t.Fatalf("development guardrails error = %v", err)
	}
}

func TestUnsetRunnerModeFailsClosed(t *testing.T) {
	t.Setenv("NEROCD_MODE", "")
	if err := validateRunnerOperatingGuardrails("http://127.0.0.1:8080", t.TempDir()); err == nil {
		t.Fatal("unset mode accepted an insecure temporary runner")
	}
}
