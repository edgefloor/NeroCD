package main

import "testing"

func TestHostingGitOperatorHosts(t *testing.T) {
	for _, raw := range []string{"", "github.com,gitlab.com", " GITHUB.com,github.com "} {
		t.Setenv("NEROCD_HOSTING_GIT_ALLOWED_HOSTS", raw)
		if _, err := loadHostingGitHosts(); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"localhost", "127.0.0.1", ".github.com", "https://github.com", "github.com:443", "github.com,", "github.com/internal", "*"} {
		t.Setenv("NEROCD_HOSTING_GIT_ALLOWED_HOSTS", raw)
		if _, err := loadHostingGitHosts(); err == nil {
			t.Fatal("unsafe operator host", raw)
		}
	}
}
