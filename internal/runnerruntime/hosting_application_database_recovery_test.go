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

// The existing engine fake models exact Docker state. This wrapper adds the
// successful empty ID inventory that recovery requires, without inferring
// absence from an inspect failure.
func recoveryFixture(t *testing.T, engine string, previous bool) (*applicationDatabaseFake, applicationDatabaseCandidate, applicationDatabaseRecoveryEvidence) {
	t.Helper()
	f, env := applicationDatabaseFixture(t, engine, previous)
	original := f.command
	f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
		a := args[4:]
		if len(a) > 2 && a[0] == "container" && a[1] == "ls" && strings.HasPrefix(a[len(a)-1], "id=") {
			id := strings.TrimPrefix(a[len(a)-1], "id=")
			if _, ok := f.apps[id]; ok {
				return []byte(id), nil
			}
			return nil, nil
		}
		return original(ctx, name, args, dir, env)
	})
	c, err := f.e.CreateStopped(context.Background(), f.in, env, time.Now().Add(time.Minute), f.before)
	if err != nil {
		t.Fatal("create fixture", err)
	}
	return f, c, applicationDatabaseRecoveryEvidence{AttachPlan: applicationDatabaseAttachPlan(f, c)}
}

func recoveryConfigure(t *testing.T, f *applicationDatabaseFake, c applicationDatabaseCandidate, p *applicationDatabaseRecoveryEvidence, start bool) {
	t.Helper()
	cfg, err := f.e.ConfigureStoppedNetwork(context.Background(), f.in, c, p.AttachPlan, f.before)
	if err != nil {
		t.Fatal(err)
	}
	p.Configuration = &cfg
	if start {
		observation, err := f.e.StartAndObserve(context.Background(), f.in, c, p.AttachPlan, cfg, f.before)
		if err != nil {
			t.Fatal(err)
		}
		p.Started = &observation
	}
}

func TestApplicationDatabaseRecoveryPhasesAndCleanup(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, phase := range []applicationDatabaseRecoveryPhase{applicationDatabaseRecoveryCreated, applicationDatabaseRecoveryConfigured, applicationDatabaseRecoveryStopped, applicationDatabaseRecoveryStarted, applicationDatabaseRecoveryReady} {
			t.Run(engine+"/"+string(phase), func(t *testing.T) {
				f, c, p := recoveryFixture(t, engine, true)
				if phase != applicationDatabaseRecoveryCreated {
					recoveryConfigure(t, f, c, &p, phase != applicationDatabaseRecoveryConfigured)
				}
				if phase == applicationDatabaseRecoveryStopped {
					stopContext, stop := context.WithTimeout(context.Background(), time.Second)
					defer stop()
					if f.e.effect(stopContext, f.in, f.before, "container", "stop", "--time", "10", c.ContainerID) != nil {
						t.Fatal("stop fixture")
					}
				}
				if phase == applicationDatabaseRecoveryStarted {
					f.ready = false
				}
				before := len(f.effects)
				got, err := f.e.ObserveCandidate(context.Background(), f.in, c, p)
				if err != nil || got.Phase != phase || got.Candidate != c || len(f.effects) != before {
					t.Fatal("phase observation", got.Phase, err)
				}
				previous := f.in.Plan.ApplicationDatabase.PreviousMembers[0].Endpoint.Target.ContainerID
				previousBytes, _ := json.Marshal(f.apps[previous])
				databaseBytes, _ := json.Marshal(f.db.containers)
				f.ready = true
				got, err = f.e.RemoveCandidate(context.Background(), f.in, c, p, f.before)
				if err != nil || got.Phase != applicationDatabaseRecoveryAbsent {
					t.Fatal("cleanup", got.Phase, err)
				}
				actualPrevious, _ := json.Marshal(f.apps[previous])
				actualDatabase, _ := json.Marshal(f.db.containers)
				if string(previousBytes) != string(actualPrevious) || string(databaseBytes) != string(actualDatabase) {
					t.Fatal("cleanup changed previous application or database")
				}
				count := len(f.effects)
				got, err = f.e.RemoveCandidate(context.Background(), f.in, c, p, func(context.Context) error { t.Fatal("absence replay requested mutation authority"); return nil })
				if err != nil || got.Phase != applicationDatabaseRecoveryAbsent || len(f.effects) != count {
					t.Fatal("absence replay mutated", err)
				}
			})
		}
	}
}

func TestApplicationDatabaseRecoveryRejectsForeignEvidence(t *testing.T) {
	for _, mutation := range []string{"unknown_id", "missing_id", "nonce", "daemon", "image", "bridge", "attach_network", "configuration", "started", "foreign_network_user", "stopped_foreign_network_user", "actual_nonce", "actual_image", "actual_bridge", "actual_private_network", "previous_member", "previous_healthy", "database"} {
		t.Run(mutation, func(t *testing.T) {
			f, c, p := recoveryFixture(t, "postgresql", true)
			recoveryConfigure(t, f, c, &p, true)
			switch mutation {
			case "unknown_id":
				c.ContainerID = strings.Repeat("a", 64)
			case "missing_id":
				c.ContainerID = ""
			case "nonce":
				c.CreateNonce = strings.Repeat("b", 32)
			case "daemon":
				c.DaemonID = "foreign"
			case "image":
				c.ImageID = "sha256:" + strings.Repeat("d", 64)
			case "bridge":
				c.DefaultNetworkID = strings.Repeat("b", 64)
			case "attach_network":
				p.AttachPlan.Target.NetworkID = strings.Repeat("b", 64)
				p.AttachPlan.Hash = p.AttachPlan.DocumentHash()
			case "configuration":
				p.Configuration.PrivateNetworkID = strings.Repeat("b", 64)
			case "started":
				p.Started.Endpoint.EndpointID = strings.Repeat("b", 64)
			case "foreign_network_user", "stopped_foreign_network_user":
				f.addEndpoint(strings.Repeat("b", 64), strings.Repeat("c", 64))
				if mutation == "stopped_foreign_network_user" {
					foreign := f.app(strings.Repeat("b", 64), "foreign", "foreign", "foreign", false)
					foreign.State.Status = "exited"
					f.apps[foreign.ID] = foreign
				}
			case "actual_nonce":
				actual := f.apps[c.ContainerID]
				actual.Config.Labels[applicationDatabaseNonceLabel] = "private-canary-value"
				f.apps[c.ContainerID] = actual
			case "actual_image":
				actual := f.apps[c.ContainerID]
				actual.Image = "sha256:" + strings.Repeat("d", 64)
				f.apps[c.ContainerID] = actual
			case "actual_bridge", "actual_private_network":
				actual := f.apps[c.ContainerID]
				name := "bridge"
				if mutation == "actual_private_network" {
					name = f.in.Proof.Binding.NetworkName
				}
				n := actual.NetworkSettings.Networks[name]
				n.NetworkID = strings.Repeat("b", 64)
				actual.NetworkSettings.Networks[name] = n
				f.apps[c.ContainerID] = actual
			case "previous_member", "previous_healthy":
				c.ContainerID = f.in.Plan.ApplicationDatabase.PreviousMembers[0].Endpoint.Target.ContainerID
				if mutation == "previous_healthy" {
					f.in.Plan.PreviousHealthy = &domain.HostingHealthy{ContainerID: c.ContainerID}
				}
			case "database":
				c.ContainerID = f.in.Proof.DatabaseHealthy.ContainerID
			}
			before := len(f.effects)
			got, err := f.e.ObserveCandidate(context.Background(), f.in, c, p)
			if err == nil || got != (applicationDatabaseRecoveryObservation{}) || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe observation", err)
			}
			if _, err := f.e.RemoveCandidate(context.Background(), f.in, c, p, f.before); err == nil || len(f.effects) != before {
				t.Fatal("foreign cleanup mutated")
			}
		})
	}
}

func TestApplicationDatabaseRecoveryFreshAuthorityAndCancellation(t *testing.T) {
	for _, mode := range []string{"nil", "stale", "canceled", "callback_cancels", "settled", "revoked_after_stop"} {
		t.Run(mode, func(t *testing.T) {
			f, c, p := recoveryFixture(t, "redis", false)
			recoveryConfigure(t, f, c, &p, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			before := func(ctx context.Context) error {
				calls++
				if mode == "stale" || mode == "settled" || mode == "revoked_after_stop" && calls > 1 {
					return errors.New("private-canary-value")
				}
				if mode == "callback_cancels" {
					cancel()
					return nil
				}
				return f.before(ctx)
			}
			if mode == "nil" {
				before = nil
			}
			if mode == "canceled" {
				cancel()
			}
			count := len(f.effects)
			got, err := f.e.RemoveCandidate(ctx, f.in, c, p, before)
			expected := count
			if mode == "revoked_after_stop" {
				expected++
			}
			if err == nil || got != (applicationDatabaseRecoveryObservation{}) || len(f.effects) != expected || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("authority/cancellation bypass", err)
			}
			if _, ok := f.apps[c.ContainerID]; !ok {
				t.Fatal("removed without current authority")
			}
		})
	}
}

func TestApplicationDatabaseRecoveryLostEffectACK(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, effect := range []string{"stop", "rm"} {
			t.Run(engine+"/"+effect, func(t *testing.T) {
				f, c, p := recoveryFixture(t, engine, false)
				recoveryConfigure(t, f, c, &p, true)
				original := f.e.image.command
				lost := false
				f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
					raw, err := original.RunEnvironment(ctx, name, args, dir, env)
					a := args[4:]
					if len(a) > 2 && a[0] == "container" && a[1] == effect && !lost {
						lost = true
						return nil, errors.New("private-canary-value")
					}
					return raw, err
				})
				if _, err := f.e.RemoveCandidate(context.Background(), f.in, c, p, f.before); err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("lost effect ACK must remain uncertain", err)
				}
				before := strings.Join(f.effects, ",")
				got, err := f.e.RemoveCandidate(context.Background(), f.in, c, p, f.before)
				expected := before
				if effect == "stop" {
					expected += ",rm"
				}
				if err != nil || got.Phase != applicationDatabaseRecoveryAbsent || strings.Join(f.effects, ",") != expected {
					t.Fatal("effect replay repeated mutation", err)
				}
			})
		}
	}
}

func TestApplicationDatabaseRecoveryAbsenceRequiresSuccessfulInventories(t *testing.T) {
	for _, mode := range []string{"id_error", "name_error", "name_occupied", "unexpected_id", "inspect_error", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			f, c, p := recoveryFixture(t, "redis", false)
			delete(f.apps, c.ContainerID)
			original := f.e.image.command
			f.e.image.command = imageCommandFunc(func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				a := args[4:]
				if a[0] == "container" && a[1] == "ls" {
					id := strings.HasPrefix(a[len(a)-1], "id=")
					if mode == "id_error" && id || mode == "name_error" && !id {
						return nil, errors.New("private-canary-value")
					}
					if mode == "name_occupied" && !id || mode == "unexpected_id" && id {
						return []byte(strings.Repeat("b", 64)), nil
					}
					if mode == "inspect_error" && id {
						return []byte(c.ContainerID), nil
					}
					if mode == "oversized" {
						return []byte(strings.Repeat("x", (16<<10)+1)), nil
					}
				}
				return original.RunEnvironment(ctx, name, args, dir, env)
			})
			count := len(f.effects)
			got, err := f.e.ObserveCandidate(context.Background(), f.in, c, p)
			if err == nil || got != (applicationDatabaseRecoveryObservation{}) || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("invented absence", err)
			}
			if _, err := f.e.RemoveCandidate(context.Background(), f.in, c, p, f.before); err == nil || len(f.effects) != count {
				t.Fatal("effect despite uncertain inventory")
			}
		})
	}
}
