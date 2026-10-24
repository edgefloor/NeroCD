//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func databaseOriginObserverFixture(t *testing.T, engine string) (*databaseFake, *databaseNetworkOriginObserver, domain.HostingDatabaseNetworkCapturePlan, domain.HostingDatabaseNetworkOrigin) {
	t.Helper()
	f, credentials := databaseFixture(t, engine)
	result := f.apply(t.Context(), credentials)
	credentials.Close()
	if !result.Ready || result.Uncertain {
		t.Fatal("origin observer setup did not initialize database")
	}
	record, exists, err := f.config.readRecord(f.plan.Database.NetworkName + ".json")
	if err != nil || !exists || !record.InitReady {
		t.Fatal("origin observer setup has no initialized record")
	}
	var origin domain.HostingDatabaseNetworkOrigin
	raw, err := json.Marshal(record)
	if err != nil || json.Unmarshal(raw, &origin) != nil {
		t.Fatal("origin fixture decode failed")
	}
	name := databaseContainerName(f.req.OriginOperationID, f.plan)
	endpointID, mac := strings.Repeat("a", 64), "02:42:ac:1c:00:02"
	f.network["Containers"].(map[string]any)[result.ContainerID] = map[string]string{"Name": name, "EndpointID": endpointID, "MacAddress": mac, "IPv4Address": "172.28.0.2/16"}
	f.containers[result.ContainerID]["NetworkSettings"] = map[string]any{"Networks": map[string]any{f.plan.Database.NetworkName: map[string]any{"NetworkID": record.NetworkID, "EndpointID": endpointID, "IPAddress": "172.28.0.2", "MacAddress": mac, "Aliases": []string{}, "DNSNames": []string{name, result.ContainerID[:12]}, "IPAMConfig": map[string]any{}, "DriverOpts": map[string]any{}, "GwPriority": 0}}}
	h := domain.HostingHealthy{OperationID: f.req.OriginOperationID, ContainerID: result.ContainerID, Snapshot: f.plan.Snapshot, Release: f.plan.Release, PlanHash: f.plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *f.plan.Storage, CreationPlanHash: record.OriginPlan}}
	receipt := domain.HostingReceipt{ID: "materialized_origin_observer", BodyHash: "sha256:" + strings.Repeat("e", 64), CreatedAt: time.Now().UTC()}
	m := domain.HostingStorageMaterialization{StorageRef: f.plan.Snapshot.Storage, State: "materialized", CreationOperationID: record.OriginOperation, CreationPlanHash: record.OriginPlan, CreationNonce: record.VolumeNonce, DaemonID: record.Daemon, Labels: f.req.labels(), MaterializedReceipt: &receipt}
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(h.OperationID), SourceOperationID: h.OperationID, ProjectID: f.req.Storage.ProjectID, ResourceID: h.Snapshot.ResourceID, BindingID: h.Snapshot.BindingID, DestinationID: h.Snapshot.DestinationID, ServerID: h.Snapshot.ServerID, RunnerID: h.Snapshot.RunnerID, SourcePlan: f.plan, SourceHealthy: h, Storage: f.req.Storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: f.req.Storage.VolumeName, Driver: "local", Scope: "local", Labels: f.req.labels(), Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil || p.ValidateOrigin(origin) != nil {
		t.Fatal("invalid observer capture fixture")
	}
	f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
		t.Error("origin observer invoked credential readiness")
		return errors.New("observer must not probe")
	}
	f.calls = nil
	f.creates, f.starts, f.stops, f.removes, f.probes, f.networkCreates = 0, 0, 0, 0, 0, 0
	// This hook models all network references, including stopped containers that
	// Docker's network inspect endpoint map may omit.
	f.hook = func(_ context.Context, a []string) ([]byte, error, bool) {
		if len(a) > 2 && a[0] == "container" && a[1] == "ls" && strings.HasPrefix(a[len(a)-1], "network=") {
			var ids []string
			for id, container := range f.containers {
				settings, _ := container["NetworkSettings"].(map[string]any)
				networks, _ := settings["Networks"].(map[string]any)
				if networks[f.plan.Database.NetworkName] != nil {
					ids = append(ids, id)
				}
			}
			slices.Sort(ids)
			return []byte(strings.Join(ids, "\n")), nil, true
		}
		return nil, nil, false
	}
	base := f.e.command
	f.e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			t.Error("unbounded origin inspection")
			return nil, errors.New("inspection deadline unavailable")
		}
		if len(args) < 5 {
			t.Error("invalid origin command")
			return nil, errors.New("invalid command")
		}
		a := args[4:]
		allowed := slices.Equal(a, []string{"info", "--format", "{{json .ID}}"}) || len(a) > 1 && ((a[0] == "image" || a[0] == "container" || a[0] == "volume" || a[0] == "network") && a[1] == "inspect" || a[0] == "container" && a[1] == "ls")
		if !allowed {
			t.Error("origin observer attempted mutation, process exec or credential command")
			return nil, errors.New("non-observation command")
		}
		return base.RunEnvironment(ctx, name, args, dir, env)
	})
	o, err := newDatabaseNetworkOriginObserver(f.e, f.config)
	if err != nil {
		t.Fatal(err)
	}
	return f, o, p, origin
}

type observerTreeEntry struct {
	Mode fs.FileMode
	Data string
}

func observerTree(t *testing.T, roots ...string) map[string]observerTreeEntry {
	t.Helper()
	out := map[string]observerTreeEntry{}
	for i, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			var data []byte
			if info.Mode().IsRegular() {
				data, err = os.ReadFile(path)
			} else if info.Mode()&os.ModeSymlink != 0 {
				var target string
				target, err = os.Readlink(path)
				data = []byte(target)
			}
			if err != nil {
				return err
			}
			out[string(rune('0'+i))+"/"+relative] = observerTreeEntry{info.Mode(), string(data)}
			return nil
		})
		if err != nil {
			t.Fatal("observer fixture snapshot failed")
		}
	}
	return out
}

func assertObserverReadOnly(t *testing.T, f *databaseFake, before map[string]observerTreeEntry) {
	t.Helper()
	if f.creates != 0 || f.starts != 0 || f.stops != 0 || f.removes != 0 || f.probes != 0 || f.networkCreates != 0 {
		t.Error("observer performed mutation or credential readiness")
	}
	if !reflect.DeepEqual(before, observerTree(t, f.config.stateRoot, f.config.secretRoot)) {
		t.Error("observer changed retained state or secret files")
	}
}

func TestDatabaseNetworkOriginObserverBothEnginesReadOnly(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, o, p, want := databaseOriginObserverFixture(t, engine)
			before := observerTree(t, f.config.stateRoot, f.config.secretRoot)
			calls := 0
			got, err := o.ObserveDatabaseNetworkOrigin(t.Context(), p, func(ctx context.Context) error { calls++; return ctx.Err() })
			if err != nil || got != want || got.DocumentHash() != want.DocumentHash() || calls != 2 {
				t.Fatal("full initialized origin observation failed", err)
			}
			assertObserverReadOnly(t, f, before)
		})
	}
}

func TestDatabaseNetworkOriginObserverRejectsChangedIdentity(t *testing.T) {
	for _, change := range []string{"record_missing", "record_fifo", "lock_missing", "lock_symlink", "lock_hardlink", "lock_fifo", "lock_mode", "record_uninitialized", "record_nonce", "daemon", "volume_created", "volume_label", "volume_options", "image", "source_plan", "source_stopped", "unknown_endpoint", "stopped_network_user", "stopped_volume_user", "alias", "dns", "endpoint", "extra_network", "network_label", "plan_materialization"} {
		t.Run(change, func(t *testing.T) {
			f, o, p, _ := databaseOriginObserverFixture(t, "postgresql")
			id := p.SourceHealthy.ContainerID
			networkName := f.plan.Database.NetworkName
			endpoint := f.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[networkName].(map[string]any)
			recordPath := filepath.Join(f.config.stateRoot, networkName+".json")
			lockPath := filepath.Join(f.config.stateRoot, networkName+".lock")
			switch change {
			case "record_missing":
				if os.Remove(recordPath) != nil {
					t.Fatal("fixture remove failed")
				}
			case "lock_missing":
				if os.Remove(lockPath) != nil {
					t.Fatal("fixture remove failed")
				}
			case "lock_symlink":
				if os.Remove(lockPath) != nil || os.Symlink(recordPath, lockPath) != nil {
					t.Fatal("fixture symlink failed")
				}
			case "lock_hardlink":
				if os.Link(lockPath, filepath.Join(f.config.stateRoot, "foreign-lock-link")) != nil {
					t.Fatal("fixture hardlink failed")
				}
			case "record_fifo", "lock_fifo":
				path := recordPath
				if change == "lock_fifo" {
					path = lockPath
				}
				if os.Remove(path) != nil || syscall.Mkfifo(path, 0o600) != nil {
					t.Fatal("fixture FIFO failed")
				}
			case "lock_mode":
				if os.Chmod(lockPath, 0o644) != nil {
					t.Fatal("fixture mode failed")
				}
			case "record_uninitialized", "record_nonce":
				r, exists, err := f.config.readRecord(networkName + ".json")
				if err != nil || !exists {
					t.Fatal("fixture record unavailable")
				}
				if change == "record_uninitialized" {
					r.InitReady = false
				} else {
					r.Nonce = strings.Repeat("f", 32)
				}
				raw, err := json.Marshal(r)
				if err != nil || os.WriteFile(recordPath, raw, 0o600) != nil {
					t.Fatal("fixture record change failed")
				}
			case "daemon":
				base := f.hook
				f.hook = func(ctx context.Context, a []string) ([]byte, error, bool) {
					if a[0] == "info" {
						return []byte(`"foreign_daemon"`), nil, true
					}
					return base(ctx, a)
				}
			case "volume_created":
				f.volume.CreatedAt = "2026-10-20T00:00:00Z"
			case "volume_label":
				f.volume.Labels[hostingStorageLabelPrefix+"project"] = "foreign"
			case "volume_options":
				f.volume.Options = map[string]string{"type": "tmpfs"}
			case "image":
				f.containers[id]["Image"] = "sha256:" + strings.Repeat("f", 64)
			case "source_plan":
				f.containers[id]["Config"].(map[string]any)["Labels"].(map[string]string)[imagePlanLabel] = "sha256:" + strings.Repeat("f", 64)
			case "source_stopped":
				f.containers[id]["State"] = map[string]any{"Running": false, "Status": "exited"}
			case "unknown_endpoint":
				f.network["Containers"].(map[string]any)[strings.Repeat("f", 64)] = map[string]string{"EndpointID": strings.Repeat("e", 64), "IPv4Address": "172.28.0.8/16"}
			case "stopped_network_user":
				f.containers[strings.Repeat("f", 64)] = map[string]any{"State": map[string]any{"Running": false}, "NetworkSettings": map[string]any{"Networks": map[string]any{networkName: map[string]any{"NetworkID": f.network["Id"]}}}}
			case "stopped_volume_user":
				f.containers[strings.Repeat("f", 64)] = map[string]any{"State": map[string]any{"Running": false}}
			case "alias":
				endpoint["Aliases"] = []string{domain.HostingDatabaseBindingAlias(p.BindingID)}
			case "dns":
				endpoint["DNSNames"] = []string{"foreign", id[:12]}
			case "endpoint":
				endpoint["EndpointID"] = strings.Repeat("e", 64)
			case "extra_network":
				f.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["bridge"] = map[string]any{"NetworkID": strings.Repeat("f", 64)}
			case "network_label":
				f.network["Labels"].(map[string]string)["io.nerocd.database.creation_nonce"] = strings.Repeat("f", 32)
			case "plan_materialization":
				p.Creation.Receipt.ID = "foreign"
				p.Hash = p.DocumentHash()
			}
			before := observerTree(t, f.config.stateRoot, f.config.secretRoot)
			got, err := o.ObserveDatabaseNetworkOrigin(t.Context(), p, func(context.Context) error { return nil })
			if err == nil || got != (domain.HostingDatabaseNetworkOrigin{}) {
				t.Fatal("changed origin identity accepted")
			}
			assertObserverReadOnly(t, f, before)
		})
	}
}

func TestDatabaseNetworkOriginObserverAuthorityAndCancellation(t *testing.T) {
	for _, phase := range []string{"entry", "final", "final_cancel", "inspection_cancel", "lock_wait", "nil_callback", "already_canceled"} {
		t.Run(phase, func(t *testing.T) {
			f, o, p, _ := databaseOriginObserverFixture(t, "redis")
			before := observerTree(t, f.config.stateRoot, f.config.secretRoot)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			callback := func(context.Context) error {
				calls++
				if phase == "entry" || phase == "final" && calls == 2 {
					return errors.New("protected-canary")
				}
				if phase == "final_cancel" && calls == 2 {
					cancel()
				}
				return nil
			}
			if phase == "nil_callback" {
				callback = nil
			}
			if phase == "already_canceled" {
				cancel()
			}
			if phase == "inspection_cancel" {
				base := f.hook
				f.hook = func(ctx context.Context, a []string) ([]byte, error, bool) { cancel(); return base(ctx, a) }
			}
			if phase == "lock_wait" {
				lock, err := f.config.lock(t.Context(), p.BindingID)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if lock.Close() != nil {
						t.Error("fixture unlock failed")
					}
				}()
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 40*time.Millisecond)
				defer stop()
			}
			got, err := o.ObserveDatabaseNetworkOrigin(ctx, p, callback)
			if err == nil || got != (domain.HostingDatabaseNetworkOrigin{}) || strings.Contains(err.Error(), "protected-canary") {
				t.Fatal("expired/canceled observation returned evidence or protected error")
			}
			if (phase == "entry" || phase == "nil_callback" || phase == "already_canceled") && len(f.calls) != 0 {
				t.Fatal("revoked authority inspected Docker")
			}
			assertObserverReadOnly(t, f, before)
		})
	}
}

func TestDatabaseNetworkOriginObserverRechecksRetainedAndPhysicalIdentity(t *testing.T) {
	for _, change := range []string{"record", "source"} {
		t.Run(change, func(t *testing.T) {
			f, o, p, _ := databaseOriginObserverFixture(t, "postgresql")
			expected := observerTree(t, f.config.stateRoot, f.config.secretRoot)
			base := f.hook
			networkInspects := 0
			authority := 0
			f.hook = func(ctx context.Context, a []string) ([]byte, error, bool) {
				if len(a) > 1 && a[0] == "network" && a[1] == "inspect" {
					networkInspects++
					if change == "record" && networkInspects == 1 {
						name := p.SourcePlan.Database.NetworkName + ".json"
						r, exists, err := f.config.readRecord(name)
						if err != nil || !exists {
							t.Fatal("reread fixture unavailable")
						}
						r.Nonce = strings.Repeat("f", 32)
						raw, err := json.Marshal(r)
						if err != nil || os.WriteFile(filepath.Join(f.config.stateRoot, name), raw, 0o600) != nil {
							t.Fatal("reread fixture change failed")
						}
						// This deliberate fixture-side external change is the new baseline.
						expected = observerTree(t, f.config.stateRoot, f.config.secretRoot)
					}
					if change == "source" && networkInspects == 2 {
						f.containers[p.SourceHealthy.ContainerID]["State"] = map[string]any{"Running": false, "Status": "exited"}
					}
				}
				return base(ctx, a)
			}
			got, err := o.ObserveDatabaseNetworkOrigin(t.Context(), p, func(context.Context) error { authority++; return nil })
			if err == nil || got != (domain.HostingDatabaseNetworkOrigin{}) || authority != 1 {
				t.Fatal("changed identity reached final authority or returned origin")
			}
			if change == "source" && networkInspects != 2 {
				t.Fatal("physical identity was not reinspected")
			}
			assertObserverReadOnly(t, f, expected)
		})
	}
}

func TestDatabaseNetworkOriginObserverConstructorCreatesNothing(t *testing.T) {
	f, _, p, _ := databaseOriginObserverFixture(t, "redis")
	before := observerTree(t, f.config.stateRoot, f.config.secretRoot)
	if _, err := newDatabaseNetworkOriginObserver(nil, f.config); err == nil {
		t.Fatal("nil engine accepted")
	}
	for _, config := range []DatabaseEngineConfig{{}, {stateRoot: f.config.stateRoot, secretRoot: f.config.stateRoot}, {stateRoot: "relative", secretRoot: f.config.secretRoot}} {
		if _, err := newDatabaseNetworkOriginObserver(f.e, config); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	missing := filepath.Join(f.config.stateRoot, "missing-root")
	config := f.config
	config.stateRoot = missing
	if _, err := newDatabaseNetworkOriginObserver(f.e, config); err != nil {
		t.Fatal("shape-only constructor touched host state")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor created state")
	}
	var observer *databaseNetworkOriginObserver
	if got, err := observer.ObserveDatabaseNetworkOrigin(t.Context(), p, func(context.Context) error { return nil }); err == nil || got != (domain.HostingDatabaseNetworkOrigin{}) {
		t.Fatal("nil observer returned evidence")
	}
	assertObserverReadOnly(t, f, before)
}
