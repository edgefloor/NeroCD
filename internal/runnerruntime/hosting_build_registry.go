package runnerruntime

import (
	"context"
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type buildRegistryClient interface {
	BuildRegistryCredential(context.Context, string, domain.HostingBuildRegistryCredentialRequest, runnerclient.SourceCredentialTransport) (*domain.HostingRegistryCredentialDelivery, error)
}
type buildMaterialsEngine interface {
	BuildWithMaterials(context.Context, hostingbuild.Request, hostingbuild.OutputSink, hostingbuild.BuildMaterialProviders) (hostingbuild.Result, error)
}

type buildSecretsClient interface {
	BuildSecrets(context.Context, string, domain.HostingBuildSecretsRequest, runnerclient.SourceCredentialTransport) (*domain.HostingBuildSecretsDelivery, error)
}

type buildRegistryEngine interface {
	BuildWithRegistryCredential(context.Context, hostingbuild.Request, hostingbuild.OutputSink, hostingbuild.RegistryCredentialProvider) (hostingbuild.Result, error)
}

func (e *BuildExecutor) runBuildEngine(ctx context.Context, b domain.HostingBuild, a domain.HostingBuildAuthority, intent string, request hostingbuild.Request, output *buildOutputAdapter) (hostingbuild.Result, error) {
	if b.Snapshot.Source.Settings.BuildRegistryCredential == nil && len(b.Snapshot.Source.Settings.BuildSecrets) == 0 {
		return e.engine.(buildOutputEngine).BuildWithOutput(ctx, request, output.Sink)
	}
	provider := func(c context.Context) (hostingbuild.RegistryCredential, error) {
		var zero hostingbuild.RegistryCredential
		client, ok := e.client.(buildRegistryClient)
		if !ok {
			return zero, errors.New("build registry client unavailable")
		}
		retained := false
		for _, entry := range e.journal.Snapshot().Build {
			if entry.ID == intent && entry.Phase == "attempt_intent" && buildAuthority(entry.Attempt, entry.BuildID) == a && entry.InputHash == domain.HostingDocumentHash(b.Snapshot) {
				retained = true
			}
		}
		if !retained {
			return zero, errors.New("build registry intent not durable")
		}
		live, err := e.status(c, a, &b.Snapshot)
		if err != nil || requireLive(live) != nil || live.Build.State != "building" || live.Build.DaemonID != b.DaemonID {
			return zero, errors.New("build registry authority lost")
		}
		id := domain.HostingDocumentHash(struct{ Intent, Build string }{intent, request.BuildID})[7:39]
		trusted := e.config.PrivateGit != nil && e.config.PrivateGit.TrustedLoopbackHTTP
		call, cancel := context.WithTimeout(c, e.config.RequestTimeout)
		material, err := client.BuildRegistryCredential(call, e.config.Target.RunnerID, domain.HostingBuildRegistryCredentialRequest{Authority: a, DeliveryID: id}, runnerclient.SourceCredentialTransport{TrustedLoopbackHTTP: trusted})
		cancel()
		if err != nil || material == nil {
			return zero, errors.New("build registry credential unavailable")
		}
		defer material.Close()
		if c.Err() != nil || !time.Now().Before(material.UseUntil) || material.UseUntil.After(time.Now().Add(time.Minute)) {
			return zero, errors.New("build registry delivery expired")
		}
		credential := material.Credential
		if err = output.addRegistrySecrets(c, credential); err != nil {
			return zero, err
		}
		return hostingbuild.RegistryCredential{Registry: credential.Registry, Username: credential.Username, Password: credential.Password}, nil
	}
	if len(b.Snapshot.Source.Settings.BuildSecrets) > 0 {
		engine, ok := e.engine.(buildMaterialsEngine)
		if !ok {
			return hostingbuild.Result{}, errors.New("managed build secrets engine unavailable")
		}
		providers := hostingbuild.BuildMaterialProviders{Secrets: e.buildSecretsProvider(b, a, intent, request, output)}
		if b.Snapshot.Source.Settings.BuildRegistryCredential != nil {
			providers.Registry = provider
		}
		return engine.BuildWithMaterials(ctx, request, output.Sink, providers)
	}
	engine, ok := e.engine.(buildRegistryEngine)
	if !ok {
		return hostingbuild.Result{}, errors.New("build registry engine unavailable")
	}
	return engine.BuildWithRegistryCredential(ctx, request, output.Sink, provider)
}

func (s *buildOutputAdapter) addRegistrySecrets(ctx context.Context, c domain.HostingRegistryCredential) error {
	// No engine command is running when its fresh material callback executes.
	// Flush already redacted source output before replacing the streaming matcher.
	if err := s.Finish(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range []string{c.Username, c.Password, c.Username + ":" + c.Password} {
		if value == "" || len(value) > 20256 {
			return errors.New("build registry redaction exceeds bound")
		}
		found := false
		for _, existing := range s.secrets {
			found = found || existing.Value == value
		}
		if !found {
			s.secrets = append(s.secrets, runner.SecretMaterial{Value: strings.Clone(value), Encodings: []string{"base64", "base64url", "hex"}})
		}
	}
	s.redactor = runner.NewRedactor(s.secrets)
	return nil
}
