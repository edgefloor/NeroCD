package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
	"nerocd/internal/runnerruntime"
)

type runnerRegisterRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Tags         []string `json:"tags"`
	Capabilities []string `json:"capabilities"`
}

type runnerRegisterResponse struct {
	Runner domain.Runner `json:"runner"`
	Token  string        `json:"token"`
}

func runRunner(args []string) error {
	return runRunnerWithBuildConfigLoader(args, loadRunnerBuildConfig)
}

func runRunnerWithBuildConfigLoader(args []string, loadBuildConfig func(string) (*runnerruntime.BuildConfig, error)) error {
	fs := flag.NewFlagSet("runner", flag.ExitOnError)
	server := fs.String("server", defaultAPIAddr(), "NeroCD server URL")
	token := fs.String("token", os.Getenv("NEROCD_TOKEN"), "privileged bearer token used only to register this runner")
	credentialFile := fs.String("credential-file", os.Getenv("NEROCD_RUNNER_CREDENTIAL_FILE"), "mode-0600 file containing an already-issued dedicated runner credential; skips registration")
	enrollmentFile := fs.String("enrollment-file", "", "mode-0600 file containing a one-time enrollment token; requires --credential-file as the durable credential destination")
	id := fs.String("id", "runner_local", "runner id")
	name := fs.String("name", "Local Runner", "runner name")
	tags := fs.String("tags", "local", "comma-separated runner tags")
	capabilities := fs.String("capabilities", "shell", "comma-separated runner capabilities")
	execute := fs.Bool("execute", true, "execute the claimed process plan")
	once := fs.Bool("once", false, "claim at most one run and then exit")
	pollInterval := fs.Duration("poll-interval", 5*time.Second, "delay between claim attempts")
	cancelPollInterval := fs.Duration("cancel-poll-interval", time.Second, "delay between lease cancellation checks while executing")
	workDir := fs.String("work-dir", os.TempDir(), "runner workspace root for checkouts")
	journalDir := fs.String("journal-dir", os.Getenv("NEROCD_RUNNER_JOURNAL_DIR"), "owner-only durable runner event/completion journal directory")
	secretRoot := fs.String("secret-root", os.Getenv("NEROCD_RUNNER_SECRET_ROOT"), "owner-only mode-0700 root for logical runner_file secrets")
	imageDockerEndpoint := fs.String("image-docker-endpoint", "unix:///var/run/docker.sock", "explicit local Docker Unix socket for image hosting")
	proxyRoot := fs.String("proxy-state-root", "", "explicit persistent mode-0700 Linux managed proxy root")
	proxyBinary := fs.String("proxy-docker-binary", "", "absolute managed proxy Docker executable path")
	proxySocket := fs.String("proxy-docker-socket", "", "absolute managed proxy Docker Unix socket path")
	proxyProtocol := fs.String("proxy-protocol", "", "explicit managed proxy protocol managed-traefik.file.v1")
	proxyVerifiedSwitch := fs.Bool("proxy-verified-switch", false, "explicitly enable verified existing-route switching")
	buildConfigFile := fs.String("build-config-file", "", "explicit owned mode0600 JSON build execution configuration; disabled when absent")
	hostingRuntimeLogs := fs.Bool("hosting-runtime-logs", false, "enable demand-driven hosting runtime log collection over HTTPS")
	hostingReadiness := fs.Bool("hosting-readiness", false, "enable read-only basic image hosting readiness diagnostics over HTTPS")
	hostingObservation := fs.Bool("hosting-observation", false, "enable independent read-only hosting status collection over HTTPS")
	databaseCaptureID := fs.String("database-network-capture-id", "", "perform one exact-ID read-only database origin capture and return")
	hostingDatabaseCapture := fs.Bool("hosting-database-network-capture", false, "explicitly select queued database origin captures before ordinary database work")
	hostingApplicationDatabaseStorage := fs.Bool("hosting-application-database-storage", false, "explicitly enable connected upload storage replacement with interruption")
	hostingApplicationDatabaseRouteContinuity := fs.Bool("hosting-application-database-route-continuity", false, "explicitly enable connected application verified HTTPS replacement")
	hostingApplicationDatabase := fs.Bool("hosting-application-database", false, "explicitly enable application database binding execution")
	hostingDatabaseMaintenance := fs.Bool("hosting-database-membership-maintenance", false, "explicitly enable member-preserving database replacement")
	hostingDatabaseUpgrade := fs.Bool("hosting-database-network-upgrade", false, "explicitly enable existing-volume empty database network upgrades")
	hostingDatabase := fs.Bool("hosting-database", false, "explicitly enable private database execution on a qualified native Linux arm64 host")
	storageDownloads := fs.Bool("hosting-storage-backup-download", false, "enable local archive streaming through one explicitly configured controller")
	storageArchiveFile := fs.String("storage-archive-config-file", "", "explicit owned mode0600 qualified native helper configuration; storage archive execution is disabled when absent")
	backupRetention := fs.Bool("hosting-database-backup-retention", false, "enable fenced exact-copy local and remote backup retention")
	remoteDatabaseBackups := fs.Bool("hosting-database-remote-backups", false, "enable fenced PostgreSQL and MySQL S3 copies and separate-database remote restores")
	remoteEndpoints := fs.String("hosting-backup-remote-endpoints", "", "operator-approved private HTTPS origins, comma-separated")
	remoteCIDRs := fs.String("hosting-backup-remote-cidrs", "", "operator-approved private endpoint CIDRs, comma-separated")
	remoteFixture := fs.String("hosting-backup-remote-trusted-loopback", "", "trusted exact loopback HTTP origin for isolated fixtures only")
	logicalDatabaseBackups := fs.Bool("hosting-database-backups", false, "explicitly enable local PostgreSQL and MySQL logical backups and held new-database restores")
	databaseStateRoot := fs.String("database-state-root", "", "persistent protected database engine state root")
	databaseSecretRoot := fs.String("database-secret-root", "", "persistent protected database credential root")
	hostingRegistryPull := fs.Bool("hosting-registry-pull", false, "explicitly enable protected private registry image pulls over HTTPS")
	hostingRuntimeEnv := fs.Bool("hosting-runtime-env", false, "explicitly enable protected hosting runtime environment delivery over HTTPS")
	localBuildDeploy := fs.Bool("local-build-deploy", false, "explicitly enable same-host local build artifact deployment")
	imagePolicyFlag := fs.String("compose-image-policy", os.Getenv("NEROCD_COMPOSE_IMAGE_POLICY"), "runner-owned Compose image policy: preloaded or pull")
	completeStatus := fs.String("complete-status", "", "optional status to immediately complete a claimed lease: succeeded, failed, or canceled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicitCaptureFlags := map[string]bool{}
	fs.Visit(func(option *flag.Flag) { explicitCaptureFlags[option.Name] = true })
	if explicitCaptureFlags["database-network-capture-id"] {
		return runRunnerDatabaseNetworkCapture(*databaseCaptureID, *server, *id, *credentialFile, *journalDir, *databaseStateRoot, *databaseSecretRoot, *imageDockerEndpoint, *execute, *completeStatus, explicitCaptureFlags)
	}
	databaseConfig, err := runnerDatabaseConfig(*hostingDatabase, *execute, *completeStatus, *databaseStateRoot, *databaseSecretRoot)
	if err != nil {
		return err
	}
	if err = runnerDatabaseUpgradeConfig(*hostingDatabaseUpgrade, databaseConfig); err != nil {
		return err
	}
	if err = runnerDatabaseCaptureConfig(*hostingDatabaseCapture, databaseConfig, *credentialFile, *journalDir, explicitCaptureFlags); err != nil {
		return err
	}
	if err = runnerApplicationDatabaseConfig(*hostingApplicationDatabase, *hostingDatabaseMaintenance, *hostingRuntimeEnv, databaseConfig); err != nil {
		return err
	}
	if *hostingApplicationDatabaseStorage {
		if databaseConfig == nil || !*hostingApplicationDatabase {
			return errors.New("connected upload storage requires connected execution")
		}
		databaseConfig.ApplicationStorage = true
	}
	if *hostingApplicationDatabaseRouteContinuity {
		if databaseConfig == nil || !*hostingApplicationDatabase || !*proxyVerifiedSwitch {
			return errors.New("route continuity requires connected execution and strict proxy switching")
		}
		databaseConfig.RouteContinuity = true
	}
	if *backupRetention {
		if databaseConfig == nil || !*remoteDatabaseBackups {
			return errors.New("backup retention requires --hosting-database-remote-backups")
		}
		databaseConfig.BackupRetention = true
	}
	if *remoteDatabaseBackups {
		if databaseConfig == nil || !*logicalDatabaseBackups {
			return errors.New("remote backups require --hosting-database-backups")
		}
		databaseConfig.RemoteBackups = true
	}
	if databaseConfig != nil {
		if *remoteEndpoints != "" {
			databaseConfig.RemoteEndpoints = strings.Split(*remoteEndpoints, ",")
		}
		if *remoteCIDRs != "" {
			databaseConfig.RemoteCIDRs = strings.Split(*remoteCIDRs, ",")
		}
		databaseConfig.RemoteFixtureOrigin = *remoteFixture
	} else if *remoteEndpoints != "" || *remoteCIDRs != "" || *remoteFixture != "" {
		return errors.New("remote network policy requires database execution")
	}
	if *logicalDatabaseBackups {
		if databaseConfig == nil {
			return errors.New("logical backups require configured database execution")
		}
		databaseConfig.LogicalBackups = true
	}
	archiveConfig, err := loadRunnerStorageArchiveConfig(*storageArchiveFile, *execute, *completeStatus)
	if err != nil {
		return err
	}
	if *storageDownloads && archiveConfig == nil {
		return errors.New("archive downloads require qualified storage archive configuration")
	}
	if archiveConfig != nil {
		archiveConfig.BackupDownloads = *storageDownloads
	}
	runnerContext := context.Background()
	if *hostingReadiness || *hostingDatabase || archiveConfig != nil {
		var stop context.CancelFunc
		runnerContext, stop = signal.NotifyContext(runnerContext, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	if archiveConfig != nil {
		runnerContext = runnerclient.WithStorageArchiveTransport(runnerContext, runnerclient.StorageArchiveTransport{})
		if err := runnerclient.ValidateRuntimeEnvironmentServer(*server, runnerclient.RuntimeEnvironmentTransport{}); err != nil {
			return err
		}
		if databaseConfig == nil || !databaseConfig.NetworkCapture {
			if err := runnerruntime.ValidateStorageArchiveExecution(runnerContext, *archiveConfig, *imageDockerEndpoint); err != nil {
				return err
			}
		}
	}
	if databaseConfig != nil {
		runnerContext = runnerclient.WithDatabaseTransport(runnerContext, runnerclient.DatabaseTransport{})
		if err := runnerclient.ValidateRuntimeEnvironmentServer(*server, runnerclient.RuntimeEnvironmentTransport{}); err != nil {
			return err
		}
		if !databaseConfig.NetworkCapture {
			if err := runnerruntime.ValidateDatabaseExecution(runnerContext, *databaseConfig, *imageDockerEndpoint); err != nil {
				return err
			}
		}
	}
	buildConfig, err := loadBuildConfig(*buildConfigFile)
	if err != nil {
		return err
	}
	if buildConfig != nil && buildConfig.PrivateGit != nil {
		if err := runnerclient.ValidateSourceCredentialServer(*server, runnerclient.SourceCredentialTransport{TrustedLoopbackHTTP: buildConfig.PrivateGit.TrustedLoopbackHTTP}); err != nil {
			return err
		}
	}
	if buildConfig != nil && (!*execute || *completeStatus != "") {
		return errors.New("build configuration requires --execute and forbids --complete-status")
	}
	if *localBuildDeploy && (!*execute || *completeStatus != "") {
		return errors.New("local build deployment requires execution and forbids --complete-status")
	}
	proxyConfig := runnerruntime.ProxyConfig{Directory: *proxyRoot, DockerBinary: *proxyBinary, DockerSocket: *proxySocket, Protocol: *proxyProtocol, VerifiedSwitch: *proxyVerifiedSwitch}
	if err := proxyConfig.Validate(); err != nil {
		return err
	}
	if proxyConfig.Enabled() && !*execute {
		return errors.New("managed proxy configuration requires --execute")
	}
	registrationCapabilities := splitCSV(*capabilities)
	capabilitiesSpecified := false
	fs.Visit(func(option *flag.Flag) {
		capabilitiesSpecified = capabilitiesSpecified || option.Name == "capabilities"
	})
	if proxyConfig.Enabled() && !capabilitiesSpecified {
		registrationCapabilities = []string{domain.RunTypeProxyApply}
	}
	proxyCapability := false
	for _, capability := range registrationCapabilities {
		proxyCapability = proxyCapability || capability == domain.RunTypeProxyApply
	}
	if proxyCapability && !proxyConfig.Enabled() {
		return errors.New("proxy_apply requires explicit managed proxy configuration")
	}
	if proxyConfig.Enabled() && !proxyCapability {
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeProxyApply)
	}
	registrationCapabilities, err = runnerProxyVerifiedSwitchCapabilities(*proxyVerifiedSwitch, proxyConfig.Enabled(), registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerBuildCapabilities(buildConfig, proxyConfig.Enabled(), capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerLocalBuildCapabilities(*localBuildDeploy, buildConfig != nil, proxyConfig.Enabled(), capabilitiesSpecified || buildConfig != nil && buildConfig.PrivateGit != nil, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerRegistryPullCapabilities(*hostingRegistryPull, *execute, *completeStatus, capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	var registryPull *runnerruntime.RegistryPullConfig
	if *hostingRegistryPull {
		registryPull = &runnerruntime.RegistryPullConfig{}
		if err := runnerclient.ValidateRuntimeEnvironmentServer(*server, runnerclient.RuntimeEnvironmentTransport{}); err != nil {
			return err
		}
	}
	registrationCapabilities, err = runnerRuntimeEnvironmentCapabilities(*hostingRuntimeEnv, *execute, *completeStatus, capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	var runtimeEnvironment *runnerruntime.RuntimeEnvironmentConfig
	if *hostingRuntimeEnv {
		runtimeEnvironment = &runnerruntime.RuntimeEnvironmentConfig{}
		if err := runnerclient.ValidateRuntimeEnvironmentServer(*server, runnerclient.RuntimeEnvironmentTransport{}); err != nil {
			return err
		}
	}
	registrationCapabilities, err = runnerObservationCapabilities(*hostingObservation, *execute, *once, *completeStatus, capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	var observation *runnerruntime.HostingObservationConfig
	if *hostingObservation {
		observation = &runnerruntime.HostingObservationConfig{}
		if err := runnerclient.ValidateObservationServer(*server, runnerclient.ObservationTransport{}); err != nil {
			return err
		}
	}
	registrationCapabilities, err = runnerRuntimeLogsCapabilities(*hostingRuntimeLogs, *execute, *once, *completeStatus, capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	var runtimeLogs *runnerruntime.HostingRuntimeLogsConfig
	if *hostingRuntimeLogs {
		runtimeLogs = &runnerruntime.HostingRuntimeLogsConfig{}
		if err := runnerclient.ValidateRuntimeLogServer(*server, runnerclient.RuntimeLogTransport{}); err != nil {
			return err
		}
	}
	registrationCapabilities, err = runnerDatabaseCapabilities(*hostingDatabase, capabilitiesSpecified || proxyConfig.Enabled() || buildConfig != nil || *localBuildDeploy || *hostingRuntimeEnv || *hostingObservation || *hostingRuntimeLogs, registrationCapabilities)
	if err != nil {
		return err
	}
	if *remoteDatabaseBackups {
		if *backupRetention {
			registrationCapabilities = append(registrationCapabilities, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability, domain.HostingDatabaseMySQLBackupRetentionCapability)
		}
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability, domain.HostingDatabaseMySQLBackupRemoteCapability, domain.HostingDatabaseMySQLRemoteRestoreCapability)
	}
	if *logicalDatabaseBackups {
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseMySQLBackupCapability)
		if *hostingRuntimeEnv {
			registrationCapabilities = append(registrationCapabilities, domain.HostingDatabaseBackupComponentCapability, domain.HostingDatabaseMySQLBackupComponentCapability)
		}
	}
	registrationCapabilities, err = runnerStorageArchiveCapabilities(archiveConfig != nil, capabilitiesSpecified || *hostingDatabase || proxyConfig.Enabled() || buildConfig != nil || *localBuildDeploy || *hostingRuntimeEnv || *hostingObservation || *hostingRuntimeLogs, registrationCapabilities)
	if err != nil {
		return err
	}
	if archiveConfig == nil || !archiveConfig.BackupRetention {
		for _, capability := range registrationCapabilities {
			if capability == domain.RunTypeStorageBackupRetention || capability == domain.HostingStorageBackupRetentionCapability || capability == domain.HostingStorageBackupDeletionCapability {
				return errors.New("storage retention capabilities require explicit archive retention opt-in")
			}
		}
	}
	if archiveConfig != nil && archiveConfig.BackupRetention {
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeStorageBackupRetention, domain.HostingStorageBackupRetentionCapability, domain.HostingStorageBackupDeletionCapability)
	}
	if archiveConfig != nil && archiveConfig.RemoteBackups {
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupRemoteCapability)
	}
	if *storageDownloads {
		registrationCapabilities = append(registrationCapabilities, domain.RunTypeStorageBackupTransfer, domain.HostingStorageBackupDownloadCapability)
	} else {
		for _, capability := range registrationCapabilities {
			if capability == domain.HostingStorageBackupDownloadCapability {
				return errors.New("archive download capability requires explicit opt-in")
			}
		}
	}
	registrationCapabilities, err = runnerComposeStorageRemoteCapabilities(registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerDatabaseCaptureCapabilities(*hostingDatabaseCapture, *hostingDatabase, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerDatabaseUpgradeCapabilities(*hostingDatabaseUpgrade, *hostingDatabase, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerApplicationDatabaseCapabilities(*hostingApplicationDatabase, *hostingDatabaseMaintenance, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerApplicationDatabaseStorageCapabilities(*hostingApplicationDatabaseStorage, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerApplicationDatabaseRouteCapabilities(*hostingApplicationDatabaseRouteContinuity, registrationCapabilities)
	if err != nil {
		return err
	}
	registrationCapabilities, err = runnerReadinessCapabilities(*hostingReadiness, *execute, *once, *completeStatus, capabilitiesSpecified, registrationCapabilities)
	if err != nil {
		return err
	}
	var readiness *runnerruntime.HostingReadinessConfig
	if *hostingReadiness {
		readiness = &runnerruntime.HostingReadinessConfig{}
		if err := runnerclient.ValidateServerReadinessServer(*server, runnerclient.ServerReadinessTransport{}); err != nil {
			return err
		}
	}
	if *pollInterval <= 0 {
		return errors.New("runner --poll-interval must be greater than zero")
	}
	if *cancelPollInterval <= 0 {
		return errors.New("runner --cancel-poll-interval must be greater than zero")
	}
	if err := validateRunnerOperatingGuardrails(*server, *workDir); err != nil {
		return err
	}
	if err := validateRunnerSecretRoot(*secretRoot); err != nil {
		return err
	}
	registrationToken := strings.TrimSpace(*token)
	credentialPath := strings.TrimSpace(*credentialFile)
	enrollmentPath := strings.TrimSpace(*enrollmentFile)
	if registrationToken != "" && (credentialPath != "" || enrollmentPath != "") {
		return errors.New("runner --token registration mode is mutually exclusive with credential and enrollment files")
	}
	if enrollmentPath != "" && credentialPath == "" {
		return errors.New("runner --enrollment-file requires --credential-file")
	}
	var runnerToken string
	idSpecified := false
	fs.Visit(func(option *flag.Flag) {
		if option.Name == "id" {
			idSpecified = true
		}
	})
	if buildConfig != nil {
		if idSpecified && *id != buildConfig.Target.RunnerID {
			return errors.New("runner --id does not match configured build target")
		}
		*id = buildConfig.Target.RunnerID
	}
	if enrollmentPath != "" {
		enrollment, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
		if err != nil {
			return err
		}
		defer enrollment.close()
		credentialSum := sha256.Sum256([]byte(enrollment.credential))
		consume := app.RunnerEnrollmentConsumeInput{RequestID: enrollment.requestID, CredentialHash: hex.EncodeToString(credentialSum[:])}
		var consumed app.ConsumedRunnerEnrollment
		var consumeErr error
		backoff := 100 * time.Millisecond
		if *hostingReadiness || *hostingDatabase || archiveConfig != nil {
			consumeErr = postRunnerBootstrap(runnerContext, *hostingDatabase || archiveConfig != nil, *hostingReadiness, *server, "/api/v1/runner-enrollments/consume", consume, enrollment.token, &consumed)
		} else {
			for attempt := 0; attempt < 5; attempt++ {
				consumeErr = postAPIInto(*server+"/api/v1/runner-enrollments/consume", consume, enrollment.token, &consumed)
				if consumeErr == nil || classifyRunnerFailure(consumeErr) != runnerFailureTransient {
					break
				}
				time.Sleep(backoff)
				backoff *= 2
			}
		}
		if consumeErr != nil {
			return fmt.Errorf("consume runner enrollment: %w", consumeErr)
		}
		if strings.TrimSpace(consumed.Runner.ID) == "" {
			return errors.New("runner enrollment response did not include identity")
		}
		if err := enrollment.remove(); err != nil {
			return fmt.Errorf("remove consumed enrollment file: %w", err)
		}
		enrollment.close()
		runnerToken = enrollment.credential
	} else if credentialPath != "" {
		credential, err := readRunnerCredentialFile(credentialPath)
		if err != nil {
			return fmt.Errorf("runner credential file: %w", err)
		}
		runnerToken = credential
	} else {
		if registrationToken == "" {
			return errors.New("runner requires registration --token/NEROCD_TOKEN or --credential-file/NEROCD_RUNNER_CREDENTIAL_FILE")
		}
		registerBody := runnerRegisterRequest{ID: *id, Name: *name, Tags: splitCSV(*tags), Capabilities: registrationCapabilities}
		var registered runnerRegisterResponse
		registerErr := postRunnerBootstrap(runnerContext, *hostingDatabase || archiveConfig != nil, *hostingReadiness, *server, "/api/v1/runners/register", registerBody, registrationToken, &registered)
		if registerErr != nil {
			return registerErr
		}
		runnerToken = strings.TrimSpace(registered.Token)
		if runnerToken == "" {
			return errors.New("runner registration did not return a runner token")
		}
	}
	// Resolve credential authority before runtime can select, open, or replay
	// a journal. Enrollment IDs and local defaults do not replace this read.
	identityContext := runnerContext
	if !*hostingReadiness && (*hostingObservation || *hostingRuntimeLogs) {
		var stop context.CancelFunc
		identityContext, stop = signal.NotifyContext(identityContext, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	effectiveRunnerID, err := runnerclient.New(*server, runnerToken).RuntimeExecutionIdentityWithOptions(identityContext, runnerclient.RuntimeExecutionOptions{StorageBackupDownloads: *storageDownloads, StorageBackupRetention: archiveConfig != nil && archiveConfig.BackupRetention, DatabaseBackupRetention: *backupRetention, RemoteDatabaseBackups: *remoteDatabaseBackups, LogicalDatabaseBackups: *logicalDatabaseBackups, ApplicationDatabaseStorage: *hostingApplicationDatabaseStorage, ApplicationDatabaseRouteContinuity: *hostingApplicationDatabaseRouteContinuity, ApplicationDatabase: *hostingApplicationDatabase, DatabaseMaintenance: *hostingDatabaseMaintenance, StorageArchive: archiveConfig != nil, RemoteStorageBackups: archiveConfig != nil && archiveConfig.RemoteBackups, Database: *hostingDatabase, DatabaseNetworkCaptureAutomatic: *hostingDatabaseCapture, DatabaseUpgrade: *hostingDatabaseUpgrade, Proxy: proxyConfig.Enabled(), ProxyVerifiedSwitch: *proxyVerifiedSwitch, Build: buildConfig != nil, ComposeBuild: buildConfig != nil && buildConfig.Compose, LocalBuild: *localBuildDeploy, PrivateGit: buildConfig != nil && buildConfig.PrivateGit != nil, RegistryPull: *hostingRegistryPull, RuntimeEnvironment: *hostingRuntimeEnv, Observation: *hostingObservation, RuntimeLogs: *hostingRuntimeLogs, Readiness: *hostingReadiness})
	if err != nil {
		return fmt.Errorf("resolve runner identity: %w", err)
	}
	if (idSpecified || buildConfig != nil) && strings.TrimSpace(*id) != effectiveRunnerID {
		return errors.New("runner --id does not match authenticated runner identity")
	}
	runtimeConfig := runnerruntime.Config{StorageArchive: archiveConfig, Database: databaseConfig, HostingReadiness: readiness, HostingRuntimeLogs: runtimeLogs, HostingObservation: observation, RegistryPull: registryPull, RuntimeEnvironment: runtimeEnvironment, LocalBuildDeploy: *localBuildDeploy, Build: buildConfig, ImageDockerEndpoint: *imageDockerEndpoint, Proxy: proxyConfig,
		Server: *server, Token: runnerToken, RunnerID: effectiveRunnerID, WorkDir: *workDir, JournalDir: *journalDir,
		SecretRoot: *secretRoot, ImagePolicy: *imagePolicyFlag, Execute: *execute, Once: *once, PollInterval: *pollInterval,
		CancelPollInterval: *cancelPollInterval, CompleteStatus: *completeStatus,
	}
	if archiveConfig != nil || *hostingDatabase || *hostingObservation || *hostingRuntimeLogs || *hostingReadiness {
		return runnerruntime.RunContext(identityContext, runtimeConfig)
	}
	if runnerNeedsSignalContext(buildConfig != nil, *localBuildDeploy, *hostingRuntimeEnv, *proxyVerifiedSwitch) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runnerruntime.RunContext(ctx, runtimeConfig)
	}
	return runnerruntime.Run(runtimeConfig)
}

func validateRunnerSecretRoot(root string) error {
	root = strings.TrimSpace(root)
	if root != "" && !filepath.IsAbs(root) {
		return errors.New("runner --secret-root must be an absolute path")
	}
	return nil
}

func runnerRuntimeEnvironmentCapabilities(enabled, execute bool, complete string, specified bool, capabilities []string) ([]string, error) {
	has := func(name string) bool {
		for _, c := range capabilities {
			if c == name {
				return true
			}
		}
		return false
	}
	if !enabled {
		if has(domain.HostingRuntimeEnvironmentCapability) || has(domain.HostingOwnedStorageRuntimeCapability) || has(domain.HostingStackCapability) || has(domain.HostingStackLifecycleCapability) {
			return nil, errors.New("hosting_runtime_env_v1 requires --hosting-runtime-env")
		}
		return capabilities, nil
	}
	if !execute || complete != "" {
		return nil, errors.New("runtime environment requires --execute and forbids --complete-status")
	}
	if !specified {
		var configured []string
		for _, c := range capabilities {
			if c != "shell" {
				configured = append(configured, c)
			}
		}
		capabilities = configured
		if !has(domain.RunTypeImageDeploy) {
			capabilities = append(capabilities, domain.RunTypeImageDeploy)
		}
		if !has(domain.HostingReleasePortsCapability) {
			capabilities = append(capabilities, domain.HostingReleasePortsCapability)
		}
	}
	if has("shell") || !has(domain.RunTypeImageDeploy) || !has(domain.HostingReleasePortsCapability) {
		return nil, errors.New("runtime environment requires image_deploy and image_release_ports_v1 without shell")
	}
	if !has(domain.HostingRuntimeEnvironmentCapability) {
		capabilities = append(capabilities, domain.HostingRuntimeEnvironmentCapability)
	}
	if !has(domain.HostingStackCapability) {
		capabilities = append(capabilities, domain.HostingStackCapability)
	}
	if !has(domain.HostingStackLifecycleCapability) {
		capabilities = append(capabilities, domain.HostingStackLifecycleCapability)
	}
	if !has(domain.HostingOwnedStorageCapability) {
		capabilities = append(capabilities, domain.HostingOwnedStorageCapability)
	}
	if !has(domain.HostingOwnedStorageRuntimeCapability) {
		capabilities = append(capabilities, domain.HostingOwnedStorageRuntimeCapability)
	}
	return capabilities, nil
}

func runnerRegistryPullCapabilities(enabled, execute bool, complete string, specified bool, caps []string) ([]string, error) {
	has := func(c string) bool {
		for _, v := range caps {
			if c == v {
				return true
			}
		}
		return false
	}
	if !enabled {
		if has(domain.HostingRegistryPullCapability) {
			return nil, errors.New("hosting_registry_pull_v1 requires --hosting-registry-pull")
		}
		return caps, nil
	}
	if !execute || complete != "" {
		return nil, errors.New("registry pull requires execution and forbids generic completion")
	}
	if !specified {
		var filtered []string
		for _, c := range caps {
			if c != "shell" {
				filtered = append(filtered, c)
			}
		}
		caps = filtered
		for _, c := range []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability} {
			if !has(c) {
				caps = append(caps, c)
			}
		}
	}
	if has("shell") || !has(domain.RunTypeImageDeploy) || !has(domain.HostingReleasePortsCapability) {
		return nil, errors.New("registry pull requires dedicated image execution")
	}
	if !has(domain.HostingRegistryPullCapability) {
		caps = append(caps, domain.HostingRegistryPullCapability)
	}
	return caps, nil
}
