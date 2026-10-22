package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
)

func buildResultAcknowledged(r hostingbuildprotocol.PhaseResponse, entry runner.JournalBuild) error {
	if entry.Result == nil || r.Acknowledgment == nil {
		return errors.New("build completion proof absent")
	}
	req := entry.Result
	phase, err := hostingbuildprotocol.CompletionPhase(req.Completion)
	if err != nil {
		return err
	}
	if hostingbuildprotocol.ValidateAcknowledgment(*r.Acknowledgment, req.Authority, phase, req.ReceiptID, hostingbuildprotocol.CompletionHash(req.Completion)) != nil || r.Build.State != req.Completion.State {
		return errors.New("build completion acknowledgment mismatch")
	}
	if req.Completion.Artifact != nil && (r.Build.Artifact == nil || *r.Build.Artifact != *req.Completion.Artifact) {
		return errors.New("build completion artifact changed")
	}
	if req.Completion.FailureCode != r.Build.FailureCode {
		return errors.New("build completion failure changed")
	}
	return nil
}
func buildStatusProof(status hostingbuildprotocol.StatusResponse, a domain.HostingBuildAuthority, phase, id, hash string) bool {
	for _, ack := range status.Receipts {
		if hostingbuildprotocol.ValidateAcknowledgment(ack, a, phase, id, hash) == nil {
			return true
		}
	}
	return false
}

// Reconcile resends only retained exact requests. It never acquires source or
// calls the engine. Historical acknowledgments settle matching bodies only;
// unresolved intent, missing local result or cleanup uncertainty blocks startup.
func (e *BuildExecutor) Reconcile(ctx context.Context) error {
	for _, entry := range e.journal.Snapshot().Build {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Phase == "attempt_intent" {
			continue
		}
		a := buildAuthority(entry.Attempt, entry.BuildID)
		unlock, err := e.lock(e.config.SourceRoot, a.BuildID)
		if err != nil {
			return err
		}
		err = e.reconcileBuildEntry(ctx, entry, a)
		unlock()
		if err != nil {
			return err
		}
	}
	for _, entry := range e.journal.Snapshot().Build {
		if entry.Phase == "attempt_intent" {
			return errors.New("build intent has no retained acknowledged completion; operator reconciliation required")
		}
	}
	return nil
}
func (e *BuildExecutor) reconcileBuildEntry(ctx context.Context, entry runner.JournalBuild, a domain.HostingBuildAuthority) error {
	status, err := e.status(ctx, a, nil)
	if err != nil {
		return err
	}
	// An unrelated current snapshot must never settle this attempt's hash.
	for _, intent := range e.journal.Snapshot().Build {
		if intent.Phase == "attempt_intent" && buildAuthority(intent.Attempt, intent.BuildID) == a && intent.InputHash != domain.HostingDocumentHash(status.Build.Snapshot) {
			return errors.New("retained build snapshot changed")
		}
	}
	switch entry.Phase {
	case "source":
		req := entry.Source
		if req == nil || domain.ValidateHostingBuildSourceReceipt(req.Receipt, status.Build.Snapshot) != nil {
			return errors.New("retained source identity mismatch")
		}
		if !buildStatusProof(status, a, "source", req.ReceiptID, hostingbuildprotocol.SourceReceiptHash(req.Receipt)) {
			if err = requireLive(status); err != nil {
				return err
			}
		}
		r, sendErr := e.phase(ctx, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
			return e.client.BuildSourceCommit(c, *req)
		}, a, status.Build.Snapshot)
		if sendErr != nil {
			return sendErr
		}
		if r.Acknowledgment == nil || hostingbuildprotocol.ValidateAcknowledgment(*r.Acknowledgment, a, "source", req.ReceiptID, hostingbuildprotocol.SourceReceiptHash(req.Receipt)) != nil || r.Build.SourceReceipt == nil || *r.Build.SourceReceipt != req.Receipt {
			return errors.New("source replay acknowledgment changed")
		}
		return e.journal.AckBuild(entry.ID)
	case "result":
		req := entry.Result
		if req == nil {
			return errors.New("retained result missing")
		}
		if req.Completion.Artifact != nil && domain.ValidateHostingBuildArtifact(*req.Completion.Artifact, status.Build) != nil {
			return errors.New("retained artifact binding changed")
		}
		phase, phaseErr := hostingbuildprotocol.CompletionPhase(req.Completion)
		if phaseErr != nil {
			return phaseErr
		}
		if !buildStatusProof(status, a, phase, req.ReceiptID, hostingbuildprotocol.CompletionHash(req.Completion)) {
			if phase == "cleanup" {
				return errors.New("uncommitted cleanup requires verified operator reconciliation")
			}
			if err = requireLive(status); err != nil {
				return err
			}
		}
		// Retained completion is appended only after source and engine cleanup. Any
		// residual checkout/engine uncertainty disproves that local settlement.
		if _, statErr := os.Lstat(filepath.Join(e.config.SourceRoot, a.BuildID)); !os.IsNotExist(statErr) {
			return errors.New("build checkout cleanup remains unresolved")
		}
		if _, statErr := os.Lstat(filepath.Join(e.config.Engine.StateDir, "cleanup_unconfirmed.json")); !os.IsNotExist(statErr) {
			return errors.New("build engine cleanup remains unresolved")
		}
		r, sendErr := e.phase(ctx, func(c context.Context) (hostingbuildprotocol.PhaseResponse, error) {
			return e.client.BuildComplete(c, *req)
		}, a, status.Build.Snapshot)
		if sendErr != nil {
			return sendErr
		}
		if err = buildResultAcknowledged(r, entry); err != nil {
			return err
		}
		if err = e.journal.AckBuild(entry.ID); err != nil {
			return err
		}
		for _, intent := range e.journal.Snapshot().Build {
			if intent.Phase == "attempt_intent" && buildAuthority(intent.Attempt, intent.BuildID) == a {
				if err = e.journal.AckBuild(intent.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return errors.New("unsupported build journal phase")
}
