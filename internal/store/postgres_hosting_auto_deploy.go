package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type autoRoute struct {
	ID         string `json:"id"`
	Version    int64  `json:"version"`
	ReleaseID  string `json:"release_id"`
	Hostname   string `json:"hostname"`
	PathPrefix string `json:"path_prefix"`
	GrantID    string `json:"grant_id"`
}
type autoContext struct {
	Version                int                            `json:"version"`
	OwnerID                string                         `json:"owner_id"`
	ConfigurationHash      string                         `json:"configuration_hash"`
	ResourceID             string                         `json:"resource_id"`
	ProjectID              string                         `json:"project_id"`
	ResourceGeneration     int64                          `json:"resource_generation"`
	SourceGeneration       int64                          `json:"source_generation"`
	RuntimeGeneration      int64                          `json:"runtime_generation"`
	ManualEpoch            int64                          `json:"manual_epoch"`
	Placement              sqlcgen.ProxyResourceTargetRow `json:"placement"`
	RuntimeNamespace       string                         `json:"runtime_namespace"`
	ProxyID                string                         `json:"proxy_id"`
	ProxyConfigurationHash string                         `json:"proxy_configuration_hash"`
	DaemonID               string                         `json:"daemon_id"`
	ManualHealthyID        string                         `json:"manual_healthy_id"`
	BaselineOperationID    string                         `json:"baseline_operation_id"`
	BaselineGeneration     int64                          `json:"baseline_generation"`
	Routes                 []autoRoute                    `json:"routes"`
}
type autoDemand struct {
	CreatedAt        time.Time   `json:"created_at"`
	ExpiresAt        time.Time   `json:"expires_at"`
	OriginalAttempts int32       `json:"original_attempts"`
	Version          int         `json:"version"`
	ID               string      `json:"id"`
	Context          autoContext `json:"context"`
	PolicyID         string      `json:"policy_id"`
	PolicyGeneration int64       `json:"policy_generation"`
	Commit           string      `json:"commit"`
	BuildID          string      `json:"build_id"`
	DeploymentID     string      `json:"deployment_id"`
	SwitchID         string      `json:"switch_id"`
}
type autoOrigin struct {
	Attempts            int32       `json:"attempts"`
	Version             int         `json:"version"`
	Demand              autoDemand  `json:"demand"`
	Routes              []autoRoute `json:"routes"`
	BaselineOperationID string      `json:"baseline_operation_id"`
	BaselineGeneration  int64       `json:"baseline_generation"`
}

func decodeAutoControl(row sqlcgen.HostingAutoDeployControl) (autoContext, error) {
	var c autoContext
	if json.Unmarshal(row.Body, &c) != nil || c.Version != 1 || c.ResourceID != row.ResourceID || domain.HostingDocumentHash(c) != row.BodyHash {
		return c, ErrConflict
	}
	return c, nil
}
func decodeAutoDemand(row sqlcgen.HostingAutoDeployDemand) (autoDemand, error) {
	var d autoDemand
	if json.Unmarshal(row.Body, &d) != nil || d.Version != 1 || d.ID != row.ID || d.Context.ResourceID != row.ResourceID || d.PolicyID != row.PolicyID || d.PolicyGeneration != row.PolicyGeneration || d.BuildID != row.BuildID || d.Commit != row.CommitSha || !d.CreatedAt.Equal(row.CreatedAt) || !d.ExpiresAt.Equal(row.ExpiresAt) || d.OriginalAttempts < 0 || d.OriginalAttempts > domain.GitHubPushMaxAttempts {
		return d, ErrConflict
	}
	return d, nil
}
func decodeAutoOrigin(row sqlcgen.HostingAutoDeployOrigin) (autoOrigin, error) {
	var o autoOrigin
	if json.Unmarshal(row.Body, &o) != nil || o.Version != 1 || o.Demand.ID != row.DemandID || o.Demand.Context.ResourceID != row.ResourceID || o.Demand.BuildID != row.BuildID || domain.HostingDocumentHash(o) != row.BodyHash {
		return o, ErrConflict
	}
	if row.Kind == "deployment" && o.Demand.DeploymentID != row.OperationID || row.Kind == "switch" && o.Demand.SwitchID != row.OperationID {
		return o, ErrConflict
	}
	return o, nil
}

// autoPrelock takes only the established destination/proxy/sorted-resource
// prefix. It is called before any resource lock, never after pushScope.
func autoPrelock(ctx context.Context, tx pgx.Tx, resource string) (domain.HostingProxy, error) {
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, resource)
	if err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	if err = q.LockHostingExecutionDestination(ctx, placement.DestinationID); err != nil {
		return domain.HostingProxy{}, err
	}
	proxy, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if err != nil {
		return domain.HostingProxy{}, hostingError(err)
	}
	p, err := proxyLock(ctx, tx, proxy.ID)
	if err != nil {
		return p, err
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return p, err
	}
	resources := map[string]bool{resource: true}
	for _, r := range rows {
		resources[r.ResourceID] = true
	}
	ids := make([]string, 0, len(resources))
	for id := range resources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err = q.ProxyLockResource(ctx, id); err != nil {
			return p, hostingError(err)
		}
	}
	return p, nil
}

// autoPushScope composes automatic prelocks without changing build-only
// pushScope ordering. The quota lock stabilizes which configured mode is read.
func autoPushScope(ctx context.Context, tx pgx.Tx, resource, actor, role string, force bool) (domain.HostingGitSource, int64, error) {
	q := sqlcgen.New(tx)
	if err := q.LockGitHubPushQuota(ctx); err != nil {
		return domain.HostingGitSource{}, 0, err
	}
	automatic := force
	row, err := q.ReadGitHubPushPolicy(ctx, resource)
	if err == nil {
		doc, e := pushDocument(row)
		if e != nil {
			return domain.HostingGitSource{}, 0, e
		}
		automatic = automatic || doc.Configuration.AutomaticDeployment != nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingGitSource{}, 0, err
	}
	if automatic {
		if _, err = autoPrelock(ctx, tx, resource); err != nil {
			return domain.HostingGitSource{}, 0, err
		}
	}
	return pushScope(ctx, tx, resource, actor, role)
}

func autoRuntimeGeneration(ctx context.Context, q *sqlcgen.Queries, resource string) (int64, error) {
	n, err := q.ReadHostingRuntimeGeneration(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
func autoRouteIdentity(r sqlcgen.HostingProxyRoute, c autoRoute) bool {
	return r.ID == c.ID && r.Hostname == c.Hostname && r.PathPrefix == c.PathPrefix && r.GrantID == c.GrantID && r.Enabled
}
func autoRouteSelection(r sqlcgen.HostingProxyRoute, c autoRoute) bool {
	return autoRouteIdentity(r, c) && r.Version == c.Version && r.SelectedReleaseOperationID == c.ReleaseID
}
func autoLoadedHTTPS(op domain.HostingProxyOperation, routes []autoRoute, now time.Time) bool {
	if op.State != "applied" || op.Result == nil || op.Result.Outcome != "applied" || op.Result.LoadedGeneration != op.Generation || op.MutationHold || op.RestoreOwnershipHeld {
		return false
	}
	for _, route := range routes {
		target := false
		for _, r := range op.Snapshot.Routes {
			if r.RouteID == route.ID && r.ReleaseOperationID == route.ReleaseID && r.Hostname == route.Hostname && r.PathPrefix == route.PathPrefix {
				target = true
			}
		}
		observed := false
		for _, r := range op.Result.Routes {
			if r.RouteID == route.ID && r.HTTPReady && r.HTTPSReady && r.Certificate.Hostname == route.Hostname && r.Certificate.Status == "trusted" && proxyDigest(r.Certificate.Fingerprint) && r.Certificate.ExpiresAt.After(now) {
				observed = true
			}
		}
		if !target || !observed {
			return false
		}
	}
	return true
}

func autoConfigure(ctx context.Context, tx pgx.Tx, doc githubPushDocument, owner string) error {
	if doc.Configuration.AutomaticDeployment == nil {
		return nil
	}
	cfg, err := domain.CanonicalHostingAutoDeployConfiguration(*doc.Configuration.AutomaticDeployment)
	if err != nil {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, doc.Policy.ResourceID)
	if err != nil {
		return hostingError(err)
	}
	p, err := autoPrelock(ctx, tx, doc.Policy.ResourceID)
	if err != nil {
		return err
	}
	if !p.Enabled || p.RestoreOwnershipHeld || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.LoadedGeneration != cfg.ExpectedLoadedGeneration || p.DaemonID == "" {
		return ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	if prior.ID != cfg.ExpectedLoadedProxyOperationID {
		return ErrConflict
	}
	healthyRaw, err := q.GetHostingHealthy(ctx, placement.BindingID)
	if err != nil {
		return hostingError(err)
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(healthyRaw, &healthy) != nil || healthy.OperationID != cfg.ExpectedHealthyOperationID || healthy.Snapshot.LocalBuild == (domain.HostingLocalBuildSource{}) || healthy.Snapshot.ResourceID != doc.Policy.ResourceID || healthy.Snapshot.ContainerPort != cfg.ContainerPort || healthy.Snapshot.Readiness != cfg.Readiness || healthy.Snapshot.RuntimeConfiguration.Generation != cfg.ExpectedRuntimeConfigurationGeneration {
		return ErrConflict
	}
	op, err := hostingOperation(ctx, tx, healthy.OperationID)
	if err != nil {
		return err
	}
	if op.State != "succeeded" || op.MutationHold || op.Snapshot != healthy.Snapshot {
		return ErrConflict
	}
	if _, err = q.AutoReadOrigin(ctx, healthy.OperationID); !errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err = hostingTargetGate(ctx, tx, placement.ProjectID, healthy.Snapshot, false); err != nil {
		return err
	}
	if err = requireHostingRuntimeAgent(ctx, tx, healthy.Snapshot); err != nil {
		return err
	}
	eligible, err := q.ProxyVerifiedSwitchRunnerEligible(ctx, p.RunnerID)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	target, err := q.LockHostingResourceTarget(ctx, doc.Policy.ResourceID)
	if err != nil {
		return hostingError(err)
	}
	c := autoContext{Version: 1, OwnerID: owner, ConfigurationHash: domain.HostingDocumentHash(cfg), ResourceID: doc.Policy.ResourceID, ProjectID: doc.Policy.ProjectID, ResourceGeneration: doc.Policy.ResourceGeneration, SourceGeneration: doc.Policy.SourceGeneration, RuntimeGeneration: cfg.ExpectedRuntimeConfigurationGeneration, Placement: placement, RuntimeNamespace: target.RuntimeNamespace, ProxyID: p.ID, ProxyConfigurationHash: domain.HostingDocumentHash(p.Configuration), DaemonID: p.DaemonID, ManualHealthyID: healthy.OperationID, BaselineOperationID: prior.ID, BaselineGeneration: p.LoadedGeneration, Routes: []autoRoute{}}
	existing, err := q.AutoLockControl(ctx, c.ResourceID)
	if err == nil {
		c.ManualEpoch = existing.ManualEpoch
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	for _, pick := range cfg.Routes {
		row, e := q.ProxyGetRoute(ctx, pick.RouteID)
		if e != nil {
			return hostingError(e)
		}
		if row.ProxyID != p.ID || row.ProjectID != c.ProjectID || row.ResourceID != c.ResourceID || row.BindingID != placement.BindingID || row.Version != pick.ExpectedVersion || row.SelectedReleaseOperationID != pick.ExpectedSelectedReleaseOperationID || !row.Enabled {
			return ErrConflict
		}
		if _, e = proxyHealthyTarget(ctx, tx, p, row); e != nil {
			return e
		}
		c.Routes = append(c.Routes, autoRoute{ID: row.ID, Version: row.Version, ReleaseID: row.SelectedReleaseOperationID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, GrantID: row.GrantID})
	}
	now, err := pushClock(ctx, tx)
	if err != nil {
		return err
	}
	if !autoLoadedHTTPS(prior, c.Routes, now) {
		return ErrConflict
	}
	_, err = q.AutoPutControl(ctx, sqlcgen.AutoPutControlParams{ResourceID: c.ResourceID, PolicyID: doc.Policy.ID, PolicyGeneration: doc.Policy.Generation, Body: buildJSON(c), BodyHash: domain.HostingDocumentHash(c)})
	return err
}

// autoCurrent checks immutable reviewed identity and current authority. Route
// selection equality is checked separately at admission; a known automatic
// switch may already have tentatively changed its selections when a newer push
// arrives. Public manual choices independently invalidate the epoch.
func autoCurrent(ctx context.Context, tx pgx.Tx, doc githubPushDocument) (autoContext, error) {
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
	if err = authorizeBuildActor(ctx, tx, c.OwnerID, c.ProjectID, "owner"); err != nil {
		return c, err
	}
	placement, err := q.ProxyResourceTarget(ctx, c.ResourceID)
	if err != nil || placement != c.Placement {
		return c, ErrConflict
	}
	r, err := q.LockHostingResourceTarget(ctx, c.ResourceID)
	if err != nil {
		return c, hostingError(err)
	}
	if r.Generation != c.ResourceGeneration || r.RuntimeNamespace != c.RuntimeNamespace {
		return c, ErrConflict
	}
	generation, err := autoRuntimeGeneration(ctx, q, c.ResourceID)
	if err != nil {
		return c, err
	}
	if generation != c.RuntimeGeneration {
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
		if _, e = proxyHealthyTarget(ctx, tx, p, row); e != nil {
			return c, e
		}
	}
	return c, nil
}
func autoPushCurrent(ctx context.Context, tx pgx.Tx, doc githubPushDocument, target bool) error {
	if err := pushCurrent(ctx, tx, doc, target); err != nil {
		return err
	}
	_, err := autoCurrent(ctx, tx, doc)
	return err
}

func autoManualMutation(ctx context.Context, tx pgx.Tx, resource string) error {
	_, err := sqlcgen.New(tx).AutoBumpManualEpoch(ctx, resource)
	return err
}
func autoSelections(ctx context.Context, tx pgx.Tx, c autoContext) error {
	for _, route := range c.Routes {
		row, err := sqlcgen.New(tx).ProxyGetRoute(ctx, route.ID)
		if err != nil {
			return hostingError(err)
		}
		if !autoRouteSelection(row, route) {
			return ErrConflict
		}
	}
	return nil
}
func autoStatus(ctx context.Context, tx pgx.Tx, doc githubPushDocument) (*domain.HostingAutoDeployStatus, error) {
	if doc.Configuration.AutomaticDeployment == nil {
		return nil, nil
	}
	out := &domain.HostingAutoDeployStatus{Mode: domain.HostingAutoDeployMode, State: "idle", Reason: "awaiting_push"}
	control, err := sqlcgen.New(tx).AutoReadControl(ctx, doc.Policy.ResourceID)
	if err != nil {
		return nil, hostingError(err)
	}
	out.ManualEpoch = control.ManualEpoch
	if _, err = autoReadCurrent(ctx, tx, doc); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, auth.ErrForbidden) {
			out.State, out.Reason = "blocked", "review_invalidated"
			return out, nil
		}
		return nil, err
	}
	row, err := sqlcgen.New(tx).AutoReadDemand(ctx, doc.Policy.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := decodeAutoDemand(row)
	if err != nil {
		return nil, err
	}
	if row.PolicyID != doc.Policy.ID || row.PolicyGeneration != doc.Policy.Generation {
		return out, nil
	}
	out.State, out.Reason, out.DesiredCommit, out.BuildID = row.Stage, row.Reason, row.CommitSha, row.BuildID
	out.DeploymentOperationID, out.SwitchOperationID = d.DeploymentID, d.SwitchID
	out.Attempts = int(row.Attempts)
	out.ExpiresAt = &row.ExpiresAt
	out.ObservedLoadedGeneration = row.ObservedLoadedGeneration
	return out, nil
}

// Manual route writers with reviewed automation take the same sorted prefix
// before changing a selection. Build-only resources retain their original path.
func autoManualPrelock(ctx context.Context, tx pgx.Tx, resource string) error {
	_, err := sqlcgen.New(tx).AutoReadControl(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = autoPrelock(ctx, tx, resource)
	return err
}

type autoOptionsRoute struct {
	Row             sqlcgen.HostingProxyRoute         `json:"row"`
	Grant           sqlcgen.HostingProxyHostnameGrant `json:"grant"`
	Release         domain.HostingHealthy             `json:"release"`
	ReleaseState    string                            `json:"release_state"`
	RetainedRunning bool                              `json:"retained_running"`
}
type autoOptionsSnapshot struct {
	Now                time.Time                    `json:"now"`
	ResourceID         string                       `json:"resource_id"`
	ProjectID          string                       `json:"project_id"`
	ResourceGeneration int64                        `json:"resource_generation"`
	SourceGeneration   int64                        `json:"source_generation"`
	Supported          bool                         `json:"supported"`
	Bound              bool                         `json:"bound"`
	BindingID          string                       `json:"binding_id"`
	DestinationID      string                       `json:"destination_id"`
	ServerID           string                       `json:"server_id"`
	RunnerID           string                       `json:"runner_id"`
	RuntimeGeneration  int64                        `json:"runtime_generation"`
	ManualEpoch        int64                        `json:"manual_epoch"`
	RestoreHold        bool                         `json:"restore_hold"`
	DestinationCurrent bool                         `json:"destination_current"`
	RunnerEligible     bool                         `json:"runner_eligible"`
	Healthy            domain.HostingHealthy        `json:"healthy"`
	HealthySnapshot    domain.HostingSnapshot       `json:"healthy_snapshot"`
	ManualHealthy      bool                         `json:"manual_healthy"`
	Proxy              domain.HostingProxy          `json:"proxy"`
	Loaded             domain.HostingProxyOperation `json:"loaded"`
	Routes             []autoOptionsRoute           `json:"routes"`
}

// autoOptions uses one bounded MVCC statement, never mutation locks. Its private
// snapshot is decoded locally and only closed reviewed facts leave the store.
func autoOptions(ctx context.Context, q *sqlcgen.Queries, resource string) (*domain.HostingAutoDeployOptions, error) {
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := q.AutoReadOptions(read, resource)
	if err != nil {
		return nil, err
	}
	var snapshot autoOptionsSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, ErrConflict
	}
	out := &domain.HostingAutoDeployOptions{Reason: "baseline_changed"}
	if !snapshot.Bound {
		out.Reason = "unbound"
		return out, nil
	}
	if snapshot.RestoreHold {
		out.Reason = "restore_hold"
		return out, nil
	}
	if !snapshot.RunnerEligible {
		out.Reason = "runner_unsupported"
		return out, nil
	}
	h := snapshot.Healthy
	if !snapshot.ManualHealthy || h.OperationID == "" || h.Snapshot.LocalBuild == (domain.HostingLocalBuildSource{}) {
		out.Reason = "manual_healthy_required"
		return out, nil
	}
	if !snapshot.Supported || !snapshot.DestinationCurrent || h.Snapshot != snapshot.HealthySnapshot || h.Snapshot.ResourceID != snapshot.ResourceID || h.Snapshot.ResourceGeneration != snapshot.ResourceGeneration || h.Snapshot.LocalBuild.SourceGeneration != snapshot.SourceGeneration || h.Snapshot.BindingID != snapshot.BindingID || h.Snapshot.DestinationID != snapshot.DestinationID || h.Snapshot.ServerID != snapshot.ServerID || h.Snapshot.RunnerID != snapshot.RunnerID || h.Snapshot.RuntimeConfiguration.Generation != snapshot.RuntimeGeneration || domain.ValidateHostingSnapshotRelease(h.Snapshot, h.Release) != nil {
		return out, nil
	}
	p := snapshot.Proxy
	if p.ID == "" || !p.Enabled || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.LoadedGeneration <= 0 || p.DaemonID == "" || p.DaemonID != h.Release.DaemonID || p.ID != snapshot.Loaded.ProxyID || p.RunnerID != snapshot.RunnerID || p.DestinationID != snapshot.DestinationID || p.ServerID != snapshot.ServerID || snapshot.Loaded.Generation != p.LoadedGeneration {
		out.Reason = "proxy_not_ready"
		return out, nil
	}
	if len(snapshot.Routes) == 0 || len(snapshot.Routes) > 32 {
		out.Reason = "routes_unavailable"
		return out, nil
	}
	routes := make([]autoRoute, 0, len(snapshot.Routes))
	picks := make([]domain.HostingAutoDeployRouteOption, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		r, g := route.Row, route.Grant
		if r.ResourceID != resource || r.ProjectID != snapshot.ProjectID || r.BindingID != snapshot.BindingID || r.ProxyID != p.ID || !r.Enabled || r.Version < 1 || g.ID != r.GrantID || g.ProxyID != p.ID || g.ProjectID != r.ProjectID || g.RevokedAt != nil || !domain.HostingHostnameGranted(g.Hostname, g.Wildcard, r.Hostname) || !proxyReleaseQualified(p, r, route.ReleaseState, route.RetainedRunning, route.Release) {
			out.Reason = "routes_unavailable"
			return out, nil
		}
		routes = append(routes, autoRoute{ID: r.ID, Version: r.Version, ReleaseID: r.SelectedReleaseOperationID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, GrantID: r.GrantID})
		picks = append(picks, domain.HostingAutoDeployRouteOption{RouteID: r.ID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, Version: r.Version, SelectedReleaseOperationID: r.SelectedReleaseOperationID})
	}
	if !autoLoadedHTTPS(snapshot.Loaded, routes, snapshot.Now) {
		out.Reason = "proxy_not_ready"
		return out, nil
	}
	out.Available, out.Reason = true, "eligible"
	out.Baseline = &domain.HostingAutoDeployBaseline{HealthyOperationID: h.OperationID, LoadedProxyOperationID: snapshot.Loaded.ID, LoadedGeneration: p.LoadedGeneration, RuntimeConfigurationGeneration: snapshot.RuntimeGeneration, ContainerPort: h.Snapshot.ContainerPort, Readiness: h.Snapshot.Readiness, Routes: picks, ManualEpoch: snapshot.ManualEpoch}
	return out, nil
}

// autoOwnerOptions is called only by GET, including an unconfigured policy.
// The GET caller repeats fresh authority after closing the snapshot.
func autoOwnerOptions(ctx context.Context, tx pgx.Tx, p domain.GitHubPushPolicy) (domain.GitHubPushPolicy, error) {
	p.OwnerAutomaticDeploymentOptions = nil
	if !p.CanManage {
		return p, nil
	}
	options, err := autoOptions(ctx, sqlcgen.New(tx), p.ResourceID)
	if err != nil {
		return p, err
	}

	p.OwnerAutomaticDeploymentOptions = options
	return p, nil
}
