package runner

import (
	"encoding/json"
	"errors"
	"nerocd/internal/domain"
	"slices"
	"time"
)

// JournalDatabaseImport retains append-before-send evidence; an intent never permits reimport.
type JournalDatabaseImport struct {
	ID         string                                 `json:"id"`
	Attempt    AttemptIdentity                        `json:"attempt_authority"`
	StagePlan  *domain.HostingDatabaseImportStagePlan `json:"stage_plan,omitempty"`
	ImportPlan *domain.HostingDatabaseImportPlan      `json:"import_plan,omitempty"`
	Request    domain.HostingDatabaseImportMutation   `json:"request"`
	CreatedAt  time.Time                              `json:"created_at"`
}

func validateJournalDatabaseImport(e JournalDatabaseImport) error {
	m := e.Request
	a := m.HostingAuthority
	if e.ID != m.ReceiptID || m.Validate() != nil || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence {
		return errors.New("invalid external import journal")
	}
	if a.RunID == "db_import_stage_"+a.OperationID {
		if e.StagePlan == nil || e.ImportPlan != nil || e.StagePlan.Validate() != nil || e.StagePlan.Stage.ID != a.OperationID || m.PlanHash != e.StagePlan.Hash || m.DaemonID != e.StagePlan.Target.Release.DaemonID || m.Ready != nil && m.Ready.ValidateFor(*e.StagePlan) != nil {
			return errors.New("invalid external stage journal")
		}
	} else if e.ImportPlan == nil || e.StagePlan != nil || e.ImportPlan.Validate() != nil || e.ImportPlan.Operation.ID != a.OperationID || m.PlanHash != e.ImportPlan.Hash || m.DaemonID != e.ImportPlan.StagePlan.Target.Release.DaemonID {
		return errors.New("invalid external SQL journal")
	}
	return nil
}
func cloneDatabaseImports(v []JournalDatabaseImport) []JournalDatabaseImport {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalDatabaseImport
	_ = json.Unmarshal(raw, &out)
	return out
}
func validateJournalDatabaseImports(s journalState) error {
	seen := map[string]bool{}
	for _, e := range s.DatabaseImports {
		if seen[e.ID] || validateJournalDatabaseImport(e) != nil {
			return errors.New("invalid or duplicate external import journal")
		}
		seen[e.ID] = true
		for _, c := range s.Completions {
			if sameAttemptAuthority(c.Attempt, e.Attempt) {
				return errors.New("external import cannot use generic completion")
			}
		}
	}
	return nil
}

// AppendDatabaseImport persists a single exact receipt before any network send.
func (j *AttemptJournal) AppendDatabaseImport(e JournalDatabaseImport) (JournalDatabaseImport, error) {
	if err := validateJournalDatabaseImport(e); err != nil {
		return JournalDatabaseImport{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.DatabaseImports {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalDatabaseImport{}, ErrJournalConflict
			}
			return cloneDatabaseImports([]JournalDatabaseImport{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalDatabaseImport{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.DatabaseImports
	j.state.DatabaseImports = append(cloneDatabaseImports(prior), e)
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseImports = prior
		return JournalDatabaseImport{}, err
	}
	return cloneDatabaseImports([]JournalDatabaseImport{e})[0], nil
}

// AckDatabaseImport removes only controller-accepted evidence.
func (j *AttemptJournal) AckDatabaseImport(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.DatabaseImports
	j.state.DatabaseImports = slices.DeleteFunc(slices.Clone(prior), func(e JournalDatabaseImport) bool { return e.ID == id })
	if e := j.persistLocked(); e != nil {
		j.state.DatabaseImports = prior
		return e
	}
	return nil
}
