package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type auditPaginationContractStore interface {
	store.BootstrapRepository
	store.UserRepository
	store.SessionRepository
	store.APITokenRepository
	store.ServiceAccountRepository
	store.OIDCRepository
	store.HostingRepository
	store.ProjectRepository
	store.ProjectMemberRepository
	store.TemplateRepository
	store.SourceRepository
	store.RunRepository
	store.RunnerRepository
	store.ApprovalRepository
	store.AuditRepository
	store.DeploymentRepository
	store.OperationalSnapshotRepository
	store.OperationalObservationWriter
	store.OperationalObservationReader
	store.RunLogRetentionRepository
}

func TestAuditPaginationFiltersAuthorizationBeforePaging(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		auditPaginationContract(t, store.NewMemoryStore())
	})
	t.Run("postgres", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		pg, _ := openPostgresIntegrationStore(t, ctx, "audit_pagination_contract")
		defer closePostgresStore(t, pg)
		auditPaginationContract(t, pg)
	})
}

func auditPaginationContract(t *testing.T, repository auditPaginationContractStore) {
	t.Helper()
	ctx := t.Context()
	const (
		viewerID        = "usr_audit_viewer"
		allowedProject  = "proj_audit_allowed"
		deniedProject   = "proj_audit_denied"
		visibleEventCnt = 113
	)
	baseTime := time.Date(2026, time.October, 15, 6, 0, 0, 0, time.UTC)
	if err := repository.BootstrapAdmin(ctx, domain.User{ID: viewerID, Email: "audit-viewer@example.local", Name: "Audit Viewer", Status: domain.UserActive, GlobalRole: domain.RoleUser, CreatedAt: baseTime.Add(-time.Hour)}, domain.AuditEvent{ID: "audit_bootstrap_viewer", ActorID: "system", Action: "test.bootstrap", TargetID: "bootstrap-admin", CreatedAt: baseTime.Add(-time.Hour)}); err != nil {
		t.Fatalf("create audit viewer: %v", err)
	}
	for _, project := range []domain.Project{
		{ID: allowedProject, Name: "Allowed audit project", CreatedAt: baseTime},
		{ID: deniedProject, Name: "Denied audit project", CreatedAt: baseTime},
	} {
		if _, err := repository.CreateProject(ctx, project); err != nil {
			t.Fatalf("create project %s: %v", project.ID, err)
		}
	}
	if _, err := repository.UpsertProjectMember(ctx, domain.ProjectMember{ID: "pm_audit_viewer", ProjectID: allowedProject, UserID: viewerID, Role: domain.RoleViewer, CreatedAt: baseTime, UpdatedAt: baseTime}); err != nil {
		t.Fatalf("create audit viewer membership: %v", err)
	}
	if _, err := repository.CreateRun(ctx, domain.TaskRun{ID: "run_audit_allowed", ProjectID: allowedProject, RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: viewerID, StartedAt: baseTime}); err != nil {
		t.Fatalf("create audit run: %v", err)
	}
	if _, err := repository.CreateTemplate(ctx, domain.TaskTemplate{ID: "tpl_audit_allowed", ProjectID: allowedProject, Name: "Audit template", Kind: domain.RunTypeShell, RunSpec: domain.RunSpec{Type: domain.RunTypeShell}, Workflow: domain.Workflow{}, RunnerTags: []string{}}); err != nil {
		t.Fatalf("create audit template: %v", err)
	}
	if _, err := repository.CreateRepository(ctx, domain.Repository{ID: "repo_audit_allowed", ProjectID: allowedProject, Name: "Audit repository", URL: "https://example.local/audit.git", Provider: domain.ProviderGit, DefaultRef: "main", Policy: domain.RepositoryPolicy{Version: 1, State: "legacy_unverified"}, CreatedAt: baseTime}); err != nil {
		t.Fatalf("create audit repository: %v", err)
	}

	for index := 0; index < 105; index++ {
		createAuditContractEvent(t, repository, domain.AuditEvent{
			ID: fmt.Sprintf("audit_allowed_bulk_%03d", index), ActorID: "usr_other", Action: "allowed.bulk",
			TargetID: allowedProject, CreatedAt: baseTime.Add(time.Duration(index) * time.Second),
		})
	}
	visibleEdges := []domain.AuditEvent{
		{ID: "audit_metadata_unicode", ActorID: "usr_other", Action: "allowed.metadata_unicode", TargetID: "unknown", Metadata: map[string]any{"project_id": "\u0085\u2003" + allowedProject + "\u3000\u00a0"}},
		{ID: "audit_project_direct", ActorID: "usr_other", Action: "allowed.project", TargetID: allowedProject},
		{ID: "audit_run_trimmed", ActorID: "usr_other", Action: "allowed.run", TargetID: "run_audit_allowed\u2003"},
		{ID: "audit_template_trimmed", ActorID: "usr_other", Action: "allowed.template", TargetID: "tpl_audit_allowed\u00a0"},
		{ID: "audit_repository_trimmed", ActorID: "usr_other", Action: "allowed.repository", TargetID: "repo_audit_allowed\u3000"},
		{ID: "audit_metadata_nonstring", ActorID: "usr_other", Action: "allowed.metadata_nonstring", TargetID: allowedProject, Metadata: map[string]any{"project_id": 17}},
		{ID: "audit_metadata_blank", ActorID: "usr_other", Action: "allowed.metadata_blank", TargetID: allowedProject, Metadata: map[string]any{"project_id": "\u0085\u2003\u3000\u00a0"}},
		{ID: "audit_actor_own", ActorID: viewerID, Action: "allowed.actor", TargetID: "unrecognized_target"},
	}
	for index, event := range visibleEdges {
		event.CreatedAt = baseTime.Add(time.Duration(200+index) * time.Second)
		createAuditContractEvent(t, repository, event)
	}
	hiddenEdges := []domain.AuditEvent{
		{ID: "audit_metadata_precedence_denied", ActorID: "usr_other", Action: "hidden.precedence", TargetID: allowedProject, Metadata: map[string]any{"project_id": deniedProject}},
		{ID: "audit_missing_run", ActorID: "usr_other", Action: "hidden.missing", TargetID: "run_missing"},
		{ID: "audit_unknown_target", ActorID: "usr_other", Action: "hidden.unknown", TargetID: "unknown_target"},
		{ID: "audit_project_not_trimmed", ActorID: "usr_other", Action: "hidden.project_whitespace", TargetID: allowedProject + "\u2003"},
		{ID: "audit_raw_prefix_required", ActorID: "usr_other", Action: "hidden.raw_prefix", TargetID: "\u2003run_audit_allowed"},
	}
	for index, event := range hiddenEdges {
		event.CreatedAt = baseTime.Add(time.Duration(210+index) * time.Second)
		createAuditContractEvent(t, repository, event)
	}
	for index := 0; index < 101; index++ {
		createAuditContractEvent(t, repository, domain.AuditEvent{
			ID: fmt.Sprintf("audit_denied_newer_%03d", index), ActorID: "usr_other", Action: "denied.newer",
			TargetID: deniedProject, CreatedAt: baseTime.Add(time.Duration(300+index) * time.Second),
		})
	}

	scope := store.AuditAuthorizationScope{ActorID: viewerID, ProjectIDs: []string{allowedProject}}
	first, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: store.MaxPageLimit, LimitSpecified: true})
	if err != nil {
		t.Fatalf("list first scoped audit page: %v", err)
	}
	if first.Total != visibleEventCnt || first.Limit != store.MaxPageLimit || first.Offset != 0 || len(first.Items) != store.MaxPageLimit {
		t.Fatalf("first scoped audit page = %#v, want 100 of %d visible events", first, visibleEventCnt)
	}
	second, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: store.MaxPageLimit, LimitSpecified: true, Offset: store.MaxPageLimit})
	if err != nil {
		t.Fatalf("list second scoped audit page: %v", err)
	}
	if second.Total != visibleEventCnt || second.Limit != store.MaxPageLimit || second.Offset != store.MaxPageLimit || len(second.Items) != visibleEventCnt-store.MaxPageLimit {
		t.Fatalf("second scoped audit page = %#v, want final %d visible events", second, visibleEventCnt-store.MaxPageLimit)
	}
	allVisible := append(append([]domain.AuditEvent{}, first.Items...), second.Items...)
	assertAuditVisibility(t, allVisible, visibleEdges, hiddenEdges)
	if allVisible[len(allVisible)-1].ID != "audit_allowed_bulk_000" {
		t.Fatalf("oldest authorized event = %q, want audit_allowed_bulk_000", allVisible[len(allVisible)-1].ID)
	}

	explicitZero, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true, LimitSpecified: true})
	if err != nil || explicitZero.Total != visibleEventCnt || explicitZero.Limit != 0 || explicitZero.Offset != 0 || len(explicitZero.Items) != 0 {
		t.Fatalf("explicit-zero scoped audit page = %#v, %v", explicitZero, err)
	}
	defaultLimit, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true})
	if err != nil || defaultLimit.Total != visibleEventCnt || defaultLimit.Limit != store.MaxPageLimit || len(defaultLimit.Items) != store.MaxPageLimit {
		t.Fatalf("default scoped audit page = %#v, %v", defaultLimit, err)
	}
	bounded, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: store.MaxPageLimit + 1, LimitSpecified: true})
	if err != nil || bounded.Total != visibleEventCnt || bounded.Limit != store.MaxPageLimit || len(bounded.Items) != store.MaxPageLimit {
		t.Fatalf("bounded scoped audit page = %#v, %v", bounded, err)
	}
	beyond, err := repository.ListAuditEventsPageForScope(ctx, scope, store.Page{Enabled: true, Limit: 1, LimitSpecified: true, Offset: store.MaxPageOffset + 1})
	if err != nil || beyond.Total != visibleEventCnt || beyond.Offset != visibleEventCnt || len(beyond.Items) != 0 {
		t.Fatalf("bounded-offset scoped audit page = %#v, %v", beyond, err)
	}
	empty, err := repository.ListAuditEventsPageForScope(ctx, store.AuditAuthorizationScope{}, store.Page{Enabled: true, Limit: 10, LimitSpecified: true})
	if err != nil || empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("empty audit authorization scope = %#v, %v", empty, err)
	}
	actorOnly, err := repository.ListAuditEventsPageForScope(ctx, store.AuditAuthorizationScope{ActorID: viewerID}, store.Page{Enabled: true, Limit: 10, LimitSpecified: true})
	if err != nil || actorOnly.Total != 1 || len(actorOnly.Items) != 1 || actorOnly.Items[0].ID != "audit_actor_own" {
		t.Fatalf("actor-only audit authorization scope = %#v, %v", actorOnly, err)
	}

	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: repository, Sessions: repository, APITokens: repository, ServiceAccounts: repository, OIDC: repository, Hosting: repository, Projects: repository, Members: repository, Templates: repository, Sources: repository, Runs: repository, Runners: repository, Approvals: repository, Audit: repository, Deployments: repository, Observability: repository, ObservationWriter: repository, ObservationReader: repository, Retention: repository})
	if err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(ctx, auth.Principal{ID: viewerID, Roles: []string{domain.RoleViewer}, Provider: domain.PrincipalLocal})
	servicePage, err := service.ListAuditEventsPage(viewer, store.Page{Enabled: true, Limit: 1, LimitSpecified: true, Offset: visibleEventCnt - 1})
	if err != nil || servicePage.Total != visibleEventCnt || len(servicePage.Items) != 1 || servicePage.Items[0].ID != "audit_allowed_bulk_000" {
		t.Fatalf("membership-scoped service audit page = %#v, %v", servicePage, err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_admin", Roles: []string{domain.RoleSystemAdmin}, Provider: domain.PrincipalLocal})
	adminPage, err := service.ListAuditEventsPage(admin, store.Page{Enabled: true, Limit: 1, LimitSpecified: true})
	if err != nil || adminPage.Total != 220 || len(adminPage.Items) != 1 {
		t.Fatalf("unscoped administrator audit page = %#v, %v", adminPage, err)
	}
}

func createAuditContractEvent(t *testing.T, repository store.AuditRepository, event domain.AuditEvent) {
	t.Helper()
	if err := repository.CreateAuditEvent(t.Context(), event); err != nil {
		t.Fatalf("create audit event %s: %v", event.ID, err)
	}
}

func assertAuditVisibility(t *testing.T, events []domain.AuditEvent, visible, hidden []domain.AuditEvent) {
	t.Helper()
	byID := make(map[string]struct{}, len(events))
	for _, event := range events {
		byID[event.ID] = struct{}{}
		if event.Action == "denied.newer" {
			t.Fatalf("scoped page leaked newer denied event %s", event.ID)
		}
	}
	for _, event := range visible {
		if _, ok := byID[event.ID]; !ok {
			t.Errorf("scoped page omitted visible edge event %s", event.ID)
		}
	}
	for _, event := range hidden {
		if _, ok := byID[event.ID]; ok {
			t.Errorf("scoped page leaked hidden edge event %s", event.ID)
		}
	}
}
