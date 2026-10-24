package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"nerocd/internal/runnerclient"
	"nerocd/internal/runnerruntime"
)

var runnerDatabaseCaptureID = regexp.MustCompile(`^hnc_[0-9a-f]{32}$`)

func validateRunnerDatabaseCaptureFlags(id string, execute bool, complete string, explicit map[string]bool) error {
	if !runnerDatabaseCaptureID.MatchString(id) || !execute || complete != "" {
		return errors.New("capture requires one exact ID and execution without generic completion")
	}
	allowed := map[string]bool{"database-network-capture-id": true, "server": true, "id": true, "credential-file": true, "journal-dir": true, "database-state-root": true, "database-secret-root": true, "image-docker-endpoint": true, "execute": true, "once": true}
	for flag := range explicit {
		if !allowed[flag] {
			return errors.New("capture cannot share registration or other execution options")
		}
	}
	for _, flag := range []string{"credential-file", "journal-dir", "database-state-root", "database-secret-root"} {
		if !explicit[flag] {
			return errors.New("capture requires explicit credential, journal and persistent root flags")
		}
	}
	return nil
}

func runRunnerDatabaseNetworkCapture(id, server, runnerID, credential, journal, state, secret, endpoint string, execute bool, complete string, explicit map[string]bool) error {
	if err := validateRunnerDatabaseCaptureFlags(id, execute, complete, explicit); err != nil {
		return err
	}
	if strings.TrimSpace(credential) != credential || credential == "" || strings.TrimSpace(journal) != journal || journal == "" {
		return errors.New("capture requires explicit credential and journal")
	}
	if runnerclient.ValidateRuntimeEnvironmentServer(server, runnerclient.RuntimeEnvironmentTransport{}) != nil {
		return errors.New("capture requires verified HTTPS")
	}
	engine, err := runnerruntime.NewDatabaseEngineConfig(state, secret)
	if err != nil {
		return errors.New("capture persistent configuration unavailable")
	}
	token, err := readRunnerCredentialFile(credential)
	if err != nil {
		return errors.New("capture credential unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{})
	request, cancel := context.WithTimeout(ctx, 5*time.Second)
	authenticated, err := runnerclient.New(server, token).RuntimeExecutionIdentityWithOptions(request, runnerclient.RuntimeExecutionOptions{DatabaseNetworkCapture: true})
	cancel()
	if err != nil || explicit["id"] && runnerID != authenticated {
		return errors.New("capture runner identity not confirmed")
	}
	return runnerruntime.RunContext(ctx, runnerruntime.Config{DatabaseNetworkCapture: &runnerruntime.DatabaseNetworkCaptureExecutionConfig{CaptureID: id, Engine: engine}, Server: server, Token: token, RunnerID: authenticated, JournalDir: journal, ImageDockerEndpoint: endpoint, Execute: true})
}
