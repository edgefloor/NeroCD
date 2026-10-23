package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type readinessAuthority struct{ server, destination, runner, identity string }

func readinessIdentity(server, destination, runner, token, enrollment string) string {
	return domain.HostingDocumentHash([]string{server, destination, runner, token, enrollment})
}

func readinessEligible(status, token string, capabilities []string) bool {
	return status == "active" && token != "" && slices.Contains(capabilities, domain.ServerReadinessCapability) && !slices.Contains(capabilities, "shell")
}

// lockReadinessAuthority orders runner SHARE, exact used enrollment SHARE, then
// readiness UPDATE. Immutable placement needs no destination/resource/lease lock.
// The INSERT follows both authority locks so its server FK cannot invert them.
func lockReadinessAuthority(ctx context.Context, tx pgx.Tx, runner, credential string) (readinessAuthority, error) {
	var zero readinessAuthority
	q := sqlcgen.New(tx)
	current, err := q.LockReadinessRunner(ctx, runner)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrConflict
	}
	if err != nil {
		return zero, err
	}
	if !readinessEligible(current.Status, current.TokenHash, current.Capabilities) || credential == "" || credential != current.TokenHash {
		return zero, ErrConflict
	}
	enrollment, err := q.LockReadinessEnrollment(ctx, sqlcgen.LockReadinessEnrollmentParams{RunnerID: runner, CredentialHash: &current.TokenHash})
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrConflict
	}
	if err != nil {
		return zero, err
	}
	p, err := q.ReadinessPlacement(ctx, runner)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrConflict
	}
	if err != nil {
		return zero, err
	}
	return readinessAuthority{p.ServerID, p.DestinationID, p.RunnerID, readinessIdentity(p.ServerID, p.DestinationID, p.RunnerID, current.TokenHash, enrollment)}, nil
}

func readinessRow(ctx context.Context, tx pgx.Tx, server string) (sqlcgen.ServerReadiness, error) {
	q := sqlcgen.New(tx)
	if err := q.EnsureServerReadiness(ctx, server); err != nil {
		return sqlcgen.ServerReadiness{}, err
	}
	return q.LockServerReadiness(ctx, server)
}

// IssueServerReadiness issues a ten-second DB-clock challenge for current authority.
func (s *PostgresStore) IssueServerReadiness(ctx context.Context, runner, credential string) (domain.ServerReadinessChallenge, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerReadinessChallenge, error) {
		var zero domain.ServerReadinessChallenge
		p, err := lockReadinessAuthority(ctx, tx, runner, credential)
		if err != nil {
			return zero, err
		}
		row, err := readinessRow(ctx, tx, p.server)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		now, err := q.ServerReadinessClock(ctx)
		if err != nil {
			return zero, err
		}
		var previous domain.ServerReadinessChallenge
		if json.Unmarshal(row.PendingTarget, &previous) == nil && row.PendingIdentity == p.identity && previous.ExpiresAt.After(now) {
			return previous, nil
		}
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return zero, err
		}
		target := domain.ServerReadinessChallenge{SampleID: "srd_" + hex.EncodeToString(id), Profile: domain.ServerReadinessProfile, ServerID: p.server, DestinationID: p.destination, RunnerID: p.runner, IssuedAt: now, ExpiresAt: now.Add(10 * time.Second)}
		raw, err := json.Marshal(target)
		if err != nil {
			return zero, err
		}
		if err = q.SetServerReadinessPending(ctx, sqlcgen.SetServerReadinessPendingParams{ServerID: p.server, PendingTarget: raw, PendingIdentity: p.identity}); err != nil {
			return zero, err
		}
		now, err = q.ServerReadinessClock(ctx)
		if err != nil {
			return zero, err
		}
		if !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		return target, nil
	})
}

// ReportServerReadiness preserves original receipt time on exact canonical replay.
func (s *PostgresStore) ReportServerReadiness(ctx context.Context, runner, credential string, in domain.ServerReadinessReport) (domain.ServerReadinessAck, error) {
	var zero domain.ServerReadinessAck
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerReadinessAck, error) {
		p, err := lockReadinessAuthority(ctx, tx, runner, credential)
		if err != nil {
			return zero, err
		}
		row, err := readinessRow(ctx, tx, p.server)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		// The DB clock must be read after every potentially contended row lock.
		now, err := q.ServerReadinessClock(ctx)
		if err != nil {
			return zero, err
		}
		var accepted domain.ServerReadinessReport
		var ack domain.ServerReadinessAck
		if json.Unmarshal(row.AcceptedBody, &accepted) == nil && accepted.SampleID == in.SampleID {
			if row.AcceptedIdentity != p.identity || accepted != in || json.Unmarshal(row.AcceptedAck, &ack) != nil {
				return zero, ErrConflict
			}
			return ack, nil
		}
		var target domain.ServerReadinessChallenge
		if json.Unmarshal(row.PendingTarget, &target) != nil || row.PendingIdentity != p.identity || target.SampleID != in.SampleID || target.Profile != in.Profile || !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		ack = domain.ServerReadinessAck{SampleID: in.SampleID, ObservedAt: now, Accepted: true}
		body, err := json.Marshal(in)
		if err != nil {
			return zero, err
		}
		receipt, err := json.Marshal(ack)
		if err != nil {
			return zero, err
		}
		if err = q.AcceptServerReadiness(ctx, sqlcgen.AcceptServerReadinessParams{ServerID: p.server, AcceptedBody: body, AcceptedAck: receipt}); err != nil {
			return zero, err
		}
		// The final write can wait too. Reject expiry transactionally so the
		// pending challenge and any previous accepted receipt remain unchanged.
		now, err = q.ServerReadinessClock(ctx)
		if err != nil {
			return zero, err
		}
		if !target.ExpiresAt.After(now) {
			return zero, ErrConflict
		}
		return ack, nil
	})
}

// GetServerReadiness derives diagnostic status from one current SQL snapshot.
func (s *PostgresStore) GetServerReadiness(ctx context.Context, id string) (domain.ServerReadiness, error) {
	out := domain.ServerReadiness{ServerID: id, Profile: domain.ServerReadinessProfile, Status: "unknown", Reason: "no_sample"}
	row, err := s.queries.ServerReadinessSnapshot(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var report domain.ServerReadinessReport
	var ack domain.ServerReadinessAck
	hasSample := json.Unmarshal(row.AcceptedBody, &report) == nil && report.Validate() == nil && json.Unmarshal(row.AcceptedAck, &ack) == nil && ack.Accepted && ack.SampleID == report.SampleID
	if hasSample {
		out.Report = &report
		out.SampleID = report.SampleID
		out.ObservedAt = &ack.ObservedAt
	}
	identity := readinessIdentity(row.ServerID, row.DestinationID, row.RunnerID, row.TokenHash, row.EnrollmentID)
	if !readinessEligible(row.Status, row.TokenHash, row.Capabilities) || row.EnrollmentID == "" || hasSample && (row.AcceptedIdentity == nil || *row.AcceptedIdentity != identity) {
		out.Reason = "authority_unavailable"
		return out, nil
	}
	if row.RestoreHeld {
		out.Status = "blocked"
		out.Reason = "restore_hold"
		return out, nil
	}
	if !hasSample {
		return out, nil
	}
	if ack.ObservedAt.After(row.Now) || row.Now.Sub(ack.ObservedAt) > 30*time.Second {
		out.Reason = "stale"
		return out, nil
	}
	out.Status = "blocked"
	if row.DaemonMismatch {
		out.Reason = "daemon_mismatch"
		return out, nil
	}
	if !report.Qualifies() {
		out.Reason = "prerequisite_failed"
		return out, nil
	}
	out.Status = "ready"
	out.Reason = "qualified"
	return out, nil
}
