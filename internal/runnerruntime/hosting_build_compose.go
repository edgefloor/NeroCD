package runnerruntime

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
)

// buildCompose prepares local images without starting or replacing any stack
// writer. All children share the parent's deadline, immutable source receipt,
// daemon and authorized log sink.
func (e *BuildExecutor) buildCompose(ctx context.Context, b domain.HostingBuild, checkout string, sink hostingbuild.OutputSink) (domain.HostingBuildComposeResult, error) {
	var zero domain.HostingBuildComposeResult
	if !e.config.Compose || !b.Snapshot.Policy.GitCompose || b.SourceReceipt == nil {
		return zero, errors.New("git Compose build is not enabled")
	}
	source, err := readBuildCompose(ctx, checkout, b.Snapshot.Source.Settings)
	if err != nil {
		return zero, err
	}
	definition, err := domain.ParseHostingGitCompose(source, b.Snapshot.Source.Settings)
	if err != nil {
		return zero, errors.New("compose source is unsupported or invalid")
	}
	result := domain.HostingBuildComposeResult{SourceReceiptHash: domain.HostingDocumentHash(b.SourceReceipt), Source: source, Definition: definition, Services: []domain.HostingBuildComposeService{}}
	// Refuse state from an interrupted child even if a caller discarded the parent
	// journal. Engine receipt replay alone must never authorize another attempt.
	buildCount := int64(0)
	for _, service := range definition.Services {
		if service.Build != nil {
			buildCount++
		}
	}
	for _, service := range definition.Services {
		if service.Build == nil {
			continue
		}
		id := domain.HostingComposeBuildID(b.ID, service.Name)
		if _, err = os.Lstat(filepath.Join(e.config.Engine.StateDir, id)); !os.IsNotExist(err) {
			return zero, hostingbuild.ErrCleanupUnconfirmed
		}
	}
	for _, service := range definition.Services {
		if service.Build == nil {
			continue
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		request := buildEngineRequest(b.Snapshot, checkout, b.DaemonID)
		request.BuildID = domain.HostingComposeBuildID(b.ID, service.Name)
		request.Strategy = hostingbuild.Dockerfile
		request.ContextDir = service.Build.Context
		request.DockerfilePath = service.Build.Dockerfile
		request.BuildTarget = service.Build.Target
		request.BuildArgs = maps.Clone(service.Build.Args)
		// Sequential children may use the parent's peak CPU/memory, but their
		// retained context/export/log budgets must not multiply its disk limit.
		request.Limits.ContextBytes /= buildCount
		request.Limits.ContextFiles /= int(buildCount)
		request.Limits.PlanBytes /= buildCount
		request.Limits.LogBytes /= buildCount
		request.Limits.ExportBytes /= buildCount
		request.Limits.UnpackedBytes /= buildCount
		request.Limits.UsageBytes /= buildCount
		if request.Limits.ContextBytes < 1 || request.Limits.ContextFiles < 1 || request.Limits.PlanBytes < 1 || request.Limits.LogBytes < 1 || request.Limits.ExportBytes < 1 || request.Limits.UnpackedBytes < 1 || request.Limits.UsageBytes < 1 {
			return zero, errors.New("compose component budgets exceed configured build limits")
		}
		request.PublishDir = ""
		request.SPAFallback = false
		built, buildErr := e.engine.(buildOutputEngine).BuildWithOutput(ctx, request, sink)
		if buildErr != nil {
			return zero, buildErr
		}
		if built.BuildID != request.BuildID || built.SourceID != request.SourceID || built.InputID != request.InputID || built.DaemonID != b.DaemonID || built.Platform != b.Snapshot.Policy.Platform {
			return zero, errors.New("compose child build identity changed")
		}
		artifact := buildArtifact(built, b)
		result.Services = append(result.Services, domain.HostingBuildComposeService{Component: service.Name, Artifact: artifact})
	}
	result.Hash = domain.HostingDocumentHash(result)
	if domain.ValidateHostingBuildComposeResult(result, b) != nil {
		return zero, errors.New("compose result does not match parent build")
	}
	return result, nil
}
