package runnerruntime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

const mountedFixtureOwner = "mounted-20261023-16a7c"
const mountedFixtureLabel = "io.nerocd.mounted.fixture"
const mountedFixtureVolume = "ncdv_1f7a0cacb82253502054feb92e24684c"
const mountedFixtureRegistry = "nerocd-mounted-registry-16a7c"
const mountedFixtureForeign = "nerocd-mounted-foreign-16a7c"
const mountedFixtureRegistryImage = "sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
const mountedFixtureDaemon = "e37dd219-b13f-47dd-99ed-21d79442ed0c"

const mountedFixtureServer = `package main
import("fmt";"io";"net/http";"os")
func main(){
 root:="/data/uploads"
 http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
 switch r.URL.Path {
 case "/ready": w.WriteHeader(204)
 case "/unready": w.WriteHeader(503)
 case "/version": fmt.Fprint(w,os.Args[1])
 case "/upload":
  if r.Method!="POST" {w.WriteHeader(405);return}
  f,e:=os.OpenFile(root+"/staged",os.O_WRONLY|os.O_CREATE|os.O_TRUNC,0600);if e!=nil{http.Error(w,"open",500);return}
  _,e=io.Copy(f,http.MaxBytesReader(w,r.Body,1<<20));if e==nil{e=f.Sync()};ce:=f.Close();if e!=nil||ce!=nil{http.Error(w,"write",500);return}
  if os.Rename(root+"/staged",root+"/payload")!=nil{http.Error(w,"rename",500);return};syncDir(root);w.WriteHeader(204)
 case "/rename":
  if r.Method!="POST" {w.WriteHeader(405);return};if os.Rename(root+"/payload",root+"/retained")!=nil{http.Error(w,"rename",500);return};syncDir(root);w.WriteHeader(204)
 case "/download":
  f,e:=os.Open(root+"/retained");if e!=nil{http.NotFound(w,r);return};defer f.Close();io.Copy(w,f)
 default: http.NotFound(w,r)
 }})
 if e:=http.ListenAndServe(":8080",nil);e!=nil{panic(e)}
}
func syncDir(root string){d,e:=os.Open(root);if e!=nil{panic(e)};defer d.Close();if e=d.Sync();e!=nil{panic(e)}}
`

func mountedFixtureCommand(ctx context.Context, e *imageEngine, stdin io.Reader, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "docker", append([]string{"--host", e.endpoint, "--config", e.configDir}, args...)...)
	command.Dir = e.configDir
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONFIG=" + e.configDir, "DOCKER_CONTEXT=", "LANG=C", "LC_ALL=C"}
	command.Stdin = stdin
	configureProvenanceProcess(command)
	command.WaitDelay = 2 * time.Second
	var output boundedCommandOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil || ctx.Err() != nil {
		killProvenanceProcess(command)
		return nil, errors.New("bounded mounted fixture Docker command failed")
	}
	return output.Bytes(), nil
}

func TestOwnedStorageDisposableDocker(t *testing.T) {
	if os.Getenv("NEROCD_MOUNTED_STORAGE_FIXTURE") != mountedFixtureOwner {
		t.Skip("requires approval for the exact mounted-20261023-16a7c disposable targets")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	engine := testImageEngine(t)
	if err := engine.verifyDaemon(ctx, mountedFixtureDaemon); err != nil {
		t.Fatal(err)
	}
	info, err := engine.docker(ctx, "info", "--format", "{{.OSType}} {{.Architecture}}")
	if err != nil || strings.TrimSpace(string(info)) != "linux aarch64" {
		t.Fatalf("approved fixture daemon platform changed: %s %v", info, err)
	}
	registryImage, err := engine.inspectImage(ctx, mountedFixtureRegistryImage)
	if err != nil || registryImage.ID != mountedFixtureRegistryImage {
		t.Fatal("approved immutable registry image unavailable")
	}
	registryName := mountedFixtureRegistry
	namespace := "ncd_bedd3011e12d4150b358f091ef914ec62145"
	operations := []string{"hop_mounted_a", "hop_mounted_b", "hop_mounted_failed"}
	names := []string{registryName, mountedFixtureForeign}
	for _, op := range operations {
		names = append(names, namespace+"_"+strings.TrimPrefix(domain.HostingDocumentHash(op), "sha256:")[:16])
	}
	for _, name := range names {
		raw, err := engine.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
		if err != nil || strings.TrimSpace(string(raw)) != "" {
			t.Fatalf("fixture refuses preexisting or unknown container %s", name)
		}
	}
	if present, err := engine.ownedVolumePresent(ctx, mountedFixtureVolume); err != nil || present {
		t.Fatal("fixture refuses preexisting or unknown volume")
	}
	for _, port := range []string{"28181", "28182", "28183"} {
		listener, err := net.Listen("tcp4", "127.0.0.1:"+port)
		if err != nil {
			t.Fatalf("approved fixture port %s occupied", port)
		}
		_ = listener.Close()
	}
	tags := []string{"nerocd-mounted-fixture-16a7c:a", "nerocd-mounted-fixture-16a7c:b", "127.0.0.1:28183/nerocd-mounted-16a7c:a", "127.0.0.1:28183/nerocd-mounted-16a7c:b"}
	for _, tag := range tags {
		raw, err := engine.docker(ctx, "image", "ls", "--quiet", "--no-trunc", tag)
		if err != nil || strings.TrimSpace(string(raw)) != "" {
			t.Fatalf("fixture refuses preexisting or unknown image alias %s", tag)
		}
	}
	preexistingImages, err := engine.docker(ctx, "image", "ls", "--quiet", "--no-trunc", "--filter", "label="+mountedFixtureLabel+"="+mountedFixtureOwner)
	if err != nil || strings.TrimSpace(string(preexistingImages)) != "" {
		t.Fatal("fixture refuses preexisting or unknown owned image identities")
	}
	ownedImages := map[string]bool{}
	ownedPlans := map[string]domain.HostingHealthy{}
	var storageRequest hostingOwnedVolumeRequest
	volumeMayExist := false
	// Cleanup targets are fixed and were absent before this fixture. Recover
	// exact IDs by these names if a command response was lost, then verify every
	// object's immutable image and ownership before removing it.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		defer func() {
			if err := engine.Close(); err != nil {
				t.Error(err)
			}
		}()
		for _, name := range names {
			raw, err := engine.docker(cleanup, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
			if err != nil {
				t.Errorf("cleanup cannot list exact container %s", name)
				continue
			}
			ids := strings.Fields(string(raw))
			if len(ids) > 1 {
				t.Errorf("cleanup ambiguous exact name %s", name)
				continue
			}
			if len(ids) == 0 {
				continue
			}
			id := ids[0]
			c, err := engine.inspectContainer(cleanup, id)
			if err != nil {
				t.Errorf("cleanup cannot inspect %s", id)
				continue
			}
			allowed := false
			if name == registryName {
				allowed = c.Image == mountedFixtureRegistryImage && c.Config.Labels[mountedFixtureLabel] == mountedFixtureOwner
			}
			if name == mountedFixtureForeign {
				allowed = ownedImages[c.Image] && c.Config.Labels[mountedFixtureLabel] == mountedFixtureOwner && c.Config.Labels[imageBindingLabel] == "foreign_mounted_fixture" && !c.State.Running
			}
			if h, ok := ownedPlans[name]; ok {
				if h.ContainerID == "" {
					h.ContainerID = id
				}
				allowed = storageContainerMatches(c, h) && ownedImages[c.Image]
			}
			if !allowed {
				t.Errorf("cleanup refuses mismatched fixture object %s", name)
				continue
			}
			if c.State.Running {
				if _, err := engine.mutate(cleanup, mountedFixtureDaemon, "container", "stop", "--time", "2", id); err != nil {
					t.Errorf("cleanup stop %s: %v", id, err)
					continue
				}
				c, err = engine.inspectContainer(cleanup, id)
				if err != nil || c.State.Running {
					t.Errorf("cleanup stop unconfirmed %s", id)
					continue
				}
			}
			if _, err := engine.mutate(cleanup, mountedFixtureDaemon, "container", "rm", id); err != nil {
				t.Errorf("cleanup remove %s: %v", id, err)
			}
		}
		for _, tag := range tags {
			raw, err := engine.docker(cleanup, "image", "ls", "--quiet", "--no-trunc", tag)
			if err != nil {
				t.Errorf("cleanup cannot resolve image alias %s", tag)
				continue
			}
			id := strings.TrimSpace(string(raw))
			if id == "" {
				continue
			}
			if !ownedImages[id] {
				t.Errorf("cleanup refuses foreign image alias %s", tag)
				continue
			}
			if _, err := engine.docker(cleanup, "image", "rm", tag); err != nil {
				t.Errorf("cleanup alias %s: %v", tag, err)
			}
		}
		for id := range ownedImages {
			raw, err := engine.docker(cleanup, "image", "ls", "--quiet", "--no-trunc", "--filter", "label="+mountedFixtureLabel+"="+mountedFixtureOwner)
			if err != nil {
				t.Error("cleanup cannot verify owned images")
				break
			}
			if !slices.Contains(strings.Fields(string(raw)), id) {
				continue
			}
			if _, err := engine.docker(cleanup, "image", "rm", id); err != nil {
				t.Errorf("cleanup exact image %s: %v", id, err)
			}
		}
		if volumeMayExist {
			storageRequest.KnownUsers = nil
			if present, err := engine.ownedVolumePresent(cleanup, mountedFixtureVolume); err != nil {
				t.Error(err)
			} else if present {
				if _, err := engine.ensureOwnedVolume(cleanup, storageRequest); err != nil {
					t.Errorf("cleanup volume identity/users unresolved: %v", err)
				} else if _, err := engine.mutate(cleanup, mountedFixtureDaemon, "volume", "rm", mountedFixtureVolume); err != nil {
					t.Error(err)
				}
			}
		}
		for _, name := range names {
			raw, err := engine.docker(cleanup, "container", "ls", "--all", "--quiet", "--filter", "name=^/"+name+"$")
			if err != nil || strings.TrimSpace(string(raw)) != "" {
				t.Errorf("fixture container absence unconfirmed %s", name)
			}
		}
		if present, err := engine.ownedVolumePresent(cleanup, mountedFixtureVolume); err != nil || present {
			t.Error("fixture volume absence unconfirmed")
		}
		if images, err := engine.docker(cleanup, "image", "ls", "--quiet", "--no-trunc", "--filter", "label="+mountedFixtureLabel+"="+mountedFixtureOwner); err != nil || strings.TrimSpace(string(images)) != "" {
			t.Error("fixture image absence unconfirmed")
		}
	})
	workspace := t.TempDir()
	source := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(source, []byte(mountedFixtureServer), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "server")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, source)
	build.Dir = workspace
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64", "GOWORK=off", "GO111MODULE=off", "GOPROXY=off")
	configureProvenanceProcess(build)
	build.WaitDelay = 2 * time.Second
	var buildOutput boundedCommandOutput
	build.Stdout, build.Stderr = &buildOutput, &buildOutput
	if err := build.Run(); err != nil || ctx.Err() != nil {
		killProvenanceProcess(build)
		t.Fatalf("fixture server compile: %v %s", err, buildOutput.Bytes())
	}
	server, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	serverHash := sha256.Sum256(server)
	var archive bytes.Buffer
	tarball := tar.NewWriter(&archive)
	if err := tarball.WriteHeader(&tar.Header{Name: "server", Mode: 0555, Size: int64(len(server))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarball.Write(server); err != nil {
		t.Fatal(err)
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	for i, version := range []string{"A", "B"} {
		raw, err := mountedFixtureCommand(ctx, engine, bytes.NewReader(archive.Bytes()), "image", "import", "--change", "LABEL "+mountedFixtureLabel+"="+mountedFixtureOwner, "--change", `CMD ["/server","`+version+`"]`, "-", tags[i])
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(string(raw))
		if !hostingEnvironmentImageID.MatchString(id) {
			t.Fatalf("invalid imported image ID %q", id)
		}
		ownedImages[id] = true
		t.Logf("owned image version=%s id=%s server_sha256=%x", version, id, serverHash)
	}
	registryRaw, err := engine.mutate(ctx, mountedFixtureDaemon, "container", "create", "--name", registryName, "--restart", "no", "--publish", "127.0.0.1:28183:5000", "--label", mountedFixtureLabel+"="+mountedFixtureOwner, "--tmpfs", "/var/lib/registry:rw,nosuid,nodev,size=128m", mountedFixtureRegistryImage)
	if err != nil {
		t.Fatal(err)
	}
	registryID := strings.TrimSpace(string(registryRaw))
	if !dockerObjectID.MatchString(registryID) {
		t.Fatal("registry identity missing")
	}
	t.Logf("owned registry id=%s image=%s", registryID, mountedFixtureRegistryImage)
	if _, err := engine.mutate(ctx, mountedFixtureDaemon, "container", "start", registryID); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for attempt := 0; attempt < 30; attempt++ {
		response, err := client.Get("http://127.0.0.1:28183/v2/")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if attempt == 29 {
			t.Fatal("owned registry readiness failed")
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := engine.docker(ctx, "image", "tag", tags[i], tags[i+2]); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.docker(ctx, "image", "push", tags[i+2]); err != nil {
			t.Fatal(err)
		}
	}
	storageRequest = ownedVolumeRequest(t)
	storageRequest.Storage.ProjectID = "proj_mounted_fixture"
	storageRequest.Storage.ResourceID = "res_mounted_fixture"
	storageRequest.Storage.BindingID = "bind_mounted_fixture_20261023_16a7c"
	storageRequest.Storage.DestinationID = "dest_mounted_fixture"
	storageRequest.Storage.ID, storageRequest.Storage.VolumeName = domain.HostingStorageIdentity(storageRequest.Storage.BindingID)
	storageRequest.Storage.Hash = storageRequest.Storage.DocumentHash()
	storageRequest.DaemonID = mountedFixtureDaemon
	storageRequest.OriginOperationID = operations[0]
	storageRequest.CreationNonce = domain.HostingStorageCreationNonce(storageRequest.Storage.Reference(), operations[0], mountedFixtureDaemon)
	snapshot := imageTestPlan(t).Snapshot
	snapshot.ResourceID, snapshot.BindingID, snapshot.DestinationID = storageRequest.Storage.ResourceID, storageRequest.Storage.BindingID, storageRequest.Storage.DestinationID
	snapshot.RuntimeNamespace = namespace
	snapshot.Storage, snapshot.PolicyVersion = storageRequest.Storage.Reference(), domain.HostingOwnedStoragePolicyVersion
	snapshot.ContainerPort, snapshot.HostPort = 8080, 28181
	snapshot.Readiness = domain.HostingReadiness{Path: "/ready", TimeoutSeconds: 3}
	snapshot.ImageReference = tags[2]
	releaseA, err := engine.Prepare(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !ownedImages[releaseA.ImageID] {
		t.Fatal("registry returned an unowned application image")
	}
	descriptor := domain.HostingStoragePlan{Reference: storageRequest.Storage.Reference(), VolumeName: mountedFixtureVolume, MountPath: storageRequest.Storage.MountPath, CreationOperationID: operations[0], CreationNonce: storageRequest.CreationNonce, DaemonID: mountedFixtureDaemon}
	planA, err := domain.CompileHostingOwnedStoragePlan(snapshot, releaseA, nil, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	storageRequest.OriginPlanHash = planA.Hash
	fresh := storageRequest
	fresh.CreatePermitted = true
	volumeMayExist = true
	materialized, err := engine.ensureOwnedVolume(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owned volume=%s daemon=%s labels=%d created_at=%s", materialized.VolumeName, materialized.DaemonID, len(materialized.Labels), materialized.CreatedAt)
	apply := func(op string, p domain.HostingApplyPlan) domain.HostingResult {
		t.Helper()
		h := domain.HostingHealthy{OperationID: op, Snapshot: p.Snapshot, Release: p.Release, PlanHash: p.Hash, Storage: &domain.HostingHealthyStorage{Plan: *p.Storage, CreationPlanHash: storageRequest.OriginPlanHash}}
		name := namespace + "_" + strings.TrimPrefix(domain.HostingDocumentHash(op), "sha256:")[:16]
		ownedPlans[name] = h
		result := engine.ApplyWithOwnedStorage(ctx, op, p, storageRequest, materialized)
		h.ContainerID = result.ContainerID
		ownedPlans[name] = h
		t.Logf("application operation=%s container=%s outcome=%s ready=%t restored=%t uncertain=%t", op, result.ContainerID, result.Outcome, result.Ready, result.Restored, result.Uncertain)
		return result
	}
	resultA := apply(operations[0], planA)
	if !resultA.Ready || resultA.Uncertain {
		t.Fatalf("A apply failed: %+v", resultA)
	}
	httpCall := func(port int, method, path string, body []byte) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("fixture HTTP %s %s status=%d err=%v", method, path, response.StatusCode, err)
		}
		return raw
	}
	payload := append([]byte("uploaded bytes survive replacement\x00\n"), bytes.Repeat([]byte{0x7f, 0, 0xff, 0x42}, 4096)...)
	httpCall(28181, http.MethodPost, "/upload", payload)
	httpCall(28181, http.MethodPost, "/rename", nil)
	assertBytes := func(port int) {
		t.Helper()
		if got := httpCall(port, http.MethodGet, "/download", nil); !bytes.Equal(got, payload) {
			t.Fatal("uploaded bytes changed")
		}
	}
	assertBytes(28181)
	healthyA := ownedPlans[names[2]]
	snapshot.ResourceGeneration++
	snapshot.ImageReference, snapshot.HostPort = tags[3], 28182
	releaseB, err := engine.Prepare(ctx, snapshot)
	if err != nil || !ownedImages[releaseB.ImageID] {
		t.Fatal("B image unavailable or unowned")
	}
	planB, err := domain.CompileHostingOwnedStoragePlan(snapshot, releaseB, &healthyA, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	resultB := apply(operations[1], planB)
	if !resultB.Ready || resultB.Uncertain {
		t.Fatalf("B apply failed: %+v", resultB)
	}
	if string(httpCall(28182, http.MethodGet, "/version", nil)) != "B" {
		t.Fatal("replacement version was not B")
	}
	assertBytes(28182)
	healthyB := ownedPlans[names[3]]
	failedSnapshot := snapshot
	failedSnapshot.HostPort = 28181
	failedSnapshot.Readiness = domain.HostingReadiness{Path: "/unready", TimeoutSeconds: 1}
	failedPlan, err := domain.CompileHostingOwnedStoragePlan(failedSnapshot, releaseA, &healthyB, []domain.HostingHealthy{healthyA}, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	failed := apply(operations[2], failedPlan)
	if failed.Outcome != "failed" || !failed.Restored || failed.Uncertain {
		t.Fatalf("known readiness failure did not restore B: %+v", failed)
	}
	assertBytes(28182)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err = newImageEngine(defaultImageDockerEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	observationOp := domain.HostingOperation{ID: operations[1], ProjectID: storageRequest.Storage.ProjectID, BindingID: snapshot.BindingID, ReconciliationHealthy: &healthyB, Plan: &planB}
	observed, err := engine.Observe(ctx, observationOp)
	if err != nil || !observed.Ready || observed.ContainerID != healthyB.ContainerID {
		t.Fatalf("fresh engine observation failed: %+v %v", observed, err)
	}
	assertBytes(28182)
	foreignRaw, err := engine.mutate(ctx, mountedFixtureDaemon, "container", "create", "--name", mountedFixtureForeign, "--restart", "no", "--label", mountedFixtureLabel+"="+mountedFixtureOwner, "--label", imageBindingLabel+"=foreign_mounted_fixture", "--mount", "type=volume,src="+mountedFixtureVolume+",dst="+storageRequest.Storage.MountPath+",volume-nocopy", releaseA.CanonicalImage)
	if err != nil {
		t.Fatal(err)
	}
	foreignID := strings.TrimSpace(string(foreignRaw))
	t.Logf("owned stopped foreign-user fixture id=%s", foreignID)
	if observed, err := engine.Observe(ctx, observationOp); err == nil || observed.Ready {
		t.Fatal("stopped foreign user was accepted")
	}
	foreign, err := engine.inspectContainer(ctx, foreignID)
	if err != nil || foreign.State.Running || foreign.Config.Labels[mountedFixtureLabel] != mountedFixtureOwner || foreign.Image != releaseA.ImageID {
		t.Fatal("foreign fixture identity changed")
	}
	if _, err := engine.mutate(ctx, mountedFixtureDaemon, "container", "rm", foreignID); err != nil {
		t.Fatal(err)
	}
	assertBytes(28182)
	digest := sha256.Sum256(payload)
	t.Logf("HTTP upload + atomic rename + replacement A→B + failed-readiness B restoration + fresh-engine observation preserved %d bytes sha256=%s; stopped foreign user rejected", len(payload), hex.EncodeToString(digest[:]))
}
