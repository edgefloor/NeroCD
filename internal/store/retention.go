package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"nerocd/internal/domain"
)

const maxRunLogRetentionBatch = 10000

func validRunLogRetentionPolicy(p domain.RunLogRetentionPolicy) bool {
	return p.KeepDays >= 1 && p.KeepDays <= 3650 && p.BatchSize >= 1 && p.BatchSize <= maxRunLogRetentionBatch
}

// RunLogRetentionBodyHash is the canonical policy-bound idempotency body for a
// manual execution. It deliberately excludes actor and wall time so an exact
// request can be replayed, while a changed policy version conflicts.
func RunLogRetentionBodyHash(p domain.RunLogRetentionPolicy) string {
	b, _ := json.Marshal(struct{ V int }{p.Version})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *MemoryStore) GetRunLogRetentionPolicy(_ context.Context) (domain.RunLogRetentionPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retentionPolicy, nil
}
func (s *MemoryStore) UpdateRunLogRetentionPolicy(_ context.Context, p domain.RunLogRetentionPolicy) (domain.RunLogRetentionPolicy, error) {
	if !validRunLogRetentionPolicy(p) {
		return domain.RunLogRetentionPolicy{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateRunLogRetentionPolicyLocked(p), nil
}

func (s *MemoryStore) updateRunLogRetentionPolicyLocked(p domain.RunLogRetentionPolicy) domain.RunLogRetentionPolicy {
	p.Version = s.retentionPolicy.Version + 1
	if p.Version <= 0 {
		p.Version = 1
	}
	p.UpdatedAt = time.Now().UTC()
	s.retentionPolicy = p
	return p
}

func (s *MemoryStore) UpdateRunLogRetentionPolicyWithAudit(_ context.Context, p domain.RunLogRetentionPolicy, audit domain.AuditEvent) (domain.RunLogRetentionPolicy, error) {
	if !validRunLogRetentionPolicy(p) {
		return domain.RunLogRetentionPolicy{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.RunLogRetentionPolicy{}, errors.New("audit id conflict")
	}
	updated := s.updateRunLogRetentionPolicyLocked(p)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return updated, nil
}
func (s *MemoryStore) retentionPreviewLocked(now time.Time) (domain.RunLogRetentionPreview, []int) {
	p := s.retentionPolicy
	result := domain.RunLogRetentionPreview{}
	if !p.Enabled {
		return result, nil
	}
	result.Cutoff = now.Add(-time.Duration(p.KeepDays) * 24 * time.Hour)
	active := map[string]bool{}
	for _, l := range s.leases {
		if l.Status == domain.LeaseActive {
			active[l.RunID] = true
		}
	}
	terminal := map[string]bool{}
	for _, r := range s.runs {
		terminal[r.ID] = domain.IsTerminalRunStatus(r.Status)
	}
	indexes := []int{}
	for i, l := range s.logs {
		if terminal[l.RunID] && !active[l.RunID] && l.CreatedAt.Before(result.Cutoff) {
			result.EligibleLogs++
			result.EligibleBytes += int64(len(l.Message))
			indexes = append(indexes, i)
		}
	}
	// Match the PostgreSQL candidate order.  Deletion later reverses only the
	// selected indexes so slicing the backing store cannot alter admission.
	sort.Slice(indexes, func(i, j int) bool {
		left, right := s.logs[indexes[i]], s.logs[indexes[j]]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return left.ID < right.ID
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
	return result, indexes
}
func (s *MemoryStore) PreviewRunLogRetention(_ context.Context) (domain.RunLogRetentionPreview, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, _ := s.retentionPreviewLocked(time.Now().UTC())
	return p, nil
}
func (s *MemoryStore) ExecuteRunLogRetention(_ context.Context, id, body string, audit domain.AuditEvent) (domain.RunLogRetentionExecution, error) {
	if strings.TrimSpace(id) == "" {
		return domain.RunLogRetentionExecution{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.retentionReceipts[id]; ok {
		if body != RunLogRetentionBodyHash(old.Policy) {
			return domain.RunLogRetentionExecution{}, ErrConflict
		}
		return old, nil
	}
	if !s.retentionPolicy.Enabled {
		return domain.RunLogRetentionExecution{}, ErrConflict
	}
	if body != RunLogRetentionBodyHash(s.retentionPolicy) {
		return domain.RunLogRetentionExecution{}, ErrConflict
	}
	// Validate the immutable audit insertion before deleting anything.  The
	// in-memory implementation holds the same all-or-nothing contract as the
	// PostgreSQL transaction below; it has no transaction to undo a deletion.
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.RunLogRetentionExecution{}, errors.New("audit id conflict")
	}
	preview, indexes := s.retentionPreviewLocked(time.Now().UTC())
	n := s.retentionPolicy.BatchSize
	if len(indexes) < n {
		n = len(indexes)
	}
	indexes = append([]int(nil), indexes[:n]...)
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))
	var bytes int64
	for _, i := range indexes[:n] {
		bytes += int64(len(s.logs[i].Message))
		s.logs = append(s.logs[:i], s.logs[i+1:]...)
	}
	audit.Metadata = map[string]any{"policy_version": s.retentionPolicy.Version, "cutoff": preview.Cutoff.UTC().Format(time.RFC3339), "deleted": n, "deleted_bytes": bytes}
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	out := domain.RunLogRetentionExecution{RequestID: id, Policy: s.retentionPolicy, Preview: preview, Deleted: int64(n), DeletedBytes: bytes}
	s.retentionReceipts[id] = out
	return out, nil
}
