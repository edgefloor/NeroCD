package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

type mountedApplyFixture struct {
	engine     *imageEngine
	request    hostingOwnedVolumeRequest
	evidence   hostingOwnedVolumeEvidence
	plan       domain.HostingApplyPlan
	containers map[string]imageContainer
	volume     hostingInspectedVolume
	mode       string
	events     []string
	created    bool
	candidate  string
	cancel     context.CancelFunc
}

func mountedTestPort(t *testing.T) int {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func mountedContainer(h domain.HostingHealthy, running bool) imageContainer {
	var c imageContainer
	c.ID, c.Image = h.ContainerID, h.Release.ImageID
	c.Config.Labels = map[string]string{imageBindingLabel: h.Snapshot.BindingID, imageOperationLabel: h.OperationID, imageReleaseLabel: h.Release.Hash, imagePlanLabel: h.PlanHash}
	c.State.Running = running
	c.HostConfig.PortBindings = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{strconv.Itoa(h.Snapshot.ContainerPort) + "/tcp": {{HostIP: "127.0.0.1", HostPort: strconv.Itoa(h.Snapshot.HostPort)}}}
	if h.Storage != nil {
		c.Mounts = []imageMount{{Type: "volume", Name: h.Storage.Plan.VolumeName, Driver: "local", Destination: h.Storage.Plan.MountPath, RW: true}}
	}
	return c
}

func mountedTestFixture(t *testing.T, predecessor string) *mountedApplyFixture {
	t.Helper()
	f := &mountedApplyFixture{engine: testImageEngine(t), request: ownedVolumeRequest(t), candidate: strings.Repeat("d", 64), containers: map[string]imageContainer{}}
	r := &f.request
	r.OriginOperationID = "hop_origin"
	r.CreationNonce = domain.HostingStorageCreationNonce(r.Storage.Reference(), r.OriginOperationID, r.DaemonID)
	snapshot := imageTestPlan(t).Snapshot
	snapshot.ResourceID, snapshot.BindingID, snapshot.DestinationID = r.Storage.ResourceID, r.Storage.BindingID, r.Storage.DestinationID
	snapshot.PolicyVersion, snapshot.Storage = domain.HostingOwnedStoragePolicyVersion, r.Storage.Reference()
	snapshot.HostPort = mountedTestPort(t)
	snapshot.Readiness.TimeoutSeconds = 1
	descriptor := domain.HostingStoragePlan{Reference: r.Storage.Reference(), VolumeName: r.Storage.VolumeName, MountPath: r.Storage.MountPath, CreationOperationID: r.OriginOperationID, CreationNonce: r.CreationNonce, DaemonID: r.DaemonID}
	var previous *domain.HostingHealthy
	if predecessor != "" {
		previous = &domain.HostingHealthy{Snapshot: snapshot, OperationID: "hop_previous", ContainerID: strings.Repeat("c", 64), Release: imageTestRelease(), PlanHash: "sha256:" + strings.Repeat("f", 64), Storage: &domain.HostingHealthyStorage{Plan: descriptor, CreationPlanHash: r.OriginPlanHash}}
		previous.Snapshot.HostPort = mountedTestPort(t)
		if predecessor == "stateless" {
			previous.Snapshot.Storage = domain.HostingStorageRef{}
			previous.Snapshot.PolicyVersion = domain.HostingReleasePolicyVersion
			previous.Storage = nil
		}
		f.containers[previous.ContainerID] = mountedContainer(*previous, true)
	}
	var err error
	f.plan, err = domain.CompileHostingOwnedStoragePlan(snapshot, imageTestRelease(), previous, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.volume = ownedVolumeInspection(*r)
	f.evidence = hostingOwnedVolumeEvidence{Storage: r.Storage.Reference(), VolumeName: r.Storage.VolumeName, DaemonID: r.DaemonID, CreatedAt: f.volume.CreatedAt, OriginOperationID: r.OriginOperationID, OriginPlanHash: r.OriginPlanHash, CreationNonce: r.CreationNonce, Driver: "local", Scope: "local", Labels: r.labels()}
	f.install(t)
	return f
}

func (f *mountedApplyFixture) install(t *testing.T) {
	t.Helper()
	f.engine.command = imageCommandFunc(func(ctx context.Context, executable string, all []string, dir string, env []string) ([]byte, error) {
		if executable != "docker" || dir != f.engine.configDir || len(all) < 5 || !slices.Equal(all[:4], []string{"--host", f.engine.endpoint, "--config", f.engine.configDir}) || !slices.Contains(env, "DOCKER_CONTEXT=") {
			t.Fatalf("unpinned executor: %s %v", executable, all)
		}
		args := all[4:]
		switch args[0] {
		case "info":
			if f.mode == "daemon-changed-after-create" && f.created {
				return []byte(`"foreign-daemon"`), nil
			}
			return json.Marshal(f.request.DaemonID)
		case "image":
			if args[1] != "inspect" {
				t.Fatalf("unexpected image mutation: %v", args)
			}
			r := f.plan.Release
			return json.Marshal(inspectedImage{ID: r.ImageID, OS: r.OS, Architecture: r.Architecture})
		case "volume":
			switch args[1] {
			case "ls":
				if f.mode == "missing-volume" {
					return nil, nil
				}
				return []byte(f.request.Storage.VolumeName + "\n"), nil
			case "inspect":
				v := f.volume
				if f.mode == "replaced-after-create" && f.created {
					v.CreatedAt = "2026-10-23T00:00:01Z"
				}
				return json.Marshal(v)
			default:
				t.Fatalf("apply attempted volume mutation: %v", args)
			}
		case "container":
			switch args[1] {
			case "ls":
				if !slices.Contains(args, "--all") {
					t.Fatal("stopped users excluded")
				}
				filter := args[len(args)-1]
				ids := []string{}
				for id, c := range f.containers {
					match := filter == "label="+imageBindingLabel+"="+c.Config.Labels[imageBindingLabel] || filter == "id="+id
					for _, mount := range c.Mounts {
						match = match || filter == "volume="+mount.Name
					}
					if match {
						ids = append(ids, id)
					}
				}
				sort.Strings(ids)
				return []byte(strings.Join(ids, "\n")), nil
			case "inspect":
				c, ok := f.containers[args[2]]
				if !ok {
					return nil, errors.New("container missing")
				}
				f.events = append(f.events, "inspect "+c.ID+" "+strconv.FormatBool(c.State.Running))
				return json.Marshal(c)
			case "create":
				wantMount := "type=volume,src=" + f.request.Storage.VolumeName + ",dst=" + f.request.Storage.MountPath + ",volume-nocopy"
				mount := slices.Index(args, "--mount")
				if mount < 0 || mount+1 >= len(args) || args[mount+1] != wantMount || args[len(args)-1] != f.plan.Release.CanonicalImage {
					t.Fatalf("unsafe or missing fixed mount: %v", args)
				}
				for _, arg := range args {
					if strings.Contains(arg, "--volume") || strings.Contains(arg, "type=bind") {
						t.Fatalf("arbitrary volume flags: %v", args)
					}
				}
				for _, c := range f.containers {
					if c.State.Running {
						t.Fatal("candidate created before predecessor stop confirmation")
					}
				}
				f.created = true
				s := ownedStorageExecution{request: f.request}
				c := mountedContainer(*s.healthy("hop_candidate", f.candidate, f.plan), false)
				if f.mode == "foreign-candidate-mount" {
					c.Mounts[0].Type = "bind"
				}
				f.containers[c.ID] = c
				f.events = append(f.events, "create "+c.ID)
				if f.mode == "create-response-lost" {
					return nil, errors.New("response lost")
				}
				return []byte(c.ID), nil
			case "stop", "start", "rm":
				id := args[len(args)-1]
				c, ok := f.containers[id]
				if !ok {
					t.Fatalf("mutating missing container: %v", args)
				}
				f.events = append(f.events, args[1]+" "+id)
				switch args[1] {
				case "stop":
					if !(f.mode == "previous-still-running" && id != f.candidate || f.mode == "candidate-still-running" && id == f.candidate) {
						c.State.Running = false
					}
					f.containers[id] = c
					if f.mode == "stop-response-lost" || f.mode == "candidate-stop-response-lost" && id == f.candidate {
						return nil, errors.New("response lost")
					}
					if f.mode == "cancel-after-stop" {
						f.cancel()
					}
				case "start":
					for other, state := range f.containers {
						if other != id && state.State.Running {
							t.Fatal("two application writers admitted")
						}
					}
					c.State.Running = true
					f.containers[id] = c
					if f.mode == "start-response-lost" {
						return nil, errors.New("response lost")
					}
				case "rm":
					allowedID := f.candidate
					if f.mode == "cleanup" {
						allowedID = f.plan.PreviousHealthy.ContainerID
					}
					if !slices.Equal(args, []string{"container", "rm", allowedID}) || c.State.Running {
						t.Fatalf("unsafe candidate removal: %v", args)
					}
					delete(f.containers, id)
				}
				return nil, nil
			}
		}
		t.Fatalf("unexpected command: %v", args)
		return nil, nil
	})
}

func TestOwnedStorageMountedReplacementAndReadinessRestoration(t *testing.T) {
	for _, previous := range []string{"", "storage", "stateless"} {
		for _, failure := range []bool{false, true} {
			t.Run(previous+"/failure="+strconv.FormatBool(failure), func(t *testing.T) {
				f := mountedTestFixture(t, previous)
				if failure {
					f.plan.Snapshot.Readiness.Path = "/fail"
					var err error
					f.plan, err = domain.CompileHostingOwnedStoragePlan(f.plan.Snapshot, f.plan.Release, f.plan.PreviousHealthy, nil, *f.plan.Storage)
					if err != nil {
						t.Fatal(err)
					}
				}
				result := f.engine.ApplyWithOwnedStorage(t.Context(), "hop_candidate", f.plan, f.request, f.evidence)
				if result.Uncertain || result.Ready == failure || result.Restored != failure || (result.Outcome == "succeeded") == failure {
					t.Fatalf("replacement/restoration failed: %+v events=%v", result, f.events)
				}
				if previous != "" {
					prior := f.containers[f.plan.PreviousHealthy.ContainerID]
					if prior.State.Running != failure || f.plan.RetainPrevious || f.plan.Snapshot.HostPort == f.plan.PreviousHealthy.Snapshot.HostPort {
						t.Fatalf("distinct ports bypassed writer stop: %+v", prior)
					}
					stop := slices.Index(f.events, "stop "+prior.ID)
					proof := slices.Index(f.events, "inspect "+prior.ID+" false")
					create := slices.Index(f.events, "create "+f.candidate)
					if stop < 0 || proof <= stop || create <= proof {
						t.Fatalf("stop proof did not precede candidate creation: %v", f.events)
					}
				}
			})
		}
	}
}

func TestOwnedStorageAmbiguousOrForeignStateNeverRestartsPrevious(t *testing.T) {
	for _, mode := range []string{"missing-volume", "replaced-before-call", "replaced-after-create", "foreign-stopped-user", "foreign-previous-mount", "foreign-candidate-mount", "previous-still-running", "stop-response-lost", "create-response-lost", "start-response-lost", "daemon-changed-after-create", "candidate-still-running", "candidate-stop-response-lost", "cancel-after-stop", "running-retained"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "storage")
			f.mode = mode
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.cancel = cancel
			switch mode {
			case "replaced-before-call":
				f.volume.CreatedAt = "2026-10-23T00:00:01Z"
			case "foreign-stopped-user":
				foreign := f.containers[f.plan.PreviousHealthy.ContainerID]
				foreign.ID = strings.Repeat("e", 64)
				foreign.Config.Labels = map[string]string{imageBindingLabel: "foreign"}
				foreign.State.Running = false
				f.containers[foreign.ID] = foreign
			case "foreign-previous-mount":
				c := f.containers[f.plan.PreviousHealthy.ContainerID]
				c.Mounts[0].Type = "bind"
				f.containers[c.ID] = c
			case "candidate-still-running", "candidate-stop-response-lost":
				f.plan.Snapshot.Readiness.Path = "/fail"
				f.plan, _ = domain.CompileHostingOwnedStoragePlan(f.plan.Snapshot, f.plan.Release, f.plan.PreviousHealthy, nil, *f.plan.Storage)
			case "running-retained":
				retained := *f.plan.PreviousHealthy
				retained.ContainerID, retained.OperationID = strings.Repeat("e", 64), "hop_retained"
				f.containers[retained.ContainerID] = mountedContainer(retained, true)
				f.plan, _ = domain.CompileHostingOwnedStoragePlan(f.plan.Snapshot, f.plan.Release, f.plan.PreviousHealthy, []domain.HostingHealthy{retained}, *f.plan.Storage)
			}
			result := f.engine.ApplyWithOwnedStorage(ctx, "hop_candidate", f.plan, f.request, f.evidence)
			if !result.Uncertain || result.Ready || result.Restored {
				t.Fatalf("unproven state settled: %+v events=%v", result, f.events)
			}
			if slices.Contains(f.events, "start "+f.plan.PreviousHealthy.ContainerID) {
				t.Fatalf("uncertain state restarted previous: %v", f.events)
			}
			if (mode == "replaced-after-create" || mode == "foreign-candidate-mount" || mode == "daemon-changed-after-create") && slices.Contains(f.events, "start "+f.candidate) {
				t.Fatalf("candidate started before exact post-create proof: %v", f.events)
			}
		})
	}
}

func TestOwnedStorageLegacyEntryPointsAndInvalidAdmissionsRejectBeforeDocker(t *testing.T) {
	for _, mode := range []string{"legacy", "environment", "create-permission", "wrong-ref", "wrong-origin", "wrong-time", "wrong-labels", "caller-adds-user", "mixed-local", "mixed-environment", "missing-real-creator", "missing-real-created-at"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			switch mode {
			case "create-permission":
				f.request.CreatePermitted = true
			case "wrong-ref":
				f.request.Storage.Hash = "sha256:" + strings.Repeat("1", 64)
			case "wrong-origin":
				f.request.OriginOperationID = "hop_foreign"
			case "wrong-time":
				f.evidence.CreatedAt = ""
			case "wrong-labels":
				f.evidence.Labels[imageBindingLabel] = "foreign"
			case "caller-adds-user":
				f.request.KnownUsers = []domain.HostingHealthy{ownedVolumeKnownUser(f.request)}
			case "mixed-local":
				f.plan.Snapshot.LocalBuild.SourceGeneration = 1
			case "mixed-environment":
				f.plan.Snapshot.RuntimeConfiguration.Generation = 1
			case "missing-real-creator":
				f.request.Storage.CreatedBy = ""
			case "missing-real-created-at":
				f.request.Storage.CreatedAt = time.Time{}
			}
			f.engine.command = imageCommandFunc(func(context.Context, string, []string, string, []string) ([]byte, error) {
				t.Fatal("invalid admission reached Docker")
				return nil, nil
			})
			var result domain.HostingResult
			switch mode {
			case "legacy":
				result = f.engine.Apply(t.Context(), "hop_candidate", f.plan)
			case "environment":
				result = f.engine.ApplyWithRuntimeEnvironment(t.Context(), "hop_candidate", f.plan, nil, time.Time{})
			default:
				result = f.engine.ApplyWithOwnedStorage(t.Context(), "hop_candidate", f.plan, f.request, f.evidence)
			}
			if result.Outcome != "failed" || result.Uncertain || result.Ready {
				t.Fatalf("invalid admission accepted: %+v", result)
			}
		})
	}
}

func TestOwnedStorageObservationChecksHashCoveredMetadataAndMounts(t *testing.T) {
	for _, mode := range []string{"valid", "forged-project", "forged-hash", "extra-label", "foreign-mount", "missing-descriptor"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "storage")
			result := f.engine.ApplyWithOwnedStorage(t.Context(), "hop_candidate", f.plan, f.request, f.evidence)
			if !result.Ready || result.Uncertain {
				t.Fatalf("setup apply failed: %+v", result)
			}
			s := ownedStorageExecution{request: f.request}
			healthy := s.healthy("hop_candidate", result.ContainerID, f.plan)
			switch mode {
			case "forged-project":
				f.volume.Labels[hostingStorageLabelPrefix+"project"] = "forged_project"
			case "forged-hash":
				f.volume.Labels[hostingStorageLabelPrefix+"hash"] = "sha256:" + strings.Repeat("1", 64)
			case "extra-label":
				f.volume.Labels["extra"] = "foreign"
			case "foreign-mount":
				c := f.containers[result.ContainerID]
				c.Mounts[0].Type = "bind"
				f.containers[c.ID] = c
			case "missing-descriptor":
				healthy.Storage = nil
			}
			op := domain.HostingOperation{BindingID: f.request.Storage.BindingID, ReconciliationHealthy: healthy, Plan: &f.plan}
			before := len(f.events)
			observed, err := f.engine.Observe(t.Context(), op)
			if (err == nil) != (mode == "valid") || observed.Ready != (mode == "valid") {
				t.Fatalf("forged storage observation accepted: %+v %v", observed, err)
			}
			for _, event := range f.events[before:] {
				if !strings.HasPrefix(event, "inspect ") {
					t.Fatalf("observation mutated: %s", event)
				}
			}
		})
	}
}

func TestOwnedStorageCleanupVerifiesMountAndLeavesSharedVolume(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-mount", "replaced-project"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "storage")
			if result := f.engine.ApplyWithOwnedStorage(t.Context(), "hop_candidate", f.plan, f.request, f.evidence); !result.Ready {
				t.Fatalf("setup apply failed: %+v", result)
			}
			target := *f.plan.PreviousHealthy
			if mode == "foreign-mount" {
				c := f.containers[target.ContainerID]
				c.Mounts[0].Destination = "/foreign"
				f.containers[c.ID] = c
			}
			if mode == "replaced-project" {
				f.volume.Labels[hostingStorageLabelPrefix+"project"] = "foreign"
			}
			plan, err := domain.CompileHostingCleanupPlan(target, "hop_candidate")
			if err != nil {
				t.Fatal(err)
			}
			f.mode = "cleanup"
			before := len(f.events)
			result := f.engine.Cleanup(t.Context(), plan)
			if (result.Outcome == "succeeded") != (mode == "valid") || result.Uncertain != (mode != "valid") {
				t.Fatalf("cleanup identity decision incorrect: %+v", result)
			}
			if mode != "valid" {
				for _, event := range f.events[before:] {
					if !strings.HasPrefix(event, "inspect ") {
						t.Fatalf("foreign cleanup mutated: %s", event)
					}
				}
			}
			if f.volume.Name != f.request.Storage.VolumeName || !f.containers[f.candidate].State.Running {
				t.Fatal("cleanup changed shared storage or current writer")
			}
		})
	}
}
