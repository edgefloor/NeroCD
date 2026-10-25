package store

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestBackupRetentionLocalIdentityIncludesStableServer(t *testing.T) {
	proof := domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 2, Bytes: 65537, Created: true}
	digest := "sha256:" + strings.Repeat("b", 64)
	first := backupRetentionLocalLocation("server_one", proof, digest)
	second := backupRetentionLocalLocation("server_two", proof, digest)
	if first == second {
		t.Fatal("coincident filesystem metadata crosses machines")
	}
	if first != backupRetentionLocalLocation("server_one", proof, digest) {
		t.Fatal("replacement runner on same server changed physical pool identity")
	}
}
