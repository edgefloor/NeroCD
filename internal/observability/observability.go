// Package observability owns the deliberately small, fixed-cardinality
// operational metrics vocabulary. It has no HTTP or database dependency so
// stores can provide one authoritative snapshot and transports can render it
// without inventing labels from request or persistence data.
package observability

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// BackupSuccess identifies a successfully completed backup.
	BackupSuccess = "success"
	// BackupFailure identifies a failed backup.
	BackupFailure = "failure"
	// BackupNone identifies a backup outcome with no specific reason.
	BackupNone = "none"
)

// Snapshot is intentionally aggregate-only. It must never grow an identifier,
// name, URL, path, token, or arbitrary error label: those belong in bounded
// logs/audit evidence, not a Prometheus time-series database.
type Snapshot struct {
	CollectedAt                 time.Time                    `json:"collected_at"`
	QueueDepth                  int64                        `json:"queue_depth"`
	QueueOldestAgeSeconds       float64                      `json:"queue_oldest_age_seconds"`
	ActiveLeases                int64                        `json:"active_leases"`
	ExpiredLeases               int64                        `json:"expired_leases"`
	OldestRunnerHeartbeatSecond float64                      `json:"oldest_runner_heartbeat_seconds"`
	TerminalRuns                map[string]DurationAggregate `json:"terminal_runs"`
	Deployments                 map[string]int64             `json:"deployments"`
	DeploymentHealthPassed      int64                        `json:"deployment_health_passed"`
	DeploymentHealthFailed      int64                        `json:"deployment_health_failed"`
	RollbackSucceeded           int64                        `json:"rollback_succeeded"`
	RollbackFailed              int64                        `json:"rollback_failed"`
	RunnerJournalDepth          int64                        `json:"runner_journal_depth"`
	RunnerRetryCount            int64                        `json:"runner_retry_count"`
	RunnerRenewFailures         int64                        `json:"runner_renew_failures"`
	BackupAgeSeconds            float64                      `json:"backup_age_seconds"`
	BackupOutcome               string                       `json:"backup_outcome"`
	BackupReason                string                       `json:"backup_reason"`
	BackupScheduleStatus        string                       `json:"backup_schedule_status"`
	BackupScheduleNextSeconds   float64                      `json:"backup_schedule_next_seconds"`
	BackupScheduleFailures      int64                        `json:"backup_schedule_failures"`
	Pool                        PoolState                    `json:"pool"`
}

// DurationAggregate summarizes a set of durations.
type DurationAggregate struct {
	Count      int64   `json:"count"`
	SumSeconds float64 `json:"sum_seconds"`
}

// PoolState reports current database-pool capacity and use.
type PoolState struct {
	Total    int64 `json:"total"`
	Idle     int64 `json:"idle"`
	Acquired int64 `json:"acquired"`
}

var (
	terminalStatuses     = []string{"succeeded", "failed", "canceled"}
	deploymentStatuses   = []string{"queued", "waiting_confirmation", "assigned", "preparing", "applying", "verifying", "cancel_requested", "rolling_back", "succeeded", "failed", "rolled_back", "rollback_failed", "canceled", "manual_intervention"}
	backupOutcomes       = []string{BackupNone, BackupSuccess, BackupFailure}
	backupReasons        = []string{"none", "preflight", "dump", "publish", "database"}
	backupScheduleStates = []string{"disabled", "waiting", "due", "running", "backoff"}
)

// Render appends only validated, statically enumerated series to base. The
// caller keeps ownership of process-local HTTP counters. Invalid snapshot data
// is an error so a scrape fails safely rather than publishing partial metrics.
func Render(base string, snapshot Snapshot) (string, error) {
	// Older in-process fixture constructors predate the scheduler. Preserve
	// their safe disabled interpretation while production snapshots always
	// carry the explicit DB-derived state.
	if snapshot.BackupScheduleStatus == "" {
		snapshot.BackupScheduleStatus = "disabled"
	}
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString(base)
	gauge(&out, "nerocd_queue_depth", snapshot.QueueDepth)
	gaugeFloat(&out, "nerocd_queue_oldest_age_seconds", snapshot.QueueOldestAgeSeconds)
	gauge(&out, "nerocd_leases", snapshot.ActiveLeases, "state", "active")
	gauge(&out, "nerocd_leases", snapshot.ExpiredLeases, "state", "expired")
	gaugeFloat(&out, "nerocd_runner_oldest_heartbeat_age_seconds", snapshot.OldestRunnerHeartbeatSecond)
	for _, status := range terminalStatuses {
		v := snapshot.TerminalRuns[status]
		gauge(&out, "nerocd_runs_terminal", v.Count, "status", status)
		gaugeFloat(&out, "nerocd_run_duration_seconds_sum", v.SumSeconds, "status", status)
	}
	for _, status := range deploymentStatuses {
		gauge(&out, "nerocd_deployments", snapshot.Deployments[status], "status", status)
	}
	gauge(&out, "nerocd_deployment_health", snapshot.DeploymentHealthPassed, "outcome", "passed")
	gauge(&out, "nerocd_deployment_health", snapshot.DeploymentHealthFailed, "outcome", "failed")
	gauge(&out, "nerocd_rollbacks", snapshot.RollbackSucceeded, "outcome", "succeeded")
	gauge(&out, "nerocd_rollbacks", snapshot.RollbackFailed, "outcome", "failed")
	gauge(&out, "nerocd_runner_journal_depth", snapshot.RunnerJournalDepth)
	gauge(&out, "nerocd_runner_retry_count", snapshot.RunnerRetryCount)
	gauge(&out, "nerocd_runner_renew_failures", snapshot.RunnerRenewFailures)
	gaugeFloat(&out, "nerocd_backup_age_seconds", snapshot.BackupAgeSeconds)
	gaugeFloat(&out, "nerocd_backup_schedule_next_seconds", snapshot.BackupScheduleNextSeconds)
	gauge(&out, "nerocd_backup_schedule_failures", snapshot.BackupScheduleFailures)
	for _, state := range backupScheduleStates {
		value := int64(0)
		if snapshot.BackupScheduleStatus == state {
			value = 1
		}
		gauge(&out, "nerocd_backup_schedule", value, "state", state)
	}
	for _, outcome := range backupOutcomes {
		v := int64(0)
		if snapshot.BackupOutcome == outcome {
			v = 1
		}
		gauge(&out, "nerocd_backup_last_result", v, "outcome", outcome)
	}
	for _, reason := range backupReasons {
		v := int64(0)
		if snapshot.BackupReason == reason {
			v = 1
		}
		gauge(&out, "nerocd_backup_last_reason", v, "reason", reason)
	}
	gauge(&out, "nerocd_postgres_pool_connections", snapshot.Pool.Total, "state", "total")
	gauge(&out, "nerocd_postgres_pool_connections", snapshot.Pool.Idle, "state", "idle")
	gauge(&out, "nerocd_postgres_pool_connections", snapshot.Pool.Acquired, "state", "acquired")
	return out.String(), nil
}

// Validate verifies Snapshot invariants before it is published.
func (s Snapshot) Validate() error {
	if !known(backupOutcomes, s.BackupOutcome) || !known(backupReasons, s.BackupReason) || (s.BackupScheduleStatus != "" && !known(backupScheduleStates, s.BackupScheduleStatus)) {
		return fmt.Errorf("invalid backup observation enum")
	}
	for _, n := range []int64{s.QueueDepth, s.ActiveLeases, s.ExpiredLeases, s.DeploymentHealthPassed, s.DeploymentHealthFailed, s.RollbackSucceeded, s.RollbackFailed, s.RunnerJournalDepth, s.RunnerRetryCount, s.RunnerRenewFailures, s.BackupScheduleFailures, s.Pool.Total, s.Pool.Idle, s.Pool.Acquired} {
		if n < 0 {
			return fmt.Errorf("negative operational count")
		}
	}
	for _, n := range []float64{s.QueueOldestAgeSeconds, s.OldestRunnerHeartbeatSecond, s.BackupAgeSeconds, s.BackupScheduleNextSeconds} {
		if n < 0 {
			return fmt.Errorf("negative operational age")
		}
	}
	for key, value := range s.TerminalRuns {
		if !known(terminalStatuses, key) || value.Count < 0 || value.SumSeconds < 0 {
			return fmt.Errorf("invalid run aggregate")
		}
	}
	for key, value := range s.Deployments {
		if !known(deploymentStatuses, key) || value < 0 {
			return fmt.Errorf("invalid deployment aggregate")
		}
	}
	return nil
}

func known(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func gauge(out *strings.Builder, name string, value int64, labels ...string) {
	out.WriteString(name)
	labelsTo(out, labels)
	out.WriteByte(' ')
	out.WriteString(strconv.FormatInt(value, 10))
	out.WriteByte('\n')
}
func gaugeFloat(out *strings.Builder, name string, value float64, labels ...string) {
	out.WriteString(name)
	labelsTo(out, labels)
	out.WriteByte(' ')
	out.WriteString(strconv.FormatFloat(value, 'f', 3, 64))
	out.WriteByte('\n')
}
func labelsTo(out *strings.Builder, labels []string) {
	if len(labels) == 0 {
		return
	}
	out.WriteByte('{')
	for i := 0; i < len(labels); i += 2 {
		if i != 0 {
			out.WriteByte(',')
		}
		out.WriteString(labels[i])
		out.WriteString(`="`)
		out.WriteString(labels[i+1])
		out.WriteByte('"')
	}
	out.WriteByte('}')
}
