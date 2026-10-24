package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func databaseConnectionSchema(ctx context.Context, q *sqlcgen.Queries) error {
	present, err := q.DatabaseConnectionSchemaPresent(ctx)
	if err != nil || !present {
		return ErrHostingDatabaseConnectionUnavailable
	}
	ok, err := q.DatabaseConnectionSchema(ctx)
	if err != nil || !ok {
		return ErrHostingDatabaseConnectionUnavailable
	}
	return nil
}

func databaseConnectionBindingGate(ctx context.Context, tx pgx.Tx, app, database string) error {
	q := sqlcgen.New(tx)
	present, err := q.DatabaseConnectionSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	c, err := q.ReadDatabaseConnectionByDatabase(ctx, database)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.ApplicationResourceID != app {
		return ErrConflict
	}
	return nil
}

func requireConnectionAgent(ctx context.Context, tx pgx.Tx, runner string, initial bool) error {
	q := sqlcgen.New(tx)
	r, err := q.LockObservationRunner(ctx, runner)
	if err != nil {
		return err
	}
	if r.Status != domain.RunnerActive || r.TokenHash == "" || slices.Contains(r.Capabilities, "shell") {
		return ErrConflict
	}
	capabilities := []string{domain.HostingApplicationDatabaseCapability, domain.HostingDatabaseMaintenanceCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseDeploy, domain.RunTypeImageDeploy}
	if initial {
		capabilities = append(capabilities, domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability)
	}
	for _, capability := range capabilities {
		if !slices.Contains(r.Capabilities, capability) {
			return ErrConflict
		}
	}
	_, err = q.LockObservationEnrollment(ctx, sqlcgen.LockObservationEnrollmentParams{RunnerID: runner, CredentialHash: &r.TokenHash})
	return hostingError(err)
}

func connectionChildID(connection, phase string) string {
	return "hop_conn_" + phase + "_" + domain.HostingDocumentHash(connection)[7:39]
}

// Existing evidence is reusable, but an unfinished capture keeps its original
// actor and lease. Admission must not reserve a workflow that cannot finish it.
func connectionCapturePrerequisite(ctx context.Context, tx pgx.Tx, source, binding, actor string) (bool, error) {
	q := sqlcgen.New(tx)
	c, err := readNetworkCapture(ctx, q, domain.HostingDatabaseNetworkCaptureID(source))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if c.ID != domain.HostingDatabaseNetworkCaptureID(source) || c.Plan.SourceOperationID != source || c.Plan.BindingID != binding {
		return false, ErrConflict
	}
	p, err := networkCaptureSource(ctx, tx, source, actor)
	if err != nil || !networkCapturePlanEqual(c.Plan, p) {
		return false, ErrConflict
	}
	if c.Receipt != nil {
		return true, networkCaptureHistorical(ctx, tx, c, actor)
	}
	if c.CreatedBy != actor || c.State != domain.RunQueued && c.State != domain.RunRunning {
		return false, ErrConflict
	}
	if c.State == domain.RunRunning {
		lease, e := q.GetActiveLeaseForRun(ctx, c.RunID)
		if e != nil || lease.RunnerID != p.RunnerID {
			return false, ErrConflict
		}
	}
	return true, nil
}

func connectionView(ctx context.Context, q *sqlcgen.Queries, c sqlcgen.HostingDatabaseConnection) (domain.HostingDatabaseConnection, error) {
	v := domain.HostingDatabaseConnection{ID: c.ID, ProjectID: c.ProjectID, ApplicationResourceID: c.ApplicationResourceID, DatabaseResourceID: c.DatabaseResourceID, BindingID: c.BindingID, State: c.State, ReasonCode: c.ReasonCode, CaptureID: c.CaptureID, UpgradeOperationID: c.UpgradeOperationID, ApplicationOperationID: c.ApplicationOperationID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, RecoveryLimit: "observe_only_once_no_expired_authority_resume"}
	b, err := readDatabaseBinding(ctx, q, c.ApplicationResourceID)
	if err != nil || b.ID != c.BindingID {
		return domain.HostingDatabaseConnection{}, ErrConflict
	}
	v.BindingHash = b.Hash
	head, err := q.ReadDatabaseMembershipHead(ctx, b.DatabaseBindingID)
	if err == nil {
		if head.ConnectionID != c.ID {
			return domain.HostingDatabaseConnection{}, ErrConflict
		}
		v.MembershipRevision = head.Revision
		var members []domain.HostingDatabaseNetworkMemberEvidence
		if json.Unmarshal(head.Members, &members) != nil || members == nil || len(members) > 1 {
			return domain.HostingDatabaseConnection{}, ErrConflict
		}
		if len(members) == 1 {
			v.ApplicationOperationID = members[0].ApplicationHealthy.OperationID
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingDatabaseConnection{}, err
	}
	hold, err := q.LockDatabaseNetworkHold(ctx, b.DatabaseBindingID)
	if err == nil {
		if hold.ConnectionID != c.ID || hold.OperationID == nil {
			return domain.HostingDatabaseConnection{}, ErrConflict
		}
		v.HoldOperationID = *hold.OperationID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingDatabaseConnection{}, err
	}
	return v, nil
}

func lockConnectionTargets(ctx context.Context, tx pgx.Tx, c sqlcgen.HostingDatabaseConnection, actor, role string) (domain.HostingDatabaseBinding, error) {
	var zero domain.HostingDatabaseBinding
	app, database, err := lockDatabaseBindingTargets(ctx, tx, c.ApplicationResourceID, c.DatabaseResourceID)
	if err != nil {
		return zero, err
	}
	if app.ProjectID != c.ProjectID || app.Kind != "application" {
		return zero, ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, actor, c.ProjectID, role); err != nil {
		return zero, err
	}
	q := sqlcgen.New(tx)
	if err = databaseBindingGrant(ctx, q, c.ProjectID, app.DestinationID); err != nil {
		return zero, err
	}
	b, err := readDatabaseBinding(ctx, q, c.ApplicationResourceID)
	if err != nil || b.ID != c.BindingID || b.ProjectID != c.ProjectID || b.ApplicationBindingID != app.BindingID || b.DatabaseBindingID != database.BindingID || b.DestinationID != app.DestinationID || b.ServerID != app.ServerID || b.RunnerID != app.RunnerID || b.DatabaseResourceID != c.DatabaseResourceID {
		return zero, ErrConflict
	}
	return b, nil
}

func finalizeConnectionRequest(ctx context.Context, tx pgx.Tx, c sqlcgen.HostingDatabaseConnection, b domain.HostingDatabaseBinding, actor, role string, initial bool) error {
	q := sqlcgen.New(tx)
	if err := q.FlushDatabaseConnection(ctx); err != nil {
		return err
	}
	if err := databaseBindingGrant(ctx, q, c.ProjectID, b.DestinationID); err != nil {
		return err
	}
	if initial {
		if err := requireConnectionAgent(ctx, tx, b.RunnerID, true); err != nil {
			return err
		}
	}
	if err := authorizeBuildActor(ctx, tx, actor, c.ProjectID, role); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, actor)
}

// ConnectHostingApplicationDatabase saves binding, runtime, workflow and River
// intent atomically. Admission cannot infer that runner capture is enabled.
func (s *PostgresStore) ConnectHostingApplicationDatabase(ctx context.Context, id, application, actor string, in domain.HostingDatabaseConnectionInput, event domain.AuditEvent) (domain.HostingDatabaseConnection, error) {
	var zero domain.HostingDatabaseConnection
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseConnectionEnabled() {
		return zero, ErrHostingDatabaseConnectionUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	requestHash := domain.HostingDocumentHash(struct {
		Actor, Application string
		Input              domain.HostingDatabaseConnectionInput
	}{actor, application, in})
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseConnection, error) {
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return zero, err
		}
		app, database, err := lockDatabaseBindingTargets(ctx, tx, application, in.Binding.DatabaseResourceID)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, app.ProjectID, domain.RoleOwner); err != nil {
			return zero, err
		}
		if err = databaseBindingGrant(ctx, q, app.ProjectID, app.DestinationID); err != nil {
			return zero, err
		}
		if err = requireConnectionAgent(ctx, tx, app.RunnerID, true); err != nil {
			return zero, err
		}
		old, err := q.ReadDatabaseConnection(ctx, application)
		if err == nil {
			if old.CreatedBy != actor || old.RequestHash != requestHash || !connectionJSONMatches(old.AcceptedRequest, in) {
				return zero, ErrConflict
			}
			binding, e := lockConnectionTargets(ctx, tx, old, actor, domain.RoleOwner)
			if e != nil {
				return zero, e
			}
			v, e := connectionView(ctx, q, old)
			if e != nil {
				return zero, e
			}
			return v, finalizeConnectionRequest(ctx, tx, old, binding, actor, domain.RoleOwner, true)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		supported, err := q.ConnectionApplicationSupported(ctx, sqlcgen.ConnectionApplicationSupportedParams{ID: application, DatabaseResourceID: in.Binding.DatabaseResourceID})
		if err != nil || !supported {
			return zero, ErrConflict
		}
		prior, err := q.GetHostingHealthy(ctx, app.BindingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if len(prior) != 0 {
			return zero, ErrConflict
		}
		for _, binding := range []string{app.BindingID, database.BindingID} {
			safe, e := q.HostingDatabaseBindingTargetSafe(ctx, binding)
			if e != nil || safe == nil || !*safe {
				return zero, ErrConflict
			}
		}
		if _, err = q.LockDatabaseNetworkHold(ctx, database.BindingID); !errors.Is(err, pgx.ErrNoRows) {
			if err != nil {
				return zero, err
			}
			return zero, ErrConflict
		}
		healthy, err := databaseBindingHealthy(ctx, tx, in.Binding.DatabaseResourceID, database.BindingID)
		if err != nil {
			return zero, err
		}
		if _, err = connectionCapturePrerequisite(ctx, tx, healthy.ID, database.BindingID, actor); err != nil {
			return zero, err
		}
		var source domain.ResourceSource
		if json.Unmarshal(app.Source, &source) != nil || source.Kind != "image" {
			return zero, ErrConflict
		}
		if _, err = domain.NormalizeHostingImage(source.Image.Reference); err != nil {
			return zero, ErrConflict
		}
		binding, err := putDatabaseBinding(ctx, tx, actor, application, in.Binding, event)
		if err != nil {
			return zero, err
		}
		// The binding helper drains its own deferred checks. Defer only the
		// new workflow association until its hold and River intent are inserted.
		if err = q.DeferDatabaseConnectionAdmission(ctx); err != nil {
			return zero, err
		}
		snapshot := domain.HostingSnapshot{ResourceID: application, ResourceGeneration: app.Generation, ImageReference: source.Image.Reference, BindingID: app.BindingID, DestinationID: app.DestinationID, RuntimeNamespace: app.RuntimeNamespace, ServerID: app.ServerID, RunnerID: app.RunnerID, ContainerPort: in.Deployment.ContainerPort, HostPort: in.Deployment.HostPort, Readiness: in.Deployment.Readiness, PolicyVersion: domain.HostingRuntimePolicyVersion, RuntimeConfiguration: binding.RuntimeConfiguration}
		if binding.ValidateTargets(app.ProjectID, snapshot, database.ProjectID, *healthy.Plan) != nil {
			return zero, ErrConflict
		}
		event.ID, event.ActorID, event.Action, event.TargetID = "aud_connection_"+id, actor, "hosting.database_connection.requested", id
		event.Metadata = map[string]any{"project_id": app.ProjectID, "application_resource_id": application, "database_resource_id": in.Binding.DatabaseResourceID, "binding_id": binding.ID, "binding_hash": binding.Hash, "request_hash": requestHash}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseConnection(ctx, sqlcgen.InsertDatabaseConnectionParams{ID: id, ApplicationResourceID: application, DatabaseResourceID: in.Binding.DatabaseResourceID, ProjectID: app.ProjectID, BindingID: binding.ID, SourceOperationID: healthy.ID, CaptureID: domain.HostingDatabaseNetworkCaptureID(healthy.ID), UpgradeOperationID: connectionChildID(id, "upgrade"), ApplicationOperationID: connectionChildID(id, "application"), AcceptedRequest: buildJSON(in), RequestHash: requestHash, BindingApplicationSnapshot: buildJSON(snapshot), CreatedBy: actor, AuditID: event.ID}); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseNetworkHold(ctx, sqlcgen.InsertDatabaseNetworkHoldParams{DatabaseBindingID: database.BindingID, ConnectionID: id, OperationID: hostingStringPointer(connectionChildID(id, "upgrade"))}); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueDatabaseConnectionTx(ctx, tx, hostingjobs.DatabaseConnectionArgs{ConnectionID: id}); err != nil {
			return zero, err
		}
		c, err := q.ReadDatabaseConnectionByID(ctx, id)
		if err != nil {
			return zero, err
		}
		v, err := connectionView(ctx, q, c)
		if err != nil {
			return zero, err
		}
		return v, finalizeConnectionRequest(ctx, tx, c, binding, actor, domain.RoleOwner, true)
	})
}

// GetHostingDatabaseConnection is a current viewer read, not execution authority.
func (s *PostgresStore) GetHostingDatabaseConnection(ctx context.Context, actor, application string) (domain.HostingDatabaseConnection, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseConnection, error) {
		var zero domain.HostingDatabaseConnection
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return zero, err
		}
		c, err := q.ReadDatabaseConnection(ctx, application)
		if err != nil {
			return zero, err
		}
		b, err := lockConnectionTargets(ctx, tx, c, actor, domain.RoleViewer)
		if err != nil {
			return zero, err
		}
		v, err := connectionView(ctx, q, c)
		if err != nil {
			return zero, err
		}
		return v, finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleViewer, false)
	})
}

// ListEligibleHostingDatabases checks at most 32 same-project/destination rows.
// The list contains no rejected or foreign identities, credentials or proof data.
func (s *PostgresStore) ListEligibleHostingDatabases(ctx context.Context, actor, application string) ([]domain.HostingDatabaseConnectionOption, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ids, err := buildTransaction(ctx, s, func(tx pgx.Tx) ([]string, error) {
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return nil, err
		}
		if err := lockHostingResourceDestination(ctx, tx, application); err != nil {
			return nil, err
		}
		app, err := q.LockHostingResourceTarget(ctx, application)
		if err != nil {
			return nil, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, app.ProjectID, domain.RoleViewer); err != nil {
			return nil, err
		}
		if err = databaseBindingGrant(ctx, q, app.ProjectID, app.DestinationID); err != nil {
			return nil, err
		}
		ids, err := q.EligibleConnectionDatabaseCandidates(ctx, application)
		if err != nil {
			return nil, err
		}
		return ids, proxyCheckLegacyToken(ctx, tx, actor)
	})
	if err != nil {
		return nil, err
	}
	options := make([]domain.HostingDatabaseConnectionOption, 0, len(ids))
	for _, id := range ids {
		option, e := buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseConnectionOption, error) {
			var zero domain.HostingDatabaseConnectionOption
			q := sqlcgen.New(tx)
			app, database, err := lockDatabaseBindingTargets(ctx, tx, application, id)
			if err != nil {
				return zero, err
			}
			if err = authorizeBuildActor(ctx, tx, actor, app.ProjectID, domain.RoleViewer); err != nil {
				return zero, err
			}
			if err = databaseBindingGrant(ctx, q, app.ProjectID, app.DestinationID); err != nil {
				return zero, err
			}
			supported, err := q.ConnectionApplicationSupported(ctx, sqlcgen.ConnectionApplicationSupportedParams{ID: application, DatabaseResourceID: id})
			if err != nil || !supported {
				return zero, ErrConflict
			}
			prior, err := q.GetHostingHealthy(ctx, app.BindingID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return zero, err
			}
			if len(prior) != 0 {
				return zero, ErrConflict
			}
			if _, err = q.ReadDatabaseConnectionByDatabase(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
				if err != nil {
					return zero, err
				}
				return zero, ErrConflict
			}
			for _, binding := range []string{app.BindingID, database.BindingID} {
				safe, e := q.HostingDatabaseBindingTargetSafe(ctx, binding)
				if e != nil || safe == nil || !*safe {
					return zero, ErrConflict
				}
			}
			healthy, err := databaseBindingHealthy(ctx, tx, id, database.BindingID)
			if err != nil {
				return zero, err
			}
			if err = requireConnectionAgent(ctx, tx, app.RunnerID, true); err != nil {
				return zero, err
			}
			resource, err := q.GetResource(ctx, id)
			if err != nil {
				return zero, err
			}
			if err = authorizeBuildActor(ctx, tx, actor, app.ProjectID, domain.RoleViewer); err != nil {
				return zero, err
			}
			if err = proxyCheckLegacyToken(ctx, tx, actor); err != nil {
				return zero, err
			}
			return domain.HostingDatabaseConnectionOption{ResourceID: id, Name: resource.Name, Engine: healthy.Snapshot.Database.Engine, Generation: database.Generation, DestinationID: database.DestinationID, Status: "available", ReasonCode: "eligible"}, nil
		})
		if e == nil {
			options = append(options, option)
		} else if !errors.Is(e, ErrConflict) && !errors.Is(e, ErrNotFound) && !errors.Is(e, pgx.ErrNoRows) {
			if errors.Is(e, auth.ErrForbidden) || errors.Is(e, auth.ErrUnauthenticated) {
				return nil, e
			}
			return nil, e
		}
	}
	return options, nil
}
