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
	"strconv"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func memberMaintenanceFixture(t *testing.T, engine string) (*applicationDatabaseFake, databaseMemberMaintenanceInput, *DatabaseCredentials) {
	t.Helper()
	f, _ := applicationDatabaseFixture(t, engine, true)
	previous := f.in.Proof.DatabasePlan
	s, db := previous.Snapshot, *previous.Database
	s.ResourceGeneration++
	db.Network = nil
	proof := domain.HostingDatabaseNetworkPlanInput{Origin: f.in.Proof.Origin, MembershipRevision: f.in.Proof.MembershipRevision, Members: f.in.Proof.Members}
	plan, err := domain.CompileHostingDatabasePlanV2(s, previous.Release, f.in.Proof.DatabaseHealthy, previous, *previous.Storage, db, proof)
	if err != nil {
		t.Fatal(err)
	}
	in := databaseMemberMaintenanceInput{Plan: plan, PreviousPlan: previous, Proof: proof}
	if err := in.validate(); err != nil {
		t.Fatal(err)
	}
	f.db.plan = plan
	upgradeEndpoint(f.db, plan.PreviousHealthy.ContainerID, []string{plan.Database.Network.Alias}, true)
	for _, member := range proof.Members {
		id := member.EndpointPlan.Target.ContainerID
		app := f.apps[id]
		key := strconv.Itoa(member.ApplicationHealthy.Snapshot.ContainerPort) + "/tcp"
		ports := app.HostConfig.PortBindings[key]
		ports[0].HostPort = strconv.Itoa(member.ApplicationHealthy.Snapshot.HostPort)
		f.apps[id] = app
	}
	f.db.e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		a := args[4:]
		if a[0] == "network" && a[1] == "inspect" && a[2] == "bridge" {
			return json.Marshal(map[string]any{"Id": f.bridgeID, "Name": "bridge", "Driver": "bridge", "Scope": "local"})
		}
		if a[0] == "container" && len(a) > 2 {
			if app, ok := f.apps[a[2]]; ok {
				if a[1] != "inspect" {
					t.Fatal("member maintenance attempted an application effect", a)
				}
				return json.Marshal(app)
			}
			if a[1] == "top" {
				return fmt.Appendf(nil, "PID UID\n%d 999\n", f.db.containers[a[2]]["State"].(map[string]any)["Pid"]), nil
			}
		}
		raw, err := f.db.command(ctx, name, args, dir, env)
		if err != nil || a[0] != "container" {
			return raw, err
		}
		switch a[1] {
		case "create":
			id := strings.TrimSpace(string(raw))
			upgradeEndpoint(f.db, id, []string{plan.Database.Network.Alias}, true)
			f.db.containers[id]["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[plan.Database.NetworkName].(map[string]any)["DNSNames"] = nil
		case "start":
			upgradeEndpoint(f.db, a[2], []string{plan.Database.Network.Alias}, true)
		case "stop":
			upgradeEndpoint(f.db, a[len(a)-1], nil, false)
		}
		return raw, nil
	})
	var values []DatabaseCredentialValue
	for i, ref := range plan.Database.Credentials {
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
	credentials, err := newDatabaseMemberMaintenanceCredentials(in, values, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentials.Close)
	return f, in, credentials
}

func applyMemberMaintenance(f *applicationDatabaseFake, in databaseMemberMaintenanceInput, credentials *DatabaseCredentials, before func(context.Context) error) domain.HostingResult {
	return f.db.e.ApplyDatabaseMemberMaintenance(context.Background(), "member_maintenance", in, f.db.req, f.db.evidence(), credentials, f.db.config, before)
}

func TestDatabaseMemberMaintenancePreservesSettledApplications(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, credentials := memberMaintenanceFixture(t, engine)
			apps := domain.HostingDocumentHash(f.apps)
			path := filepath.Join(f.db.config.stateRoot, in.Plan.Database.NetworkName+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			evidence := f.db.evidence()
			result := applyMemberMaintenance(f, in, credentials, f.db.before)
			if !result.Ready || result.Uncertain || f.db.creates != 2 || f.db.starts != 2 || f.db.stops != 1 || f.db.removes != 1 || f.db.networkCreates != 1 {
				t.Fatalf("member maintenance failed or violated single writer sequence: %+v counts %d/%d/%d/%d", result, f.db.creates, f.db.starts, f.db.stops, f.db.removes)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || !sameOwnedVolumeEvidence(evidence, f.db.evidence()) || apps != domain.HostingDocumentHash(f.apps) {
				t.Fatal("retained origin, volume or app endpoints changed", err)
			}
			calls := len(f.db.calls)
			op := domain.HostingOperation{ID: "member_maintenance", Plan: &in.Plan, Result: &result}
			observed, err := f.db.e.ObserveDatabaseMemberMaintenance(context.Background(), op, in, credentials, f.db.config)
			if err != nil || !observed.Ready || observed.ContainerID != result.ContainerID {
				t.Fatal("durable member maintenance observation", err)
			}
			for _, call := range f.db.calls[calls:] {
				for _, mutation := range []string{"container create", "container start", "container stop", "container rm", "container exec", "network create", "volume create"} {
					if strings.HasPrefix(call, mutation) {
						t.Fatal("observation issued effect", call)
					}
				}
			}
			replayed := applyMemberMaintenance(f, in, credentials, f.db.before)
			if !replayed.Ready || replayed.ContainerID != result.ContainerID || f.db.creates != 2 || f.db.starts != 2 || f.db.stops != 1 || f.db.removes != 1 {
				t.Fatal("exact retry duplicated a writer or changed app endpoints", replayed)
			}
			op.Result = nil
			if _, err := f.db.e.ObserveDatabaseMemberMaintenance(context.Background(), op, in, credentials, f.db.config); err == nil {
				t.Fatal("observation discovered a candidate without durable result")
			}
			if databaseNetworkUpgradeInput(in).validate() == nil {
				t.Fatal("empty adapter admitted members")
			}
			if _, err := NewDatabaseCredentials(in.Plan, credentials.values, time.Now().Add(time.Minute)); err == nil {
				t.Fatal("v1 credential constructor admitted member plan")
			}
		})
	}
}

func TestDatabaseMemberMaintenanceRejectsUntrustedState(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"unknown", "stopped", "foreign_image", "foreign_label", "endpoint", "bridge", "alias", "address", "public_port", "proof", "origin", "expired", "revoked", "no_authority", "create_permitted", "duplicate_writer"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, in, credentials := memberMaintenanceFixture(t, engine)
				id := in.Plan.Database.Network.Members[0].Endpoint.Target.ContainerID
				app := f.apps[id]
				before := f.db.before
				switch mode {
				case "unknown":
					f.addEndpoint(strings.Repeat("f", 64), strings.Repeat("e", 64))
				case "stopped":
					app.State.Running, app.State.Status = false, "exited"
				case "foreign_image":
					app.Image = "sha256:" + strings.Repeat("f", 64)
				case "foreign_label":
					app.Config.Labels[imageBindingLabel] = "foreign"
				case "endpoint", "alias", "address":
					n := app.NetworkSettings.Networks[in.Plan.Database.NetworkName]
					switch mode {
					case "endpoint":
						n.EndpointID = strings.Repeat("f", 64)
					case "alias":
						n.Aliases = []string{"forged"}
					case "address":
						n.IPAddress = "172.28.0.99"
					}
					app.NetworkSettings.Networks[in.Plan.Database.NetworkName] = n
				case "bridge":
					n := app.NetworkSettings.Networks["bridge"]
					n.NetworkID = strings.Repeat("f", 64)
					app.NetworkSettings.Networks["bridge"] = n
				case "public_port":
					for _, ports := range app.HostConfig.PortBindings {
						ports[0].HostIP = "0.0.0.0"
					}
				case "proof":
					in.Proof.Members = nil
				case "origin":
					in.Proof.Origin.Nonce = strings.Repeat("f", 32)
				case "expired":
					credentials.until = time.Now().Add(-time.Second)
				case "revoked":
					credentials.Close()
				case "no_authority":
					before = func(context.Context) error { return errors.New("revoked") }
				case "create_permitted":
					f.db.req.CreatePermitted = true
				case "duplicate_writer":
					f.db.containers[strings.Repeat("f", 64)] = f.db.containers[in.Plan.PreviousHealthy.ContainerID]
				}
				f.apps[id] = app
				result := applyMemberMaintenance(f, in, credentials, before)
				if result.Ready || !result.Uncertain || f.db.creates != 1 || f.db.starts != 1 || f.db.stops != 0 || f.db.removes != 0 {
					t.Fatal("untrusted prerequisite caused a database effect", result)
				}
			})
		}
	}
}

func TestDatabaseMemberMaintenanceRechecksMembersAfterAuthority(t *testing.T) {
	f, in, credentials := memberMaintenanceFixture(t, "postgresql")
	id := in.Plan.Database.Network.Members[0].Endpoint.Target.ContainerID
	before := func(ctx context.Context) error {
		app := f.apps[id]
		bridge := app.NetworkSettings.Networks["bridge"]
		bridge.EndpointID = strings.Repeat("f", 64)
		app.NetworkSettings.Networks["bridge"] = bridge
		f.apps[id] = app
		return f.db.before(ctx)
	}
	result := applyMemberMaintenance(f, in, credentials, before)
	if result.Ready || !result.Uncertain || f.db.stops != 0 || f.db.creates != 1 {
		t.Fatal("member endpoint replacement crossed authority callback", result)
	}
}

func TestDatabaseMemberMaintenanceRetryAfterLostCreate(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, credentials := memberMaintenanceFixture(t, engine)
			apps := domain.HostingDocumentHash(f.apps)
			command := f.db.e.command
			lost := false
			f.db.e.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				raw, err := command.RunEnvironment(ctx, name, args, dir, env)
				if args[4] == "container" && args[5] == "create" && err == nil && !lost {
					lost = true
					return nil, errors.New("lost response")
				}
				return raw, err
			})
			first := applyMemberMaintenance(f, in, credentials, f.db.before)
			if first.Ready || !first.Uncertain || f.db.creates != 2 || f.db.starts != 1 || f.db.stops != 1 || f.db.removes != 0 || apps != domain.HostingDocumentHash(f.apps) {
				t.Fatal("lost create did not retain exact uncertain state", first)
			}
			second := applyMemberMaintenance(f, in, credentials, f.db.before)
			if !second.Ready || f.db.creates != 2 || f.db.starts != 2 || f.db.stops != 1 || f.db.removes != 1 || apps != domain.HostingDocumentHash(f.apps) {
				t.Fatal("authorized retry failed to reuse exact candidate", second)
			}
		})
	}
}

func TestDatabaseMemberMaintenanceRechecksReadinessAndRevocation(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"readiness_change", "observation_change", "revoked_after_stop"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, in, credentials := memberMaintenanceFixture(t, engine)
				id := in.Plan.Database.Network.Members[0].Endpoint.Target.ContainerID
				probe := f.db.config.readiness
				observing := false
				if mode != "revoked_after_stop" {
					f.db.config.readiness = func(ctx context.Context, address string, p domain.HostingApplyPlan, values []DatabaseCredentialValue, readOnly, initialize bool, before func(context.Context) error) error {
						if err := probe(ctx, address, p, values, readOnly, initialize, before); err != nil {
							return err
						}
						if mode == "readiness_change" || observing {
							app := f.apps[id]
							bridge := app.NetworkSettings.Networks["bridge"]
							bridge.EndpointID = strings.Repeat("f", 64)
							app.NetworkSettings.Networks["bridge"] = bridge
							f.apps[id] = app
						}
						return nil
					}
				}
				revoked := false
				before := func(ctx context.Context) error {
					if revoked {
						return errors.New("revoked")
					}
					return f.db.before(ctx)
				}
				if mode == "revoked_after_stop" {
					f.db.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
						if args[0] == "container" && args[1] == "stop" {
							revoked = true
						}
						return nil, nil, false
					}
				}
				result := applyMemberMaintenance(f, in, credentials, before)
				if mode == "observation_change" {
					if !result.Ready {
						t.Fatal("setup apply", result)
					}
					observing = true
					op := domain.HostingOperation{ID: "member_maintenance", Plan: &in.Plan, Result: &result}
					if _, err := f.db.e.ObserveDatabaseMemberMaintenance(context.Background(), op, in, credentials, f.db.config); err == nil {
						t.Fatal("observation accepted a member replaced during its probe")
					}
					return
				}
				if result.Ready || !result.Uncertain || f.db.stops != 1 || f.db.removes != 0 {
					t.Fatal("readiness or authority change escaped hold", result)
				}
				if mode == "revoked_after_stop" {
					if f.db.creates != 1 || f.db.starts != 1 {
						t.Fatal("effect after revocation")
					}
					f.db.hook = nil
					revoked = false
					result = applyMemberMaintenance(f, in, credentials, before)
					if result.Ready || !result.Uncertain || f.db.stops != 1 || f.db.creates != 1 || f.db.starts != 1 {
						t.Fatal("unresolved create intent was bypassed by a retry", result)
					}
				}
			})
		}
	}
}

func TestDatabaseMemberMaintenanceTwoAuthenticatedMembers(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, oldCredentials := memberMaintenanceFixture(t, engine)
			proof := in.Proof.Members[0]
			proof.ApplicationHealthy.ContainerID = strings.Repeat("b", 64)
			proof.ApplicationHealthy.OperationID = "second_app"
			proof.EndpointPlan.OperationID = "second_attach"
			proof.EndpointPlan.MembershipRevision = 2
			proof.EndpointPlan.Target.ContainerID = proof.ApplicationHealthy.ContainerID
			proof.EndpointPlan.Target.ContainerOperationID = proof.ApplicationHealthy.OperationID
			proof.EndpointPlan.Hash = proof.EndpointPlan.DocumentHash()
			proof.Receipt.ReceiptID = "second_attached"
			proof.Receipt.OperationID = proof.EndpointPlan.OperationID
			proof.Receipt.PlanHash = proof.EndpointPlan.Hash
			proof.Receipt.MembershipRevision = 2
			observation := *proof.Receipt.Observation
			observation.Endpoint.Target = proof.EndpointPlan.Target
			observation.Endpoint.EndpointID = strings.Repeat("c", 64)
			proof.Receipt.Observation = &observation
			proof.Receipt.Hash = proof.Receipt.DocumentHash()
			in.Proof.Members = append(in.Proof.Members, proof)
			in.Proof.MembershipRevision = 2
			db := *in.Plan.Database
			db.Network = nil
			p, err := domain.CompileHostingDatabasePlanV2(in.Plan.Snapshot, in.Plan.Release, *in.Plan.PreviousHealthy, in.PreviousPlan, *in.Plan.Storage, db, in.Proof)
			if err != nil {
				t.Fatal("two-member proof", err)
			}
			in.Plan, f.db.plan = p, p
			app := f.app(proof.ApplicationHealthy.ContainerID, proof.ApplicationHealthy.OperationID, proof.ApplicationHealthy.PlanHash, "second", true)
			app.Name = "/second_app"
			app.NetworkSettings.Networks[db.NetworkName] = applicationDatabaseEndpoint{NetworkID: in.Proof.Origin.NetworkID, EndpointID: observation.Endpoint.EndpointID, IPAddress: "172.28.0.5", DNSNames: []string{"second_app", app.ID[:12]}}
			for _, ports := range app.HostConfig.PortBindings {
				ports[0].HostPort = strconv.Itoa(proof.ApplicationHealthy.Snapshot.HostPort)
			}
			f.apps[app.ID] = app
			f.db.network["Containers"].(map[string]any)[app.ID] = map[string]string{"EndpointID": observation.Endpoint.EndpointID, "IPv4Address": "172.28.0.5/16"}
			credentials, err := newDatabaseMemberMaintenanceCredentials(in, oldCredentials.values, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			defer credentials.Close()
			apps := domain.HostingDocumentHash(f.apps)
			result := applyMemberMaintenance(f, in, credentials, f.db.before)
			if !result.Ready || apps != domain.HostingDocumentHash(f.apps) || len(f.db.network["Containers"].(map[string]any)) != 3 {
				t.Fatal("two exact members did not survive replacement", result)
			}
		})
	}
}

func TestDatabaseMemberMaintenanceRequiresV2PredecessorAndCompleteProofs(t *testing.T) {
	f, in, credentials := memberMaintenanceFixture(t, "postgresql")
	for _, mode := range []string{"v1_predecessor", "receipt", "retirement"} {
		t.Run(mode, func(t *testing.T) {
			bad := in
			bad.Proof.Members = append([]domain.HostingDatabaseNetworkMemberProof(nil), in.Proof.Members...)
			switch mode {
			case "v1_predecessor":
				bad.PreviousPlan = in.Proof.Members[0].DatabasePlan
			case "receipt":
				bad.Proof.Members[0].Receipt.Hash = "sha256:" + strings.Repeat("f", 64)
			case "retirement":
				bad.Proof.Retired = []domain.HostingDatabaseNetworkRetirementProof{{}}
			}
			if bad.validate() == nil {
				t.Fatal("incomplete transition accepted")
			}
			if _, err := newDatabaseMemberMaintenanceCredentials(bad, credentials.values, time.Now().Add(time.Minute)); err == nil {
				t.Fatal("credential derivation preceded input validation")
			}
			result := applyMemberMaintenance(f, bad, credentials, f.db.before)
			if result.Ready || !result.Uncertain || f.db.stops != 0 || f.db.creates != 1 {
				t.Fatal("invalid proof caused effect", result)
			}
		})
	}
}
