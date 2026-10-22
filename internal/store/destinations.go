package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

// ResourceBindingInput omits the runtime namespace, which the repository generates.
type ResourceBindingInput struct {
	ID            string
	ResourceID    string
	DestinationID string
	CreatedBy     string
	CreatedAt     time.Time
}

// DestinationRepository stores stable targets without altering legacy runner scheduling.
// Admission requires an active credentialed runner with a consumed nonrevoked enrollment,
// compose_deploy capability, no shell capability, and no prior server association.
// This is not scheduler exclusivity. Future hosting execution must check the exact target and current authorization.
// Every mutation requires a fresh audit and commits it atomically with its state change.
type DestinationRepository interface {
	CreateServerWithDestination(context.Context, domain.Server, domain.Destination, domain.AuditEvent) (domain.Server, domain.Destination, error)
	GetServer(context.Context, string) (domain.Server, error)
	ListServers(context.Context, Page) (PageResult[domain.Server], error)
	GetDestination(context.Context, string) (domain.Destination, error)
	ListDestinations(context.Context, string, Page) (PageResult[domain.Destination], error)
	GrantDestinationProject(context.Context, domain.DestinationProjectGrant, domain.AuditEvent) (domain.DestinationProjectGrant, error)
	RevokeDestinationProject(context.Context, string, string, domain.AuditEvent) (domain.DestinationProjectGrant, error)
	GetDestinationProjectGrant(context.Context, string, string) (domain.DestinationProjectGrant, error)
	ListDestinationProjectGrants(context.Context, string, Page) (PageResult[domain.DestinationProjectGrant], error)
	ListProjectDestinations(context.Context, string, Page) (PageResult[domain.Destination], error)
	BindResourceDestination(context.Context, ResourceBindingInput, domain.AuditEvent) (domain.ResourceBinding, error)
	GetResourceBinding(context.Context, string) (domain.ResourceBinding, error)
	ListResourceBindings(context.Context, string, Page) (PageResult[domain.ResourceBinding], error)
	ResolveResourceDestination(context.Context, string) (domain.ResourceDestination, error)
}

var _ DestinationRepository = (*MemoryStore)(nil)
var _ DestinationRepository = (*PostgresStore)(nil)

func validServerDestination(server domain.Server, destination domain.Destination, event domain.AuditEvent) bool {
	return server.ID != "" && server.RunnerID != "" && server.CreatedBy != "" && server.CreatedBy == event.ActorID && domain.ValidateHostingName(server.Name) == nil && destination.ID != "" && destination.ServerID == server.ID && destination.RuntimeKind == "docker_compose" && domain.ValidateHostingName(destination.Name) == nil
}
func destinationAudit(event domain.AuditEvent, action, target string, metadata map[string]any) (domain.AuditEvent, error) {
	if event.ID == "" || event.ActorID == "" {
		return domain.AuditEvent{}, ErrConflict
	}
	if request, ok := event.Metadata["request_id"].(string); ok && len(request) <= 128 {
		metadata["request_id"] = request
	}
	event.Action = action
	event.TargetID = target
	event.Metadata = metadata
	return event, nil
}
func copyDestinationGrant(grant domain.DestinationProjectGrant) domain.DestinationProjectGrant {
	if grant.RevokedAt != nil {
		at := *grant.RevokedAt
		grant.RevokedAt = &at
	}
	return grant
}
func copyDestinationRunner(runner domain.Runner) domain.Runner {
	runner.Tags = append([]string(nil), runner.Tags...)
	runner.Capabilities = append([]string(nil), runner.Capabilities...)
	return runner
}
