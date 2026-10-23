package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"nerocd/internal/domain"
	"nerocd/internal/serverbootstrap"
	"nerocd/internal/store"
	"net/netip"
	"time"
)

// ProcessServerBootstrap resolves only opaque queued IDs. Every remote call owns
// a renewing lease, a bounded context, cancellation, and a join before return.
func (s *Service) ProcessServerBootstrap(ctx context.Context, id string) error {
	r, ok := s.hosting.(serverBootstrapRepository)
	if !ok || s.serverBootstrap == nil || s.secretKeyring == nil {
		return nil
	}
	actor, e := r.ServerBootstrapActor(ctx, id)
	if e != nil {
		return e
	}
	for range 4 {
		op, e := r.GetServerBootstrap(ctx, actor, id)
		if e != nil {
			return e
		}
		if op.RestoreHeld {
			return nil
		}
		var pending *domain.ServerBootstrapStage
		for i := range op.Commands {
			stage := &op.Commands[i]
			if stage.Receipt == nil && !stage.EffectIntent {
				if stage.ExpiresAt.After(time.Now()) {
					continue
				}
				pending = stage
				break
			}
		}
		if pending == nil {
			action := ""
			switch op.State {
			case "installed_waiting_enrollment":
				action = "register_server"
			case "registered":
				for _, c := range op.Commands {
					if c.Command.Action == "install" && c.Command.GrantProject != nil && *c.Command.GrantProject {
						action = "grant_destination"
					}
				}
			case "running", "uncertain":
				for _, c := range op.Commands {
					if c.EffectIntent && c.Receipt == nil && !c.ExpiresAt.After(time.Now()) {
						action = "inspect_owned"
					}
				}
			}
			if action == "" {
				return nil
			}
			command := domain.ServerBootstrapCommand{Key: bootstrapSystemCommandID(id, action), Action: action, ExpectedVersion: op.Version, Deadline: op.CreatedAt.Add(domain.ServerBootstrapMaxLifetime)}
			event, e := s.auditEvent(ctx, actor, "server_bootstrap.command_requested", id, nil)
			if e != nil {
				return e
			}
			c, e := r.RequestServerBootstrapCommand(ctx, actor, id, command, event)
			if e != nil {
				return e
			}
			// An exact replay of an already completed automatic inspection is terminal.
			for _, stage := range op.Commands {
				if stage.Command.ID == c.ID && stage.Receipt != nil {
					return nil
				}
			}
			continue
		}
		if pending.Command.Action == "register_server" {
			consumed, err := r.ServerBootstrapEnrollmentConsumed(ctx, actor, id)
			if err != nil {
				return err
			}
			if !consumed {
				return nil
			}
		}
		stage, e := r.ClaimServerBootstrapStage(ctx, actor, id, pending.Command.ID)
		if e != nil {
			return e
		}
		receipt := domain.ServerBootstrapReceipt{CommandID: stage.Command.ID, LeaseID: stage.LeaseID, Attempt: stage.Attempt, Fence: stage.Fence, Outcome: "accepted"}
		if stage.Command.Action == "probe" || stage.Command.Action == "install" || stage.Command.Action == "inspect_owned" {
			receipt, e = s.bootstrapRemote(ctx, r, actor, op, stage)
			if e != nil {
				return e
			}
		}
		event, e := s.auditEvent(ctx, actor, "server_bootstrap.stage_completed", id, nil)
		if e != nil {
			return e
		}
		if _, e = r.CompleteServerBootstrapStage(ctx, actor, id, receipt, event); e != nil {
			return e
		}
	}
	return nil
}
func bootstrapSystemCommandID(id, action string) string {
	sum := sha256.Sum256([]byte(id + ":" + action))
	b := sum[:16]
	b[6] = (b[6] & 15) | 0x40
	b[8] = (b[8] & 63) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *Service) bootstrapRemote(parent context.Context, r store.ServerBootstrapRepository, actor string, op domain.ServerBootstrapOperation, stage domain.ServerBootstrapStage) (domain.ServerBootstrapReceipt, error) {
	ctx, cancel := context.WithTimeout(parent, 125*time.Second)
	defer cancel()
	receipt := domain.ServerBootstrapReceipt{CommandID: stage.Command.ID, LeaseID: stage.LeaseID, Attempt: stage.Attempt, Fence: stage.Fence}
	event, e := s.auditEvent(ctx, actor, "server_bootstrap.delivery", op.IDs.OperationID, nil)
	if e != nil {
		return receipt, e
	}
	delivery, e := r.AuthorizeServerBootstrapDelivery(ctx, actor, op.IDs.OperationID, stage, s.secretKeyring, event)
	if e != nil {
		return receipt, e
	}
	// Recheck current authority immediately after materialization and before submission.
	if _, e = r.RenewServerBootstrapStage(ctx, actor, op.IDs.OperationID, stage); e != nil {
		return receipt, e
	}
	target := serverbootstrap.Target{Address: netip.MustParseAddr(op.Input.Target.IP), Port: op.Input.Target.Port, User: op.Input.Target.User, HostKeyFingerprint: op.Input.Target.HostFingerprint}
	type result struct {
		probe serverbootstrap.Observations
		owned serverbootstrap.OwnedInspection
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var out result
		switch stage.Command.Action {
		case "probe":
			out.probe, out.err = serverbootstrap.Probe(ctx, target, delivery.SSHPrivateKey)
		case "install", "inspect_owned":
			m, _, _, err := bootstrapManifest(op)
			if err != nil {
				out.err = err
				break
			}
			if stage.Command.Action == "inspect_owned" {
				out.owned, out.err = serverbootstrap.InspectOwned(ctx, target, delivery.SSHPrivateKey, serverbootstrap.Ownership{Manifest: m})
			} else {
				var artifact *ServerBootstrapArtifact
				for i := range s.serverBootstrap.Artifacts {
					a := &s.serverBootstrap.Artifacts[i]
					if a.ArtifactID == op.Input.ArtifactID && a.Size == op.Input.Artifact.Size && a.SHA256 == op.Input.Artifact.SHA256 && a.Version == op.Input.Artifact.Version && a.Architecture == op.Input.Artifact.Architecture {
						artifact = a
					}
				}
				if artifact == nil {
					out.err = serverbootstrap.InvalidArtifact
					break
				}
				out.owned, out.err = serverbootstrap.Install(ctx, target, delivery.SSHPrivateKey, serverbootstrap.InstallRequest{OperationID: op.IDs.InstallOperationID, Manifest: m, Artifact: serverbootstrap.ArtifactSource{Path: artifact.Path}, EnrollmentToken: delivery.EnrollmentToken})
			}
		}
		done <- out
	}()
	out, e := waitServerBootstrapRemote(ctx, cancel, 10*time.Second, done, func() error { _, err := r.RenewServerBootstrapStage(ctx, actor, op.IDs.OperationID, stage); return err })
	if e != nil {
		return receipt, e
	}
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	if stage.Command.Action == "install" {
		receipt.ManifestSHA256 = delivery.Operation.Review.ManifestSHA256
		receipt.RemoteOperationID = op.IDs.InstallOperationID
		receipt.Outcome = "uncertain"
		if out.err == nil {
			receipt.Outcome = "installed"
		}
	} else {
		receipt.Outcome = "refused"
		if out.err == nil {
			receipt.Outcome = "observed"
		}
	}
	if out.err == nil {
		if stage.Command.Action == "probe" {
			o := out.probe
			receipt.Probe = &domain.ServerBootstrapProbeReceipt{Prerequisites: domain.ServerBootstrapPrerequisites{Profile: "prepared_debian12_v1", OSState: string(o.OS.State), OSID: o.OS.ID, OSVersion: o.OS.VersionID, Architecture: o.OS.Architecture, SystemdState: string(o.Systemd.State), SystemdStatus: string(o.Systemd.Status), DockerState: string(o.Docker.State), DockerAPIVersion: o.Docker.APIVersion, ComposeState: string(o.Compose.State), ComposeVersion: o.Compose.Version, DiskState: string(o.Disk.State), DiskTotalBytes: o.Disk.TotalBytes, DiskAvailableBytes: o.Disk.AvailableBytes}}
		} else {
			o := out.owned
			receipt.Owned = &domain.ServerBootstrapOwnedReceipt{InstallationID: o.InstallationID, ManifestSHA256: o.ManifestSHA256, OperationID: o.OperationID, State: string(o.State), ArtifactMatches: o.ArtifactMatches, ServiceActive: o.ServiceActive, ServiceJobPending: o.ServiceJobPending, EnrollmentFileExists: o.EnrollmentFileExists, CredentialFileExists: o.CredentialFileExists}
		}
	}
	// Adapter failures are represented only by a closed outcome, never raw errors.
	return receipt, nil
}

// waitServerBootstrapRemote cancels and joins the owned call on every authority loss.
func waitServerBootstrapRemote[T any](ctx context.Context, cancel context.CancelFunc, interval time.Duration, done <-chan T, renew func() error) (T, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case out := <-done:
			return out, nil
		case <-ctx.Done():
			cancel()
			<-done
			var zero T
			return zero, ctx.Err()
		case <-ticker.C:
			if err := renew(); err != nil {
				cancel()
				<-done
				var zero T
				return zero, err
			}
		}
	}
}
