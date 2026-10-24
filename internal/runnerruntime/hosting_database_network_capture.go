//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/domain"
)

var errDatabaseNetworkOriginObservation = errors.New("database network origin observation not confirmed")

// This private observer borrows the existing engine and protected configuration.
// It is not registered with a runner, coordinator, public API or capability.
type databaseNetworkOriginObserver struct {
	engine *imageEngine
	config DatabaseEngineConfig
}

func newDatabaseNetworkOriginObserver(engine *imageEngine, config DatabaseEngineConfig) (*databaseNetworkOriginObserver, error) {
	if engine == nil || !strings.HasPrefix(engine.endpoint, "unix://") || engine.configDir == "" {
		return nil, errDatabaseNetworkOriginObservation
	}
	for _, path := range []string{config.stateRoot, config.secretRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, ",\r\n\x00") {
			return nil, errDatabaseNetworkOriginObservation
		}
	}
	if config.stateRoot == config.secretRoot || strings.HasPrefix(config.stateRoot, config.secretRoot+"/") || strings.HasPrefix(config.secretRoot, config.stateRoot+"/") {
		return nil, errDatabaseNetworkOriginObservation
	}
	return &databaseNetworkOriginObserver{engine: engine, config: config}, nil
}

func captureObservationAuthority(ctx context.Context, before func(context.Context) error) error {
	if ctx.Err() != nil || before == nil || before(ctx) != nil || ctx.Err() != nil {
		return errDatabaseNetworkOriginObservation
	}
	return nil
}

// Unlike DatabaseEngineConfig.lock, observing cannot create a missing lock file.
// Exclusive flock serializes with the existing writer's lock without writing it.
func lockDatabaseNetworkOrigin(ctx context.Context, config DatabaseEngineConfig, binding string) (*os.File, error) {
	path := filepath.Join(config.stateRoot, domain.HostingDatabaseNetworkName(binding)+".lock")
	if databaseFilePolicy(path, 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return nil, errDatabaseNetworkOriginObservation
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errDatabaseNetworkOriginObservation
	}
	closeFailure := func() (*os.File, error) { _ = file.Close(); return nil, errDatabaseNetworkOriginObservation }
	if databaseFilePolicy(path, 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return closeFailure()
	}
	opened, err := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || !os.SameFile(opened, current) {
		return closeFailure()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return closeFailure()
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return closeFailure()
		}
		select {
		case <-ctx.Done():
			return closeFailure()
		case <-ticker.C:
		}
	}
}

type databaseCaptureEndpoint struct {
	NetworkID, EndpointID, IPAddress, GlobalIPv6Address, MacAddress string
	Aliases, DNSNames, Links                                        []string
	IPAMConfig, DriverOpts                                          map[string]json.RawMessage
	GwPriority                                                      int
}

// Docker29's retained private-network diagnostic profile has no explicit aliases
// and generates exactly the container name and its short ID as DNS names. Actual
// database-create qualification must confirm this profile before runtime wiring.
func (o *databaseNetworkOriginObserver) sourceEndpoint(ctx context.Context, plan domain.HostingDatabaseNetworkCapturePlan, record databaseNetworkRecord, network databaseNetwork) error {
	e, p, h := o.engine, plan.SourcePlan, plan.SourceHealthy
	container, err := e.inspectDatabaseContainer(ctx, h.ContainerID)
	if err != nil || !container.State.Running || container.State.Status != "running" || container.ID != h.ContainerID || !databaseContainerMatches(container, h.OperationID, p, record, filepath.Join(o.config.secretRoot, databaseGeneration(h.OperationID, p))) {
		return errDatabaseNetworkOriginObservation
	}
	raw, err := e.ownedVolumeDocker(ctx, 256<<10, "container", "inspect", h.ContainerID, "--format", "{{json .}}")
	var detail struct {
		ID              string `json:"Id"`
		NetworkSettings struct {
			Networks map[string]databaseCaptureEndpoint
		}
	}
	if err != nil || json.Unmarshal(raw, &detail) != nil || detail.ID != h.ContainerID || len(detail.NetworkSettings.Networks) != 1 {
		return errDatabaseNetworkOriginObservation
	}
	endpoint, ok := detail.NetworkSettings.Networks[p.Database.NetworkName]
	observed, member := network.Containers[h.ContainerID]
	name := databaseContainerName(h.OperationID, p)
	address, addressErr := databaseNetworkAddress(network, h.ContainerID)
	mac, macErr := net.ParseMAC(endpoint.MacAddress)
	if !ok || !member || observed.Name != name || endpoint.NetworkID != network.ID || endpoint.EndpointID != observed.EndpointID || !dockerObjectID.MatchString(endpoint.EndpointID) || addressErr != nil || endpoint.IPAddress != address || endpoint.GlobalIPv6Address != "" || endpoint.MacAddress != observed.MacAddress || macErr != nil || len(mac) != 6 || len(endpoint.Aliases) != 0 || !slices.Equal(endpoint.DNSNames, []string{name, h.ContainerID[:12]}) || len(endpoint.Links) != 0 || len(endpoint.IPAMConfig) != 0 || len(endpoint.DriverOpts) != 0 || endpoint.GwPriority != 0 {
		return errDatabaseNetworkOriginObservation
	}
	return nil
}

func (o *databaseNetworkOriginObserver) exactUsers(ctx context.Context, kind, identity, source string) error {
	raw, err := o.engine.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", kind+"="+identity)
	if err != nil {
		return errDatabaseNetworkOriginObservation
	}
	ids, err := hostingVolumeLines(raw, 2, dockerObjectID)
	if err != nil || len(ids) != 1 || ids[0] != source {
		return errDatabaseNetworkOriginObservation
	}
	return nil
}

func (o *databaseNetworkOriginObserver) identities(ctx context.Context, plan domain.HostingDatabaseNetworkCapturePlan, record databaseNetworkRecord) error {
	e, h, p := o.engine, plan.SourceHealthy, plan.SourcePlan
	if e.verifyDaemon(ctx, record.Daemon) != nil || e.verifyLocalRelease(ctx, p.Release) != nil {
		return errDatabaseNetworkOriginObservation
	}
	req := hostingOwnedVolumeRequest{Storage: plan.Storage, DaemonID: record.Daemon, OriginOperationID: record.OriginOperation, OriginPlanHash: record.OriginPlan, CreationNonce: record.VolumeNonce, KnownUsers: []domain.HostingHealthy{h}}
	volume, err := e.verifyOwnedVolume(ctx, req)
	if err != nil || volume.CreatedAt != record.VolumeCreatedAt || volume.Driver != plan.Creation.Driver || volume.Scope != plan.Creation.Scope || !maps.Equal(volume.Labels, plan.Creation.Labels) || !maps.Equal(volume.Options, plan.Creation.Options) || o.exactUsers(ctx, "volume", plan.Storage.VolumeName, h.ContainerID) != nil {
		return errDatabaseNetworkOriginObservation
	}
	network, err := e.inspectDatabaseNetwork(ctx, p.Database.NetworkName, record, map[string]bool{h.ContainerID: true})
	if err != nil || len(network.Containers) != 1 || o.exactUsers(ctx, "network", network.ID, h.ContainerID) != nil || o.sourceEndpoint(ctx, plan, record, network) != nil || e.verifyDaemon(ctx, record.Daemon) != nil {
		return errDatabaseNetworkOriginObservation
	}
	return nil
}

// ObserveDatabaseNetworkOrigin returns full retained initialization attestation
// after read-only host inspection. It neither probes credentials/readiness nor
// repairs missing state. Host root and the configured daemon remain trusted; copied
// metadata from an administrator is not cryptographic storage identity proof.
func (o *databaseNetworkOriginObserver) ObserveDatabaseNetworkOrigin(parent context.Context, plan domain.HostingDatabaseNetworkCapturePlan, before func(context.Context) error) (origin domain.HostingDatabaseNetworkOrigin, err error) {
	zero := domain.HostingDatabaseNetworkOrigin{}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if o == nil || o.engine == nil || plan.Validate() != nil || captureObservationAuthority(ctx, before) != nil || o.config.validateHost(ctx, o.engine) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	lock, err := lockDatabaseNetworkOrigin(ctx, o.config, plan.BindingID)
	if err != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			origin, err = zero, errDatabaseNetworkOriginObservation
		}
	}()
	name := plan.SourcePlan.Database.NetworkName + ".json"
	if databaseFilePolicy(filepath.Join(o.config.stateRoot, name), 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	record, exists, err := o.config.readRecord(name)
	if err != nil || !exists {
		return zero, errDatabaseNetworkOriginObservation
	}
	raw, err := json.Marshal(record)
	if err != nil || json.Unmarshal(raw, &origin) != nil || origin.DocumentHash() != domain.HostingDocumentHash(record) || plan.ValidateOrigin(origin) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	if o.identities(ctx, plan, record) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	// Recheck the complete retained record and physical identities under the same
	// lock. Platform writers serialize here; daemon/root administrators are trusted.
	if databaseFilePolicy(filepath.Join(o.config.stateRoot, name), 0o600, os.Geteuid(), os.Getegid(), false) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	current, exists, err := o.config.readRecord(name)
	if err != nil || !exists || current != record || o.identities(ctx, plan, current) != nil || captureObservationAuthority(ctx, before) != nil {
		return zero, errDatabaseNetworkOriginObservation
	}
	return origin, nil
}
