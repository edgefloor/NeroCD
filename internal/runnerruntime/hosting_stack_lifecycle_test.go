package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func stackStopTestPlan(t *testing.T) domain.HostingStackStopPlan {
	t.Helper()
	plan := stackTestPlan(t)
	state := domain.HostingStackState{ObservationComplete: true, PublicComponent: plan.Stack.Definition.PublicComponent, NetworkID: strings.Repeat("a", 64), Volumes: []domain.HostingStackVolume{}}
	for i, image := range plan.Stack.Images {
		state.Components = append(state.Components, domain.HostingStackComponent{Component: image.Component, ContainerID: strings.Repeat(string(rune('b'+i)), 64), ImageID: image.Release.ImageID, OperationID: "hop_old", Running: true, Health: "healthy"})
	}
	healthy := domain.HostingHealthy{OperationID: "hop_old", Snapshot: plan.Snapshot, Release: plan.Release, PlanHash: plan.Hash, Stack: &state}
	stop, err := domain.CompileHostingStackStopPlan(healthy, plan)
	if err != nil {
		t.Fatal(err)
	}
	return stop
}

func TestStackAbsentPredecessorRequiresConfirmedStop(t *testing.T) {
	stop := stackStopTestPlan(t)
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing without receipt", true: "confirmed stopped receipt"}[confirmed], func(t *testing.T) {
			plan := stop.TargetPlan
			plan.PreviousHealthy = &stop.Target
			if confirmed {
				plan.Snapshot.StackStoppedOperationID = "hop_stop"
				plan.PreviousStopped = &domain.HostingStackStopped{StopOperationID: "hop_stop", Target: stop.Target}
			}
			engine := testImageEngine(t)
			engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				if args[4] == "container" && args[5] == "ls" {
					return nil, nil
				}
				t.Fatal("absent predecessor attempted mutation", args[4:])
				return nil, errors.New("unexpected command")
			})
			if err := engine.stopStackPredecessor(t.Context(), plan); (err == nil) != confirmed {
				t.Fatal("absence authority", err)
			}
		})
	}
}

func TestStackStoppedObservationRejectsForeignProjectInventory(t *testing.T) {
	stop := stackStopTestPlan(t)
	engine := testImageEngine(t)
	engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
		switch args[4] {
		case "info":
			return json.Marshal(stop.Target.Release.DaemonID)
		case "network":
			if args[5] == "ls" {
				return []byte(stop.Target.Stack.NetworkID), nil
			}
			return json.Marshal(map[string]any{"Id": stop.Target.Stack.NetworkID, "Name": stop.TargetPlan.Stack.NetworkName, "Driver": "bridge", "Scope": "local", "Internal": false, "Labels": map[string]string{imageBindingLabel: stop.Target.Snapshot.BindingID, stackOriginLabel: "hop_old"}})
		case "container":
			if args[5] == "ls" {
				return []byte(strings.Repeat("f", 64)), nil
			}
		}
		return nil, errors.New("foreign inventory cannot be inspected")
	})
	if _, err := engine.observeStoppedStack(t.Context(), stop); err == nil {
		t.Fatal("foreign inventory reported stopped")
	}
}

func TestStackStopPlanRejectsChangedTargetAndRetainedOrigins(t *testing.T) {
	stop := stackStopTestPlan(t)
	if stop.Validate() != nil {
		t.Fatal("valid stop rejected")
	}
	changed := stop
	changed.Target.OperationID = "hop_other"
	if changed.Validate() == nil {
		t.Fatal("changed target accepted")
	}
	state := *stop.Target.Stack
	state.Components = []domain.HostingStackComponent{}
	if domain.ValidateHostingStackStoppedState(stop, state) != nil {
		t.Fatal("retained empty inventory rejected")
	}
	state.NetworkID = strings.Repeat("f", 64)
	if domain.ValidateHostingStackStoppedState(stop, state) == nil {
		t.Fatal("changed network accepted")
	}
}

func TestStackStopRemovesOnlyAfterAllWritersStop(t *testing.T) {
	for _, mode := range []string{"success", "stop failed", "foreign image"} {
		t.Run(mode, func(t *testing.T) {
			stop := stackStopTestPlan(t)
			running, removed := true, false
			stops, removals := 0, 0
			engine := testImageEngine(t)
			ids := []string{stop.Target.Stack.Components[0].ContainerID, stop.Target.Stack.Components[1].ContainerID}
			engine.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				switch args[4] {
				case "info":
					return json.Marshal(stop.Target.Release.DaemonID)
				case "network":
					if args[5] == "ls" {
						return []byte(stop.Target.Stack.NetworkID), nil
					}
					if args[len(args)-1] == "{{json .Containers}}" {
						return []byte(`{}`), nil
					}
					return json.Marshal(map[string]any{"Id": stop.Target.Stack.NetworkID, "Name": stop.TargetPlan.Stack.NetworkName, "Driver": "bridge", "Scope": "local", "Labels": map[string]string{imageBindingLabel: stop.Target.Snapshot.BindingID}})
				case "container":
					switch args[5] {
					case "ls":
						if removed {
							return nil, nil
						}
						return []byte(strings.Join(ids, "\n")), nil
					case "inspect":
						for i, component := range stop.Target.Stack.Components {
							if component.ContainerID != args[6] {
								continue
							}
							image := component.ImageID
							if mode == "foreign image" && i == 1 {
								image = "sha256:" + strings.Repeat("f", 64)
							}
							ports := map[string]any{}
							if component.Component == stop.Target.Stack.PublicComponent {
								ports["80/tcp"] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "18080"}}
							}
							return json.Marshal(map[string]any{"Id": component.ContainerID, "Image": image, "Labels": map[string]string{imageBindingLabel: stop.Target.Snapshot.BindingID, imageOperationLabel: stop.Target.OperationID, imagePlanLabel: stop.Target.PlanHash, imageReleaseLabel: stop.Target.Release.Hash, stackComponentLabel: component.Component}, "State": map[string]any{"Running": running, "ExitCode": 0}, "Mounts": []any{}, "Networks": map[string]any{stop.TargetPlan.Stack.NetworkName: map[string]any{"NetworkID": stop.Target.Stack.NetworkID, "Aliases": []string{component.Component}}}, "Ports": ports})
						}
					case "stop":
						stops++
						if mode == "stop failed" {
							return nil, errors.New("interrupted")
						}
						running = false
						return nil, nil
					case "rm":
						if running {
							t.Fatal("removed running writer")
						}
						removals++
						removed = true
						return nil, nil
					}
				}
				return nil, errors.New("unexpected command")
			})
			result := engine.stopStack(t.Context(), stop)
			if mode == "success" {
				if result.Outcome != "succeeded" || !result.StackStopped || result.Uncertain || removals != 1 || stops != 1 {
					t.Fatalf("stop failed: %+v stops=%d removals=%d", result, stops, removals)
				}
			} else if !result.Uncertain || result.StackStopped || removals != 0 || mode == "foreign image" && stops != 0 {
				t.Fatal("unsafe failed stop", result, stops, removals)
			}
		})
	}
}
