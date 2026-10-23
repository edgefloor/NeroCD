package store

import (
	"context"
	"fmt"
	"nerocd/internal/domain"
)

// ErrHostingProxyControlProject prevents orphaning immutable proxy authority.
var ErrHostingProxyControlProject = fmt.Errorf("%w: a configured managed proxy still uses this control project", ErrConflict)

// HostingProxyRouteRecord carries private scope for application authorization.
// Public project responses contain only Route.
type HostingProxyRouteRecord struct {
	Route                                  domain.HostingRoute
	ProxyID, ProjectID, BindingID, GrantID string
}

// HostingProxyRepository owns persisted proxy generations and exact runner authority.
// Memory implementations deliberately omit this interface and fail unavailable.
type HostingProxyRepository interface {
	ConfigureHostingProxy(context.Context, string, string, string, domain.HostingProxyInput, domain.AuditEvent) (domain.HostingProxy, error)
	GetHostingProxyByServer(context.Context, string) (domain.HostingProxy, error)
	GetHostingProxy(context.Context, string) (domain.HostingProxy, error)
	SetHostingProxyEnabled(context.Context, string, string, string, bool, domain.AuditEvent) (domain.HostingProxy, error)
	ListHostingHostnameGrants(context.Context, string) ([]domain.HostingHostnameGrant, error)
	GrantHostingHostname(context.Context, string, string, string, domain.HostingHostnameGrantInput, domain.AuditEvent) (domain.HostingHostnameGrant, error)
	RevokeHostingHostname(context.Context, string, string, string, string, domain.AuditEvent) (domain.HostingProxy, error)
	GetHostingRoute(context.Context, string) (HostingProxyRouteRecord, error)
	ListHostingRoutes(context.Context, string) ([]domain.HostingRoute, error)
	RequestHostingRoute(context.Context, string, string, string, string, domain.HostingRouteInput, domain.AuditEvent) (domain.HostingRoute, error)
	DeleteHostingRoute(context.Context, string, string, string, domain.HostingRouteDeletionInput, domain.AuditEvent) (domain.HostingRoute, error)
	GetHostingProxyOperation(context.Context, string) (domain.HostingProxyOperation, error)
	CancelHostingProxyOperation(context.Context, string, string, domain.AuditEvent) (domain.HostingProxyOperation, error)
	RunnerProxyPlan(context.Context, string, domain.HostingAuthority) (domain.HostingProxyOperation, error)
	RunnerProxyPrepared(context.Context, string, domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error)
	RunnerProxyApplyBegin(context.Context, string, domain.HostingAuthority) (domain.HostingProxyOperation, error)
	RunnerProxyComplete(context.Context, string, domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error)
	RunnerProxyStatus(context.Context, string, domain.HostingAuthority) (domain.HostingProxyOperation, error)
}

// HostingRouteSwitchRepository atomically admits one bounded verified switch.
type HostingRouteSwitchRepository interface {
	RequestHostingRouteSwitch(context.Context, string, string, string, domain.HostingRouteSwitchInput, domain.AuditEvent) (domain.HostingProxyOperation, error)
}
