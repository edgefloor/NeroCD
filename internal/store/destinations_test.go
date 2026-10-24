package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type destinationContractStore interface {
	store.DestinationRepository
	store.RunnerRepository
	store.HostingRepository
	store.AuditRepository
}

func destinationEvent(id string) domain.AuditEvent {
	return domain.AuditEvent{ID: "aud_destination_" + id, ActorID: "usr_bootstrap", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Metadata: map[string]any{"untrusted": "discard"}}
}
func enrollDestinationRunner(t *testing.T, s destinationContractStore, id string, capabilities []string) domain.Runner {
	t.Helper()
	now := time.Now().UTC()
	enrollment, err := s.CreateRunnerEnrollment(t.Context(), domain.RunnerEnrollment{ID: "enroll_" + id, TokenHash: "enrollhash_" + id, RunnerID: id, RunnerName: id, Tags: []string{"shared-tag"}, Capabilities: capabilities, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, destinationEvent("enroll_"+id))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := s.ConsumeRunnerEnrollment(t.Context(), domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "consume_" + id, CredentialHash: "credential_" + id}, destinationEvent("consume_"+id))
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func createDestinationTarget(t *testing.T, s destinationContractStore, id string) (domain.Server, domain.Destination) {
	t.Helper()
	runner := enrollDestinationRunner(t, s, "runner_"+id, []string{domain.RunTypeComposeDeploy})
	event := destinationEvent("server_" + id)
	server := domain.Server{ID: "server_" + id, Name: id, RunnerID: runner.ID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}
	destination := domain.Destination{ID: "destination_" + id, ServerID: server.ID, Name: id, RuntimeKind: "docker_compose", CreatedAt: event.CreatedAt}
	got, dest, err := s.CreateServerWithDestination(t.Context(), server, destination, event)
	if err != nil {
		t.Fatal(err)
	}
	return got, dest
}
func createDestinationResource(t *testing.T, s destinationContractStore, id, project string) domain.Resource {
	t.Helper()
	now := time.Now().UTC()
	env, err := s.CreateProjectEnvironment(t.Context(), domain.ProjectEnvironment{ID: "env_" + id, ProjectID: project, Name: id, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(t.Context(), domain.Resource{ID: "resource_" + id, ProjectID: project, ProjectEnvironmentID: env.ID, Name: id, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return resource
}
func grantDestination(t *testing.T, s destinationContractStore, dest, project, id string) domain.DestinationProjectGrant {
	t.Helper()
	event := destinationEvent(id)
	grant, err := s.GrantDestinationProject(t.Context(), domain.DestinationProjectGrant{DestinationID: dest, ProjectID: project, GrantedBy: event.ActorID, CreatedAt: event.CreatedAt}, event)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func TestDestinationRepositoryContract(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		destinationRepositoryContract(t, store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash"))
	})
	t.Run("postgres", func(t *testing.T) {
		pg, _ := openPostgresIntegrationStore(t, t.Context(), "dest_contract")
		t.Cleanup(func() { closePostgresStore(t, pg) })
		destinationRepositoryContract(t, pg)
	})
}
func destinationRepositoryContract(t *testing.T, s destinationContractStore) {
	ctx := t.Context()
	server, dest := createDestinationTarget(t, s, "first")
	_, other := createDestinationTarget(t, s, "second")
	resource := createDestinationResource(t, s, "first", "proj_platform")
	cross := createDestinationResource(t, s, "cross", "proj_security")
	input := store.ResourceBindingInput{ID: "binding_first", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}
	if _, err := s.BindResourceDestination(ctx, input, destinationEvent("without_grant")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("absent grant: %v", err)
	}
	grant := grantDestination(t, s, dest.ID, "proj_platform", "grant")
	crossInput := input
	crossInput.ID = "binding_cross"
	crossInput.ResourceID = cross.ID
	if _, err := s.BindResourceDestination(ctx, crossInput, destinationEvent("cross")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross project bind: %v", err)
	}
	// Colliding audit identifiers must roll back every owned mutation, including generated namespace allocation.
	duplicate := destinationEvent("grant")
	if _, err := s.BindResourceDestination(ctx, input, duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("bind audit collision: %v", err)
	}
	if _, err := s.GetResourceBinding(ctx, resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed bind survived")
	}
	if _, err := s.RevokeDestinationProject(ctx, dest.ID, "proj_platform", duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoke audit collision: %v", err)
	}
	current, err := s.GetDestinationProjectGrant(ctx, dest.ID, "proj_platform")
	if err != nil || current.RevokedAt != nil {
		t.Fatal("failed revoke changed grant")
	}
	failedGrant := grant
	failedGrant.DestinationID = other.ID
	if _, err := s.GrantDestinationProject(ctx, failedGrant, duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("grant audit collision: %v", err)
	}
	if _, err := s.GetDestinationProjectGrant(ctx, other.ID, "proj_platform"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed grant survived")
	}
	// A valid fresh runner still cannot create a server if its audit cannot commit.
	runner := enrollDestinationRunner(t, s, "runner_failed", []string{"compose_deploy"})
	candidate := server
	candidate.ID = "server_failed"
	candidate.RunnerID = runner.ID
	failedDest := dest
	failedDest.ID = "destination_failed"
	failedDest.ServerID = candidate.ID
	if _, _, err := s.CreateServerWithDestination(ctx, candidate, failedDest, duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("create audit collision: %v", err)
	}
	if _, err := s.GetServer(ctx, candidate.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed server survived")
	}
	if _, err := s.GetDestination(ctx, failedDest.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed destination survived")
	}
	// Different destinations and matching runner tags do not permit a resource to acquire a second binding.
	grantDestination(t, s, other.ID, "proj_platform", "other_grant")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := input
			in.ID = fmt.Sprintf("binding_cas_%d", i)
			if i%2 == 1 {
				in.DestinationID = other.ID
			}
			_, err := s.BindResourceDestination(ctx, in, destinationEvent(fmt.Sprintf("cas_%d", i)))
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("bind winners=%d", wins)
	}
	binding, err := s.GetResourceBinding(ctx, resource.ID)
	if err != nil || !regexp.MustCompile(`^ncd_[a-f0-9]{36}$`).MatchString(binding.RuntimeNamespace) {
		t.Fatalf("generated namespace: %+v %v", binding, err)
	}
	resolved, err := s.ResolveResourceDestination(ctx, resource.ID)
	if err != nil || resolved.Binding != binding || resolved.Destination.ID != binding.DestinationID || resolved.Server.ID != resolved.Destination.ServerID || resolved.Runner.ID != resolved.Server.RunnerID || resolved.Grant.ProjectID != resource.ProjectID || resolved.Grant.RevokedAt != nil {
		t.Fatalf("resolution: %+v %v", resolved, err)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil || strings.Contains(string(encoded), "credential_") {
		t.Fatal("resolution serialized runner credential")
	}
	if _, err = s.RevokeDestinationProject(ctx, binding.DestinationID, resource.ProjectID, destinationEvent("revoke")); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.GetResourceBinding(ctx, resource.ID)
	if err != nil || remaining != binding {
		t.Fatal("revocation moved/deleted binding")
	}
	resolved, err = s.ResolveResourceDestination(ctx, resource.ID)
	if err != nil || resolved.Grant.RevokedAt == nil || resolved.Binding != binding {
		t.Fatal("resolution hid grant revocation")
	}
	list, err := s.ListProjectDestinations(ctx, resource.ProjectID, store.Page{})
	if err != nil || list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID == binding.DestinationID {
		t.Fatalf("active grant list: %+v %v", list, err)
	}
	unbound := createDestinationResource(t, s, "revoked", "proj_platform")
	revokedInput := input
	revokedInput.ID = "binding_revoked"
	revokedInput.ResourceID = unbound.ID
	revokedInput.DestinationID = binding.DestinationID
	if _, err = s.BindResourceDestination(ctx, revokedInput, destinationEvent("after_revoke")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoked grant bind: %v", err)
	}
	regrant := grantDestination(t, s, binding.DestinationID, resource.ProjectID, "regrant")
	if regrant.RevokedAt != nil {
		t.Fatal("regrant remains revoked")
	}
	if _, err = s.BindResourceDestination(ctx, revokedInput, destinationEvent("after_regrant")); err != nil {
		t.Fatal(err)
	}
	bindingPage, err := s.ListResourceBindings(ctx, binding.DestinationID, store.Page{Enabled: true, Limit: 1, LimitSpecified: true})
	if err != nil || bindingPage.Total != 2 || len(bindingPage.Items) != 1 {
		t.Fatalf("binding page: %+v %v", bindingPage, err)
	}
	serverPage, err := s.ListServers(ctx, store.Page{Enabled: true, Limit: 1, LimitSpecified: true})
	if err != nil || serverPage.Total != 2 || len(serverPage.Items) != 1 {
		t.Fatalf("server page: %+v %v", serverPage, err)
	}
	destinationPage, err := s.ListDestinations(ctx, server.ID, store.Page{})
	if err != nil || destinationPage.Total != 1 || destinationPage.Items[0].ID != dest.ID {
		t.Fatal("server destination list")
	}
	grants, err := s.ListDestinationProjectGrants(ctx, dest.ID, store.Page{})
	if err != nil || grants.Total != 1 {
		t.Fatal("grant page")
	}
	if _, err = s.UpdateRunnerToken(ctx, resolved.Runner.ID, "", domain.RunnerRevoked, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resolved, err = s.ResolveResourceDestination(ctx, resource.ID)
	if err != nil || resolved.Runner.Status != domain.RunnerRevoked || resolved.Runner.ID != bindingRunner(t, s, binding) {
		t.Fatal("inactive runner replaced or hidden")
	}
}
func bindingRunner(t *testing.T, s destinationContractStore, b domain.ResourceBinding) string {
	t.Helper()
	d, err := s.GetDestination(t.Context(), b.DestinationID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := s.GetServer(t.Context(), d.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	return server.RunnerID
}
func TestDestinationRunnerAdmission(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var s destinationContractStore
			if backend == "memory" {
				s = store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", "hash", "hash")
			} else {
				pg, _ := openPostgresIntegrationStore(t, t.Context(), "dest_admission")
				t.Cleanup(func() { closePostgresStore(t, pg) })
				s = pg
			}
			for _, mode := range []string{"legacy", "shell", "historical shell", "wrong capability", "inactive", "empty credential", "unused enrollment", "revoked enrollment", "valid", "historical alias", "database valid", "database missing deploy", "database missing database", "database missing storage", "database shell", "database inactive", "database empty credential", "database unused enrollment", "database revoked enrollment", "database legacy"} {
				t.Run(mode, func(t *testing.T) {
					id := "runner_" + strings.ReplaceAll(mode, " ", "_")
					scenario := strings.TrimPrefix(mode, "database ")
					caps := []string{domain.RunTypeComposeDeploy}
					if strings.HasPrefix(mode, "database ") {
						caps = []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability}
						switch scenario {
						case "missing deploy":
							caps = caps[1:]
						case "missing database":
							caps = []string{domain.RunTypeDatabaseDeploy, domain.HostingOwnedStorageCapability}
						case "missing storage":
							caps = caps[:2]
						}
					}
					var runner domain.Runner
					if scenario == "unused enrollment" || scenario == "revoked enrollment" {
						now := time.Now().UTC()
						if _, err := s.CreateRunnerEnrollment(t.Context(), domain.RunnerEnrollment{ID: "pending_" + id, TokenHash: "pendinghash_" + id, RunnerID: id, RunnerName: id, Capabilities: caps, Tags: []string{}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, destinationEvent("pending_"+id)); err != nil {
							t.Fatal(err)
						}
						if scenario == "revoked enrollment" {
							if _, err := s.RevokeRunnerEnrollment(t.Context(), "pending_"+id, destinationEvent("revoke_"+id)); err != nil {
								t.Fatal(err)
							}
						}
					}
					if scenario == "legacy" || scenario == "unused enrollment" || scenario == "revoked enrollment" {
						var err error
						runner, err = s.RegisterRunner(t.Context(), domain.Runner{ID: id, Name: id, Capabilities: caps, Tags: []string{"shared-tag"}, TokenHash: "credential", Status: domain.RunnerActive, RegisteredAt: time.Now(), LastHeartbeatAt: time.Now()})
						if err != nil {
							t.Fatal(err)
						}
					} else {
						if mode == "historical alias" {
							caps = []string{"compose_deploy"}
						}
						if mode == "historical shell" {
							caps = []string{"compose_deploy", "shell"}
						}
						if scenario == "shell" {
							caps = append(caps, "shell")
						}
						if mode == "wrong capability" {
							caps = []string{"compose-deploy-other"}
						}
						runner = enrollDestinationRunner(t, s, id, caps)
					}
					if scenario == "inactive" {
						var err error
						runner, err = s.UpdateRunnerToken(t.Context(), runner.ID, runner.TokenHash, domain.RunnerRevoked, time.Now())
						if err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "empty credential" {
						var err error
						runner, err = s.UpdateRunnerToken(t.Context(), runner.ID, "", domain.RunnerActive, time.Now())
						if err != nil {
							t.Fatal(err)
						}
					}
					event := destinationEvent("admit_" + id)
					server := domain.Server{ID: "server_" + id, RunnerID: runner.ID, Name: id, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}
					destination := domain.Destination{ID: "destination_" + id, ServerID: server.ID, Name: id, RuntimeKind: "docker_compose", CreatedAt: event.CreatedAt}
					_, _, err := s.CreateServerWithDestination(t.Context(), server, destination, event)
					if scenario == "valid" || mode == "historical alias" {
						if _, revokeErr := s.RevokeRunnerEnrollment(t.Context(), "enroll_"+id, destinationEvent("revoke_used_"+id)); !errors.Is(revokeErr, store.ErrNotFound) {
							t.Fatalf("consumed enrollment revocation unexpectedly changed runner authority: %v", revokeErr)
						}
						if err != nil {
							t.Fatal(err)
						}
						server.ID += "duplicate"
						destination.ID += "duplicate"
						destination.ServerID = server.ID
						if _, _, err = s.CreateServerWithDestination(t.Context(), server, destination, destinationEvent("duplicate_runner")); !errors.Is(err, store.ErrConflict) {
							t.Fatal("runner reused")
						}
					} else {
						if !errors.Is(err, store.ErrConflict) {
							t.Fatalf("ineligible %s runner admitted: %v", mode, err)
						}
						if _, err := s.GetServer(t.Context(), server.ID); !errors.Is(err, store.ErrNotFound) {
							t.Fatal("rejected admission retained server")
						}
						if _, err := s.GetDestination(t.Context(), destination.ID); !errors.Is(err, store.ErrNotFound) {
							t.Fatal("rejected admission retained destination")
						}
					}
					events, err := s.ListAuditEvents(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, audit := range events {
						if audit.ID == event.ID {
							found = true
							if audit.Action != "server.created" || audit.TargetID != "server_"+id {
								t.Fatal("server admission audit has incorrect action or target")
							}
						}
					}
					if want := scenario == "valid" || mode == "historical alias"; found != want {
						t.Fatalf("server admission audit present=%v want=%v", found, want)
					}
				})
			}
		})
	}
}
