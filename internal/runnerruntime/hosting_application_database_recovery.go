//go:build unix

package runnerruntime

import (
	"context"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// Recovery evidence is retained physical evidence, not current controller
// authority or an ACK. A missing create ID cannot be recovered by discovery.
type applicationDatabaseRecoveryEvidence struct {
	AttachPlan    domain.HostingDatabaseBindingEndpointPlan
	Configuration *applicationDatabaseConfiguration
	Started       *domain.HostingDatabaseBindingEndpointObservation
}

type applicationDatabaseRecoveryPhase string

const (
	applicationDatabaseRecoveryCreated    applicationDatabaseRecoveryPhase = "created"
	applicationDatabaseRecoveryStopped    applicationDatabaseRecoveryPhase = "stopped"
	applicationDatabaseRecoveryConfigured applicationDatabaseRecoveryPhase = "configured"
	applicationDatabaseRecoveryStarted    applicationDatabaseRecoveryPhase = "started_attached"
	applicationDatabaseRecoveryReady      applicationDatabaseRecoveryPhase = "ready"
	applicationDatabaseRecoveryAbsent     applicationDatabaseRecoveryPhase = "absent"
)

type applicationDatabaseRecoveryObservation struct {
	Phase     applicationDatabaseRecoveryPhase
	Candidate applicationDatabaseCandidate
	Endpoint  domain.HostingDatabaseBindingEndpointObservation
}

func (e *applicationDatabaseEngine) recoveryIDAbsent(ctx context.Context, id string) (bool, error) {
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	if err != nil || ctx.Err() != nil {
		return false, errApplicationDatabaseEngine
	}
	value := strings.TrimSpace(string(raw))
	if value != "" && value != id {
		return false, errApplicationDatabaseEngine
	}
	return value == "", nil
}

func (e *applicationDatabaseEngine) recoveryIdentity(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, proof applicationDatabaseRecoveryEvidence) error {
	if e.check(ctx, in) != nil || c.OperationID != in.Proof.OperationID || c.PlanHash != in.Plan.Hash || c.CreateNonce != in.Proof.CreateNonce || c.ImageID != in.Plan.Release.ImageID || c.DaemonID != in.Plan.Release.DaemonID || !dockerObjectID.MatchString(c.ContainerID) || !dockerObjectID.MatchString(c.DefaultNetworkID) || c.ContainerID == in.Proof.DatabaseHealthy.ContainerID || !applicationDatabaseAttachMatches(in, c, proof.AttachPlan) {
		return errApplicationDatabaseEngine
	}
	if in.Plan.PreviousHealthy != nil && c.ContainerID == in.Plan.PreviousHealthy.ContainerID {
		return errApplicationDatabaseEngine
	}
	for _, member := range in.Plan.ApplicationDatabase.PreviousMembers {
		if member.Endpoint.Target.ContainerID == c.ContainerID {
			return errApplicationDatabaseEngine
		}
	}
	if proof.Configuration != nil && *proof.Configuration != applicationDatabaseExpectedConfiguration(in, c, proof.AttachPlan) {
		return errApplicationDatabaseEngine
	}
	if proof.Started != nil && (proof.Configuration == nil || !proof.Started.Present || proof.Started.Endpoint.Target != proof.AttachPlan.Target || !dockerObjectID.MatchString(proof.Started.Endpoint.EndpointID)) {
		return errApplicationDatabaseEngine
	}
	bridge, err := e.bridge(ctx)
	if err != nil || bridge != c.DefaultNetworkID {
		return errApplicationDatabaseEngine
	}
	return nil
}

// ObserveCandidate never creates, adopts, or mutates a container. The phases
// describe physical facts only; ready does not imply that activation settled.
func (e *applicationDatabaseEngine) ObserveCandidate(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, proof applicationDatabaseRecoveryEvidence) (applicationDatabaseRecoveryObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var zero applicationDatabaseRecoveryObservation
	if e.recoveryIdentity(ctx, in, c, proof) != nil {
		return zero, errApplicationDatabaseEngine
	}
	result := applicationDatabaseRecoveryObservation{Candidate: c}
	// A failed inspect never proves absence. Both bounded inventories must
	// succeed, and the database/network proof must remain intact.
	absent, inventoryErr := e.recoveryIDAbsent(ctx, c.ContainerID)
	if inventoryErr != nil {
		return zero, errApplicationDatabaseEngine
	}
	if absent {
		if !e.nameAbsent(ctx, applicationDatabaseName(in)) || e.network(ctx, in, nil, "") != nil || ctx.Err() != nil {
			return zero, errApplicationDatabaseEngine
		}
		result.Phase = applicationDatabaseRecoveryAbsent
		return result, nil
	}
	actual, err := e.container(ctx, c.ContainerID)
	if err != nil {
		return zero, errApplicationDatabaseEngine
	}
	_, attached := actual.NetworkSettings.Networks[in.Proof.Binding.NetworkName]
	stopped := actual.State.Status == "exited"
	if e.inspectCandidateState(ctx, in, c, attached, actual.State.Running, stopped) != nil {
		return zero, errApplicationDatabaseEngine
	}
	switch {
	case actual.State.Running:
		if !attached || proof.Configuration == nil {
			return zero, errApplicationDatabaseEngine
		}
		result.Endpoint = domain.HostingDatabaseBindingEndpointObservation{Present: true, Endpoint: domain.HostingDatabaseBindingEndpoint{Target: proof.AttachPlan.Target, EndpointID: actual.NetworkSettings.Networks[in.Proof.Binding.NetworkName].EndpointID}}
		if proof.Started != nil && *proof.Started != result.Endpoint {
			return zero, errApplicationDatabaseEngine
		}
		result.Phase = applicationDatabaseRecoveryStarted
		// A bounded readiness sample may fail while identity remains valid. That
		// is started-attached evidence, never readiness or successful activation.
		probe, stop := context.WithTimeout(ctx, time.Second)
		ready := e.VerifyReady(probe, in, c, result.Endpoint) == nil
		stop()
		if ctx.Err() != nil || e.observeAttached(ctx, in, c, result.Endpoint) != nil {
			return zero, errApplicationDatabaseEngine
		}
		if ready {
			result.Phase = applicationDatabaseRecoveryReady
		}
	case stopped:
		if proof.Started != nil && !attached {
			return zero, errApplicationDatabaseEngine
		}
		result.Phase = applicationDatabaseRecoveryStopped
	case attached:
		if proof.Started != nil {
			return zero, errApplicationDatabaseEngine
		}
		result.Phase = applicationDatabaseRecoveryConfigured
	default:
		if proof.Configuration != nil || proof.Started != nil {
			return zero, errApplicationDatabaseEngine
		}
		result.Phase = applicationDatabaseRecoveryCreated
	}
	return result, nil
}

// RemoveCandidate cleans up only an unsettled exact candidate. Before each
// effect the caller must freshly prove it remains unsettled, is not the current
// healthy application, and that current authority permits cleanup. Historical
// receipts cannot implement that callback. This does not release database holds.
func (e *applicationDatabaseEngine) RemoveCandidate(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, proof applicationDatabaseRecoveryEvidence, beforeEffect func(context.Context) error) (applicationDatabaseRecoveryObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var zero applicationDatabaseRecoveryObservation
	if beforeEffect == nil {
		return zero, errApplicationDatabaseEngine
	}
	observed, err := e.ObserveCandidate(ctx, in, c, proof)
	if err != nil {
		return zero, errApplicationDatabaseEngine
	}
	if observed.Phase == applicationDatabaseRecoveryAbsent {
		return observed, nil
	}
	if observed.Phase == applicationDatabaseRecoveryStarted || observed.Phase == applicationDatabaseRecoveryReady {
		if e.effect(ctx, in, beforeEffect, "container", "stop", "--time", "10", c.ContainerID) != nil {
			return zero, errApplicationDatabaseEngine
		}
		observed, err = e.ObserveCandidate(ctx, in, c, proof)
		if err != nil || observed.Phase != applicationDatabaseRecoveryStopped {
			return zero, errApplicationDatabaseEngine
		}
	}
	// Removal of a stopped container removes its stopped network configuration.
	// There is no independent disconnect effect or window that adopts a new ID.
	if e.effect(ctx, in, beforeEffect, "container", "rm", c.ContainerID) != nil {
		return zero, errApplicationDatabaseEngine
	}
	observed, err = e.ObserveCandidate(ctx, in, c, proof)
	if err != nil || observed.Phase != applicationDatabaseRecoveryAbsent {
		return zero, errApplicationDatabaseEngine
	}
	return observed, nil
}
