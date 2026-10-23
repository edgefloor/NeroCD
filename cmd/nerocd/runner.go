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
	hostingObservation := fs.Bool("hosting-observation", false, "enable independent read-only hosting status collection over HTTPS")
	hostingRuntimeEnv := fs.Bool("hosting-runtime-env", false, "explicitly enable protected hosting runtime environment delivery over HTTPS")
	localBuildDeploy := fs.Bool("local-build-deploy", false, "explicitly enable same-host local build artifact deployment")
	imagePolicyFlag := fs.String("compose-image-policy", os.Getenv("NEROCD_COMPOSE_IMAGE_POLICY"), "runner-owned Compose image policy: preloaded or pull")
	completeStatus := fs.String("complete-status", "", "optional status to immediately complete a claimed lease: succeeded, failed, or canceled")
	if err := fs.Parse(args); err != nil {
		return err
	}
	buildConfig, err := loadRunnerBuildConfig(*buildConfigFile)
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
		enrollmentToken, credential, requestID, err := prepareRunnerEnrollmentFiles(enrollmentPath, credentialPath)
		if err != nil {
			return err
		}
		credentialSum := sha256.Sum256([]byte(credential))
		consume := app.RunnerEnrollmentConsumeInput{RequestID: requestID, CredentialHash: hex.EncodeToString(credentialSum[:])}
		var consumed app.ConsumedRunnerEnrollment
		var consumeErr error
		backoff := 100 * time.Millisecond
		for attempt := 0; attempt < 5; attempt++ {
			consumeErr = postAPIInto(*server+"/api/v1/runner-enrollments/consume", consume, enrollmentToken, &consumed)
			if consumeErr == nil || classifyRunnerFailure(consumeErr) != runnerFailureTransient {
				break
			}
			time.Sleep(backoff)
			backoff *= 2
		}
		if consumeErr != nil {
			return fmt.Errorf("consume runner enrollment: %w", consumeErr)
		}
		if strings.TrimSpace(consumed.Runner.ID) == "" {
			return errors.New("runner enrollment response did not include identity")
		}
		if err := removeRunnerEnrollmentFile(enrollmentPath); err != nil {
			return fmt.Errorf("remove consumed enrollment file: %w", err)
		}
		runnerToken = credential
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
		if err := postAPIInto(*server+"/api/v1/runners/register", registerBody, registrationToken, &registered); err != nil {
			return err
		}
		runnerToken = strings.TrimSpace(registered.Token)
		if runnerToken == "" {
			return errors.New("runner registration did not return a runner token")
		}
	}
	// Resolve credential authority before runtime can select, open, or replay
	// a journal. Enrollment IDs and local defaults do not replace this read.
	identityContext := context.Background()
	if *hostingObservation || *hostingRuntimeLogs {
		var stop context.CancelFunc
		identityContext, stop = signal.NotifyContext(identityContext, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	effectiveRunnerID, err := runnerclient.New(*server, runnerToken).RuntimeExecutionIdentityWithOptions(identityContext, runnerclient.RuntimeExecutionOptions{Proxy: proxyConfig.Enabled(), ProxyVerifiedSwitch: *proxyVerifiedSwitch, Build: buildConfig != nil, LocalBuild: *localBuildDeploy, PrivateGit: buildConfig != nil && buildConfig.PrivateGit != nil, RuntimeEnvironment: *hostingRuntimeEnv, Observation: *hostingObservation, RuntimeLogs: *hostingRuntimeLogs})
	if err != nil {
		return fmt.Errorf("resolve runner identity: %w", err)
	}
	if (idSpecified || buildConfig != nil) && strings.TrimSpace(*id) != effectiveRunnerID {
		return errors.New("runner --id does not match authenticated runner identity")
	}
	runtimeConfig := runnerruntime.Config{HostingRuntimeLogs: runtimeLogs, HostingObservation: observation, RuntimeEnvironment: runtimeEnvironment, LocalBuildDeploy: *localBuildDeploy, Build: buildConfig, ImageDockerEndpoint: *imageDockerEndpoint, Proxy: proxyConfig,
		Server: *server, Token: runnerToken, RunnerID: effectiveRunnerID, WorkDir: *workDir, JournalDir: *journalDir,
		SecretRoot: *secretRoot, ImagePolicy: *imagePolicyFlag, Execute: *execute, Once: *once, PollInterval: *pollInterval,
		CancelPollInterval: *cancelPollInterval, CompleteStatus: *completeStatus,
	}
	if *hostingObservation || *hostingRuntimeLogs {
		return runnerruntime.RunContext(identityContext, runtimeConfig)
	}
	if buildConfig != nil || *localBuildDeploy || *hostingRuntimeEnv {
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
		if has(domain.HostingRuntimeEnvironmentCapability) {
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
	return capabilities, nil
}
