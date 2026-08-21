package store

import (
	"time"

	"nerocd/internal/domain"
)

// NewFixtureMemoryStore is parameterized test/demo data. It contains no
// credential, email, or hash and production startup never selects it.
func NewFixtureMemoryStore(adminEmail, viewerEmail, adminHash, viewerHash string) *MemoryStore {
	now := time.Now().UTC()
	finishedAt := now.Add(-20 * time.Minute)
	tplPlan := "tpl_plan"
	tplRotate := "tpl_rotate"
	return &MemoryStore{
		users: []domain.User{
			{ID: "usr_bootstrap", Email: adminEmail, Name: "Bootstrap Admin", Status: domain.UserActive, GlobalRole: domain.RoleSystemAdmin, PasswordHash: adminHash, CreatedAt: now},
			{ID: "usr_viewer", Email: viewerEmail, Name: "Security Viewer", Status: domain.UserActive, GlobalRole: "user", PasswordHash: viewerHash, CreatedAt: now},
		},
		projects: []domain.Project{
			{ID: "proj_platform", Name: "Platform Automation", Description: "Shared infrastructure runbooks and deployments.", CreatedAt: now},
			{ID: "proj_security", Name: "Security Operations", Description: "Audited response and compliance automation.", CreatedAt: now},
		},
		templates: []domain.TaskTemplate{
			{ID: "tpl_patch", ProjectID: "proj_platform", Name: "Patch Linux Fleet", Kind: "ansible", RunSpec: domain.RunSpec{Type: "ansible", Inputs: map[string]any{"playbook": "patch.yml"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "ansible"}, Process: &domain.ProcessSpec{Command: []string{"ansible-playbook", "patch.yml"}, TimeoutSeconds: 1800}, Artifacts: []domain.ArtifactSpec{{Name: "patch-report", Path: "reports/patch.json"}}, Secrets: []domain.SecretBinding{{Name: "ansible-vault", Provider: "database", Reference: "sec_ansible_vault", Target: "env:ANSIBLE_VAULT_PASSWORD"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout runbooks", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "patch", Name: "Patch fleet", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: "ansible", Inputs: map[string]any{"playbook": "patch.yml"}}}}}, RunnerTags: []string{"linux", "prod"}, RequiresAck: true},
			{ID: "tpl_plan", ProjectID: "proj_platform", Name: "Terraform Plan", Kind: "opentofu", RunSpec: domain.RunSpec{Type: "opentofu", Inputs: map[string]any{"command": "plan"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "tofu"}, Process: &domain.ProcessSpec{Command: []string{"tofu", "plan", "-out=tfplan"}, TimeoutSeconds: 1200}, Artifacts: []domain.ArtifactSpec{{Name: "tfplan", Path: "tfplan", Required: true}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout IaC", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "plan", Name: "OpenTofu plan", DependsOn: []string{"checkout"}, RunSpec: domain.RunSpec{Type: "opentofu", Inputs: map[string]any{"command": "plan"}}}}}, RunnerTags: []string{"tofu"}, RequiresAck: false},
			{ID: "tpl_rotate", ProjectID: "proj_security", Name: "Rotate Service Tokens", Kind: "shell", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "./rotate-tokens.sh"}, Repository: &domain.RepositoryRef{ID: "repo_security_runbooks", Ref: "main", Path: "tokens"}, Process: &domain.ProcessSpec{Command: []string{"./rotate-tokens.sh"}, TimeoutSeconds: 600}, Secrets: []domain.SecretBinding{{Name: "token-admin", Provider: "database", Reference: "sec_token_admin", Target: "env:TOKEN_ADMIN"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout security runbooks", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "git checkout"}}}, {ID: "rotate", Name: "Rotate tokens", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "./rotate-tokens.sh"}}}}}, RunnerTags: []string{"secure"}, RequiresAck: true},
		},
		repositories: []domain.Repository{
			{ID: "repo_platform_runbooks", ProjectID: "proj_platform", Name: "Platform Runbooks", URL: "https://example.local/platform/runbooks.git", Provider: domain.ProviderGit, DefaultRef: "main", CreatedAt: now},
			{ID: "repo_security_runbooks", ProjectID: "proj_security", Name: "Security Runbooks", URL: "https://example.local/security/runbooks.git", Provider: domain.ProviderGit, DefaultRef: "main", CreatedAt: now},
		},
		accessKeys: []domain.AccessKey{
			{ID: "key_ansible_vault", ProjectID: "proj_platform", Name: "Ansible Vault", Kind: domain.AccessKeyPassword, Fingerprint: "sha256:seed-ansible-vault", CreatedAt: now},
			{ID: "key_token_admin", ProjectID: "proj_security", Name: "Token Admin", Kind: domain.AccessKeyPassword, Fingerprint: "sha256:seed-token-admin", CreatedAt: now},
		},
		inventories: []domain.Inventory{
			{ID: "inv_platform_prod", ProjectID: "proj_platform", Name: "Platform Production", Kind: domain.InventoryStatic, Source: "inventories/prod.ini", CreatedAt: now},
			{ID: "inv_security_response", ProjectID: "proj_security", Name: "Security Response", Kind: domain.InventoryStatic, Source: "inventories/response.ini", CreatedAt: now},
		},
		projectMembers: []domain.ProjectMember{
			{ID: "pm_proj_platform_usr_bootstrap", ProjectID: "proj_platform", UserID: "usr_bootstrap", Email: adminEmail, Name: "Bootstrap Admin", Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now},
			{ID: "pm_proj_security_usr_bootstrap", ProjectID: "proj_security", UserID: "usr_bootstrap", Email: adminEmail, Name: "Bootstrap Admin", Role: domain.RoleOwner, CreatedAt: now, UpdatedAt: now},
			{ID: "pm_proj_security_usr_viewer", ProjectID: "proj_security", UserID: "usr_viewer", Email: viewerEmail, Name: "Security Viewer", Role: domain.RoleViewer, CreatedAt: now, UpdatedAt: now},
		},
		runs: []domain.TaskRun{
			{ID: "run_001", ProjectID: "proj_platform", TemplateID: &tplPlan, RunSpec: domain.RunSpec{Type: domain.RunTypeOpenTofu, Inputs: map[string]any{"command": "plan"}, Repository: &domain.RepositoryRef{ID: "repo_platform_runbooks", Ref: "main", Path: "tofu"}, Process: &domain.ProcessSpec{Command: []string{"tofu", "plan", "-out=tfplan"}, TimeoutSeconds: 1200}, Artifacts: []domain.ArtifactSpec{{Name: "tfplan", Path: "tfplan", Required: true}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout IaC", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "git checkout"}}}, {ID: "plan", Name: "OpenTofu plan", DependsOn: []string{"checkout"}, RunSpec: domain.RunSpec{Type: domain.RunTypeOpenTofu, Inputs: map[string]any{"command": "plan"}}}}}, RunnerTags: []string{"tofu"}, Status: domain.RunSucceeded, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-22 * time.Minute), FinishedAt: &finishedAt},
			{ID: "run_002", ProjectID: "proj_security", TemplateID: &tplRotate, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "./rotate-tokens.sh"}, Repository: &domain.RepositoryRef{ID: "repo_security_runbooks", Ref: "main", Path: "tokens"}, Process: &domain.ProcessSpec{Command: []string{"./rotate-tokens.sh"}, TimeoutSeconds: 600}, Secrets: []domain.SecretBinding{{Name: "token-admin", Provider: "database", Reference: "sec_token_admin", Target: "env:TOKEN_ADMIN"}}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "checkout", Name: "Checkout security runbooks", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "git checkout"}}}, {ID: "rotate", Name: "Rotate tokens", DependsOn: []string{"checkout"}, RequiresAck: true, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "./rotate-tokens.sh"}}}}}, RunnerTags: []string{"secure"}, Status: domain.RunWaitingApproval, RequestedBy: "usr_bootstrap", StartedAt: now.Add(-5 * time.Minute)},
		},
		approvals: []domain.Approval{
			{ID: "apr_002", RunID: "run_002", Status: domain.ApprovalPending, RequestedBy: "usr_bootstrap", CreatedAt: now.Add(-5 * time.Minute)},
		},
		tokenHashBySessionID:  map[string]string{},
		claimCursors:          map[string]memoryClaimCursor{},
		claimOrderByRun:       map[string]time.Time{},
		policyConfigurations:  map[string]memoryPolicyConfiguration{},
		deploymentTransitions: map[string]domain.DeploymentTransitionRequest{},
		deploymentCancels:     map[string]domain.DeploymentCancelRequest{},
		provenanceReplays:     map[string]memoryProvenanceReplay{},
		runnerObservations:    map[string]memoryRunnerObservation{},
		serviceByID:           map[string]int{},
		environmentByID:       map[string]int{},
		deploymentByID:        map[string]int{},
		nextAttemptByRun:      map[string]int{},
		retentionReceipts:     map[string]domain.RunLogRetentionExecution{},
		retentionPolicy:       domain.RunLogRetentionPolicy{KeepDays: 30, BatchSize: 1000, Version: 1},
		logs: []domain.RunLog{
			{ID: "log_001", RunID: "run_001", Sequence: 1, Stream: domain.LogStdout, Message: "Initializing OpenTofu working directory", CreatedAt: now.Add(-22 * time.Minute)},
			{ID: "log_002", RunID: "run_001", Sequence: 2, Stream: domain.LogStdout, Message: "Plan completed with no destructive changes", CreatedAt: now.Add(-21 * time.Minute)},
			{ID: "log_003", RunID: "run_002", Sequence: 1, Stream: domain.LogSystem, Message: "Run is waiting for an authorized approval", CreatedAt: now.Add(-5 * time.Minute)},
		},
	}
}
