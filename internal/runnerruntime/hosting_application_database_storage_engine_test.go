//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestApplicationDatabaseStorageMountScopeRemainsClosed(t *testing.T) {
	f, env := applicationDatabaseFixture(t, "redis", false)
	candidate, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
	if err != nil {
		t.Fatal(err)
	}
	actual := f.apps[candidate.ContainerID]
	descriptor := domain.HostingStoragePlan{VolumeName: "owned_uploads", MountPath: "/data/uploads"}
	actual.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Name":"owned_uploads","Driver":"local","Destination":"/data/uploads","RW":true}`)}
	actual.HostConfig.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Source":"owned_uploads","Target":"/data/uploads","VolumeOptions":{"NoCopy":true}}`)}
	if applicationDatabaseSafeContainer(actual) || !applicationDatabaseMountedSafe(actual, descriptor) {
		t.Fatal("storage mount widened stateless engine or rejected exact scope")
	}
	for _, bad := range []string{`{"Type":"bind","Source":"/tmp","Target":"/data/uploads"}`, `{"Type":"volume","Source":"owned_uploads","Target":"/data/uploads","VolumeOptions":{"NoCopy":false}}`, `{"Type":"volume","Source":"foreign","Target":"/data/uploads","VolumeOptions":{"NoCopy":true}}`, `{"Type":"volume","Source":"owned_uploads","Target":"/data/uploads","VolumeOptions":{"NoCopy":true,"Subpath":"other"}}`} {
		copy := actual
		copy.HostConfig.Mounts = []json.RawMessage{json.RawMessage(bad)}
		if applicationDatabaseMountedSafe(copy, descriptor) {
			t.Fatal("foreign mount configuration accepted")
		}
	}
	actual.HostConfig.Privileged = true
	if applicationDatabaseMountedSafe(actual, descriptor) {
		t.Fatal("mount proof weakened process isolation")
	}
}

// B is removed without its volume before A resumes with a fresh endpoint.
func TestApplicationDatabaseStorageStopsAttachedCandidateBeforeResumingPredecessor(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, candidate, req, foreign := applicationDatabaseStorageResumeFixture(t, engine)
			_, endpoint, err := f.e.ResumeStoragePredecessor(t.Context(), f.in, candidate, f.before)
			if err != nil || endpoint == nil || !endpoint.Present || endpoint.Endpoint.EndpointID == f.in.Proof.Members[0].Receipt.Observation.Endpoint.EndpointID || !f.apps[f.in.Plan.PreviousHealthy.ContainerID].State.Running || f.apps[candidate.ContainerID].State.Running {
				t.Fatalf("exact predecessor recovery failed: %v effects=%v", err, f.effects)
			}
			if len(req.KnownUsers) != 1 || req.KnownUsers[0].ContainerID != f.in.Plan.PreviousHealthy.ContainerID || req.KnownUsers[0].Storage == nil {
				t.Fatal("next C must know only the original owned storage release")
			}
			if strings.Join(f.effects[len(f.effects)-2:], ",") != "rm,start" {
				t.Fatal("failed B removal must precede A restart")
			}
			if _, err := f.e.image.ensureOwnedVolume(t.Context(), req); err != nil {
				t.Fatal("next C volume preflight rejected stopped failed B after successful resume", err)
			}
			if _, exists := f.apps[candidate.ContainerID]; exists {
				t.Fatal("successful resume retained failed B volume user")
			}
			*foreign = true
			if _, err := f.e.image.ensureOwnedVolume(t.Context(), req); err == nil {
				t.Fatal("next C preflight accepted genuine foreign user")
			}
		})
	}
}

func applicationDatabaseStorageResumeFixture(t *testing.T, engine string) (*applicationDatabaseFake, applicationDatabaseCandidate, hostingOwnedVolumeRequest, *bool) {
	t.Helper()
	f, env := applicationDatabaseFixture(t, engine, true)
	ctx := context.Background()
	candidate, err := f.e.CreateStopped(ctx, f.in, env, time.Now().Add(time.Minute), f.before)
	if err != nil {
		t.Fatal(err)
	}
	attach := applicationDatabaseAttachPlan(f, candidate)
	configured, err := f.e.ConfigureStoppedNetwork(ctx, f.in, candidate, attach, f.before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.e.StartAndObserve(ctx, f.in, candidate, attach, configured, f.before); err != nil {
		t.Fatal(err)
	}
	previous := f.in.Proof.Members[0].ApplicationHealthy
	previous.Snapshot.HostPort = f.in.Plan.Snapshot.HostPort
	previous.Snapshot.Readiness = f.in.Plan.Snapshot.Readiness
	id, volume := domain.HostingStorageIdentity(f.in.Plan.Snapshot.BindingID)
	storage := domain.HostingStorage{ID: id, ProjectID: f.in.Proof.ApplicationProjectID, ResourceID: f.in.Plan.Snapshot.ResourceID, BindingID: f.in.Plan.Snapshot.BindingID, DestinationID: f.in.Plan.Snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now()}
	storage.Hash = storage.DocumentHash()
	snapshot := f.in.Plan.Snapshot
	snapshot.Storage = storage.Reference()
	snapshot.PolicyVersion = domain.HostingApplicationDatabaseStoragePolicyVersion
	descriptor := domain.HostingStoragePlan{Reference: snapshot.Storage, VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: previous.OperationID, CreationNonce: domain.HostingStorageCreationNonce(snapshot.Storage, previous.OperationID, f.in.Plan.Release.DaemonID), DaemonID: f.in.Plan.Release.DaemonID}
	previous.Snapshot.Storage = storage.Reference()
	previous.Snapshot.PolicyVersion = domain.HostingApplicationDatabaseStoragePolicyVersion
	previous.Snapshot.ApplicationDatabase = snapshot.ApplicationDatabase
	previous.Storage = &domain.HostingHealthyStorage{Plan: descriptor, CreationPlanHash: previous.PlanHash}
	f.in.Proof.Members[0].ApplicationHealthy = previous
	plan, err := domain.CompileHostingApplicationDatabaseStoragePlan(snapshot, f.in.Plan.Release, &previous, nil, f.in.Proof, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.in.Plan = plan
	candidate.PlanHash = plan.Hash
	c := f.apps[candidate.ContainerID]
	c.Config.Labels[imagePlanLabel] = plan.Hash
	c.Name = "/" + applicationDatabaseName(f.in)
	network := c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName]
	network.DNSNames = []string{strings.TrimPrefix(c.Name, "/"), c.ID[:12]}
	c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName] = network
	c.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Name":"` + volume + `","Driver":"local","Destination":"/data/uploads","RW":true}`)}
	c.HostConfig.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Source":"` + volume + `","Target":"/data/uploads","VolumeOptions":{"NoCopy":true}}`)}
	f.apps[c.ID] = c
	req := hostingOwnedVolumeRequest{Storage: storage, DaemonID: descriptor.DaemonID, OriginOperationID: descriptor.CreationOperationID, OriginPlanHash: previous.PlanHash, CreationNonce: descriptor.CreationNonce, KnownUsers: []domain.HostingHealthy{previous}}
	f.e.storage = &req
	f.e.storagePredecessor = &previous
	foreign := false
	f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		a := args[4:]
		if a[0] == "volume" && a[1] == "ls" {
			return []byte(volume), nil
		}
		if a[0] == "volume" && a[1] == "inspect" {
			return json.Marshal(hostingInspectedVolume{Name: volume, Driver: "local", Scope: "local", Labels: req.labels(), CreatedAt: "2026-10-25T00:00:00Z"})
		}
		if a[0] == "container" && a[1] == "ls" && a[len(a)-1] == "volume="+volume {
			if foreign {
				return []byte(strings.Repeat("f", 64)), nil
			}
			var ids []string
			for _, id := range []string{previous.ContainerID, candidate.ContainerID} {
				if _, exists := f.apps[id]; exists {
					ids = append(ids, id)
				}
			}
			return []byte(strings.Join(ids, "\n")), nil
		}
		if a[0] == "container" && a[1] == "ls" && a[len(a)-1] == "id="+candidate.ContainerID {
			if _, exists := f.apps[candidate.ContainerID]; exists {
				return []byte(candidate.ContainerID), nil
			}
			return nil, nil
		}
		if a[0] == "volume" {
			t.Fatal("recovery attempted a volume mutation")
		}
		if a[0] == "container" && a[1] == "rm" && (len(a) != 3 || a[2] != candidate.ContainerID) {
			t.Fatal("candidate cleanup was not exact container rm without volume removal")
		}
		return f.command(ctx, name, args, dir, env)
	})
	old := f.apps[previous.ContainerID]
	old.HostConfig.PortBindings = c.HostConfig.PortBindings
	old.Mounts = c.Mounts
	old.HostConfig.Mounts = c.HostConfig.Mounts
	f.apps[old.ID] = old
	f.authorized = true
	if _, err = f.command(ctx, "docker", []string{"--host", "unix:///var/run/docker.sock", "--config", "fixture", "container", "stop", "--time", "10", old.ID}, "", nil); err != nil {
		t.Fatal(err)
	}
	effects := len(f.effects)
	foreign = true
	if _, err = f.e.StopStorageCandidate(ctx, f.in, candidate, f.before); err == nil || len(f.effects) != effects {
		t.Fatal("foreign writer allowed candidate mutation")
	}
	foreign = false
	if _, err = f.e.StopStorageCandidate(ctx, f.in, candidate, f.before); err != nil || f.apps[candidate.ContainerID].State.Running {
		t.Fatal("attached candidate was not stopped", err)
	}

	return f, candidate, req, &foreign
}

func TestApplicationDatabaseStorageResumeCleanupFailsClosed(t *testing.T) {
	for _, mode := range []string{"revoked_rm", "revoked_start", "rm_error", "rm_lost_response", "rm_unconfirmed", "absence_error", "cancel_after_rm", "foreign_after_rm", "start_lost_response", "inspect_error", "missing_candidate", "wrong_binding", "wrong_release", "wrong_mount", "changed_network"} {
		t.Run(mode, func(t *testing.T) {
			f, candidate, _, foreign := applicationDatabaseStorageResumeFixture(t, "redis")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			base := f.e.image.command
			f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				a := args[4:]
				if mode == "inspect_error" && a[0] == "container" && a[1] == "inspect" && a[2] == candidate.ContainerID {
					return nil, errors.New("inspection unavailable")
				}
				if mode == "absence_error" && a[0] == "container" && a[1] == "ls" && a[len(a)-1] == "id="+candidate.ContainerID {
					if _, exists := f.apps[candidate.ContainerID]; !exists {
						return nil, errors.New("listing unavailable")
					}
				}
				if a[0] == "container" && a[1] == "rm" {
					if mode == "rm_unconfirmed" {
						f.mutation("rm")
						return nil, nil
					}
					out, err := base.RunEnvironment(ctx, name, args, dir, env)
					switch mode {
					case "rm_lost_response":
						return nil, errors.New("lost removal response")
					case "cancel_after_rm":
						cancel()
					case "foreign_after_rm":
						*foreign = true
					}
					return out, err
				}
				return base.RunEnvironment(ctx, name, args, dir, env)
			})
			switch mode {
			case "rm_error":
				f.removeErr = errors.New("removal failed")
			case "start_lost_response":
				f.startErr = errors.New("lost start response")
			case "missing_candidate":
				delete(f.apps, candidate.ContainerID)
			case "wrong_binding", "wrong_release", "wrong_mount", "changed_network":
				c := f.apps[candidate.ContainerID]
				switch mode {
				case "wrong_binding":
					c.Config.Labels[imageBindingLabel] = "foreign"
				case "wrong_release":
					c.Config.Labels[imageReleaseLabel] = "sha256:" + strings.Repeat("f", 64)
				case "wrong_mount":
					c.HostConfig.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"bind","Source":"/foreign","Target":"/data/uploads"}`)}
				case "changed_network":
					n := c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName]
					n.EndpointID = strings.Repeat("f", 64)
					c.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName] = n
				}
				f.apps[c.ID] = c
			}
			calls := 0
			before := func(ctx context.Context) error {
				calls++
				if mode == "revoked_rm" && calls == 1 || mode == "revoked_start" && calls == 2 {
					return errors.New("current authority revoked")
				}
				return f.before(ctx)
			}
			prior := len(f.effects)
			observation, endpoint, err := f.e.ResumeStoragePredecessor(ctx, f.in, candidate, before)
			if err == nil || endpoint != nil || observation != "" {
				t.Fatal("ambiguous or unauthorized cleanup published resumed evidence")
			}
			expected := ""
			switch mode {
			case "revoked_start", "rm_error", "rm_lost_response", "rm_unconfirmed", "absence_error", "cancel_after_rm", "foreign_after_rm":
				expected = "rm"
			case "start_lost_response":
				expected = "rm,start"
			}
			if strings.Join(f.effects[prior:], ",") != expected {
				t.Fatalf("unexpected effects %v; want %s", f.effects[prior:], expected)
			}
			if f.apps[f.in.Plan.PreviousHealthy.ContainerID].State.Running != (mode == "start_lost_response") {
				t.Fatal("predecessor started across an unconfirmed cleanup boundary")
			}
		})
	}
}
