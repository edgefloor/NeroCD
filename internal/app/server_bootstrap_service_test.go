package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerBootstrapOptionsAuthorizationAndDisabledState(t *testing.T) {
	s := newTestService()
	if _, e := s.ServerBootstrapOptions(t.Context(), "proj_platform"); !errors.Is(e, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous %v", e)
	}
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	o, e := s.ServerBootstrapOptions(ctx, "proj_platform")
	if e != nil {
		t.Fatal(e)
	}
	if o.Available || o.UnavailableReason != "disabled" || len(o.Artifacts) != 0 {
		t.Fatalf("disabled %+v", o)
	}
}
func TestServerBootstrapProjectionExcludesAuthorityAndCredentials(t *testing.T) {
	now := time.Now()
	s := &Service{clock: func() time.Time { return now }}
	op := domain.ServerBootstrapOperation{IDs: domain.ServerBootstrapIDs{OperationID: "sbo_one", RunnerID: "runner_private", EnrollmentID: "enrollment_private"}, Input: domain.ServerBootstrapInput{ProjectID: "project", SSHSecretID: "secret_private", SSHSecretVersion: 99, ControllerURL: "https://private-controller.invalid"}, CreatedBy: "private_actor", State: "reviewed", Version: 2, Commands: []domain.ServerBootstrapStage{{Command: domain.ServerBootstrapCommand{ID: "receipt", Key: "public-command", Action: "review", Review: &domain.ServerBootstrapReview{Manifest: []byte("private_manifest"), ManifestSHA256: "hash", ProbeExpiresAt: now.Add(time.Second)}}, LeaseID: "private_lease", Fence: 9, Receipt: &domain.ServerBootstrapReceipt{Outcome: "accepted"}}}}
	b, e := json.Marshal(s.bootstrapProjection(op, "private_actor"))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"private_actor", "private_lease", "private_manifest", "secret_private", "private-controller", "runner_private", "enrollment_private", "fence"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("projection leaks %s", secret)
		}
	}
	op.RestoreHeld = true
	view := s.bootstrapProjection(op, "private_actor")
	if len(view.AvailableActions) != 0 {
		t.Fatal("restore held action exposed")
	}
}
func TestServerBootstrapRenewalFailureCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan int, 1)
	started := make(chan struct{})
	var joined atomic.Bool
	go func() { close(started); <-ctx.Done(); time.Sleep(10 * time.Millisecond); joined.Store(true); done <- 1 }()
	<-started
	want := errors.New("authority revoked")
	_, e := waitServerBootstrapRemote(ctx, cancel, time.Millisecond, done, func() error { return want })
	if !errors.Is(e, want) || !joined.Load() {
		t.Fatalf("returned before join: %v joined=%v", e, joined.Load())
	}
}

func TestServerBootstrapArtifactQualification(t *testing.T) {
	path := t.TempDir() + "/runner"
	data := []byte("qualified-binary")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	good := ServerBootstrapConfig{ControllerOrigin: "https://controller.example", Artifacts: []ServerBootstrapArtifact{{ArtifactID: "qualified", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Version: "v1.2.3", Architecture: "arm64", Profile: "prepared_debian12_v1"}}}
	for _, test := range []struct {
		name   string
		mutate func(*ServerBootstrapConfig)
	}{{"valid", func(*ServerBootstrapConfig) {}}, {"digest", func(c *ServerBootstrapConfig) { c.Artifacts[0].SHA256 = strings.Repeat("0", 64) }}, {"architecture", func(c *ServerBootstrapConfig) { c.Artifacts[0].Architecture = "amd64" }}, {"profile", func(c *ServerBootstrapConfig) { c.Artifacts[0].Profile = "arbitrary" }}, {"http", func(c *ServerBootstrapConfig) { c.ControllerOrigin = "http://controller.example" }}} {
		t.Run(test.name, func(t *testing.T) {
			cfg := good
			cfg.Artifacts = append([]ServerBootstrapArtifact(nil), good.Artifacts...)
			test.mutate(&cfg)
			s := &Service{}
			err := s.ConfigureServerBootstrap(&cfg)
			if (test.name == "valid") != (err == nil) {
				t.Fatal(err)
			}
		})
	}
}
