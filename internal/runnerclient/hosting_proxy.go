package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"nerocd/internal/domain"
)

type proxyIdentityResponse struct {
	RunnerID     string   `json:"runner_id"`
	Capabilities []string `json:"capabilities"`
}

func (r *proxyIdentityResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxIdentityResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxIdentityResponseBytes {
		return errors.New("proxy identity response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var result proxyIdentityResponse
	if err = decoder.Decode(&result); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("proxy identity requires one JSON document")
	}
	*r = result
	return nil
}

// ProxyIdentity verifies persisted proxy capability without heartbeat or claim.
// Older servers without capabilities fail closed; Identity remains compatible.
func (c Client) ProxyIdentity(ctx context.Context) (string, error) {
	return c.RuntimeIdentity(ctx, true)
}

// RuntimeIdentity preserves the proxy-only startup contract and requires build
// execution to be disabled. Build-capable runners need explicit build validation.
func (c Client) RuntimeIdentity(ctx context.Context, proxyConfigured bool) (string, error) {
	return c.RuntimeCapabilitiesIdentity(ctx, proxyConfigured, false)
}

// RuntimeCapabilitiesIdentity checks exact persisted proxy/build capabilities
// against explicit local configuration. Legacy responses require both disabled.
func (c Client) RuntimeCapabilitiesIdentity(ctx context.Context, proxyConfigured, buildConfigured bool) (string, error) {
	return c.RuntimeExecutionIdentity(ctx, proxyConfigured, buildConfigured, false)
}

// RuntimeExecutionIdentity checks persisted execution capabilities against all
// explicit local execution modes. Omitted privateConfigured defaults to false;
// legacy wrappers require local builds and private Git disabled.
func (c Client) RuntimeExecutionIdentity(ctx context.Context, proxyConfigured, buildConfigured, localBuildConfigured bool, privateConfigured ...bool) (string, error) {
	if len(privateConfigured) > 1 {
		return "", errors.New("invalid private Git configuration")
	}
	return c.RuntimeExecutionIdentityWithOptions(ctx, RuntimeExecutionOptions{Proxy: proxyConfigured, Build: buildConfigured, LocalBuild: localBuildConfigured, PrivateGit: len(privateConfigured) == 1 && privateConfigured[0]})
}

// RuntimeExecutionOptions selects explicitly configured local execution modes.
type RuntimeExecutionOptions struct {
	RegistryPull                                                                                                                 bool
	StorageBackupRetention                                                                                                       bool
	DatabaseBackupRetention                                                                                                      bool
	RemoteStorageBackups                                                                                                         bool
	RemoteDatabaseBackups                                                                                                        bool
	LogicalDatabaseBackups                                                                                                       bool
	ApplicationDatabaseStorage                                                                                                   bool
	ApplicationDatabaseRouteContinuity                                                                                           bool
	ApplicationDatabase, DatabaseMaintenance                                                                                     bool
	DatabaseNetworkCaptureAutomatic                                                                                              bool
	DatabaseNetworkCapture, DatabaseUpgrade                                                                                      bool
	StorageArchive, StorageArchiveTrustedLoopbackHTTP                                                                            bool
	Database, Proxy, Build, LocalBuild, PrivateGit, RuntimeEnvironment, Observation, RuntimeLogs, Readiness, ProxyVerifiedSwitch bool
	DatabaseTrustedLoopbackHTTP, ReadinessTrustedLoopbackHTTP                                                                    bool
}

// RuntimeExecutionIdentityWithOptions checks every protected execution capability.
func (c Client) RuntimeExecutionIdentityWithOptions(ctx context.Context, options RuntimeExecutionOptions) (string, error) {
	if options.DatabaseNetworkCaptureAutomatic {
		if ValidateRuntimeEnvironmentServer(c.Server, RuntimeEnvironmentTransport{}) != nil {
			return "", errors.New("automatic database capture requires verified HTTPS")
		}
	}
	if options.StorageArchive {
		ctx = WithStorageArchiveTransport(ctx, StorageArchiveTransport{TrustedLoopbackHTTP: options.StorageArchiveTrustedLoopbackHTTP})
	}
	if options.Database || options.DatabaseNetworkCapture || options.DatabaseNetworkCaptureAutomatic {
		ctx = WithDatabaseTransport(ctx, DatabaseTransport{TrustedLoopbackHTTP: options.DatabaseTrustedLoopbackHTTP})
	}
	if options.Readiness {
		secured, closeTransport, err := c.serverReadinessHTTP(ServerReadinessTransport{TrustedLoopbackHTTP: options.ReadinessTrustedLoopbackHTTP})
		if err != nil {
			return "", err
		}
		defer closeTransport()
		c = secured
	} else if options.Observation || options.RuntimeLogs {
		secured, closeTransport, err := c.observationHTTP()
		if err != nil {
			return "", err
		}
		defer closeTransport()
		c = secured
	}
	proxyConfigured, buildConfigured, localBuildConfigured := options.Proxy, options.Build, options.LocalBuild
	var result proxyIdentityResponse
	if err := c.get(ctx, "/api/v1/runners/identity", &result); err != nil {
		return "", err
	}
	// Preserve Identity's established ID contract and registration's opaque
	// capability names. Only exact persisted proxy/build names participate in
	// local configuration checks; duplicates do not change those checks.
	if result.RunnerID == "" || strings.TrimSpace(result.RunnerID) != result.RunnerID {
		return "", errors.New("runner identity response did not include a valid runner_id")
	}
	if options.Readiness && !readinessResponseID.MatchString(result.RunnerID) {
		return "", errors.New("readiness identity did not include a canonical runner_id")
	}
	private := options.PrivateGit
	if private && !buildConfigured {
		return "", errors.New("invalid private Git configuration")
	}
	proxyCapable, buildCapable, shellCapable, localBuildCapable, privateCapable := false, false, false, false, false
	verifiedSwitchCapable := false
	for _, capability := range result.Capabilities {
		verifiedSwitchCapable = verifiedSwitchCapable || capability == domain.HostingProxyVerifiedSwitchCapability
		if capability == domain.HostingBuildTriggerRevisionCapability && !buildConfigured {
			return "", errors.New("authenticated trigger revision capability requires build configuration")
		}
		privateCapable = privateCapable || capability == domain.HostingPrivateGitCapability
		proxyCapable = proxyCapable || capability == domain.RunTypeProxyApply
		buildCapable = buildCapable || capability == domain.HostingBuildTaskType
		localBuildCapable = localBuildCapable || capability == domain.RunTypeLocalBuildDeploy
		shellCapable = shellCapable || capability == "shell"
	}
	if verifiedSwitchCapable != options.ProxyVerifiedSwitch || verifiedSwitchCapable && !proxyConfigured {
		return "", errors.New("authenticated verified proxy switch capability does not match explicit local configuration")
	}
	for _, capability := range result.Capabilities {
		if capability == domain.HostingStackCapability && !options.RuntimeEnvironment {
			return "", errors.New("managed stack capability requires configured runtime environment")
		}
	}
	environmentCap, imageCap, releaseCap := false, false, false
	for _, capability := range result.Capabilities {
		environmentCap = environmentCap || capability == domain.HostingRuntimeEnvironmentCapability
		imageCap = imageCap || capability == domain.RunTypeImageDeploy
		releaseCap = releaseCap || capability == domain.HostingReleasePortsCapability
	}
	registryCap := false
	for _, capability := range result.Capabilities {
		registryCap = registryCap || capability == domain.HostingRegistryPullCapability
	}
	if registryCap != options.RegistryPull || registryCap && (!imageCap || !releaseCap || shellCapable) {
		return "", errors.New("registry pull capabilities do not match explicit configuration")
	}
	if environmentCap != options.RuntimeEnvironment || environmentCap && (!imageCap || !releaseCap || shellCapable) {
		return "", errors.New("authenticated runtime environment capabilities do not match explicit configuration")
	}
	for _, capability := range result.Capabilities {
		if capability == domain.HostingOwnedStorageRuntimeCapability && (!options.RuntimeEnvironment || !imageCap || !releaseCap || shellCapable) {
			return "", errors.New("owned storage runtime capability requires configured image runtime environment")
		}
	}
	observationCap := false
	for _, capability := range result.Capabilities {
		observationCap = observationCap || capability == domain.HostingObservationCapability
	}
	if observationCap != options.Observation || observationCap && shellCapable {
		return "", errors.New("authenticated observation capability does not match explicit configuration")
	}
	for _, capability := range result.Capabilities {
		if capability == domain.HostingStorageObservationCapability && (!options.Observation || !observationCap || shellCapable) {
			return "", errors.New("authenticated storage observation capability requires configured hosting observation")
		}
	}
	logCap := false
	for _, capability := range result.Capabilities {
		logCap = logCap || capability == domain.HostingRuntimeLogsCapability
	}
	if logCap != options.RuntimeLogs || logCap && shellCapable {
		return "", errors.New("authenticated runtime log capability does not match explicit configuration")
	}
	for _, capability := range result.Capabilities {
		if capability == domain.HostingStackRuntimeLogsCapability && (!options.RuntimeLogs || !logCap || shellCapable) {
			return "", errors.New("authenticated stack runtime log capability requires configured runtime logs")
		}
	}
	readinessCap := false
	for _, capability := range result.Capabilities {
		readinessCap = readinessCap || capability == domain.ServerReadinessCapability
	}
	if readinessCap != options.Readiness || readinessCap && shellCapable {
		return "", errors.New("authenticated readiness capability does not match explicit configuration")
	}
	captureCapable := false
	for _, capability := range result.Capabilities {
		captureCapable = captureCapable || capability == domain.HostingDatabaseNetworkCaptureCapability
	}
	if options.DatabaseNetworkCapture && (!captureCapable || options.Database || proxyConfigured || buildConfigured || localBuildConfigured || private || options.StorageArchive || options.RuntimeEnvironment || options.Observation || options.RuntimeLogs || options.Readiness || options.ProxyVerifiedSwitch) || captureCapable && shellCapable || options.DatabaseNetworkCaptureAutomatic && (!captureCapable || !options.Database || options.DatabaseNetworkCapture) {
		return "", errors.New("authenticated capture capability does not match explicit exact-ID configuration")
	}
	upgradeCapable := false
	for _, capability := range result.Capabilities {
		upgradeCapable = upgradeCapable || capability == domain.HostingDatabaseUpgradeCapability
	}
	databaseCapable, databaseTask, storageCapable := false, false, false
	for _, capability := range result.Capabilities {
		databaseCapable = databaseCapable || capability == domain.HostingDatabaseCapability
		databaseTask = databaseTask || capability == domain.RunTypeDatabaseDeploy
		storageCapable = storageCapable || capability == domain.HostingOwnedStorageCapability
	}
	if (!options.DatabaseNetworkCapture && (databaseCapable != options.Database || databaseTask != options.Database)) || (options.DatabaseNetworkCapture && databaseCapable != databaseTask) || databaseCapable && (shellCapable || !storageCapable) {
		return "", errors.New("authenticated database capability does not match explicit configuration")
	}
	if options.DatabaseNetworkCapture {
		// Exact-ID capture recognizes persisted deployment capabilities without
		// configuring their executor or entering the ordinary claim loop.
		if options.DatabaseUpgrade || upgradeCapable && (!databaseCapable || !databaseTask || !storageCapable) {
			return "", errors.New("authenticated capture upgrade prerequisites unavailable")
		}
	} else if upgradeCapable != options.DatabaseUpgrade || options.DatabaseUpgrade && !options.Database {
		return "", errors.New("authenticated database upgrade capability does not match explicit configuration")
	}
	storageComposition := false
	for _, capability := range result.Capabilities {
		storageComposition = storageComposition || capability == domain.HostingApplicationDatabaseStorageCapability
	}
	if storageComposition != options.ApplicationDatabaseStorage || storageComposition && (!options.ApplicationDatabase || !options.Database || !options.RuntimeEnvironment) {
		return "", errors.New("authenticated connected storage capability does not match explicit configuration")
	}
	routeContinuityCap := false
	for _, capability := range result.Capabilities {
		routeContinuityCap = routeContinuityCap || capability == domain.HostingApplicationDatabaseRouteContinuityCapability
	}
	if routeContinuityCap != options.ApplicationDatabaseRouteContinuity || routeContinuityCap && (!options.ApplicationDatabase || !options.ProxyVerifiedSwitch || !options.Proxy) {
		return "", errors.New("authenticated route continuity capability does not match explicit configuration")
	}
	applicationCap, maintenanceCap := false, false
	for _, capability := range result.Capabilities {
		applicationCap = applicationCap || capability == domain.HostingApplicationDatabaseCapability
		maintenanceCap = maintenanceCap || capability == domain.HostingDatabaseMaintenanceCapability
	}
	if applicationCap != options.ApplicationDatabase || maintenanceCap != options.DatabaseMaintenance || (applicationCap || maintenanceCap) && (!options.Database || !options.DatabaseNetworkCaptureAutomatic || !options.DatabaseUpgrade || !options.RuntimeEnvironment || !imageCap || !releaseCap || !databaseCapable || !storageCapable || !captureCapable || !upgradeCapable || shellCapable || options.DatabaseNetworkCapture) {
		return "", errors.New("authenticated connected database capabilities do not match explicit configuration")
	}
	retentionCap, retentionTask := false, false
	for _, capability := range result.Capabilities {
		retentionCap = retentionCap || capability == domain.HostingDatabaseBackupRetentionCapability
		retentionTask = retentionTask || capability == domain.RunTypeDatabaseBackupRetention
	}
	if retentionCap != options.DatabaseBackupRetention || retentionTask != options.DatabaseBackupRetention || retentionCap && (!options.RemoteDatabaseBackups || !options.LogicalDatabaseBackups || !options.Database || shellCapable) {
		return "", errors.New("authenticated retention capability does not match explicit configuration")
	}
	remoteCap, remoteTask := false, false
	for _, capability := range result.Capabilities {
		remoteCap = remoteCap || capability == domain.HostingDatabaseBackupRemoteCapability
		remoteTask = remoteTask || capability == domain.RunTypeDatabaseBackupTransfer
	}
	if remoteCap != options.RemoteDatabaseBackups || remoteTask != options.RemoteDatabaseBackups || remoteCap && (!options.LogicalDatabaseBackups || !options.Database || shellCapable) {
		return "", errors.New("authenticated remote backup capability does not match explicit configuration")
	}
	backupCapability, backupTask := false, false
	for _, capability := range result.Capabilities {
		backupCapability = backupCapability || capability == domain.HostingDatabaseBackupCapability
		backupTask = backupTask || capability == domain.RunTypeDatabaseBackup
	}
	if backupCapability != options.LogicalDatabaseBackups || backupTask != options.LogicalDatabaseBackups || backupCapability && (!options.Database || shellCapable) {
		return "", errors.New("authenticated logical database backup capability does not match explicit configuration")
	}
	archiveCapability, archiveTask := false, false
	for _, capability := range result.Capabilities {
		archiveCapability = archiveCapability || capability == domain.HostingStorageArchiveCapability
		archiveTask = archiveTask || capability == domain.RunTypeStorageArchive
	}
	storageRemoteCap, storageRemoteTask := false, false
	for _, capability := range result.Capabilities {
		storageRemoteCap = storageRemoteCap || capability == domain.HostingStorageBackupRemoteCapability
		storageRemoteTask = storageRemoteTask || capability == domain.RunTypeStorageBackupTransfer
	}
	storageDeletionCap := false
	storageRetentionCap, storageRetentionTask := false, false
	for _, capability := range result.Capabilities {
		storageDeletionCap = storageDeletionCap || capability == domain.HostingStorageBackupDeletionCapability
		storageRetentionCap = storageRetentionCap || capability == domain.HostingStorageBackupRetentionCapability
		storageRetentionTask = storageRetentionTask || capability == domain.RunTypeStorageBackupRetention
	}
	if storageDeletionCap && !options.StorageBackupRetention {
		return "", errors.New("storage deletion capability requires explicit retention opt-in")
	}
	if storageRetentionCap != options.StorageBackupRetention || storageRetentionTask != options.StorageBackupRetention || storageRetentionCap && (!options.StorageArchive || shellCapable) {
		return "", errors.New("storage retention capability does not match configuration")
	}
	if storageRemoteCap != options.RemoteStorageBackups || storageRemoteTask != options.RemoteStorageBackups || storageRemoteCap && (!options.StorageArchive || shellCapable) {
		return "", errors.New("storage remote backup capability mismatch")
	}
	composeArchiveCap, composeRemoteCap, stackCapable := false, false, false
	for _, capability := range result.Capabilities {
		composeArchiveCap = composeArchiveCap || capability == domain.HostingComposeStorageArchiveCapability
		composeRemoteCap = composeRemoteCap || capability == domain.HostingComposeStorageRemoteCapability
		stackCapable = stackCapable || capability == domain.HostingStackCapability
	}
	if composeRemoteCap != composeArchiveCap {
		return "", errors.New("authenticated Compose remote archive capability does not match explicit configuration")
	}
	if composeArchiveCap && (!options.StorageArchive || !options.RuntimeEnvironment || !stackCapable) {
		return "", errors.New("authenticated Compose archive capability requires configured archive and stack execution")
	}
	if archiveCapability != options.StorageArchive || archiveTask != options.StorageArchive || archiveCapability && (shellCapable || !storageCapable) {
		return "", errors.New("authenticated storage archive capability does not match explicit configuration")
	}
	if private != privateCapable {
		return "", errors.New("authenticated private Git capability does not match explicit configuration")
	}
	if (buildConfigured || localBuildConfigured) && shellCapable {
		return "", errors.New("authenticated shell capability cannot share configured build execution")
	}
	if proxyCapable != proxyConfigured {
		return "", errors.New("authenticated proxy_apply capability does not match explicit local proxy configuration")
	}
	if buildCapable != buildConfigured {
		return "", errors.New("authenticated hosting_build_v1 capability does not match explicit local build configuration")
	}
	if localBuildCapable != localBuildConfigured {
		return "", errors.New("authenticated local_build_deploy_v1 capability does not match explicit local configuration")
	}
	return result.RunnerID, nil
}

type proxyOperationResponse domain.HostingProxyOperation

func (r *proxyOperationResponse) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, (128<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 128<<10 {
		return errors.New("proxy response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(r); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("proxy response requires one JSON document")
	}
	return nil
}
func (c Client) proxyPost(ctx context.Context, path string, in any) (domain.HostingProxyOperation, error) {
	var response proxyOperationResponse
	if err := c.post(ctx, "/api/v1/runners/proxy/"+path, in, &response); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return domain.HostingProxyOperation(response), nil
}

// ProxyPlan returns typed input for an exact live proxy attempt.
func (c Client) ProxyPlan(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "plan", in)
}

// ProxyPrepared commits local nonsecret engine identities before admission.
func (c Client) ProxyPrepared(ctx context.Context, in domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "prepared", in)
}

// ProxyApplyBegin acquires the independent durable proxy hold before engine effects.
func (c Client) ProxyApplyBegin(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "apply-begin", in)
}

// ProxyComplete reports durable external effects using full fenced authority.
func (c Client) ProxyComplete(ctx context.Context, in domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "complete", in)
}

// ProxyStatus reads exact persisted attempt state without restoring authority.
func (c Client) ProxyStatus(ctx context.Context, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return c.proxyPost(ctx, "status", in)
}
