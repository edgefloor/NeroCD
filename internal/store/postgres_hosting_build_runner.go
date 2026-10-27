package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// buildRunnerTransaction locks the task and historical lease first, then the
// destination advisory lock, resource, authorization, source, placement, credentials,
// and build row. Every new write must also pass freshBuildAuthority after its
// last blocking write.
func (s *PostgresStore) buildRunnerTransaction(ctx context.Context, runner string, a domain.HostingBuildAuthority, credentials bool, fn func(pgx.Tx, domain.HostingBuild) (domain.HostingBuild, error)) (domain.HostingBuild, error) {
	if !buildRecordID(a.BuildID) || a.RunID != "build_run_"+a.BuildID || a.LeaseID == "" || a.Fence == "" || a.Attempt < 1 || a.Attempt > math.MaxInt32 {
		return domain.HostingBuild{}, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		b, err := readBuild(ctx, q, a.BuildID)
		if err != nil {
			return b, err
		}
		if _, err = q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
			return b, ErrConflict
		}
		if err = checkBuildAuthority(ctx, q, runner, a, b, false); err != nil {
			return b, err
		}
		if _, err = lockBuildResourceDestination(ctx, tx, b.ResourceID); err != nil {
			return b, err
		}
		if err = authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer"); err != nil {
			return b, err
		}
		if _, err = readBuildSource(ctx, tx, b.ResourceID); err != nil {
			return b, err
		}
		if err = checkBuildPlacement(ctx, tx, b, credentials); err != nil {
			return b, err
		}
		if _, err = q.LockHostingBuildState(ctx, b.ID); err != nil {
			return b, err
		}
		b, err = readBuild(ctx, q, b.ID)
		if err != nil || b.RunID != a.RunID || b.Snapshot.RunnerID != runner {
			return b, ErrConflict
		}
		return fn(tx, b)
	})
}
func checkBuildAuthority(ctx context.Context, q *sqlcgen.Queries, runner string, a domain.HostingBuildAuthority, b domain.HostingBuild, live bool) error {
	_, err := q.CheckHostingBuildAuthority(ctx, sqlcgen.CheckHostingBuildAuthorityParams{BuildID: b.ID, RunnerID: runner, RunID: a.RunID, ID: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence, ProjectID: b.ProjectID, Column8: live})
	if err != nil {
		return ErrConflict
	}
	return nil
}
func freshBuildAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingBuildAuthority, b domain.HostingBuild) error {
	if err := authorizeBuildActor(ctx, tx, b.CreatedBy, b.ProjectID, "maintainer"); err != nil {
		return err
	}
	if err := currentBuildExecution(ctx, tx, b); err != nil {
		return err
	}
	return checkBuildAuthority(ctx, sqlcgen.New(tx), runner, a, b, true)
}
func buildRunnerAudit(ctx context.Context, tx pgx.Tx, b domain.HostingBuild, phase, receipt string) error {
	return buildAudit(ctx, tx, domain.AuditEvent{ID: "aud_build_" + domain.HostingDocumentHash(struct{ Build, Phase, Receipt string }{b.ID, phase, receipt})[7:], ActorID: b.CreatedBy}, b.CreatedBy, "hosting.build."+phase, b.ID)
}

// receiptReplay acknowledges only an already committed identical body and tuple.
// Historical expiry never authorizes another mutation.
func receiptReplay(ctx context.Context, q *sqlcgen.Queries, b domain.HostingBuild, a domain.HostingBuildAuthority, phase, id string, body any) (bool, error) {
	if id == "" || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
		return false, ErrConflict
	}
	row, err := q.GetHostingBuildReceipt(ctx, sqlcgen.GetHostingBuildReceiptParams{BuildID: b.ID, Phase: phase})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var old domain.HostingBuildAuthority
	if json.Unmarshal(row.Authority, &old) != nil || old != a || row.ReceiptID != id || row.BodyHash != domain.HostingDocumentHash(body) {
		return false, ErrConflict
	}
	return true, nil
}
func appendBuildReceipt(ctx context.Context, q *sqlcgen.Queries, b domain.HostingBuild, a domain.HostingBuildAuthority, phase, id string, body any) error {
	return q.InsertHostingBuildReceipt(ctx, sqlcgen.InsertHostingBuildReceiptParams{BuildID: b.ID, Phase: phase, ReceiptID: id, Authority: buildJSON(a), Body: buildJSON(body), BodyHash: domain.HostingDocumentHash(body)})
}

// BeginHostingBuildSource starts sourcing under a live full lease tuple and revision CAS.
func (s *PostgresStore) BeginHostingBuildSource(ctx context.Context, runner string, a domain.HostingBuildAuthority, revision int64) (domain.HostingBuild, error) {
	return s.buildRunnerTransaction(ctx, runner, a, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		if b.State != "queued" || b.Revision != revision {
			return b, ErrConflict
		}
		if err := freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		if err := casBuild(q.SetHostingBuildState(ctx, sqlcgen.SetHostingBuildStateParams{ID: b.ID, Revision: revision, State: "sourcing"})); err != nil {
			return b, err
		}
		if err := buildRunnerAudit(ctx, tx, b, "sourcing", a.LeaseID); err != nil {
			return b, err
		}
		if err := freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		return readBuild(ctx, q, b.ID)
	})
}

// CommitHostingBuildSource durably records an immutable receipt before checkout use.
func (s *PostgresStore) CommitHostingBuildSource(ctx context.Context, runner string, a domain.HostingBuildAuthority, id string, r domain.HostingBuildSourceReceipt) (domain.HostingBuild, error) {
	return s.buildRunnerTransaction(ctx, runner, a, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		if domain.ValidateHostingBuildSourceReceipt(r, b.Snapshot) != nil {
			return b, ErrConflict
		}
		replay, err := receiptReplay(ctx, q, b, a, "source", id, r)
		if err != nil || replay {
			return b, err
		}
		if b.State != "sourcing" {
			return b, ErrConflict
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		if err = appendBuildReceipt(ctx, q, b, a, "source", id, r); err != nil {
			return b, err
		}
		if err = casBuild(q.SetHostingBuildState(ctx, sqlcgen.SetHostingBuildStateParams{ID: b.ID, Revision: b.Revision, State: b.State})); err != nil {
			return b, err
		}
		if err = buildRunnerAudit(ctx, tx, b, "source_committed", id); err != nil {
			return b, err
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		return readBuild(ctx, q, b.ID)
	})
}

// PrepareHostingBuild pins a live runner-observed daemon before any image build or import.
func (s *PostgresStore) PrepareHostingBuild(ctx context.Context, runner string, a domain.HostingBuildAuthority, revision int64, daemon string) (domain.HostingBuild, error) {
	if daemon == "" || len(daemon) > 256 || daemon != strings.TrimSpace(daemon) || strings.ContainsFunc(daemon, unicode.IsControl) {
		return domain.HostingBuild{}, ErrConflict
	}
	return s.buildRunnerTransaction(ctx, runner, a, true, func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		if b.Revision != revision || b.SourceReceipt == nil || (b.State != "sourcing" && b.State != "building") || (b.DaemonID != "" && b.DaemonID != daemon) || (b.Snapshot.ExpectedDaemonID != "" && b.Snapshot.ExpectedDaemonID != daemon) {
			return b, ErrConflict
		}
		observed, err := q.HostingBuildObservedDaemon(ctx, b.Snapshot.BindingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return b, err
		}
		if observed != "" && observed != daemon {
			return b, ErrConflict
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		// Revalidation of the retained pin has no new effects or receipt.
		if b.State == "building" {
			return b, nil
		}
		if err = casBuild(q.PinHostingBuildDaemon(ctx, sqlcgen.PinHostingBuildDaemonParams{ID: b.ID, Revision: revision, DaemonID: daemon})); err != nil {
			return b, err
		}
		if err = buildRunnerAudit(ctx, tx, b, "prepared", daemon); err != nil {
			return b, err
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		return readBuild(ctx, q, b.ID)
	})
}

// CompleteHostingBuild atomically records a receipt, optional artifact, state, and audit under live authority.
func (s *PostgresStore) CompleteHostingBuild(ctx context.Context, runner string, a domain.HostingBuildAuthority, id string, revision int64, c HostingBuildCompletion) (domain.HostingBuild, error) {
	if c.State != "succeeded" && c.State != "failed" && c.State != "canceled" && c.State != "cleanup_required" {
		return domain.HostingBuild{}, ErrConflict
	}
	if c.State == "succeeded" {
		if (c.Artifact == nil) == (c.ComposeResult == nil) || c.FailureCode != "" {
			return domain.HostingBuild{}, ErrConflict
		}
	} else if c.Artifact != nil || c.ComposeResult != nil || !domain.ValidHostingBuildFailure(c.FailureCode) {
		return domain.HostingBuild{}, ErrConflict
	}
	return s.buildRunnerTransaction(ctx, runner, a, c.State == "succeeded", func(tx pgx.Tx, b domain.HostingBuild) (domain.HostingBuild, error) {
		q := sqlcgen.New(tx)
		phase := "complete"
		if c.State == "cleanup_required" {
			phase = "cleanup"
		}
		replay, err := receiptReplay(ctx, q, b, a, phase, id, c)
		if err != nil || replay {
			return b, err
		}
		if b.Revision != revision || b.State == "succeeded" || b.State == "failed" || b.State == "canceled" {
			return b, ErrConflict
		}
		if c.State == "succeeded" {
			if b.State != "building" {
				return b, ErrConflict
			}
			if b.Snapshot.Source.Settings.Strategy == "compose" {
				if !b.Snapshot.Policy.GitCompose || c.ComposeResult == nil || c.Artifact != nil || domain.ValidateHostingBuildComposeResult(*c.ComposeResult, b) != nil {
					return b, ErrConflict
				}
			} else if c.Artifact == nil || c.ComposeResult != nil || domain.ValidateHostingBuildArtifact(*c.Artifact, b) != nil {
				return b, ErrConflict
			}
		}

		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		if c.Artifact != nil {
			if err = q.InsertHostingBuildArtifact(ctx, sqlcgen.InsertHostingBuildArtifactParams{BuildID: b.ID, Body: buildJSON(c.Artifact), BodyHash: domain.HostingDocumentHash(c.Artifact)}); err != nil {
				return b, err
			}
		}
		if c.ComposeResult != nil {
			if err = q.InsertHostingBuildComposeResult(ctx, sqlcgen.InsertHostingBuildComposeResultParams{BuildID: b.ID, Body: buildJSON(c.ComposeResult), BodyHash: c.ComposeResult.Hash, DefinitionHash: domain.HostingDocumentHash(c.ComposeResult.Definition)}); err != nil {
				return b, err
			}
		}
		if err = appendBuildReceipt(ctx, q, b, a, phase, id, c); err != nil {
			return b, err
		}
		if err = casBuild(q.SetHostingBuildState(ctx, sqlcgen.SetHostingBuildStateParams{ID: b.ID, Revision: revision, State: c.State, FailureCode: c.FailureCode})); err != nil {
			return b, err
		}
		if err = s.autoWakeup(ctx, tx, b.ResourceID); err != nil {
			return b, err
		}
		if err = buildRunnerAudit(ctx, tx, b, phase, id); err != nil {
			return b, err
		}
		if err = freshBuildAuthority(ctx, tx, runner, a, b); err != nil {
			return b, err
		}
		if c.State != "cleanup_required" {
			if err = casBuild(q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: a.RunID, Status: c.State})); err != nil {
				return b, err
			}
			if err = casBuild(q.CompleteHostingBuildLease(ctx, sqlcgen.CompleteHostingBuildLeaseParams{LeaseID: a.LeaseID, RunID: a.RunID, RunnerID: runner, Attempt: int32(a.Attempt), Fence: a.Fence, State: c.State, ReceiptID: &id})); err != nil {
				return b, err
			}
		}
		return readBuild(ctx, q, b.ID)
	})
}
