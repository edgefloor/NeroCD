//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func composeArchiveTestPlan(t *testing.T) domain.HostingStorageArchivePlan {
	t.Helper()
	p := archiveEnginePlan(t)
	definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: `services:
  app:
    image: nginx:alpine
    volumes: [uploads:/uploads]
    depends_on:
      init: {condition: service_completed_successfully}
  db:
    image: postgres:16
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
