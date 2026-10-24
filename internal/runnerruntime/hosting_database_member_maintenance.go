//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"path/filepath"
	"strconv"
	"time"

	"nerocd/internal/domain"
)

// This synchronous private adapter borrows complete immutable documents. It
// supplies neither controller admission nor authority to discover app members.
type databaseMemberMaintenanceInput struct {
	Plan, PreviousPlan domain.HostingApplyPlan
	Proof              domain.HostingDatabaseNetworkPlanInput
}

func (in databaseMemberMaintenanceInput) validate() error {
	if domain.ValidateHostingDatabasePlanV2(in.Plan) != nil || domain.ValidateHostingDatabasePlanV2(in.PreviousPlan) != nil || !in.Proof.Origin.InitReady || domain.ValidateHostingDatabasePlanTransition(in.Plan, in.PreviousPlan, in.Proof) != nil {
		return errors.New("invalid database member maintenance")
	}
	return nil
}

func newDatabaseMemberMaintenanceCredentials(in databaseMemberMaintenanceInput, values []DatabaseCredentialValue, useUntil time.Time) (*DatabaseCredentials, error) {
	if in.validate() != nil {
		return nil, errDatabaseSecret
	}
	return newValidatedDatabaseCredentials(in.Plan, values, useUntil)
}

// Only the validated member adapter constructs this inspection context. The
// ordinary and empty-upgrade paths retain their original attachment bounds.
type databaseMemberInspection struct {
	proofs  map[string]domain.HostingDatabaseNetworkMemberProof
	settled map[string]string
	known   map[string]bool
}

func (in databaseMemberMaintenanceInput) inspection() *databaseUpgradeInspection {
	m := &databaseMemberInspection{proofs: make(map[string]domain.HostingDatabaseNetworkMemberProof), settled: make(map[string]string)}
	for _, proof := range in.Proof.Members {
		m.proofs[proof.EndpointPlan.Target.ContainerID] = proof
	}
	return &databaseUpgradeInspection{Previous: in.PreviousPlan, Origin: in.Proof.Origin, members: m}
}

func (e *imageEngine) databaseMemberNetwork(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, known map[string]bool, in *databaseUpgradeInspection) (databaseNetwork, error) {
	m := in.members
	// Retain only the exact database IDs supplied by the single-writer sequence;
	// member IDs enter the census exclusively through authenticated full proofs.
	m.known = maps.Clone(known)
	allowed := maps.Clone(known)
	for id := range m.proofs {
		allowed[id] = true
	}
	n, err := e.inspectDatabaseNetworkBounded(ctx, plan.Database.NetworkName, record, allowed, len(m.proofs)+2)
	if err != nil {
		return n, err
	}
	app := &applicationDatabaseEngine{image: e, config: c}
	for _, member := range plan.Database.Network.Members {
		id := member.Endpoint.Target.ContainerID
		proof, ok := m.proofs[id]
		if !ok {
			return n, errors.New("database member full proof unavailable")
		}
		container, err := app.member(ctx, member, proof.Binding.ApplicationBindingID)
		endpoint, present := n.Containers[id]
		private := container.NetworkSettings.Networks[n.Name]
		bridge := container.NetworkSettings.Networks["bridge"]
		address, addressErr := databaseNetworkAddress(n, id)
		bridgeAddress, bridgeErr := netip.ParseAddr(bridge.IPAddress)
		snapshot := proof.ApplicationHealthy.Snapshot
		ports := container.HostConfig.PortBindings[strconv.Itoa(snapshot.ContainerPort)+"/tcp"]
		if err != nil || !present || !container.State.Running || container.State.Status != "running" || endpoint.EndpointID != member.Endpoint.EndpointID || private.EndpointID != endpoint.EndpointID || addressErr != nil || private.IPAddress != address || !dockerObjectID.MatchString(bridge.EndpointID) || bridgeErr != nil || !bridgeAddress.Is4() || !bridgeAddress.IsPrivate() || len(container.HostConfig.PortBindings) != 1 || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(snapshot.HostPort) {
			return n, errors.New("database member live endpoint identity changed")
		}
		digest := domain.HostingDocumentHash(container)
		if previous, ok := m.settled[id]; ok && previous != digest {
			return n, errors.New("database member changed during maintenance")
		}
		m.settled[id] = digest
	}
	for id, endpoint := range n.Containers {
		if _, member := m.proofs[id]; member {
			continue
		}
		p, op := plan, operation
		if id == plan.PreviousHealthy.ContainerID {
			p, op = in.Previous, plan.PreviousHealthy.OperationID
		}
		container, err := e.inspectDatabaseContainer(ctx, id)
		if err != nil || !container.State.Running || !databaseContainerMatchesWithInspection(container, op, p, record, filepath.Join(c.secretRoot, databaseGeneration(op, p)), in) {
			return n, errors.New("database member maintenance writer provenance changed")
		}
		configured := container.NetworkSettings.Networks[n.Name]
		address, err := databaseNetworkAddress(n, id)
		if err != nil || configured.EndpointID != endpoint.EndpointID || configured.IPAddress != address {
			return n, errors.New("database member maintenance writer endpoint changed")
		}
	}
	if e.verifyDaemon(ctx, plan.Release.DaemonID) != nil || ctx.Err() != nil {
		return n, errors.New("database member maintenance daemon continuity unavailable")
	}
	return n, nil
}

// ApplyDatabaseMemberMaintenance replaces one existing v2 writer while exact
// authenticated app endpoints remain attached. It never creates a network or
// volume. Failure retains uncertain evidence for the caller's recovery policy.
func (e *imageEngine) ApplyDatabaseMemberMaintenance(parent context.Context, operation string, in databaseMemberMaintenanceInput, volumeReq hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, c DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	p := in.Plan
	result := domain.HostingResult{Outcome: "failed", Uncertain: true, DaemonID: p.Release.DaemonID, PlanHash: p.Hash, ImageID: p.Release.ImageID}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	if in.validate() != nil || before == nil || operation == p.PreviousHealthy.OperationID || operation == in.Proof.Origin.OriginOperation || c.validateHost(ctx, e) != nil {
		return result
	}
	req, err := databaseValidatedVolumeRequest(operation, p, volumeReq, evidence)
	if err != nil || evidence.CreatedAt != in.Proof.Origin.VolumeCreatedAt {
		return result
	}
	inspection := in.inspection()
	check := func(ctx context.Context) error {
		record, err := inspection.record(c)
		if err != nil {
			return err
		}
		_, err = e.databaseNetworkForInspection(ctx, operation, p, record, c, inspection.members.known, inspection)
		return err
	}
	guarded := func(ctx context.Context) error {
		if check(ctx) != nil || databaseAuthorize(ctx, before) != nil || check(ctx) != nil {
			return errors.New("database member maintenance authority or endpoint unavailable")
		}
		return nil
	}
	err = credentials.use(ctx, p, func(ctx context.Context, values []DatabaseCredentialValue) error {
		lock, err := c.lock(ctx, p.Snapshot.BindingID)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		if _, err := inspection.record(c); err != nil || e.verifyLocalRelease(ctx, p.Release) != nil {
			return errors.New("database member maintenance origin or image unavailable")
		}
		return e.applyDatabase(ctx, operation, p, req, evidence, values, c, guarded, &result, inspection)
	})
	if err == nil && ctx.Err() == nil {
		result.Outcome, result.Ready, result.Uncertain = "succeeded", true, false
	}
	return result
}

// ObserveDatabaseMemberMaintenance inspects only the exact durable candidate.
// It performs no discovery, repairs, endpoint changes or writable probes.
func (e *imageEngine) ObserveDatabaseMemberMaintenance(ctx context.Context, operation domain.HostingOperation, in databaseMemberMaintenanceInput, credentials *DatabaseCredentials, c DatabaseEngineConfig) (domain.HostingObservation, error) {
	if in.validate() != nil || operation.Plan == nil || domain.HostingDocumentHash(*operation.Plan) != domain.HostingDocumentHash(in.Plan) || operation.ID == in.Proof.Origin.OriginOperation || operation.ID == in.Plan.PreviousHealthy.OperationID {
		return domain.HostingObservation{}, errors.New("invalid durable database member maintenance observation")
	}
	return e.observeDatabase(ctx, operation, credentials, c, in.inspection())
}
