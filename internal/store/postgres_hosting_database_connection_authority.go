package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func connectionOperation(op domain.HostingOperation) bool {
	return op.ApplicationDatabase != nil || op.DatabaseMaintenance != nil
}

func loadDatabaseConnectionExecution(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	q := sqlcgen.New(db)
	present, err := q.DatabaseConnectionSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	row, err := q.ReadDatabaseConnectionExecution(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch row.Family {
	case "application":
		var p domain.HostingApplicationDatabaseExecutionProof
		if json.Unmarshal(row.Proof, &p) != nil || p.Hash != row.ProofHash || p.MembershipRevision != row.ExpectedMembershipRevision {
			return ErrConflict
		}
		op.ApplicationDatabase = &p
		if err = loadApplicationDatabaseContinuity(ctx, db, op); err != nil {
			return err
		}
		if domain.ValidateHostingApplicationDatabaseOperation(*op) != nil {
			return ErrConflict
		}
	case "maintenance":
		var p domain.HostingDatabaseMaintenanceProof
		if json.Unmarshal(row.Proof, &p) != nil || p.Hash != row.ProofHash || p.MembershipRevision != row.ExpectedMembershipRevision {
			return ErrConflict
		}
		op.DatabaseMaintenance = &p
		if domain.ValidateHostingDatabaseMaintenanceOperation(*op) != nil {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	return connectionPublicationBound(*op)
}

// Historical reads retain evidence after legitimate membership advances. They
// still check current owner, target, grants and capabilities and grant no effect.
func databaseConnectionAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe, historical bool) error {
	if !connectionOperation(op) {
		return nil
	}
	q := sqlcgen.New(tx)
	if err := databaseConnectionSchema(ctx, q); err != nil {
		return err
	}
	row, err := q.ReadDatabaseConnectionExecution(ctx, op.ID)
	if err != nil {
		return hostingError(err)
	}
	c, err := q.ReadDatabaseConnectionByID(ctx, row.ConnectionID)
	if err != nil {
		return hostingError(err)
	}
	actor := op.CreatedBy
	if observe {
		if op.ReconciliationRunID == "" {
			return ErrConflict
		}
		actor, err = q.HostingTaskActor(ctx, op.ReconciliationRunID)
		if err != nil {
			return hostingError(err)
		}
	}
	b, err := lockConnectionTargets(ctx, tx, c, actor, domain.RoleOwner)
	if err != nil {
		return err
	}
	if op.ApplicationDatabaseRouteContinuity != nil {
		compatible, e := q.ApplicationDatabaseRouteSchemaCompatible(ctx)
		if e != nil || !compatible {
			return ErrHostingDatabaseConnectionUnavailable
		}
		if err = continuityAgent(ctx, tx, b.RunnerID); err != nil {
			return err
		}
	}
	if err = requireConnectionAgent(ctx, tx, b.RunnerID, false); err != nil {
		return err
	}
	view, err := hostingDatabaseLoad(ctx, q, c.DatabaseResourceID)
	if err != nil {
		return err
	}
	credentialMatches := false
	for _, credential := range view.Credentials {
		if credential.Role == "app" && credential == b.Credential {
			credentialMatches = true
		}
	}
	if !credentialMatches {
		return ErrConflict
	}
	refs := []domain.HostingRuntimeSecretReference{{SecretID: b.Credential.SecretID, Version: b.Credential.Version}}
	if op.DatabaseMaintenance != nil {
		refs = make([]domain.HostingRuntimeSecretReference, 0, len(view.Credentials))
		for _, credential := range view.Credentials {
			refs = append(refs, domain.HostingRuntimeSecretReference{SecretID: credential.SecretID, Version: credential.Version})
		}
	}
	if err = validateRuntimeReferences(ctx, q, c.ProjectID, refs); err != nil {
		return err
	}
	app, database, err := lockDatabaseBindingTargets(ctx, tx, c.ApplicationResourceID, c.DatabaseResourceID)
	if err != nil || app.Generation != b.ApplicationResourceGeneration || database.Generation != b.DatabaseResourceGeneration || op.Snapshot.RunnerID != b.RunnerID {
		return ErrConflict
	}
	var revision int64
	var members, retired any
	var previous domain.HostingHealthy
	var previousPlan domain.HostingApplyPlan
	if op.ApplicationDatabase != nil {
		p := op.ApplicationDatabase
		if row.Family != "application" || row.ProofHash != p.Hash || !networkCaptureDocumentEqual(p.Binding, b) || domain.ValidateHostingApplicationDatabaseOperation(op) != nil {
			return ErrConflict
		}
		revision, members, retired, previous = p.MembershipRevision, p.Members, p.Retired, p.DatabaseHealthy
		previousPlan = p.DatabasePlan
		ref, e := lockHostingRuntimeConfiguration(ctx, tx, c.ProjectID, c.ApplicationResourceID, b.RuntimeConfiguration.Generation)
		if e != nil || ref != b.RuntimeConfiguration {
			return ErrConflict
		}
	} else {
		p := op.DatabaseMaintenance
		if row.Family != "maintenance" || row.ProofHash != p.Hash || domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil {
			return ErrConflict
		}
		revision, members, retired, previous = p.MembershipRevision, p.Members, p.Retired, p.PreviousHealthy
		previousPlan = p.PreviousPlan
	}
	if previousPlan.Snapshot.Database != b.Database || previousPlan.Snapshot.Storage != view.StorageRef {
		return ErrConflict
	}
	configurationHash, err := domain.HostingDatabaseConfigurationHash(previousPlan.Snapshot, view.Credentials)
	if err != nil || configurationHash != b.Database.ConfigurationHash {
		return ErrConflict
	}
	if !observe && !historical {
		head, e := q.LockDatabaseMembershipHead(ctx, b.DatabaseBindingID)
		var currentMembers []domain.HostingDatabaseNetworkMemberEvidence
		var ledger []domain.HostingDatabaseNetworkRetirementEvidence
		if e != nil || head.ConnectionID != c.ID || head.Revision != revision || !connectionJSONMatches(head.Members, members) || json.Unmarshal(head.Members, &currentMembers) != nil || json.Unmarshal(head.Retired, &ledger) != nil {
			return ErrConflict
		}
		transitions := connectionTransitionRetirements(previousPlan, currentMembers, ledger)
		proofs := make([]domain.HostingDatabaseNetworkRetirementEvidence, 0, len(transitions))
		for _, transition := range transitions {
			proofs = append(proofs, domain.HostingDatabaseNetworkRetirementEvidence{Attached: domain.HostingDatabaseNetworkMemberEvidence(transition.Attached), RetirePlan: transition.RetirePlan, RetireIntent: transition.RetireIntent, Detached: transition.Detached})
		}
		if !networkCaptureDocumentEqual(proofs, retired) {
			return ErrConflict
		}
		hold, e := q.LockDatabaseNetworkHold(ctx, b.DatabaseBindingID)
		if e != nil || hold.ConnectionID != c.ID || hold.OperationID == nil || *hold.OperationID != op.ID {
			return ErrConflict
		}
		raw, e := q.GetHostingHealthy(ctx, b.DatabaseBindingID)
		if e != nil {
			return e
		}
		var current domain.HostingHealthy
		if json.Unmarshal(raw, &current) != nil || !networkCaptureDocumentEqual(current, previous) {
			return ErrConflict
		}
	}
	// Owner expiration is evaluated after all potentially blocking metadata locks.
	return authorizeBuildActor(ctx, tx, actor, c.ProjectID, domain.RoleOwner)
}

func connectionJSONMatches(raw []byte, value any) bool {
	// Decode through the expected concrete type at callers when consuming proof.
	// Here both arrays have already been bounded and validated. Normalize map key
	// ordering because PostgreSQL jsonb does not retain the Go wire field order.
	var left, right any
	encoded, err := json.Marshal(value)
	a, b := json.NewDecoder(bytes.NewReader(raw)), json.NewDecoder(bytes.NewReader(encoded))
	a.UseNumber()
	b.UseNumber()
	if err != nil || a.Decode(&left) != nil || b.Decode(&right) != nil {
		return false
	}
	return networkCaptureDocumentEqual(left, right)
}

// Existing empty upgrades may serve the reserved initial child only. Once a
// membership head exists, a legacy empty proof cannot erase attached members.
func databaseConnectionLegacyDatabaseGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	q := sqlcgen.New(tx)
	present, err := q.DatabaseConnectionSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	c, err := q.ReadDatabaseConnectionByDatabase(ctx, op.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if op.ID != c.UpgradeOperationID || op.DatabaseUpgrade == nil || op.DatabaseUpgrade.CapturePlan.OperationID != c.CaptureID {
		return ErrConflict
	}
	_, err = q.LockDatabaseMembershipHead(ctx, op.BindingID)
	if !errors.Is(err, pgx.ErrNoRows) {
		if err != nil {
			return err
		}
		return ErrConflict
	}
	hold, err := q.LockDatabaseNetworkHold(ctx, op.BindingID)
	if err != nil || hold.ConnectionID != c.ID || hold.OperationID == nil || *hold.OperationID != op.ID {
		return ErrConflict
	}
	return requireConnectionAgent(ctx, tx, op.Snapshot.RunnerID, true)
}

func finalDatabaseConnectionAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation, observe, settled bool) error {
	if !connectionOperation(op) {
		return nil
	}
	if err := sqlcgen.New(tx).FlushDatabaseConnection(ctx); err != nil {
		return err
	}
	if err := databaseOperationAuthorityMode(ctx, tx, op, observe, settled); err != nil {
		return err
	}
	if err := hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, observe); err != nil {
		return err
	}
	if err := databaseOperationAuthorityMode(ctx, tx, op, observe, settled); err != nil {
		return err
	}
	if a.OperationID != op.ID || observe && a.RunID != op.ReconciliationRunID || !observe && a.RunID != op.RunID && !continuityFinalizeRun(op, a.RunID) || op.Snapshot.RunnerID != runner {
		return ErrConflict
	}
	if err := checkDatabaseConnectionRunnerContext(ctx, tx, runner); err != nil {
		return err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, a, !settled); err != nil {
		return err
	}
	// Wall time can advance while audit/receipt locks wait, even with row locks.
	live, err := sqlcgen.New(tx).HostingLeaseUnexpired(ctx, a.LeaseID)
	if err != nil {
		return err
	}
	if !live {
		return ErrConflict
	}
	return nil
}

func databaseConnectionHistoricalRunnerRead(ctx context.Context, tx pgx.Tx, runner string, op domain.HostingOperation) error {
	return databaseConnectionRunnerRead(ctx, tx, runner, op, false)
}

// Observation can inspect an original held attempt only through its single,
// explicit owner request. It cannot supply authority to any deployment effect.
func databaseConnectionObservationHold(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if !connectionOperation(op) || !op.ReconcileRequested || op.ReconciliationRunID != "observe_"+op.ID {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	requested, err := q.ConnectionObservationRequested(ctx, sqlcgen.ConnectionObservationRequestedParams{RunID: op.ReconciliationRunID, OperationID: op.ID})
	if err != nil {
		return err
	}
	if !requested {
		return ErrConflict
	}
	run, err := q.GetRun(ctx, op.ReconciliationRunID)
	if err != nil || run.Status != domain.RunQueued && run.Status != domain.RunRunning {
		return ErrConflict
	}
	var spec domain.RunSpec
	if json.Unmarshal(run.RunSpec, &spec) != nil || spec.HostingOperationID != op.ID || spec.HostingMode != "observe" || spec.Type != hostingSnapshotTaskType(op.Snapshot) {
		return ErrConflict
	}
	if run.Status == domain.RunRunning {
		lease, e := q.GetActiveLeaseForRun(ctx, run.ID)
		if e != nil || lease.RunnerID != op.Snapshot.RunnerID {
			return ErrConflict
		}
	}
	e, err := q.ReadDatabaseConnectionExecution(ctx, op.ID)
	if err != nil {
		return err
	}
	c, err := q.ReadDatabaseConnectionByID(ctx, e.ConnectionID)
	if err != nil {
		return err
	}
	b, err := readDatabaseBinding(ctx, q, c.ApplicationResourceID)
	if err != nil {
		return err
	}
	hold, err := q.LockDatabaseNetworkHold(ctx, b.DatabaseBindingID)
	if err != nil || hold.ConnectionID != c.ID || hold.OperationID == nil || *hold.OperationID != op.ID {
		return ErrConflict
	}
	return nil
}

func databaseConnectionRunnerRead(ctx context.Context, tx pgx.Tx, runner string, op domain.HostingOperation, observe bool) error {
	if !connectionOperation(op) {
		return nil
	}
	if err := databaseOperationAuthorityMode(ctx, tx, op, observe, true); err != nil {
		return err
	}
	if err := hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
		return err
	}
	if err := databaseOperationAuthorityMode(ctx, tx, op, observe, true); err != nil {
		return err
	}
	return checkDatabaseConnectionRunnerContext(ctx, tx, runner)
}

// Only the exact application credential injected by this immutable binding may
// cross the existing lease-bound runtime delivery path. Other DB-owned refs are
// never generalized into ordinary managed-secret access.
func connectionRuntimeSecretReferences(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, refs []domain.HostingRuntimeSecretReference) error {
	if op.ApplicationDatabase == nil {
		return ErrConflict
	}
	b := op.ApplicationDatabase.Binding
	q := sqlcgen.New(tx)
	found := false
	for _, ref := range refs {
		owner, err := q.ConnectionRuntimeSecretOwner(ctx, ref.SecretID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if owner.ResourceID != b.DatabaseResourceID || owner.Role != "app" || owner.Version != ref.Version || ref.SecretID != b.Credential.SecretID || ref.Version != b.Credential.Version {
			return ErrConflict
		}
		found = true
	}
	if !found {
		return ErrConflict
	}
	return nil
}
