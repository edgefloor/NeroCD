//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// This private input borrows immutable retained documents for a synchronous
// call. It is not an execution interface, capability or controller admission.
type databaseNetworkUpgradeInput struct {
	Plan, PreviousPlan domain.HostingApplyPlan
	Proof              domain.HostingDatabaseNetworkPlanInput
}

func (in databaseNetworkUpgradeInput) validate() error {
	p := in.Plan
	if domain.ValidateHostingDatabasePlanTransition(p, in.PreviousPlan, in.Proof) != nil || len(p.Database.Network.Members) != 0 || len(in.Proof.Members) != 0 || len(in.Proof.Retired) != 0 || !in.Proof.Origin.InitReady {
		return errors.New("invalid empty database network upgrade")
	}
	switch in.PreviousPlan.CompilerVersion {
	case domain.HostingDatabaseCompilerVersion:
		if domain.ValidateHostingDatabasePlan(in.PreviousPlan) != nil {
			return errors.New("invalid original database predecessor")
		}
	case domain.HostingDatabaseNetworkCompilerVersion:
		if domain.ValidateHostingDatabasePlanV2(in.PreviousPlan) != nil || len(in.PreviousPlan.Database.Network.Members) != 0 {
			return errors.New("database maintenance requires an empty predecessor")
		}
	default:
		return errors.New("unsupported original database predecessor")
	}
	return nil
}

func newDatabaseNetworkUpgradeCredentials(in databaseNetworkUpgradeInput, values []DatabaseCredentialValue, useUntil time.Time) (*DatabaseCredentials, error) {
	if in.validate() != nil {
		return nil, errDatabaseSecret
	}
	return newValidatedDatabaseCredentials(in.Plan, values, useUntil)
}

type databaseUpgradeInspection struct {
	Previous domain.HostingApplyPlan
	Origin   domain.HostingDatabaseNetworkOrigin
	members  *databaseMemberInspection
}

// record is existing-only. No fallback can create, reconstruct or update the
// initialized record, including when it disappears after an earlier check.
func (in *databaseUpgradeInspection) record(c DatabaseEngineConfig) (databaseNetworkRecord, error) {
	r, exists, err := c.readRecord(in.Previous.Database.NetworkName + ".json")
	if err != nil || !exists || !r.InitReady || domain.HostingDatabaseNetworkOrigin(r) != in.Origin {
		return databaseNetworkRecord{}, errors.New("database upgrade retained origin unavailable")
	}
	return r, nil
}

func databasePreviousInspectionPlan(p domain.HostingApplyPlan, in *databaseUpgradeInspection) domain.HostingApplyPlan {
	if in != nil {
		return in.Previous
	}
	h := p.PreviousHealthy
	return domain.HostingApplyPlan{Snapshot: h.Snapshot, Release: h.Release, Storage: &h.Storage.Plan, Database: p.Database, Hash: h.PlanHash}
}

type databaseUpgradeEndpoint struct {
	NetworkID, EndpointID, IPAddress, GlobalIPv6Address string
	Aliases, Links, DNSNames                            []string
	IPAMConfig                                          map[string]json.RawMessage
	DriverOpts                                          map[string]string
	GwPriority                                          int
}

func databaseUpgradeTopMatches(raw []byte, expectedPID int) bool {
	if expectedPID < 1 || len(raw) > 8192 {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 || len(lines) > 256 || !slices.Equal(strings.Fields(lines[0]), []string{"PID", "UID"}) {
		return false
	}
	seen, found := map[int]bool{}, false
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid < 1 || strconv.Itoa(pid) != fields[0] || seen[pid] || fields[1] != "999" {
			return false
		}
		seen[pid] = true
		found = found || pid == expectedPID
	}
	return found
}

func (e *imageEngine) databaseUpgradeProcessOwnership(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, container databaseContainer, in *databaseUpgradeInspection) error {
	if container.State.Pid < 1 || e.verifyDaemon(ctx, plan.Release.DaemonID) != nil {
		return errors.New("database upgrade process identity unavailable")
	}
	raw, err := e.ownedVolumeDocker(ctx, 8192, "container", "top", container.ID, "-eo", "pid,uid")
	if err != nil || !databaseUpgradeTopMatches(raw, container.State.Pid) {
		return errors.New("database upgrade process ownership mismatch")
	}
	current, err := e.inspectDatabaseContainer(ctx, container.ID)
	if err != nil || !current.State.Running || current.State.Pid != container.State.Pid || !databaseContainerMatchesWithInspection(current, operation, plan, record, filepath.Join(c.secretRoot, databaseGeneration(operation, plan)), in) || e.verifyDaemon(ctx, plan.Release.DaemonID) != nil || ctx.Err() != nil {
		return errors.New("database upgrade observed process restarted or changed")
	}
	if in.members != nil {
		_, err = e.databaseNetworkForInspection(ctx, operation, plan, record, c, databaseKnownContainers(plan, container.ID), in)
		return err
	}
	return nil
}

func databaseContainerMatchesWithInspection(container databaseContainer, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, secretPath string, in *databaseUpgradeInspection) bool {
	if !databaseContainerMatches(container, operation, plan, record, secretPath) {
		return false
	}
	if in == nil {
		return true
	}
	n := container.NetworkSettings.Networks[plan.Database.NetworkName]
	var aliases []string
	if plan.CompilerVersion == domain.HostingDatabaseNetworkCompilerVersion {
		if plan.Database.Network == nil || in.members == nil && len(plan.Database.Network.Members) != 0 || plan.Database.Network.Alias != domain.HostingDatabaseBindingAlias(plan.Snapshot.BindingID) {
			return false
		}
		aliases = []string{plan.Database.Network.Alias}
	} else if plan.CompilerVersion != domain.HostingDatabaseCompilerVersion || plan.Database.Network != nil {
		return false
	}
	dns := append([]string{strings.TrimPrefix(container.Name, "/")}, aliases...)
	dns = append(dns, container.ID[:12])
	if !slices.Equal(n.Aliases, aliases) || len(n.Links) != 0 || len(n.IPAMConfig) != 0 || len(n.DriverOpts) != 0 || n.GwPriority != 0 {
		return false
	}
	if !container.State.Running {
		if container.State.Pid != 0 || n.EndpointID != "" || n.IPAddress != "" {
			return false
		}
		switch container.State.Status {
		case "created":
			// Native Docker29 reports the exact declared alias before first
			// start, but no generated DNSNames or active network identity yet.
			return n.NetworkID == "" && (slices.Equal(n.DNSNames, dns) || plan.CompilerVersion == domain.HostingDatabaseNetworkCompilerVersion && len(n.DNSNames) == 0)
		case "exited":
			// A previously started/stopped writer retains its generated names.
			return slices.Equal(n.DNSNames, dns)
		default:
			return false
		}
	}
	address, err := netip.ParseAddr(n.IPAddress)
	return slices.Equal(n.DNSNames, dns) && n.NetworkID == record.NetworkID && dockerObjectID.MatchString(n.EndpointID) && err == nil && address.Is4() && address.IsPrivate()
}

func (e *imageEngine) databaseNetworkForInspection(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, c DatabaseEngineConfig, known map[string]bool, in *databaseUpgradeInspection) (databaseNetwork, error) {
	if in != nil {
		if current, err := in.record(c); err != nil || current != record {
			return databaseNetwork{}, errors.New("database upgrade retained origin changed")
		}
	}
	if in != nil && in.members != nil {
		return e.databaseMemberNetwork(ctx, operation, plan, record, c, known, in)
	}
	n, err := e.inspectDatabaseNetwork(ctx, plan.Database.NetworkName, record, known)
	if err != nil || in == nil {
		return n, err
	}
	for id, endpoint := range n.Containers {
		p, op := plan, operation
		if id == plan.PreviousHealthy.ContainerID {
			p, op = in.Previous, plan.PreviousHealthy.OperationID
		}
		container, err := e.inspectDatabaseContainer(ctx, id)
		if err != nil || !container.State.Running || !databaseContainerMatchesWithInspection(container, op, p, record, filepath.Join(c.secretRoot, databaseGeneration(op, p)), in) {
			return n, errors.New("database upgrade active endpoint provenance changed")
		}
		configured := container.NetworkSettings.Networks[n.Name]
		address, err := databaseNetworkAddress(n, id)
		if err != nil || configured.EndpointID != endpoint.EndpointID || configured.IPAddress != address {
			return n, errors.New("database upgrade endpoint identity changed")
		}
	}
	return n, nil
}

// ApplyDatabaseNetworkUpgrade is a private, unadvertised engine entry. It cannot
// create a volume or network and preserves the initialized record byte for byte.
// The caller must durably admit this exact operation and supply current authority.
func (e *imageEngine) ApplyDatabaseNetworkUpgrade(parent context.Context, operation string, in databaseNetworkUpgradeInput, volumeReq hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, c DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
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
	inspection := &databaseUpgradeInspection{Previous: in.PreviousPlan, Origin: in.Proof.Origin}
	guarded := func(ctx context.Context) error {
		if _, err := inspection.record(c); err != nil || databaseAuthorize(ctx, before) != nil {
			return errors.New("database upgrade current authority or origin unavailable")
		}
		_, err := inspection.record(c)
		return err
	}
	err = credentials.use(ctx, p, func(ctx context.Context, values []DatabaseCredentialValue) error {
		lock, err := c.lock(ctx, p.Snapshot.BindingID)
		if err != nil {
			return err
		}
		defer func() { _ = lock.Close() }()
		if _, err := inspection.record(c); err != nil || e.verifyLocalRelease(ctx, p.Release) != nil {
			return errors.New("database upgrade origin or image unavailable")
		}
		return e.applyDatabase(ctx, operation, p, req, evidence, values, c, guarded, &result, inspection)
	})
	if err == nil && ctx.Err() == nil {
		result.Outcome, result.Ready, result.Uncertain = "succeeded", true, false
	}
	return result
}

// ObserveDatabaseNetworkUpgrade requires the exact durable candidate result. It
// uses read-only probes and never discovers a candidate by adopting labels.
func (e *imageEngine) ObserveDatabaseNetworkUpgrade(ctx context.Context, operation domain.HostingOperation, in databaseNetworkUpgradeInput, credentials *DatabaseCredentials, c DatabaseEngineConfig) (domain.HostingObservation, error) {
	if in.validate() != nil || operation.Plan == nil || domain.HostingDocumentHash(*operation.Plan) != domain.HostingDocumentHash(in.Plan) || operation.ID == in.Proof.Origin.OriginOperation || operation.ID == in.Plan.PreviousHealthy.OperationID {
		return domain.HostingObservation{}, errors.New("invalid durable database upgrade observation")
	}
	return e.observeDatabase(ctx, operation, credentials, c, &databaseUpgradeInspection{Previous: in.PreviousPlan, Origin: in.Proof.Origin})
}
