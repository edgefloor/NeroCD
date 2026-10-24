//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

func applicationDatabaseConfigure(t *testing.T, f *applicationDatabaseFake, c applicationDatabaseCandidate) applicationDatabaseConfiguration {
	t.Helper()
	configured, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), f.before)
	if err != nil {
		t.Fatal(err)
	}
	return configured
}

func TestApplicationDatabaseInspectionRejectsSubstitution(t *testing.T) {
	for _, mode := range []string{"candidate_nonce", "candidate_id", "image", "binding", "default_network", "extra_network", "privileged", "host_network", "port", "unknown_app", "extra_db", "db_stopped", "network_labels", "origin", "daemon"} {
		t.Run(mode, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, "redis", false)
			c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
			if err != nil {
				t.Fatal(err)
			}
			actual := f.apps[c.ContainerID]
			switch mode {
			case "candidate_nonce":
				c.CreateNonce = strings.Repeat("0", 32)
			case "candidate_id":
				c.ContainerID = strings.Repeat("0", 64)
			case "image":
				actual.Image = "sha256:" + strings.Repeat("0", 64)
			case "binding":
				actual.Config.Labels[imageBindingLabel] = "foreign"
			case "default_network":
				f.bridgeID = strings.Repeat("0", 64)
			case "extra_network":
				actual.NetworkSettings.Networks["other"] = applicationDatabaseEndpoint{NetworkID: strings.Repeat("0", 64)}
			case "privileged":
				actual.HostConfig.Privileged = true
			case "host_network":
				actual.HostConfig.NetworkMode = "host"
			case "port":
				actual.HostConfig.PortBindings["foreign/tcp"] = nil
			case "unknown_app", "extra_db":
				f.addEndpoint(strings.Repeat("0", 64), strings.Repeat("1", 64))
			case "db_stopped":
				f.db.containers[f.in.Proof.DatabaseHealthy.ContainerID]["State"].(map[string]any)["Running"] = false
			case "network_labels":
				f.db.network["Labels"].(map[string]string)["foreign"] = "value"
			case "origin":
				f.in.Proof.Origin.Nonce = strings.Repeat("0", 32)
			case "daemon":
				f.db.hook = func(_ context.Context, args []string) ([]byte, error, bool) {
					if args[0] == "info" {
						return []byte(`"foreign_daemon"`), nil, true
					}
					return nil, nil, false
				}
			}
			f.apps[actual.ID] = actual
			if f.e.InspectCandidate(context.Background(), f.in, c) == nil {
				t.Fatal("substituted inspection accepted")
			}
			if _, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), f.before); err == nil || len(f.effects) != 1 {
				t.Fatal("effect escaped failed identity check")
			}
		})
	}
}

func TestApplicationDatabasePreflightAndStageAuthority(t *testing.T) {
	for _, mode := range []string{"invalid_proof", "unknown_endpoint", "nil_authority", "denied_attach", "cancel_attach", "denied_start", "readiness_failure"} {
		t.Run(mode, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, "postgresql", false)
			before := f.before
			if mode == "invalid_proof" {
				f.in.Proof.Binding.Hash = "sha256:" + strings.Repeat("0", 64)
			}
			if mode == "unknown_endpoint" {
				f.addEndpoint(strings.Repeat("0", 64), strings.Repeat("1", 64))
			}
			if mode == "nil_authority" {
				before = nil
			}
			c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), before)
			if mode == "invalid_proof" || mode == "unknown_endpoint" || mode == "nil_authority" {
				if err == nil || len(f.effects) != 0 {
					t.Fatal("invalid preflight created")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			deny := func(context.Context) error { return errors.New("private-canary-value") }
			if mode == "denied_attach" || mode == "cancel_attach" {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				callback := deny
				if mode == "cancel_attach" {
					callback = func(context.Context) error { cancel(); return nil }
				}
				_, err = f.e.ConfigureStoppedNetwork(ctx, f.in, c, applicationDatabaseAttachPlan(f, c), callback)
				if err == nil || len(f.effects) != 1 || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("attachment escaped authority", err)
				}
				return
			}
			configured := applicationDatabaseConfigure(t, f, c)
			if mode == "denied_start" {
				if _, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, deny); err == nil || len(f.effects) != 2 {
					t.Fatal("start escaped stage authority")
				}
				return
			}
			f.ready = false
			observed, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, f.before)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.e.VerifyReady(context.Background(), f.in, c, observed); err == nil {
				t.Fatal("readiness failure invented health")
			}
			if strings.Join(f.effects, ",") != "create,connect,start" {
				t.Fatal("readiness caused terminal cleanup/retry")
			}
		})
	}
}

func applicationDatabaseRetirePlan(f *applicationDatabaseFake) domain.HostingDatabaseBindingEndpointPlan {
	m := f.in.Plan.ApplicationDatabase.PreviousMembers[0]
	p := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: f.in.Proof.OperationID, BindingHash: f.in.Proof.Binding.Hash, MembershipRevision: f.in.Proof.MembershipRevision + 2, Action: "retire", Target: m.Endpoint.Target, ExpectedEndpointID: m.Endpoint.EndpointID}
	p.Hash = p.DocumentHash()
	return p
}

func TestApplicationDatabaseRetirementEvidenceAndAuthority(t *testing.T) {
	for _, mode := range []string{"success", "remove_failure", "missing_candidate", "wrong_candidate", "not_ready", "denied_stop", "denied_disconnect", "denied_remove", "db_target", "candidate_target"} {
		t.Run(mode, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, "postgresql", true)
			c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
			if err != nil {
				t.Fatal(err)
			}
			configured := applicationDatabaseConfigure(t, f, c)
			observed, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, f.before)
			if err != nil {
				t.Fatal(err)
			}
			p := applicationDatabaseRetirePlan(f)
			callback := f.before
			switch mode {
			case "remove_failure":
				f.removeErr = errors.New("private-canary-value")
			case "missing_candidate":
				c = applicationDatabaseCandidate{}
			case "wrong_candidate":
				c.PlanHash = "sha256:" + strings.Repeat("0", 64)
			case "not_ready":
				f.ready = false
			case "db_target":
				p.Target.ContainerID = f.in.Proof.DatabaseHealthy.ContainerID
				p.Hash = p.DocumentHash()
			case "candidate_target":
				p.Target.ContainerID = c.ContainerID
				p.Target.ContainerOperationID = c.OperationID
				p.Hash = p.DocumentHash()
			}
			if strings.HasPrefix(mode, "denied_") {
				calls := 0
				limit := map[string]int{"denied_stop": 1, "denied_disconnect": 2, "denied_remove": 3}[mode]
				callback = func(ctx context.Context) error {
					calls++
					if calls == limit {
						return errors.New("private-canary-value")
					}
					return f.before(ctx)
				}
			}
			result, err := f.e.Retire(context.Background(), f.in, c, observed, p, callback)
			if mode == "success" {
				if err != nil || result.Endpoint.Present || result.Endpoint.Endpoint.EndpointID != p.ExpectedEndpointID || !result.ContainerRemoved {
					t.Fatal("retirement incomplete", err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("unsafe retirement error", err)
				}
				if result.ContainerRemoved {
					t.Fatal("removal falsely reported")
				}
				if mode == "remove_failure" || mode == "denied_remove" {
					if result.Endpoint.Present || result.Endpoint.Endpoint.EndpointID != p.ExpectedEndpointID {
						t.Fatal("lost detached partial evidence")
					}
				}
				if mode == "missing_candidate" || mode == "wrong_candidate" || mode == "not_ready" || mode == "db_target" || mode == "candidate_target" {
					if len(f.effects) != 3 {
						t.Fatal("old effect without exact ready candidate")
					}
				}
			}
			actual := f.apps[strings.Repeat("5", 64)]
			if !actual.State.Running || len(actual.NetworkSettings.Networks) != 2 {
				t.Fatal("retirement changed current candidate")
			}
		})
	}
}

func TestApplicationDatabaseConfigurationAndObservedEvidenceAreDistinct(t *testing.T) {
	for _, mode := range []string{"premature_endpoint", "custom_alias", "custom_dns", "custom_ipam", "changed_configuration", "lost_start", "changed_observation"} {
		t.Run(mode, func(t *testing.T) {
			f, env := applicationDatabaseFixture(t, "redis", false)
			c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
			if err != nil {
				t.Fatal(err)
			}
			configured := applicationDatabaseConfigure(t, f, c)
			actual := f.apps[c.ContainerID]
			n := actual.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName]
			switch mode {
			case "premature_endpoint":
				n.NetworkID, n.EndpointID = f.in.Proof.Origin.NetworkID, strings.Repeat("4", 64)
			case "custom_alias":
				n.Aliases = []string{"foreign"}
			case "custom_dns":
				n.DNSNames = append(n.DNSNames, "foreign")
			case "custom_ipam":
				n.DriverOpts = map[string]string{"foreign": "option"}
			case "changed_configuration":
				configured.PrivateNetworkID = strings.Repeat("0", 64)
				configured.Hash = ""
				configured.Hash = domain.HostingDocumentHash(configured)
			case "lost_start":
				f.startErr = errors.New("private-canary-value")
			}
			actual.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName] = n
			f.apps[c.ContainerID] = actual
			observed, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, f.before)
			if mode == "changed_observation" {
				if err != nil {
					t.Fatal(err)
				}
				observed.Endpoint.EndpointID = strings.Repeat("0", 64)
				if f.e.VerifyReady(context.Background(), f.in, c, observed) == nil {
					t.Fatal("rewritten observed endpoint accepted")
				}
				if len(f.effects) != 3 {
					t.Fatal("observation caused effects")
				}
				return
			}
			if err == nil || observed.Present || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("invalid evidence invented endpoint", err)
			}
			if mode == "lost_start" {
				if !f.apps[c.ContainerID].State.Running || len(f.effects) != 3 {
					t.Fatal("uncertain start was misclassified")
				}
				if _, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, f.before); err == nil || len(f.effects) != 3 {
					t.Fatal("uncertain start blindly repeated")
				}
			} else if len(f.effects) != 2 {
				t.Fatal("invalid stopped configuration started")
			}
		})
	}
}

func TestApplicationDatabaseEndpointAuthorityAndRevision(t *testing.T) {
	f, env := applicationDatabaseFixture(t, "postgresql", true)
	c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"operation", "revision"} {
		p := applicationDatabaseAttachPlan(f, c)
		if mode == "operation" {
			p.OperationID = "foreign"
		} else {
			p.MembershipRevision++
		}
		p.Hash = p.DocumentHash()
		if _, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, p, f.before); err == nil || len(f.effects) != 1 {
			t.Fatal("configuration accepted foreign operation/revision")
		}
	}
	configured := applicationDatabaseConfigure(t, f, c)
	observed, err := f.e.StartAndObserve(context.Background(), f.in, c, applicationDatabaseAttachPlan(f, c), configured, f.before)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"operation", "revision"} {
		p := applicationDatabaseRetirePlan(f)
		if mode == "operation" {
			p.OperationID = "foreign"
		} else {
			p.MembershipRevision--
		}
		p.Hash = p.DocumentHash()
		if _, err := f.e.Retire(context.Background(), f.in, c, observed, p, f.before); err == nil || len(f.effects) != 3 {
			t.Fatal("retirement accepted foreign operation/revision")
		}
	}
}

func TestApplicationDatabaseRejectsRetirementRevisionOverflowBeforeCreate(t *testing.T) {
	f, environment := applicationDatabaseFixture(t, "redis", true)
	f.in.Proof.MembershipRevision = math.MaxInt64 - 1
	original := f.in.Proof.BindingDatabasePlan
	current, err := domain.CompileHostingDatabasePlanV2(original.Snapshot, original.Release, *f.in.Proof.DatabasePlan.PreviousHealthy, original, *original.Storage, *original.Database, domain.HostingDatabaseNetworkPlanInput{Origin: f.in.Proof.Origin, MembershipRevision: f.in.Proof.MembershipRevision, Members: f.in.Proof.Members})
	if err != nil {
		t.Fatal("valid near-limit database proof rejected", err)
	}
	f.in.Proof.DatabasePlan = current
	f.in.Proof.DatabaseHealthy.PlanHash = current.Hash
	p, err := domain.CompileHostingApplicationDatabasePlan(f.in.Plan.Snapshot, f.in.Plan.Release, nil, nil, f.in.Proof)
	if err != nil {
		t.Fatal("valid near-limit proof fixture rejected", err)
	}
	f.in.Plan = p
	metadata, err := environment.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	metadata.PlanHash = p.Hash
	updated, err := hostingruntimeprotocol.NewEnvironment(metadata, []hostingruntimeprotocol.Entry{{Name: "PASSWORD", Value: []byte("private-canary-value")}})
	if err != nil {
		t.Fatal(err)
	}
	defer updated.Close()
	if _, err := f.e.CreateStopped(context.Background(), f.in, updated, time.Now().Add(time.Minute), f.before); err == nil || len(f.effects) != 0 {
		t.Fatal("created replacement whose retirement revision overflows")
	}
}

func TestApplicationDatabaseRequiresObservedDatabaseAliasBeforeEffects(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"missing", "wrong", "extra", "dns_missing"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f, environment := applicationDatabaseFixture(t, engine, false)
				db := f.db.containers[f.in.Proof.DatabaseHealthy.ContainerID]
				endpoint := db["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.in.Proof.Binding.NetworkName].(map[string]any)
				switch mode {
				case "missing":
					endpoint["Aliases"] = nil
				case "wrong":
					endpoint["Aliases"] = []string{"foreign"}
				case "extra":
					endpoint["Aliases"] = []string{f.in.Proof.Binding.Alias, "foreign"}
				case "dns_missing":
					endpoint["DNSNames"] = []string{strings.TrimPrefix(db["Name"].(string), "/"), f.in.Proof.DatabaseHealthy.ContainerID[:12]}
				}
				if _, err := f.e.CreateStopped(context.Background(), f.in, environment, time.Now().Add(time.Minute), f.before); err == nil || len(f.effects) != 0 {
					t.Fatal("unresolvable or substituted database alias admitted an app effect")
				}
			})
		}
	}
}

func TestApplicationDatabaseAliasLossPreventsConfigurationAndStart(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, stage := range []string{"configure", "start"} {
			t.Run(engine+"/"+stage, func(t *testing.T) {
				f, environment := applicationDatabaseFixture(t, engine, false)
				candidate, err := f.e.CreateStopped(context.Background(), f.in, environment, time.Now().Add(time.Minute), f.before)
				if err != nil {
					t.Fatal(err)
				}
				var configured applicationDatabaseConfiguration
				if stage == "start" {
					configured = applicationDatabaseConfigure(t, f, candidate)
				}
				db := f.db.containers[f.in.Proof.DatabaseHealthy.ContainerID]
				db["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.in.Proof.Binding.NetworkName].(map[string]any)["Aliases"] = nil
				before := len(f.effects)
				if stage == "configure" {
					_, err = f.e.ConfigureStoppedNetwork(context.Background(), f.in, candidate, applicationDatabaseAttachPlan(f, candidate), f.before)
				} else {
					_, err = f.e.StartAndObserve(context.Background(), f.in, candidate, applicationDatabaseAttachPlan(f, candidate), configured, f.before)
				}
				if err == nil || len(f.effects) != before || f.apps[candidate.ContainerID].State.Running {
					t.Fatal("database alias loss escaped the next effect boundary")
				}
			})
		}
	}
}
