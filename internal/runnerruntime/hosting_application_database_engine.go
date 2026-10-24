//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

var errApplicationDatabaseEngine = errors.New("application database engine rejected evidence or effect")
var errApplicationDatabaseCreateUncertain = errors.New("application database create outcome requires reconciliation")

const applicationDatabaseNonceLabel = "io.nerocd.application.create_nonce"

type applicationDatabaseEngineInput struct {
	Plan  domain.HostingApplyPlan
	Proof domain.HostingApplicationDatabasePlanInput
}

type applicationDatabaseCandidate struct {
	OperationID, PlanHash, CreateNonce, ContainerID, ImageID, DaemonID, DefaultNetworkID string
}

type applicationDatabaseRetirement struct {
	Endpoint         domain.HostingDatabaseBindingEndpointObservation
	ContainerID      string
	ContainerRemoved bool
}

// Configuration is stopped-container configuration evidence, never an observed
// endpoint. The coordinator must durably retain it before admitting start.
type applicationDatabaseConfiguration struct {
	OperationID        string `json:"operation_id"`
	PlanHash           string `json:"plan_hash"`
	CreateNonce        string `json:"create_nonce"`
	ContainerID        string `json:"container_id"`
	ImageID            string `json:"image_id"`
	DaemonID           string `json:"daemon_id"`
	DefaultNetworkID   string `json:"default_network_id"`
	PrivateNetworkID   string `json:"private_network_id"`
	PrivateNetworkName string `json:"private_network_name"`
	AttachPlanHash     string `json:"attach_plan_hash"`
	Hash               string `json:"hash"`
}

func applicationDatabaseExpectedConfiguration(in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan) applicationDatabaseConfiguration {
	v := applicationDatabaseConfiguration{OperationID: c.OperationID, PlanHash: c.PlanHash, CreateNonce: c.CreateNonce, ContainerID: c.ContainerID, ImageID: c.ImageID, DaemonID: c.DaemonID, DefaultNetworkID: c.DefaultNetworkID, PrivateNetworkID: in.Proof.Origin.NetworkID, PrivateNetworkName: in.Proof.Binding.NetworkName, AttachPlanHash: p.Hash}
	v.Hash = domain.HostingDocumentHash(v)
	return v
}

// This Unix-only adapter is not registered for claims or advertised capabilities.
// Callers own durable intents/ACKs and current lease checks; a callback is only
// their admitted-stage/fresh-authority boundary, never proof of a durable receipt.
type applicationDatabaseEngine struct {
	image  *imageEngine
	config DatabaseEngineConfig
	create func(context.Context, string, []string, string, map[string][]byte, func(context.Context) error) (string, error)
}

func newApplicationDatabaseEngine(endpoint string, config DatabaseEngineConfig) (*applicationDatabaseEngine, error) {
	if config.stateRoot == "" || config.secretRoot == "" {
		return nil, errApplicationDatabaseEngine
	}
	image, err := newImageEngine(endpoint)
	if err != nil {
		return nil, errApplicationDatabaseEngine
	}
	return &applicationDatabaseEngine{image: image, config: config, create: image.createWithAuthorizedRuntimeEnvironment}, nil
}

func (e *applicationDatabaseEngine) Close() error { return e.image.Close() }

func applicationDatabaseName(in applicationDatabaseEngineInput) string {
	return in.Plan.Snapshot.RuntimeNamespace + "_appdb_" + domain.HostingDocumentHash(struct{ Operation, Plan, Nonce string }{in.Proof.OperationID, in.Plan.Hash, in.Proof.CreateNonce})[7:31]
}

func (e *applicationDatabaseEngine) check(ctx context.Context, in applicationDatabaseEngineInput) error {
	// Historical v1 proof remains valid domain input, but cannot serve the
	// binding-derived hostname. Runtime effects require the settled v2 alias.
	if ctx.Err() != nil || e == nil || e.image == nil || domain.ValidateHostingApplicationDatabasePlan(in.Plan, in.Proof) != nil || domain.ValidateHostingDatabasePlanV2(in.Proof.DatabasePlan) != nil || e.config.validateHost(ctx, e.image) != nil || e.image.verifyDaemon(ctx, in.Plan.Release.DaemonID) != nil {
		return errApplicationDatabaseEngine
	}
	return nil
}

func (e *applicationDatabaseEngine) effect(ctx context.Context, in applicationDatabaseEngineInput, before func(context.Context) error, args ...string) error {
	if before == nil || e.image.verifyDaemon(ctx, in.Plan.Release.DaemonID) != nil || ctx.Err() != nil || before(ctx) != nil || ctx.Err() != nil {
		return errApplicationDatabaseEngine
	}
	if _, err := e.image.docker(ctx, args...); err != nil {
		return errApplicationDatabaseEngine
	}
	return nil
}

// A missing returned ID is still uncertain: Docker could have committed create.
// Never retry here, adopt a name, or manufacture a healthy record.
func (e *applicationDatabaseEngine) CreateStopped(ctx context.Context, in applicationDatabaseEngineInput, environment *hostingruntimeprotocol.Environment, deadline time.Time, beforeEffect func(context.Context) error) (applicationDatabaseCandidate, error) {
	c := applicationDatabaseCandidate{OperationID: in.Proof.OperationID, PlanHash: in.Plan.Hash, CreateNonce: in.Proof.CreateNonce, ImageID: in.Plan.Release.ImageID, DaemonID: in.Plan.Release.DaemonID}
	metadata, err := environment.Metadata()
	if err != nil || deadline.IsZero() || beforeEffect == nil || e == nil || e.create == nil || metadata.Authority.OperationID != c.OperationID || metadata.PlanHash != c.PlanHash || metadata.RuntimeConfiguration != in.Plan.Snapshot.RuntimeConfiguration {
		return c, errApplicationDatabaseEngine
	}
	if metadata.UseUntil.Before(deadline) {
		deadline = metadata.UseUntil
	}
	if maximum := time.Now().Add(time.Minute); maximum.Before(deadline) {
		deadline = maximum
	}
	work, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if e.check(work, in) != nil || len(in.Proof.Members) > 1 || len(in.Proof.Members) == 1 && in.Proof.MembershipRevision > math.MaxInt64-2 || e.image.verifyLocalRelease(work, in.Plan.Release) != nil || e.network(work, in, nil, "") != nil {
		return c, errApplicationDatabaseEngine
	}
	bridge, err := e.bridge(work)
	if err != nil || !e.nameAbsent(work, applicationDatabaseName(in)) {
		return c, errApplicationDatabaseEngine
	}
	c.DefaultNetworkID = bridge
	flags := []string{"--pull=never", "--name", applicationDatabaseName(in), "--restart", "no", "--publish", "127.0.0.1:" + strconv.Itoa(in.Plan.Snapshot.HostPort) + ":" + strconv.Itoa(in.Plan.Snapshot.ContainerPort)}
	for _, pair := range [][2]string{{imageBindingLabel, in.Plan.Snapshot.BindingID}, {imageOperationLabel, c.OperationID}, {imageReleaseLabel, in.Plan.Release.Hash}, {imagePlanLabel, c.PlanHash}, {applicationDatabaseNonceLabel, c.CreateNonce}} {
		flags = append(flags, "--label", pair[0]+"="+pair[1])
	}
	launched := false
	err = environment.WithValues(func(entries []hostingruntimeprotocol.Entry) error {
		values := make(map[string][]byte, len(entries))
		for _, entry := range entries {
			values[entry.Name] = entry.Value
		}
		id, createErr := e.create(work, c.DaemonID, flags, hostingReleaseImage(in.Plan.Release), values, func(ctx context.Context) error {
			if ctx.Err() != nil || beforeEffect(ctx) != nil || ctx.Err() != nil {
				return errApplicationDatabaseEngine
			}
			launched = true
			return nil
		})
		if dockerObjectID.MatchString(id) {
			c.ContainerID = id
		}
		return createErr
	})
	if launched && (err != nil || c.ContainerID == "") {
		return c, errApplicationDatabaseCreateUncertain
	}
	if err != nil {
		return c, errApplicationDatabaseEngine
	}
	if e.inspectCandidate(work, in, c, false, false) != nil {
		return c, errApplicationDatabaseCreateUncertain
	}
	return c, nil
}

func (e *applicationDatabaseEngine) InspectCandidate(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.inspectCandidate(ctx, in, c, false, false) == nil || e.inspectCandidate(ctx, in, c, true, false) == nil || e.inspectCandidate(ctx, in, c, true, true) == nil {
		return nil
	}
	return errApplicationDatabaseEngine
}

func applicationDatabaseAttachMatches(in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan) bool {
	t := p.Target
	return p.Validate(in.Proof.Binding) == nil && p.OperationID == in.Proof.OperationID && p.Action == "attach" && in.Proof.MembershipRevision < math.MaxInt64 && p.MembershipRevision == in.Proof.MembershipRevision+1 && t.ContainerID == c.ContainerID && t.ContainerOperationID == c.OperationID && t.ContainerPlanHash == c.PlanHash && t.ImageID == c.ImageID && t.ReleaseHash == in.Plan.Release.Hash && t.NetworkID == in.Proof.Origin.NetworkID && t.DaemonID == c.DaemonID
}

func (e *applicationDatabaseEngine) ConfigureStoppedNetwork(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan, beforeEffect func(context.Context) error) (applicationDatabaseConfiguration, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var zero applicationDatabaseConfiguration
	if !applicationDatabaseAttachMatches(in, c, p) || e.inspectCandidate(ctx, in, c, false, false) != nil || len(in.Proof.Members) >= 2 {
		return zero, errApplicationDatabaseEngine
	}
	if e.effect(ctx, in, beforeEffect, "network", "connect", p.Target.NetworkID, c.ContainerID) != nil || e.inspectCandidate(ctx, in, c, true, false) != nil {
		return zero, errApplicationDatabaseEngine
	}
	return applicationDatabaseExpectedConfiguration(in, c, p), nil
}

func (e *applicationDatabaseEngine) StartAndObserve(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, p domain.HostingDatabaseBindingEndpointPlan, configured applicationDatabaseConfiguration, beforeEffect func(context.Context) error) (domain.HostingDatabaseBindingEndpointObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var zero domain.HostingDatabaseBindingEndpointObservation
	if !applicationDatabaseAttachMatches(in, c, p) || configured != applicationDatabaseExpectedConfiguration(in, c, p) || e.inspectCandidate(ctx, in, c, true, false) != nil || e.effect(ctx, in, beforeEffect, "container", "start", c.ContainerID) != nil || e.inspectCandidate(ctx, in, c, true, true) != nil {
		return zero, errApplicationDatabaseEngine
	}
	container, err := e.container(ctx, c.ContainerID)
	if err != nil {
		return zero, errApplicationDatabaseEngine
	}
	return domain.HostingDatabaseBindingEndpointObservation{Endpoint: domain.HostingDatabaseBindingEndpoint{Target: p.Target, EndpointID: container.NetworkSettings.Networks[in.Proof.Binding.NetworkName].EndpointID}, Present: true}, nil
}

func (e *applicationDatabaseEngine) observeAttached(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, observed domain.HostingDatabaseBindingEndpointObservation, retired ...string) error {
	t := observed.Endpoint.Target
	if !observed.Present || !dockerObjectID.MatchString(observed.Endpoint.EndpointID) || t.ContainerID != c.ContainerID || t.ContainerOperationID != c.OperationID || t.ContainerPlanHash != c.PlanHash || t.ImageID != c.ImageID || t.ReleaseHash != in.Plan.Release.Hash || t.DaemonID != c.DaemonID || t.NetworkID != in.Proof.Origin.NetworkID || t.NetworkName != in.Proof.Binding.NetworkName || e.inspectCandidate(ctx, in, c, true, true, retired...) != nil {
		return errApplicationDatabaseEngine
	}
	actual, err := e.container(ctx, c.ContainerID)
	if err != nil || actual.NetworkSettings.Networks[t.NetworkName].EndpointID != observed.Endpoint.EndpointID {
		return errApplicationDatabaseEngine
	}
	return nil
}

// VerifyReady is read-only and pins the actual endpoint supplied by the caller.
// Supplying an observation does not itself prove a controller ACK.
func (e *applicationDatabaseEngine) VerifyReady(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, observed domain.HostingDatabaseBindingEndpointObservation) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.observeAttached(ctx, in, c, observed) != nil {
		return errApplicationDatabaseEngine
	}
	if e.image.readiness(ctx, in.Plan, c.OperationID, c.ContainerID, func(ctx context.Context) error { return e.observeAttached(ctx, in, c, observed) }) != nil {
		return errApplicationDatabaseEngine
	}
	return nil
}

// Retire never authorizes a DB/candidate target, and preserves detached evidence
// separately from removal. Any unconfirmed effect returns a sanitized error.
func (e *applicationDatabaseEngine) Retire(ctx context.Context, in applicationDatabaseEngineInput, candidate applicationDatabaseCandidate, observed domain.HostingDatabaseBindingEndpointObservation, p domain.HostingDatabaseBindingEndpointPlan, beforeEffect func(context.Context) error) (applicationDatabaseRetirement, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := applicationDatabaseRetirement{ContainerID: p.Target.ContainerID}
	if beforeEffect == nil || e.check(ctx, in) != nil || len(in.Proof.Members) != 1 || p.Validate(in.Proof.Binding) != nil || p.OperationID != in.Proof.OperationID || p.Action != "retire" || in.Proof.MembershipRevision > math.MaxInt64-2 || p.MembershipRevision != in.Proof.MembershipRevision+2 || p.Target.ContainerOperationID == in.Proof.OperationID || p.Target.ContainerID == in.Proof.DatabaseHealthy.ContainerID {
		return result, errApplicationDatabaseEngine
	}
	var member *domain.HostingDatabaseNetworkMember
	for i := range in.Plan.ApplicationDatabase.PreviousMembers {
		m := &in.Plan.ApplicationDatabase.PreviousMembers[i]
		if m.Endpoint.Target == p.Target && m.Endpoint.EndpointID == p.ExpectedEndpointID {
			member = m
		}
	}
	if member == nil || e.VerifyReady(ctx, in, candidate, observed) != nil {
		return result, errApplicationDatabaseEngine
	}
	old, err := e.member(ctx, *member, in.Proof.Binding.ApplicationBindingID)
	if err != nil {
		return result, errApplicationDatabaseEngine
	}
	if old.State.Running {
		if e.effect(ctx, in, beforeEffect, "container", "stop", "--time", "10", result.ContainerID) != nil {
			return result, errApplicationDatabaseEngine
		}
	}
	if e.stoppedMember(ctx, in, *member, candidate.DefaultNetworkID) != nil || e.observeAttached(ctx, in, candidate, observed, result.ContainerID) != nil || e.effect(ctx, in, beforeEffect, "network", "disconnect", p.Target.NetworkID, result.ContainerID) != nil {
		return result, errApplicationDatabaseEngine
	}
	old, err = e.container(ctx, result.ContainerID)
	bridge, bridgeErr := e.bridge(ctx)
	if err != nil || bridgeErr != nil || !e.memberIdentity(old, *member, in.Proof.Binding.ApplicationBindingID) || old.State.Running || len(old.NetworkSettings.Networks) != 1 || old.NetworkSettings.Networks["bridge"].NetworkID != bridge || old.NetworkSettings.Networks["bridge"].EndpointID != "" || e.observeAttached(ctx, in, candidate, observed, result.ContainerID) != nil {
		return result, errApplicationDatabaseEngine
	}
	result.Endpoint = domain.HostingDatabaseBindingEndpointObservation{Endpoint: member.Endpoint, Present: false}
	if e.effect(ctx, in, beforeEffect, "container", "rm", result.ContainerID) != nil || e.image.verifyDaemon(ctx, candidate.DaemonID) != nil || !e.idAbsent(ctx, result.ContainerID) {
		return result, errApplicationDatabaseEngine
	}
	result.ContainerRemoved = true
	return result, nil
}

func (e *applicationDatabaseEngine) nameAbsent(ctx context.Context, name string) bool {
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	return err == nil && strings.TrimSpace(string(raw)) == ""
}

func (e *applicationDatabaseEngine) idAbsent(ctx context.Context, id string) bool {
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "id="+id)
	return err == nil && strings.TrimSpace(string(raw)) == ""
}
