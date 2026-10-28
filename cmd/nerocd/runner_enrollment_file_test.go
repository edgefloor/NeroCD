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
	files, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.close)
	enrollment, credential, requestID := files.token, files.credential, files.requestID
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
	replayed, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replayed.close)
	if replayed.credential != credential || replayed.requestID != requestID {
		t.Fatalf("pending replay credential/request changed: %v", err)
	}
	if err := files.remove(); err != nil {
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
	if _, err := prepareRunnerEnrollmentFiles(symlink, filepath.Join(dir, "credential-one")); err == nil {
		t.Fatal("symlink enrollment unexpectedly accepted")
	}
	if err := os.Chmod(safe, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRunnerEnrollmentFiles(safe, filepath.Join(dir, "credential-two")); err == nil {
		t.Fatal("permissive enrollment unexpectedly accepted")
	}
	if err := os.Chmod(safe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRunnerEnrollmentFiles(safe, filepath.Join(dir, "credential-three")); err == nil {
		t.Fatal("permissive credential directory unexpectedly accepted")
	}
}

func TestEnrollmentRemovalRetainsConsumedFileAndParent(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink", "file", "file symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root := privateBackupTestDir(t)
			parent := filepath.Join(root, "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "enrollment")
			original := "original enrollment token\n"
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := prepareRunnerEnrollmentFiles(path, filepath.Join(root, "credential"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(files.close)
			var movedPath string
			switch replacement {
			case "file", "file symlink":
				movedPath = path + ".original"
				if err := os.Rename(path, movedPath); err != nil {
					t.Fatal(err)
				}
			default:
				moved := parent + "-original"
				if err := os.Rename(parent, moved); err != nil {
					t.Fatal(err)
				}
				movedPath = filepath.Join(moved, "enrollment")
				target := parent
				if replacement == "symlink" {
					target += "-replacement"
				}
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if replacement == "symlink" {
					if err := os.Symlink(target, parent); err != nil {
						t.Fatal(err)
					}
				}
			}
			victim := path
			if replacement == "file symlink" {
				victim = filepath.Join(root, "victim")
			}
			if err := os.WriteFile(victim, []byte("unrelated private file"), 0600); err != nil {
				t.Fatal(err)
			}
			if replacement == "file symlink" {
				if err := os.Symlink(victim, path); err != nil {
					t.Fatal(err)
				}
			}
			err = files.remove()
			if replacement == "file" || replacement == "file symlink" {
				if err == nil {
					t.Fatal("replacement entry was unlinked")
				}
				wiped, err := os.ReadFile(movedPath)
				if err != nil || string(wiped) != strings.Repeat("\x00", len(original)) {
					t.Fatalf("consumed file was not wiped: %q, %v", wiped, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(movedPath); !os.IsNotExist(err) {
					t.Fatalf("original entry remains: %v", err)
				}
			}
			raw, err := os.ReadFile(victim)
			if err != nil || string(raw) != "unrelated private file" {
				t.Fatalf("replacement was modified: %q, %v", raw, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("replacement entry was removed: %v", err)
			}
		})
	}
}

func TestEnrollmentRejectsHardlinkedInput(t *testing.T) {
	root := privateBackupTestDir(t)
	path := filepath.Join(root, "enrollment")
	if err := os.WriteFile(path, []byte("private enrollment"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(root, "other")); err != nil {
		t.Fatal(err)
	}
	if files, err := prepareRunnerEnrollmentFiles(path, filepath.Join(root, "credential")); err == nil {
		files.close()
		t.Fatal("enrollment with multiple hard links was accepted")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "private enrollment" {
		t.Fatalf("input changed: %q, %v", raw, err)
	}
}

func TestEnrollmentAllowsOwnerWritableReadableParent(t *testing.T) {
	root := privateBackupTestDir(t)
	parent := filepath.Join(root, "enrollment-input")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "enrollment")
	if err := os.WriteFile(path, []byte("private enrollment"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := prepareRunnerEnrollmentFiles(path, filepath.Join(root, "credential"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.close)
	if err := files.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed entry remains: %v", err)
	}
}
