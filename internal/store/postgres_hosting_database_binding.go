package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func databaseBindingSchema(ctx context.Context, q *sqlcgen.Queries) error {
	present, err := q.HostingDatabaseBindingSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !present {
		return ErrHostingDatabaseBindingUnavailable
	}
	compatible, err := q.HostingDatabaseBindingSchemaCompatible(ctx)
	if err != nil {
		return err
	}
	if !compatible {
		return ErrHostingDatabaseBindingUnavailable
	}
	return nil
}

// Historical receipt paths call this only after their exact-replay branch.
func databaseBindingPendingGate(ctx context.Context, tx pgx.Tx, resource string) error {
	q := sqlcgen.New(tx)
	present, err := q.HostingDatabaseBindingSchemaPresent(ctx)
	if err != nil || !present {
		return err // Pre-binding schemas retain their existing v1 behavior.
	}
	if err = databaseBindingSchema(ctx, q); err != nil {
		return err
	}
	pending, err := q.HostingDatabaseBindingPending(ctx, resource)
	if err != nil {
		return err
	}
	if pending {
		return ErrConflict
	}
	return nil
}

func readDatabaseBinding(ctx context.Context, q *sqlcgen.Queries, resource string) (domain.HostingDatabaseBinding, error) {
	var binding domain.HostingDatabaseBinding
	row, err := q.ReadHostingDatabaseBinding(ctx, resource)
	if err != nil {
		return binding, err
	}
	if json.Unmarshal(row.Body, &binding) != nil || binding.Hash != row.BodyHash || binding.ApplicationResourceID != resource || binding.RuntimeConfiguration.Generation != row.RuntimeGeneration || binding.RuntimeConfiguration.Hash != row.RuntimeHash {
		return domain.HostingDatabaseBinding{}, ErrConflict
	}
	return binding, nil
}

func databaseBindingGrant(ctx context.Context, q *sqlcgen.Queries, project, destination string) error {
	revoked, err := q.LockHostingProjectGrant(ctx, sqlcgen.LockHostingProjectGrantParams{DestinationID: destination, ProjectID: project})
	if err != nil || revoked != nil {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return ErrConflict
	}
	return nil
}

// Drain deferred checks before rechecking locks and the advancing token clock.
func finalizeDatabaseBinding(ctx context.Context, tx pgx.Tx, actor, project, destination, role string) error {
	q := sqlcgen.New(tx)
	if err := q.FlushHostingDatabaseBindingConstraints(ctx); err != nil {
		return err
	}
	if err := authorizeBuildActor(ctx, tx, actor, project, role); err != nil {
		return err
	}
	if err := databaseBindingGrant(ctx, q, project, destination); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, actor)
}

// GetHostingDatabaseBinding reads immutable desired input, never endpoint readiness.
func (s *PostgresStore) GetHostingDatabaseBinding(ctx context.Context, actor, resource string) (domain.HostingDatabaseBinding, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBinding, error) {
		var zero domain.HostingDatabaseBinding
		q := sqlcgen.New(tx)
		if err := databaseBindingSchema(ctx, q); err != nil {
			return zero, err
		}
		if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
			return zero, err
		}
		r, err := q.ReadHostingRuntimeResource(ctx, resource)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer"); err != nil {
			return zero, err
		}
		binding, err := readDatabaseBinding(ctx, q, resource)
		if err != nil {
			return zero, err
		}
		return binding, finalizeDatabaseBinding(ctx, tx, actor, r.ProjectID, binding.DestinationID, "viewer")
	})
}

func lockDatabaseBindingTargets(ctx context.Context, tx pgx.Tx, app, database string) (sqlcgen.LockHostingResourceTargetRow, sqlcgen.LockHostingResourceTargetRow, error) {
	var zero sqlcgen.LockHostingResourceTargetRow
	if app == database {
		return zero, zero, ErrConflict
	}
	q := sqlcgen.New(tx)
	destination, err := q.HostingResourceDestination(ctx, app)
	if err != nil {
		return zero, zero, err
	}
	if err = q.LockHostingExecutionDestination(ctx, destination); err != nil {
		return zero, zero, err
	}
	rows := map[string]sqlcgen.LockHostingResourceTargetRow{}
	ids := []string{app, database}
	slices.Sort(ids)
	for _, id := range ids {
		row, e := q.LockHostingResourceTarget(ctx, id)
		if e != nil {
			return zero, zero, e
		}
		if row.DestinationID != destination {
			return zero, zero, ErrConflict
		}
		rows[id] = row
	}
	a, d := rows[app], rows[database]
	if a.ProjectID != d.ProjectID || a.ServerID != d.ServerID || a.RunnerID != d.RunnerID || d.Kind != "database" {
		return zero, zero, ErrConflict
	}
	bindings := []string{a.BindingID, d.BindingID}
	slices.Sort(bindings)
	for _, binding := range bindings {
		if _, e := q.LockHostingAdmissionState(ctx, binding); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return zero, zero, e
		}
	}
	return a, d, nil
}

func databaseBindingHealthy(ctx context.Context, tx pgx.Tx, resource, binding string) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, binding)
	if err != nil {
		return zero, err
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(raw, &healthy) != nil || healthy.OperationID == "" {
		return zero, ErrConflict
	}
	op, err := hostingOperation(ctx, tx, healthy.OperationID)
	if err != nil {
		return zero, err
	}
	if op.ResourceID != resource || op.BindingID != binding || op.State != "succeeded" || op.MutationHold || op.RestoreOwnershipHeld || op.Plan == nil || op.Release == nil || op.CompleteReceipt == nil || op.Result == nil || !op.Result.Ready || op.Result.Uncertain || op.Result.ContainerID != healthy.ContainerID || op.Result.PlanHash != healthy.PlanHash || op.Plan.Hash != healthy.PlanHash || op.Snapshot != healthy.Snapshot || *op.Release != healthy.Release || domain.ValidateHostingDatabasePlan(*op.Plan) != nil || domain.ValidateHostingHealthyStorage(healthy) != nil {
		return zero, ErrConflict
	}
	return op, nil
}

func databaseBindingEntries(binding domain.HostingDatabaseBinding) []domain.HostingRuntimeEntry {
	recipe, _ := domain.HostingDatabaseRecipeFor(binding.Database.Engine, binding.Database.RecipeVersion) // Validated binding.
	literals := map[string]string{"REDIS_HOST": binding.Alias, "REDIS_PORT": strconv.Itoa(recipe.ContainerPort), "REDIS_USERNAME": binding.Credential.Username}
	password := "REDIS_PASSWORD"
	if binding.Database.Engine == "postgresql" {
		literals = map[string]string{"PGHOST": binding.Alias, "PGPORT": strconv.Itoa(recipe.ContainerPort), "PGDATABASE": recipe.DatabaseName, "PGUSER": binding.Credential.Username}
		password = "PGPASSWORD"
	}
	entries := make([]domain.HostingRuntimeEntry, 0, len(binding.EnvironmentNames))
	for _, name := range binding.EnvironmentNames {
		entry := domain.HostingRuntimeEntry{Name: name, Kind: "literal"}
		if name == password {
			entry.Kind = "managed_secret"
			entry.Secret = &domain.HostingRuntimeSecretReference{SecretID: binding.Credential.SecretID, Version: binding.Credential.Version}
		} else {
			value := literals[name]
			entry.Value = &value
		}
		entries = append(entries, entry)
	}
	return entries
}

// PutHostingDatabaseBinding atomically saves one pending binding and injected
// runtime version. It does not dispatch jobs, connect containers or reveal secrets.
func (s *PostgresStore) PutHostingDatabaseBinding(ctx context.Context, actor, application string, in domain.HostingDatabaseBindingInput, event domain.AuditEvent) (domain.HostingDatabaseBinding, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingDatabaseBinding{}, err
	}
	requestHash := domain.HostingDocumentHash(struct {
		Actor, Application string
		Input              domain.HostingDatabaseBindingInput
	}{actor, application, in})
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBinding, error) {
		var zero domain.HostingDatabaseBinding
		q := sqlcgen.New(tx)
		if err := databaseBindingSchema(ctx, q); err != nil {
			return zero, err
		}
		app, database, err := lockDatabaseBindingTargets(ctx, tx, application, in.DatabaseResourceID)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, app.ProjectID, "owner"); err != nil {
			return zero, err
		}
		if err = databaseBindingGrant(ctx, q, app.ProjectID, app.DestinationID); err != nil {
			return zero, err
		}
		generation, err := q.LockHostingRuntimeConfiguration(ctx, application)
		if errors.Is(err, pgx.ErrNoRows) {
			generation, err = 0, nil
		}
		if err != nil {
			return zero, err
		}
		receipt, err := q.ReadHostingDatabaseBindingReceipt(ctx, sqlcgen.ReadHostingDatabaseBindingReceiptParams{ApplicationResourceID: application, RequestID: in.RequestID})
		if err == nil {
			var accepted domain.HostingDatabaseBindingInput
			if receipt.ActorID != actor || receipt.RequestHash != requestHash || json.Unmarshal(receipt.AcceptedRequest, &accepted) != nil || accepted != in {
				return zero, ErrConflict
			}
			binding, e := readDatabaseBinding(ctx, q, application)
			if e != nil || binding.DatabaseResourceID != in.DatabaseResourceID || binding.ProjectID != app.ProjectID || binding.DestinationID != app.DestinationID || binding.ApplicationBindingID != app.BindingID || binding.DatabaseBindingID != database.BindingID || binding.ServerID != app.ServerID || binding.RunnerID != app.RunnerID || binding.Hash != receipt.BindingHash || binding.RuntimeConfiguration.Generation != receipt.RuntimeGeneration || binding.RuntimeConfiguration.Hash != receipt.RuntimeHash {
				return zero, ErrConflict
			}
			version, e := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: application, Generation: receipt.RuntimeGeneration})
			if e != nil || version.BodyHash != receipt.RuntimeHash || version.ProjectID != app.ProjectID {
				return zero, ErrConflict
			}
			var document domain.HostingRuntimeDocument
			if json.Unmarshal(version.Body, &document) != nil {
				return zero, ErrConflict
			}
			canonical, refs, e := domain.CanonicalHostingRuntimeDocument(document.Entries)
			if e != nil || domain.HostingDocumentHash(canonical) != version.BodyHash {
				return zero, ErrConflict
			}
			if e = validateRuntimeReferences(ctx, q, binding.ProjectID, refs); e != nil {
				return zero, e
			}
			return binding, finalizeDatabaseBinding(ctx, tx, actor, app.ProjectID, app.DestinationID, "owner")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if err = databaseBindingPendingGate(ctx, tx, application); err != nil {
			return zero, err
		}
		if app.Generation != in.ExpectedResourceGeneration || database.Generation != in.ExpectedDatabaseGeneration || generation != in.ExpectedRuntimeGeneration || generation == math.MaxInt64 {
			return zero, ErrConflict
		}
		supported, err := q.HostingDatabaseBindingApplicationSupported(ctx, application)
		if err != nil || supported == nil || !*supported {
			return zero, ErrConflict
		}
		for _, target := range []string{app.BindingID, database.BindingID} {
			safe, e := q.HostingDatabaseBindingTargetSafe(ctx, target)
			if e != nil || safe == nil || !*safe {
				return zero, ErrConflict
			}
		}
		healthy, err := databaseBindingHealthy(ctx, tx, in.DatabaseResourceID, database.BindingID)
		if err != nil {
			return zero, err
		}
		binding := domain.HostingDatabaseBinding{Version: domain.HostingDatabaseBindingVersion, ID: domain.HostingDatabaseBindingID(app.BindingID, database.BindingID), Generation: 1, ApplicationResourceID: application, ApplicationResourceGeneration: app.Generation, ApplicationBindingID: app.BindingID, DatabaseResourceID: in.DatabaseResourceID, DatabaseResourceGeneration: database.Generation, DatabaseBindingID: database.BindingID, ProjectID: app.ProjectID, DestinationID: app.DestinationID, ServerID: app.ServerID, RunnerID: app.RunnerID, Database: healthy.Snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(database.BindingID), Alias: domain.HostingDatabaseBindingAlias(database.BindingID), EnvironmentNames: domain.HostingDatabaseBindingEnvironmentNames(healthy.Snapshot.Database.Engine)}
		for _, ref := range healthy.Plan.Database.Credentials {
			if ref.Role == "app" {
				binding.Credential = ref
			}
		}
		credentials, err := q.HostingDatabaseCredentials(ctx, in.DatabaseResourceID)
		if err != nil {
			return zero, err
		}
		matched := false
		for _, ref := range credentials {
			if ref.Role == "app" && ref.Username == binding.Credential.Username && ref.SecretID == binding.Credential.SecretID && ref.Version == binding.Credential.Version {
				matched = true
			}
		}
		if !matched {
			return zero, ErrConflict
		}
		entries := []domain.HostingRuntimeEntry{}
		if generation > 0 {
			prior, e := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: application, Generation: generation})
			if e != nil {
				return zero, e
			}
			saved, e := runtimeVersion(prior, app.Generation)
			if e != nil {
				return zero, e
			}
			entries = saved.Entries
		}
		for _, entry := range entries {
			if slices.Contains(binding.EnvironmentNames, entry.Name) {
				return zero, ErrConflict
			}
		}
		entries = append(entries, databaseBindingEntries(binding)...)
		doc, refs, err := domain.CanonicalHostingRuntimeDocument(entries)
		if err != nil {
			return zero, ErrConflict
		}
		binding.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{Generation: generation + 1, Hash: domain.HostingDocumentHash(doc)}
		binding.Hash = binding.DocumentHash()
		appSnapshot := domain.HostingSnapshot{ResourceID: application, ResourceGeneration: app.Generation, BindingID: app.BindingID, DestinationID: app.DestinationID, ServerID: app.ServerID, RunnerID: app.RunnerID, PolicyVersion: domain.HostingRuntimePolicyVersion, RuntimeConfiguration: binding.RuntimeConfiguration}
		if binding.ValidateTargets(app.ProjectID, appSnapshot, database.ProjectID, *healthy.Plan) != nil {
			return zero, ErrConflict
		}
		if err = validateRuntimeReferences(ctx, q, app.ProjectID, refs); err != nil {
			return zero, err
		}
		if generation == 0 {
			if err = q.CreateHostingRuntimeConfiguration(ctx, sqlcgen.CreateHostingRuntimeConfigurationParams{ResourceID: application, ProjectID: app.ProjectID}); err != nil {
				return zero, err
			}
		}
		if _, err = q.InsertHostingRuntimeVersion(ctx, sqlcgen.InsertHostingRuntimeVersionParams{ResourceID: application, ProjectID: app.ProjectID, Generation: generation + 1, Body: buildJSON(doc), BodyHash: binding.RuntimeConfiguration.Hash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		for _, ref := range refs {
			if err = q.InsertHostingRuntimeSecretReference(ctx, sqlcgen.InsertHostingRuntimeSecretReferenceParams{ResourceID: application, ProjectID: app.ProjectID, Generation: generation + 1, SecretID: ref.SecretID, SecretVersion: ref.Version}); err != nil {
				return zero, err
			}
		}
		if generation > 0 {
			if err = casBuild(q.AdvanceHostingRuntimeConfiguration(ctx, sqlcgen.AdvanceHostingRuntimeConfigurationParams{ResourceID: application, Generation: generation})); err != nil {
				return zero, err
			}
		}
		event.ID, event.ActorID, event.Action, event.TargetID = domain.HostingRuntimeAuditID(application, in.RequestID), actor, "hosting.runtime_configuration.updated", application
		event.Metadata = map[string]any{"purpose": "database_binding_pending", "binding_id": binding.ID, "database_resource_id": in.DatabaseResourceID, "project_id": app.ProjectID, "resource_id": application, "runtime_generation": generation + 1, "entry_count": len(doc.Entries), "secret_reference_count": len(refs)}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertHostingRuntimeReceipt(ctx, sqlcgen.InsertHostingRuntimeReceiptParams{ResourceID: application, ProjectID: app.ProjectID, RequestID: in.RequestID, ActorID: actor, RequestHash: requestHash, ExpectedResourceGeneration: app.Generation, ExpectedRuntimeGeneration: generation, RuntimeGeneration: generation + 1, AuditID: event.ID}); err != nil {
			return zero, err
		}
		if err = q.InsertHostingDatabaseBinding(ctx, sqlcgen.InsertHostingDatabaseBindingParams{ID: binding.ID, ApplicationResourceID: application, ApplicationBindingID: app.BindingID, DatabaseResourceID: in.DatabaseResourceID, DatabaseBindingID: database.BindingID, ProjectID: app.ProjectID, DestinationID: app.DestinationID, DatabaseOperationID: healthy.ID, RuntimeGeneration: generation + 1, RuntimeHash: binding.RuntimeConfiguration.Hash, CredentialSecretID: binding.Credential.SecretID, CredentialVersion: binding.Credential.Version, Body: buildJSON(binding), BodyHash: binding.Hash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		if err = q.InsertHostingDatabaseBindingReceipt(ctx, sqlcgen.InsertHostingDatabaseBindingReceiptParams{ApplicationResourceID: application, RequestID: in.RequestID, ActorID: actor, AcceptedRequest: buildJSON(in), RequestHash: requestHash, BindingHash: binding.Hash, RuntimeGeneration: generation + 1, RuntimeHash: binding.RuntimeConfiguration.Hash, AuditID: event.ID}); err != nil {
			return zero, err
		}
		return binding, finalizeDatabaseBinding(ctx, tx, actor, app.ProjectID, app.DestinationID, "owner")
	})
}
