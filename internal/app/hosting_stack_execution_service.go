package app

import (
	"context"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

// RunnerHostingStackVolumes seals creation provenance under the existing runner authority.
func (s *Service) RunnerHostingStackVolumes(ctx context.Context, in domain.HostingStackVolumeRequest) (domain.HostingOperation, error) {
	return s.withHostingRunner(ctx, func(repo store.HostingExecutionRepository, runner string) (domain.HostingOperation, error) {
		stack, ok := repo.(interface {
			HostingStackVolumes(context.Context, string, domain.HostingStackVolumeRequest) (domain.HostingOperation, error)
		})
		if !ok {
			return domain.HostingOperation{}, hostingjobs.ErrUnavailable
		}
		return stack.HostingStackVolumes(ctx, runner, in)
	})
}
