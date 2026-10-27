package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func ownedVolumeRequest(t *testing.T) hostingOwnedVolumeRequest {
	t.Helper()
	storage := domain.HostingStorage{
		ProjectID: "proj_volume", ResourceID: "res_volume", BindingID: "bind_volume", DestinationID: "dest_volume",
		Kind: "platform_managed", Driver: "local", MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain",
		Generation: 1, CreatedBy: "user_volume", CreatedAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
	}
	storage.ID, storage.VolumeName = domain.HostingStorageIdentity(storage.BindingID)
	storage.Hash = storage.DocumentHash()
	return hostingOwnedVolumeRequest{Storage: storage, DaemonID: "daemon-image-test", OriginOperationID: "hop_origin", OriginPlanHash: "sha256:" + strings.Repeat("d", 64), CreationNonce: strings.Repeat("a", 32)}
}

func ownedVolumeInspection(req hostingOwnedVolumeRequest) hostingInspectedVolume {
	return hostingInspectedVolume{Name: req.Storage.VolumeName, Driver: "local", Scope: "local", Labels: req.labels(), CreatedAt: "2026-10-23T00:00:00Z"}
}

func ownedVolumeKnownUser(req hostingOwnedVolumeRequest) domain.HostingHealthy {
	return domain.HostingHealthy{
		Snapshot:    domain.HostingSnapshot{BindingID: req.Storage.BindingID, ResourceID: req.Storage.ResourceID, DestinationID: req.Storage.DestinationID},
		OperationID: "hop_prior", ContainerID: strings.Repeat("c", 64), Release: imageTestRelease(), PlanHash: "sha256:" + strings.Repeat("e", 64),
	}
}

func ownedVolumeUserInspection(req hostingOwnedVolumeRequest, known domain.HostingHealthy) map[string]any {
	return map[string]any{
		"Id": known.ContainerID, "Image": known.Release.ImageID,
		"Config":     map[string]any{"Labels": map[string]string{imageBindingLabel: req.Storage.BindingID, imageOperationLabel: known.OperationID, imageReleaseLabel: known.Release.Hash, imagePlanLabel: known.PlanHash}},
		"State":      map[string]bool{"Running": false},
		"HostConfig": map[string]any{"NetworkMode": "default"},
		"Mounts":     []map[string]any{{"Type": "volume", "Name": req.Storage.VolumeName, "Driver": "local", "Destination": req.Storage.MountPath, "RW": true}},
	}
}

// ownedVolumeFake models retained daemon state separately from the create
// response, so a lost response can be reconciled without another mutation.
type ownedVolumeFake struct {
	req          hostingOwnedVolumeRequest
	volume       hostingInspectedVolume
	present      bool
	creates      int
	calls        []string
	users        []string
	userEvidence map[string]any
	hook         func(context.Context, []string) ([]byte, error, bool)
}

func (f *ownedVolumeFake) install(t *testing.T, e *imageEngine) {
	t.Helper()
	e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if name != "docker" || dir != e.configDir || len(args) < 5 || !slices.Equal(args[:4], []string{"--host", e.endpoint, "--config", e.configDir}) || !slices.Contains(env, "DOCKER_CONTEXT=") || !slices.Contains(env, "DOCKER_CONFIG="+e.configDir) {
			t.Fatalf("executor lost explicit daemon/config isolation: %s %v %s %v", name, args, dir, env)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("volume command lost bounded caller context")
		}
		args = args[4:]
		f.calls = append(f.calls, strings.Join(args, " "))
		if f.hook != nil {
			if raw, err, handled := f.hook(ctx, args); handled {
				return raw, err
			}
		}
		switch {
		case slices.Equal(args, []string{"info", "--format", "{{json .ID}}"}):
			return json.Marshal(f.req.DaemonID)
		case slices.Equal(args, []string{"volume", "ls", "--quiet", "--filter", "name=" + f.req.Storage.VolumeName}):
			if f.present {
				return []byte(f.req.Storage.VolumeName + "\n"), nil
			}
			return nil, nil
		case slices.Equal(args, []string{"volume", "inspect", f.req.Storage.VolumeName, "--format", "{{json .}}"}):
			if !f.present {
				t.Fatal("absent volume inspected")
			}
			return json.Marshal(f.volume)
		case slices.Equal(args, []string{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume=" + f.req.Storage.VolumeName}):
			return []byte(strings.Join(f.users, "\n")), nil
		case len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == strings.Repeat("c", 64) && args[3] == "--format" && args[4] == "{{json .}}":
			return json.Marshal(f.userEvidence)
		case len(args) >= 5 && args[0] == "volume" && args[1] == "create":
			if f.present || args[2] != "--driver" || args[3] != "local" || args[len(args)-1] != f.req.Storage.VolumeName {
				t.Fatalf("unsafe volume create: %v", args)
			}
			labels := map[string]string{}
			for i := 4; i < len(args)-1; i += 2 {
				if i+1 >= len(args)-1 || args[i] != "--label" {
					t.Fatalf("arbitrary mount or driver flags: %v", args)
				}
				key, value, ok := strings.Cut(args[i+1], "=")
				if !ok || labels[key] != "" {
					t.Fatalf("ambiguous create label: %v", args)
				}
				labels[key] = value
			}
			if !maps.Equal(labels, f.req.labels()) || len(labels) != 11 {
				t.Fatalf("ownership labels changed: %v", labels)
			}
			f.creates++
			f.present = true
			f.volume = ownedVolumeInspection(f.req)
			return []byte(f.req.Storage.VolumeName + "\n"), nil
		default:
			t.Fatalf("unexpected Docker action: %v", args)
			return nil, nil
		}
	})
}

func TestHostingOwnedVolumeCreationAndReadOnlyReconciliation(t *testing.T) {
	req := ownedVolumeRequest(t)
	req.CreatePermitted = true
	e := testImageEngine(t)
	fake := &ownedVolumeFake{req: req}
	fake.install(t, e)
	evidence, err := e.ensureOwnedVolume(t.Context(), req)
	if err != nil || fake.creates != 1 || evidence.Storage != req.Storage.Reference() || evidence.VolumeName != req.Storage.VolumeName || evidence.DaemonID != req.DaemonID || evidence.OriginOperationID != req.OriginOperationID || evidence.OriginPlanHash != req.OriginPlanHash || evidence.CreationNonce != req.CreationNonce || evidence.CreatedAt == "" || evidence.Driver != "local" || evidence.Scope != "local" || !maps.Equal(evidence.Labels, req.labels()) || len(evidence.Options) != 0 {
		t.Fatalf("missing exact materialization evidence: %+v creates=%d err=%v", evidence, fake.creates, err)
	}
	req.CreatePermitted = false
	replayed, err := e.ensureOwnedVolume(t.Context(), req)
	if err != nil || !reflect.DeepEqual(replayed, evidence) || fake.creates != 1 {
		t.Fatalf("read-only reconciliation changed retained storage: %+v creates=%d err=%v", replayed, fake.creates, err)
	}
	// Returned evidence owns its map; mutating it cannot authorize later calls.
	evidence.Labels[imageBindingLabel] = "foreign"
	if _, err := e.ensureOwnedVolume(t.Context(), req); err != nil {
		t.Fatalf("returned map aliases daemon or request metadata: %v", err)
	}
}

func TestHostingOwnedVolumeMissingAndFailedReadsNeverAuthorizeCreation(t *testing.T) {
	for _, mode := range []string{"permission-lost", "substring-only", "list-failed", "inspect-failed", "oversized-list", "too-many-volumes", "duplicate-list", "malformed-list"} {
		t.Run(mode, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			req.CreatePermitted = mode != "permission-lost" && mode != "substring-only"
			e := testImageEngine(t)
			fake := &ownedVolumeFake{req: req, present: mode == "inspect-failed", volume: ownedVolumeInspection(req)}
			fake.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if args[0] == "volume" && args[1] == "ls" {
					switch mode {
					case "substring-only":
						return []byte(req.Storage.VolumeName + "_foreign\n"), nil, true
					case "list-failed":
						return nil, errors.New("daemon disconnected"), true
					case "oversized-list":
						return []byte(strings.Repeat("v", (32<<10)+1)), nil, true
					case "too-many-volumes":
						lines := make([]string, 257)
						for i := range lines {
							lines[i] = "volume_" + strconv.Itoa(i)
						}
						return []byte(strings.Join(lines, "\n")), nil, true
					case "duplicate-list":
						return []byte("volume\nvolume\n"), nil, true
					case "malformed-list":
						return []byte("volume\r\n"), nil, true
					}
				}
				if args[0] == "volume" && args[1] == "inspect" && mode == "inspect-failed" {
					return nil, errors.New("inspection failed"), true
				}
				return nil, nil, false
			}
			fake.install(t, e)
			if _, err := e.ensureOwnedVolume(t.Context(), req); err == nil || fake.creates != 0 {
				t.Fatalf("read failure or missing permission created storage: creates=%d err=%v", fake.creates, err)
			}
		})
	}
}

func TestHostingOwnedVolumeRejectsInvalidMetadataBeforeDocker(t *testing.T) {
	for _, mutate := range []struct {
		name string
		fn   func(*hostingOwnedVolumeRequest)
	}{
		{"invalid-storage", func(r *hostingOwnedVolumeRequest) {
			r.Storage.MountPath = "/var/run/docker.sock"
			r.Storage.Hash = "stale"
		}},
		{"origin-option", func(r *hostingOwnedVolumeRequest) { r.OriginOperationID = "--foreign" }},
		{"origin-plan", func(r *hostingOwnedVolumeRequest) { r.OriginPlanHash = "sha256:short" }},
		{"nonce", func(r *hostingOwnedVolumeRequest) { r.CreationNonce = strings.Repeat("A", 32) }},
		{"daemon", func(r *hostingOwnedVolumeRequest) { r.DaemonID = "daemon\nforeign" }},
		{"foreign-binding", func(r *hostingOwnedVolumeRequest) {
			u := ownedVolumeKnownUser(*r)
			u.Snapshot.BindingID = "foreign"
			r.KnownUsers = []domain.HostingHealthy{u}
		}},
		{"foreign-resource", func(r *hostingOwnedVolumeRequest) {
			u := ownedVolumeKnownUser(*r)
			u.Snapshot.ResourceID = "foreign"
			r.KnownUsers = []domain.HostingHealthy{u}
		}},
		{"foreign-destination", func(r *hostingOwnedVolumeRequest) {
			u := ownedVolumeKnownUser(*r)
			u.Snapshot.DestinationID = "foreign"
			r.KnownUsers = []domain.HostingHealthy{u}
		}},
		{"foreign-daemon", func(r *hostingOwnedVolumeRequest) {
			u := ownedVolumeKnownUser(*r)
			u.Release.DaemonID = "foreign"
			r.KnownUsers = []domain.HostingHealthy{u}
		}},
		{"repeated-user", func(r *hostingOwnedVolumeRequest) {
			u := ownedVolumeKnownUser(*r)
			r.KnownUsers = []domain.HostingHealthy{u, u}
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			mutate.fn(&req)
			e := testImageEngine(t)
			e.command = imageCommandFunc(func(context.Context, string, []string, string, []string) ([]byte, error) {
				t.Fatal("invalid metadata reached Docker")
				return nil, nil
			})
			if _, err := e.ensureOwnedVolume(t.Context(), req); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}

func TestHostingOwnedVolumeRejectsForeignPolicyAndMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"name", "driver", "scope", "bind-options", "foreign-label", "extra-label", "missing-label", "origin-plan", "nonce", "daemon-label", "created-at", "malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			req.CreatePermitted = true
			e := testImageEngine(t)
			volume := ownedVolumeInspection(req)
			switch mode {
			case "name":
				volume.Name += "_foreign"
			case "driver":
				volume.Driver = "nfs"
			case "scope":
				volume.Scope = "global"
			case "bind-options":
				volume.Options = map[string]string{"type": "none", "device": "/home", "o": "bind"}
			case "foreign-label":
				volume.Labels[imageBindingLabel] = "foreign"
			case "extra-label":
				volume.Labels["extra"] = "foreign"
			case "missing-label":
				delete(volume.Labels, hostingStorageLabelPrefix+"id")
			case "origin-plan":
				volume.Labels[hostingStorageLabelPrefix+"origin_plan"] = "sha256:" + strings.Repeat("f", 64)
			case "nonce":
				volume.Labels[hostingStorageLabelPrefix+"creation_nonce"] = strings.Repeat("b", 32)
			case "daemon-label":
				volume.Labels[hostingStorageLabelPrefix+"daemon"] = "foreign"
			case "created-at":
				volume.CreatedAt = ""
			}
			fake := &ownedVolumeFake{req: req, present: true, volume: volume}
			fake.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if args[0] == "volume" && args[1] == "inspect" {
					if mode == "malformed" {
						return []byte(`{"Name":`), nil, true
					}
					if mode == "oversized" {
						return []byte(strings.Repeat(" ", (64<<10)+1)), nil, true
					}
				}
				return nil, nil, false
			}
			fake.install(t, e)
			if _, err := e.ensureOwnedVolume(t.Context(), req); err == nil || fake.creates != 0 {
				t.Fatalf("foreign metadata adopted or changed: creates=%d err=%v", fake.creates, err)
			}
		})
	}
}

func TestHostingOwnedVolumeChecksStoppedUsersAndExactMounts(t *testing.T) {
	for _, mode := range []string{"allowed-stopped", "privileged", "capability", "host-network", "extra-bind", "foreign-stopped", "forged-binding", "image", "id", "mount-name", "mount-path", "mount-driver", "bind", "read-only", "missing-mount", "duplicate-mount", "malformed-user", "oversized-user", "malformed-user-list", "too-many-users"} {
		t.Run(mode, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			known := ownedVolumeKnownUser(req)
			req.KnownUsers = []domain.HostingHealthy{known}
			user := ownedVolumeUserInspection(req, known)
			mounts := user["Mounts"].([]map[string]any)
			switch mode {
			case "privileged":
				user["HostConfig"].(map[string]any)["Privileged"] = true
			case "capability":
				user["HostConfig"].(map[string]any)["CapAdd"] = []string{"SYS_ADMIN"}
			case "host-network":
				user["HostConfig"].(map[string]any)["NetworkMode"] = "host"
			case "extra-bind":
				user["Mounts"] = append(mounts, map[string]any{"Type": "bind", "Destination": "/host", "RW": true})
			case "forged-binding":
				user["Config"].(map[string]any)["Labels"].(map[string]string)[imageBindingLabel] = "foreign"
			case "image":
				user["Image"] = "sha256:" + strings.Repeat("f", 64)
			case "id":
				user["Id"] = strings.Repeat("f", 64)
			case "mount-name":
				mounts[0]["Name"] = "foreign"
			case "mount-path":
				mounts[0]["Destination"] = "/other"
			case "mount-driver":
				mounts[0]["Driver"] = "nfs"
			case "bind":
				mounts[0]["Type"] = "bind"
			case "read-only":
				mounts[0]["RW"] = false
			case "missing-mount":
				user["Mounts"] = nil
			case "duplicate-mount":
				user["Mounts"] = append(mounts, maps.Clone(mounts[0]))
			}
			e := testImageEngine(t)
			fake := &ownedVolumeFake{req: req, present: true, volume: ownedVolumeInspection(req), users: []string{known.ContainerID}, userEvidence: user}
			if mode == "foreign-stopped" {
				fake.users = []string{strings.Repeat("f", 64)}
			}
			fake.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if args[0] == "container" && args[1] == "inspect" {
					if mode == "malformed-user" {
						return []byte(`{"Id":`), nil, true
					}
					if mode == "oversized-user" {
						return []byte(strings.Repeat(" ", (256<<10)+1)), nil, true
					}
				}
				if args[0] == "container" && args[1] == "ls" {
					if mode == "malformed-user-list" {
						return []byte("short-id"), nil, true
					}
					if mode == "too-many-users" {
						return []byte(strings.Repeat(known.ContainerID+"\n", 129)), nil, true
					}
				}
				return nil, nil, false
			}
			fake.install(t, e)
			_, err := e.ensureOwnedVolume(t.Context(), req)
			if (err == nil) != (mode == "allowed-stopped") || fake.creates != 0 {
				t.Fatalf("stopped-user/mount evidence mismatch: creates=%d err=%v", fake.creates, err)
			}
		})
	}
}

func TestHostingOwnedVolumeCreateResponseLossNeverRetriesOrDeletes(t *testing.T) {
	req := ownedVolumeRequest(t)
	req.CreatePermitted = true
	e := testImageEngine(t)
	fake := &ownedVolumeFake{req: req}
	fake.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
		if args[0] == "volume" && args[1] == "create" {
			fake.creates++
			fake.present = true
			fake.volume = ownedVolumeInspection(req)
			return nil, errors.New("response lost after creation"), true
		}
		return nil, nil, false
	}
	fake.install(t, e)
	if evidence, err := e.ensureOwnedVolume(t.Context(), req); !errors.Is(err, errHostingOwnedVolumeUncertain) || evidence.VolumeName != "" || fake.creates != 1 {
		t.Fatalf("lost response settled or retried: %+v creates=%d err=%v", evidence, fake.creates, err)
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "volume inspect") {
			t.Fatal("same call tried to settle uncertain creation")
		}
	}
	req.CreatePermitted = false
	if _, err := e.ensureOwnedVolume(t.Context(), req); err != nil || fake.creates != 1 {
		t.Fatalf("read-only replay did not recover existing exact volume: creates=%d err=%v", fake.creates, err)
	}
	fake.present = false
	if _, err := e.ensureOwnedVolume(t.Context(), req); !errors.Is(err, errHostingOwnedVolumeMissing) || fake.creates != 1 {
		t.Fatalf("lost retained volume recreated: creates=%d err=%v", fake.creates, err)
	}
}

func TestHostingOwnedVolumeAbsentForeignUserBlocksFreshCreation(t *testing.T) {
	req := ownedVolumeRequest(t)
	req.CreatePermitted = true
	e := testImageEngine(t)
	fake := &ownedVolumeFake{req: req, users: []string{strings.Repeat("f", 64)}}
	fake.install(t, e)
	if _, err := e.ensureOwnedVolume(t.Context(), req); err == nil || fake.creates != 0 {
		t.Fatalf("foreign container reference allowed fresh creation: creates=%d err=%v", fake.creates, err)
	}
}

func TestHostingOwnedVolumeWaitsForCanceledExecutor(t *testing.T) {
	for _, stage := range []string{"info", "create"} {
		t.Run(stage, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			req.CreatePermitted = true
			e := testImageEngine(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			returned := false
			fake := &ownedVolumeFake{req: req}
			fake.hook = func(commandContext context.Context, args []string) ([]byte, error, bool) {
				if stage == "info" && args[0] == "info" || stage == "create" && args[0] == "volume" && args[1] == "create" {
					if stage == "create" {
						fake.creates++
					}
					<-commandContext.Done()
					returned = true
					return nil, commandContext.Err(), true
				}
				return nil, nil, false
			}
			fake.install(t, e)
			_, err := e.ensureOwnedVolume(ctx, req)
			if !returned || !errors.Is(err, context.DeadlineExceeded) || stage == "info" && fake.creates != 0 || stage == "create" && (fake.creates != 1 || !errors.Is(err, errHostingOwnedVolumeUncertain)) {
				t.Fatalf("helper escaped executor or deadline boundary: joined=%v creates=%d err=%v", returned, fake.creates, err)
			}
		})
	}
}

func TestHostingOwnedVolumeDaemonChangesAndCancellationFailClosed(t *testing.T) {
	for _, mode := range []string{"wrong-daemon", "pre-create-daemon-change", "post-create-daemon-change", "final-daemon-change", "canceled-before-call", "canceled-before-create", "canceled-during-create", "canceled-during-inspection"} {
		t.Run(mode, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			req.CreatePermitted = true
			e := testImageEngine(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &ownedVolumeFake{req: req}
			infos := 0
			fake.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
				if args[0] == "info" {
					infos++
					if mode == "wrong-daemon" || mode == "pre-create-daemon-change" && infos == 2 || mode == "post-create-daemon-change" && fake.creates == 1 || mode == "final-daemon-change" && infos == 4 {
						return []byte(`"foreign-daemon"`), nil, true
					}
					if mode == "canceled-before-create" && infos == 2 {
						cancel()
					}
				}
				if args[0] == "volume" && args[1] == "create" && mode == "canceled-during-create" {
					fake.creates++
					cancel()
					return nil, context.Canceled, true
				}
				if args[0] == "volume" && args[1] == "inspect" && mode == "canceled-during-inspection" {
					cancel()
				}
				return nil, nil, false
			}
			fake.install(t, e)
			if mode == "canceled-before-call" {
				cancel()
			}
			_, err := e.ensureOwnedVolume(ctx, req)
			wantCreates := 0
			if mode == "post-create-daemon-change" || mode == "final-daemon-change" || mode == "canceled-during-create" || mode == "canceled-during-inspection" {
				wantCreates = 1
			}
			if err == nil || fake.creates != wantCreates || wantCreates == 1 && !errors.Is(err, errHostingOwnedVolumeUncertain) {
				t.Fatalf("authority loss did not preserve boundary: creates=%d want=%d err=%v", fake.creates, wantCreates, err)
			}
			if strings.HasPrefix(mode, "canceled") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
		})
	}
}

func TestHostingOwnedVolumeDisposableDocker(t *testing.T) {
	if os.Getenv("NEROCD_HOSTING_OWNED_VOLUME_FIXTURE") != "ncdv_4ad2d3cdfed3ba82f5a929cad42bad83" {
		t.Skip("requires explicit approval for the named local disposable volume")
	}
	req := ownedVolumeRequest(t)
	req.Storage.ProjectID = "proj_volume_engine_fixture"
	req.Storage.ResourceID = "res_volume_engine_fixture"
	req.Storage.BindingID = "bind_volume_engine_fixture_20261023_16a42"
	req.Storage.DestinationID = "dest_volume_engine_fixture"
	req.Storage.ID, req.Storage.VolumeName = domain.HostingStorageIdentity(req.Storage.BindingID)
	req.Storage.Hash = req.Storage.DocumentHash()
	req.DaemonID = "e37dd219-b13f-47dd-99ed-21d79442ed0c"
	req.OriginOperationID = "hop_volume_engine_fixture"
	req.OriginPlanHash = "sha256:" + strings.Repeat("d", 64)
	req.CreationNonce = "16a42000000000000000000000000000"
	if req.Storage.VolumeName != os.Getenv("NEROCD_HOSTING_OWNED_VOLUME_FIXTURE") {
		t.Fatal("fixture target changed")
	}
	e := testImageEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := e.verifyDaemon(ctx, req.DaemonID); err != nil {
		t.Fatal(err)
	}
	present, err := e.ownedVolumePresent(ctx, req.Storage.VolumeName)
	if err != nil || present {
		t.Fatalf("fixture refuses preexisting or unknown volume: present=%v err=%v", present, err)
	}
	// Only this exact previously absent object is eligible for fixture cleanup,
	// including an uncertain create response. No production code deletes storage.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		present, err := e.ownedVolumePresent(cleanup, req.Storage.VolumeName)
		if err != nil {
			t.Errorf("fixture cleanup could not determine presence: %v", err)
			return
		}
		if !present {
			return
		}
		if _, err := e.ensureOwnedVolume(cleanup, req); err != nil {
			t.Errorf("fixture cleanup refuses mismatched ownership or users: %v", err)
			return
		}
		if _, err := e.mutate(cleanup, req.DaemonID, "volume", "rm", req.Storage.VolumeName); err != nil {
			t.Errorf("exact fixture volume cleanup failed: %v", err)
			return
		}
		if present, err := e.ownedVolumePresent(cleanup, req.Storage.VolumeName); err != nil || present {
			t.Errorf("fixture absence not verified: present=%v err=%v", present, err)
		}
	})
	if _, err := e.ensureOwnedVolume(ctx, req); !errors.Is(err, errHostingOwnedVolumeMissing) {
		t.Fatalf("missing volume received replay creation authority: %v", err)
	}
	fresh := req
	fresh.CreatePermitted = true
	created, err := e.ensureOwnedVolume(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := e.ensureOwnedVolume(ctx, req)
	if err != nil || !reflect.DeepEqual(created, reconciled) || len(created.Labels) != 11 || len(created.Options) != 0 {
		t.Fatalf("real daemon reconciliation mismatch: %+v %+v err=%v", created, reconciled, err)
	}
	t.Logf("qualified exact volume=%s daemon=%s labels=%d driver=%s scope=%s; cleanup verifies ownership/users and absence", created.VolumeName, created.DaemonID, len(created.Labels), created.Driver, created.Scope)
}

func TestHostingOwnedVolumeChecksAuthorityAfterInspection(t *testing.T) {
	for _, mode := range []string{"revoked", "canceled", "valid"} {
		t.Run(mode, func(t *testing.T) {
			req := ownedVolumeRequest(t)
			req.CreatePermitted = true
			e := testImageEngine(t)
			fake := &ownedVolumeFake{req: req}
			fake.install(t, e)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checks := 0
			_, err := e.ensureOwnedVolumeWithAuthority(ctx, req, func(context.Context) error {
				checks++
				inspected := false
				for _, call := range fake.calls {
					if strings.HasPrefix(call, "container ls") {
						inspected = true
					}
				}
				if !inspected {
					t.Fatal("authority checked before fallible user inspection")
				}
				if mode == "revoked" {
					return errors.New("authority revoked")
				}
				if mode == "canceled" {
					cancel()
				}
				return nil
			})
			if checks != 1 || (err == nil) != (mode == "valid") || (fake.creates == 1) != (mode == "valid") {
				t.Fatalf("mode=%s checks=%d creates=%d err=%v", mode, checks, fake.creates, err)
			}
		})
	}
}
