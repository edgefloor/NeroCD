package app

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/store"
)

func (s *Service) buildController() (store.HostingBuildControllerRepository, error) {
	controller, ok := s.runs.(store.HostingBuildControllerRepository)
	if !ok {
		return nil, store.ErrHostingBuildUnavailable
	}
	return controller, nil
}

// RequestHostingBuild accepts Git intent and an optional owner-created source grant.
func (s *Service) RequestHostingBuild(ctx context.Context, resourceID string, in domain.HostingBuildRequest) (domain.PublicHostingBuild, error) {
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleMaintainer)
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	if resource.Source.Kind == "dockerfile" {
		if resource.Source.Dockerfile == nil || in.GrantID != "" {
			return domain.PublicHostingBuild{}, InvalidInput(errors.New("stored Dockerfile does not accept a Git grant"))
		}
	} else {
		if resource.Source.Kind != "git" || resource.Source.Git == nil || (resource.Source.Git.CredentialKind != "public" && resource.Source.Git.CredentialKind != "https_basic") {
			return domain.PublicHostingBuild{}, InvalidInput(errors.New("unsupported Git credential kind"))
		}
		if (resource.Source.Git.CredentialKind == "public") != (in.GrantID == "") || len(in.GrantID) > 128 {
			return domain.PublicHostingBuild{}, InvalidInput(errors.New("invalid source grant selection"))
		}
	}
	if in.ExpectedResourceGeneration < 1 || in.ExpectedSourceGeneration < 1 || in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey || strings.ContainsFunc(in.IdempotencyKey, unicode.IsControl) {
		return domain.PublicHostingBuild{}, InvalidInput(errors.New("invalid hosting build request"))
	}
	controller, err := s.buildController()
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	id, err := prefixedID("build")
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	id = strings.ReplaceAll(id, "_", "-")
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.build.requested", id, nil)
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	b, err := controller.RequestConfiguredHostingBuild(ctx, id, resource.ID, principal.ID, in, audit)
	return domain.PublicBuild(b), err
}

// HostingBuild returns a safe projection under current persisted viewer authority.
func (s *Service) HostingBuild(ctx context.Context, id string) (domain.PublicHostingBuild, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	if err = validateDestinationID(id, "build_id"); err != nil {
		return domain.PublicHostingBuild{}, err
	}
	controller, err := s.buildController()
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	b, err := controller.GetHostingBuild(ctx, principal.ID, id)
	return domain.PublicBuild(b), err
}

// ListHostingBuilds returns bounded retained history for a persisted resource project.
func (s *Service) ListHostingBuilds(ctx context.Context, resourceID string, page store.Page) (store.PageResult[domain.PublicHostingBuild], error) {
	var result store.PageResult[domain.PublicHostingBuild]
	resource, principal, err := s.resourceForDestinationRole(ctx, resourceID, domain.RoleViewer)
	if err != nil {
		return result, err
	}
	controller, err := s.buildController()
	if err != nil {
		return result, err
	}
	rows, err := controller.ListHostingBuilds(ctx, principal.ID, resource.ID, page)
	if err != nil {
		return result, err
	}
	result = store.PageResult[domain.PublicHostingBuild]{Items: []domain.PublicHostingBuild{}, Limit: rows.Limit, Offset: rows.Offset, Total: rows.Total}
	for _, b := range rows.Items {
		result.Items = append(result.Items, domain.PublicBuild(b))
	}
	return result, nil
}

// CancelHostingBuild requests bounded cleanup; it never grants another engine attempt.
func (s *Service) CancelHostingBuild(ctx context.Context, id string, revision int64) (domain.PublicHostingBuild, error) {
	principal, err := s.CurrentPrincipal(ctx)
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	if err = validateDestinationID(id, "build_id"); err != nil {
		return domain.PublicHostingBuild{}, err
	}
	if revision < 1 {
		return domain.PublicHostingBuild{}, InvalidInput(errors.New("expected_revision is required"))
	}
	controller, err := s.buildController()
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	audit, err := s.auditEvent(ctx, principal.ID, "hosting.build.cancel_requested", id, nil)
	if err != nil {
		return domain.PublicHostingBuild{}, err
	}
	b, err := controller.CancelHostingBuild(ctx, principal.ID, id, revision, audit)
	return domain.PublicBuild(b), err
}

// RunnerHostingBuildStatus returns historical proof separately from live permission.
func (s *Service) RunnerHostingBuildStatus(ctx context.Context, in hostingbuildprotocol.StatusRequest) (hostingbuildprotocol.StatusResponse, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return hostingbuildprotocol.StatusResponse{}, err
	}
	if err = in.Validate(); err != nil {
		return hostingbuildprotocol.StatusResponse{}, InvalidInput(err)
	}
	controller, err := s.buildController()
	if err != nil {
		return hostingbuildprotocol.StatusResponse{}, err
	}
	return controller.HostingBuildStatus(ctx, p.RunnerID, in.Authority)
}

// RunnerHostingBuildSourceBegin starts a fenced source phase.
func (s *Service) RunnerHostingBuildSourceBegin(ctx context.Context, in hostingbuildprotocol.SourceBeginRequest) (hostingbuildprotocol.PhaseResponse, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	if err = in.Validate(); err != nil {
		return hostingbuildprotocol.PhaseResponse{}, InvalidInput(err)
	}
	c, err := s.buildController()
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	b, err := c.BeginHostingBuildSource(ctx, p.RunnerID, in.Authority, in.ExpectedRevision)
	return hostingbuildprotocol.PhaseResponse{Build: b}, err
}

// RunnerHostingBuildSourceCommit acknowledges the exact immutable source receipt.
func (s *Service) RunnerHostingBuildSourceCommit(ctx context.Context, in hostingbuildprotocol.SourceCommitRequest) (hostingbuildprotocol.PhaseResponse, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	if err = in.Validate(); err != nil {
		return hostingbuildprotocol.PhaseResponse{}, InvalidInput(err)
	}
	c, err := s.buildController()
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	b, err := c.CommitHostingBuildSource(ctx, p.RunnerID, in.Authority, in.ReceiptID, in.Receipt)
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	return hostingbuildprotocol.PhaseResponse{Build: b, Acknowledgment: &hostingbuildprotocol.ReceiptAcknowledgment{Authority: in.Authority, Phase: "source", ReceiptID: in.ReceiptID, BodyHash: hostingbuildprotocol.SourceReceiptHash(in.Receipt)}}, nil
}

// RunnerHostingBuildPrepare pins observed daemon identity before engine effects.
func (s *Service) RunnerHostingBuildPrepare(ctx context.Context, in hostingbuildprotocol.PrepareRequest) (hostingbuildprotocol.PhaseResponse, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	if err = in.Validate(); err != nil {
		return hostingbuildprotocol.PhaseResponse{}, InvalidInput(err)
	}
	c, err := s.buildController()
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	b, err := c.PrepareHostingBuild(ctx, p.RunnerID, in.Authority, in.ExpectedRevision, in.DaemonID)
	return hostingbuildprotocol.PhaseResponse{Build: b}, err
}

// RunnerHostingBuildComplete atomically settles typed execution and its exact receipt.
func (s *Service) RunnerHostingBuildComplete(ctx context.Context, in hostingbuildprotocol.CompleteRequest) (hostingbuildprotocol.PhaseResponse, error) {
	p, err := s.CurrentRunnerIdentity(ctx)
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	if err = in.Validate(); err != nil {
		return hostingbuildprotocol.PhaseResponse{}, InvalidInput(err)
	}
	c, err := s.buildController()
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	b, err := c.CompleteHostingBuild(ctx, p.RunnerID, in.Authority, in.ReceiptID, in.ExpectedRevision, store.HostingBuildCompletion{State: in.Completion.State, FailureCode: in.Completion.FailureCode, Artifact: in.Completion.Artifact, ComposeResult: in.Completion.ComposeResult})
	if err != nil {
		return hostingbuildprotocol.PhaseResponse{}, err
	}
	phase, _ := hostingbuildprotocol.CompletionPhase(in.Completion)
	return hostingbuildprotocol.PhaseResponse{Build: b, Acknowledgment: &hostingbuildprotocol.ReceiptAcknowledgment{Authority: in.Authority, Phase: phase, ReceiptID: in.ReceiptID, BodyHash: hostingbuildprotocol.CompletionHash(in.Completion)}}, nil
}
