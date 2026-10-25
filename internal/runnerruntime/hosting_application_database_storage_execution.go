//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type applicationStorageExecutionEngine interface {
	bindStorage(domain.HostingOperation, domain.HostingStorageCreateAck) error
	StopStoragePredecessor(context.Context, applicationDatabaseEngineInput, func(context.Context) error) (string, error)
	StopStorageCandidate(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, func(context.Context) error) (string, error)
	ResumeStoragePredecessor(context.Context, applicationDatabaseEngineInput, applicationDatabaseCandidate, func(context.Context) error) (string, *domain.HostingDatabaseBindingEndpointObservation, error)
}

func (x *applicationDatabaseExchange) storageBefore(action string) func(context.Context) error {
	return func(ctx context.Context) error {
		if ctx.Err() != nil || x.s.Context().Err() != nil {
			return errLeaseAuthorityLost
		}
		state, err := x.c.HostingApplicationDatabaseState(ctx, x.a, x.op)
		if err != nil || state.StorageWriter == nil {
			return errApplicationDatabaseExecution
		}
		hash := ""
		if x.stage != nil {
			hash = x.stage.Hash
		}
		if state.Stage == nil && hash != "" || state.Stage != nil && state.Stage.Hash != hash {
			return errApplicationDatabaseExecution
		}
		id, err := newRuntimeDeliveryID()
		if err != nil {
			return err
		}
		grant, err := x.c.HostingApplicationDatabaseStorageAuthorize(ctx, domain.HostingApplicationDatabaseStorageEffectRequest{HostingAuthority: x.a, AuthorizationID: id, Action: action, StateHash: state.StorageWriter.Receipt.Hash, StageHash: hash}, x.op, x.s.GuardDeadline())
		if err != nil || !time.Now().Before(grant.UseUntil) || ctx.Err() != nil || x.s.Context().Err() != nil {
			return errApplicationDatabaseExecution
		}
		return nil
	}
}
func (x *applicationDatabaseExchange) storagePublish(phase, observation string, endpoint *domain.HostingDatabaseBindingEndpointObservation, recovery *domain.HostingApplicationDatabaseStorageRecoveryPlan) error {
	state, err := x.c.HostingApplicationDatabaseState(x.s.Context(), x.a, x.op)
	if err != nil {
		return err
	}
	previous := ""
	if state.StorageWriter != nil {
		previous = state.StorageWriter.Receipt.Hash
	}
	r := domain.HostingApplicationDatabaseStorageWriterReceipt{Version: domain.HostingApplicationDatabaseStorageVersion, ReceiptID: attemptMutationKey("appdb_storage", x.a.LeaseID, x.a.Attempt, phase), Phase: phase, PlanHash: x.in.Plan.Hash, StorageRef: x.in.Plan.Snapshot.Storage, PreviousStateHash: previous, PredecessorHash: domain.HostingDocumentHash(x.in.Plan.PreviousHealthy), ObservationHash: observation, PredecessorEndpoint: endpoint}
	if x.stage != nil {
		r.StageHash = x.stage.Hash
		r.CandidateHash = x.stage.Candidate.Hash
	}
	r.Hash = r.DocumentHash()
	req := domain.HostingApplicationDatabaseStorageWriterRequest{HostingAuthority: x.a, Receipt: r}
	entry := applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "application_storage_writer", r.ReceiptID)
	entry.Plan = x.op.Plan
	entry.ApplicationStorageWriter = &req
	entry.ApplicationStorageRecovery = recovery
	if _, err = appendApplicationDatabaseEntry(x.j, entry); err != nil {
		return err
	}
	var ack domain.HostingOperation
	if err = retryAttemptRequest(x.s, func(ctx context.Context) error {
		var e error
		ack, e = x.c.HostingApplicationDatabaseStorageWriter(ctx, req, x.op)
		return e
	}); err != nil {
		return err
	}
	x.op = ack
	return nil
}
func prepareApplicationStorage(x *applicationDatabaseExchange) error {
	concrete, ok := x.e.(*applicationDatabaseEngine)
	if !ok {
		return errApplicationDatabaseExecution
	}
	create := domain.HostingStorageCreateRequest{HostingAuthority: x.a, PlanHash: x.op.Plan.Hash, StorageRef: x.op.Snapshot.Storage, CreationNonce: x.op.Plan.Storage.CreationNonce}
	entry := applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "storage_create_intent", attemptMutationKey("appdb_storage", x.a.LeaseID, x.a.Attempt, "create"))
	entry.Plan = x.op.Plan
	entry.StorageRef = x.op.Snapshot.Storage
	entry.StorageCreate = &create
	if _, err := appendApplicationDatabaseEntry(x.j, entry); err != nil {
		return err
	}
	var ack domain.HostingStorageCreateAck
	if err := retryAttemptRequest(x.s, func(ctx context.Context) error {
		var e error
		ack, e = x.c.HostingStorageCreateBegin(ctx, create)
		return e
	}); err != nil {
		return err
	}
	volume, err := storageVolumeRequest(x.op, ack)
	if err != nil {
		return err
	}
	evidence, err := concrete.image.ensureOwnedVolumeWithAuthority(x.s.Context(), volume, func(ctx context.Context) error {
		current, e := x.c.HostingPlan(ctx, x.a)
		if e != nil || !applicationDatabaseSame(x.op, current) || current.State != "prepared" || current.Result != nil {
			return errApplicationDatabaseExecution
		}
		return ctx.Err()
	})
	volume.CreatePermitted = false
	if errors.Is(err, errHostingOwnedVolumeUncertain) {
		evidence, err = concrete.image.ensureOwnedVolume(x.s.Context(), volume)
	}
	if err != nil {
		return errApplicationDatabaseExecution
	}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: x.a, ReceiptID: attemptMutationKey("appdb_storage", x.a.LeaseID, x.a.Attempt, "materialized"), PlanHash: x.op.Plan.Hash, StorageRef: evidence.Storage, CreationOperationID: evidence.OriginOperationID, CreationPlanHash: evidence.OriginPlanHash, CreationNonce: evidence.CreationNonce, DaemonID: evidence.DaemonID, VolumeName: evidence.VolumeName, Driver: evidence.Driver, Scope: evidence.Scope, Labels: evidence.Labels, Options: evidence.Options}
	entry = applicationDatabaseEntry(x.s, x.a, x.lease, x.op, "storage_materialized", proof.ReceiptID)
	entry.Plan = x.op.Plan
	entry.StorageRef = x.op.Snapshot.Storage
	entry.StorageMaterialized = &proof
	entry.StorageMetadata = &ack.Storage
	if _, err = appendApplicationDatabaseEntry(x.j, entry); err != nil {
		return err
	}
	if err = retryAttemptRequest(x.s, func(ctx context.Context) error { _, e := x.c.HostingStorageMaterialized(ctx, proof); return e }); err != nil {
		return err
	}
	return concrete.bindStorage(x.op, ack)
}
func (x *applicationDatabaseExchange) stopStoragePredecessor() error {
	e, ok := x.e.(applicationStorageExecutionEngine)
	if !ok {
		return errApplicationDatabaseExecution
	}
	if err := x.storagePublish("stop_intent", "", nil, nil); err != nil {
		return err
	}
	observation, err := e.StopStoragePredecessor(x.s.Context(), x.in, x.storageBefore("stop_predecessor"))
	if err != nil {
		return err
	}
	return x.storagePublish("stopped", observation, nil, nil)
}
func (x *applicationDatabaseExchange) storageFailed(candidate applicationDatabaseCandidate, drain func(context.Context) error) error {
	if x.op.ApplicationDatabaseStorage == nil {
		return errApplicationDatabaseExecution
	}
	e, ok := x.e.(applicationStorageExecutionEngine)
	if !ok || x.stage == nil {
		return errApplicationDatabaseExecution
	}
	observation, err := e.StopStorageCandidate(x.s.Context(), x.in, candidate, x.storageBefore("stop_candidate"))
	if err != nil {
		return err
	}
	if err = x.storagePublish("candidate_stopped", observation, nil, nil); err != nil {
		return err
	}
	if drain != nil {
		if err = drain(x.s.Context()); err != nil {
			return err
		}
	}
	if err = x.storagePublish("replacement_failed", observation, nil, nil); err != nil {
		return err
	}
	return x.j.AckHostingConnection(x.a)
}
func executeApplicationStorageRecovery(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, c runnerclient.Client, e applicationDatabaseExecutionEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation) error {
	in, err := applicationDatabaseInput(op)
	if err != nil {
		return err
	}
	state, err := c.HostingApplicationDatabaseState(s.Context(), a, op)
	if err != nil || state.Stage == nil || state.Storage == nil || state.StorageWriter == nil || state.StorageWriter.Receipt.Phase != "replacement_failed" || state.StorageRecovery == nil || state.StorageRecovery.Action != "resume_predecessor" || state.StorageRecovery.RunID != a.RunID || state.StorageRecovery.StorageStateHash != state.StorageWriter.Receipt.Hash {
		return errApplicationDatabaseExecution
	}
	engine, ok := e.(applicationStorageExecutionEngine)
	if !ok || engine.bindStorage(op, *state.Storage) != nil {
		return errApplicationDatabaseExecution
	}
	entry := applicationDatabaseEntry(s, a, lease, op, "application_storage_recovery", attemptMutationKey("appdb_storage", a.LeaseID, a.Attempt, "recovery"))
	entry.Plan = op.Plan
	entry.ApplicationStorageRecovery = state.StorageRecovery
	if _, err = appendApplicationDatabaseEntry(j, entry); err != nil {
		return err
	}
	x := applicationDatabaseExchange{s: s, j: j, c: c, e: e, a: a, lease: lease, op: op, in: in, stage: state.Stage}
	if err = x.storagePublish("resume_intent", "", nil, state.StorageRecovery); err != nil {
		return err
	}
	observation, endpoint, err := engine.ResumeStoragePredecessor(s.Context(), in, privateApplicationDatabaseCandidate(state.Stage.Candidate), x.storageBefore("resume_predecessor"))
	if err != nil {
		return err
	}
	if drain != nil {
		if err = drain(s.Context()); err != nil {
			return err
		}
	}
	if err = x.storagePublish("resumed", observation, endpoint, state.StorageRecovery); err != nil {
		return err
	}
	return j.AckHostingConnection(a)
}

func effectiveApplicationMemberEndpoint(m domain.HostingDatabaseNetworkMemberProof) string {
	endpoint, _, _, ok := domain.HostingDatabaseMemberEndpoint(m)
	if !ok {
		return ""
	}
	return endpoint.EndpointID
}
