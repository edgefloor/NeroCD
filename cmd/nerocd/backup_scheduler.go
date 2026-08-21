package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	backupSchedulerLockKey = "nerocd:backup-scheduler:v1"
	backupSchedulerMaxRun  = 10 * time.Minute
)

// backup-scheduler is a separate owner-credential process. The server stays
// read-only with respect to scheduling, so browser and application workers can
// neither start nor rotate archives.
func backupScheduler(args []string) error {
	fs := flag.NewFlagSet("backup-scheduler", flag.ContinueOnError)
	databaseURL := fs.String("database-url", "", "PostgreSQL connection URL (development only)")
	output := fs.String("output-dir", "", "existing owner-only backup root")
	runnerRoot := fs.String("runner-file-root", "", "owner-only runner_file root")
	interval := fs.Int("interval-seconds", 86400, "durable schedule interval (60..604800)")
	retain := fs.Int("retention-count", 7, "backup directories retained (1..365)")
	enabled := fs.Bool("enabled", false, "enable the durable schedule")
	once := fs.Bool("once", false, "perform at most one due scheduling decision")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		return errors.New("backup-scheduler requires --output-dir")
	}
	if *interval < 60 || *interval > 604800 || *retain < 1 || *retain > 365 {
		return errors.New("backup-scheduler policy is invalid")
	}
	if err := ensureSecureBackupParent(*output); err != nil {
		return err
	}
	resolvedURL, err := resolveBackupRestoreDatabaseURL(fs, *databaseURL)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(context.Background(), resolvedURL)
	if err != nil {
		return errors.New("backup scheduler database is unavailable")
	}
	defer pool.Close()
	if err := configureBackupSchedule(context.Background(), pool, *enabled, *interval, *retain); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		ran, err := runDueBackupSchedule(ctx, pool, resolvedURL, *output, *runnerRoot)
		if err != nil {
			return err
		}
		if *once || ran {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
	}
}

func configureBackupSchedule(ctx context.Context, pool *pgxpool.Pool, enabled bool, interval, retain int) error {
	_, err := pool.Exec(ctx, `UPDATE backup_schedule
SET next_run_at=CASE WHEN enabled IS DISTINCT FROM $1 OR interval_seconds IS DISTINCT FROM $2 OR retention_count IS DISTINCT FROM $3 THEN clock_timestamp() ELSE next_run_at END,
    consecutive_failures=CASE WHEN enabled IS DISTINCT FROM $1 OR interval_seconds IS DISTINCT FROM $2 OR retention_count IS DISTINCT FROM $3 THEN 0 ELSE consecutive_failures END,
    enabled=$1, interval_seconds=$2, retention_count=$3, updated_at=clock_timestamp()
WHERE singleton`, enabled, interval, retain)
	if err != nil {
		return errors.New("backup scheduler configuration could not be saved")
	}
	return nil
}

// runDueBackupSchedule holds a PostgreSQL session advisory lock across the
// actual dump and rotation. A second scheduler can observe configuration but
// cannot overlap a local archive operation. The schedule timestamps are DB
// clock values; the host clock only bounds process lifetime.
func runDueBackupSchedule(ctx context.Context, pool *pgxpool.Pool, resolvedURL, output, runnerRoot string) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, errors.New("backup scheduler database is unavailable")
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, backupSchedulerLockKey).Scan(&locked); err != nil {
		return false, errors.New("backup scheduler lock is unavailable")
	}
	if !locked {
		return false, nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, backupSchedulerLockKey)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, errors.New("backup scheduler transaction is unavailable")
	}
	defer tx.Rollback(ctx)
	var enabled bool
	var interval, retain, failures int
	var next, now time.Time
	if err := tx.QueryRow(ctx, `SELECT enabled, interval_seconds, retention_count, next_run_at, consecutive_failures, clock_timestamp() FROM backup_schedule WHERE singleton FOR UPDATE`).Scan(&enabled, &interval, &retain, &next, &failures, &now); err != nil {
		return false, errors.New("backup scheduler state is unavailable")
	}
	staleBefore := now.Add(-backupSchedulerMaxRun)
	if _, err := tx.Exec(ctx, `UPDATE backup_schedule_runs SET status='failed', reason='database', completed_at=clock_timestamp() WHERE status='running' AND started_at < $1`, staleBefore); err != nil {
		return false, errors.New("backup scheduler recovery failed")
	}
	var running int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM backup_schedule_runs WHERE status='running'`).Scan(&running); err != nil {
		return false, errors.New("backup scheduler state is unavailable")
	}
	if !enabled || next.After(now) || running != 0 {
		if err := tx.Commit(ctx); err != nil {
			return false, errors.New("backup scheduler transaction failed")
		}
		return false, nil
	}
	runID, err := randomRuntimeHex(16)
	if err != nil {
		return false, errors.New("backup scheduler run could not be created")
	}
	runID = "bks_" + runID
	if _, err := tx.Exec(ctx, `INSERT INTO backup_schedule_runs (id, scheduled_for, status, reason) VALUES ($1,$2,'running','none')`, runID, next); err != nil {
		return false, errors.New("backup scheduler run could not be recorded")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, errors.New("backup scheduler transaction failed")
	}

	backupCtx, cancel := context.WithTimeout(ctx, backupSchedulerMaxRun)
	defer cancel()
	// The established manual command owns secure staging, manifest publication,
	// runner-file verification, and closed backup observation results. We invoke
	// it in-process so no owner URL appears in an argv or child environment.
	args := []string{"--output-dir", output}
	// The manual parser intentionally resolves development URLs from explicit
	// input, while production resolves the owner secret file a second time.
	// Keep the development value in-process only; never place it in a child
	// argv or production flag.
	if mode, modeErr := configuredMode(); modeErr == nil && mode != modeProduction {
		args = append(args, "--database-url", resolvedURL)
	}
	if strings.TrimSpace(runnerRoot) != "" {
		args = append(args, "--runner-file-root", runnerRoot)
	}
	err = backupDatabaseContext(backupCtx, args)
	reason, status := "none", "succeeded"
	if err == nil {
		err = rotateSecureBackups(output, retain)
		if err != nil {
			reason, status = "rotation", "failed"
		}
	}
	if err != nil && reason == "none" {
		reason, status = "database", "failed"
	}

	finish, finishErr := conn.Begin(context.Background())
	if finishErr != nil {
		return true, errors.New("backup scheduler completion is unavailable")
	}
	defer finish.Rollback(context.Background())
	var finishNow time.Time
	if finishErr = finish.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&finishNow); finishErr != nil {
		return true, errors.New("backup scheduler completion is unavailable")
	}
	if _, finishErr = finish.Exec(context.Background(), `UPDATE backup_schedule_runs SET status=$1, reason=$2, completed_at=clock_timestamp() WHERE id=$3 AND status='running'`, status, reason, runID); finishErr != nil {
		return true, errors.New("backup scheduler completion failed")
	}
	if status == "succeeded" {
		_, finishErr = finish.Exec(context.Background(), `UPDATE backup_schedule SET next_run_at=$1, consecutive_failures=0, updated_at=clock_timestamp() WHERE singleton`, finishNow.Add(time.Duration(interval)*time.Second))
	} else {
		failures, backoff := backupScheduleBackoff(interval, failures)
		_, finishErr = finish.Exec(context.Background(), `UPDATE backup_schedule SET next_run_at=$1, consecutive_failures=$2, updated_at=clock_timestamp() WHERE singleton`, finishNow.Add(backoff), failures)
	}
	if finishErr != nil || finish.Commit(context.Background()) != nil {
		return true, errors.New("backup scheduler completion failed")
	}
	if err != nil {
		return true, fmt.Errorf("backup scheduler run failed: %w", err)
	}
	return true, nil
}

// backupScheduleBackoff has a deliberately closed policy: failures are
// bounded in the durable row and retry no sooner than one minute, doubling
// only until the configured interval. A restart reads that row rather than
// reconstructing schedule time from a host clock.
func backupScheduleBackoff(interval, failures int) (int, time.Duration) {
	if interval < 60 {
		interval = 60
	}
	failures = min(failures+1, 8)
	seconds := min(interval, 60<<uint(failures-1))
	return failures, time.Duration(seconds) * time.Second
}
