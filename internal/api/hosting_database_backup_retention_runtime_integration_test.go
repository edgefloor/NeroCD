package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// Private projections keep this fixture compilable at its frozen base while
// the independently reviewed production contracts are integrated separately.
type databaseRetentionLimits struct {
	KeepCount int   `json:"keep_count"`
	KeepDays  int   `json:"keep_days"`
	MaxBytes  int64 `json:"max_bytes"`
}
type databaseRetentionItem struct {
	CopyID           string   `json:"copy_id"`
	BackupID         string   `json:"backup_id"`
	TransferID       string   `json:"transfer_id"`
	Location         string   `json:"location"`
	Digest           string   `json:"digest"`
	Bytes            int64    `json:"bytes"`
	Selected         bool     `json:"selected"`
	Availability     string   `json:"availability"`
	ProtectedReasons []string `json:"protected_reasons"`
}
type databaseRetentionPreview struct {
	ID                 string                  `json:"id"`
	PolicyRevision     int64                   `json:"policy_revision"`
	PlanHash           string                  `json:"plan_hash"`
	Items              []databaseRetentionItem `json:"items"`
	SelectionTruncated bool                    `json:"selection_truncated"`
}
type databaseRetentionExecutionItem struct {
	CopyID          string `json:"copy_id"`
	BackupID        string `json:"backup_id"`
	Location        string `json:"location"`
	State           string `json:"state"`
	FailureCode     string `json:"failure_code"`
	ReservationHeld bool   `json:"reservation_held"`
}
type databaseRetentionExecution struct {
	ID              string                           `json:"id"`
	State           string                           `json:"state"`
	ReservationHeld bool                             `json:"reservation_held"`
	LocalState      string                           `json:"local_state"`
	RemoteState     string                           `json:"remote_state"`
	Items           []databaseRetentionExecutionItem `json:"items"`
}

type databaseRetentionMutation struct {
	ProbeOnly bool   `json:"probe_only,omitempty"`
	ProbeKey  string `json:"probe_key,omitempty"`
	domain.HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	CopyID      string `json:"copy_id"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
}

type databaseRetentionWire struct {
	mu               sync.Mutex
	observeOnly      bool
	pausePlan        bool
	first            bool
	entered, release chan struct{}
	body, response   [32]byte
	replays          int
	copy             string
	terminals        map[string]int
	intents          []databaseRetentionMutation
	committed        databaseRetentionMutation
}

func (w *databaseRetentionWire) serve(out http.ResponseWriter, r *http.Request, handler http.Handler) bool {
	if r.Method != "POST" || r.URL.Path != "/api/v1/runners/hosting/database-backup-retention/mutation" && r.URL.Path != "/api/v1/runners/hosting/database-backup-retention/plan" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(raw) > 8192 {
		out.WriteHeader(400)
		return true
	}
	defer clear(raw)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var in databaseRetentionMutation
	_ = json.Unmarshal(raw, &in)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	if rec.Code != 200 {
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	// Qualification probes never count as archive intents or deletion receipts.
	if in.ProbeOnly {
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	w.mu.Lock()
	if in.Phase == "intent" {
		seen := false
		for _, saved := range w.intents {
			if saved == in {
				seen = true
				break
			}
		}
		if !seen {
			if len(w.intents) >= 100 {
				w.mu.Unlock()
				out.WriteHeader(409)
				return true
			}
			w.intents = append(w.intents, in)
		}
	}
	if w.observeOnly {
		if in.Phase == "deleted" || in.Phase == "absent" {
			if w.terminals == nil {
				w.terminals = map[string]int{}
			}
			w.terminals[in.CopyID]++
		}
		w.mu.Unlock()
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	if w.pausePlan {
		if strings.HasSuffix(r.URL.Path, "/plan") && !w.first {
			w.first = true
			close(w.entered)
			w.mu.Unlock()
			select {
			case <-w.release:
			case <-r.Context().Done():
				return true
			}
			databaseRemoteCopyResponse(out, rec)
			return true
		}
		w.mu.Unlock()
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	if !slices.Contains([]string{"deleted", "absent"}, in.Phase) {
		w.mu.Unlock()
		databaseRemoteCopyResponse(out, rec)
		return true
	}
	if w.terminals == nil {
		w.terminals = map[string]int{}
	}
	w.terminals[in.CopyID]++
	body, response := sha256.Sum256(raw), sha256.Sum256(rec.Body.Bytes())
	if !w.first {
		w.first = true
		w.copy = in.CopyID
		w.committed = in
		w.body, w.response = body, response
		close(w.entered)
		w.mu.Unlock()
		select {
		case <-w.release:
		case <-r.Context().Done():
		}
		return true
	}
	if in.CopyID != w.copy || body != w.body || response != w.response {
		w.mu.Unlock()
		out.WriteHeader(409)
		return true
	}
	w.replays++
	w.mu.Unlock()
	databaseRemoteCopyResponse(out, rec)
	return true
}

type databaseRetentionFixture struct {
	f                     *databaseRunnerFixture
	silo                  *databaseRemoteSilo
	source                domain.HostingDatabase
	application           domain.HostingOperation
	targetID              string
	revision              int64
	backups               []domain.HostingDatabaseBackupOperation
	uploads               []databaseRemoteTransfer
	archiveDeleteRequests atomic.Int64
	deleteFault           *databaseRetentionDeleteFault
}

func newDatabaseRetentionFixture(t *testing.T, faultMode ...string) *databaseRetentionFixture {
	t.Helper()
	if os.Getenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME") != "1" || os.Getenv("NEROCD_DATABASE_BACKUP_RUNTIME") != "1" || os.Getenv("NEROCD_HOSTING_DATABASE_RUNNER_RUNTIME") != "disposable-dind" {
		t.Skip("requires exact authorized retention runtime profile")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("native Linux arm64 required")
	}
	mysql := os.Getenv("NEROCD_DATABASE_MYSQL_RUNTIME") == "1"
	f := newDatabaseRunnerFixtureWithProfile(t, true, true, true, mysql)
	x := &databaseRetentionFixture{f: f, silo: startDatabaseRemoteSilo(f)}
	x.silo.policy(true)
	origin := "http://127.0.0.1:9000"
	mode := ""
	if len(faultMode) > 0 {
		mode = faultMode[0]
	}
	if mode == "conditional_unsupported" || mode == "unknown_delete" || mode == "delete_denied" {
		if mode == "unknown_delete" {
			x.deleteFault = &databaseRetentionDeleteFault{}
		}
		target, _ := url.Parse(origin)
		proxy := httputil.NewSingleHostReverseProxy(target)
		transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Second}
		proxy.Transport = transport
		t.Cleanup(transport.CloseIdleConnections)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
		listener, err := net.Listen("tcp", "127.0.0.1:9002")
		if err != nil {
			t.Fatal("owned conditional fault listener unavailable")
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if retentionArchiveDelete(r) {
				x.archiveDeleteRequests.Add(1)
			}
			if mode == "conditional_unsupported" && retentionConditionalFault(r) {
				w.WriteHeader(204)
				return
			}
			if x.deleteFault != nil && x.deleteFault.serve(w, r, proxy) {
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		_ = server.Listener.Close()
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		origin = "http://127.0.0.1:9002"
		f.remoteBackupFault = true
		f.stop()
		f.start()
	}
	create := backupRuntimeCreateInput(t, f.environment, f.destination)
	if mysql {
		create = applicationDatabaseCreateInput("mysql", f.environment, f.destination)
		create.RequestID = backupRuntimeRequestID(t)
	}
	x.source = databaseRunnerJSON[domain.HostingDatabase](f, f.token, "POST", "/api/v1/databases", create, 201)
	f.registerRouteDatabaseCleanup(x.source.Resource.ID)
	deployed := f.deploy(x.source, "retention_source")
	f.registerRouteOperationCleanup(deployed.ID)
	f.await(deployed.ID)
	x.application = backupRuntimeApplication(f, x.source, "retention_source")
	if mysql {
		resource := databaseRunnerJSON[domain.Resource](f, f.token, "GET", "/api/v1/resources/"+x.application.ResourceID, nil, 200)
		x.application = f.storage.bootstrap(f, resource, x.application, domain.HostingDeploymentInput{ExpectedGeneration: resource.Generation, ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 30}})
		f.appRecord(x.application, "PUT", "a", "retained_a")
		roots, public := newApplicationDatabaseRouteTrust(t)
		f.initialApplicationDatabaseRoute(x.application, "mysql", roots, public)
		f.storage.write(f, x.application, "retained_a", 0x41)
	}
	f.appRecord(x.application, "PUT", "before", "retention_origin")
	target := databaseRunnerJSON[struct {
		ID       string `json:"id"`
		Revision int64  `json:"revision"`
	}](f, f.token, "POST", "/api/v1/projects/"+x.source.Resource.ProjectID+"/database-backup-targets", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": 0, "name": "Retention S3", "endpoint": origin, "bucket": "nerocd-runtime", "region": "us-east-1", "enabled": true, "credentials": map[string]string{"access_key": x.silo.access, "secret_key": x.silo.secret}}, 200)
	x.targetID = target.ID
	x.revision = target.Revision
	v := databaseRunnerJSON[databaseRemoteTransfer](f, f.token, "POST", "/api/v1/projects/"+x.source.Resource.ProjectID+"/database-backup-targets/"+target.ID+"/validations", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_revision": target.Revision, "destination_id": f.destination}, 202)
	databaseRemoteAwaitValidation(f, v)
	return x
}
func (x *databaseRetentionFixture) assertSourcePreserved() {
	x.f.t.Helper()
	x.f.appRecord(x.application, "GET", "before", "retention_origin")
	x.f.appRecord(x.application, "PUT", "after_retention", "retention_source_write")
	x.f.appRecord(x.application, "GET", "after_retention", "retention_source_write")
	if x.source.Engine == "mysql" {
		x.f.routes.assertSelected(x.application)
		x.f.routes.record(x.f.t.Context(), x.application, "GET", "after_retention", "retention_source_write")
		x.f.storage.assertFiles(x.f, x.application)
	}
}

func (x *databaseRetentionFixture) path() string {
	return "/api/v1/resources/" + x.source.Resource.ID + "/database-backup-retention"
}
func (x *databaseRetentionFixture) admitBackup() domain.HostingDatabaseBackupOperation {
	return databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", "/api/v1/resources/"+x.source.Resource.ID+"/database-backups", map[string]any{"request_id": backupRuntimeRequestID(x.f.t), "expected_resource_generation": x.source.Resource.Generation, "remote_target": map[string]any{"target_id": x.targetID, "revision": x.revision}}, 202)
}
func (x *databaseRetentionFixture) backup(suffix string, uploadState string) domain.HostingDatabaseBackupOperation {
	x.f.appRecord(x.application, "PUT", suffix, "retention_"+suffix)
	admitted := x.admitBackup()
	settled := backupRuntimeAwait(x.f, admitted.ID)
	upload := retentionAwaitUpload(x.f, admitted.ID, uploadState)
	if upload.Blob == nil || *upload.Blob != *settled.Blob {
		x.f.t.Fatal("retention upload changed archive identity")
	}
	x.backups = append(x.backups, settled)
	x.uploads = append(x.uploads, upload)
	return settled
}
func (x *databaseRetentionFixture) policy(local, remote databaseRetentionLimits) int64 {
	out := databaseRunnerJSON[struct {
		Revision int64 `json:"revision"`
	}](x.f, x.f.token, "PUT", x.path(), map[string]any{"request_id": backupRuntimeRequestID(x.f.t), "expected_revision": x.revisionPolicy(), "expected_resource_generation": x.source.Resource.Generation, "enabled": true, "automatic": false, "local": local, "remote": remote}, 200)
	return out.Revision
}
func (x *databaseRetentionFixture) revisionPolicy() int64 {
	v := databaseRunnerJSON[*struct {
		Revision int64 `json:"revision"`
	}](x.f, x.f.token, "GET", x.path(), nil, 200)
	if v == nil {
		return 0
	}
	return v.Revision
}
func (x *databaseRetentionFixture) preview() databaseRetentionPreview {
	return databaseRunnerJSON[databaseRetentionPreview](x.f, x.f.token, "POST", x.path()+"/previews", map[string]any{"request_id": backupRuntimeRequestID(x.f.t), "expected_policy_revision": x.revisionPolicy()}, 200)
}
func (x *databaseRetentionFixture) execute(p databaseRetentionPreview, token string, status int) databaseRetentionExecution {
	return databaseRunnerJSON[databaseRetentionExecution](x.f, token, "POST", x.path()+"/executions", map[string]any{"request_id": backupRuntimeRequestID(x.f.t), "preview_id": p.ID, "expected_policy_revision": p.PolicyRevision, "expected_plan_hash": p.PlanHash}, status)
}
func (x *databaseRetentionFixture) execution(id string) databaseRetentionExecution {
	return databaseRunnerJSON[databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions/"+id, nil, 200)
}
func (x *databaseRetentionFixture) await(id string) databaseRetentionExecution {
	ctx, cancel := context.WithTimeout(x.f.t.Context(), 90*time.Second)
	defer cancel()
	for {
		v := x.execution(id)
		if slices.Contains([]string{"succeeded", "failed", "uncertain"}, v.State) {
			return v
		}
		if x.f.failureRunnerStatus().Exited {
			x.f.t.Fatal("retention runner exited before settlement")
		}
		select {
		case <-ctx.Done():
			x.f.t.Fatal("retention execution did not settle")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func retentionSelected(p databaseRetentionPreview) []string {
	var out []string
	for _, v := range p.Items {
		if v.Selected {
			out = append(out, v.CopyID)
		}
	}
	slices.Sort(out)
	return out
}
func retentionExactSelection(p databaseRetentionPreview, want []string) bool {
	slices.Sort(want)
	return !p.SelectionTruncated && slices.Equal(retentionSelected(p), want)
}
func (x *databaseRetentionFixture) deletePolicy(allow bool) {
	resources := []string{"arn:aws:s3:::nerocd-runtime/nerocd/" + x.source.Engine + "/retention-probes/*"}
	for _, b := range x.backups {
		resources = append(resources, "arn:aws:s3:::nerocd-runtime/"+retentionObjectKey(b))
	}
	statements := []any{map[string]any{"Effect": "Allow", "Action": []string{"s3:ListBucket"}, "Resource": []string{"arn:aws:s3:::nerocd-runtime"}}, map[string]any{"Effect": "Allow", "Action": []string{"s3:PutObject", "s3:GetObject"}, "Resource": []string{"arn:aws:s3:::nerocd-runtime/nerocd/" + x.source.Engine + "/*"}}}
	if allow {
		statements = append(statements, map[string]any{"Effect": "Allow", "Action": []string{"s3:DeleteObject"}, "Resource": resources})
	}
	raw, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	x.silo.command("cat >/tmp/fixture-policy.json <<'POLICY'\n" + string(raw) + "\nPOLICY\nmc admin policy create fixture fixture-backup /tmp/fixture-policy.json >/dev/null\n")
}
func retentionObjectKey(b domain.HostingDatabaseBackupOperation) string {
	if b.Blob == nil || b.Validate() != nil {
		panic("invalid retained archive identity")
	}
	return domain.BackupRemoteObjectKey(b.ID, *b.Blob)
}

// Observe the owned Silo directly, outside runner fault proxies. Only a complete,
// successful bounded inventory can prove absence; request failures prove nothing.
func databaseRetentionObservationClient(access, secret string, client s3.HTTPClient) *s3.Client {
	return s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("http://127.0.0.1:9000"), UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: access, SecretAccessKey: secret}, nil
		}),
		HTTPClient: databaseRetentionObservationBoundedHTTP{client}, RetryMaxAttempts: 1,
	})
}

type databaseRetentionObservationBoundedHTTP struct{ client s3.HTTPClient }

func (c databaseRetentionObservationBoundedHTTP) Do(r *http.Request) (*http.Response, error) {
	response, err := c.client.Do(r)
	if err != nil {
		return nil, errors.New("remote observation request failed")
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("remote observation response absent")
	}
	// Finish reading before SDK decoding: its XML decoder can stop at the first
	// root and ignore errors encountered while draining the remaining body.
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > 64<<10 || !databaseRetentionObservationDocument(raw) {
		clear(raw)
		return nil, errors.New("remote observation document invalid")
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	return response, nil
}

func databaseRetentionObservationDocument(raw []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	seenRoot, depth := false, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return seenRoot && depth == 0
		}
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenRoot || token.Name.Local != "ListBucketResult" ||
					(token.Name.Space != "" && token.Name.Space != "http://s3.amazonaws.com/doc/2006-03-01/") {
					return false
				}
				seenRoot = true
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(token)) != 0 {
				return false
			}
		case xml.Directive:
			return false
		}
	}
}

func databaseRetentionObserveRemote(ctx context.Context, client *s3.Client, key string) string {
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String("nerocd-runtime"), Prefix: aws.String(key), MaxKeys: aws.Int32(2),
	})
	if err != nil || out == nil || out.Name == nil || *out.Name != "nerocd-runtime" ||
		out.Prefix == nil || *out.Prefix != key || out.IsTruncated == nil || *out.IsTruncated ||
		out.KeyCount == nil || *out.KeyCount != int32(len(out.Contents)) || len(out.Contents) > 2 || len(out.CommonPrefixes) != 0 {
		return "unknown"
	}
	observation := "missing"
	for _, object := range out.Contents {
		if object.Key == nil || !strings.HasPrefix(*object.Key, key) {
			return "unknown"
		}
		if *object.Key == key {
			observation = "present"
		}
	}
	return observation
}

func (x *databaseRetentionFixture) assertCopies(p databaseRetentionPreview, deleted map[string]bool) {
	for _, item := range p.Items {
		known := false
		var archive domain.HostingDatabaseBackupOperation
		for _, b := range x.backups {
			if b.ID == item.BackupID && b.Blob != nil && b.Blob.Digest == item.Digest && b.Blob.Bytes == item.Bytes && b.Validate() == nil {
				known = true
				archive = b
				break
			}
		}
		if !known {
			x.f.t.Fatal("copy inventory escaped the fixture archive identities")
		}
		if item.Location != "local" && item.Location != "remote" {
			x.f.t.Fatal("copy inventory has unknown location")
		}
		if item.Location == "local" {
			_, err := os.Stat(filepath.Join(x.f.root, "state", "logical-backups", strings.TrimPrefix(item.Digest, "sha256:")))
			if deleted[item.CopyID] {
				if !os.IsNotExist(err) {
					x.f.t.Fatal("selected local archive survived deletion")
				}
			} else if err != nil {
				x.f.t.Fatal("unselected local archive disappeared")
			} else {
				raw, err := os.ReadFile(filepath.Join(x.f.root, "state", "logical-backups", strings.TrimPrefix(item.Digest, "sha256:")))
				sum := sha256.Sum256(raw)
				clear(raw)
				if err != nil || fmt.Sprintf("sha256:%x", sum) != item.Digest {
					x.f.t.Fatal("unselected local archive content changed")
				}
			}
		} else {
			key := retentionObjectKey(archive)
			transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second}
			client := databaseRetentionObservationClient(x.silo.access, x.silo.secret, &http.Client{
				Transport: transport, Timeout: 3 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("observation redirect denied") },
			})
			ctx, cancel := context.WithTimeout(x.f.t.Context(), 3*time.Second)
			observation := databaseRetentionObserveRemote(ctx, client, key)
			cancel()
			transport.CloseIdleConnections()
			if observation == "unknown" {
				x.f.t.Fatal("remote archive observation unavailable")
			}
			if deleted[item.CopyID] && observation != "missing" || !deleted[item.CopyID] && observation != "present" {
				x.f.t.Fatal("remote archive availability differs from exact retention batch")
			}
			if !deleted[item.CopyID] {
				for _, b := range x.backups {
					if b.ID == item.BackupID {
						x.silo.verify(b)
						break
					}
				}
			}
		}
	}
}
func (x *databaseRetentionFixture) restoreApplication(backup domain.HostingDatabaseBackupOperation, restoreID, suffix string) (domain.HostingDatabase, domain.HostingOperation) {
	restored := backupRuntimeAwait(x.f, restoreID)
	if restored.BackupID != backup.ID || restored.Blob == nil || backup.Blob == nil || *restored.Blob != *backup.Blob {
		x.f.t.Fatal("retention restore changed immutable archive identity")
	}
	fresh := databaseRunnerJSON[domain.HostingDatabase](x.f, x.f.token, "GET", "/api/v1/resources/"+restored.TargetResourceID+"/database", nil, 200)
	x.f.registerRouteDatabaseCleanup(fresh.Resource.ID)
	if fresh.Engine != x.source.Engine || fresh.RecipeVersion != x.source.RecipeVersion || fresh.Binding.ID == x.source.Binding.ID || fresh.Resource.ID == x.source.Resource.ID || fresh.StorageRef == x.source.StorageRef || fresh.Credentials[0].SecretID == x.source.Credentials[0].SecretID {
		x.f.t.Fatal("retention restore reused source identity")
	}
	app := backupRuntimeApplication(x.f, fresh, suffix)
	if app.ResourceID == x.application.ResourceID {
		x.f.t.Fatal("retention restore reused application")
	}
	x.f.appRecord(app, "GET", "before", "retention_origin")
	x.f.appRecord(app, "PUT", "restored_new", "retention_new_write")
	x.f.appRecord(app, "GET", "restored_new", "retention_new_write")
	if x.source.Engine == "mysql" {
		var credential domain.HostingDatabaseCredentialRef
		for _, ref := range fresh.Credentials {
			if ref.Role == "app" {
				credential = ref
			}
		}
		if app.ApplicationDatabase == nil || app.ApplicationDatabase.Binding.DatabaseResourceID != fresh.Resource.ID || app.ApplicationDatabase.Binding.DatabaseBindingID != fresh.Binding.ID || app.ApplicationDatabase.Binding.Alias != domain.HostingDatabaseBindingAlias(fresh.Binding.ID) || app.ApplicationDatabase.Binding.Credential.SecretID != credential.SecretID || app.ApplicationDatabase.Binding.Credential.Version != credential.Version {
			x.f.t.Fatal("retention target application lost exact binding snapshot")
		}
		if databaseRemoteMissingRecord(x.f.t.Context(), x.application, "restored_new") != nil {
			x.f.t.Fatal("retention target write reached source")
		}
	}
	return fresh, app
}

// Public previews omit deleted inventory in the current contract. If a future
// response includes it, it must still retain its deleted availability.
func retentionRecoveredLocalCopy(p databaseRetentionPreview, backup domain.HostingDatabaseBackupOperation, original, remote string) (string, bool) {
	if backup.Blob == nil {
		return "", false
	}
	localID, remoteCount, localCount := "", 0, 0
	for _, item := range p.Items {
		if item.BackupID != backup.ID {
			continue
		}
		if item.Digest != backup.Blob.Digest || item.Bytes != backup.Blob.Bytes {
			return "", false
		}
		if item.CopyID == original && item.Availability != "deleted" {
			return "", false
		}
		if item.Availability == "deleted" {
			continue
		}
		switch item.Location {
		case "local":
			if item.CopyID == "" || item.CopyID == original || item.Availability != "available" {
				return "", false
			}
			localID = item.CopyID
			localCount++
		case "remote":
			if item.CopyID != remote || item.Availability != "available" {
				return "", false
			}
			remoteCount++
		default:
			return "", false
		}
	}
	return localID, localCount == 1 && remoteCount == 1
}

func retentionSameInventory(a, b []databaseRetentionItem) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]databaseRetentionItem, len(a))
	for _, item := range a {
		if _, exists := byID[item.CopyID]; exists {
			return false
		}
		byID[item.CopyID] = item
	}
	for _, item := range b {
		prior, exists := byID[item.CopyID]
		if !exists || prior.BackupID != item.BackupID || prior.TransferID != item.TransferID || prior.Location != item.Location || prior.Digest != item.Digest || prior.Bytes != item.Bytes || prior.Selected != item.Selected || prior.Availability != item.Availability || !slices.Equal(prior.ProtectedReasons, item.ProtectedReasons) {
			return false
		}
		delete(byID, item.CopyID)
	}
	return len(byID) == 0
}

func retentionHistoryPreserved(history []databaseRetentionExecution, prior databaseRetentionExecution) bool {
	for _, current := range history {
		if current.ID == prior.ID {
			return current.State == prior.State && current.ReservationHeld == prior.ReservationHeld && current.LocalState == prior.LocalState && current.RemoteState == prior.RemoteState && slices.Equal(current.Items, prior.Items)
		}
	}
	return false
}

func TestHostingDatabaseBackupRetentionCompiledRunner(t *testing.T) {
	x := newDatabaseRetentionFixture(t)
	for _, name := range []string{"one", "two", "three", "four"} {
		x.backup(name, "succeeded")
	}
	x.deletePolicy(true)
	browser := startDatabaseBackupBrowserProfile(x.f, x.source, "backup_retention")
	browser.setPhase("retention_ready", "")
	var preview databaseRetentionPreview
	select {
	case preview = <-browser.retentionPreviews:
	case <-time.After(120 * time.Second):
		t.Fatal("real retention UI did not preview")
	}
	// UI chooses independent local keep=2 and remote keep=1.
	want := []string{"dbc_local_" + x.backups[0].ID, "dbc_local_" + x.backups[1].ID}
	for _, u := range x.uploads[:3] {
		want = append(want, "dbc_remote_"+u.ID)
	}
	if !retentionExactSelection(preview, want) {
		t.Fatal("public preview selected different local or remote copies")
	}
	browser.setPhase("retention_execute", preview.ID)
	var execution databaseRetentionExecution
	select {
	case execution = <-browser.retentions:
	case <-time.After(120 * time.Second):
		t.Fatal("real retention UI did not confirm exact preview")
	}
	settled := x.await(execution.ID)
	if settled.State != "succeeded" || settled.ReservationHeld || settled.LocalState != "succeeded" || settled.RemoteState != "succeeded" {
		t.Fatal("real conditional retention did not settle independently")
	}
	if len(settled.Items) != len(want) {
		t.Fatal("execution changed selected copy count")
	}
	deleted := map[string]bool{}
	for _, v := range settled.Items {
		if !slices.Contains(want, v.CopyID) || v.State != "deleted" && v.State != "absent" || v.ReservationHeld {
			t.Fatal("execution changed selected batch")
		}
		deleted[v.CopyID] = true
	}
	if len(deleted) != len(want) {
		t.Fatal("retention omitted selected copies")
	}
	x.assertCopies(preview, deleted)
	// No automatic next batch is authorized by this manual policy.
	history := databaseRunnerJSON[[]databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions", nil, 200)
	if len(history) != 1 {
		t.Fatal("manual retention expanded into another batch")
	}
	browser.setPhase("complete", execution.ID)
	browser.finished()
	// Admit one newer local-only backup, then delete the survivor's original
	// local copy through retention. Its remote archive remains available.
	x.f.appRecord(x.application, "PUT", "local_newest", "retention_local_newest")
	newest := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", "/api/v1/resources/"+x.source.Resource.ID+"/database-backups", map[string]any{"request_id": backupRuntimeRequestID(t), "expected_resource_generation": x.source.Resource.Generation}, 202)
	x.backups = append(x.backups, backupRuntimeAwait(x.f, newest.ID))
	if transfers := databaseRunnerJSON[[]databaseRemoteTransfer](x.f, x.f.token, "GET", "/api/v1/database-backups/"+newest.ID+"/transfers", nil, 200); len(transfers) != 0 {
		t.Fatal("local-only backup unexpectedly admitted remote work")
	}
	x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{})
	localPreview := x.preview()
	localWant := []string{"dbc_local_" + x.backups[2].ID, "dbc_local_" + x.backups[3].ID}
	if !retentionExactSelection(localPreview, localWant) {
		t.Fatal("local-only retention changed the exact selected copies")
	}
	localExecution := x.execute(localPreview, x.f.token, 202)
	localSettled := x.await(localExecution.ID)
	if localSettled.State != "succeeded" || localSettled.ReservationHeld || len(localSettled.Items) != len(localWant) {
		t.Fatal("local-only retention did not settle the exact batch")
	}
	localDeleted := map[string]bool{}
	for _, item := range localSettled.Items {
		if !slices.Contains(localWant, item.CopyID) || item.Location != "local" || item.State != "deleted" && item.State != "absent" || item.ReservationHeld {
			t.Fatal("local-only retention changed selected copy results")
		}
		localDeleted[item.CopyID] = true
	}
	if len(localDeleted) != len(localWant) {
		t.Fatal("local-only retention omitted a selected copy")
	}
	x.assertCopies(localPreview, localDeleted)

	survivor, upload := x.backups[3], x.uploads[3]
	restorePath := "/api/v1/database-backups/" + survivor.ID + "/restores"
	remoteRequest := map[string]any{"request_id": backupRuntimeRequestID(t), "name": "Retention remote survivor", "remote_source": map[string]any{"transfer_id": upload.ID, "expected_target_revision": upload.TargetRevision}}
	survivorCAS := filepath.Join(x.f.root, "state", "logical-backups", strings.TrimPrefix(survivor.Blob.Digest, "sha256:"))
	if _, err := os.Lstat(survivorCAS); !os.IsNotExist(err) {
		t.Fatal("retention survivor local CAS remains before remote recovery")
	}
	remoteAdmission := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", restorePath, remoteRequest, 202)
	remoteDatabase, remoteApp := x.restoreApplication(survivor, remoteAdmission.ID, "retention_remote_survivor")
	if x.source.Engine == "mysql" {
		if databaseRemoteMissingRecord(t.Context(), remoteApp, "local_newest") != nil {
			t.Fatal("remote survivor imported post-snapshot source record")
		}
		route := &applicationDatabaseRouteFixture{f: x.f, roots: x.f.routes.roots, publicRoot: x.f.routes.publicRoot, proxy: x.f.routes.proxy, hostname: "restored-mysql.example.test", daemonID: remoteApp.Release.DaemonID}
		databaseRunnerJSON[domain.HostingHostnameGrant](x.f, x.f.adminToken, "POST", "/api/v1/servers/"+x.f.serverID+"/proxy/hostname-grants", domain.HostingHostnameGrantInput{ProjectID: remoteApp.ProjectID, Hostname: route.hostname}, 201)
		route.route = databaseRunnerJSON[domain.HostingRoute](x.f, x.f.token, "POST", "/api/v1/resources/"+remoteApp.ResourceID+"/routes", domain.HostingRouteInput{IdempotencyKey: "retention_remote_route", Hostname: route.hostname, PathPrefix: "/", ExpectedHealthyOperationID: remoteApp.ID}, 202)
		route.awaitProxy()
		route.assertSelected(remoteApp)
		route.record(t.Context(), remoteApp, "GET", "before", "retention_origin")
		route.record(t.Context(), remoteApp, "PUT", "remote_https", "retention_target_write")
		route.record(t.Context(), remoteApp, "GET", "remote_https", "retention_target_write")
	}
	download := databaseRemoteAwait(x.f, survivor.ID, "download", "succeeded", remoteAdmission.ID)
	if download.RestoreOperationID != remoteAdmission.ID || download.Blob == nil || *download.Blob != *survivor.Blob {
		t.Fatal("retention recovery did not download the original remote archive")
	}
	cas := filepath.Join(x.f.root, "state", "logical-backups", strings.TrimPrefix(survivor.Blob.Digest, "sha256:"))
	raw, err := os.ReadFile(cas)
	sum := sha256.Sum256(raw)
	clear(raw)
	if err != nil || fmt.Sprintf("sha256:%x", sum) != survivor.Blob.Digest {
		t.Fatal("remote recovery did not republish the original local archive")
	}
	recoveredPreview := x.preview()
	copyID, valid := retentionRecoveredLocalCopy(recoveredPreview, survivor, "dbc_local_"+survivor.ID, "dbc_remote_"+upload.ID)
	if !valid {
		t.Fatal("remote recovery did not publish one distinct available local copy")
	}
	// Replay only the public admission. Protected download-receipt replay is
	// qualified separately by the core receipt regression, not this assertion.
	replayed := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", restorePath, remoteRequest, 202)
	replayedPreview := x.preview()
	replayedCopyID, valid := retentionRecoveredLocalCopy(replayedPreview, survivor, "dbc_local_"+survivor.ID, "dbc_remote_"+upload.ID)
	if replayed.ID != remoteAdmission.ID || !valid || replayedCopyID != copyID || !retentionSameInventory(recoveredPreview.Items, replayedPreview.Items) {
		t.Fatal("public restore admission replay changed the operation or copy inventory")
	}
	localAdmission := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", restorePath, map[string]any{"request_id": backupRuntimeRequestID(t), "name": "Retention local survivor"}, 202)
	localDatabase, localApp := x.restoreApplication(survivor, localAdmission.ID, "retention_local_survivor")
	if localAdmission.ID == remoteAdmission.ID || localDatabase.Resource.ID == remoteDatabase.Resource.ID || localDatabase.StorageRef == remoteDatabase.StorageRef || localDatabase.Credentials[0].SecretID == remoteDatabase.Credentials[0].SecretID || localApp.ResourceID == remoteApp.ResourceID {
		t.Fatal("subsequent local restore reused the remote recovery target")
	}
	transfers := databaseRunnerJSON[[]databaseRemoteTransfer](x.f, x.f.token, "GET", "/api/v1/database-backups/"+survivor.ID+"/transfers", nil, 200)
	if len(transfers) != 2 {
		t.Fatal("local restore or admission replay created another remote transfer")
	}
	finalPreview := x.preview()
	if !retentionSameInventory(replayedPreview.Items, finalPreview.Items) {
		t.Fatal("subsequent local restore changed the available copy inventory")
	}
	finalHistory := databaseRunnerJSON[[]databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions", nil, 200)
	if len(finalHistory) != 2 || !retentionHistoryPreserved(finalHistory, settled) || !retentionHistoryPreserved(finalHistory, localSettled) {
		t.Fatal("restore or replay changed the original deletion history")
	}
	x.f.assertRedacted()
	x.assertSourcePreserved()
	fmt.Println("DATABASE_BACKUP_RETENTION_PASS " + x.source.Engine)
	fmt.Println("DATABASE_BACKUP_RETENTION_RESTORE_APP_PASS " + x.source.Engine)
}

func TestHostingDatabaseBackupRetentionReplayCompiledRunner(t *testing.T) {
	x := newDatabaseRetentionFixture(t)
	for _, n := range []string{"one", "two", "three"} {
		x.backup(n, "succeeded")
	}
	x.deletePolicy(true)
	x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{KeepCount: 1})
	preview := x.preview()
	if len(retentionSelected(preview)) < 2 {
		t.Fatal("replay needs multiple exact selected copies")
	}
	wire := &databaseRetentionWire{entered: make(chan struct{}), release: make(chan struct{})}
	x.f.retentionWire.Store(wire)
	execution := x.execute(preview, x.f.token, 202)
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("committed deletion ACK loss not reached")
	}
	wire.mu.Lock()
	liveAuthority := wire.committed
	wire.mu.Unlock()
	observeAttempt := func() (int, int) {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		var next, leases int
		if err := x.f.database.QueryRow(ctx, `SELECT next_attempt,(SELECT count(*) FROM run_leases WHERE run_id=task_runs.id) FROM task_runs WHERE id=$1`, liveAuthority.RunID).Scan(&next, &leases); err != nil {
			t.Fatal("exact live attempt observation unavailable")
		}
		return next, leases
	}
	nextBefore, leasesBefore := observeAttempt()
	beforeLive := x.execution(execution.ID)
	liveReplay := x.execute(preview, x.f.token, 202)
	nextAfter, leasesAfter := observeAttempt()
	afterLive := x.execution(execution.ID)
	if liveReplay.ID != execution.ID || nextBefore != nextAfter || leasesBefore != leasesAfter || leasesBefore != 1 || !retentionHistoryPreserved([]databaseRetentionExecution{liveReplay}, beforeLive) || !retentionHistoryPreserved([]databaseRetentionExecution{afterLive}, beforeLive) {
		t.Fatal("live continuation changed its existing batch or attempt")
	}
	x.f.stop()
	close(wire.release)
	x.f.start()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		wire.mu.Lock()
		n := wire.replays
		wire.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("saved deletion receipt did not replay")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Join this exact replay process as well. Untouched copies remain reserved;
	// no new copy may acquire a physical intent before explicit continuation.
	x.f.stop()
	wire.mu.Lock()
	deleted := map[string]bool{wire.copy: true}
	distinct := len(wire.terminals)
	retainedIntents := append([]databaseRetentionMutation(nil), wire.intents...)
	committed := wire.committed
	wire.mu.Unlock()
	if distinct != 1 || len(retainedIntents) != 1 || retainedIntents[0].OperationID != execution.ID || retainedIntents[0].CopyID != committed.CopyID {
		t.Fatal("restart dispatched an untouched selected copy")
	}
	x.assertCopies(preview, deleted)
	history := databaseRunnerJSON[[]databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions", nil, 200)
	if len(history) != 1 || history[0].ID != execution.ID || !history[0].ReservationHeld {
		t.Fatal("interrupted execution lost its exact reserved batch")
	}
	credential, err := os.ReadFile(filepath.Join(x.f.root, "credential"))
	if err != nil {
		t.Fatal("owned runner credential unavailable")
	}
	defer clear(credential)
	client := runnerclient.New(x.f.server.URL, strings.TrimSpace(string(credential)))
	client.HTTP = x.f.client
	lease, err := client.Lease(t.Context(), runnerclient.Authority{LeaseID: committed.LeaseID, Attempt: committed.Attempt, Fence: committed.Fence})
	if err != nil || lease.RunID != committed.RunID {
		t.Fatal("exact old lease observation unavailable")
	}
	expiry, cancelExpiry := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancelExpiry()
	for time.Now().Before(lease.ExpiresAt.Add(250 * time.Millisecond)) {
		select {
		case <-expiry.Done():
			t.Fatal("old lease did not quiesce within fixed profile bound")
		case <-time.After(100 * time.Millisecond):
		}
	}
	x.f.retentionWire.Store(nil)
	browser := startDatabaseBackupBrowserProfile(x.f, x.source, "backup_retention_recovery")
	browser.setPhase("retention_recovery", execution.ID)
	var resumed databaseRetentionExecution
	select {
	case resumed = <-browser.retentions:
	case <-time.After(120 * time.Second):
		t.Fatal("reloaded history did not explicitly continue saved batch")
	}
	if resumed.ID != execution.ID || len(resumed.Items) != len(retentionSelected(preview)) {
		t.Fatal("continuation selected a different execution or copy count")
	}
	for _, item := range resumed.Items {
		if !slices.Contains(retentionSelected(preview), item.CopyID) || item.CopyID == committed.CopyID && item.State != "deleted" && item.State != "absent" {
			t.Fatal("continuation changed the saved batch or completed result")
		}
	}
	stale := committed
	for _, copyID := range retentionSelected(preview) {
		if copyID != committed.CopyID {
			stale.CopyID = copyID
			break
		}
	}
	stale.ReceiptID = backupRuntimeRequestID(t)
	stale.Phase = "uncertain"
	stale.FailureCode = "interrupted"
	beforeStale := x.execution(execution.ID)
	databaseRunnerJSON[databaseRetentionExecution](x.f, strings.TrimSpace(string(credential)), "POST", "/api/v1/runners/hosting/database-backup-retention/mutation", stale, 409)
	afterStale := x.execution(execution.ID)
	if domain.HostingDocumentHash(beforeStale) != domain.HostingDocumentHash(afterStale) {
		t.Fatal("stale authority changed saved execution state")
	}
	replayed := databaseRunnerJSON[databaseRetentionExecution](x.f, strings.TrimSpace(string(credential)), "POST", "/api/v1/runners/hosting/database-backup-retention/mutation", committed, 200)
	if replayed.ID != execution.ID {
		t.Fatal("committed old receipt did not retain its saved execution")
	}
	// Restart with the original journal and roots. Only the server's protected
	// supersession proof may retire an old entry; the fixture edits none.
	x.f.start()
	settled := x.await(execution.ID)
	if settled.ID != execution.ID || settled.State != "succeeded" || settled.ReservationHeld {
		t.Fatal("known result and untouched copies did not reach terminal same-batch success")
	}
	allDeleted := map[string]bool{}
	for _, item := range settled.Items {
		if !slices.Contains(retentionSelected(preview), item.CopyID) || item.ReservationHeld || item.State != "deleted" && item.State != "absent" {
			t.Fatal("terminal continuation changed selected effects")
		}
		allDeleted[item.CopyID] = true
	}
	if len(allDeleted) != len(retentionSelected(preview)) {
		t.Fatal("continuation omitted a saved copy")
	}
	x.assertCopies(preview, allDeleted)
	finalHistory := databaseRunnerJSON[[]databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions", nil, 200)
	if len(finalHistory) != 1 || finalHistory[0].ID != execution.ID {
		t.Fatal("continuation admitted another batch")
	}
	browser.setPhase("complete", execution.ID)
	browser.finished()
	x.assertSourcePreserved()
	fmt.Println("DATABASE_BACKUP_RETENTION_RECOVERY_PASS " + x.source.Engine)
	fmt.Println("DATABASE_BACKUP_RETENTION_STALE_AUTHORITY_PASS " + x.source.Engine)

	fmt.Println("DATABASE_BACKUP_RETENTION_REPLAY_PASS " + x.source.Engine)
}

func TestHostingDatabaseBackupRetentionFailuresCompiledRunner(t *testing.T) {
	for _, kind := range []string{"delete_denied", "conditional_unsupported", "authorization_revoked"} {
		t.Run(kind, func(t *testing.T) {
			x := newDatabaseRetentionFixture(t, kind)
			for _, n := range []string{"one", "two", "three"} {
				x.backup(n, "succeeded")
			}
			x.deletePolicy(kind != "delete_denied")
			x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{KeepCount: 1})
			p := x.preview()
			if len(retentionSelected(p)) == 0 {
				t.Fatal("failure profile selected no owned copies")
			}
			if kind == "authorization_revoked" {
				databaseRunnerJSON[domain.ProjectMember](x.f, x.f.token, "POST", "/api/v1/project-members", app.ProjectMemberInput{ProjectID: x.source.Resource.ProjectID, Email: "viewer@example.local", Role: "viewer"}, 200)
				viewer := databaseRunnerJSON[struct {
					Token string `json:"token"`
				}](x.f, "", "POST", "/api/v1/sessions", map[string]string{"email": "viewer@example.local", "password": "viewer"}, 201)
				x.f.protected = append(x.f.protected, []byte(viewer.Token))
				beforeDenial := x.admissionInventory()
				x.execute(p, viewer.Token, 403)
				if x.admissionInventory() != beforeDenial {
					t.Fatal("viewer denial allocated retention work or changed copy inventory")
				}

				var routes []domain.HostingRoute
				if x.source.Engine == "mysql" {
					routes = backupRuntimeCaptureFixtureRoutes(x.f, x.source.Resource.ProjectID, []domain.HostingRoute{x.f.routes.route, x.f.routes.unrelatedRoute})
				}
				restoreGrant := backupRuntimeRegisterGrantCleanup(x.f, x.source.Resource.ProjectID, x.f.token)
				databaseRunnerJSON[json.RawMessage](x.f, x.f.token, "POST", "/api/v1/destinations/"+x.f.destination+"/project-grants/revoke", app.DestinationProjectInput{ProjectID: x.source.Resource.ProjectID}, 200)
				if x.source.Engine == "mysql" {
					for _, known := range routes {
						current := databaseRunnerJSON[domain.HostingRoute](x.f, x.f.token, "GET", "/api/v1/routes/"+known.ID, nil, 200)
						if current.Enabled || current.Version != known.Version+1 || current.SelectedReleaseOperationID != known.SelectedReleaseOperationID {
							t.Fatal("retention grant revoke did not withdraw exact fixture route")
						}
					}
					x.f.routes.awaitProxy()
				}
				// Project ownership remains valid; the revoked placement is a conflict.
				x.execute(p, x.f.token, 409)
				if x.admissionInventory() != beforeDenial {
					t.Fatal("revoked placement allocated retention work or changed copy inventory")
				}
				if restoreGrant() != nil {
					t.Fatal("owned grant restore failed")
				}
				if x.source.Engine == "mysql" {
					backupRuntimeRecoverFixtureRoutes(x.f, routes, "retention_restore_")
				}
				if !retentionSameInventory(p.Items, x.preview().Items) {
					t.Fatal("denied admission or grant cleanup changed the exact copy selection")
				}
				x.assertCopies(p, map[string]bool{})
			} else {
				wire := &databaseRetentionWire{observeOnly: true}
				x.f.retentionWire.Store(wire)
				executed := x.execute(p, x.f.token, 202)
				settled := x.await(executed.ID)
				wire.mu.Lock()
				intents, terminals := len(wire.intents), len(wire.terminals)
				wire.mu.Unlock()
				if !retentionQualificationHeld(p, settled) || intents != 0 || terminals != 0 || x.archiveDeleteRequests.Load() != 0 {
					t.Fatal("failed qualification changed selected archives or lost its held failure")
				}
				x.assertCopies(p, map[string]bool{})
			}
			x.assertSourcePreserved()
			fmt.Println("DATABASE_BACKUP_RETENTION_FAILURE_PASS " + x.source.Engine + " " + kind)
		})
	}
}

type databaseRetentionAdmissionInventory struct{ Executions, Copies, Items, Tasks int }

func (x *databaseRetentionFixture) admissionInventory() databaseRetentionAdmissionInventory {
	ctx, cancel := context.WithTimeout(x.f.t.Context(), 3*time.Second)
	defer cancel()
	var inventory databaseRetentionAdmissionInventory
	err := x.f.database.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM hosting_database_backup_retention_executions WHERE resource_id=$1),
 (SELECT count(*) FROM hosting_database_backup_copies WHERE resource_id=$1),
 (SELECT count(*) FROM hosting_database_backup_retention_items i JOIN hosting_database_backup_retention_executions e ON e.id=i.execution_id WHERE e.resource_id=$1),
 (SELECT count(*) FROM task_runs WHERE project_id=$2 AND run_spec->>'type'='database_backup_retention')`, x.source.Resource.ID, x.source.Resource.ProjectID).Scan(&inventory.Executions, &inventory.Copies, &inventory.Items, &inventory.Tasks)
	if err != nil {
		x.f.t.Fatal("owned retention allocation observation unavailable")
	}
	return inventory
}

func retentionQualificationHeld(preview databaseRetentionPreview, execution databaseRetentionExecution) bool {
	selected := retentionSelected(preview)
	if execution.State != "uncertain" || !execution.ReservationHeld || len(execution.Items) != len(selected) || len(selected) == 0 {
		return false
	}
	expected := map[string]databaseRetentionItem{}
	for _, item := range preview.Items {
		if item.Selected {
			expected[item.CopyID] = item
		}
	}
	seen, failures := map[string]bool{}, 0
	for _, item := range execution.Items {
		copy, exists := expected[item.CopyID]
		if !exists || item.BackupID != copy.BackupID || item.Location != copy.Location || !slices.Contains(selected, item.CopyID) || seen[item.CopyID] || item.State != "pending" || !item.ReservationHeld {
			return false
		}
		seen[item.CopyID] = true
		if item.FailureCode != "" {
			if item.Location != "remote" || item.FailureCode != "conditional_unsupported" {
				return false
			}
			failures++
		}
	}
	return failures == 1
}

func retentionDumpShadowScripts(engine string) (string, string) {
	// The exact pinned arm64 image was observed selecting /usr/bin/pg_dump,
	// resolved to pg_wrapper and reporting major17. The pinned MySQL image
	// selects the regular /usr/bin/mysqldump at8.4.13. Never move either binary.
	const wrapper = "#!/bin/sh\n# nerocd-owned-retention-duplicate\nexec cat /tmp/nerocd-retention-duplicate.dump\n"
	quoted := "'" + wrapper + "'"
	install := "set -eu\ntest \"$(command -v pg_dump)\" = /usr/bin/pg_dump\ntest -x /usr/bin/pg_dump\ntest \"$(readlink -f /usr/bin/pg_dump)\" = /usr/share/postgresql-common/pg_wrapper\ncase \"$(pg_dump --version)\" in \"pg_dump (PostgreSQL) 17.\"*) ;; *) exit 1 ;; esac\ntest ! -e /usr/local/bin/pg_dump\ntest ! -L /usr/local/bin/pg_dump\n(set -C; printf '%s' " + quoted + " >/usr/local/bin/pg_dump)\nchmod 0755 /usr/local/bin/pg_dump\nhash -r\ntest \"$(command -v pg_dump)\" = /usr/local/bin/pg_dump\n"
	cleanup := "set -eu\nif test -e /usr/local/bin/pg_dump || test -L /usr/local/bin/pg_dump; then\n  test ! -L /usr/local/bin/pg_dump\n  test \"$(cat /usr/local/bin/pg_dump)\" = \"$(printf '%s' " + quoted + ")\"\n  rm /usr/local/bin/pg_dump\nfi\ntest \"$(command -v pg_dump)\" = /usr/bin/pg_dump\nrm -f /tmp/nerocd-retention-duplicate.dump\n"
	if engine == "mysql" {
		install = strings.ReplaceAll(install, "pg_dump", "mysqldump")
		install = strings.ReplaceAll(install, "/usr/share/postgresql-common/pg_wrapper", "/usr/bin/mysqldump")
		install = strings.ReplaceAll(install, "mysqldump (PostgreSQL) 17.", "mysqldump  Ver 8.4.13")
		install = strings.Replace(install, "test -x /usr/bin/mysqldump\n", "test -x /usr/bin/mysqldump\ntest ! -L /usr/bin/mysqldump\n", 1)
		cleanup = strings.ReplaceAll(cleanup, "pg_dump", "mysqldump")
	} else if engine != "postgresql" {
		panic("invalid fixture dump engine")
	}
	return install, cleanup
}

func (x *databaseRetentionFixture) duplicateArchive(original domain.HostingDatabaseBackupOperation) domain.HostingDatabaseBackupOperation {
	f := x.f
	connection := f.connection(x.application.ResourceID)
	source := f.await(connection.UpgradeOperationID)
	data, err := os.ReadFile(filepath.Join(f.root, "state", "logical-backups", strings.TrimPrefix(original.Blob.Digest, "sha256:")))
	if err != nil {
		f.t.Fatal("duplicate-content source archive unavailable")
	}
	defer clear(data)
	// Controlled duplicate-content injection. Only this owned database container's
	// dump invocation is shadowed, using a real archive produced by the
	// compiled runner. Cleanup removes only this wrapper; the real binary stays intact.
	raw, err := databaseRunnerDocker(f.t.Context(), data, "exec", "--interactive", source.Result.ContainerID, "sh", "-c", "cat >/tmp/nerocd-retention-duplicate.dump && chmod 0444 /tmp/nerocd-retention-duplicate.dump")
	clear(raw)
	if err != nil {
		f.t.Fatal("duplicate-content archive injection failed")
	}
	install, cleanup := retentionDumpShadowScripts(x.source.Engine)
	restore := func(ctx context.Context) error {
		raw, err := databaseRunnerDocker(ctx, nil, "exec", source.Result.ContainerID, "sh", "-c", cleanup)
		clear(raw)
		return err
	}
	// Register cleanup before installation or any subsequent backup admission.
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if restore(ctx) != nil {
			f.t.Error("duplicate-content executable cleanup failed")
		}
	})
	f.docker("exec", source.Result.ContainerID, "sh", "-c", install)
	admitted := x.admitBackup()
	settled := backupRuntimeAwait(f, admitted.ID)
	upload := retentionAwaitUpload(f, admitted.ID, "succeeded")
	if restore(f.t.Context()) != nil {
		f.t.Fatal("duplicate-content executable restore failed")
	}
	if settled.Blob == nil || *settled.Blob != *original.Blob {
		f.t.Fatal("duplicate-content injection did not produce a shared archive")
	}
	x.backups = append(x.backups, settled)
	x.uploads = append(x.uploads, upload)
	return settled
}
func (x *databaseRetentionFixture) adoptRemote() domain.HostingDatabaseBackupOperation {
	x.f.appRecord(x.application, "PUT", "adopted", "retention_adopted")
	wire := &databaseBackupRuntimeWire{mode: "backup", entered: make(chan struct{}), release: make(chan struct{})}
	x.f.backupWire.Store(wire)
	admitted := x.admitBackup()
	select {
	case <-wire.entered:
	case <-time.After(90 * time.Second):
		x.f.t.Fatal("external adoption dump ACK boundary not reached")
	}
	x.f.stop()
	settled := backupRuntimeAwait(x.f, admitted.ID)
	archive, err := os.ReadFile(filepath.Join(x.f.root, "state", "logical-backups", strings.TrimPrefix(settled.Blob.Digest, "sha256:")))
	if err != nil {
		x.f.t.Fatal("adoption archive unavailable")
	}
	defer clear(archive)
	// This object intentionally predates the runner upload. Matching contents
	// grant no ownership proof and must never grant retention deletion rights.
	raw, err := databaseRunnerDocker(x.f.t.Context(), archive, "exec", "--interactive", x.silo.id, "sh", "-c", "cat >/tmp/retention-external.dump")
	clear(raw)
	if err != nil {
		x.f.t.Fatal("external adoption staging failed")
	}
	x.silo.command("mc cp /tmp/retention-external.dump fixture/nerocd-runtime/" + retentionObjectKey(settled) + " >/dev/null\nrm /tmp/retention-external.dump\n")
	close(wire.release)
	x.f.start()
	backupRuntimeWaitReplay(x.f, wire)
	x.f.backupWire.Store(nil)
	uploaded := retentionAwaitUpload(x.f, admitted.ID, "succeeded")
	x.backups = append(x.backups, settled)
	x.uploads = append(x.uploads, uploaded)
	return settled
}
func retentionUploadObservation(v databaseRemoteTransfer) (string, string) {
	state, failure := "unknown", "unknown"
	switch v.State {
	case "queued", "dispatched", "applying", "succeeded", "failed", "uncertain":
		state = v.State
	}
	if domain.ValidBackupRemoteFailure(v.FailureCode) {
		failure = v.FailureCode
	}
	return state, failure
}

func retentionAwaitUpload(f *databaseRunnerFixture, backup, expected string) databaseRemoteTransfer {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	state, failure := "missing", "unknown"
	for {
		select {
		case <-ctx.Done():
			f.t.Fatalf("retention upload did not settle: state=%s failure=%s terminal=%s", state, failure, databaseBackupTerminalClass(f.logs.snapshot()))
		case <-ticker.C:
			values := databaseRunnerJSON[[]databaseRemoteTransfer](f, f.token, "GET", "/api/v1/database-backups/"+backup+"/transfers", nil, 200)
			for _, v := range values {
				if v.Mode != "upload" {
					continue
				}
				state, failure = retentionUploadObservation(v)
				if v.State == expected {
					return v
				}
			}
		}
	}
}

func TestDatabaseBackupRetentionUploadObservationClosed(t *testing.T) {
	state, failure := retentionUploadObservation(databaseRemoteTransfer{State: "uncertain", FailureCode: "interrupted"})
	if state != "uncertain" || failure != "interrupted" {
		t.Fatal("known upload state lost")
	}
	state, failure = retentionUploadObservation(databaseRemoteTransfer{State: "private_state_canary", FailureCode: "private_failure_canary"})
	if state != "unknown" || failure != "unknown" {
		t.Fatal("upload observation exposed arbitrary values")
	}
}

func retentionProtected(p databaseRetentionPreview, backup, location, reason string) bool {
	for _, v := range p.Items {
		if v.BackupID == backup && v.Location == location && slices.Contains(v.ProtectedReasons, reason) && !v.Selected {
			return true
		}
	}
	return false
}
func TestHostingDatabaseBackupRetentionProtectionsCompiledRunner(t *testing.T) {
	x := newDatabaseRetentionFixture(t)
	first := x.backup("one", "succeeded")
	second := x.duplicateArchive(first)
	x.silo.policy(false)
	failed := x.backup("failed", "failed")
	x.silo.policy(true)
	adopted := x.adoptRemote()
	x.backup("eligible", "succeeded")
	x.f.appRecord(x.application, "PUT", "active_newest", "retention_active_newest")
	x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{KeepCount: 1})
	active := &databaseBackupRuntimeWire{mode: "backup", pause: true, entered: make(chan struct{}), release: make(chan struct{})}
	x.f.backupWire.Store(active)
	admitted := x.admitBackup()
	select {
	case <-active.entered:
	case <-time.After(90 * time.Second):
		t.Fatal("active backup plan boundary not reached")
	}
	preview := x.preview()
	for _, v := range preview.Items {
		if v.Selected || !slices.Contains(v.ProtectedReasons, "active") {
			t.Fatal("active backup failed to protect exact inventory")
		}
	}
	close(active.release)
	newest := backupRuntimeAwait(x.f, admitted.ID)
	uploaded := retentionAwaitUpload(x.f, admitted.ID, "succeeded")
	x.f.backupWire.Store(nil)
	x.backups = append(x.backups, newest)
	x.uploads = append(x.uploads, uploaded)
	p := x.preview()
	for _, b := range []domain.HostingDatabaseBackupOperation{first, second} {
		if !retentionProtected(p, b.ID, "local", "shared") {
			t.Fatal("shared local inode lost protection")
		}
	}
	if !retentionProtected(p, failed.ID, "local", "upload_pending") || !retentionProtected(p, adopted.ID, "remote", "unproven_ownership") || !retentionProtected(p, newest.ID, "local", "newest") || !retentionProtected(p, newest.ID, "remote", "newest") {
		t.Fatal("failed-upload external or newest protection missing")
	}
	x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{})
	local := x.preview()
	if len(retentionSelected(local)) == 0 {
		t.Fatal("local limit proof selected no eligible copy")
	}
	for _, v := range local.Items {
		if v.Selected && v.Location != "local" {
			t.Fatal("local limits selected a remote copy")
		}
	}
	x.policy(databaseRetentionLimits{}, databaseRetentionLimits{KeepCount: 1})
	remote := x.preview()
	if len(retentionSelected(remote)) == 0 {
		t.Fatal("remote limit proof selected no eligible copy")
	}
	for _, v := range remote.Items {
		if v.Selected && v.Location != "remote" {
			t.Fatal("remote limits selected a local copy")
		}
	}
	x.assertCopies(p, map[string]bool{})
	sharedRestore := databaseRunnerJSON[domain.HostingDatabaseBackupOperation](x.f, x.f.token, "POST", "/api/v1/database-backups/"+second.ID+"/restores", map[string]any{"request_id": backupRuntimeRequestID(t), "name": "Shared archive recovery"}, 202)
	x.restoreApplication(second, sharedRestore.ID, "retention_shared_survivor")
	x.f.assertRedacted()
	x.assertSourcePreserved()
	fmt.Println("DATABASE_BACKUP_RETENTION_PROTECTION_PASS " + x.source.Engine + " newest_shared_active_failed_upload_external")
	fmt.Println("DATABASE_BACKUP_RETENTION_INDEPENDENT_LIMITS_PASS " + x.source.Engine)
}

func TestDatabaseBackupRetentionFixtureSelectionAndOptIn(t *testing.T) {
	p := databaseRetentionPreview{Items: []databaseRetentionItem{{CopyID: "local", Selected: true}, {CopyID: "remote", Selected: false, ProtectedReasons: []string{"newest"}}}}
	if !retentionExactSelection(p, []string{"local"}) || retentionExactSelection(p, []string{"remote"}) || !retentionProtected(databaseRetentionPreview{Items: []databaseRetentionItem{{BackupID: "b", Location: "remote", ProtectedReasons: []string{"newest"}}}}, "b", "remote", "newest") {
		t.Fatal("exact copy assertion accepted another batch")
	}
	p.SelectionTruncated = true
	if retentionExactSelection(p, []string{"local"}) {
		t.Fatal("truncated selection accepted as exact")
	}
	t.Setenv("NEROCD_DATABASE_BACKUP_RUNTIME", "1")
	t.Setenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME", "1")
	t.Setenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME", "0")
	f := &databaseRunnerFixture{server: &httptest.Server{URL: "https://fixture.invalid"}}
	if slices.Contains(databaseRunnerArguments(f), "--hosting-database-backup-retention") {
		t.Fatal("default remote profile enabled retention")
	}
	t.Setenv("NEROCD_DATABASE_BACKUP_RETENTION_RUNTIME", "1")
	if !slices.Contains(databaseRunnerArguments(f), "--hosting-database-backup-retention") {
		t.Fatal("retention profile omitted compiled runner flag")
	}
	// Storage bootstrap and unrelated-route setup both restart through f.start,
	// which regenerates these actual arguments rather than retaining a prior slice.
	defaultOrigin := "--hosting-backup-remote-trusted-loopback=http://127.0.0.1:9000"
	faultOrigin := "--hosting-backup-remote-trusted-loopback=http://127.0.0.1:9002"
	if !slices.Contains(databaseRunnerArguments(f), defaultOrigin) {
		t.Fatal("ordinary remote fixture changed trusted origin")
	}
	f.remoteBackupFault = true
	for _, storage := range []*applicationDatabaseStorageFixture{nil, {}} {
		f.storage = storage
		args := databaseRunnerArguments(f)
		if !slices.Contains(args, faultOrigin) || slices.Contains(args, defaultOrigin) {
			t.Fatal("fault origin lost when shared restart regenerated runner arguments")
		}
	}
	t.Setenv("NEROCD_DATABASE_BACKUP_REMOTE_RUNTIME", "0")
	if slices.Contains(databaseRunnerArguments(f), "--hosting-database-backup-retention") {
		t.Fatal("retention bypassed remote prerequisite")
	}
}

func TestDatabaseBackupRetentionWireExactReplayAndDefaultIsolation(t *testing.T) {
	wire := &databaseRetentionWire{entered: make(chan struct{}), release: make(chan struct{})}
	close(wire.release)
	private := `{"phase":"deleted","copy_id":"owned_copy","receipt_id":"private_canary"}`
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != private {
			t.Error("retention wire changed committed receipt body")
		}
		w.Header().Set("X-Fixture-Receipt", "real")
		_, _ = w.Write([]byte(`{"state":"applying"}`))
	})
	send := func(body string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-retention/mutation", strings.NewReader(body)), handler) {
			t.Fatal("retention receipt was not forwarded")
		}
		return out
	}
	first := send(private)
	if first.Body.Len() != 0 || first.Header().Get("X-Fixture-Receipt") != "" {
		t.Fatal("lost committed retention response escaped")
	}
	replay := send(private)
	if replay.Code != 200 || replay.Header().Get("X-Fixture-Receipt") != "real" || wire.replays != 1 || calls != 2 {
		t.Fatal("retention replay did not preserve real receipt")
	}
	other := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"state":"applying"}`)) })
	rec := httptest.NewRecorder()
	wire.serve(rec, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-retention/mutation", strings.NewReader(`{"phase":"deleted","copy_id":"another_copy"}`)), other)
	if rec.Code != 409 || wire.replays != 1 {
		t.Fatal("retention replay accepted another selected copy")
	}
	for _, path := range []string{"/api/v1/runners/hosting/database-backup/mutation", "/api/v1/runners/hosting/database-backup-transfer/mutation", "/api/v1/runners/hosting/database-backup-retention/mutation?private_canary=1"} {
		request := httptest.NewRequest("GET", path, nil)
		if wire.serve(httptest.NewRecorder(), request, other) {
			t.Fatal("retention fault changed another profile")
		}
	}
}

func retentionArchiveDelete(r *http.Request) bool {
	if r.Method != http.MethodDelete {
		return false
	}
	for _, profile := range []struct{ engine, suffix string }{{"postgresql", ".dump"}, {"mysql", ".sql"}} {
		prefix := "/nerocd-runtime/nerocd/" + profile.engine + "/"
		if strings.HasPrefix(r.URL.Path, prefix) && !strings.HasPrefix(r.URL.Path, prefix+"retention-probes/") && strings.HasSuffix(r.URL.Path, profile.suffix) {
			return true
		}
	}
	return false
}

func retentionConditionalFault(r *http.Request) bool {
	return r.Method == http.MethodDelete && (strings.HasPrefix(r.URL.Path, "/nerocd-runtime/nerocd/postgresql/retention-probes/") || strings.HasPrefix(r.URL.Path, "/nerocd-runtime/nerocd/mysql/retention-probes/")) && strings.HasPrefix(r.Header.Get("If-Match"), "\"nerocd-does-not-match-")
}
func TestDatabaseBackupRetentionConditionalFaultScope(t *testing.T) {
	for _, v := range []struct {
		method, path, etag string
		want               bool
	}{
		{"DELETE", "/nerocd-runtime/nerocd/postgresql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", true},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", true},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/backup/digest.sql", "\"nerocd-does-not-match-owned\"", false},
		{"GET", "/nerocd-runtime/nerocd/mysql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", false},
		{"DELETE", "/foreign/nerocd/mysql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", false},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/retention-probes/owned/nonce", "\"real-object-etag\"", false},
		{"DELETE", "/nerocd-runtime/nerocd/postgresql/backup/digest.dump", "\"nerocd-does-not-match-owned\"", false},
		{"GET", "/nerocd-runtime/nerocd/postgresql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", false},
		{"DELETE", "/foreign/nerocd/postgresql/retention-probes/owned/nonce", "\"nerocd-does-not-match-owned\"", false},
		{"DELETE", "/nerocd-runtime/nerocd/postgresql/retention-probes/owned/nonce", "\"real-object-etag\"", false},
	} {
		r := httptest.NewRequest(v.method, v.path, nil)
		r.Header.Set("If-Match", v.etag)
		if retentionConditionalFault(r) != v.want {
			t.Fatal("conditional fault escaped its exact owned probe boundary")
		}
	}
}

func TestDatabaseBackupRetentionWireRetainsPrivateIntentOnce(t *testing.T) {
	wire := &databaseRetentionWire{entered: make(chan struct{}), release: make(chan struct{})}
	request := `{"operation_id":"owned_execution","run_id":"owned_run","lease_id":"owned_lease","attempt":1,"fence":"private_intent_canary","receipt_id":"owned_receipt","plan_hash":"owned_hash","copy_id":"owned_copy","phase":"intent","failure_code":""}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || string(raw) != request {
			t.Error("private intent body changed")
		}
		_, _ = w.Write([]byte(`{"state":"applying"}`))
	})
	for range 2 {
		out := httptest.NewRecorder()
		if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-retention/mutation", strings.NewReader(request)), handler) || out.Code != 200 || strings.Contains(out.Body.String(), "private_intent_canary") {
			t.Fatal("retention observation reflected private intent")
		}
	}
	if len(wire.intents) != 1 || wire.intents[0].Fence != "private_intent_canary" || wire.first {
		t.Fatal("retention observer did not retain one exact private intent")
	}
}

// This fault delegates exactly one selected archive DELETE to the owned Silo,
// then drops its response. Subsequent exact-object observation is denied. The
// fixture knows the effect, but never supplies that private knowledge to the
// production runner or controller. Probe qualification is always forwarded.
type databaseRetentionDeleteFault struct {
	mu          sync.Mutex
	path        string
	calls, puts int
	delegated   bool
}

func (f *databaseRetentionDeleteFault) serve(w http.ResponseWriter, r *http.Request, proxy http.Handler) bool {
	f.mu.Lock()
	if f.path == "" || r.URL.Path != f.path {
		f.mu.Unlock()
		return false
	}
	if r.Method == http.MethodPut {
		f.puts++
	}
	if r.Method == http.MethodDelete {
		f.calls++
		first := f.calls == 1
		f.mu.Unlock()
		if first {
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			clear(rec.Body.Bytes())
			f.mu.Lock()
			f.delegated = rec.Code >= 200 && rec.Code < 300
			f.mu.Unlock()
		}
		if h, ok := w.(http.Hijacker); ok {
			connection, _, err := h.Hijack()
			if err == nil {
				_ = connection.Close()
				return true
			}
		}
		w.WriteHeader(503)
		return true
	}
	deny := f.calls > 0
	f.mu.Unlock()
	if deny {
		w.WriteHeader(503)
		return true
	}
	return false
}
func (x *databaseRetentionFixture) retryStatus(p databaseRetentionPreview) (databaseRetentionExecution, int) {
	raw, _ := json.Marshal(map[string]any{"request_id": backupRuntimeRequestID(x.f.t), "preview_id": p.ID, "expected_policy_revision": p.PolicyRevision, "expected_plan_hash": p.PlanHash})
	defer clear(raw)
	request, err := http.NewRequestWithContext(x.f.t.Context(), http.MethodPost, x.f.server.URL+x.path()+"/executions", bytes.NewReader(raw))
	if err != nil {
		x.f.t.Fatal("retention retry request failed")
	}
	request.Header.Set("Authorization", "Bearer "+x.f.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := x.f.client.Do(request)
	if err != nil {
		x.f.t.Fatal("retention retry transport failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	defer clear(data)
	closed := response.Body.Close()
	var value databaseRetentionExecution
	if err != nil || closed != nil || len(data) > 65536 || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || response.StatusCode != 202 && response.StatusCode != 409 {
		x.f.t.Fatal("retention retry response unavailable")
	}
	if response.StatusCode == 202 && json.Unmarshal(data, &value) != nil {
		x.f.t.Fatal("retention retry document invalid")
	}
	return value, response.StatusCode
}
func retentionHasUntouched(v databaseRetentionExecution) bool {
	for _, item := range v.Items {
		if item.State == "pending" || item.State == "intent" {
			return true
		}
	}
	return false
}
func TestHostingDatabaseBackupRetentionUnknownDeleteCompiledRunner(t *testing.T) {
	x := newDatabaseRetentionFixture(t, "unknown_delete")
	for _, n := range []string{"one", "two", "three"} {
		x.backup(n, "succeeded")
	}
	x.deletePolicy(true)
	x.policy(databaseRetentionLimits{KeepCount: 1}, databaseRetentionLimits{KeepCount: 1})
	preview := x.preview()
	var unknown databaseRetentionItem
	for _, item := range preview.Items {
		if item.Selected && item.Location == "remote" {
			unknown = item
			break
		}
	}
	if unknown.CopyID == "" {
		t.Fatal("unknown deletion profile has no exact remote candidate")
	}
	x.deleteFault.mu.Lock()
	for _, backup := range x.backups {
		if backup.ID == unknown.BackupID {
			x.deleteFault.path = "/nerocd-runtime/" + retentionObjectKey(backup)
		}
	}
	if x.deleteFault.path == "" {
		t.Fatal("unknown delete escaped exact owned archive")
	}
	x.deleteFault.mu.Unlock()
	admitted := x.execute(preview, x.f.token, 202)
	settled := x.await(admitted.ID)
	x.f.stop()
	if settled.State != "uncertain" || !settled.ReservationHeld {
		t.Fatal("unknown dispatched nonversioned deletion did not remain held")
	}
	for attempts := 0; retentionHasUntouched(settled); attempts++ {
		if attempts >= len(preview.Items) {
			t.Fatal("untouched continuation did not converge")
		}
		resumed, status := x.retryStatus(preview)
		if status != 202 || resumed.ID != admitted.ID {
			t.Fatal("unknown-copy batch could not continue only untouched copies")
		}
		x.f.start()
		settled = x.await(admitted.ID)
		x.f.stop()
	}
	// No further action is eligible. The unknown object's key remains reserved;
	// an explicit new request cannot turn uncertainty into permission to resend.
	_, status := x.retryStatus(preview)
	if status != 409 {
		t.Fatal("unknown deletion admitted an unjustified retry")
	}
	current := x.execution(admitted.ID)
	if current.State != "uncertain" || !current.ReservationHeld || len(current.Items) != len(retentionSelected(preview)) {
		t.Fatal("unknown deletion lost its exact reservation")
	}
	unknownHeld := false
	deleted := map[string]bool{unknown.CopyID: true}
	for _, item := range current.Items {
		if !slices.Contains(retentionSelected(preview), item.CopyID) {
			t.Fatal("unknown continuation selected another copy")
		}
		if item.CopyID == unknown.CopyID {
			unknownHeld = item.State == "uncertain" && item.ReservationHeld
		} else if item.State == "deleted" || item.State == "absent" {
			deleted[item.CopyID] = true
		}
	}
	if !unknownHeld {
		t.Fatal("unknown object ceased to be reserved")
	}
	x.deleteFault.mu.Lock()
	calls, puts, delegated := x.deleteFault.calls, x.deleteFault.puts, x.deleteFault.delegated
	x.deleteFault.mu.Unlock()
	if calls != 1 || puts != 0 || !delegated {
		t.Fatal("unknown DELETE was repeated or its object key reused")
	}
	x.assertCopies(preview, deleted)
	history := databaseRunnerJSON[[]databaseRetentionExecution](x.f, x.f.token, "GET", x.path()+"/executions", nil, 200)
	if len(history) != 1 || history[0].ID != admitted.ID {
		t.Fatal("unknown deletion escaped its saved batch")
	}
	x.assertSourcePreserved()
	fmt.Println("DATABASE_BACKUP_RETENTION_UNKNOWN_DELETE_HELD_PASS " + x.source.Engine)
}

func TestDatabaseBackupRetentionUnknownFaultScope(t *testing.T) {
	fault := &databaseRetentionDeleteFault{path: "/nerocd-runtime/nerocd/postgresql/owned/digest.dump"}
	delegated := 0
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { delegated++; w.WriteHeader(204) })
	// A recorder cannot hijack. It receives a closed 503 after the real upstream
	// effect, while the live server closes the connection without a response.
	for range 2 {
		out := httptest.NewRecorder()
		if !fault.serve(out, httptest.NewRequest("DELETE", fault.path, nil), upstream) || out.Code != 503 {
			t.Fatal("unknown effect was acknowledged")
		}
	}
	if delegated != 1 || fault.calls != 2 || !fault.delegated {
		t.Fatal("fault repeated a delegated DELETE")
	}
	if fault.serve(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/nerocd-runtime/nerocd/postgresql/retention-probes/owned/nonce", nil), upstream) {
		t.Fatal("unknown fault intercepted conditional probe")
	}
	out := httptest.NewRecorder()
	if !fault.serve(out, httptest.NewRequest("HEAD", fault.path, nil), upstream) || out.Code != 503 {
		t.Fatal("unknown object observation escaped denial")
	}
}

// The SDK decodes these in-memory HTTP responses, so parsing failures exercise
// the same boundary as physical Silo observations without starting a service.
type databaseRetentionObservationHTTP struct {
	status  int
	body    string
	err     error
	readErr error
	calls   int
	scoped  bool
}

func (c *databaseRetentionObservationHTTP) Do(r *http.Request) (*http.Response, error) {
	c.calls++
	c.scoped = r.Method == "GET" && r.URL.Scheme == "http" && r.URL.Host == "127.0.0.1:9000" && r.URL.Path == "/nerocd-runtime" && r.URL.Query().Get("list-type") == "2" && r.URL.Query().Get("prefix") == "nerocd/postgresql/owned-backup/archive.dump" && r.URL.Query().Get("max-keys") == "2"
	if c.err != nil {
		return nil, c.err
	}
	body := io.Reader(strings.NewReader(c.body))
	if c.readErr != nil {
		body = io.MultiReader(body, databaseRetentionObservationReadError{c.readErr})
	}
	return &http.Response{StatusCode: c.status, Header: http.Header{"Content-Type": {"application/xml"}}, Body: io.NopCloser(body), Request: r}, nil
}

type databaseRetentionObservationReadError struct{ err error }

func (r databaseRetentionObservationReadError) Read([]byte) (int, error) { return 0, r.err }

func TestDatabaseBackupRetentionRemoteAbsenceRequiresSuccessfulObservation(t *testing.T) {
	const key = "nerocd/postgresql/owned-backup/archive.dump"
	inventory := func(contents string, count int, truncated bool) string {
		return fmt.Sprintf(`<ListBucketResult><Name>nerocd-runtime</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><IsTruncated>%t</IsTruncated>%s</ListBucketResult>`, key, count, truncated, contents)
	}
	present := "<Contents><Key>" + key + "</Key></Contents>"
	canary := "private-error-url-credential-canary"
	for _, tc := range []struct {
		name, body, want string
		status           int
		err, readErr     error
	}{
		{name: "missing", status: 200, body: inventory("", 0, false), want: "missing"},
		{name: "present", status: 200, body: inventory(present, 1, false), want: "present"},
		{name: "different_suffix", status: 200, body: inventory("<Contents><Key>"+key+".other</Key></Contents>", 1, false), want: "missing"},
		{name: "transport_failure", err: errors.New(canary), want: "unknown"},
		{name: "command_failure", err: errors.New("exit status 1: " + canary), want: "unknown"},
		{name: "authorization_failure", status: 403, body: "<Error><Code>AccessDenied</Code><Message>" + canary + "</Message></Error>", want: "unknown"},
		{name: "server_failure", status: 500, body: canary, want: "unknown"},
		{name: "not_found_error", status: 404, body: "<Error><Code>NoSuchKey</Code></Error>", want: "unknown"},
		{name: "deadline", err: context.DeadlineExceeded, want: "unknown"},
		{name: "parsing_failure", status: 200, body: "<ListBucketResult><broken>" + canary, want: "unknown"},
		{name: "wrong_root", status: 200, body: strings.ReplaceAll(inventory("", 0, false), "ListBucketResult", "UnexpectedRoot"), want: "unknown"},
		{name: "malformed_tail", status: 200, body: inventory("", 0, false) + "<broken", want: "unknown"},
		{name: "second_root", status: 200, body: inventory("", 0, false) + "<another/>", want: "unknown"},
		{name: "overflow_after_root", status: 200, body: inventory("", 0, false) + strings.Repeat(" ", 65<<10), want: "unknown"},
		{name: "read_error_after_root", status: 200, body: inventory("", 0, false), readErr: errors.New(canary), want: "unknown"},
		{name: "empty_response", status: 200, want: "unknown"},
		{name: "oversize_response", status: 200, body: "<ListBucketResult><Name>" + strings.Repeat("x", 65<<10) + "</Name></ListBucketResult>", want: "unknown"},
		{name: "truncated", status: 200, body: inventory("", 0, true), want: "unknown"},
		{name: "count_mismatch", status: 200, body: inventory("", 1, false), want: "unknown"},
		{name: "over_bound", status: 200, body: inventory(present+present+present, 3, false), want: "unknown"},
		{name: "wrong_bucket", status: 200, body: strings.ReplaceAll(inventory("", 0, false), "nerocd-runtime", canary), want: "unknown"},
		{name: "wrong_prefix", status: 200, body: strings.ReplaceAll(inventory("", 0, false), key, canary), want: "unknown"},
		{name: "outside_prefix", status: 200, body: inventory("<Contents><Key>"+canary+"</Key></Contents>", 1, false), want: "unknown"},
		{name: "missing_key", status: 200, body: inventory("<Contents></Contents>", 1, false), want: "unknown"},
		{name: "incomplete_inventory", status: 200, body: "<ListBucketResult><Name>nerocd-runtime</Name></ListBucketResult>", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := &databaseRetentionObservationHTTP{status: tc.status, body: tc.body, err: tc.err, readErr: tc.readErr}
			client := databaseRetentionObservationClient("fixture-access", "fixture-secret", wire)
			got := databaseRetentionObserveRemote(t.Context(), client, key)
			if got != tc.want {
				t.Fatalf("observation classification = %q, want %q", got, tc.want)
			}
			if wire.calls != 1 || !wire.scoped {
				t.Fatal("observation must make exactly one bounded request for the owned exact prefix")
			}
		})
	}
}

func TestDatabaseBackupRetentionRecoveredCopyPublicEvidence(t *testing.T) {
	backup := domain.HostingDatabaseBackupOperation{ID: "backup", Blob: &domain.HostingDatabaseBackupBlob{Digest: "sha256:archive", Bytes: 42}}
	local := databaseRetentionItem{CopyID: "downloaded", BackupID: backup.ID, Location: "local", Availability: "available", Digest: backup.Blob.Digest, Bytes: backup.Blob.Bytes}
	remote := local
	remote.CopyID, remote.Location = "remote", "remote"
	original := local
	original.CopyID, original.Availability = "original", "deleted"
	for _, tc := range []struct {
		name  string
		items []databaseRetentionItem
		want  bool
	}{
		{name: "new_copy_and_remote", items: []databaseRetentionItem{local, remote}, want: true},
		{name: "deleted_original_preserved", items: []databaseRetentionItem{local, remote, original}, want: true},
		{name: "duplicate_download_copy", items: []databaseRetentionItem{local, local, remote}},
		{name: "missing_local", items: []databaseRetentionItem{remote}},
		{name: "missing_remote", items: []databaseRetentionItem{local}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, valid := retentionRecoveredLocalCopy(databaseRetentionPreview{Items: tc.items}, backup, original.CopyID, remote.CopyID)
			if valid != tc.want || valid && id != local.CopyID {
				t.Fatal("recovery inventory qualification differs from expected evidence")
			}
		})
	}
	for _, mutate := range []func(*databaseRetentionItem){
		func(v *databaseRetentionItem) { v.CopyID = original.CopyID },
		func(v *databaseRetentionItem) { v.Availability = "unproven" },
		func(v *databaseRetentionItem) { v.Digest = "sha256:other" },
		func(v *databaseRetentionItem) { v.Bytes++ },
	} {
		changed := local
		mutate(&changed)
		if _, valid := retentionRecoveredLocalCopy(databaseRetentionPreview{Items: []databaseRetentionItem{changed, remote}}, backup, original.CopyID, remote.CopyID); valid {
			t.Fatal("changed or resurrected local copy satisfied recovery proof")
		}
	}
	if !retentionSameInventory([]databaseRetentionItem{local, remote}, []databaseRetentionItem{remote, local}) || retentionSameInventory([]databaseRetentionItem{local, remote}, []databaseRetentionItem{local, local}) {
		t.Fatal("public inventory replay comparison lost copy identity")
	}
	prior := databaseRetentionExecution{ID: "execution", State: "succeeded", Items: []databaseRetentionExecutionItem{{CopyID: original.CopyID, State: "deleted"}}}
	if !retentionHistoryPreserved([]databaseRetentionExecution{prior}, prior) {
		t.Fatal("unchanged deletion history was rejected")
	}
	changed := prior
	changed.Items = slices.Clone(prior.Items)
	changed.Items[0].State = "available"
	if retentionHistoryPreserved([]databaseRetentionExecution{changed}, prior) {
		t.Fatal("recovery erased original deletion evidence")
	}
}

func TestDatabaseBackupRetentionQualificationProbeIsolation(t *testing.T) {
	wire := &databaseRetentionWire{entered: make(chan struct{}), release: make(chan struct{})}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"state":"dispatched"}`)) })
	for _, phase := range []string{"intent", "deleted", "absent", "uncertain"} {
		body := fmt.Sprintf(`{"probe_only":true,"probe_key":"nerocd/postgresql/retention-probes/owned/nonce","phase":%q,"copy_id":"owned"}`, phase)
		out := httptest.NewRecorder()
		if !wire.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-retention/mutation", strings.NewReader(body)), handler) || out.Code != 200 || out.Body.Len() == 0 {
			t.Fatal("qualification response entered the archive receipt fault")
		}
	}
	if wire.first || len(wire.intents) != 0 || len(wire.terminals) != 0 || wire.replays != 0 {
		t.Fatal("qualification probes were counted as physical archive effects")
	}
	observer := &databaseRetentionWire{observeOnly: true}
	for _, phase := range []string{"intent", "deleted"} {
		body := fmt.Sprintf(`{"phase":%q,"copy_id":"owned"}`, phase)
		out := httptest.NewRecorder()
		observer.serve(out, httptest.NewRequest("POST", "/api/v1/runners/hosting/database-backup-retention/mutation", strings.NewReader(body)), handler)
		if out.Code != 200 || out.Body.Len() == 0 {
			t.Fatal("archive observer withheld a real response")
		}
	}
	if len(observer.intents) != 1 || len(observer.terminals) != 1 {
		t.Fatal("archive observer failed to detect physical effects")
	}
}

func TestDatabaseBackupRetentionQualificationPendingProof(t *testing.T) {
	preview := databaseRetentionPreview{Items: []databaseRetentionItem{{CopyID: "local", Location: "local", Selected: true}, {CopyID: "remote", Location: "remote", Selected: true}}}
	execution := databaseRetentionExecution{State: "uncertain", ReservationHeld: true, Items: []databaseRetentionExecutionItem{{CopyID: "local", Location: "local", State: "pending", ReservationHeld: true}, {CopyID: "remote", Location: "remote", State: "pending", ReservationHeld: true, FailureCode: "conditional_unsupported"}}}
	if !retentionQualificationHeld(preview, execution) {
		t.Fatal("untouched pending copies lost visible qualification failure")
	}
	for _, mutate := range []func(*databaseRetentionExecution){
		func(v *databaseRetentionExecution) { v.Items[0].State = "intent" },
		func(v *databaseRetentionExecution) { v.Items[0].State = "deleted" },
		func(v *databaseRetentionExecution) { v.Items[0].State = "uncertain" },
		func(v *databaseRetentionExecution) { v.Items[0].ReservationHeld = false },
		func(v *databaseRetentionExecution) { v.Items[1].FailureCode = "" },
		func(v *databaseRetentionExecution) { v.Items[1].FailureCode = "private_canary" },
		func(v *databaseRetentionExecution) { v.Items[0].CopyID = v.Items[1].CopyID },
		func(v *databaseRetentionExecution) { v.Items = v.Items[:1] },
	} {
		changed := execution
		changed.Items = slices.Clone(execution.Items)
		mutate(&changed)
		if retentionQualificationHeld(preview, changed) {
			t.Fatal("changed physical effects satisfied qualification failure proof")
		}
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"DELETE", "/nerocd-runtime/nerocd/postgresql/backup/archive.dump", true},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/backup/archive.sql", true},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/backup/archive.dump", false},
		{"GET", "/nerocd-runtime/nerocd/mysql/backup/archive.sql", false},
		{"DELETE", "/nerocd-runtime/nerocd/mysql/retention-probes/owned/archive.sql", false},
		{"DELETE", "/foreign/nerocd/mysql/backup/archive.sql", false},
		{"GET", "/nerocd-runtime/nerocd/postgresql/backup/archive.dump", false},
		{"DELETE", "/nerocd-runtime/nerocd/postgresql/retention-probes/owned/archive.dump", false},
		{"DELETE", "/foreign/nerocd/postgresql/backup/archive.dump", false},
	} {
		if retentionArchiveDelete(httptest.NewRequest(tc.method, tc.path, nil)) != tc.want {
			t.Fatal("archive DELETE observation escaped owned archive scope")
		}
	}
}

func TestDatabaseBackupRetentionDumpShadowGuard(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		tool := "pg_dump"
		if engine == "mysql" {
			tool = "mysqldump"
		}
		install, cleanup := retentionDumpShadowScripts(engine)
		for _, script := range []string{install, cleanup} {
			command := exec.CommandContext(t.Context(), "sh", "-n")
			command.Stdin = strings.NewReader(script)
			if err := command.Run(); err != nil {
				t.Fatal("owned dump shadow script syntax invalid")
			}
			if strings.Contains(script, "mv ") || strings.Contains(script, "retention-original") {
				t.Fatal("dump shadow altered the real executable")
			}
		}
		for _, guard := range []string{"test ! -e /usr/local/bin/pg_dump", "test ! -L /usr/local/bin/pg_dump", "set -C", "hash -r", "$(command -v pg_dump)\" = /usr/bin/pg_dump", "$(command -v pg_dump)\" = /usr/local/bin/pg_dump"} {
			if !strings.Contains(install, strings.ReplaceAll(guard, "pg_dump", tool)) {
				t.Fatal("dump shadow installation lost an executable ownership guard")
			}
		}
		if !strings.Contains(cleanup, "test ! -L /usr/local/bin/"+tool) || !strings.Contains(cleanup, "$(cat /usr/local/bin/"+tool+")") || !strings.Contains(cleanup, "nerocd-owned-retention-duplicate") {
			t.Fatal("dump shadow cleanup lost exact-content guards")
		}
	}
}

func TestDatabaseBackupRetentionDumpShadowLifecycle(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		for _, preexisting := range []bool{false, true} {
			t.Run(engine+fmt.Sprint(preexisting), func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal("shadow unit fixture canonical path failed")
				}
				localBin, bin, share := filepath.Join(root, "usr/local/bin"), filepath.Join(root, "usr/bin"), filepath.Join(root, "usr/share/postgresql-common")
				for _, dir := range []string{localBin, bin, share} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal("shadow unit fixture directory failed")
					}
				}
				tool := "pg_dump"
				if engine == "mysql" {
					tool = "mysqldump"
				}
				real := filepath.Join(share, "pg_wrapper")
				realBytes := []byte("#!/bin/sh\nprintf '%s\\n' 'pg_dump (PostgreSQL) 17.11'\n")
				if engine == "mysql" {
					real = filepath.Join(bin, tool)
					realBytes = []byte("#!/bin/sh\nprintf '%s\\n' 'mysqldump  Ver 8.4.13 for Linux on aarch64 (MySQL Community Server - GPL)'\n")
					if os.WriteFile(real, realBytes, 0700) != nil {
						t.Fatal("MySQL unit executable failed")
					}
				} else if os.WriteFile(real, realBytes, 0700) != nil || os.Symlink(real, filepath.Join(bin, tool)) != nil {
					t.Fatal("shadow unit fixture executable failed")
				}
				archive := filepath.Join(root, "archive.dump")
				payload := []byte("owned archive bytes for shell lifecycle regression")
				if os.WriteFile(archive, payload, 0600) != nil {
					t.Fatal("shadow unit fixture archive failed")
				}
				shadow := filepath.Join(localBin, tool)
				if preexisting && os.WriteFile(shadow, []byte("unrelated"), 0600) != nil {
					t.Fatal("shadow preexisting fixture failed")
				}
				install, cleanup := retentionDumpShadowScripts(engine)
				rewrite := strings.NewReplacer("/usr/local/bin", localBin, "/usr/bin", bin, "/usr/share/postgresql-common", share, "/tmp/nerocd-retention-duplicate.dump", archive)
				run := func(script string) ([]byte, error) {
					command := exec.CommandContext(t.Context(), "/bin/sh", "-c", rewrite.Replace(script))
					command.Env = append(os.Environ(), "PATH="+localBin+":"+bin+":/usr/bin:/bin")
					return command.Output()
				}
				_, installed := run(install)
				if preexisting {
					if installed == nil {
						t.Fatal("shadow installation overwrote an existing executable")
					}
					if _, err := run(cleanup); err == nil {
						t.Fatal("shadow cleanup accepted an unrelated file")
					}
					raw, err := os.ReadFile(shadow)
					if err != nil || string(raw) != "unrelated" {
						t.Fatal("shadow cleanup removed an unrelated file")
					}
					return
				}
				if installed != nil {
					t.Fatal("observed executable layout rejected by shadow installation")
				}
				if raw, err := run(tool); err != nil || !bytes.Equal(raw, payload) {
					t.Fatal("shadow was not the executable selected by PATH")
				}
				if _, err := run(cleanup); err != nil {
					t.Fatal("owned shadow cleanup failed")
				}
				if _, err := run(cleanup); err != nil {
					t.Fatal("owned shadow cleanup was not idempotent")
				}
				if _, err := os.Lstat(shadow); !os.IsNotExist(err) {
					t.Fatal("owned shadow survived cleanup")
				}
				if raw, err := os.ReadFile(real); err != nil || !bytes.Equal(raw, realBytes) {
					t.Fatal("real executable changed during shadow lifecycle")
				}
			})
		}
	}
}
