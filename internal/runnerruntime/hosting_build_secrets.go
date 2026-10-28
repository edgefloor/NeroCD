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

func (e *BuildExecutor) buildSecretsProvider(b domain.HostingBuild, a domain.HostingBuildAuthority, intent string, request hostingbuild.Request, output *buildOutputAdapter) hostingbuild.SecretProvider {
	return func(ctx context.Context) ([]hostingbuild.SecretValue, error) {
		client, ok := e.client.(buildSecretsClient)
		if !ok {
			return nil, errors.New("build secrets client unavailable")
		}
		retained := false
		for _, entry := range e.journal.Snapshot().Build {
			if entry.ID == intent && entry.Phase == "attempt_intent" && buildAuthority(entry.Attempt, entry.BuildID) == a && entry.InputHash == domain.HostingDocumentHash(b.Snapshot) {
				retained = true
			}
		}
		if !retained {
			return nil, errors.New("build secret intent not durable")
		}
		live, err := e.status(ctx, a, &b.Snapshot)
		if err != nil || requireLive(live) != nil || live.Build.State != "building" || live.Build.DaemonID != b.DaemonID {
			return nil, errors.New("build secrets authority lost")
		}
		id := domain.HostingDocumentHash(struct{ Intent, Build string }{intent, request.BuildID})[7:39]
		trusted := e.config.PrivateGit != nil && e.config.PrivateGit.TrustedLoopbackHTTP
		call, cancel := context.WithTimeout(ctx, e.config.RequestTimeout)
		material, err := client.BuildSecrets(call, e.config.Target.RunnerID, domain.HostingBuildSecretsRequest{Authority: a, DeliveryID: id}, runnerclient.SourceCredentialTransport{TrustedLoopbackHTTP: trusted})
		cancel()
		if err != nil || material == nil {
			return nil, errors.Join(errors.New("build secrets unavailable"), ctx.Err())
		}
		defer material.Close()
		if ctx.Err() != nil || !time.Now().Before(material.UseUntil) || material.UseUntil.After(time.Now().Add(time.Minute)) || len(material.Secrets) != len(b.Snapshot.Source.Settings.BuildSecrets) {
			return nil, errors.Join(errors.New("build secrets delivery expired or changed"), ctx.Err())
		}
		selected := map[string]bool{}
		for _, ref := range b.Snapshot.Source.Settings.BuildSecrets {
			selected[ref.Name] = true
		}
		for _, value := range material.Secrets {
			if !selected[value.Name] || !domain.ValidHostingBuildSecretValue(value.Value) {
				return nil, errors.New("invalid build secret delivery")
			}
			delete(selected, value.Name)
		}
		if len(selected) != 0 {
			return nil, errors.New("build secret selection changed")
		}
		if err = output.addBuildSecrets(ctx, material.Secrets); err != nil {
			return nil, err
		}
		values := make([]hostingbuild.SecretValue, 0, len(material.Secrets))
		for _, value := range material.Secrets {
			values = append(values, hostingbuild.SecretValue{Name: value.Name, Value: value.Value})
		}
		return values, nil
	}
}

func (s *buildOutputAdapter) addBuildSecrets(ctx context.Context, values []domain.HostingBuildSecretValue) error {
	// The engine invokes fresh providers before starting commands. Flush prior
	// redacted source/registry output before rebuilding the bounded streaming matcher.
	if err := s.Finish(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(values) > 64 {
		return errors.New("too many build secrets")
	}
	for _, value := range values {
		if !domain.ValidHostingBuildSecretValue(value.Value) {
			return errors.New("invalid build secret redaction")
		}
		if value.Value == "" {
			continue
		}
		found := false
		for _, existing := range s.secrets {
			found = found || existing.Value == value.Value
		}
		if !found {
			s.secrets = append(s.secrets, runner.SecretMaterial{Value: strings.Clone(value.Value), Encodings: []string{"base64", "base64url", "hex"}})
		}
	}
	s.redactor = runner.NewRedactor(s.secrets)
	return nil
}
