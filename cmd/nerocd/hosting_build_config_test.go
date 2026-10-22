package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostingBuildPolicyDisabledAndStrictFile(t *testing.T) {
	t.Setenv("NEROCD_HOSTING_BUILD_POLICY_FILE", "")
	p, err := loadHostingBuildPolicy()
	if err != nil || p != nil {
		t.Fatalf("default policy=%+v %v", p, err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	t.Setenv("NEROCD_HOSTING_BUILD_POLICY_FILE", path)
	for _, body := range []string{"null", "{}", `{"platform":"linux/amd64","unexpected":true}`, "{} {}", strings.Repeat(" ", 65537)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHostingBuildPolicy(); err == nil {
			t.Fatalf("invalid policy accepted %q", body[:min(len(body), 100)])
		}
	}
}

func TestHostingBuildDocumentedExplicitPolicyLoads(t *testing.T) {
	guide, err := os.ReadFile("../../docs/hosting-build-control.md")
	if err != nil {
		t.Fatal(err)
	}
	_, example, ok := strings.Cut(string(guide), "```json\n")
	if !ok {
		t.Fatal("documented policy missing")
	}
	example, _, ok = strings.Cut(example, "```")
	if !ok {
		t.Fatal("documented policy unterminated")
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err = os.WriteFile(path, []byte(example), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_HOSTING_BUILD_POLICY_FILE", path)
	p, err := loadHostingBuildPolicy()
	if err != nil || p == nil || p.Platform != "linux/amd64" || p.Limits.Deadline != 60000000000 {
		t.Fatalf("documented policy=%+v %v", p, err)
	}
}
