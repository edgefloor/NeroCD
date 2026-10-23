package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// autoReadPolicySnapshot never enters mutation authorization or placement locks.
// All policy, source, status and owner options facts share one read-only snapshot.
func autoReadPolicySnapshot(ctx context.Context, tx pgx.Tx, actor, resource string) (domain.GitHubPushPolicy, error) {
	q := sqlcgen.New(tx)
	rowSource, err := q.AutoReadSource(ctx, resource)
	var src domain.HostingGitSource
	if err == nil {
		err = json.Unmarshal(rowSource.Body, &src)
	}
	if err == nil {
		err = domain.ValidateHostingGitSource(src)
	}
	generation := rowSource.Generation
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	if src.ResourceID != resource || src.ProjectID != rowSource.ProjectID {
		return domain.GitHubPushPolicy{}, ErrConflict
	}
	rank, err := q.AutoReadActorRank(ctx, sqlcgen.AutoReadActorRankParams{ActorID: actor, ProjectID: src.ProjectID, ResourceID: resource})
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	if rank < 1 {
		return domain.GitHubPushPolicy{}, auth.ErrForbidden
	}
	owner := rank >= 3
	row, err := q.ReadGitHubPushPolicy(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		p := domain.GitHubPushPolicy{CanManage: owner, ResourceID: resource, ProjectID: src.ProjectID, ResourceGeneration: generation, SourceGeneration: src.Generation, Status: "unconfigured", Reason: "unverified"}
		o, r, b, e := domain.GitHubPushRepositoryBranch(src)
		if e != nil {
			p.Status = "blocked"
			p.Reason = "unsupported_source"
		} else {
			p.Repository = o + "/" + r
			p.Branch = b
		}
		return autoOwnerOptions(ctx, tx, p)
	}
	if err != nil {
		return domain.GitHubPushPolicy{}, err
	}
	doc, err := pushDocument(row)
	if err != nil {
		return doc.Policy, err
	}
	if src.Generation != doc.Policy.SourceGeneration || generation != doc.Policy.ResourceGeneration {
		doc.Policy.Status = "blocked"
		doc.Policy.Reason = "source_changed"
	}
	doc.Policy.AutomaticDeployment, err = autoStatus(ctx, tx, doc)
	if err != nil {
		return doc.Policy, err
	}
	return autoOwnerOptions(ctx, tx, pushPublic(doc, owner))
}

// autoReadCurrent mirrors reviewed identity qualification without acquiring locks.
// Mutation callers continue to use the independently locked autoCurrent.
func autoReadCurrent(ctx context.Context, tx pgx.Tx, doc githubPushDocument) (autoContext, error) {
	var c autoContext
	cfg := doc.Configuration.AutomaticDeployment
	if cfg == nil {
		return c, nil
	}
	q := sqlcgen.New(tx)
	control, err := q.AutoReadControl(ctx, doc.Policy.ResourceID)
	if err != nil {
		return c, hostingError(err)
	}
	c, err = decodeAutoControl(control)
	if err != nil {
		return c, err
	}
	canonical, err := domain.CanonicalHostingAutoDeployConfiguration(*cfg)
	if err != nil {
		return c, ErrConflict
	}
	if control.PolicyID != doc.Policy.ID || control.ManualEpoch != c.ManualEpoch || c.ConfigurationHash != domain.HostingDocumentHash(canonical) || c.ResourceGeneration != doc.Policy.ResourceGeneration || c.SourceGeneration != doc.Policy.SourceGeneration {
		return c, ErrConflict
	}
	rank, err := q.AutoReadActorRank(ctx, sqlcgen.AutoReadActorRankParams{ActorID: c.OwnerID, ProjectID: c.ProjectID, ResourceID: c.ResourceID})
	if err != nil {
		return c, err
	}
	if rank < 3 {
		return c, auth.ErrForbidden
	}
	placement, err := q.ProxyResourceTarget(ctx, c.ResourceID)
	if err != nil || placement != c.Placement {
		return c, ErrConflict
	}
	r, err := q.AutoReadResourceTarget(ctx, c.ResourceID)
	if err != nil {
		return c, hostingError(err)
	}
	if r.Generation != c.ResourceGeneration || r.RuntimeNamespace != c.RuntimeNamespace {
		return c, ErrConflict
	}
	if r.RuntimeGeneration != c.RuntimeGeneration {
		return c, ErrConflict
	}
	p, err := proxyLoad(ctx, tx, c.ProxyID)
	if err != nil {
		return c, err
	}
	if !p.Enabled || p.RestoreOwnershipHeld || p.DestinationID != placement.DestinationID || p.ServerID != placement.ServerID || p.RunnerID != placement.RunnerID || p.DaemonID != c.DaemonID || domain.HostingDocumentHash(p.Configuration) != c.ProxyConfigurationHash {
		return c, ErrConflict
	}
	eligible, err := q.ProxyVerifiedSwitchRunnerEligible(ctx, p.RunnerID)
	if err != nil {
		return c, err
	}
	if !eligible {
		return c, ErrConflict
	}
	capabilities := []string{domain.RunTypeLocalBuildDeploy, domain.HostingReleasePortsCapability}
	if c.RuntimeGeneration > 0 {
		capabilities = append(capabilities, domain.HostingRuntimeEnvironmentCapability)
	}
	for _, capability := range capabilities {
		capable, e := q.HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: placement.RunnerID, Capability: capability})
		if e != nil {
			return c, e
		}
		if !capable {
			return c, ErrConflict
		}
	}
	for _, route := range c.Routes {
		row, e := q.ProxyGetRoute(ctx, route.ID)
		if e != nil {
			return c, hostingError(e)
		}
		if row.ProxyID != p.ID || row.ResourceID != c.ResourceID || row.ProjectID != c.ProjectID || row.BindingID != placement.BindingID || !autoRouteIdentity(row, route) {
			return c, ErrConflict
		}
		// Reuse current hostname, destination, binding, release and grant qualification.
		if _, e = autoReadHealthyTarget(ctx, tx, p, row); e != nil {
			return c, e
		}
	}
	return c, nil
}

func autoReadHealthyTarget(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, row sqlcgen.HostingProxyRoute) (domain.HostingProxyRouteTarget, error) {
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, row.ResourceID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if placement.ProjectID != row.ProjectID || placement.BindingID != row.BindingID || placement.DestinationID != p.DestinationID || placement.ServerID != p.ServerID || placement.RunnerID != p.RunnerID {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	grant, err := q.ProxyGetGrant(ctx, row.GrantID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if grant.ProxyID != p.ID || grant.ProjectID != row.ProjectID || grant.RevokedAt != nil || !domain.HostingHostnameGranted(grant.Hostname, grant.Wildcard, row.Hostname) {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	release, err := q.ProxyReleaseTarget(ctx, row.SelectedReleaseOperationID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	var healthy domain.HostingHealthy
	if err = json.Unmarshal(release.Body, &healthy); err != nil {
		return domain.HostingProxyRouteTarget{}, err
	}
	if !proxyReleaseQualified(p, row, release.State, release.RetainedRunning, healthy) {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	return domain.HostingProxyRouteTarget{RouteID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, ReleaseOperationID: row.SelectedReleaseOperationID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, HostPort: uint16(healthy.Snapshot.HostPort)}, nil
}
