package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/serverbootstrap"
	"nerocd/internal/store"
)

// ServerBootstrapArtifact qualifies one operator-provisioned binary. Path stays private.
type ServerBootstrapArtifact struct {
	ArtifactID   string `json:"artifact_id"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Profile      string `json:"profile"`
}

// ServerBootstrapConfig is frozen before workers start; nil disables onboarding.
type ServerBootstrapConfig struct {
	ControllerOrigin string                    `json:"controller_origin"`
	Artifacts        []ServerBootstrapArtifact `json:"artifacts"`
}

// ConfigureServerBootstrap validates explicit operator policy without remote I/O.
func (s *Service) ConfigureServerBootstrap(cfg *ServerBootstrapConfig) error {
	if cfg == nil {
		s.serverBootstrap = nil
		return nil
	}
	u, e := url.Parse(cfg.ControllerOrigin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(cfg.Artifacts) < 1 || len(cfg.Artifacts) > 16 {
		return errors.New("invalid server bootstrap policy")
	}
	copyCfg := *cfg
	copyCfg.Artifacts = append([]ServerBootstrapArtifact(nil), cfg.Artifacts...)
	seen := map[string]bool{}
	for _, a := range copyCfg.Artifacts {
		info, e := os.Lstat(a.Path)
		if e != nil || !info.Mode().IsRegular() || !filepath.IsAbs(a.Path) || info.Size() != a.Size || !bootstrapOpaqueID.MatchString(a.ArtifactID) || seen[a.ArtifactID] || a.Profile != "prepared_debian12_v1" || a.Architecture != "arm64" {
			return errors.New("unqualified server bootstrap artifact")
		}

		f, err := os.Open(a.Path)
		if err != nil {
			return errors.New("unqualified server bootstrap artifact")
		}
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(f, 256<<20+1))
		closeErr := f.Close()
		if err != nil || closeErr != nil || n != a.Size || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
			return errors.New("unqualified server bootstrap artifact")
		}
		seen[a.ArtifactID] = true
		_, _, e = serverbootstrap.CanonicalManifest(serverbootstrap.InstallManifest{Version: 1, InstallationID: "00000000000000000000000000000000", InstallOperationID: "00000000000000000000000000000001", RunnerID: "runner_validation", CredentialReferenceID: "credential_validation", ControllerURL: cfg.ControllerOrigin, Artifact: serverbootstrap.Artifact{Size: a.Size, SHA256: a.SHA256, Version: a.Version, Architecture: a.Architecture}, CapabilityPreset: domain.ServerBootstrapPreset})
		if e != nil {
			return errors.New("unqualified server bootstrap artifact")
		}
	}
	s.serverBootstrap = &copyCfg
	return nil
}

var bootstrapUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var bootstrapOpaqueID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ServerBootstrapArtifactOption omits local paths and controller configuration.
type ServerBootstrapArtifactOption struct {
	ArtifactID   string `json:"artifact_id"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Profile      string `json:"profile"`
}

// ServerBootstrapOptions explains availability without disclosing private configuration.
type ServerBootstrapOptions struct {
	Available         bool                            `json:"available"`
	UnavailableReason string                          `json:"unavailable_reason"`
	Profile           string                          `json:"profile"`
	Preset            string                          `json:"preset"`
	Artifacts         []ServerBootstrapArtifactOption `json:"artifacts"`
}

// ServerBootstrapCreateInput excludes caller-selected execution configuration.
type ServerBootstrapCreateInput struct {
	RequestID        string                       `json:"request_id"`
	ProjectID        string                       `json:"project_id"`
	Name             string                       `json:"name"`
	Target           domain.ServerBootstrapTarget `json:"target"`
	SSHSecretID      string                       `json:"ssh_secret_id"`
	SSHSecretVersion int64                        `json:"ssh_secret_version"`
	ArtifactID       string                       `json:"artifact_id"`
}

// ServerBootstrapCommandInput is closed at the HTTP boundary for each action.
type ServerBootstrapCommandInput struct {
	CommandID                  string `json:"command_id"`
	ExpectedVersion            int64  `json:"expected_version"`
	ProbeReceiptID             string `json:"probe_receipt_id,omitempty"`
	ManifestSHA256             string `json:"manifest_sha256,omitempty"`
	AcknowledgeInstallAndStart bool   `json:"acknowledge_install_and_start,omitempty"`
	GrantProject               *bool  `json:"grant_project,omitempty"`
}

// ServerBootstrapProgress exposes bounded command status without lease authority.
type ServerBootstrapProgress struct {
	CommandID string `json:"command_id"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	ReceiptID string `json:"receipt_id,omitempty"`
}

// ServerBootstrapProbe projects a committed, expiring prepared-host observation.
type ServerBootstrapProbe struct {
	ReceiptID     string                              `json:"receipt_id"`
	Prerequisites domain.ServerBootstrapPrerequisites `json:"prerequisites"`
	ObservedAt    time.Time                           `json:"observed_at"`
	ExpiresAt     time.Time                           `json:"expires_at"`
}

// ServerBootstrapReview identifies the exact reviewed installer manifest.
type ServerBootstrapReview struct {
	ManifestSHA256 string    `json:"manifest_sha256"`
	ProbeReceiptID string    `json:"probe_receipt_id"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// ServerBootstrap is the public metadata projection; internal authority never crosses this boundary.
type ServerBootstrap struct {
	ID               string                       `json:"id"`
	ProjectID        string                       `json:"project_id"`
	Name             string                       `json:"name"`
	Target           domain.ServerBootstrapTarget `json:"target"`
	ArtifactID       string                       `json:"artifact_id"`
	State            string                       `json:"state"`
	Version          int64                        `json:"version"`
	RemoteHold       bool                         `json:"remote_hold"`
	RestoreHeld      bool                         `json:"restore_held"`
	CreatedAt        time.Time                    `json:"created_at"`
	Progress         []ServerBootstrapProgress    `json:"progress"`
	Probe            *ServerBootstrapProbe        `json:"probe,omitempty"`
	Review           *ServerBootstrapReview       `json:"review,omitempty"`
	AvailableActions []string                     `json:"available_actions"`
	ServerID         string                       `json:"server_id,omitempty"`
	DestinationID    string                       `json:"destination_id,omitempty"`
	ReadinessState   string                       `json:"readiness_state"`
}

// ServerBootstrapList bounds metadata pages and reports whether another page exists.
type ServerBootstrapList struct {
	Items   []ServerBootstrap `json:"items"`
	Limit   int               `json:"limit"`
	Offset  int               `json:"offset"`
	HasMore bool              `json:"has_more"`
}

type serverBootstrapRepository interface {
	store.ServerBootstrapRepository
	ListServerBootstraps(context.Context, string, string, int, int) ([]domain.ServerBootstrapOperation, error)
	ServerBootstrapActor(context.Context, string) (string, error)
	ServerBootstrapEnrollmentConsumed(context.Context, string, string) (bool, error)
}

func (s *Service) bootstrapAdmin(ctx context.Context, project string) (serverBootstrapRepository, string, error) {
	p, e := s.requireDestinationAdmin(ctx)
	if e != nil {
		return nil, "", e
	}
	if project != "" {
		if _, e = s.authorizeHostingProject(ctx, project, domain.RoleOwner); e != nil {
			return nil, "", e
		}
	}
	r, ok := s.hosting.(serverBootstrapRepository)
	if !ok {
		return nil, p.ID, hostingjobs.ErrUnavailable
	}
	return r, p.ID, nil
}

// ServerBootstrapOptions rechecks administrator and project owner access.
func (s *Service) ServerBootstrapOptions(ctx context.Context, project string) (ServerBootstrapOptions, error) {
	o := ServerBootstrapOptions{Profile: "prepared_debian12_v1", Preset: domain.ServerBootstrapPreset, Artifacts: []ServerBootstrapArtifactOption{}}
	if project == "" {
		return o, InvalidInput(errors.New("project_id is required"))
	}
	p, e := s.requireDestinationAdmin(ctx)
	if e != nil {
		return o, e
	}
	_ = p
	if _, e = s.authorizeHostingProject(ctx, project, domain.RoleOwner); e != nil {
		return o, e
	}
	switch {
	case s.serverBootstrap == nil:
		o.UnavailableReason = "disabled"
	case s.secretKeyring == nil:
		o.UnavailableReason = "managed_secrets_unavailable"
	default:
		if _, ok := s.hosting.(serverBootstrapRepository); !ok {
			o.UnavailableReason = "durable_store_required"
		} else {
			o.Available = true
		}
	}
	if o.Available {
		for _, a := range s.serverBootstrap.Artifacts {
			o.Artifacts = append(o.Artifacts, ServerBootstrapArtifactOption{a.ArtifactID, a.Version, a.Architecture, a.Size, a.SHA256, a.Profile})
		}
	}
	return o, nil
}

// CreateServerBootstrap reserves stable identities under an actor-bound request UUID.
func (s *Service) CreateServerBootstrap(ctx context.Context, in ServerBootstrapCreateInput) (ServerBootstrap, error) {
	r, actor, e := s.bootstrapAdmin(ctx, in.ProjectID)
	if e != nil {
		return ServerBootstrap{}, e
	}
	options, e := s.ServerBootstrapOptions(ctx, in.ProjectID)
	if e != nil {
		return ServerBootstrap{}, e
	}
	if !options.Available {
		return ServerBootstrap{}, hostingjobs.ErrUnavailable
	}
	if !bootstrapUUID.MatchString(in.RequestID) {
		return ServerBootstrap{}, InvalidInput(errors.New("request_id must be a UUID"))
	}
	var artifact *ServerBootstrapArtifact
	for i := range s.serverBootstrap.Artifacts {
		if s.serverBootstrap.Artifacts[i].ArtifactID == in.ArtifactID {
			artifact = &s.serverBootstrap.Artifacts[i]
		}
	}
	if artifact == nil {
		return ServerBootstrap{}, InvalidInput(errors.New("artifact_id is unavailable"))
	}
	input := domain.ServerBootstrapInput{ArtifactID: artifact.ArtifactID, ProjectID: in.ProjectID, Name: in.Name, Target: in.Target, ControllerURL: s.serverBootstrap.ControllerOrigin, Artifact: domain.ServerBootstrapArtifact{Size: artifact.Size, SHA256: artifact.SHA256, Version: artifact.Version, Architecture: artifact.Architecture}, SSHSecretID: in.SSHSecretID, SSHSecretVersion: in.SSHSecretVersion}
	if e = input.Validate(); e != nil {
		return ServerBootstrap{}, InvalidInput(e)
	}
	event, e := s.auditEvent(ctx, actor, "server_bootstrap.created", "", nil)
	if e != nil {
		return ServerBootstrap{}, e
	}
	op, e := r.CreateServerBootstrap(ctx, actor, in.RequestID, input, event)
	return s.bootstrapProjection(op, actor), e
}

// GetServerBootstrap returns the closed projection under current authority.
func (s *Service) GetServerBootstrap(ctx context.Context, id string) (ServerBootstrap, error) {
	r, actor, e := s.bootstrapAdmin(ctx, "")
	if e != nil {
		return ServerBootstrap{}, e
	}
	op, e := r.GetServerBootstrap(ctx, actor, id)
	return s.bootstrapProjection(op, actor), e
}

// ListServerBootstraps returns one bounded, authorized project page.
func (s *Service) ListServerBootstraps(ctx context.Context, project string, limit, offset int) (ServerBootstrapList, error) {
	out := ServerBootstrapList{Items: []ServerBootstrap{}, Limit: limit, Offset: offset}
	if project == "" || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return out, InvalidInput(errors.New("invalid list parameters"))
	}
	r, actor, e := s.bootstrapAdmin(ctx, project)
	if e != nil {
		return out, e
	}
	ops, e := r.ListServerBootstraps(ctx, actor, project, limit+1, offset)
	if e != nil {
		return out, e
	}
	out.HasMore = len(ops) > limit
	if out.HasMore {
		ops = ops[:limit]
	}
	for _, op := range ops {
		out.Items = append(out.Items, s.bootstrapProjection(op, actor))
	}
	return out, nil
}
func bootstrapManifest(op domain.ServerBootstrapOperation) (serverbootstrap.InstallManifest, []byte, string, error) {
	m := serverbootstrap.InstallManifest{Version: 1, InstallationID: op.IDs.InstallationID, InstallOperationID: op.IDs.InstallOperationID, RunnerID: op.IDs.RunnerID, CredentialReferenceID: op.IDs.CredentialReferenceID, ControllerURL: op.Input.ControllerURL, Artifact: serverbootstrap.Artifact(op.Input.Artifact), CapabilityPreset: domain.ServerBootstrapPreset}
	b, h, e := serverbootstrap.CanonicalManifest(m)
	return m, b, h, e
}

// CommandServerBootstrap binds action-specific public input to durable control.
func (s *Service) CommandServerBootstrap(ctx context.Context, id, action string, in ServerBootstrapCommandInput) (ServerBootstrap, error) {
	r, actor, e := s.bootstrapAdmin(ctx, "")
	if e != nil {
		return ServerBootstrap{}, e
	}
	if s.serverBootstrap == nil && action != "cancel" {
		return ServerBootstrap{}, hostingjobs.ErrUnavailable
	}
	if !bootstrapUUID.MatchString(in.CommandID) || in.ExpectedVersion < 1 {
		return ServerBootstrap{}, InvalidInput(errors.New("command_id UUID and expected_version are required"))
	}
	if action != "review" && in.ProbeReceiptID != "" || action != "install" && (in.ManifestSHA256 != "" || in.AcknowledgeInstallAndStart || in.GrantProject != nil) {
		return ServerBootstrap{}, InvalidInput(errors.New("fields do not match command action"))
	}
	op, e := r.GetServerBootstrap(ctx, actor, id)
	if e != nil {
		return ServerBootstrap{}, e
	}
	internalAction := action
	switch action {
	case "inspect":
		internalAction = "inspect_owned"
	case "register":
		internalAction = "register_server"
	case "grant":
		internalAction = "grant_destination"
	case "probe", "review", "install", "cancel":
	default:
		return ServerBootstrap{}, InvalidInput(errors.New("unsupported command"))
	}
	command := domain.ServerBootstrapCommand{Key: in.CommandID, Action: internalAction, ExpectedVersion: in.ExpectedVersion, Deadline: op.CreatedAt.Add(domain.ServerBootstrapMaxLifetime), ProbeReceiptID: in.ProbeReceiptID, ManifestSHA256: in.ManifestSHA256, InstallAcknowledged: in.AcknowledgeInstallAndStart, GrantProject: in.GrantProject}
	if action == "review" {
		if in.ProbeReceiptID == "" {
			return ServerBootstrap{}, InvalidInput(errors.New("probe_receipt_id is required"))
		}
		var probe *domain.ServerBootstrapProbeReceipt
		for _, stage := range op.Commands {
			if stage.Command.ID == in.ProbeReceiptID && stage.Command.Action == "probe" && stage.Receipt != nil && stage.Receipt.Outcome == "observed" {
				probe = stage.Receipt.Probe
			}
		}
		if probe == nil {
			return ServerBootstrap{}, InvalidInput(errors.New("committed probe receipt is required"))
		}
		_, b, h, err := bootstrapManifest(op)
		if err != nil {
			return ServerBootstrap{}, err
		}
		command.Review = &domain.ServerBootstrapReview{Prerequisites: probe.Prerequisites, Manifest: b, ManifestSHA256: h, ProbeObservedAt: probe.ObservedAt, ProbeExpiresAt: probe.ExpiresAt, TargetHash: domain.HostingDocumentHash(op.Input.Target)}
	}
	if action == "install" && (!in.AcknowledgeInstallAndStart || in.GrantProject == nil || in.ManifestSHA256 == "") {
		return ServerBootstrap{}, InvalidInput(errors.New("reviewed manifest, installation acknowledgment, and explicit project grant choice are required"))
	}
	event, e := s.auditEvent(ctx, actor, "server_bootstrap.command_requested", id, nil)
	if e != nil {
		return ServerBootstrap{}, e
	}
	if _, e = r.RequestServerBootstrapCommand(ctx, actor, id, command, event); e != nil {
		return ServerBootstrap{}, e
	}
	op, e = r.GetServerBootstrap(ctx, actor, id)
	return s.bootstrapProjection(op, actor), e
}
func (s *Service) bootstrapProjection(op domain.ServerBootstrapOperation, actor string) ServerBootstrap {
	out := ServerBootstrap{ID: op.IDs.OperationID, ProjectID: op.Input.ProjectID, Name: op.Input.Name, Target: op.Input.Target, ArtifactID: op.Input.ArtifactID, State: op.State, Version: op.Version, RemoteHold: op.RemoteHold, RestoreHeld: op.RestoreHeld, CreatedAt: op.CreatedAt, Progress: []ServerBootstrapProgress{}, AvailableActions: []string{}, ReadinessState: "not_registered"}
	for _, stage := range op.Commands {
		action := stage.Command.Action
		switch action {
		case "inspect_owned":
			action = "inspect"
		case "register_server":
			action = "register"
		case "grant_destination":
			action = "grant"
		}
		p := ServerBootstrapProgress{CommandID: stage.Command.Key, Action: action, Status: "pending"}
		if stage.LeaseID != "" {
			p.Status = "running"
		}
		if stage.Receipt != nil {
			p.Status = "completed"
			p.Outcome = stage.Receipt.Outcome
			p.ReceiptID = stage.Command.ID
			if stage.Receipt.Probe != nil {
				v := stage.Receipt.Probe
				out.Probe = &ServerBootstrapProbe{stage.Command.ID, v.Prerequisites, v.ObservedAt, v.ExpiresAt}
			}
		}
		out.Progress = append(out.Progress, p)
		if stage.Command.Review != nil && stage.Receipt != nil {
			out.Review = &ServerBootstrapReview{stage.Command.Review.ManifestSHA256, stage.Command.ProbeReceiptID, stage.Command.Review.ProbeExpiresAt}
		}
	}
	if op.State == "registered" || op.State == "completed" {
		out.ServerID = op.IDs.ServerID
		out.DestinationID = op.IDs.DestinationID
		out.ReadinessState = "check_server_readiness"
	}
	if op.RestoreHeld || actor != op.CreatedBy || !op.CreatedAt.Add(domain.ServerBootstrapMaxLifetime).After(s.clock()) {
		return out
	}
	for _, a := range []string{"probe", "review", "install", "inspect", "register", "grant", "cancel"} {
		internal := a
		switch a {
		case "inspect":
			internal = "inspect_owned"
		case "register":
			internal = "register_server"
		case "grant":
			internal = "grant_destination"
		}
		if !domain.ServerBootstrapActionAllowed(internal, op.State, op.RemoteHold) || actor != op.CreatedBy && a != "inspect" {
			continue
		}
		if op.ActiveStage != nil && a != "inspect" && a != "cancel" {
			continue
		}
		if a == "review" && (out.Probe == nil || !out.Probe.ExpiresAt.After(s.clock()) || !out.Probe.Prerequisites.Qualifies(op.Input.Artifact.Architecture)) {
			continue
		}
		if a == "install" && (out.Review == nil || !out.Review.ExpiresAt.After(s.clock())) {
			continue
		}
		out.AvailableActions = append(out.AvailableActions, a)
	}
	return out
}
