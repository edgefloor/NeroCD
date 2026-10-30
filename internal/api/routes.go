package api

import "net/http"

var publicRoutes = []PublicRoute{
	{Method: http.MethodGet, Path: "/api/v1/database-backups/{id}/archive-options"},
	{Method: http.MethodGet, Path: "/api/v1/database-backups/{id}/archive"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-download/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-download/stream"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-import-upload-stages"},
	{Method: http.MethodPut, Path: "/api/v1/database-import-stages/{id}/content"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import-stage/upload-stream"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import-stage/cleanup"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import-stage/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import-stage/mutation"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import/mutation"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-import-stage/credentials"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-import-stages"},
	{Method: http.MethodGet, Path: "/api/v1/database-import-stages/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-imports"},
	{Method: http.MethodGet, Path: "/api/v1/database-imports/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/storage-backups/{id}/archive-options"},
	{Method: http.MethodGet, Path: "/api/v1/storage-backups/{id}/archive"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-download/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-download/stream"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-schedules"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backup-schedules"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/storage-backup-schedules/{schedule_id}"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-schedules/{schedule_id}/occurrences"},

	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/notification-channels"},
	{Method: http.MethodPost, Path: "/api/v1/projects/{project_id}/notification-channels"},
	{Method: http.MethodPut, Path: "/api/v1/projects/{project_id}/notification-channels/{channel_id}"},
	{Method: http.MethodPost, Path: "/api/v1/projects/{project_id}/notification-channels/{channel_id}/tests"},
	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/notification-deliveries"},
	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/notification-deliveries/{delivery_id}"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backups/{backup_id}/deletion-previews"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backups/{backup_id}/deletions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backups/{backup_id}/deletions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backups/{backup_id}/deletions/{execution_id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-retention/retirement"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-retention/credentials"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-retention"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/storage-backup-retention"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backup-retention/previews"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backup-retention/executions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-retention/executions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-retention/executions/{execution_id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-retention/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-retention/mutation"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-retention/retirement"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-retention/credentials"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-retention"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/database-backup-retention"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-backup-retention/previews"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-backup-retention/executions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-retention/executions"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-retention/executions/{execution_id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-retention/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-retention/mutation"},

	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/database-backup-targets"},
	{Method: http.MethodPost, Path: "/api/v1/projects/{project_id}/database-backup-targets"},
	{Method: http.MethodPut, Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}"},
	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/validations"},
	{Method: http.MethodPost, Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/validations"},
	{Method: http.MethodGet, Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations"},
	{Method: http.MethodPost, Path: "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations"},
	{Method: http.MethodGet, Path: "/api/v1/storage-backups/{id}/transfers"},
	{Method: http.MethodPost, Path: "/api/v1/storage-backups/{id}/transfers/retry"},
	{Method: http.MethodGet, Path: "/api/v1/storage-backup-transfers/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-transfer/credentials"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-transfer/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-backup-transfer/mutation"},
	{Method: http.MethodGet, Path: "/api/v1/database-backups/{id}/transfers"},
	{Method: http.MethodPost, Path: "/api/v1/database-backups/{id}/transfers/retry"},
	{Method: http.MethodGet, Path: "/api/v1/database-backup-transfers/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-transfer/credentials"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-transfer/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup-transfer/mutation"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-schedules"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-backup-schedules"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/database-backup-schedules/{schedule_id}"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-schedules/{schedule_id}/occurrences"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backups"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-backup-components"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-backups"},
	{Method: http.MethodGet, Path: "/api/v1/database-backups/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/database-backups/{id}/restores"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-backup/mutation"},

	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-connections"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-connection"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database-connections/options"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-connection/deployments"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database-connection/recovery"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database/maintenance"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/yield"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/storage/writer"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/storage/authorize"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/storage/proxy-plan"},

	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/stage"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/retirement-plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/retirement"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/authorize"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-binding/state"},
	{Method: http.MethodPost, Path: "/api/v1/runners/database-network-captures/claim"},
	{Method: http.MethodPost, Path: "/api/v1/runners/database-network-captures/claim-next"},
	{Method: http.MethodPost, Path: "/api/v1/runners/database-network-captures/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/database-network-captures/origin"},

	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backup-volumes"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-backups"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-backups"},
	{Method: http.MethodGet, Path: "/api/v1/storage-backups/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/storage-backups/{id}/restores"},
	{Method: http.MethodGet, Path: "/api/v1/storage-restores/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/hosting-operations/{id}/storage-archive-recovery"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-archive/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-archive/prepared"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-archive/apply-begin"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-archive/progress"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-archive/complete"},

	{Method: http.MethodPost, Path: "/api/v1/databases"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/database/deployments"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/database"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage"},
	{Method: http.MethodGet, Path: "/api/v1/server-bootstrap-options"},
	{Method: http.MethodGet, Path: "/api/v1/server-bootstraps"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps"},
	{Method: http.MethodGet, Path: "/api/v1/server-bootstraps/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/probe"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/review"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/install"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/inspect"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/register"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/grant"},
	{Method: http.MethodPost, Path: "/api/v1/server-bootstraps/{id}/cancel"},
	{Method: http.MethodPost, Path: "/api/v1/runner/server-readiness/challenge"},
	{Method: http.MethodPost, Path: "/api/v1/runner/server-readiness/report"},
	{Method: http.MethodGet, Path: "/api/v1/servers/{id}/readiness"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/github-push-policy"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/github-push-policy"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/github-push-policy/verify"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/github-push-policy/enabled"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/github-push-policy/retry"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/github-push-policy/deliveries"},
	{Method: http.MethodPost, Path: "/api/v1/hosting/github-push/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/runtime-log-targets"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/stack-lifecycle"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/stack-stop"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/runtime-log-sessions"},
	{Method: http.MethodPost, Path: "/api/v1/runtime-log-sessions/{id}/renew"},
	{Method: http.MethodDelete, Path: "/api/v1/runtime-log-sessions/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/runtime-log-sessions/{id}/logs"},
	{Method: http.MethodPost, Path: "/api/v1/runners/runtime-log-sessions/claim"},
	{Method: http.MethodPost, Path: "/api/v1/runners/runtime-log-sessions/events"},

	{Method: http.MethodPost, Path: "/api/v1/runners/hosting-observations/targets"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting-observations/report"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/runtime-status"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/runtime-environment"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/registry-credential"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/database-credentials"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/runtime-configuration"},
	{Method: http.MethodPut, Path: "/api/v1/resources/{id}/runtime-configuration"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/source-credential"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/registry-credential"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/secrets"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/source-grants"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/source-grants"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/source-grants/{grant_id}/revoke"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/events"},
	{Method: http.MethodGet, Path: "/api/v1/hosting-builds/{id}/logs"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/builds"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/builds"},
	{Method: http.MethodGet, Path: "/api/v1/hosting-builds/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/hosting-builds/{id}/cancel"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/status"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/source-begin"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/source-commit"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/prepare"},
	{Method: http.MethodPost, Path: "/api/v1/runners/builds/complete"},
	{Method: http.MethodGet, Path: "/api/v1/servers/{id}/proxy"},
	{Method: http.MethodPost, Path: "/api/v1/servers/{id}/proxy"},
	{Method: http.MethodPost, Path: "/api/v1/servers/{id}/proxy/disable"},
	{Method: http.MethodGet, Path: "/api/v1/servers/{id}/proxy/hostname-grants"},
	{Method: http.MethodPost, Path: "/api/v1/servers/{id}/proxy/hostname-grants"},
	{Method: http.MethodPost, Path: "/api/v1/servers/{id}/proxy/hostname-grants/{grant_id}/revoke"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/routes"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/routes"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/route-switches"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/route-switches"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-deployment-options"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/storage-route-continuity/{operation_id}"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/storage-route-continuity/{operation_id}/recover"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/route-switches/{operation_id}"},
	{Method: http.MethodGet, Path: "/api/v1/routes/{id}"},
	{Method: http.MethodPatch, Path: "/api/v1/routes/{id}"},
	{Method: http.MethodDelete, Path: "/api/v1/routes/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/proxy-operations/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/proxy-operations/{id}/cancel"},
	{Method: http.MethodGet, Path: "/api/v1/proxy-operations/{id}/recovery"},
	{Method: http.MethodPost, Path: "/api/v1/proxy-operations/{id}/recovery-plans"},
	{Method: http.MethodPost, Path: "/api/v1/proxy-operations/{id}/recovery-plans/{plan_id}/apply"},
	{Method: http.MethodPost, Path: "/api/v1/runners/proxy/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/proxy/prepared"},
	{Method: http.MethodPost, Path: "/api/v1/runners/proxy/apply-begin"},
	{Method: http.MethodPost, Path: "/api/v1/runners/proxy/complete"},
	{Method: http.MethodPost, Path: "/api/v1/runners/proxy/status"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/retained-releases"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/retained-releases/{operation_id}/cleanup"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/deployments"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/deployments"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/deployments/from-build"},
	{Method: http.MethodGet, Path: "/api/v1/hosting-operations/{id}"},
	{Method: http.MethodPost, Path: "/api/v1/hosting-operations/{id}/cancel"},
	{Method: http.MethodPost, Path: "/api/v1/hosting-operations/{id}/reconcile"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/plan"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/prepared"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/apply-begin"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/stack-volumes"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-create-begin"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/storage-materialized"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/complete"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/status"},
	{Method: http.MethodPost, Path: "/api/v1/runners/hosting/reconciliation"},

	{Method: http.MethodGet, Path: "/api/v1/runners/identity"},
	{Method: http.MethodGet, Path: "/api/v1/servers"},
	{Method: http.MethodPost, Path: "/api/v1/servers"},
	{Method: http.MethodGet, Path: "/api/v1/servers/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/destinations"},
	{Method: http.MethodGet, Path: "/api/v1/destinations/{id}/project-grants"},
	{Method: http.MethodPost, Path: "/api/v1/destinations/{id}/project-grants"},
	{Method: http.MethodPost, Path: "/api/v1/destinations/{id}/project-grants/revoke"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/binding"},
	{Method: http.MethodPost, Path: "/api/v1/resources/{id}/binding"},
	{Method: http.MethodGet, Path: "/api/v1/managed-secrets"},
	{Method: http.MethodPost, Path: "/api/v1/managed-secrets"},
	{Method: http.MethodGet, Path: "/api/v1/managed-secrets/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/managed-secrets/{id}/versions"},
	{Method: http.MethodPost, Path: "/api/v1/managed-secrets/{id}/versions"},
	{Method: http.MethodPost, Path: "/api/v1/managed-secrets/{id}/revoke"},
	{Method: http.MethodPost, Path: "/api/v1/managed-secrets/{id}/reveal"},

	{Method: http.MethodGet, Path: "/api/v1/project-environments"},
	{Method: http.MethodPost, Path: "/api/v1/project-environments"},
	{Method: http.MethodGet, Path: "/api/v1/hosting-catalog"},
	{Method: http.MethodGet, Path: "/api/v1/hosting-catalog/{id}/destination-options"},
	{Method: http.MethodPost, Path: "/api/v1/hosting-catalog/installations"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}/catalog-installation"},
	{Method: http.MethodGet, Path: "/api/v1/resources"},
	{Method: http.MethodPost, Path: "/api/v1/resources"},
	{Method: http.MethodGet, Path: "/api/v1/resources/{id}"},
	{Method: http.MethodPatch, Path: "/api/v1/resources/{id}"},
	{Method: http.MethodGet, Path: "/api/v1/health"},
	{Method: http.MethodGet, Path: "/api/v1/ready"},
	{Method: http.MethodGet, Path: "/api/v1/bootstrap-status"},
	{Method: http.MethodGet, Path: "/api/v1/oidc/status"},
	{Method: http.MethodGet, Path: "/api/v1/oidc/login"},
	{Method: http.MethodGet, Path: "/api/v1/oidc/callback"},
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
	{Method: http.MethodGet, Path: "/api/v1/service-accounts"},
	{Method: http.MethodPost, Path: "/api/v1/service-accounts"},
	{Method: http.MethodPatch, Path: "/api/v1/service-accounts"},
	{Method: http.MethodPost, Path: "/api/v1/service-accounts/rotate"},
	{Method: http.MethodPost, Path: "/api/v1/service-accounts/revoke"},
	{Method: http.MethodGet, Path: "/api/v1/users"},
	{Method: http.MethodPost, Path: "/api/v1/users/offboard"},
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
	{Method: http.MethodGet, Path: "/api/v1/runs/{id}"},
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

// PublicRoutes returns a copy of the routes exposed by the public API.
func PublicRoutes() []PublicRoute {
	routes := make([]PublicRoute, len(publicRoutes))
	copy(routes, publicRoutes)
	return routes
}

func (s *Server) handlerFor(path string) http.HandlerFunc {
	switch path {
	case "/api/v1/resources/{id}/storage-backups/{backup_id}/deletion-previews":
		return s.previewStorageBackupDeletion
	case "/api/v1/resources/{id}/storage-backups/{backup_id}/deletions":
		return s.executeStorageBackupDeletion
	case "/api/v1/resources/{id}/storage-backups/{backup_id}/deletions/{execution_id}":
		return s.getStorageBackupDeletion
	case "/api/v1/runners/hosting/storage-backup-retention/retirement":
		return s.runnerStorageBackupRetentionRetirement
	case "/api/v1/runners/hosting/storage-backup-retention/credentials":
		return s.runnerBackupRemoteCredentials
	case "/api/v1/resources/{id}/storage-backup-retention":
		return s.hostingStorageBackupRetention
	case "/api/v1/resources/{id}/storage-backup-retention/previews":
		return s.previewStorageBackupRetention
	case "/api/v1/resources/{id}/storage-backup-retention/executions":
		return s.executeStorageBackupRetention
	case "/api/v1/resources/{id}/storage-backup-retention/executions/{execution_id}":
		return s.getStorageBackupRetentionExecution
	case "/api/v1/runners/hosting/storage-backup-retention/plan":
		return s.runnerStorageBackupRetentionPlan
	case "/api/v1/runners/hosting/storage-backup-retention/mutation":
		return s.runnerStorageBackupRetentionMutation
	case "/api/v1/runners/hosting/database-backup-retention/retirement":
		return s.runnerDatabaseBackupRetentionRetirement
	case "/api/v1/runners/hosting/database-backup-retention/credentials":
		return s.runnerBackupRemoteCredentials
	case "/api/v1/resources/{id}/database-backup-retention":
		return s.hostingDatabaseBackupRetention
	case "/api/v1/resources/{id}/database-backup-retention/previews":
		return s.previewDatabaseBackupRetention
	case "/api/v1/resources/{id}/database-backup-retention/executions":
		return s.executeDatabaseBackupRetention
	case "/api/v1/resources/{id}/database-backup-retention/executions/{execution_id}":
		return s.getDatabaseBackupRetentionExecution
	case "/api/v1/runners/hosting/database-backup-retention/plan":
		return s.runnerDatabaseBackupRetentionPlan
	case "/api/v1/runners/hosting/database-backup-retention/mutation":
		return s.runnerDatabaseBackupRetentionMutation
	case "/api/v1/resources/{id}/database-connections":
		return s.connectHostingDatabase
	case "/api/v1/resources/{id}/database-connection":
		return s.getHostingDatabaseConnection
	case "/api/v1/resources/{id}/database-connections/options":
		return s.hostingDatabaseConnectionOptions
	case "/api/v1/resources/{id}/database-connection/deployments":
		return s.deployHostingDatabaseConnection
	case "/api/v1/resources/{id}/database-connection/recovery":
		return s.recoverHostingDatabaseConnection
	case "/api/v1/resources/{id}/database/maintenance":
		return s.maintainHostingDatabaseConnection
	case "/api/v1/runners/hosting/database-binding/storage/writer":
		return s.runnerApplicationDatabaseStorageWriter
	case "/api/v1/runners/hosting/database-binding/storage/authorize":
		return s.runnerApplicationDatabaseStorageAuthorize
	case "/api/v1/runners/hosting/database-binding/storage/proxy-plan":
		return s.runnerApplicationDatabaseStorageProxyPlan
	case "/api/v1/runners/hosting/database-binding/yield":
		return s.runnerDatabaseConnectionYield
	case "/api/v1/runners/hosting/database-binding/stage":
		return s.runnerDatabaseConnectionStage
	case "/api/v1/runners/hosting/database-binding/retirement-plan":
		return s.runnerDatabaseConnectionRetirementPlan
	case "/api/v1/runners/hosting/database-binding/retirement":
		return s.runnerDatabaseConnectionRetirement
	case "/api/v1/runners/hosting/database-binding/authorize":
		return s.runnerDatabaseConnectionAuthorize
	case "/api/v1/runners/hosting/database-binding/state":
		return s.runnerDatabaseConnectionState
	case "/api/v1/projects/{project_id}/database-backup-targets":
		return s.databaseBackupTargets
	case "/api/v1/projects/{project_id}/notification-channels", "/api/v1/projects/{project_id}/notification-channels/{channel_id}":
		return s.notificationChannels
	case "/api/v1/projects/{project_id}/notification-channels/{channel_id}/tests":
		return s.testNotificationChannel
	case "/api/v1/projects/{project_id}/notification-deliveries", "/api/v1/projects/{project_id}/notification-deliveries/{delivery_id}":
		return s.notificationDeliveries
	case "/api/v1/projects/{project_id}/database-backup-targets/{target_id}":
		return s.databaseBackupTargets
	case "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/validations":
		return s.databaseBackupTargetValidations
	case "/api/v1/projects/{project_id}/database-backup-targets/{target_id}/storage-validations":
		return s.storageBackupTargetValidations
	case "/api/v1/storage-backups/{id}/transfers":
		return s.storageBackupTransfers
	case "/api/v1/storage-backups/{id}/transfers/retry":
		return s.retryStorageBackupTransfer
	case "/api/v1/storage-backup-transfers/{id}":
		return s.storageBackupTransfer
	case "/api/v1/runners/hosting/storage-backup-transfer/credentials":
		return s.runnerBackupRemoteCredentials
	case "/api/v1/runners/hosting/storage-backup-transfer/plan":
		return s.runnerStorageBackupTransferPlan
	case "/api/v1/runners/hosting/storage-backup-transfer/mutation":
		return s.runnerStorageBackupTransferMutation
	case "/api/v1/database-backups/{id}/transfers":
		return s.databaseBackupTransfers
	case "/api/v1/database-backups/{id}/transfers/retry":
		return s.retryDatabaseBackupTransfer
	case "/api/v1/database-backup-transfers/{id}":
		return s.databaseBackupTransfer
	case "/api/v1/runners/hosting/database-backup-transfer/credentials":
		return s.runnerBackupRemoteCredentials
	case "/api/v1/runners/hosting/database-backup-transfer/plan":
		return s.runnerDatabaseBackupTransferPlan
	case "/api/v1/runners/hosting/database-backup-transfer/mutation":
		return s.runnerDatabaseBackupTransferMutation
	case "/api/v1/resources/{id}/database-import-upload-stages":
		return s.requestDatabaseImportUploadStage
	case "/api/v1/database-import-stages/{id}/content":
		return s.uploadDatabaseImportStageContent
	case "/api/v1/runners/hosting/database-import-stage/upload-stream":
		return s.runnerDatabaseImportUploadStream
	case "/api/v1/runners/hosting/database-import-stage/cleanup":
		return s.runnerDatabaseImportStageCleanup
	case "/api/v1/runners/hosting/database-import-stage/plan":
		return s.runnerDatabaseImportStagePlan
	case "/api/v1/runners/hosting/database-import/plan":
		return s.runnerDatabaseImportPlan
	case "/api/v1/runners/hosting/database-import-stage/mutation":
		return s.runnerDatabaseImportMutation
	case "/api/v1/runners/hosting/database-import/mutation":
		return s.runnerDatabaseImportMutation
	case "/api/v1/runners/hosting/database-import-stage/credentials":
		return s.runnerBackupRemoteCredentials
	case "/api/v1/resources/{id}/database-import-stages":
		return s.requestDatabaseImportStage
	case "/api/v1/database-import-stages/{id}":
		return s.getDatabaseImportStage
	case "/api/v1/resources/{id}/database-imports":
		return s.requestDatabaseImport
	case "/api/v1/database-imports/{id}":
		return s.getDatabaseImport
	case "/api/v1/resources/{id}/database-backups":
		return s.hostingDatabaseBackups
	case "/api/v1/resources/{id}/database-backup-components":
		return s.hostingDatabaseBackupComponents

	case "/api/v1/resources/{id}/storage-backup-schedules":
		return s.hostingStorageBackupSchedules
	case "/api/v1/resources/{id}/storage-backup-schedules/{schedule_id}":
		return s.saveStorageBackupSchedule
	case "/api/v1/resources/{id}/storage-backup-schedules/{schedule_id}/occurrences":
		return s.hostingStorageBackupOccurrences
	case "/api/v1/resources/{id}/database-backup-schedules":
		return s.hostingDatabaseBackupSchedules
	case "/api/v1/resources/{id}/database-backup-schedules/{schedule_id}":
		return s.saveDatabaseBackupSchedule
	case "/api/v1/resources/{id}/database-backup-schedules/{schedule_id}/occurrences":
		return s.hostingDatabaseBackupOccurrences
	case "/api/v1/database-backups/{id}":
		return s.hostingDatabaseBackup
	case "/api/v1/database-backups/{id}/restores":
		return s.restoreHostingDatabaseBackup
	case "/api/v1/runners/hosting/database-backup/plan":
		return s.runnerDatabaseBackupPlan
	case "/api/v1/runners/hosting/database-backup/mutation":
		return s.runnerDatabaseBackupMutation
	case "/api/v1/resources/{id}/storage-backup-volumes":
		return s.hostingStorageBackupVolumes
	case "/api/v1/resources/{id}/storage-backups":
		return s.hostingStorageBackups
	case "/api/v1/database-backups/{id}/archive-options":
		return s.databaseBackupDownloadOptions
	case "/api/v1/database-backups/{id}/archive":
		return s.databaseBackupDownload
	case "/api/v1/runners/hosting/database-backup-download/plan":
		return s.runnerDatabaseBackupDownloadPlan
	case "/api/v1/runners/hosting/database-backup-download/stream":
		return s.runnerDatabaseBackupDownloadStream
	case "/api/v1/storage-backups/{id}/archive-options":
		return s.storageBackupDownloadOptions
	case "/api/v1/storage-backups/{id}/archive":
		return s.storageBackupDownload
	case "/api/v1/runners/hosting/storage-backup-download/plan":
		return s.runnerStorageBackupDownloadPlan
	case "/api/v1/runners/hosting/storage-backup-download/stream":
		return s.runnerStorageBackupDownloadStream
	case "/api/v1/storage-backups/{id}":
		return s.hostingStorageBackup
	case "/api/v1/storage-backups/{id}/restores":
		return s.requestStorageRestore
	case "/api/v1/storage-restores/{id}":
		return s.hostingStorageRestore
	case "/api/v1/hosting-operations/{id}/storage-archive-recovery":
		return s.requestStorageArchiveRecovery
	case "/api/v1/runners/database-network-captures/claim":
		return s.runnerClaimDatabaseNetworkCapture
	case "/api/v1/runners/database-network-captures/claim-next":
		return s.runnerClaimNextDatabaseNetworkCapture
	case "/api/v1/runners/database-network-captures/plan":
		return s.runnerDatabaseNetworkCapturePlan
	case "/api/v1/runners/database-network-captures/origin":
		return s.runnerRecordDatabaseNetworkOrigin
	case "/api/v1/runners/hosting/storage-archive/plan":
		return s.runnerStorageArchivePlan
	case "/api/v1/runners/hosting/storage-archive/prepared":
		return s.runnerStorageArchivePrepared
	case "/api/v1/runners/hosting/storage-archive/apply-begin":
		return s.runnerStorageArchiveApplyBegin
	case "/api/v1/runners/hosting/storage-archive/progress":
		return s.runnerStorageArchiveProgress
	case "/api/v1/runners/hosting/storage-archive/complete":
		return s.runnerStorageArchiveComplete

	case "/api/v1/databases":
		return s.createHostingDatabase
	case "/api/v1/resources/{id}/database/deployments":
		return s.hostingDatabaseDeploymentRequest
	case "/api/v1/resources/{id}/database":
		return s.getHostingDatabase
	case "/api/v1/resources/{id}/storage":
		return s.hostingStorage
	case "/api/v1/server-bootstrap-options":
		return s.serverBootstrapOptions
	case "/api/v1/server-bootstraps":
		return s.serverBootstraps
	case "/api/v1/server-bootstraps/{id}":
		return s.serverBootstrap
	case "/api/v1/server-bootstraps/{id}/probe", "/api/v1/server-bootstraps/{id}/review", "/api/v1/server-bootstraps/{id}/install", "/api/v1/server-bootstraps/{id}/inspect", "/api/v1/server-bootstraps/{id}/register", "/api/v1/server-bootstraps/{id}/grant", "/api/v1/server-bootstraps/{id}/cancel":
		return s.serverBootstrapCommand
	case "/api/v1/servers/{id}/proxy":
		return s.hostingProxy
	case "/api/v1/servers/{id}/proxy/disable":
		return s.hostingProxyDisable
	case "/api/v1/servers/{id}/proxy/hostname-grants":
		return s.hostingHostnameGrants
	case "/api/v1/servers/{id}/proxy/hostname-grants/{grant_id}/revoke":
		return s.hostingHostnameRevoke
	case "/api/v1/resources/{id}/runtime-configuration":
		return http.HandlerFunc(s.hostingRuntimeConfiguration)
	case "/api/v1/resources/{id}/routes":
		return s.hostingRoutes
	case "/api/v1/resources/{id}/storage-deployment-options":
		return s.hostingStorageDeploymentOptions
	case "/api/v1/resources/{id}/storage-route-continuity/{operation_id}":
		return s.hostingStorageRouteContinuityStatus
	case "/api/v1/resources/{id}/storage-route-continuity/{operation_id}/recover":
		return s.recoverHostingStorageRouteContinuity
	case "/api/v1/resources/{id}/route-switches":
		return s.hostingRouteSwitch
	case "/api/v1/resources/{id}/route-switches/{operation_id}":
		return s.hostingRouteSwitchOperation
	case "/api/v1/routes/{id}":
		return s.hostingRoute
	case "/api/v1/proxy-operations/{id}":
		return s.proxyOperation
	case "/api/v1/proxy-operations/{id}/cancel":
		return s.proxyOperationCancel
	case "/api/v1/proxy-operations/{id}/recovery":
		return s.hostingProxyRecovery
	case "/api/v1/proxy-operations/{id}/recovery-plans":
		return s.hostingProxyRecoveryPlan
	case "/api/v1/proxy-operations/{id}/recovery-plans/{plan_id}/apply":
		return s.hostingProxyRecoveryApply
	case "/api/v1/runners/proxy/plan":
		return s.runnerProxyPlan
	case "/api/v1/runners/proxy/prepared":
		return s.runnerProxyPrepared
	case "/api/v1/runners/proxy/apply-begin":
		return s.runnerProxyApplyBegin
	case "/api/v1/runners/proxy/complete":
		return s.runnerProxyComplete
	case "/api/v1/runners/proxy/status":
		return s.runnerProxyStatus
	case "/api/v1/resources/{id}/retained-releases":
		return s.hostingRetainedReleases
	case "/api/v1/resources/{id}/retained-releases/{operation_id}/cleanup":
		return s.hostingReleaseCleanup
	case "/api/v1/resources/{id}/source-grants":
		return s.hostingSourceGrants
	case "/api/v1/resources/{id}/source-grants/{grant_id}/revoke":
		return s.hostingSourceGrantRevoke
	case "/api/v1/resources/{id}/runtime-log-targets":
		return s.runtimeLogTargets
	case "/api/v1/resources/{id}/stack-lifecycle":
		return s.hostingStackLifecycle
	case "/api/v1/resources/{id}/stack-stop":
		return s.hostingStackStop
	case "/api/v1/resources/{id}/runtime-log-sessions":
		return s.runtimeLogOpen
	case "/api/v1/runtime-log-sessions/{id}/renew":
		return s.runtimeLogRenew
	case "/api/v1/runtime-log-sessions/{id}":
		return s.runtimeLogClose
	case "/api/v1/runtime-log-sessions/{id}/logs":
		return s.runtimeLogPage
	case "/api/v1/runners/runtime-log-sessions/claim":
		return s.runtimeLogClaim
	case "/api/v1/runners/runtime-log-sessions/events":
		return s.runtimeLogEvents
	case "/api/v1/resources/{id}/github-push-policy":
		return s.githubPushPolicy
	case "/api/v1/resources/{id}/github-push-policy/verify":
		return s.githubPushVerify
	case "/api/v1/resources/{id}/github-push-policy/enabled":
		return s.githubPushEnabled
	case "/api/v1/resources/{id}/github-push-policy/retry":
		return s.githubPushRetry
	case "/api/v1/resources/{id}/github-push-policy/deliveries":
		return s.githubPushDeliveries
	case "/api/v1/hosting/github-push/{id}":
		return s.githubPushReceive
	case "/api/v1/runners/hosting-observations/targets":
		return s.runnerHostingObservationTargets
	case "/api/v1/runners/hosting-observations/report":
		return s.runnerReportHostingObservation
	case "/api/v1/resources/{id}/runtime-status":
		return s.hostingRuntimeStatus
	case "/api/v1/runners/hosting/database-credentials":
		return s.runnerHostingDatabaseCredentials
	case "/api/v1/runners/hosting/registry-credential":
		return s.runnerHostingRegistryCredential
	case "/api/v1/runners/hosting/runtime-environment":
		return s.runnerHostingRuntimeEnvironment
	case "/api/v1/runners/builds/secrets":
		return s.runnerBuildSecrets
	case "/api/v1/runners/builds/registry-credential":
		return s.runnerBuildRegistryCredential
	case "/api/v1/runners/builds/source-credential":
		return s.runnerBuildSourceCredential
	case "/api/v1/resources/{id}/builds":
		return s.hostingBuilds
	case "/api/v1/hosting-builds/{id}":
		return s.hostingBuild
	case "/api/v1/hosting-builds/{id}/cancel":
		return s.hostingBuildCancel
	case "/api/v1/runners/builds/events":
		return s.runnerBuildEvents
	case "/api/v1/hosting-builds/{id}/logs":
		return s.hostingBuildLogs
	case "/api/v1/runners/builds/status":
		return s.runnerBuildStatus
	case "/api/v1/runners/builds/source-begin":
		return s.runnerBuildSourceBegin
	case "/api/v1/runners/builds/source-commit":
		return s.runnerBuildSourceCommit
	case "/api/v1/runners/builds/prepare":
		return s.runnerBuildPrepare
	case "/api/v1/runners/builds/complete":
		return s.runnerBuildComplete
	case "/api/v1/resources/{id}/deployments/from-build":
		return s.hostingBuildDeploymentRequest
	case "/api/v1/resources/{id}/deployments":
		return s.hostingDeploymentRequest
	case "/api/v1/hosting-operations/{id}":
		return s.hostingOperation
	case "/api/v1/hosting-operations/{id}/cancel":
		return s.hostingOperationCancel
	case "/api/v1/hosting-operations/{id}/reconcile":
		return s.hostingOperationReconcile
	case "/api/v1/runners/hosting/plan":
		return s.runnerHostingPlan
	case "/api/v1/runners/hosting/prepared":
		return s.runnerHostingPrepared
	case "/api/v1/runners/hosting/stack-volumes":
		return s.runnerHostingStackVolumes
	case "/api/v1/runners/hosting/apply-begin":
		return s.runnerHostingApplyBegin
	case "/api/v1/runners/hosting/storage-create-begin":
		return s.runnerHostingStorageCreateBegin
	case "/api/v1/runners/hosting/storage-materialized":
		return s.runnerHostingStorageMaterialized
	case "/api/v1/runners/hosting/complete":
		return s.runnerHostingComplete
	case "/api/v1/runners/hosting/status":
		return s.runnerHostingStatus
	case "/api/v1/runners/hosting/reconciliation":
		return s.runnerHostingReconciliation

	case "/api/v1/runners/identity":
		return s.runnerIdentity
	case "/api/v1/servers":
		return s.hostingServers
	case "/api/v1/runner/server-readiness/challenge":
		return s.runnerServerReadinessChallenge
	case "/api/v1/runner/server-readiness/report":
		return s.runnerReportServerReadiness
	case "/api/v1/servers/{id}/readiness":
		return s.serverReadiness
	case "/api/v1/servers/{id}":
		return s.hostingServerByID
	case "/api/v1/destinations":
		return s.hostingDestinations
	case "/api/v1/destinations/{id}/project-grants":
		return s.destinationProjectGrants
	case "/api/v1/destinations/{id}/project-grants/revoke":
		return s.revokeDestinationProjectGrant
	case "/api/v1/resources/{id}/binding":
		return s.resourceBinding
	case "/api/v1/managed-secrets":
		return s.managedSecretsHandler
	case "/api/v1/managed-secrets/{id}":
		return s.managedSecretByID
	case "/api/v1/managed-secrets/{id}/versions":
		return s.managedSecretVersions
	case "/api/v1/managed-secrets/{id}/revoke":
		return s.managedSecretRevoke
	case "/api/v1/managed-secrets/{id}/reveal":
		return s.managedSecretReveal

	case "/api/v1/project-environments":
		return s.projectEnvironmentsHandler
	case "/api/v1/hosting-catalog/{id}/destination-options":
		return s.hostingCatalogDestinationOptions
	case "/api/v1/hosting-catalog":
		return s.hostingCatalog
	case "/api/v1/hosting-catalog/installations":
		return s.hostingCatalogInstall
	case "/api/v1/resources/{id}/catalog-installation":
		return s.hostingCatalogInstallation
	case "/api/v1/resources":
		return s.resourcesHandler
	case "/api/v1/resources/{id}":
		return s.resourceByID
	case "/api/v1/health":
		return s.health
	case "/api/v1/ready":
		return s.ready
	case "/api/v1/bootstrap-status":
		return s.bootstrapStatus
	case "/api/v1/oidc/status":
		return s.oidcStatus
	case "/api/v1/oidc/login":
		return s.oidcLogin
	case "/api/v1/oidc/callback":
		return s.oidcCallback
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
	case "/api/v1/service-accounts":
		return s.serviceAccountsHandler
	case "/api/v1/service-accounts/rotate":
		return s.rotateServiceAccountCredential
	case "/api/v1/service-accounts/revoke":
		return s.revokeServiceAccount
	case "/api/v1/users":
		return s.listUsers
	case "/api/v1/users/offboard":
		return s.offboardUser
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
	case "/api/v1/runs/{id}":
		return s.runByID
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
