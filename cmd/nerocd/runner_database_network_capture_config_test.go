package main

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureExplicitFlags() map[string]bool {
	return map[string]bool{"database-network-capture-id": true, "credential-file": true, "journal-dir": true, "database-state-root": true, "database-secret-root": true}
}
func TestRunnerDatabaseNetworkCaptureFlags(t *testing.T) {
	id := "hnc_" + strings.Repeat("a", 32)
	if err := validateRunnerDatabaseCaptureFlags(id, true, "", captureExplicitFlags()); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"credential-file", "journal-dir", "database-state-root", "database-secret-root"} {
		flags := captureExplicitFlags()
		delete(flags, flag)
		if validateRunnerDatabaseCaptureFlags(id, true, "", flags) == nil {
			t.Fatalf("accepted missing explicit %s", flag)
		}
	}
	for _, flag := range []string{"token", "enrollment-file", "capabilities", "hosting-database", "hosting-readiness", "hosting-observation", "hosting-runtime-env", "storage-archive-config-file", "build-config-file", "local-build-deploy", "proxy-state-root", "complete-status"} {
		flags := maps.Clone(captureExplicitFlags())
		flags[flag] = true
		if validateRunnerDatabaseCaptureFlags(id, true, "", flags) == nil {
			t.Fatalf("accepted simultaneous %s", flag)
		}
	}
	for _, bad := range []string{"", "foreign", "hnc_" + strings.Repeat("A", 32), id + "/"} {
		if validateRunnerDatabaseCaptureFlags(bad, true, "", captureExplicitFlags()) == nil {
			t.Fatal("invalid ID accepted")
		}
	}
	if validateRunnerDatabaseCaptureFlags(id, false, "", captureExplicitFlags()) == nil || validateRunnerDatabaseCaptureFlags(id, true, "succeeded", captureExplicitFlags()) == nil {
		t.Fatal("generic mode accepted")
	}
}
func TestRunnerDatabaseNetworkCaptureCLIRejectsBeforeController(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	base := []string{"--database-network-capture-id", "hnc_" + strings.Repeat("a", 32), "--server", server.URL, "--credential-file", "/private/credential_canary", "--journal-dir", "/private/journal_canary", "--database-state-root", "/private/state_canary", "--database-secret-root", "/private/secret_canary"}
	for _, extra := range [][]string{nil, {"--hosting-database"}, {"--token", "registration_canary"}, {"--enrollment-file", "/private/enrollment"}, {"--complete-status", "succeeded"}, {"--execute=false"}, {"--capabilities", "shell"}} {
		err := runRunner(append(append([]string{}, base...), extra...))
		if err == nil {
			t.Fatal("invalid capture invocation accepted")
		}
		if strings.Contains(err.Error(), "canary") {
			t.Fatal("protected input leaked")
		}
	}
	if calls != 0 {
		t.Fatal("invalid capture contacted controller")
	}
}
