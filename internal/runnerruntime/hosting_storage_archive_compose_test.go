//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func composeArchiveTestPlan(t *testing.T) domain.HostingStorageArchivePlan {
	t.Helper()
	p := archiveEnginePlan(t)
	definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: `services:
  app:
    image: nginx:alpine
    environment:
      VIKUNJA_DATABASE_PASSWORD: ${POSTGRES_PASSWORD}
    volumes: [uploads:/uploads]
    depends_on:
      init: {condition: service_completed_successfully}
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes: [database:/var/lib/postgresql/data]
  init:
    image: nginx:alpine
    volumes: [uploads:/uploads:ro]
  reader:
    image: nginx:alpine
    volumes: [uploads:/uploads:ro]
`, PublicComponent: "app", ContainerPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	images := []domain.HostingStackImage{}
	components := []domain.HostingStackComponent{}
	for i, svc := range definition.Services {
		r := p.SourceHealthy.Release
		if svc.Name == "db" {
			r.CanonicalImage = "docker.io/library/postgres@sha256:" + strings.Repeat("a", 64)
		}
		r.ImageID = "sha256:" + strings.Repeat(strconv.Itoa(i+1), 64)
		r.Hash = ""
		r.Hash = domain.HostingDocumentHash(r)
		images = append(images, domain.HostingStackImage{Component: svc.Name, Release: r})
		components = append(components, domain.HostingStackComponent{Component: svc.Name, ContainerID: strings.Repeat(strconv.Itoa(i+1), 64), ImageID: r.ImageID, OperationID: "deployed", Running: svc.Name != "init"})
	}
	release := domain.HostingRelease{DaemonID: p.Helper.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	snap := p.SourceHealthy.Snapshot
	snap.Storage = domain.HostingStorageRef{}
	snap.ResourceGeneration = 1
	snap.ImageReference = ""
	snap.PolicyVersion = domain.HostingStackPolicyVersion
	snap.Stack = domain.HostingStackRef{Generation: 1, Hash: domain.HostingDocumentHash(definition)}
	volumes := []domain.HostingStackVolume{}
	for _, name := range definition.Volumes {
		volumes = append(volumes, domain.HostingStackVolume{Name: name, VolumeName: domain.HostingStackObjectID(snap.BindingID, "volume", name), CreationOperationID: "deployed", CreationNonce: strings.Repeat("a", 32), CreatedAt: "2026-10-24T00:00:00Z"})
	}
	plan, err := domain.CompileHostingStackPlan(snap, release, nil, domain.HostingStackPlan{Definition: definition, Images: images, Volumes: volumes, NetworkName: domain.HostingStackObjectID(snap.BindingID, "network", "private")})
	if err != nil {
		t.Fatal(err)
	}
	state := domain.HostingStackState{PublicComponent: "app", NetworkID: strings.Repeat("a", 64), Components: components, Volumes: volumes, ObservationComplete: true}
	healthy := domain.HostingHealthy{OperationID: "deployed", Snapshot: snap, Release: release, PlanHash: plan.Hash, Stack: &state}
	p.ComposeSource = &domain.HostingStorageArchiveComposeSource{Healthy: healthy, Plan: plan, Volume: volumes[1], Retained: []domain.HostingStorageArchiveStackProof{}}
	p.SourceHealthy = domain.HostingHealthy{}
	p.SourceStorage = domain.HostingStorage{}
	p.SourceMaterialization = domain.HostingStorageMaterialization{}
	p.Writers = []domain.HostingHealthy{}
	p.CompilerVersion = domain.HostingComposeStorageArchiveVersion
	p.PolicyVersion = p.CompilerVersion
	archiveHash(&p)
	if p.Validate() != nil {
		t.Fatal("compose plan rejected")
	}
	return p
}

func TestComposeArchiveFamilyProofAndObservedSubset(t *testing.T) {
	p := composeArchiveTestPlan(t)
	states := p.WriterStates()
	if len(states) != 3 {
		t.Fatal("database writer selected or upload users missing")
	}
	for _, mutate := range []func(*domain.HostingStorageArchivePlan){
		func(p *domain.HostingStorageArchivePlan) { p.CompilerVersion = domain.HostingStorageArchiveVersion },
		func(p *domain.HostingStorageArchivePlan) { p.SourceHealthy = p.ComposeSource.Healthy },
		func(p *domain.HostingStorageArchivePlan) {
			p.ComposeSource.Volume.CreationNonce = strings.Repeat("b", 32)
		},
		func(p *domain.HostingStorageArchivePlan) { p.ComposeSource.Volume.CreatedAt = "" },
		func(p *domain.HostingStorageArchivePlan) { p.Mode = "restore" },
		func(p *domain.HostingStorageArchivePlan) { p.DestinationID = "other" },
	} {
		copy := composeArchiveTestPlan(t)
		mutate(&copy)
		archiveHash(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid family proof accepted")
		}
	}
	if p.MatchesWriterStates(states[:2], true) {
		t.Fatal("missing current reader accepted")
	}
	// Several prior deployments remain candidate proofs while their containers may be absent.
	for i := 0; i < 5; i++ {
		h := p.ComposeSource.Healthy
		state := *h.Stack
		state.Components = append([]domain.HostingStackComponent(nil), state.Components...)
		h.OperationID = "old_" + strconv.Itoa(i)
		for j := range state.Components {
			state.Components[j].OperationID = h.OperationID
			state.Components[j].ContainerID = strings.Repeat(strconv.Itoa(i+4), 63) + strconv.Itoa(j)
		}
		h.Stack = &state
		p.ComposeSource.Retained = append(p.ComposeSource.Retained, domain.HostingStorageArchiveStackProof{Healthy: h, Plan: p.ComposeSource.Plan})
	}
	archiveHash(&p)
	if p.Validate() != nil || !p.MatchesWriterStates(states, true) {
		t.Fatal("absent retained users rejected")
	}
}

func TestComposeArchiveExactUsersAndResumeOnlyRunning(t *testing.T) {
	f := archiveFixture(t)
	p := composeArchiveTestPlan(t)
	f.p = p
	e := f.e
	c := p.ComposeSource
	path := filepath.Join(e.dockerRoot, "volumes", p.SourceVolumeName(), "_data")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{imageBindingLabel: p.SourceBindingID(), stackVolumeLabel: c.Volume.Name, stackOriginLabel: c.Volume.CreationOperationID, stackNonceLabel: c.Volume.CreationNonce}
	f.volumes[p.SourceVolumeName()] = map[string]any{"Name": p.SourceVolumeName(), "Driver": "local", "Scope": "local", "Labels": labels, "CreatedAt": c.Volume.CreatedAt, "Mountpoint": path}
	containers := map[string]stackInspectedContainer{}
	for _, state := range c.Healthy.Stack.Components {
		container := stackInspectedContainer{ID: state.ContainerID, Image: state.ImageID, Labels: map[string]string{imageBindingLabel: p.SourceBindingID(), imageOperationLabel: state.OperationID, imagePlanLabel: c.Plan.Hash, imageReleaseLabel: c.Healthy.Release.Hash, stackComponentLabel: state.Component}}
		container.State.Running = state.Running
		container.Networks = map[string]struct {
			NetworkID string   `json:"NetworkID"`
			Aliases   []string `json:"Aliases"`
		}{c.Plan.Stack.NetworkName: {NetworkID: c.Healthy.Stack.NetworkID, Aliases: []string{state.Component}}}
		for _, svc := range c.Plan.Stack.Definition.Services {
			if svc.Name == state.Component {
				for _, m := range svc.Mounts {
					container.Mounts = append(container.Mounts, imageMount{Type: "volume", Name: domain.HostingStackObjectID(p.SourceBindingID(), "volume", m.Volume), Destination: m.Target, RW: !m.ReadOnly})
				}
			}
		}
		if state.Component == "app" {
			raw := []byte(`{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"18080"}]}`)
			if json.Unmarshal(raw, &container.Ports) != nil {
				t.Fatal("ports")
			}
		}
		containers[state.ContainerID] = container
	}
	original := e.image.command
	mutations := []string{}
	foreign := false
	e.image.command = imageCommandFunc(func(ctx context.Context, binary string, args []string, dir string, env []string) ([]byte, error) {
		a := args[4:]
		if len(a) > 2 && a[0] == "container" && a[1] == "ls" && a[len(a)-1] == "volume="+p.SourceVolumeName() {
			ids := []string{}
			for id, container := range containers {
				if slices.ContainsFunc(container.Mounts, func(m imageMount) bool { return m.Name == p.SourceVolumeName() }) {
					ids = append(ids, id)
				}
			}
			if foreign {
				ids = append(ids, strings.Repeat("f", 64))
			}
			return []byte(strings.Join(ids, "\n")), nil
		}
		if len(a) > 2 && a[0] == "container" && a[1] == "inspect" {
			if container, ok := containers[a[2]]; ok {
				return json.Marshal(container)
			}
		}
		if len(a) > 2 && a[0] == "container" && (a[1] == "start" || a[1] == "stop") {
			id := a[len(a)-1]
			container, ok := containers[id]
			if !ok {
				return nil, errors.New("foreign mutation")
			}
			container.State.Running = a[1] == "start"
			containers[id] = container
			mutations = append(mutations, a[1]+":"+id)
			return []byte(id), nil
		}
		return original.RunEnvironment(ctx, binary, args, dir, env)
	})
	if err := e.Prepare(t.Context(), p); err != nil {
		t.Fatal("prepare", err)
	}
	states, err := e.InspectWriters(t.Context(), p)
	if err != nil || len(states) != 3 {
		t.Fatal("writers", err)
	}
	for _, w := range states {
		if w.Running {
			if err = e.StopWriter(t.Context(), p, w, f.before); err != nil {
				t.Fatal("stop", err)
			}
		}
	}
	for _, w := range states {
		if w.Running {
			if err = e.ResumeWriter(t.Context(), p, w, f.before); err != nil {
				t.Fatal("resume", err)
			}
		}
	}
	if len(mutations) != 4 {
		t.Fatal("initially stopped init mutated", mutations)
	}
	if !containers[c.Healthy.Stack.Components[1].ContainerID].State.Running {
		t.Fatal("unselected database stopped")
	}
	foreign = true
	if _, err = e.InspectWriters(t.Context(), p); err == nil {
		t.Fatal("unknown volume user admitted")
	}
	foreign = false
	readerID := c.Healthy.Stack.Components[3].ContainerID
	reader := containers[readerID]
	reader.Mounts[0].RW = true
	containers[readerID] = reader
	if _, err = e.InspectWriters(t.Context(), p); err == nil {
		t.Fatal("read-only mount drift admitted")
	}
}

func TestComposeArchiveDurableCoordinatorAndFreshResumeRecovery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "fresh recovery"}[fail], func(t *testing.T) {
			x, c, e, _ := archiveRuntimeFixture(t)
			p := composeArchiveTestPlan(t)
			x.p = p
			c.op.ProjectID = p.ProjectID
			c.op.ResourceID = p.SourceResourceID()
			c.op.BindingID = p.SourceBindingID()
			c.op.StorageArchive.Plan = p
			e.writers = p.WriterStates()
			required := []string{}
			for i := range e.writers {
				w := &e.writers[i]
				w.Running = w.ContainerID != p.ComposeSource.Healthy.Stack.Components[2].ContainerID
				if w.Running {
					required = append(required, w.ContainerID)
				}
			}
			if fail {
				e.failAt = "cleanup"
			}
			err := x.execute(c.op)
			if !fail {
				if err != nil {
					t.Fatal(err)
				}
				if len(x.j.Snapshot().StorageArchive) != 0 {
					t.Fatal("settled journal retained")
				}
				if c.op.StorageArchive.Result.ArchiveOutcome != "succeeded" || c.op.Result != nil {
					t.Fatal("archive implied application readiness")
				}
				return
			}
			if err == nil || len(x.j.Snapshot().StorageArchive) == 0 {
				t.Fatal("uncertain cleanup lost durable evidence")
			}
			if !storageArchiveConflict(x.j.Snapshot(), domain.HostingStorageRef{}, p.SourceBindingID(), "new_stack", "") {
				t.Fatal("unresolved Compose archive did not guard binding")
			}
			recovery := archiveRecoveryRuntime(t, x, c, e, required)
			if recovery.a.LeaseID == x.a.LeaseID || recovery.a.Fence == x.a.Fence {
				t.Fatal("old attempt authority reused")
			}
			if err = recovery.execute(c.op); err != nil {
				t.Fatal(err)
			}
			want := []string{"cleanup"}
			for _, id := range required {
				want = append(want, "resume:"+id)
			}
			if !slices.Equal(want, e.effects) {
				t.Fatal("recovery exceeded exact running-only resume obligations", e.effects)
			}
			if len(x.j.Snapshot().StorageArchive) != 0 {
				t.Fatal("recovery did not retire exact durable chain")
			}
		})
	}
}

func TestComposeArchiveWireAndJournalPreserveEnvironmentMapKeys(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(strconv.FormatBool(remote), func(t *testing.T) { testComposeArchiveWireAndJournalPreserveEnvironmentMapKeys(t, remote) })
	}
}
func testComposeArchiveWireAndJournalPreserveEnvironmentMapKeys(t *testing.T, remote bool) {
	p := composeArchiveTestPlan(t)
	if remote {
		p.RemoteTarget = &domain.BackupTargetSelection{TargetID: "target", Revision: 1}
		p.CompilerVersion = domain.HostingComposeStorageRemoteVersion
		p.PolicyVersion = p.CompilerVersion
	}

	old := p.ComposeSource.Healthy
	old.OperationID = "previous"
	oldState := *old.Stack
	oldState.Components = append([]domain.HostingStackComponent(nil), old.Stack.Components...)
	for i := range oldState.Components {
		oldState.Components[i].OperationID = old.OperationID
		oldState.Components[i].ContainerID = strings.Repeat("e", 63) + strconv.Itoa(i)
	}
	old.Stack = &oldState
	p.ComposeSource.Retained = append(p.ComposeSource.Retained, domain.HostingStorageArchiveStackProof{Healthy: old, Plan: p.ComposeSource.Plan})
	archiveHash(&p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: p.OperationID, RunID: "backup_" + p.OperationID, LeaseID: "lease", Attempt: 1, Fence: "fence"}
	op := domain.HostingOperation{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: p.SourceResourceID(), BindingID: p.SourceBindingID(), RunID: a.RunID, State: "dispatched", TaskMode: "backup", StorageArchive: &domain.HostingStorageArchive{Mode: "backup", BackupID: p.BackupID, Plan: p, Progress: []domain.HostingStorageArchiveProgressEvidence{}}}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response.Load().([]byte))
	}))
	defer server.Close()
	ctx := runnerclient.WithStorageArchiveTransport(t.Context(), runnerclient.StorageArchiveTransport{TrustedLoopbackHTTP: true})
	client := runnerclient.New(server.URL, "nonsecret_fixture_token")
	decoded, err := client.HostingStorageArchivePlan(ctx, a)
	if err != nil {
		t.Fatal("real serialized operation response rejected", err)
	}
	if decoded.StorageArchive.Plan.ComposeSource.Plan.Stack.Definition.Services[0].Environment["VIKUNJA_DATABASE_PASSWORD"] != "${POSTGRES_PASSWORD}" || decoded.StorageArchive.Plan.ComposeSource.Retained[0].Plan.Stack.Definition.Services[1].Environment["POSTGRES_PASSWORD"] != "${POSTGRES_PASSWORD}" {
		t.Fatal("case-sensitive environment map changed")
	}
	// These are wire decoder checks, independent of the immutable plan hash.
	for name, change := range map[string]func(string) string{
		"duplicate map key": func(s string) string {
			return strings.Replace(s, `"POSTGRES_PASSWORD":"${POSTGRES_PASSWORD}"`, `"POSTGRES_PASSWORD":"first","POSTGRES_PASSWORD":"${POSTGRES_PASSWORD}"`, 1)
		},
		"typed field alias": func(s string) string { return strings.Replace(s, `"compose_source":`, `"Compose_source":`, 1) },
		"unknown typed field": func(s string) string {
			return strings.Replace(s, `"compose_source":`, `"unknown_typed_field":true,"compose_source":`, 1)
		},
		"duplicate typed field": func(s string) string {
			return strings.Replace(s, `"mode":"backup"`, `"mode":"restore","mode":"backup"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := change(string(raw))
			if bad == string(raw) {
				t.Fatal("negative mutation missed fixture")
			}
			response.Store([]byte(bad))
			if _, err := client.HostingStorageArchivePlan(ctx, a); err == nil {
				t.Fatal("ambiguous response accepted")
			}
		})
	}
	response.Store(raw)
	dir := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if journal != nil {
			if err := journal.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	now := time.Now().UTC()
	entry := runner.JournalStorageArchive{ID: "compose_plan", Phase: "plan", Plan: p, CreatedAt: now, Attempt: runner.AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	if _, err = journal.AppendStorageArchive(entry); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = nil
	saved, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal("Compose archive journal restart rejected", err)
	}
	entries := journal.Snapshot().StorageArchive
	if len(entries) != 1 || entries[0].Plan.Hash != p.Hash || entries[0].Plan.ComposeSource.Plan.Stack.Definition.Services[1].Environment["POSTGRES_PASSWORD"] != "${POSTGRES_PASSWORD}" || entries[0].Plan.ComposeSource.Retained[0].Plan.Stack.Definition.Services[0].Environment["VIKUNJA_DATABASE_PASSWORD"] != "${POSTGRES_PASSWORD}" {
		t.Fatal("restart changed environment proof")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = nil
	for name, change := range map[string]func(string) string{
		"duplicate map key": func(s string) string {
			return strings.Replace(s, `"POSTGRES_PASSWORD":"${POSTGRES_PASSWORD}"`, `"POSTGRES_PASSWORD":"first","POSTGRES_PASSWORD":"${POSTGRES_PASSWORD}"`, 1)
		},
		"typed field alias": func(s string) string { return strings.Replace(s, `"compose_source":`, `"Compose_source":`, 1) },
		"unknown typed field": func(s string) string {
			return strings.Replace(s, `"compose_source":`, `"unknown_typed_field":true,"compose_source":`, 1)
		},
		"duplicate root field": func(s string) string { return strings.Replace(s, `"version":12`, `"version":12,"version":12`, 1) },
	} {
		t.Run("journal "+name, func(t *testing.T) {
			bad := change(string(saved))
			if bad == string(saved) {
				t.Fatal("negative journal mutation missed fixture")
			}
			if err := os.WriteFile(filepath.Join(dir, "journal.json"), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if opened, err := runner.OpenAttemptJournal(dir); err == nil {
				_ = opened.Close()
				t.Fatal("ambiguous saved journal accepted")
			}
		})
	}
}

func TestComposeArchiveHeldJournalComposesWithUnrelatedStackAndDatabaseBackup(t *testing.T) {
	x, c, e, dir := archiveRuntimeFixture(t)
	p := composeArchiveTestPlan(t)
	x.p = p
	c.op.ProjectID = p.ProjectID
	c.op.ResourceID = p.SourceResourceID()
	c.op.BindingID = p.SourceBindingID()
	c.op.StorageArchive.Plan = p
	e.writers = p.WriterStates()
	e.writers[0].Running = true
	e.failAt = "cleanup"
	if err := x.execute(c.op); err == nil {
		t.Fatal("cleanup failure did not retain held archive")
	}
	if len(x.j.Snapshot().StorageArchive) == 0 {
		t.Fatal("held archive evidence missing")
	}
	plan := stackTestPlan(t)
	snapshot := plan.Snapshot
	snapshot.ResourceID = "unrelated_stack_resource"
	snapshot.BindingID = "unrelated_stack_binding"
	stack := *plan.Stack
	stack.NetworkName = domain.HostingStackObjectID(snapshot.BindingID, "network", "private")
	var err error
	plan, err = domain.CompileHostingStackPlan(snapshot, plan.Release, nil, stack)
	if err != nil {
		t.Fatal(err)
	}
	if storageArchiveConflict(x.j.Snapshot(), domain.HostingStorageRef{}, snapshot.BindingID, "unrelated_stack_operation", "") {
		t.Fatal("held archive globally blocked unrelated stack")
	}
	now := time.Now().UTC()
	entry := runner.JournalHosting{ID: "unrelated_stack_intent", Phase: "apply_intent", OperationID: "unrelated_stack_operation", Plan: &plan, RuntimeEnvironment: true, RuntimeConfiguration: snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("a", 32), CreatedAt: now, Attempt: runner.AttemptIdentity{RunID: "deploy_unrelated_stack_operation", LeaseID: "other_stack_lease", Attempt: 1, Fence: "other_stack_fence", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}}
	if _, err = x.j.AppendHosting(entry); err != nil {
		t.Fatal("ordinary unrelated stack intent", err)
	}
	if err = x.j.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertWrapper := func(raw []byte, base int) {
		t.Helper()
		var wire struct {
			Version int `json:"version"`
			Base    int `json:"stack_base_version"`
		}
		if json.Unmarshal(raw, &wire) != nil || wire.Version != 23 || wire.Base != base {
			t.Fatalf("mixed journal wrapper version=%d base=%d", wire.Version, wire.Base)
		}
	}
	assertWrapper(raw, 12)
	opened, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal("held archive plus unrelated stack restart", err)
	}
	if len(opened.Snapshot().StorageArchive) == 0 || len(opened.Snapshot().Hosting) != 1 || opened.Snapshot().Hosting[0].Plan.Stack.Definition.Services[0].Environment["PASSWORD"] != "${PASSWORD:?required}" {
		t.Fatal("mixed restart changed stack environment proof")
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	reject := func(name string, raw []byte, old, replacement string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(string(raw), old, replacement, 1)
			if bad == string(raw) {
				t.Fatal("negative mutation missed mixed fixture")
			}
			if err := os.WriteFile(filepath.Join(dir, "journal.json"), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if opened, err := runner.OpenAttemptJournal(dir); err == nil {
				_ = opened.Close()
				t.Fatal("ambiguous sibling accepted before wrapper normalization")
			}
		})
	}
	reject("hosting typed alias", raw, `"operation_id":"unrelated_stack_operation"`, `"Operation_id":"unrelated_stack_operation"`)
	reject("hosting duplicate typed field", raw, `"operation_id":"unrelated_stack_operation"`, `"operation_id":"unrelated_stack_operation","operation_id":"unrelated_stack_operation"`)
	reject("hosting unknown field", raw, `"id":"unrelated_stack_intent"`, `"unknown_sibling":true,"id":"unrelated_stack_intent"`)
	reject("hosting duplicate environment key", raw, `"PASSWORD":"${PASSWORD:?required}"`, `"PASSWORD":"first","PASSWORD":"${PASSWORD:?required}"`)
	if err = os.WriteFile(filepath.Join(dir, "journal.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	opened, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A component logical backup is the other journal family that legitimately
	// contains a managed Compose definition. Keep its existing map grammar too.
	_, databasePlan, _, _ := componentBackupEngineFixture(t, "backup")
	databasePlan.SourcePlan.Snapshot.ResourceID = "unrelated_database_resource"
	databasePlan.SourcePlan.Snapshot.BindingID = "unrelated_database_binding"
	databasePlan.SourcePlan.Stack.NetworkName = domain.HostingStackObjectID(databasePlan.SourcePlan.Snapshot.BindingID, "network", "private")
	for i := range databasePlan.SourcePlan.Stack.Volumes {
		databasePlan.SourcePlan.Stack.Volumes[i].VolumeName = domain.HostingStackObjectID(databasePlan.SourcePlan.Snapshot.BindingID, "volume", databasePlan.SourcePlan.Stack.Volumes[i].Name)
	}
	databasePlan.SourcePlan.Hash = ""
	databasePlan.SourcePlan.Hash = domain.HostingDocumentHash(databasePlan.SourcePlan)
	databasePlan.Source.Snapshot = databasePlan.SourcePlan.Snapshot
	databasePlan.Source.PlanHash = databasePlan.SourcePlan.Hash
	databasePlan.Source.Stack.Volumes = databasePlan.SourcePlan.Stack.Volumes
	databasePlan.Operation.SourceResourceID = databasePlan.Source.Snapshot.ResourceID
	databasePlan.Hash = ""
	databasePlan.Hash = domain.HostingDocumentHash(databasePlan)
	if err = databasePlan.Validate(); err != nil {
		t.Fatal("unrelated component backup proof", err)
	}
	dbEntry, _ := backupJournalEntries(t, databasePlan)
	dbEntry.Request.ComponentVersion = domain.HostingDatabaseBackupComponentVersion
	if _, err = opened.AppendDatabaseBackup(dbEntry); err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertWrapper(raw, 18)
	opened, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal("archive/stack/component-backup mixed restart", err)
	}
	snapshotState := opened.Snapshot()
	if len(snapshotState.StorageArchive) == 0 || len(snapshotState.Hosting) != 1 || len(snapshotState.DatabaseBackup) != 1 || snapshotState.DatabaseBackup[0].Plan.SourcePlan.Stack.Definition.Services[1].Environment["POSTGRES_PASSWORD"] != "${PASSWORD}" {
		_ = opened.Close()
		t.Fatal("component backup map changed")
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	reject("component backup unknown field", raw, `"database_backup":[{`, `"database_backup":[{"unknown_sibling":true,`)
	reject("component backup typed alias", raw, `"source_component":"db"`, `"Source_component":"db"`)
	reject("component backup duplicate typed field", raw, `"source_component":"db"`, `"source_component":"db","source_component":"db"`)
	reject("component backup duplicate environment key", raw, `"POSTGRES_PASSWORD":"${PASSWORD}"`, `"POSTGRES_PASSWORD":"first","POSTGRES_PASSWORD":"${PASSWORD}"`)
}

func TestComposeRemoteArchiveFamilyRejectsCrossFamilyPlans(t *testing.T) {
	p := composeArchiveTestPlan(t)
	p.RemoteTarget = &domain.BackupTargetSelection{TargetID: "target", Revision: 1}
	p.CompilerVersion = domain.HostingComposeStorageRemoteVersion
	p.PolicyVersion = p.CompilerVersion
	archiveHash(&p)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing_target", "local_compiler", "local_policy", "remote_restore", "remote_source"} {
		t.Run(kind, func(t *testing.T) {
			v := p
			switch kind {
			case "missing_target":
				v.RemoteTarget = nil
			case "local_compiler":
				v.CompilerVersion = domain.HostingComposeStorageArchiveVersion
			case "local_policy":
				v.PolicyVersion = domain.HostingComposeStorageArchiveVersion
			case "remote_restore":
				v.Mode = "restore"
			case "remote_source":
				v.RemoteSource = &domain.BackupRemoteSource{TransferID: "transfer", ExpectedTargetRevision: 1}
			}
			archiveHash(&v)
			if v.Validate() == nil {
				t.Fatal("mixed family accepted")
			}
		})
	}
}

func TestComposeRemoteArchiveClaimRequiresUploadExecutionOptIn(t *testing.T) {
	x, c, e, _ := archiveRuntimeFixture(t)
	p := composeArchiveTestPlan(t)
	p.RemoteTarget = &domain.BackupTargetSelection{TargetID: "target", Revision: 1}
	p.CompilerVersion = domain.HostingComposeStorageRemoteVersion
	p.PolicyVersion = p.CompilerVersion
	archiveHash(&p)
	x.p = p
	c.op.ResourceID = p.SourceResourceID()
	c.op.BindingID = p.SourceBindingID()
	c.op.StorageArchive.Plan = p
	e.writers = p.WriterStates()
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: x.a.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: x.a.OperationID, HostingMode: p.Mode}}, Lease: x.lease, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: x.a.RunID}}
	reporter := &attemptReporter{journal: x.j, supervisor: x.s, authority: journalAttemptIdentity(x.a.RunID, x.lease, x.s)}
	config := &StorageArchiveExecutionConfig{Helper: p.Helper, TrustedLoopbackHTTP: true}
	if err := executeStorageArchiveClaimWithEngine(x.s, x.j, reporter, x.c, claim, config, e); err == nil {
		t.Fatal("remote archive executed without upload opt-in")
	}
	if len(x.j.Snapshot().StorageArchive) != 0 || e.closeCalls != 1 {
		t.Fatal("rejected claim wrote archive intent or leaked engine")
	}
}
