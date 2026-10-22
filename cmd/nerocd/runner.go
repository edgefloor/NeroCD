package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	imagePolicyFlag := fs.String("compose-image-policy", os.Getenv("NEROCD_COMPOSE_IMAGE_POLICY"), "runner-owned Compose image policy: preloaded or pull")
	completeStatus := fs.String("complete-status", "", "optional status to immediately complete a claimed lease: succeeded, failed, or canceled")
	if err := fs.Parse(args); err != nil {
		return err
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
		registerBody := runnerRegisterRequest{ID: *id, Name: *name, Tags: splitCSV(*tags), Capabilities: splitCSV(*capabilities)}
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
	effectiveRunnerID, err := runnerclient.New(*server, runnerToken).Identity(context.Background())
	if err != nil {
		return fmt.Errorf("resolve runner identity: %w", err)
	}
	if idSpecified && strings.TrimSpace(*id) != effectiveRunnerID {
		return errors.New("runner --id does not match authenticated runner identity")
	}
	return runnerruntime.Run(runnerruntime.Config{ImageDockerEndpoint: *imageDockerEndpoint,
		Server: *server, Token: runnerToken, RunnerID: effectiveRunnerID, WorkDir: *workDir, JournalDir: *journalDir,
		SecretRoot: *secretRoot, ImagePolicy: *imagePolicyFlag, Execute: *execute, Once: *once, PollInterval: *pollInterval,
		CancelPollInterval: *cancelPollInterval, CompleteStatus: *completeStatus,
	})
}

func validateRunnerSecretRoot(root string) error {
	root = strings.TrimSpace(root)
	if root != "" && !filepath.IsAbs(root) {
		return errors.New("runner --secret-root must be an absolute path")
	}
	return nil
}
