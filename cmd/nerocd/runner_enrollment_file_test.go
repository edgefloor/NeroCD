//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRunnerEnrollmentFilesCreatesStableOwnerOnlyCredential(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	enrollmentPath := filepath.Join(dir, "enrollment")
	credentialPath := filepath.Join(dir, "credential")
	if err := os.WriteFile(enrollmentPath, []byte("nce_"+strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	enrollment, credential, requestID, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment != "nce_"+strings.Repeat("a", 64) || !strings.HasPrefix(credential, "ncr_") || len(credential) != 68 || !strings.HasPrefix(requestID, "enroll_consume_") {
		t.Fatalf("unexpected enrollment file result token_len=%d credential_shape=%t request=%q", len(enrollment), strings.HasPrefix(credential, "ncr_"), requestID)
	}
	info, err := os.Stat(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode=%04o", info.Mode().Perm())
	}
	_, replayCredential, replayRequestID, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
	if err != nil || replayCredential != credential || replayRequestID != requestID {
		t.Fatalf("pending replay credential/request changed: %v", err)
	}
	if err := removeRunnerEnrollmentFile(enrollmentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(enrollmentPath); !os.IsNotExist(err) {
		t.Fatalf("enrollment file still exists: %v", err)
	}
}

func TestPrepareRunnerEnrollmentFilesFailsClosedOnUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	safe := filepath.Join(dir, "safe")
	if err := os.WriteFile(safe, []byte("nce_"+strings.Repeat("b", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(safe, symlink); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareRunnerEnrollmentFiles(symlink, filepath.Join(dir, "credential-one")); err == nil {
		t.Fatal("symlink enrollment unexpectedly accepted")
	}
	if err := os.Chmod(safe, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareRunnerEnrollmentFiles(safe, filepath.Join(dir, "credential-two")); err == nil {
		t.Fatal("permissive enrollment unexpectedly accepted")
	}
	if err := os.Chmod(safe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareRunnerEnrollmentFiles(safe, filepath.Join(dir, "credential-three")); err == nil {
		t.Fatal("permissive credential directory unexpectedly accepted")
	}
}
