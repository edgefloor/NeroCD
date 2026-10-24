//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func upgradeCredentials(t *testing.T, in databaseNetworkUpgradeInput) *DatabaseCredentials {
	t.Helper()
	var values []DatabaseCredentialValue
	for i, ref := range in.Plan.Database.Credentials {
		v, err := NewDatabaseCredentialValue(ref, []byte(strings.Repeat(string(rune('k'+i)), 48)))
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	defer func() {
		for i := range values {
			values[i].Close()
		}
	}()
	c, err := newDatabaseNetworkUpgradeCredentials(in, values, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal("upgrade credential construction failed", err)
	}
	t.Cleanup(c.Close)
	return c
}

func upgradeNext(t *testing.T, previous domain.HostingApplyPlan, healthy domain.HostingHealthy, origin domain.HostingDatabaseNetworkOrigin) databaseNetworkUpgradeInput {
	t.Helper()
	s, db := previous.Snapshot, *previous.Database
	s.ResourceGeneration++
	db.Network = nil
	proof := domain.HostingDatabaseNetworkPlanInput{Origin: origin, MembershipRevision: 1}
	p, err := domain.CompileHostingDatabasePlanV2(s, previous.Release, healthy, previous, *previous.Storage, db, proof)
	if err != nil {
		t.Fatal(err)
	}
	return databaseNetworkUpgradeInput{Plan: p, PreviousPlan: previous, Proof: proof}
}

func upgradeEndpoint(f *databaseFake, id string, aliases []string, configured bool) {
	c := f.containers[id]
	n := c["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.plan.Database.NetworkName].(map[string]any)
	if configured {
		n["Aliases"] = aliases
		dns := append([]string{strings.TrimPrefix(c["Name"].(string), "/")}, aliases...)
		n["DNSNames"] = append(dns, id[:12])
	}
	if endpoint, ok := f.network["Containers"].(map[string]any)[id]; ok {
		c["State"].(map[string]any)["Pid"] = 1000 + f.creates
		n["NetworkID"], n["EndpointID"] = f.network["Id"], endpoint.(map[string]string)["EndpointID"]
		n["IPAddress"] = strings.Split(endpoint.(map[string]string)["IPv4Address"], "/")[0]
	} else {
		c["State"].(map[string]any)["Pid"] = 0
		// Stopped containers retain configured DNS and alias intent while
		// reporting no active endpoint or network ID.
		n["NetworkID"], n["EndpointID"], n["IPAddress"] = "", "", ""
	}
}

func upgradeFixture(t *testing.T, engine string) (*databaseFake, databaseNetworkUpgradeInput, *DatabaseCredentials) {
	t.Helper()
	f, credentials := databaseFixture(t, engine)
	first := f.apply(context.Background(), credentials)
	if !first.Ready {
		t.Fatal("initial v1 database failed", first)
	}
	previous := f.plan
	healthy := domain.HostingHealthy{OperationID: f.req.OriginOperationID, ContainerID: first.ContainerID, Snapshot: previous.Snapshot, Release: previous.Release, PlanHash: previous.Hash, Storage: &domain.HostingHealthyStorage{Plan: *previous.Storage, CreationPlanHash: f.req.OriginPlanHash}}
	record, exists, err := f.config.readRecord(previous.Database.NetworkName + ".json")
	if err != nil || !exists || !record.InitReady {
		t.Fatal("initialized original record unavailable", err)
	}
	upgradeEndpoint(f, first.ContainerID, nil, true)
	in := upgradeNext(t, previous, healthy, domain.HostingDatabaseNetworkOrigin(record))
	f.plan = in.Plan
	f.e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		if len(args) > 6 && args[4] == "container" && args[5] == "top" {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, strings.Join(args[4:], " "))
			if len(args) != 9 || args[7] != "-eo" || args[8] != "pid,uid" || f.containers[args[6]] == nil {
				t.Fatal("noncanonical read-only process inspection")
			}
			return fmt.Appendf(nil, "PID UID\n%d 999\n", f.containers[args[6]]["State"].(map[string]any)["Pid"]), nil
		}
		raw, err := f.command(ctx, name, args, dir, env)
		if err != nil {
			return raw, err
		}
		a := args[4:]
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(a) > 1 && a[0] == "container" {
			switch a[1] {
			case "create":
				var aliases []string
				for i := 0; i < len(a)-1; i++ {
					if a[i] == "--network-alias" {
						aliases = append(aliases, a[i+1])
					}
				}
				id := strings.TrimSpace(string(raw))
				upgradeEndpoint(f, id, aliases, true)
				f.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.plan.Database.NetworkName].(map[string]any)["DNSNames"] = nil
			case "start":
				id := a[len(a)-1]
				n := f.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.plan.Database.NetworkName].(map[string]any)
				aliases, _ := n["Aliases"].([]string)
				upgradeEndpoint(f, id, aliases, true)
			case "stop":
				upgradeEndpoint(f, a[len(a)-1], nil, false)
			}
		}
		return raw, nil
	})
	return f, in, upgradeCredentials(t, in)
}

func applyUpgrade(f *databaseFake, in databaseNetworkUpgradeInput, credentials *DatabaseCredentials, before func(context.Context) error) domain.HostingResult {
	return f.e.ApplyDatabaseNetworkUpgrade(context.Background(), "database_upgrade", in, f.req, f.evidence(), credentials, f.config, before)
}

func TestDatabaseNetworkUpgradePreservesDataIdentityAndObservation(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, credentials := upgradeFixture(t, engine)
			if _, err := NewDatabaseCredentials(in.Plan, credentials.values, time.Now().Add(time.Minute)); err == nil {
				t.Fatal("public v1 credential constructor admitted the private v2 family")
			}
			path := filepath.Join(f.config.stateRoot, in.Plan.Database.NetworkName+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			evidence := f.evidence()
			probe := f.config.readiness
			f.config.readiness = func(ctx context.Context, address string, p domain.HostingApplyPlan, values []DatabaseCredentialValue, readOnly, initialize bool, authority func(context.Context) error) error {
				if initialize {
					t.Fatal("upgrade attempted to reinitialize retained database data")
				}
				return probe(ctx, address, p, values, readOnly, initialize, authority)
			}
			result := applyUpgrade(f, in, credentials, f.before)
			if !result.Ready || result.Uncertain || f.creates != 2 || f.starts != 2 || f.stops != 1 || f.removes != 1 || f.networkCreates != 1 {
				t.Fatalf("upgrade failed or violated single-writer sequence: %+v counts %d/%d/%d/%d/%d", result, f.creates, f.starts, f.stops, f.removes, f.networkCreates)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || !sameOwnedVolumeEvidence(f.evidence(), evidence) {
				t.Fatal("upgrade changed original network bytes or volume data identity", err)
			}
			calls := len(f.calls)
			op := domain.HostingOperation{ID: "database_upgrade", Plan: &in.Plan, Result: &result}
			observed, err := f.e.ObserveDatabaseNetworkUpgrade(context.Background(), op, in, credentials, f.config)
			if err != nil || !observed.Ready || observed.ContainerID != result.ContainerID {
				t.Fatal("durable upgrade observation failed", err)
			}
			for _, call := range f.calls[calls:] {
				for _, mutation := range []string{"container create", "container start", "container stop", "container rm", "container exec", "network create", "volume create"} {
					if strings.HasPrefix(call, mutation) {
						t.Fatal("upgrade observation issued a mutation", call)
					}
				}
			}
			if _, err := f.e.ObserveDatabaseNetworkUpgrade(context.Background(), domain.HostingOperation{ID: op.ID, Plan: op.Plan}, in, credentials, f.config); err == nil {
				t.Fatal("observation adopted a candidate without its durable result")
			}
			if _, err := f.e.ObserveDatabase(context.Background(), op, credentials, f.config); err == nil {
				t.Fatal("legacy observation admitted v2")
			}
			if legacy := f.e.ApplyDatabase(context.Background(), op.ID, in.Plan, f.req, evidence, credentials, f.config, f.before); legacy.Ready || f.creates != 2 {
				t.Fatal("legacy apply admitted v2")
			}
			// Maintain the now-healthy empty v2 family using the exact original
			// predecessor, preserving its alias and initialized storage origin.
			healthy := domain.HostingHealthy{OperationID: op.ID, ContainerID: result.ContainerID, Snapshot: in.Plan.Snapshot, Release: in.Plan.Release, PlanHash: in.Plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *in.Plan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
			next := upgradeNext(t, in.Plan, healthy, in.Proof.Origin)
			f.plan = next.Plan
			nextCredentials := upgradeCredentials(t, next)
			maintained := f.e.ApplyDatabaseNetworkUpgrade(context.Background(), "database_maintenance", next, f.req, f.evidence(), nextCredentials, f.config, f.before)
			if !maintained.Ready || maintained.Uncertain || f.networkCreates != 1 || f.creates != 3 || f.stops != 2 {
				t.Fatal("empty v2 maintenance lost exact predecessor alias or volume", maintained)
			}
			all, _ := json.Marshal(maintained)
			for _, value := range []string{strings.Repeat("k", 48), strings.Repeat("l", 48)} {
				if bytes.Contains(all, []byte(value)) || strings.Contains(strings.Join(f.calls, "\n"), value) {
					t.Fatal("protected credentials leaked through results or commands")
				}
			}
		})
	}
}

func TestDatabaseNetworkUpgradeRejectsChangedPrerequisites(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"missing_record", "missing_after_precheck", "origin", "predecessor", "unknown_endpoint", "extra_network", "public_port", "v1_alias", "expired_credentials", "no_authority", "create_permitted", "members", "retired", "incompatible_host"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, in, credentials := upgradeFixture(t, engine)
				before := f.before
				path := filepath.Join(f.config.stateRoot, in.Plan.Database.NetworkName+".json")
				old := f.containers[in.Plan.PreviousHealthy.ContainerID]
				switch mode {
				case "missing_record":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "missing_after_precheck":
					f.hook = func(_ context.Context, a []string) ([]byte, error, bool) {
						if len(a) > 1 && a[0] == "container" && a[1] == "ls" {
							if err := os.Remove(path); err != nil {
								t.Fatal(err)
							}
							f.hook = nil
						}
						return nil, nil, false
					}
				case "origin":
					in.Proof.Origin.Nonce = strings.Repeat("0", 32)
				case "predecessor":
					in.PreviousPlan.Hash = "sha256:" + strings.Repeat("0", 64)
				case "unknown_endpoint":
					f.network["Containers"].(map[string]any)[strings.Repeat("f", 64)] = map[string]string{"EndpointID": strings.Repeat("e", 64), "IPv4Address": "172.28.0.8/16"}
				case "extra_network":
					old["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["foreign"] = map[string]any{}
				case "public_port":
					old["HostConfig"].(map[string]any)["PortBindings"] = map[string]any{"5432/tcp": []string{"0.0.0.0:5432"}}
				case "v1_alias":
					old["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[in.Plan.Database.NetworkName].(map[string]any)["Aliases"] = []string{in.Plan.Database.Network.Alias}
				case "expired_credentials":
					credentials.until = time.Now().Add(-time.Second)
				case "no_authority":
					before = nil
				case "create_permitted":
					f.req.CreatePermitted = true
				case "members":
					in.Proof.Members = []domain.HostingDatabaseNetworkMemberProof{{}}
				case "retired":
					in.Proof.Retired = []domain.HostingDatabaseNetworkRetirementProof{{}}
				case "incompatible_host":
					f.config.hostCheck = func(context.Context, *imageEngine) error { return errors.New("unsupported remapped host") }
				}
				result := applyUpgrade(f, in, credentials, before)
				if result.Ready || !result.Uncertain || f.creates != 1 || f.networkCreates != 1 || f.starts != 1 || f.stops != 0 {
					t.Fatal("invalid upgrade prerequisite caused a Docker effect", result)
				}
			})
		}
	}
}

func TestDatabaseNetworkUpgradePartialEffectsAndReplay(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"lost_create", "authority_after_stop", "readiness", "candidate_alias"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, in, credentials := upgradeFixture(t, engine)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				before := f.before
				expired := false
				switch mode {
				case "lost_create", "candidate_alias":
					command := f.e.command
					first := true
					f.e.command = imageCommandFunc(func(ctx context.Context, name string, a []string, dir string, env []string) ([]byte, error) {
						raw, err := command.RunEnvironment(ctx, name, a, dir, env)
						if len(a) > 5 && a[4] == "container" && a[5] == "create" && err == nil && first {
							first = false
							if mode == "lost_create" {
								return nil, errors.New("protected daemon error")
							}
							id := strings.TrimSpace(string(raw))
							f.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[in.Plan.Database.NetworkName].(map[string]any)["Aliases"] = []string{"foreign"}
						}
						return raw, err
					})
				case "authority_after_stop":
					f.hook = func(_ context.Context, a []string) ([]byte, error, bool) {
						if len(a) > 1 && a[0] == "container" && a[1] == "stop" {
							expired = true
						}
						return nil, nil, false
					}
					before = func(ctx context.Context) error {
						if expired {
							return errors.New("authority expired")
						}
						return f.before(ctx)
					}
				case "readiness":
					f.config.readiness = func(context.Context, string, domain.HostingApplyPlan, []DatabaseCredentialValue, bool, bool, func(context.Context) error) error {
						cancel()
						return errDatabaseReadiness
					}
				}
				result := f.e.ApplyDatabaseNetworkUpgrade(ctx, "database_upgrade", in, f.req, f.evidence(), credentials, f.config, before)
				if result.Ready || !result.Uncertain || f.removes != 0 || f.networkCreates != 1 {
					t.Fatal("partial upgrade lost uncertainty or retired predecessor prematurely", result)
				}
				if mode == "lost_create" {
					if f.creates != 2 {
						t.Fatal("lost-response fixture did not create exactly one candidate")
					}
					replayed := applyUpgrade(f, in, credentials, f.before)
					if !replayed.Ready || replayed.Uncertain || f.creates != 2 {
						t.Fatal("lost create response duplicated candidate or could not reconcile", replayed)
					}
				}
				if mode == "authority_after_stop" && (f.creates != 1 || f.starts != 1) {
					t.Fatal("expired authority reached a later effect")
				}
				if mode == "candidate_alias" && f.starts != 1 {
					t.Fatal("candidate alias mismatch reached writer start")
				}
			})
		}
	}
}

func TestDatabaseNetworkUpgradeReadOnlyProcessAndAliasInspection(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"wrong_uid", "missing_pid", "duplicate_pid", "extra_output", "unparseable", "restarted_pid", "missing_state_pid", "v2_alias"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, in, credentials := upgradeFixture(t, engine)
				result := applyUpgrade(f, in, credentials, f.before)
				if !result.Ready {
					t.Fatal(result)
				}
				container := f.containers[result.ContainerID]
				pid := container["State"].(map[string]any)["Pid"].(int)
				command := f.e.command
				f.e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
					raw, err := command.RunEnvironment(ctx, name, args, dir, env)
					if len(args) > 5 && args[4] == "container" && args[5] == "top" && err == nil {
						switch mode {
						case "wrong_uid":
							return fmt.Appendf(nil, "PID UID\n%d 0\n", pid), nil
						case "missing_pid":
							return []byte("PID UID\n2000 999\n"), nil
						case "duplicate_pid":
							return append(raw, fmt.Appendf(nil, "%d 999\n", pid)...), nil
						case "extra_output":
							return append(raw, []byte("2000 0\n")...), nil
						case "unparseable":
							return []byte("PID UID\nprotected malformed extra\n"), nil
						case "restarted_pid":
							container["State"].(map[string]any)["Pid"] = pid + 1
						}
					}
					return raw, err
				})
				if mode == "missing_state_pid" {
					container["State"].(map[string]any)["Pid"] = 0
				}
				if mode == "v2_alias" {
					container["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[in.Plan.Database.NetworkName].(map[string]any)["Aliases"] = []string{"foreign"}
				}
				calls := len(f.calls)
				op := domain.HostingOperation{ID: "database_upgrade", Plan: &in.Plan, Result: &result}
				if _, err := f.e.ObserveDatabaseNetworkUpgrade(context.Background(), op, in, credentials, f.config); err == nil {
					t.Fatal("changed observed process or deterministic alias was accepted")
				}
				for _, call := range f.calls[calls:] {
					for _, mutation := range []string{"container exec", "container create", "container start", "container stop", "container rm", "network create", "volume create"} {
						if strings.HasPrefix(call, mutation) {
							t.Fatal("rejected observation issued an effect", call)
						}
					}
				}
				if mode == "v2_alias" {
					healthy := domain.HostingHealthy{OperationID: op.ID, ContainerID: result.ContainerID, Snapshot: in.Plan.Snapshot, Release: in.Plan.Release, PlanHash: in.Plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *in.Plan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
					next := upgradeNext(t, in.Plan, healthy, in.Proof.Origin)
					f.plan = next.Plan
					maintenance := f.e.ApplyDatabaseNetworkUpgrade(context.Background(), "maintenance", next, f.req, f.evidence(), upgradeCredentials(t, next), f.config, f.before)
					if maintenance.Ready || !maintenance.Uncertain || f.creates != 2 || f.stops != 1 {
						t.Fatal("changed original v2 alias reached maintenance effects", maintenance)
					}
				}
			})
		}
	}
}

func TestDatabaseNetworkUpgradeProcessOutputBounds(t *testing.T) {
	if !databaseUpgradeTopMatches([]byte(" PID UID\n1001 999\n1002 999\n"), 1001) {
		t.Fatal("supported PostgreSQL process list rejected")
	}
	for _, raw := range []string{"", "PID UID\n", "USER PID\n999 1001", "PID UID\n+1001 999", "PID UID\n1001 999 extra", strings.Repeat("x", 8193)} {
		if databaseUpgradeTopMatches([]byte(raw), 1001) {
			t.Fatal("unsupported or unbounded process output accepted")
		}
	}
}

// The reviewed native Docker29 PG receipt has State=created, exact declared
// alias, nil DNSNames, PID=0 and no active network/endpoint/address before start.
func TestDatabaseNetworkUpgradeCreatedEndpointShape(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, credentials := upgradeFixture(t, engine)
			result := applyUpgrade(f, in, credentials, f.before)
			if !result.Ready {
				t.Fatal("shape fixture upgrade failed")
			}
			inspectCtx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			candidate, err := f.e.inspectDatabaseContainer(inspectCtx, result.ContainerID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			record := databaseNetworkRecord(in.Proof.Origin)
			inspection := &databaseUpgradeInspection{Previous: in.PreviousPlan, Origin: in.Proof.Origin}
			for _, mode := range []string{"created_nil", "created_empty", "created_exact", "exited_exact", "created_foreign_dns", "created_partial_dns", "created_wrong_alias", "created_pid", "created_network", "created_endpoint", "created_address", "exited_missing_dns", "exited_pid", "unknown_state", "running_missing_dns", "running_wrong_dns", "running_exact", "base_changed"} {
				t.Run(mode, func(t *testing.T) {
					var c databaseContainer
					if json.Unmarshal(raw, &c) != nil {
						t.Fatal("shape clone failed")
					}
					c.imageEnvironment = append([]string(nil), candidate.imageEnvironment...)
					n := c.NetworkSettings.Networks[in.Plan.Database.NetworkName]
					c.State.Running, c.State.Status, c.State.Pid = false, "created", 0
					n.NetworkID, n.EndpointID, n.IPAddress = "", "", ""
					n.DNSNames = nil
					want := false
					switch mode {
					case "created_nil":
						want = true
					case "created_empty":
						n.DNSNames = []string{}
						want = true
					case "created_exact":
						n.DNSNames = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName].DNSNames
						want = true
					case "exited_exact":
						c.State.Status = "exited"
						n.DNSNames = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName].DNSNames
						want = true
					case "created_foreign_dns":
						n.DNSNames = []string{"foreign"}
					case "created_partial_dns":
						n.DNSNames = []string{in.Plan.Database.Network.Alias}
					case "created_wrong_alias":
						n.Aliases = []string{"foreign"}
					case "created_pid":
						c.State.Pid = 123
					case "created_network":
						n.NetworkID = record.NetworkID
					case "created_endpoint":
						n.EndpointID = strings.Repeat("a", 64)
					case "created_address":
						n.IPAddress = "172.28.0.2"
					case "exited_missing_dns":
						c.State.Status = "exited"
					case "exited_pid":
						c.State.Status = "exited"
						c.State.Pid = 123
						n.DNSNames = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName].DNSNames
					case "unknown_state":
						c.State.Status = "dead"
					case "running_missing_dns":
						c.State = candidate.State
						n = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName]
						n.DNSNames = nil
					case "running_wrong_dns":
						c.State = candidate.State
						n = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName]
						n.DNSNames = []string{"foreign"}
					case "running_exact":
						c.State = candidate.State
						n = candidate.NetworkSettings.Networks[in.Plan.Database.NetworkName]
						want = true
					case "base_changed":
						c.Config.User = "root"
					}
					c.NetworkSettings.Networks[in.Plan.Database.NetworkName] = n
					if got := databaseContainerMatchesWithInspection(c, "database_upgrade", in.Plan, record, filepath.Join(f.config.secretRoot, databaseGeneration("database_upgrade", in.Plan)), inspection); got != want {
						t.Fatalf("endpoint shape %s accepted=%t want=%t", mode, got, want)
					}
				})
			}
		})
	}
}
