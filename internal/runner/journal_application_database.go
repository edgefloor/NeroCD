package runner

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"nerocd/internal/domain"
)

var errJournalConnection = errors.New("invalid application database connection journal")

func hostingJournalUsesRouteContinuity(e JournalHosting) bool {
	return e.ApplicationRouteContinuity != nil || e.ApplicationYield != nil || e.ApplicationFinalize != nil
}
func journalStateUsesRouteContinuity(s journalState) bool {
	for _, e := range s.Hosting {
		if hostingJournalUsesRouteContinuity(e) {
			return true
		}
	}
	return false
}
func hostingJournalUsesApplicationDatabase(e JournalHosting) bool {
	return hostingJournalUsesRouteContinuity(e) || e.ApplicationDatabase != nil || e.ApplicationSnapshot != nil || e.ApplicationCandidate != nil || e.ApplicationStage != nil || e.ApplicationRetirementPlan != nil || e.ApplicationRetirement != nil || e.Plan != nil && e.Plan.ApplicationDatabase != nil
}
func hostingJournalUsesConnection(e JournalHosting) bool {
	return hostingJournalUsesApplicationDatabase(e) || e.DatabaseMaintenance != nil
}
func journalStateUsesConnection(state journalState) bool {
	for _, e := range state.Hosting {
		if hostingJournalUsesConnection(e) {
			return true
		}
	}
	return false
}

func validateDatabaseMaintenanceJournal(e JournalHosting) error {
	p := e.DatabaseMaintenance
	if p == nil || p.Validate() != nil || e.DatabaseUpgrade != nil || hostingJournalUsesApplicationDatabase(e) || p.PreviousPlan.Snapshot.Database != e.DatabaseRef || p.PreviousPlan.Snapshot.Storage != e.StorageRef {
		return errJournalConnection
	}
	snapshot := p.PreviousPlan.Snapshot
	if e.Plan != nil {
		snapshot = e.Plan.Snapshot
	}
	op := domain.HostingOperation{ID: e.OperationID, ProjectID: p.CapturePlan.ProjectID, ResourceID: p.CapturePlan.ResourceID, BindingID: p.CapturePlan.BindingID, Snapshot: snapshot, DatabaseMaintenance: p, Plan: e.Plan}
	if e.Plan != nil {
		op.Release = &e.Plan.Release
	}
	if domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil || e.Prepared != nil && e.Prepared.Release != p.PreviousPlan.Release {
		return errJournalConnection
	}
	return nil
}

func validateApplicationDatabaseJournal(e JournalHosting) error {
	if e.ApplicationDatabase == nil || e.ApplicationSnapshot == nil || e.ApplicationDatabase.Validate() != nil || e.DatabaseMaintenance != nil || e.DatabaseUpgrade != nil || e.Database || e.DatabaseRef != (domain.HostingDatabaseExecutionRef{}) || HostingJournalUsesStorage(e) && e.ApplicationStorage == nil || e.LocalBuild || HostingJournalUsesLocalBuild(e) || !e.RuntimeEnvironment || e.RuntimeConfiguration != e.ApplicationSnapshot.RuntimeConfiguration || e.CleanupPlan != nil || e.ID == "" || len(e.ID) > 128 || e.OperationID == "" || len(e.OperationID) > 128 || e.CreatedAt.IsZero() || validateAttemptIdentity(e.Attempt) != nil {
		return errJournalConnection
	}
	proof := e.ApplicationDatabase
	op := domain.HostingOperation{ID: e.OperationID, ProjectID: proof.Binding.ProjectID, ResourceID: e.ApplicationSnapshot.ResourceID, BindingID: e.ApplicationSnapshot.BindingID, RunID: "run_" + e.OperationID, Snapshot: *e.ApplicationSnapshot, ApplicationDatabase: proof, ApplicationDatabaseStorage: e.ApplicationStorage, Plan: e.Plan, ApplicationDatabaseRouteContinuity: e.ApplicationRouteContinuity}
	if e.Plan != nil {
		op.Release = &e.Plan.Release
		op.ApplicationDatabaseYield = e.ApplicationFinalize
	}
	if domain.ValidateHostingApplicationDatabaseOperation(op) != nil || e.ApplicationSnapshot.RuntimeConfiguration == (domain.HostingRuntimeConfigurationRef{}) || e.ApplicationSnapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		return errJournalConnection
	}
	in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*proof)
	if err != nil || e.ApplicationStorage == nil && (e.StorageCreate != nil || e.StorageMaterialized != nil || e.StorageMetadata != nil) {
		return errJournalConnection
	}
	a := domain.HostingAuthority{OperationID: e.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
	if e.ApplicationFinalize != nil && (e.ApplicationFinalize.Validate() != nil || e.ApplicationRouteContinuity == nil || a.RunID != e.ApplicationRouteContinuity.FinalizeRunID || e.ApplicationFinalize.ContinuationHash != e.ApplicationRouteContinuity.Hash || e.ApplicationFinalize.Version != domain.HostingApplicationDatabaseRouteContinuityVersion || e.Phase != "application_finalize" && e.Phase != "application_retirement" && e.Phase != "result") {
		return errJournalConnection
	}
	if e.ApplicationYield != nil && e.Phase != "application_yield" {
		return errJournalConnection
	}
	if e.ApplicationStorage != nil {
		if err := validateApplicationStorageJournal(e, a); err != nil {
			return err
		}
		if e.Phase == "storage_create_intent" || e.Phase == "storage_materialized" || e.Phase == "application_storage_writer" || e.Phase == "application_storage_recovery" {
			return nil
		}
	} else if e.ApplicationStorageWriter != nil || e.ApplicationStorageRecovery != nil {
		return errJournalConnection
	}
	if e.Phase != "apply_intent" && e.DeliveryID != "" || e.Phase != "application_candidate" && e.ApplicationCandidate != nil || e.Phase != "application_stage" && e.Phase != "application_retirement" && e.Phase != "application_yield" && e.Phase != "application_finalize" && e.ApplicationStage != nil || e.Phase != "application_retirement" && (e.ApplicationRetirementPlan != nil || e.ApplicationRetirement != nil) {
		return errJournalConnection
	}
	switch e.Phase {
	case "application_yield":
		if e.ApplicationYield == nil || e.Plan == nil || e.ApplicationStage == nil || e.ApplicationYield.HostingAuthority != a || e.ApplicationYield.ReceiptID != e.ID || e.ApplicationYield.ValidateStage(op, *e.ApplicationStage) != nil || e.Prepared != nil || e.Result != nil || e.ApplicationFinalize != nil {
			return errJournalConnection
		}
	case "application_finalize":
		if e.ApplicationFinalize == nil || e.Plan == nil || e.ApplicationStage == nil || e.ApplicationStage.Phase != "ready" || e.ApplicationStage.Hash != e.ApplicationFinalize.StageHash || e.ApplicationStage.Candidate.DocumentHash() != e.ApplicationFinalize.CandidateHash || e.ApplicationStage.Validate(*e.Plan, in) != nil || e.Prepared != nil || e.Result != nil || e.ApplicationYield != nil {
			return errJournalConnection
		}
	case "prepared":
		if e.Plan != nil || e.Result != nil || e.Prepared == nil || e.Prepared.ReceiptID != e.ID || e.Prepared.HostingAuthority != a || domain.ValidateHostingSnapshotRelease(*e.ApplicationSnapshot, e.Prepared.Release) != nil {
			return errJournalConnection
		}
	case "apply_intent":
		id, err := hex.DecodeString(e.DeliveryID)
		if e.Plan == nil || e.Prepared != nil || e.Result != nil || err != nil || len(id) != 16 || hex.EncodeToString(id) != e.DeliveryID {
			return errJournalConnection
		}
	case "application_candidate":
		if e.Plan == nil || e.Prepared != nil || e.Result != nil || e.ApplicationCandidate == nil || e.ApplicationCandidate.Validate(*e.Plan, in) != nil {
			return errJournalConnection
		}
	case "application_stage", "application_retirement":
		if e.Plan == nil || e.Prepared != nil || e.Result != nil || e.ApplicationStage == nil || e.ApplicationStage.Validate(*e.Plan, in) != nil || e.Phase == "application_stage" && e.ApplicationStage.ReceiptID != e.ID {
			return errJournalConnection
		}
		if e.Phase == "application_retirement" {
			p, r := e.ApplicationRetirementPlan, e.ApplicationRetirement
			if e.ApplicationStage.Phase != "ready" || len(in.Members) != 1 || p == nil || r == nil || p.Validate(in.Binding) != nil || p.OperationID != e.OperationID || p.Action != "retire" || p.Target != in.Members[0].EndpointPlan.Target || p.ExpectedEndpointID != effectiveApplicationMemberEndpoint(in.Members[0]) || p.MembershipRevision != in.MembershipRevision+2 || r.Validate(*p, in.Binding) != nil {
				return errJournalConnection
			}
		}
	case "result":
		if e.Plan != nil || e.Prepared != nil || e.Result == nil || e.Result.ReceiptID != e.ID || e.Result.HostingAuthority != a || e.Result.Result.Outcome == "succeeded" && (!e.Result.Result.Ready || e.Result.Result.Uncertain || e.Result.Result.Restored) {
			return errJournalConnection
		}
	default:
		return errJournalConnection
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > domain.HostingApplicationDatabaseJournalJSONLimit {
		return errJournalConnection
	}
	return nil
}

func validateApplicationDatabaseJournalChain(entries []JournalHosting) error {
	type chain struct {
		proof, snapshot, maintenance, plan, delivery, previous string
		continuity, finalize                                   string
		storage                                                string
		writer                                                 *domain.HostingApplicationDatabaseStorageWriterRequest
		candidate                                              *domain.HostingApplicationDatabaseCandidate
		stage                                                  *domain.HostingApplicationDatabaseStage
		retirement                                             *domain.HostingDatabaseBindingEndpointReceipt
	}
	chains := map[domain.HostingAuthority]chain{}
	operations := map[string]domain.HostingAuthority{}
	connections := map[string]bool{}
	for _, e := range entries {
		a := domain.HostingAuthority{OperationID: e.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
		if previous, exists := operations[e.OperationID]; exists && previous != a && (hostingJournalUsesConnection(e) || connections[e.OperationID]) {
			return errJournalConnection
		}
		operations[e.OperationID] = a
		connections[e.OperationID] = connections[e.OperationID] || hostingJournalUsesConnection(e)
		c, exists := chains[a]
		proof, snapshot, maintenance := "", "", ""
		if e.ApplicationDatabase != nil {
			proof = e.ApplicationDatabase.Hash
		}
		if e.ApplicationSnapshot != nil {
			snapshot = domain.HostingDocumentHash(*e.ApplicationSnapshot)
		}
		if e.DatabaseMaintenance != nil {
			maintenance = e.DatabaseMaintenance.Hash
		}
		if exists && (c.proof != proof || c.snapshot != snapshot || c.maintenance != maintenance) {
			return errJournalConnection
		}
		if exists && (c.continuity != domain.HostingDocumentHash(e.ApplicationRouteContinuity) || c.finalize != domain.HostingDocumentHash(e.ApplicationFinalize)) {
			return errJournalConnection
		}
		if exists && c.storage != domain.HostingDocumentHash(e.ApplicationStorage) {
			return errJournalConnection
		}
		c.storage = domain.HostingDocumentHash(e.ApplicationStorage)
		c.continuity, c.finalize = domain.HostingDocumentHash(e.ApplicationRouteContinuity), domain.HostingDocumentHash(e.ApplicationFinalize)
		c.proof, c.snapshot, c.maintenance = proof, snapshot, maintenance
		if proof != "" || maintenance != "" {
			// Both connection families retain one compiled plan and original
			// delivery identity across every phase and response-loss replay.
			if e.Plan != nil {
				if c.plan != "" && c.plan != e.Plan.Hash {
					return errJournalConnection
				}
				c.plan = e.Plan.Hash
				if e.Plan.PreviousHealthy != nil {
					c.previous = e.Plan.PreviousHealthy.ContainerID
				}
			}
			if e.Phase == "apply_intent" {
				if c.delivery != "" && c.delivery != e.DeliveryID {
					return errJournalConnection
				}
				c.delivery = e.DeliveryID
			}
		}
		if proof != "" {
			if validateApplicationDatabaseJournal(e) != nil {
				return errJournalConnection
			}
			in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*e.ApplicationDatabase)
			if err != nil {
				return errJournalConnection
			}
			if e.ApplicationStorage != nil && e.ApplicationCandidate != nil && (c.writer == nil || c.writer.Receipt.Phase != "stopped") {
				return errJournalConnection
			}
			if e.ApplicationStorageWriter != nil {
				w := e.ApplicationStorageWriter
				if c.writer == nil {
					if w.Receipt.Phase != "stop_intent" && !(e.ApplicationStorageRecovery != nil && w.Receipt.Phase == "resume_intent") {
						return errJournalConnection
					}
				} else if w.Receipt.PreviousStateHash != c.writer.Receipt.Hash || !applicationStorageJournalWriterPhase(c.writer.Receipt.Phase, w.Receipt.Phase) {
					return errJournalConnection
				}
				c.writer = w
			}
			if e.Phase == "application_finalize" {
				if exists {
					return errJournalConnection
				}
				c.stage = e.ApplicationStage
				c.candidate = &e.ApplicationStage.Candidate
			}
			if e.Phase == "application_yield" && (c.stage == nil || c.stage.Phase != "ready" || c.stage.Hash != e.ApplicationStage.Hash || c.retirement != nil) {
				return errJournalConnection
			}
			if e.ApplicationCandidate != nil {
				if c.delivery == "" || c.candidate != nil && *c.candidate != *e.ApplicationCandidate {
					return errJournalConnection
				}
				c.candidate = e.ApplicationCandidate
			}
			if e.Phase == "application_stage" {
				if c.candidate == nil || *c.candidate != e.ApplicationStage.Candidate || e.ApplicationStage.ValidateTransition(c.stage, *e.Plan, in) != nil {
					return errJournalConnection
				}
				c.stage = e.ApplicationStage
			}
			if e.Phase == "application_retirement" {
				if c.stage == nil || c.stage.Phase != "ready" || c.stage.Hash != e.ApplicationStage.Hash || e.ApplicationRetirement.ValidateTransition(c.retirement, *e.ApplicationRetirementPlan, in.Binding) != nil {
					return errJournalConnection
				}
				c.retirement = e.ApplicationRetirement
			}
			if e.Result != nil && e.Result.Result.Ready {
				r := e.Result.Result
				if c.stage == nil || c.stage.Phase != "ready" || c.candidate == nil || r.Outcome != "succeeded" || r.Uncertain || r.Restored || r.PlanHash != c.candidate.PlanHash || r.ContainerID != c.candidate.ContainerID || r.ImageID != c.candidate.ImageID || r.DaemonID != c.candidate.DaemonID || r.PreviousContainerID != c.previous || len(in.Members) == 1 && (c.retirement == nil || c.retirement.Phase != "detached") {
					return errJournalConnection
				}
			}
		}
		chains[a] = c
	}
	return nil
}

// AckHostingConnection removes one completely settled connection exchange in a
// single rewrite. Callers must first verify the exact controller completion ACK.
func (j *AttemptJournal) AckHostingConnection(a domain.HostingAuthority) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	previous := j.state.Hosting
	j.state.Hosting = slices.DeleteFunc(slices.Clone(previous), func(e JournalHosting) bool {
		return e.OperationID == a.OperationID && e.Attempt.RunID == a.RunID && e.Attempt.LeaseID == a.LeaseID && e.Attempt.Attempt == a.Attempt && e.Attempt.Fence == a.Fence
	})
	if err := j.persistLocked(); err != nil {
		j.state.Hosting = previous
		return err
	}
	return nil
}

func journalStateUsesApplicationStorage(s journalState) bool {
	for _, e := range s.Hosting {
		if e.ApplicationStorage != nil {
			return true
		}
	}
	return false
}
func validateApplicationStorageJournal(e JournalHosting, a domain.HostingAuthority) error {
	if e.ApplicationSnapshot == nil || e.ApplicationStorage.Validate(*e.ApplicationSnapshot) != nil {
		return errJournalConnection
	}
	if e.ApplicationStorageWriter != nil && e.Phase != "application_storage_writer" || e.ApplicationStorageRecovery != nil && e.ApplicationStorageRecovery.Validate() != nil {
		return errJournalConnection
	}
	switch e.Phase {
	case "storage_create_intent":
		if e.Plan == nil || e.StorageRef != e.Plan.Snapshot.Storage || e.StorageCreate == nil || e.StorageCreate.HostingAuthority != a || e.StorageCreate.PlanHash != e.Plan.Hash || e.StorageCreate.StorageRef != e.StorageRef || e.StorageCreate.CreationNonce != e.Plan.Storage.CreationNonce || e.StorageMaterialized != nil || e.StorageMetadata != nil || e.DeliveryID != "" || e.Result != nil || e.Prepared != nil {
			return errJournalConnection
		}
	case "storage_materialized":
		if e.Plan == nil || e.StorageMetadata == nil || e.StorageMaterialized == nil || e.StorageMaterialized.HostingAuthority != a || e.StorageMaterialized.PlanHash != e.Plan.Hash || e.StorageMaterialized.ReceiptID != e.ID || e.StorageMaterialized.Validate() != nil || e.StorageMetadata.Reference() != e.Plan.Snapshot.Storage || e.DeliveryID != "" || e.Result != nil || e.Prepared != nil {
			return errJournalConnection
		}
	case "application_storage_writer":
		if e.Plan == nil || e.ApplicationStorageWriter == nil || e.ApplicationStorageWriter.Validate() != nil || e.ApplicationStorageWriter.HostingAuthority != a || e.ApplicationStorageWriter.Receipt.ReceiptID != e.ID || e.ApplicationStorageWriter.Receipt.PlanHash != e.Plan.Hash || e.ApplicationStorageWriter.Receipt.StorageRef != e.Plan.Snapshot.Storage || e.Plan.PreviousHealthy == nil || e.ApplicationStorageWriter.Receipt.PredecessorHash != domain.HostingDocumentHash(e.Plan.PreviousHealthy) || e.DeliveryID != "" || e.Result != nil || e.Prepared != nil {
			return errJournalConnection
		}
	case "application_storage_recovery":
		if e.Plan == nil || e.ApplicationStorageRecovery == nil || e.ApplicationStorageRecovery.RunID != a.RunID || a.RunID != "storage_recover_"+e.OperationID || e.DeliveryID != "" || e.Result != nil || e.Prepared != nil {
			return errJournalConnection
		}
	default:
		if e.StorageCreate != nil || e.StorageMaterialized != nil || e.StorageMetadata != nil || e.StorageRef != (domain.HostingStorageRef{}) {
			return errJournalConnection
		}
	}
	return nil
}

func effectiveApplicationMemberEndpoint(m domain.HostingDatabaseNetworkMemberProof) string {
	endpoint, _, _, ok := domain.HostingDatabaseMemberEndpoint(m)
	if !ok {
		return ""
	}
	return endpoint.EndpointID
}

func applicationStorageJournalWriterPhase(previous, next string) bool {
	switch previous {
	case "stop_intent":
		return next == "stopped"
	case "stopped":
		return next == "candidate_stopped"
	case "candidate_stopped":
		return next == "replacement_failed"
	case "resume_intent":
		return next == "resumed"
	default:
		return false
	}
}
