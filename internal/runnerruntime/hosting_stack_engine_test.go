package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

func stackTestPlan(t *testing.T) domain.HostingApplyPlan {
	t.Helper()
	definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: `services:
  app:
    image: nginx:alpine
    environment:
      PASSWORD: ${PASSWORD:?required}
      DEFAULT: ${OPTIONAL:-fallback}
    depends_on:
      db: {condition: service_healthy}
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: ${PASSWORD}
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER"]
      interval: 1s
      retries: 3
`, PublicComponent: "app", ContainerPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	images := []domain.HostingStackImage{}
	for _, service := range definition.Services {
		release := imageTestRelease()
		if service.Name == "db" {
			release.CanonicalImage = "docker.io/library/postgres@sha256:" + strings.Repeat("c", 64)
			release.ImageID = "sha256:" + strings.Repeat("d", 64)
		}
		release.Hash = ""
		release.Hash = domain.HostingDocumentHash(release)
		images = append(images, domain.HostingStackImage{Component: service.Name, Release: release})
	}
	release := domain.HostingRelease{DaemonID: images[0].Release.DaemonID, Stack: domain.HostingStackReleaseRef{Hash: domain.HostingDocumentHash(images)}}
	release.Hash = domain.HostingDocumentHash(release)
	snapshot := imageTestPlan(t).Snapshot
	snapshot.ImageReference = ""
	snapshot.PolicyVersion = domain.HostingStackPolicyVersion
	snapshot.Stack = domain.HostingStackRef{Generation: 1, Hash: domain.HostingDocumentHash(definition)}
	snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}
	plan, err := domain.CompileHostingStackPlan(snapshot, release, nil, domain.HostingStackPlan{Definition: definition, Images: images, Volumes: []domain.HostingStackVolume{}, NetworkName: domain.HostingStackObjectID(snapshot.BindingID, "network", "private")})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func stackTestEnvironment(t *testing.T, plan domain.HostingApplyPlan, password string) *hostingruntimeprotocol.Environment {
	t.Helper()
	material, err := hostingruntimeprotocol.NewEnvironment(hostingruntimeprotocol.Metadata{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, PlanHash: plan.Hash, RuntimeConfiguration: plan.Snapshot.RuntimeConfiguration, DeliveryID: strings.Repeat("a", 32), RequestHash: "sha256:" + strings.Repeat("b", 64), Purpose: hostingruntimeprotocol.Purpose, UseUntil: time.Now().Add(time.Minute)}, []hostingruntimeprotocol.Entry{{Name: "PASSWORD", Value: []byte(password)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(material.Close)
	return material
}

func TestStackRenderingExpandsProtectedValuesExactlyOnce(t *testing.T) {
	plan := stackTestPlan(t)
	password := "literal$cash$$${EVIL}"
	t.Setenv("EVIL", "ambient-must-not-enter")
	material := stackTestEnvironment(t, plan, password)
	body, err := renderStack("hop", plan, material)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	// Compose serializes literal dollars escaped again so its config output
	// remains safe as input. Actual container bytes are checked by the runtime gate.
	var document struct {
		Services map[string]struct {
			Environment map[string]string
			Healthcheck struct{ Test []string }
			Ports       []string
		}
	}
	if json.Unmarshal(body, &document) != nil {
		t.Fatal("invalid rendered document")
	}
	if document.Services["app"].Environment["PASSWORD"] != strings.ReplaceAll(password, "$", "$$") || document.Services["app"].Environment["DEFAULT"] != "fallback" || document.Services["db"].Healthcheck.Test[1] != "pg_isready -U $$POSTGRES_USER" || len(document.Services["db"].Ports) != 0 {
		t.Fatal("protected value, escaped container variable or private database changed")
	}
	// Recompile after changing the source reference; ambient values must never satisfy it.
	stack := *plan.Stack
	stack.Definition.Services = append([]domain.HostingStackService{}, stack.Definition.Services...)
	stack.Definition.Services[0].Environment = map[string]string{"VALUE": "${EVIL:?private-error-operand}"}
	snapshot := plan.Snapshot
	snapshot.Stack.Hash = domain.HostingDocumentHash(stack.Definition)
	missing, err := domain.CompileHostingStackPlan(snapshot, plan.Release, nil, stack)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = renderStack("hop", missing, material); err == nil || strings.Contains(err.Error(), "private-error-operand") || strings.Contains(err.Error(), "ambient-must-not-enter") {
		t.Fatal("ambient interpolation or secret error disclosure")
	}
}

func TestStackCommandPresenceSurvivesPlanAndRendering(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		present      bool
		length       int
	}{
		{name: "omitted"},
		{name: "empty", source: "    command: []\n", present: true},
		{name: "override", source: "    command: [\"serve\"]\n", present: true, length: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, err := domain.ParseHostingStack(domain.HostingStackSource{Original: "services:\n  app:\n    image: nginx:alpine\n" + tc.source, PublicComponent: "app", ContainerPort: 80})
			if err != nil {
				t.Fatal(err)
			}
			plan := stackTestPlan(t)
			plan.Stack.Definition.Services[0].Command = definition.Services[0].Command
			plan.Snapshot.Stack.Hash = domain.HostingDocumentHash(plan.Stack.Definition)
			plan, err = domain.CompileHostingStackPlan(plan.Snapshot, plan.Release, nil, *plan.Stack)
			if err != nil {
				t.Fatal(err)
			}
			// The durable plan must retain [] separately from an omitted command.
			encoded, err := json.Marshal(plan)
			if err != nil || json.Unmarshal(encoded, &plan) != nil {
				t.Fatal("plan round trip failed")
			}
			body, err := renderStack("hop", plan, stackTestEnvironment(t, plan, "password"))
			if err != nil {
				t.Fatal(err)
			}
			defer clear(body)
			var document struct {
				Services map[string]map[string]json.RawMessage
			}
			if json.Unmarshal(body, &document) != nil {
				t.Fatal("invalid rendered document")
			}
			command, present := document.Services["app"]["command"]
			if present != tc.present {
				t.Fatal("command presence changed")
			}
			if present {
				var values []string
				if json.Unmarshal(command, &values) != nil || values == nil || len(values) != tc.length {
					t.Fatal("explicit command changed")
				}
			}
		})
	}
}

func TestStackReadinessRequiresSuccessfulInitExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		exit    int
		ready   bool
	}{
		{name: "completed", ready: true},
		{name: "failed", exit: 1},
		{name: "still running", running: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := stackTestPlan(t)
			plan.Stack.Definition.Services[0].DependsOn["db"] = "service_completed_successfully"
			state := domain.HostingStackState{ObservationComplete: true, PublicComponent: "app"}
			for _, image := range plan.Stack.Images {
				component := domain.HostingStackComponent{Component: image.Component, ContainerID: strings.Repeat("a", 64), ImageID: image.Release.ImageID, OperationID: "hop", Running: true}
				if image.Component == "db" {
					component.Running, component.ExitCode = tc.running, tc.exit
				}
				state.Components = append(state.Components, component)
			}
			if err := domain.ValidateHostingStackState(plan, "hop", state, true); (err == nil) != tc.ready {
				t.Fatalf("init readiness: %v", err)
			}
		})
	}
}

// This check starts no containers and contacts no daemon. It exercises Compose's
// real interpolation pass through the same protected stdin command adapter.
func TestStackProtectedDocumentWithInstalledCompose(t *testing.T) {
	if os.Getenv("NEROCD_TEST_COMPOSE_CONFIG") != "1" {
		t.Skip("set NEROCD_TEST_COMPOSE_CONFIG=1 for installed Compose config check")
	}
	plan := stackTestPlan(t)
	password := "literal$cash$$${EVIL}"
	body, err := renderStack("hop", plan, stackTestEnvironment(t, plan, password))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	e := testImageEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = e.prepareCompose(ctx); err != nil {
		t.Fatal(err)
	}
	name, prefix := e.stackCommandArguments()
	args := append(prefix, "--project-directory", e.configDir, "--env-file", "/dev/null", "--project-name", plan.Snapshot.RuntimeNamespace, "--file", "-", "config", "--format", "json")
	output, err := (osProvenanceCommand{}).RunProtectedInput(ctx, name, args, e.configDir, []string{"DOCKER_CONFIG=" + e.configDir, "DOCKER_HOST=" + e.endpoint, "DOCKER_CONTEXT="}, body)
	if err != nil {
		t.Fatalf("installed Compose rejected protected document: %v; %s", err, strings.ReplaceAll(string(output), password, "[fixture]"))
	}
	defer clear(output)
	var document struct {
		Services map[string]struct {
			Environment map[string]string
			Healthcheck struct{ Test []string }
		}
	}
	if json.Unmarshal(output, &document) != nil || document.Services["app"].Environment["PASSWORD"] != strings.ReplaceAll(password, "$", "$$") || document.Services["db"].Environment["POSTGRES_PASSWORD"] != strings.ReplaceAll(password, "$", "$$") {
		t.Fatal("Compose reinterpreted protected references during round-trip rendering")
	}
	if got := document.Services["db"].Healthcheck.Test[1]; got != "pg_isready -U $$POSTGRES_USER" && got != "pg_isready -U $POSTGRES_USER" {
		t.Fatal("Compose changed container-side health variable")
	}
}

func TestStackStopBarrierRejectsForeignAndStillRunningWriters(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "still-running", true: "foreign-project"}[foreign], func(t *testing.T) {
			plan := stackTestPlan(t)
			id := strings.Repeat("f", 64)
			old := domain.HostingHealthy{Snapshot: plan.Snapshot, OperationID: "hop_old", PlanHash: plan.Hash, Release: plan.Release, Stack: &domain.HostingStackState{Components: []domain.HostingStackComponent{{Component: "app", ContainerID: id, ImageID: plan.Stack.Images[0].Release.ImageID, OperationID: "hop_old", Running: true}}}}
			plan.PreviousHealthy = &old
			e := testImageEngine(t)
			stops := 0
			e.command = imageCommandFunc(func(_ context.Context, _ string, args []string, _ string, _ []string) ([]byte, error) {
				switch args[4] {
				case "info":
					return []byte(`"daemon-image-test"`), nil
				case "container":
					switch args[5] {
					case "ls":
						if foreign && strings.Contains(args[len(args)-1], "com.docker.compose.project") {
							return []byte(strings.Repeat("e", 64)), nil
						}
						return []byte(id), nil
					case "inspect":
						return json.Marshal(map[string]any{"Id": id, "Image": old.Stack.Components[0].ImageID, "Labels": map[string]string{imageBindingLabel: plan.Snapshot.BindingID, imageOperationLabel: old.OperationID, imagePlanLabel: old.PlanHash, imageReleaseLabel: old.Release.Hash, stackComponentLabel: "app"}, "State": map[string]any{"Running": true}})
					case "stop":
						stops++
						return nil, nil
					case "rm":
						t.Fatal("removed a container before proving all writers stopped")
					}
				}
				return nil, errors.New("unexpected command")
			})
			if e.stopStackPredecessor(t.Context(), plan) == nil || foreign && stops != 0 || !foreign && stops != 1 {
				t.Fatal("stack writer barrier was bypassed")
			}
		})
	}
}
