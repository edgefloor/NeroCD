package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestRunnerDatabaseConfigRequiresExplicitExecution(t *testing.T) {
	for _, tc := range []struct {
		enabled, execute        bool
		complete, state, secret string
		valid                   bool
	}{
		{false, false, "", "", "", true}, {false, true, "", "/var/lib/state", "", false}, {true, false, "", "/var/lib/state", "/var/lib/secret", false}, {true, true, "succeeded", "/var/lib/state", "/var/lib/secret", false}, {true, true, "", "", "/var/lib/secret", false}, {true, true, "", "/var/lib/state", "/var/lib/secret", true},
	} {
		_, err := runnerDatabaseConfig(tc.enabled, tc.execute, tc.complete, tc.state, tc.secret)
		if (err == nil) != tc.valid {
			t.Fatalf("config validation mismatch: %v", err)
		}
	}
}
func TestRunnerDatabaseCapabilitiesDefaultOffAndNoShell(t *testing.T) {
	if got, err := runnerDatabaseCapabilities(false, false, []string{"shell"}); err != nil || !slices.Equal(got, []string{"shell"}) {
		t.Fatal("ordinary capabilities changed", err)
	}
	for _, capability := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability} {
		if _, err := runnerDatabaseCapabilities(false, true, []string{capability}); err == nil {
			t.Fatal("accepted database capability without config")
		}
	}
	if _, err := runnerDatabaseCapabilities(true, true, []string{"shell"}); err == nil {
		t.Fatal("explicit shell accepted")
	}
	got, err := runnerDatabaseCapabilities(true, false, []string{"shell"})
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
		if !slices.Contains(got, capability) {
			t.Fatal("database prerequisite absent")
		}
	}
	if slices.Contains(got, "shell") {
		t.Fatal("database kept shell")
	}
}
func TestRunnerDatabaseCommandRejectsBeforeControllerEffects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	for _, args := range [][]string{
		{"--execute", "--capabilities", domain.HostingDatabaseCapability},
		{"--execute", "--complete-status", "succeeded", "--hosting-database", "--database-state-root", "/var/lib/database-state", "--database-secret-root", "/var/lib/database-secret"},
		{"--execute", "--hosting-database", "--database-state-root", "/var/lib/database-state", "--database-secret-root", "/var/lib/database-secret"},
	} {
		err := runRunner(append([]string{"--server", server.URL}, args...))
		if err == nil {
			t.Fatal("invalid database command succeeded")
		}
		if strings.Contains(err.Error(), "/var/lib/") {
			t.Fatal("protected roots escaped diagnostics")
		}
	}
	if requests != 0 {
		t.Fatal("invalid database command contacted controller")
	}
}
