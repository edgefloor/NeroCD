package runnerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
)

func TestBackupRetentionS3ConditionalQualificationAndExactDeletion(t *testing.T) {
	for _, mode := range []string{"qualified", "nonversioned", "ignores_condition", "denied", "changed", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			etags := map[string]string{}
			backupDeletes := 0
			client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if mode != "nonversioned" {
					w.Header().Set("x-amz-version-id", "fixture-version-1")
				}
				key := r.URL.Path
				body, present := objects[key]
				if r.Method == "DELETE" && mode == "denied" {
					w.WriteHeader(403)
					io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
					return
				}
				switch r.Method {
				case "PUT":
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("unconditional creation")
					}
					if present {
						w.WriteHeader(412)
						return
					}
					data, e := io.ReadAll(io.LimitReader(r.Body, 4096))
					if e != nil {
						t.Error(e)
					}
					objects[key] = data
					sum := sha256.Sum256(data)
					etags[key] = fmt.Sprintf(`"%x"`, sum[:16])
					w.Header().Set("ETag", etags[key])
				case "HEAD", "GET":
					if !present {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("ETag", etags[key])
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
					if r.Method == "GET" {
						w.Write(body)
					}
				case "DELETE":
					match := r.Header.Get("If-Match")
					if match == "" || match == "*" {
						t.Error("unscoped delete")
					}
					if mode != "ignores_condition" && match != etags[key] {
						w.WriteHeader(412)
						io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
						return
					}
					if !strings.Contains(key, "retention-probes") {
						backupDeletes++
					}
					delete(objects, key)
					delete(etags, key)
					if mode == "uncertain" && !strings.Contains(key, "retention-probes") {
						w.WriteHeader(500)
						return
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected request")
				}
			})
			data := []byte("PGDMP saved database copy")
			sum := sha256.Sum256(data)
			blob := domain.HostingDatabaseBackupBlob{Digest: fmt.Sprintf("sha256:%x", sum), Bytes: int64(len(data)), Format: "postgresql_custom"}
			key := domain.BackupRemoteObjectKey("backup", blob)
			if e := client.upload(t.Context(), target.Bucket, key, blob, bytes.NewReader(data)); e != nil {
				t.Fatal(e)
			}
			if client.ownership == nil || !client.ownership.Created {
				t.Fatal("new creation proof absent")
			}
			proof := *client.ownership
			probe := "nerocd/postgresql/retention-probes/execution/0123456789abcdef0123456789abcdef"
			err := client.qualifyRetention(t.Context(), target.Bucket, probe)
			if mode == "ignores_condition" || mode == "denied" {
				if err == nil {
					t.Fatal("unsupported conditional delete accepted")
				}
				if backupDeletes != 0 {
					t.Fatal("qualification deleted selected copy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			copy := domain.BackupRetentionCopy{Item: domain.BackupRetentionItem{Digest: blob.Digest, Bytes: blob.Bytes}, Remote: &proof, Target: &domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: key}
			if mode == "changed" {
				mu.Lock()
				etags["/"+target.Bucket+"/"+key] = `"foreign"`
				mu.Unlock()
			}
			absent, err := client.deleteRetentionCopy(t.Context(), copy)
			if mode == "changed" {
				if err == nil || backupDeletes != 0 {
					t.Fatal("changed object deleted")
				}
				return
			}
			if mode == "uncertain" {
				if err == nil {
					t.Fatal("lost delete response claimed completed")
				}
				if absent, err = client.retentionHead(t.Context(), target.Bucket, key, proof, blob); err != nil || !absent {
					t.Fatal("exact absence reconciliation", err)
				}
				return
			}
			if err != nil || absent || backupDeletes != 1 {
				t.Fatalf("deletion: absent=%v deletes=%d err=%v", absent, backupDeletes, err)
			}
			if absent, err = client.deleteRetentionCopy(t.Context(), copy); err != nil || !absent || backupDeletes != 1 {
				t.Fatal("replay did not observe exact absence", err)
			}
		})
	}
}
func TestBackupRetentionS3AdoptionDoesNotGrantDeletion(t *testing.T) {
	data := []byte("PGDMP preexisting object")
	sum := sha256.Sum256(data)
	blob := domain.HostingDatabaseBackupBlob{Digest: fmt.Sprintf("sha256:%x", sum), Bytes: int64(len(data)), Format: "postgresql_custom"}
	deletes := 0
	client, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "HEAD":
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		case "GET":
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Write(data)
		case "DELETE":
			deletes++
		default:
			t.Error("adoption attempted publication")
		}
	})
	key := domain.BackupRemoteObjectKey("backup", blob)
	if err := client.upload(t.Context(), target.Bucket, key, blob, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if client.ownership != nil {
		t.Fatal("adopted remote acquired creation proof")
	}
	if _, err := client.deleteRetentionCopy(t.Context(), domain.BackupRetentionCopy{Item: domain.BackupRetentionItem{Digest: blob.Digest, Bytes: blob.Bytes}, Target: &domain.HostingDatabaseBackupTargetSnapshot{Target: target}, ObjectKey: key}); err == nil || deletes != 0 {
		t.Fatal("unproven deletion")
	}
}

func TestBackupRetentionDownloadedCopyProofDistinguishesCreationAndAdoption(t *testing.T) {
	e, _, p, data := remoteReplayPlan(t)
	key := strings.TrimPrefix(p.Operation.Blob.Digest, "sha256:")
	if err := e.root.Remove(key); err != nil {
		t.Fatal(err)
	}
	remote, target := remoteS3Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("download made non-read request")
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Write(data)
	})
	p.Target.Target = target
	p.Operation.Mode = "download"
	p.Operation.RestoreOperationID = "restore"
	if err := executeBackupRemoteObject(t.Context(), e, remote, p, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e.localCopy == nil || !e.localCopy.Created || e.localCopy.Bytes != int64(len(data)) {
		t.Fatal("fresh download publication proof missing")
	}
	first := *e.localCopy
	p.Operation.ID = "another_download"
	e.localCopy = nil
	if err := executeBackupRemoteObject(t.Context(), e, remote, p, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e.localCopy == nil || e.localCopy.Created || e.localCopy.Inode != first.Inode || e.localCopy.PoolID != first.PoolID {
		t.Fatal("matching existing file acquired new creation authority")
	}
}
