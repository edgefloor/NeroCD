package api

import "net/http"

var publicRoutes = []PublicRoute{
	{Method: http.MethodGet, Path: "/api/v1/health"},
	{Method: http.MethodGet, Path: "/api/v1/ready"},
	{Method: http.MethodGet, Path: "/api/v1/bootstrap-status"},
	{Method: http.MethodGet, Path: "/api/v1/operations/status"},
	{Method: http.MethodGet, Path: "/api/v1/run-log-retention"},
	{Method: http.MethodPut, Path: "/api/v1/run-log-retention"},
	{Method: http.MethodPost, Path: "/api/v1/run-log-retention/preview"},
	{Method: http.MethodPost, Path: "/api/v1/run-log-retention/execute"},
	{Method: http.MethodGet, Path: "/api/v1/me"},
	{Method: http.MethodPost, Path: "/api/v1/sessions"},
	{Method: http.MethodGet, Path: "/api/v1/sessions"},
	{Method: http.MethodDelete, Path: "/api/v1/sessions"},
	{Method: http.MethodPost, Path: "/api/v1/sessions/revoke"},
	{Method: http.MethodPost, Path: "/api/v1/browser-sessions"},
	{Method: http.MethodDelete, Path: "/api/v1/browser-sessions"},
	{Method: http.MethodPost, Path: "/api/v1/api-tokens"},
	{Method: http.MethodPost, Path: "/api/v1/api-tokens/revoke"},
	{Method: http.MethodGet, Path: "/api/v1/capabilities"},
	{Method: http.MethodGet, Path: "/api/v1/projects"},
	{Method: http.MethodPost, Path: "/api/v1/projects"},
	{Method: http.MethodPatch, Path: "/api/v1/projects"},
	{Method: http.MethodPost, Path: "/api/v1/projects/archive"},
	{Method: http.MethodGet, Path: "/api/v1/project-members"},
	{Method: http.MethodPost, Path: "/api/v1/project-members"},
	{Method: http.MethodGet, Path: "/api/v1/project-role"},
	{Method: http.MethodGet, Path: "/api/v1/repositories"},
	{Method: http.MethodPost, Path: "/api/v1/repositories"},
	{Method: http.MethodPut, Path: "/api/v1/repositories/{id}/policy"},
	{Method: http.MethodGet, Path: "/api/v1/access-keys"},
	{Method: http.MethodPost, Path: "/api/v1/access-keys"},
	{Method: http.MethodGet, Path: "/api/v1/inventories"},
	{Method: http.MethodPost, Path: "/api/v1/inventories"},
	{Method: http.MethodGet, Path: "/api/v1/templates"},
	{Method: http.MethodPost, Path: "/api/v1/templates"},
	{Method: http.MethodPatch, Path: "/api/v1/templates"},
	{Method: http.MethodGet, Path: "/api/v1/runs"},
	{Method: http.MethodPost, Path: "/api/v1/runs"},
	{Method: http.MethodPost, Path: "/api/v1/runs/approve"},
	{Method: http.MethodPost, Path: "/api/v1/runs/reject"},
	{Method: http.MethodPost, Path: "/api/v1/runs/cancel"},
	{Method: http.MethodGet, Path: "/api/v1/runners"},
	{Method: http.MethodGet, Path: "/api/v1/runners/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/register"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments/revoke"},
	{Method: http.MethodPost, Path: "/api/v1/runner-enrollments/consume"},
	{Method: http.MethodPost, Path: "/api/v1/runners/rotate-token"},
	{Method: http.MethodPost, Path: "/api/v1/runners/revoke-token"},
	{Method: http.MethodPost, Path: "/api/v1/runners/heartbeat"},
	{Method: http.MethodPost, Path: "/api/v1/runners/telemetry"},
	{Method: http.MethodPost, Path: "/api/v1/runners/claim"},
	{Method: http.MethodPost, Path: "/api/v1/runners/renew"},
	{Method: http.MethodGet, Path: "/api/v1/runners/lease"},
	{Method: http.MethodPost, Path: "/api/v1/runners/logs"},
	{Method: http.MethodPost, Path: "/api/v1/runners/events/batch"},
	{Method: http.MethodPost, Path: "/api/v1/runners/secrets/access"},
	{Method: http.MethodPost, Path: "/api/v1/runners/artifacts"},
	{Method: http.MethodPost, Path: "/api/v1/runners/complete"},
	{Method: http.MethodPost, Path: "/api/v1/runners/deployments/transition"},
	{Method: http.MethodGet, Path: "/api/v1/runners/deployments/plan"},
	{Method: http.MethodGet, Path: "/api/v1/runners/deployments/status"},
	{Method: http.MethodPost, Path: "/api/v1/runners/deployments/provenance"},
	{Method: http.MethodPost, Path: "/api/v1/runners/deployments/fail-and-rollback"},
	{Method: http.MethodGet, Path: "/api/v1/run-logs"},
	{Method: http.MethodGet, Path: "/api/v1/artifacts"},
	{Method: http.MethodGet, Path: "/api/v1/runner-primitive-plan"},
	{Method: http.MethodGet, Path: "/api/v1/approvals"},
	{Method: http.MethodGet, Path: "/api/v1/audit-events"},
	{Method: http.MethodGet, Path: "/api/v1/services"},
	{Method: http.MethodPost, Path: "/api/v1/services"},
	{Method: http.MethodGet, Path: "/api/v1/environments"},
	{Method: http.MethodPost, Path: "/api/v1/environments"},
	{Method: http.MethodGet, Path: "/api/v1/revisions"},
	{Method: http.MethodPost, Path: "/api/v1/revisions"},
	{Method: http.MethodGet, Path: "/api/v1/deployments"},
	{Method: http.MethodPost, Path: "/api/v1/deployments"},
	{Method: http.MethodGet, Path: "/api/v1/deployments/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/deployments/confirm"},
	{Method: http.MethodPost, Path: "/api/v1/deployments/cancel"},
	{Method: http.MethodPost, Path: "/api/v1/deployments/fail-preassignment"},
}

func PublicRoutes() []PublicRoute {
	routes := make([]PublicRoute, len(publicRoutes))
	copy(routes, publicRoutes)
	return routes
}

func (s *Server) handlerFor(path string) http.HandlerFunc {
	switch path {
	case "/api/v1/health":
		return s.health
	case "/api/v1/ready":
		return s.ready
	case "/api/v1/bootstrap-status":
		return s.bootstrapStatus
	case "/api/v1/operations/status":
		return s.operationsStatus
	case "/api/v1/run-log-retention":
		return s.runLogRetentionPolicy
	case "/api/v1/run-log-retention/preview":
		return s.runLogRetentionPreview
	case "/api/v1/run-log-retention/execute":
		return s.runLogRetentionExecute
	case "/api/v1/me":
		return s.me
	case "/api/v1/sessions":
		return s.sessionsHandler
	case "/api/v1/sessions/revoke":
		return s.revokeSessionByID
	case "/api/v1/browser-sessions":
		return s.browserSessionsHandler
	case "/api/v1/api-tokens":
		return s.createAPIToken
	case "/api/v1/api-tokens/revoke":
		return s.revokeAPIToken
	case "/api/v1/capabilities":
		return s.capabilities
	case "/api/v1/projects":
		return s.projectsHandler
	case "/api/v1/projects/archive":
		return s.archiveProject
	case "/api/v1/project-members":
		return s.projectMembersHandler
	case "/api/v1/project-role":
		return s.projectRole
	case "/api/v1/repositories":
		return s.repositoriesHandler
	case "/api/v1/repositories/{id}/policy":
		return s.repositoryPolicyHandler
	case "/api/v1/access-keys":
		return s.accessKeysHandler
	case "/api/v1/inventories":
		return s.inventoriesHandler
	case "/api/v1/templates":
		return s.templatesHandler
	case "/api/v1/runs":
		return s.runsHandler
	case "/api/v1/runs/approve":
		return s.approveRun
	case "/api/v1/runs/reject":
		return s.rejectRun
	case "/api/v1/runs/cancel":
		return s.cancelRun
	case "/api/v1/deployments/cancel":
		return s.cancelDeployment
	case "/api/v1/runners":
		return s.runnersHandler
	case "/api/v1/runners/{id}":
		return s.runnerByID
	case "/api/v1/runners/register":
		return s.registerRunner
	case "/api/v1/runner-enrollments":
		return s.createRunnerEnrollment
	case "/api/v1/runner-enrollments/revoke":
		return s.revokeRunnerEnrollment
	case "/api/v1/runner-enrollments/consume":
		return s.consumeRunnerEnrollment
	case "/api/v1/runners/rotate-token":
		return s.rotateRunnerToken
	case "/api/v1/runners/revoke-token":
		return s.revokeRunnerToken
	case "/api/v1/runners/heartbeat":
		return s.heartbeatRunner
	case "/api/v1/runners/telemetry":
		return s.runnerOperationalTelemetry
	case "/api/v1/runners/claim":
		return s.claimRun
	case "/api/v1/runners/renew":
		return s.renewLease
	case "/api/v1/runners/lease":
		return s.runnerLease
	case "/api/v1/runners/logs":
		return s.appendRunLog
	case "/api/v1/runners/events/batch":
		return s.appendRunEvents
	case "/api/v1/runners/secrets/access":
		return s.authorizeSecretAccess
	case "/api/v1/runners/artifacts":
		return s.createArtifact
	case "/api/v1/runners/complete":
		return s.completeLease
	case "/api/v1/runners/deployments/transition":
		return s.transitionDeploymentAttempt
	case "/api/v1/runners/deployments/plan":
		return s.runnerDeploymentPlan
	case "/api/v1/runners/deployments/status":
		return s.runnerDeploymentStatus
	case "/api/v1/runners/deployments/provenance":
		return s.resolveDeploymentProvenance
	case "/api/v1/runners/deployments/fail-and-rollback":
		return s.failDeploymentAndCreateRollback
	case "/api/v1/run-logs":
		return s.runLogs
	case "/api/v1/artifacts":
		return s.artifacts
	case "/api/v1/runner-primitive-plan":
		return s.runnerPrimitivePlan
	case "/api/v1/approvals":
		return s.approvalsHandler
	case "/api/v1/audit-events":
		return s.auditEvents
	case "/api/v1/services":
		return s.servicesHandler
	case "/api/v1/environments":
		return s.environmentsHandler
	case "/api/v1/revisions":
		return s.revisionsHandler
	case "/api/v1/deployments":
		return s.deploymentsHandler
	case "/api/v1/deployments/{id}":
		return s.deploymentByID
	case "/api/v1/deployments/confirm":
		return s.confirmDeployment
	case "/api/v1/deployments/fail-preassignment":
		return s.failPreAssignmentDeployment
	default:
		panic("missing handler for public route " + path)
	}
}
