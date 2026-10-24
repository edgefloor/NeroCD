//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"nerocd/internal/domain"
)

type applicationDatabaseContainer struct {
	ID, Name, Image string
	Config          struct{ Labels map[string]string }
	State           struct {
		Running, Paused, Restarting, Dead bool
		Status                            string
	}
	HostConfig struct {
		Privileged, PublishAllPorts, AutoRemove, ReadonlyRootfs                                                   bool
		NetworkMode, PidMode, IpcMode, UsernsMode, UTSMode, CgroupnsMode, Runtime                                 string
		Binds, VolumesFrom, CapAdd, CapDrop, GroupAdd, SecurityOpt, ExtraHosts, Links, DNS, DNSSearch, DNSOptions []string
		Devices, DeviceRequests, Mounts                                                                           []json.RawMessage
		Sysctls                                                                                                   map[string]string
		PortBindings                                                                                              map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
		RestartPolicy struct {
			Name              string
			MaximumRetryCount int
		}
	}
	Mounts          []json.RawMessage
	NetworkSettings struct {
		Networks map[string]applicationDatabaseEndpoint
	}
}

type applicationDatabaseEndpoint struct {
	NetworkID, EndpointID, IPAddress, GlobalIPv6Address string
	Aliases, Links, DNSNames                            []string
	IPAMConfig                                          map[string]json.RawMessage
	DriverOpts                                          map[string]string
	GwPriority                                          int
}

func applicationDatabaseEndpointConfig(n applicationDatabaseEndpoint, name, id string, private bool) bool {
	if len(n.Aliases) != 0 || len(n.Links) != 0 || len(n.IPAMConfig) != 0 || len(n.DriverOpts) != 0 || n.GwPriority != 0 || n.GlobalIPv6Address != "" {
		return false
	}
	if private {
		return slices.Equal(n.DNSNames, []string{strings.TrimPrefix(name, "/"), id[:12]})
	}
	return len(n.DNSNames) == 0
}

func (e *applicationDatabaseEngine) container(ctx context.Context, id string) (applicationDatabaseContainer, error) {
	var c applicationDatabaseContainer
	if !dockerObjectID.MatchString(id) {
		return c, errApplicationDatabaseEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 256<<10, "container", "inspect", id, "--format", "{{json .}}")
	defer clear(raw) // Raw Docker inspection may contain Env; never retain or log it.
	if err != nil || json.Unmarshal(raw, &c) != nil || c.ID != id {
		return c, errApplicationDatabaseEngine
	}
	return c, nil
}

func (e *applicationDatabaseEngine) bridge(ctx context.Context) (string, error) {
	var n databaseNetwork
	raw, err := e.image.ownedVolumeDocker(ctx, 256<<10, "network", "inspect", "bridge", "--format", "{{json .}}")
	if err != nil || json.Unmarshal(raw, &n) != nil || !dockerObjectID.MatchString(n.ID) || n.Name != "bridge" || n.Driver != "bridge" || n.Scope != "local" || n.Internal || n.Ingress || n.ConfigOnly || n.EnableIPv6 {
		return "", errApplicationDatabaseEngine
	}
	return n.ID, nil
}

func applicationDatabaseSafeContainer(c applicationDatabaseContainer) bool {
	h := c.HostConfig
	return !c.State.Paused && !c.State.Restarting && !c.State.Dead && !h.Privileged && !h.ReadonlyRootfs && !h.PublishAllPorts && !h.AutoRemove && (h.NetworkMode == "default" || h.NetworkMode == "bridge") && h.PidMode == "" && h.IpcMode == "private" && h.UsernsMode == "" && h.UTSMode == "" && h.CgroupnsMode == "private" && (h.Runtime == "" || h.Runtime == "runc") && len(h.Binds) == 0 && len(h.VolumesFrom) == 0 && len(h.CapAdd) == 0 && len(h.CapDrop) == 0 && len(h.SecurityOpt) == 0 && len(h.ExtraHosts) == 0 && len(h.Links) == 0 && len(h.DNS) == 0 && len(h.DNSSearch) == 0 && len(h.DNSOptions) == 0 && len(h.Sysctls) == 0 && len(h.GroupAdd) == 0 && len(h.Devices) == 0 && len(h.DeviceRequests) == 0 && len(h.Mounts) == 0 && len(c.Mounts) == 0 && h.RestartPolicy.Name == "no" && h.RestartPolicy.MaximumRetryCount == 0
}

func (e *applicationDatabaseEngine) inspectCandidate(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, attached, running bool, retired ...string) error {
	return e.inspectCandidateState(ctx, in, c, attached, running, false, retired...)
}

// Recovery alone admits an exited candidate; ordinary configuration requires created.
func (e *applicationDatabaseEngine) inspectCandidateState(ctx context.Context, in applicationDatabaseEngineInput, c applicationDatabaseCandidate, attached, running, stopped bool, retired ...string) error {
	if len(retired) > 1 || stopped && running {
		return errApplicationDatabaseEngine
	}
	removed := ""
	if len(retired) == 1 {
		removed = retired[0]
	}
	if e.check(ctx, in) != nil || c.OperationID != in.Proof.OperationID || c.PlanHash != in.Plan.Hash || c.CreateNonce != in.Proof.CreateNonce || c.ImageID != in.Plan.Release.ImageID || c.DaemonID != in.Plan.Release.DaemonID || !dockerObjectID.MatchString(c.ContainerID) || !dockerObjectID.MatchString(c.DefaultNetworkID) {
		return errApplicationDatabaseEngine
	}
	bridge, err := e.bridge(ctx)
	if err != nil || bridge != c.DefaultNetworkID {
		return errApplicationDatabaseEngine
	}
	status, defaultID, privateID := "created", "", ""
	if stopped {
		status, defaultID, privateID = "exited", bridge, in.Proof.Origin.NetworkID
	}
	if running {
		status = "running"
	}
	actual, err := e.container(ctx, c.ContainerID)
	if err != nil || !applicationDatabaseSafeContainer(actual) || actual.Image != c.ImageID || actual.Name != "/"+applicationDatabaseName(in) || actual.Config.Labels[applicationDatabaseNonceLabel] != c.CreateNonce || actual.Config.Labels[imageBindingLabel] != in.Plan.Snapshot.BindingID || actual.Config.Labels[imageOperationLabel] != c.OperationID || actual.Config.Labels[imagePlanLabel] != c.PlanHash || actual.Config.Labels[imageReleaseLabel] != in.Plan.Release.Hash || actual.State.Running != running || actual.State.Status != status {
		return errApplicationDatabaseEngine
	}
	ports := actual.HostConfig.PortBindings[strconv.Itoa(in.Plan.Snapshot.ContainerPort)+"/tcp"]
	if len(actual.HostConfig.PortBindings) != 1 || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(in.Plan.Snapshot.HostPort) {
		return errApplicationDatabaseEngine
	}
	count := 1
	if attached {
		count++
	}
	defaultNetwork := actual.NetworkSettings.Networks["bridge"]
	if len(actual.NetworkSettings.Networks) != count || !applicationDatabaseEndpointConfig(defaultNetwork, actual.Name, c.ContainerID, false) || running && (defaultNetwork.NetworkID != bridge || !dockerObjectID.MatchString(defaultNetwork.EndpointID)) || !running && (defaultNetwork.NetworkID != defaultID || defaultNetwork.EndpointID != "" || defaultNetwork.IPAddress != "") {
		return errApplicationDatabaseEngine
	}
	if attached {
		n := actual.NetworkSettings.Networks[in.Proof.Binding.NetworkName]
		if !applicationDatabaseEndpointConfig(n, actual.Name, c.ContainerID, true) || running && (n.NetworkID != in.Proof.Origin.NetworkID || !dockerObjectID.MatchString(n.EndpointID)) || !running && (n.NetworkID != privateID || n.EndpointID != "" || n.IPAddress != "") {
			return errApplicationDatabaseEngine
		}
		if running {
			return e.network(ctx, in, &c, removed)
		}
	}
	return e.network(ctx, in, nil, "")
}

func (e *applicationDatabaseEngine) memberIdentity(c applicationDatabaseContainer, m domain.HostingDatabaseNetworkMember, binding string) bool {
	t := m.Endpoint.Target
	return applicationDatabaseSafeContainer(c) && c.ID == t.ContainerID && c.Image == t.ImageID && c.Config.Labels[imageBindingLabel] == binding && c.Config.Labels[imageOperationLabel] == t.ContainerOperationID && c.Config.Labels[imagePlanLabel] == t.ContainerPlanHash && c.Config.Labels[imageReleaseLabel] == t.ReleaseHash
}

func (e *applicationDatabaseEngine) member(ctx context.Context, m domain.HostingDatabaseNetworkMember, binding string) (applicationDatabaseContainer, error) {
	c, err := e.container(ctx, m.Endpoint.Target.ContainerID)
	n := c.NetworkSettings.Networks[m.Endpoint.Target.NetworkName]
	bridge, bridgeErr := e.bridge(ctx)
	if err != nil || bridgeErr != nil || !e.memberIdentity(c, m, binding) || len(c.NetworkSettings.Networks) != 2 || c.NetworkSettings.Networks["bridge"].NetworkID != bridge || !applicationDatabaseEndpointConfig(c.NetworkSettings.Networks["bridge"], c.Name, c.ID, false) || n.NetworkID != m.Endpoint.Target.NetworkID || n.EndpointID != m.Endpoint.EndpointID || !applicationDatabaseEndpointConfig(n, c.Name, c.ID, true) {
		return c, errApplicationDatabaseEngine
	}
	return c, nil
}

func (e *applicationDatabaseEngine) stoppedMember(ctx context.Context, in applicationDatabaseEngineInput, m domain.HostingDatabaseNetworkMember, bridge string) error {
	c, err := e.container(ctx, m.Endpoint.Target.ContainerID)
	n, d := c.NetworkSettings.Networks[in.Proof.Binding.NetworkName], c.NetworkSettings.Networks["bridge"]
	if err != nil || !e.memberIdentity(c, m, in.Proof.Binding.ApplicationBindingID) || c.State.Running || c.State.Status != "exited" || len(c.NetworkSettings.Networks) != 2 || n.NetworkID != m.Endpoint.Target.NetworkID || d.NetworkID != bridge || n.EndpointID != "" || d.EndpointID != "" || n.IPAddress != "" || d.IPAddress != "" || !applicationDatabaseEndpointConfig(n, c.Name, c.ID, true) || !applicationDatabaseEndpointConfig(d, c.Name, c.ID, false) {
		return errApplicationDatabaseEngine
	}
	return nil
}

func (e *applicationDatabaseEngine) network(ctx context.Context, in applicationDatabaseEngineInput, candidate *applicationDatabaseCandidate, retired string) error {
	if e.check(ctx, in) != nil {
		return errApplicationDatabaseEngine
	}
	o := in.Proof.Origin
	// This is the exact retained filename used by ensureDatabaseNetwork/readRecord.
	record, exists, err := e.config.readRecord(in.Proof.Binding.NetworkName + ".json")
	if err != nil || !exists || domain.HostingDocumentHash(record) != o.DocumentHash() {
		return errApplicationDatabaseEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 256<<10, "network", "inspect", o.NetworkID, "--format", "{{json .}}")
	var n databaseNetwork
	if err != nil || json.Unmarshal(raw, &n) != nil || n.ID != o.NetworkID || n.Name != in.Proof.Binding.NetworkName || n.Driver != "bridge" || n.Scope != "local" || !n.Internal || n.Attachable || n.Ingress || n.EnableIPv6 || n.ConfigOnly || len(n.Options) != 0 || !maps.Equal(n.Labels, record.labels()) || n.IPAM.Driver != "default" || len(n.IPAM.Options) != 0 || len(n.IPAM.Config) != 1 {
		return errApplicationDatabaseEngine
	}
	ipam := n.IPAM.Config[0]
	prefix, err := netip.ParsePrefix(ipam.Subnet)
	gateway, gatewayErr := netip.ParseAddr(ipam.Gateway)
	if err != nil || gatewayErr != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Masked() != prefix || prefix.Bits() < 16 || !prefix.Contains(gateway) || !gateway.IsPrivate() || ipam.IPRange != "" || len(ipam.AuxiliaryAddresses) != 0 {
		return errApplicationDatabaseEngine
	}
	allowed := map[string]string{in.Proof.DatabaseHealthy.ContainerID: ""}
	for _, m := range in.Plan.ApplicationDatabase.PreviousMembers {
		if m.Endpoint.Target.ContainerID == retired {
			continue
		}
		if _, err := e.member(ctx, m, in.Proof.Binding.ApplicationBindingID); err != nil {
			return errApplicationDatabaseEngine
		}
		allowed[m.Endpoint.Target.ContainerID] = m.Endpoint.EndpointID
	}
	if candidate != nil {
		allowed[candidate.ContainerID] = ""
	}
	if len(allowed) > 3 || len(n.Containers) != len(allowed) {
		return errApplicationDatabaseEngine
	}
	endpoints := map[string]bool{}
	for id, endpoint := range n.Containers {
		wanted, ok := allowed[id]
		ip, ipErr := netip.ParsePrefix(endpoint.IPv4Address)
		if !ok || !dockerObjectID.MatchString(id) || !dockerObjectID.MatchString(endpoint.EndpointID) || endpoints[endpoint.EndpointID] || wanted != "" && endpoint.EndpointID != wanted || endpoint.IPv6Address != "" || ipErr != nil || !ip.Addr().Is4() || !prefix.Contains(ip.Addr()) || !ip.Addr().IsPrivate() {
			return errApplicationDatabaseEngine
		}
		endpoints[endpoint.EndpointID] = true
		if candidate != nil && id == candidate.ContainerID {
			c, err := e.container(ctx, id)
			if err != nil || c.NetworkSettings.Networks[n.Name].EndpointID != endpoint.EndpointID {
				return errApplicationDatabaseEngine
			}
		}
	}
	h := in.Proof.DatabaseHealthy
	db, err := e.image.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil || !db.State.Running || !databaseContainerMatches(db, h.OperationID, in.Proof.DatabasePlan, record, filepath.Join(e.config.secretRoot, databaseGeneration(h.OperationID, in.Proof.DatabasePlan))) {
		return errApplicationDatabaseEngine
	}
	observed, err := e.container(ctx, h.ContainerID)
	if err != nil {
		return errApplicationDatabaseEngine
	}
	dbEndpoint := observed.NetworkSettings.Networks[n.Name]
	aliases := []string{in.Proof.DatabasePlan.Database.Network.Alias}
	dns := append([]string{strings.TrimPrefix(db.Name, "/")}, aliases...)
	dns = append(dns, db.ID[:12])
	if !slices.Equal(dbEndpoint.Aliases, aliases) || !slices.Equal(dbEndpoint.DNSNames, dns) || len(dbEndpoint.Links) != 0 || len(dbEndpoint.IPAMConfig) != 0 || len(dbEndpoint.DriverOpts) != 0 || dbEndpoint.GwPriority != 0 || dbEndpoint.EndpointID != n.Containers[h.ContainerID].EndpointID {
		return errApplicationDatabaseEngine
	}
	return nil
}
