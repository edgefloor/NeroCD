package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
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
	_, e := waitServerBootstrapRemote(ctx, cancel, time.Now().Add(time.Minute), time.Millisecond, done, func(context.Context) (time.Time, error) { return time.Time{}, want })
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

func TestServerBootstrapPostgresReviewedGrantAndEnrollmentResume(t *testing.T) {
	for _, grantProject := range []bool{false, true} {
		t.Run(fmt.Sprintf("grant_%t", grantProject), func(t *testing.T) {
			s := newPostgresDestinationTestService(t)
			s.secretKeyring = managedTestRing(t, "bootstrap")
			pg := s.hosting.(*store.PostgresStore)
			path := t.TempDir() + "/runner"
			data := []byte("fixture-artifact")
			if e := os.WriteFile(path, data, 0600); e != nil {
				t.Fatal(e)
			}
			digest := sha256.Sum256(data)
			cfg := &ServerBootstrapConfig{ControllerOrigin: "https://controller.example", Artifacts: []ServerBootstrapArtifact{{ArtifactID: "qualified", Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Version: "v1.2.3", Architecture: "arm64", Profile: "prepared_debian12_v1"}}}
			if e := s.ConfigureServerBootstrap(cfg); e != nil {
				t.Fatal(e)
			}
			ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
			secret, e := s.CreateManagedSecret(ctx, ManagedSecretInput{ProjectID: "proj_platform", Name: "bootstrap-key", Value: "fixture-private-key"})
			if e != nil {
				t.Fatal(e)
			}
			input := ServerBootstrapCreateInput{RequestID: "00000000-0000-4000-8000-000000000001", ProjectID: "proj_platform", Name: "prepared", Target: domain.ServerBootstrapTarget{IP: "192.0.2.10", Port: 22, User: "root", HostFingerprint: "SHA256:" + strings.Repeat("A", 43)}, SSHSecretID: secret.ID, SSHSecretVersion: 1, ArtifactID: "qualified"}
			view, e := s.CreateServerBootstrap(ctx, input)
			if e != nil {
				t.Fatal(e)
			}
			replayed, e := s.CreateServerBootstrap(ctx, input)
			if e != nil || replayed.ID != view.ID {
				t.Fatal("create response-loss replay", e)
			}

			page, e := s.ListServerBootstraps(ctx, "proj_platform", 1, 0)
			if e != nil || len(page.Items) != 1 || page.Items[0].ID != view.ID || page.HasMore {
				t.Fatalf("bootstrap SQLC list %+v %v", page, e)
			}
			command := ServerBootstrapCommandInput{CommandID: "00000000-0000-4000-8000-000000000002", ExpectedVersion: view.Version}
			view, e = s.CommandServerBootstrap(ctx, view.ID, "probe", command)
			if e != nil {
				t.Fatal(e)
			}
			op, e := pg.GetServerBootstrap(ctx, "usr_bootstrap", view.ID)
			if e != nil {
				t.Fatal(e)
			}
			stage, e := pg.ClaimServerBootstrapStage(ctx, "usr_bootstrap", view.ID, op.ActiveCommandID)
			if e != nil {
				t.Fatal(e)
			}
			receipt := domain.ServerBootstrapReceipt{CommandID: stage.Command.ID, LeaseID: stage.LeaseID, Attempt: stage.Attempt, Fence: stage.Fence, Outcome: "observed", Probe: &domain.ServerBootstrapProbeReceipt{Prerequisites: domain.ServerBootstrapPrerequisites{Profile: "prepared_debian12_v1", OSState: "available", OSID: "debian", OSVersion: "12", Architecture: "arm64", SystemdState: "available", SystemdStatus: "running", DockerState: "available", DockerAPIVersion: "1.54", ComposeState: "available", ComposeVersion: "5.1.1", DiskState: "available", DiskTotalBytes: 2 << 30, DiskAvailableBytes: 1 << 30}}}
			event, e := s.auditEvent(ctx, "usr_bootstrap", "server_bootstrap.stage_completed", view.ID, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = pg.CompleteServerBootstrapStage(ctx, "usr_bootstrap", view.ID, receipt, event); e != nil {
				t.Fatal(e)
			}
			view, e = s.GetServerBootstrap(ctx, view.ID)
			if e != nil {
				t.Fatal(e)
			}
			command = ServerBootstrapCommandInput{CommandID: "00000000-0000-4000-8000-000000000003", ExpectedVersion: view.Version, ProbeReceiptID: view.Probe.ReceiptID}
			view, e = s.CommandServerBootstrap(ctx, view.ID, "review", command)
			if e != nil {
				t.Fatal(e)
			}
			// Retrying exact review keeps its request hash, observation, and original deadline.
			if _, e = s.CommandServerBootstrap(ctx, view.ID, "review", command); e != nil {
				t.Fatal("review replay", e)
			}
			command = ServerBootstrapCommandInput{CommandID: "00000000-0000-4000-8000-000000000004", ExpectedVersion: view.Version, ManifestSHA256: view.Review.ManifestSHA256, AcknowledgeInstallAndStart: true}
			if _, e = s.CommandServerBootstrap(ctx, view.ID, "install", command); !errors.Is(e, ErrInvalidInput) {
				t.Fatal("omitted grant choice accepted", e)
			}
			command.GrantProject = &grantProject
			view, e = s.CommandServerBootstrap(ctx, view.ID, "install", command)
			if e != nil {
				t.Fatal(e)
			}
			op, e = pg.GetServerBootstrap(ctx, "usr_bootstrap", view.ID)
			if e != nil {
				t.Fatal(e)
			}
			stage, e = pg.ClaimServerBootstrapStage(ctx, "usr_bootstrap", view.ID, op.ActiveCommandID)
			if e != nil {
				t.Fatal(e)
			}
			event, e = s.auditEvent(ctx, "usr_bootstrap", "server_bootstrap.delivery", view.ID, nil)
			if e != nil {
				t.Fatal(e)
			}
			delivery, e := pg.AuthorizeServerBootstrapDelivery(ctx, "usr_bootstrap", view.ID, stage, s.secretKeyring, event)
			if e != nil {
				t.Fatal(e)
			}
			receipt = domain.ServerBootstrapReceipt{CommandID: stage.Command.ID, LeaseID: stage.LeaseID, Attempt: stage.Attempt, Fence: stage.Fence, Outcome: "installed", ManifestSHA256: op.Review.ManifestSHA256, RemoteOperationID: op.IDs.InstallOperationID, Owned: &domain.ServerBootstrapOwnedReceipt{InstallationID: op.IDs.InstallationID, ManifestSHA256: op.Review.ManifestSHA256, OperationID: op.IDs.InstallOperationID, State: "installed", ArtifactMatches: true, ServiceActive: true}}
			event, e = s.auditEvent(ctx, "usr_bootstrap", "server_bootstrap.stage_completed", view.ID, nil)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = pg.CompleteServerBootstrapStage(ctx, "usr_bootstrap", view.ID, receipt, event); e != nil {
				t.Fatal(e)
			}

			lost := errors.New("registration request response lost")
			s.hosting = &bootstrapResponseLossRepository{PostgresStore: pg, lost: lost}
			if e = s.ProcessServerBootstrap(t.Context(), view.ID); !errors.Is(e, lost) {
				t.Fatal("missing simulated response loss", e)
			}

			view, e = s.GetServerBootstrap(ctx, view.ID)
			if e != nil || view.State != "installed_waiting_enrollment" {
				t.Fatal("install invented enrollment", view.State, e)
			}
			credentialHash := sha256.Sum256([]byte("runner-credential"))
			if _, e = s.ConsumeRunnerEnrollment(t.Context(), string(delivery.EnrollmentToken), RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + strings.Repeat("1", 32), CredentialHash: hex.EncodeToString(credentialHash[:])}); e != nil {
				t.Fatal(e)
			}
			for range 2 {
				if e = s.ProcessServerBootstrap(t.Context(), view.ID); e != nil {
					t.Fatal("registration resume", e)
				}
			}
			view, e = s.GetServerBootstrap(ctx, view.ID)
			if e != nil {
				t.Fatal(e)
			}
			want := "registered"
			if grantProject {
				want = "completed"
			}
			if view.State != want || view.ServerID == "" || view.DestinationID == "" || view.ReadinessState != "check_server_readiness" {
				t.Fatalf("registration/project grant %+v", view)
			}
			if !grantProject {
				view, e = s.CommandServerBootstrap(ctx, view.ID, "grant", ServerBootstrapCommandInput{CommandID: "00000000-0000-4000-8000-000000000005", ExpectedVersion: view.Version})
				if e != nil {
					t.Fatal(e)
				}
				if e = s.ProcessServerBootstrap(t.Context(), view.ID); e != nil {
					t.Fatal(e)
				}
				view, e = s.GetServerBootstrap(ctx, view.ID)
				if e != nil || view.State != "completed" {
					t.Fatal("explicit grant", e)
				}
			}

			s.serverBootstrap.Artifacts[0].Version = "v99.99.99"
			view, e = s.GetServerBootstrap(ctx, view.ID)
			if e != nil || view.Artifact.Version != "v1.2.3" || view.Artifact.ArtifactID != input.ArtifactID || view.Artifact.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("saved artifact changed with current inventory", view.Artifact, e)
			}
			document, e := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
			if e != nil {
				t.Fatal(e)
			}
			raw, e := json.Marshal(view)
			if e != nil {
				t.Fatal(e)
			}
			var value any
			if e = json.Unmarshal(raw, &value); e != nil {
				t.Fatal(e)
			}
			if e = document.Components.Schemas["ServerBootstrap"].Value.VisitJSON(value); e != nil {
				t.Fatal("response schema", e)
			}
			for _, forbidden := range []string{"fixture-private-key", string(delivery.EnrollmentToken), secret.ID, "lease_id", "manifest\"", "actor_id"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("public workflow leaked %s", forbidden)
				}
			}
		})
	}
}

type bootstrapResponseLossRepository struct {
	*store.PostgresStore
	lost error
}

func (r *bootstrapResponseLossRepository) RequestServerBootstrapCommand(ctx context.Context, actor, id string, in domain.ServerBootstrapCommand, event domain.AuditEvent) (domain.ServerBootstrapCommand, error) {
	command, err := r.PostgresStore.RequestServerBootstrapCommand(ctx, actor, id, in, event)
	if err == nil && in.Action == "register_server" && r.lost != nil {
		err = r.lost
		r.lost = nil
	}
	return command, err
}

type bootstrapCompletedInspectionRepository struct {
	store.HostingRepository
	store.ServerBootstrapRepository
	op    domain.ServerBootstrapOperation
	calls int
}

func (r *bootstrapCompletedInspectionRepository) GetServerBootstrap(context.Context, string, string) (domain.ServerBootstrapOperation, error) {
	return r.op, nil
}
func (r *bootstrapCompletedInspectionRepository) ServerBootstrapActor(context.Context, string) (string, error) {
	return r.op.CreatedBy, nil
}
func (r *bootstrapCompletedInspectionRepository) ListServerBootstraps(context.Context, string, string, int, int) ([]domain.ServerBootstrapOperation, error) {
	return nil, nil
}
func (r *bootstrapCompletedInspectionRepository) ServerBootstrapEnrollmentConsumed(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *bootstrapCompletedInspectionRepository) RequestServerBootstrapCommand(context.Context, string, string, domain.ServerBootstrapCommand, domain.AuditEvent) (domain.ServerBootstrapCommand, error) {
	r.calls++
	return domain.ServerBootstrapCommand{}, errors.New("unexpected replay")
}
func TestServerBootstrapCompletedAutomaticInspectionRequiresExplicitCommand(t *testing.T) {
	s := newTestService()
	s.serverBootstrap = &ServerBootstrapConfig{}
	s.secretKeyring = managedTestRing(t, "inspection")
	op := domain.ServerBootstrapOperation{IDs: domain.ServerBootstrapIDs{OperationID: "sbo_test"}, CreatedBy: "usr_bootstrap", State: "uncertain", RemoteHold: true, Version: 4, CreatedAt: time.Now(), Commands: []domain.ServerBootstrapStage{{Command: domain.ServerBootstrapCommand{ID: "install", Action: "install"}, EffectIntent: true}, {Command: domain.ServerBootstrapCommand{Key: bootstrapSystemCommandID("sbo_test", "inspect_owned"), Action: "inspect_owned", ExpectedVersion: 3}, Receipt: &domain.ServerBootstrapReceipt{Outcome: "observed", Owned: &domain.ServerBootstrapOwnedReceipt{State: "staged"}}}}}
	r := &bootstrapCompletedInspectionRepository{HostingRepository: s.hosting, op: op}
	s.hosting = r
	if e := s.ProcessServerBootstrap(t.Context(), op.IDs.OperationID); e != nil {
		t.Fatal(e)
	}
	if r.calls != 0 {
		t.Fatal("completed automatic inspection replayed with changed CAS version")
	}
}
