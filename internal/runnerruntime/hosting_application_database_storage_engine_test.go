//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
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

// This exercises the Docker-facing path, rather than the protocol coordinator:
// B is still attached while it is stopped, then A resumes with a fresh endpoint.
func TestApplicationDatabaseStorageStopsAttachedCandidateBeforeResumingPredecessor(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
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
			f.in.Proof.Members[0].ApplicationHealthy = previous
			id, volume := domain.HostingStorageIdentity(f.in.Plan.Snapshot.BindingID)
			storage := domain.HostingStorage{ID: id, ProjectID: f.in.Proof.ApplicationProjectID, ResourceID: f.in.Plan.Snapshot.ResourceID, BindingID: f.in.Plan.Snapshot.BindingID, DestinationID: f.in.Plan.Snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now()}
			storage.Hash = storage.DocumentHash()
			snapshot := f.in.Plan.Snapshot
			snapshot.Storage = storage.Reference()
			snapshot.PolicyVersion = domain.HostingApplicationDatabaseStoragePolicyVersion
			descriptor := domain.HostingStoragePlan{Reference: snapshot.Storage, VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: f.in.Proof.OperationID, CreationNonce: domain.HostingStorageCreationNonce(snapshot.Storage, f.in.Proof.OperationID, f.in.Plan.Release.DaemonID), DaemonID: f.in.Plan.Release.DaemonID}
			plan, err := domain.CompileHostingApplicationDatabaseStoragePlan(snapshot, f.in.Plan.Release, &previous, nil, f.in.Proof, descriptor)
			if err != nil {
				t.Fatal(err)
			}
			f.in.Plan = plan
			candidate.PlanHash = plan.Hash
			c := f.apps[candidate.ContainerID]
			c.Config.Labels[imagePlanLabel] = plan.Hash
			c.Name = "/" + applicationDatabaseName(f.in)
			c.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Name":"` + volume + `","Driver":"local","Destination":"/data/uploads","RW":true}`)}
			c.HostConfig.Mounts = []json.RawMessage{json.RawMessage(`{"Type":"volume","Source":"` + volume + `","Target":"/data/uploads","VolumeOptions":{"NoCopy":true}}`)}
			f.apps[c.ID] = c
			req := hostingOwnedVolumeRequest{Storage: storage, DaemonID: descriptor.DaemonID, OriginOperationID: descriptor.CreationOperationID, OriginPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce}
			f.e.storage = &req
			foreign := false
			f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				a := args[4:]
				if a[0] == "volume" && a[1] == "ls" {
					return []byte(volume), nil
				}
				if a[0] == "volume" && a[1] == "inspect" {
					return json.Marshal(hostingInspectedVolume{Name: volume, Driver: "local", Scope: "local", Labels: req.labels(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
				}
				if a[0] == "container" && a[1] == "ls" && a[len(a)-1] == "volume="+volume {
					if foreign {
						return []byte(strings.Repeat("f", 64)), nil
					}
					return []byte(candidate.ContainerID), nil
				}
				return f.command(ctx, name, args, dir, env)
			})
			old := f.apps[previous.ContainerID]
			old.HostConfig.PortBindings = c.HostConfig.PortBindings
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
			_, endpoint, err := f.e.ResumeStoragePredecessor(ctx, f.in, candidate, f.before)
			if err != nil || endpoint == nil || !endpoint.Present || endpoint.Endpoint.EndpointID == f.in.Proof.Members[0].Receipt.Observation.Endpoint.EndpointID || !f.apps[old.ID].State.Running || f.apps[candidate.ContainerID].State.Running {
				t.Fatalf("exact predecessor recovery failed: %v effects=%v", err, f.effects)
			}
		})
	}
}
