//go:build unix

package runnerruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/domain"
)

// The one binding lock covers the engine's local network/init records and Docker
// workflow. It is not lease fencing: each mutation still needs current authority.
func (c DatabaseEngineConfig) lock(ctx context.Context, binding string) (*os.File, error) {
	name := domain.HostingDatabaseNetworkName(binding) + ".lock"
	file, err := os.OpenFile(filepath.Join(c.stateRoot, name), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, errors.New("database binding lock unavailable")
	}
	if databaseFilePolicy(filepath.Join(c.stateRoot, name), 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		_ = file.Close()
		return nil, errors.New("database binding lock policy changed")
	}
	for {
		if ctx.Err() != nil {
			_ = file.Close()
			return nil, errors.New("database binding lock canceled")
		}
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return file, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, errors.New("database binding lock failed")
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, errors.New("database binding lock canceled")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type databaseNetworkRecord struct {
	Version         int                      `json:"version"`
	Binding         string                   `json:"binding"`
	Destination     string                   `json:"destination"`
	Daemon          string                   `json:"daemon"`
	Configuration   string                   `json:"configuration"`
	OriginOperation string                   `json:"origin_operation"`
	OriginPlan      string                   `json:"origin_plan"`
	Nonce           string                   `json:"nonce"`
	Storage         domain.HostingStorageRef `json:"storage"`
	VolumeNonce     string                   `json:"volume_nonce"`
	VolumeCreatedAt string                   `json:"volume_created_at"`
	NetworkID       string                   `json:"network_id"`
	InitReady       bool                     `json:"init_ready"`
}

func (r databaseNetworkRecord) labels() map[string]string {
	return map[string]string{
		imageBindingLabel:                     r.Binding,
		"io.nerocd.database.network.version":  "1",
		"io.nerocd.database.destination":      r.Destination,
		"io.nerocd.database.configuration":    r.Configuration,
		"io.nerocd.database.daemon":           r.Daemon,
		"io.nerocd.database.origin_operation": r.OriginOperation,
		"io.nerocd.database.origin_plan":      r.OriginPlan,
		"io.nerocd.database.creation_nonce":   r.Nonce,
	}
}

func (c DatabaseEngineConfig) readRecord(name string) (databaseNetworkRecord, bool, error) {
	var record databaseNetworkRecord
	path := filepath.Join(c.stateRoot, name)
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil {
		return record, false, errors.New("database retained network history unavailable")
	}
	defer func() { _ = file.Close() }()
	if databaseFilePolicy(path, 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return record, false, errors.New("database retained history policy changed")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8192 {
		return record, false, errors.New("invalid database network history")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 || domain.DecodeHostingStorageProtocolJSON(raw, &record) != nil || record.Version != 1 || !hostingVolumeReference.MatchString(record.OriginOperation) || !hostingVolumeHash.MatchString(record.OriginPlan) || !hostingVolumeNonce.MatchString(record.Nonce) || record.NetworkID != "" && !dockerObjectID.MatchString(record.NetworkID) {
		return record, false, errors.New("invalid database retained network history")
	}
	return record, true, nil
}

func (c DatabaseEngineConfig) writeRecord(ctx context.Context, name string, record databaseNetworkRecord, before func(context.Context) error) error {
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 8192 {
		return errors.New("invalid database network history")
	}
	return c.writeHistory(ctx, name, raw, before)
}

func (c DatabaseEngineConfig) writeHistory(ctx context.Context, name string, raw []byte, before func(context.Context) error) error {
	if err := databaseAuthorize(ctx, before); err != nil {
		return err
	}
	root, err := os.OpenRoot(c.stateRoot)
	if err != nil {
		return errors.New("database retained history unavailable")
	}
	defer func() { _ = root.Close() }()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return errors.New("database history identity unavailable")
	}
	temp := name + "." + hex.EncodeToString(nonce[:]) + ".pending"
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("database retained history write failed")
	}
	defer func() { _ = file.Close(); _ = root.Remove(temp) }()
	if _, err = file.Write(raw); err != nil {
		return errors.New("database retained history write failed")
	}
	if file.Sync() != nil {
		return errors.New("database retained history synchronization failed")
	}
	if err = databaseAuthorize(ctx, before); err != nil {
		return err
	}
	if root.Rename(temp, name) != nil {
		return errors.New("database retained history publication failed")
	}
	dir, err := os.Open(c.stateRoot)
	if err != nil {
		return errors.New("database history root unavailable")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return errors.New("database history directory synchronization failed")
	}
	if ctx.Err() != nil {
		return errors.New("database retained history authority expired")
	}
	return nil
}

type databaseNetwork struct {
	ID                                                    string `json:"Id"`
	Name, Driver, Scope                                   string
	Internal, Attachable, Ingress, EnableIPv6, ConfigOnly bool
	Labels, Options                                       map[string]string
	IPAM                                                  struct {
		Driver  string
		Options map[string]string
		Config  []struct {
			Subnet, Gateway, IPRange string
			AuxiliaryAddresses       map[string]string
		}
	}
	Containers map[string]struct{ Name, EndpointID, MacAddress, IPv4Address, IPv6Address string }
}

func (e *imageEngine) databaseNetworkPresent(ctx context.Context, name string) (bool, error) {
	raw, err := e.ownedVolumeDocker(ctx, 32<<10, "network", "ls", "--format", "{{.Name}}", "--filter", "name="+name)
	if err != nil {
		return false, err
	}
	names, err := hostingVolumeLines(raw, 256, hostingVolumeName)
	if err != nil {
		return false, err
	}
	for _, got := range names {
		if got == name {
			return true, nil
		}
	}
	return false, nil
}

func (e *imageEngine) inspectDatabaseNetwork(ctx context.Context, name string, record databaseNetworkRecord, known map[string]bool) (databaseNetwork, error) {
	var network databaseNetwork
	raw, err := e.ownedVolumeDocker(ctx, 256<<10, "network", "inspect", name, "--format", "{{json .}}")
	if err != nil {
		return network, err
	}
	if json.Unmarshal(raw, &network) != nil || network.Name != name || !dockerObjectID.MatchString(network.ID) || record.NetworkID != "" && network.ID != record.NetworkID || network.Driver != "bridge" || network.Scope != "local" || !network.Internal || network.Attachable || network.Ingress || network.EnableIPv6 || network.ConfigOnly || len(network.Options) != 0 || !maps.Equal(network.Labels, record.labels()) || network.IPAM.Driver != "default" || len(network.IPAM.Options) != 0 || len(network.IPAM.Config) != 1 || len(network.Containers) > 2 {
		return network, errors.New("database private network ownership or policy mismatch")
	}
	ipam := network.IPAM.Config[0]
	prefix, err := netip.ParsePrefix(ipam.Subnet)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Masked() != prefix || prefix.Bits() < 16 || ipam.IPRange != "" || len(ipam.AuxiliaryAddresses) != 0 {
		return network, errors.New("database private network address policy mismatch")
	}
	gateway, err := netip.ParseAddr(ipam.Gateway)
	if err != nil || !prefix.Contains(gateway) || !gateway.IsPrivate() {
		return network, errors.New("database private network gateway mismatch")
	}
	for id, endpoint := range network.Containers {
		address, err := netip.ParsePrefix(endpoint.IPv4Address)
		if !known[id] || !dockerObjectID.MatchString(id) || !dockerObjectID.MatchString(endpoint.EndpointID) || endpoint.IPv6Address != "" || err != nil || !prefix.Contains(address.Addr()) || !address.Addr().IsPrivate() {
			return network, errors.New("database network has an untrusted attachment")
		}
	}
	return network, nil
}

func (e *imageEngine) ensureDatabaseNetwork(ctx context.Context, operation string, plan domain.HostingApplyPlan, req hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, c DatabaseEngineConfig, before func(context.Context) error, known map[string]bool) (databaseNetworkRecord, databaseNetwork, error) {
	var zero databaseNetwork
	name := plan.Database.NetworkName
	record, exists, err := c.readRecord(name + ".json")
	if err != nil {
		return record, zero, err
	}
	if exists && (record.Binding != plan.Snapshot.BindingID || record.Destination != plan.Snapshot.DestinationID || record.Daemon != plan.Release.DaemonID || record.Configuration != plan.Database.Reference.ConfigurationHash || record.OriginOperation != req.OriginOperationID || record.OriginPlan != req.OriginPlanHash || record.Storage != req.Storage.Reference() || record.VolumeNonce != req.CreationNonce || record.VolumeCreatedAt != evidence.CreatedAt) {
		return record, zero, errors.New("database retained origin or configuration changed")
	}
	present, err := e.databaseNetworkPresent(ctx, name)
	if err != nil {
		return record, zero, err
	}
	fresh := false
	if !exists {
		if present || operation != req.OriginOperationID || plan.Hash != req.OriginPlanHash {
			return record, zero, errors.New("database network cannot adopt missing local origin history")
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return record, zero, errors.New("database network identity unavailable")
		}
		record = databaseNetworkRecord{Version: 1, Binding: plan.Snapshot.BindingID, Destination: plan.Snapshot.DestinationID, Daemon: plan.Release.DaemonID, Configuration: plan.Database.Reference.ConfigurationHash, OriginOperation: req.OriginOperationID, OriginPlan: req.OriginPlanHash, Nonce: hex.EncodeToString(nonce[:]), Storage: req.Storage.Reference(), VolumeNonce: req.CreationNonce, VolumeCreatedAt: evidence.CreatedAt}
		if err = c.writeRecord(ctx, name+".json", record, before); err != nil {
			return record, zero, err
		}
		fresh = true
	}
	if !present {
		if !fresh {
			return record, zero, errors.New("admitted database network missing; creation cannot be replayed")
		}
		args := []string{"network", "create", "--driver", "bridge", "--internal"}
		labels := record.labels()
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "--label", key+"="+labels[key])
		}
		args = append(args, name)
		if err = databaseAuthorize(ctx, before); err != nil {
			return record, zero, err
		}
		if _, err = e.mutate(ctx, plan.Release.DaemonID, args...); err != nil {
			return record, zero, errors.New("database network create outcome requires inspection")
		}
	}
	network, err := e.inspectDatabaseNetwork(ctx, name, record, known)
	if err != nil {
		return record, zero, err
	}
	if record.NetworkID == "" {
		record.NetworkID = network.ID
		if err = c.writeRecord(ctx, name+".json", record, before); err != nil {
			return record, zero, err
		}
	}
	return record, network, nil
}

type databaseContainerIntent struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Plan      string `json:"plan"`
	Binding   string `json:"binding"`
	Daemon    string `json:"daemon"`
	NetworkID string `json:"network_id"`
}

// Retirement belongs to one exact successor and predecessor. It is retained
// after acknowledgment so a replay can explain absence without adopting an
// unknown container or erasing storage creation history.
type databaseRetirement struct {
	Version              int                      `json:"version"`
	Operation            string                   `json:"operation"`
	Plan                 string                   `json:"plan"`
	ReplacementID        string                   `json:"replacement_id"`
	Binding              string                   `json:"binding"`
	Destination          string                   `json:"destination"`
	Daemon               string                   `json:"daemon"`
	Storage              domain.HostingStorageRef `json:"storage"`
	Volume               string                   `json:"volume"`
	OriginOperation      string                   `json:"origin_operation"`
	OriginPlan           string                   `json:"origin_plan"`
	VolumeNonce          string                   `json:"volume_nonce"`
	NetworkID            string                   `json:"network_id"`
	PredecessorOperation string                   `json:"predecessor_operation"`
	PredecessorPlan      string                   `json:"predecessor_plan"`
	PredecessorID        string                   `json:"predecessor_id"`
	PredecessorRelease   string                   `json:"predecessor_release"`
	PredecessorHash      string                   `json:"predecessor_hash"`
	Completed            bool                     `json:"completed"`
}

func databaseExpectedRetirement(operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, replacement string) databaseRetirement {
	previous := plan.PreviousHealthy
	return databaseRetirement{Version: 1, Operation: operation, Plan: plan.Hash, ReplacementID: replacement, Binding: plan.Snapshot.BindingID, Destination: plan.Snapshot.DestinationID, Daemon: plan.Release.DaemonID, Storage: plan.Snapshot.Storage, Volume: plan.Storage.VolumeName, OriginOperation: record.OriginOperation, OriginPlan: record.OriginPlan, VolumeNonce: record.VolumeNonce, NetworkID: record.NetworkID, PredecessorOperation: previous.OperationID, PredecessorPlan: previous.PlanHash, PredecessorID: previous.ContainerID, PredecessorRelease: previous.Release.Hash, PredecessorHash: domain.HostingDocumentHash(previous)}
}

func databaseRetirementName(operation string, plan domain.HostingApplyPlan) string {
	return databaseGeneration(operation, plan) + ".retirement.json"
}

func (c DatabaseEngineConfig) readRetirement(operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, replacement string) (databaseRetirement, bool, error) {
	var retirement databaseRetirement
	if plan.PreviousHealthy == nil {
		return retirement, false, nil
	}
	path := filepath.Join(c.stateRoot, databaseRetirementName(operation, plan))
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return retirement, false, nil
	}
	if err != nil {
		return retirement, false, errors.New("database retirement history unavailable")
	}
	defer func() { _ = file.Close() }()
	if databaseFilePolicy(path, 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return retirement, false, errors.New("database retirement history policy changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(raw) > 8192 || domain.DecodeHostingStorageProtocolJSON(raw, &retirement) != nil {
		return retirement, false, errors.New("invalid database retirement history")
	}
	want := databaseExpectedRetirement(operation, plan, record, replacement)
	comparison := retirement
	comparison.Completed = false
	if !dockerObjectID.MatchString(replacement) || comparison != want {
		return retirement, false, errors.New("database retirement identity changed")
	}
	return retirement, true, nil
}

func (c DatabaseEngineConfig) writeRetirement(ctx context.Context, operation string, plan domain.HostingApplyPlan, retirement databaseRetirement, before func(context.Context) error) error {
	raw, err := json.Marshal(retirement)
	if err != nil || len(raw) > 8192 {
		return errors.New("invalid database retirement history")
	}
	return c.writeHistory(ctx, databaseRetirementName(operation, plan), raw, before)
}

func (c DatabaseEngineConfig) admitContainer(ctx context.Context, operation string, plan domain.HostingApplyPlan, record databaseNetworkRecord, present bool, before func(context.Context) error) error {
	name := databaseGeneration(operation, plan) + ".json"
	want := databaseContainerIntent{Version: 1, Operation: operation, Plan: plan.Hash, Binding: plan.Snapshot.BindingID, Daemon: plan.Release.DaemonID, NetworkID: record.NetworkID}
	file, err := os.OpenFile(filepath.Join(c.stateRoot, name), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		defer func() { _ = file.Close() }()
		if databaseFilePolicy(filepath.Join(c.stateRoot, name), 0o600, os.Geteuid(), os.Getegid(), false) != nil {
			return errors.New("database container intent policy changed")
		}
		raw, err := io.ReadAll(io.LimitReader(file, 8193))
		var got databaseContainerIntent
		if err != nil || len(raw) > 8192 || domain.DecodeHostingStorageProtocolJSON(raw, &got) != nil || got != want || !present {
			return errors.New("database admitted container missing or changed; creation cannot be replayed")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) || present {
		return errors.New("database container lacks retained creation intent")
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return errors.New("invalid database container intent")
	}
	return c.writeHistory(ctx, name, raw, before)
}

func databaseKnownContainers(plan domain.HostingApplyPlan, candidate string) map[string]bool {
	known := map[string]bool{}
	if plan.PreviousHealthy != nil {
		known[plan.PreviousHealthy.ContainerID] = true
	}
	if candidate != "" {
		known[candidate] = true
	}
	return known
}

func databaseNetworkAddress(network databaseNetwork, id string) (string, error) {
	endpoint, ok := network.Containers[id]
	if !ok {
		return "", errors.New("database private endpoint missing")
	}
	address, err := netip.ParsePrefix(endpoint.IPv4Address)
	if err != nil || !address.Addr().IsPrivate() || !address.Addr().Is4() {
		return "", errors.New("database private endpoint invalid")
	}
	return strings.TrimSpace(address.Addr().String()), nil
}
