package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/db"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type runtimeAdmissionFixture struct {
	pg               *store.PostgresStore
	db               *pgxpool.Pool
	resource, runner string
	build            *domain.HostingBuild
}

func newRuntimeAdmissionFixture(t *testing.T, local bool) runtimeAdmissionFixture {
	t.Helper()
	if local {
		f, b := successfulLocalBuildFixture(t)
		stop := startBuildController(t, f)
		if err := stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		return runtimeAdmissionFixture{f.pg, f.db, f.src.ResourceID, f.runner, &b}
	}
	var stop func(context.Context) error
	pg, db, r, server, _ := hostingExecutionFixture(t, &stop)
	if err := stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	return runtimeAdmissionFixture{pg, db, r.ID, server.RunnerID, nil}
}
func (f runtimeAdmissionFixture) capabilities(t *testing.T, caps ...string) {
	t.Helper()
	for _, cap := range caps {
		if _, err := f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, cap); err != nil {
			t.Fatal(err)
		}
	}
}
func (f runtimeAdmissionFixture) request(t *testing.T, id, actor string, generation int64) (domain.HostingOperation, error) {
	t.Helper()
	if f.build != nil {
		in := localBuildDeploymentInput(f.build.ID)
		in.ExpectedRuntimeConfigurationGeneration = generation
		return f.pg.RequestHostingBuildDeployment(t.Context(), id, f.resource, actor, in, destinationEvent(id))
	}
	in := hostingDeploymentRequest()
	in.ExpectedRuntimeConfigurationGeneration = generation
	return f.pg.RequestHostingDeployment(t.Context(), id, f.resource, actor, in, destinationEvent(id))
}
func (f runtimeAdmissionFixture) prepare(t *testing.T, op domain.HostingOperation) (domain.HostingOperation, domain.HostingAuthority, domain.HostingPreparedRequest) {
	t.Helper()
	if err := f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(t.Context(), f.runner, a); err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "daemon_image_host", OS: "linux", Architecture: "amd64"}
	release.Hash = domain.HostingDocumentHash(release)
	if f.build != nil {
		release, err = domain.NewHostingLocalBuildRelease(*f.build.Artifact)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: op.ID + "-prepared", Release: release}
	op, err = f.pg.HostingPrepared(t.Context(), f.runner, req)
	if err != nil {
		t.Fatal(err)
	}
	return op, a, req
}

func TestPostgresHostingRuntimeEnvironmentAdmissionAndReplay(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "image"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			f := newRuntimeAdmissionFixture(t, local)
			// A saved empty document is still an explicit runtime generation.
			in := runtimeInput(1, 0)
			in.Entries = []domain.HostingRuntimeEntry{}
			saved := runtimeSave(t, f.pg, f.resource, in)
			if _, err := f.request(t, "omitted", "usr_bootstrap", 0); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("omitted generation %v", err)
			}
			if _, err := f.request(t, "future", "usr_bootstrap", 2); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("future generation %v", err)
			}
			if _, err := f.request(t, "old-agent", "usr_bootstrap", 1); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("old agent %v", err)
			}
			f.capabilities(t, domain.HostingRuntimeEnvironmentCapability)
			if _, err := f.request(t, "no-release-cap", "usr_bootstrap", 1); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("missing release cap %v", err)
			}
			f.capabilities(t, domain.HostingReleasePortsCapability)
			op, err := f.request(t, "runtime-operation", "usr_bootstrap", 1)
			if err != nil {
				t.Fatal(err)
			}
			doc, _, err := domain.CanonicalHostingRuntimeDocument(saved.Entries)
			if err != nil {
				t.Fatal(err)
			}
			if op.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: domain.HostingDocumentHash(doc)}) {
				t.Fatal("reference mismatch", op.Snapshot)
			}
			op, a, prepared := f.prepare(t, op)
			if !domain.HostingPlanFamilyMatches(op.Snapshot, *op.Plan) {
				t.Fatal("invalid compiler family")
			}
			if f.build != nil && (op.Snapshot.LocalBuild.Artifact != *f.build.Artifact || op.Release.LocalBuild != *f.build.Artifact) {
				t.Fatal("build artifact changed")
			}
			original := op
			// A new config blocks first mutation, but exact receipt and request replay survive it.
			runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, a); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("config drift apply %v", err)
			}
			replay, err := f.pg.HostingPrepared(t.Context(), f.runner, prepared)
			if err != nil || replay.Plan.Hash != original.Plan.Hash {
				t.Fatalf("prepared replay %v", err)
			}
			if f.build != nil {
				next := f.build.Snapshot.Source
				next.Generation++
				next.Settings.ContextDir = "changed"
				if _, err = configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", 1, next, destinationEvent("runtime-source-change")); err != nil {
					t.Fatal(err)
				}
			}
			replay, err = f.request(t, "ignored", "usr_bootstrap", 1)
			if err != nil || replay.ID != original.ID || replay.Snapshot != original.Snapshot || replay.Plan.Hash != original.Plan.Hash {
				t.Fatalf("request replay changed evidence %+v %v", replay, err)
			}
			if _, err = f.db.Exec(t.Context(), `INSERT INTO project_members(id,project_id,user_id,role) VALUES('runtime_member','proj_platform','usr_viewer','maintainer')`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.request(t, "ignored-viewer", "usr_viewer", 1); err != nil {
				t.Fatal("current maintainer replay", err)
			}
			if _, err = f.db.Exec(t.Context(), `DELETE FROM project_members WHERE id='runtime_member'`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.request(t, "revoked", "usr_viewer", 1); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("revoked actor replay %v", err)
			}
			state, err := f.pg.GetHostingOperation(t.Context(), op.ID)
			if err != nil || state.MutationHold || state.State != "prepared" {
				t.Fatalf("drift mutated state %+v %v", state, err)
			}
		})
	}
}

func TestPostgresHostingRuntimeEnvironmentSecretRevocationAndCompletion(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, afterApply := range []bool{false, true} {
			name := "image"
			if local {
				name = "local"
			}
			if afterApply {
				name += "-admitted"
			} else {
				name += "-fresh"
			}
			t.Run(name, func(t *testing.T) {
				f := newRuntimeAdmissionFixture(t, local)
				f.capabilities(t, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability)
				item, record, event := managedFixture(t, "runtime_admission_secret", 1)
				if _, err := f.pg.CreateManagedSecret(t.Context(), item, record, event); err != nil {
					t.Fatal(err)
				}
				in := runtimeInput(1, 0)
				in.Entries = []domain.HostingRuntimeEntry{{Name: "PASSWORD", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 1}}}
				runtimeSave(t, f.pg, f.resource, in)
				op, err := f.request(t, "secret-operation", "usr_bootstrap", 1)
				if err != nil {
					t.Fatal(err)
				}
				op, a, prepared := f.prepare(t, op)
				raw, _ := json.Marshal(op)
				if strings.Contains(string(raw), item.ID) || strings.Contains(string(raw), "PASSWORD") {
					t.Fatal("snapshot leaked runtime document")
				}
				if afterApply {
					op, err = f.pg.HostingApplyBegin(t.Context(), f.runner, a)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = f.pg.RevokeManagedSecret(t.Context(), item.ID, 1, destinationEvent("revoke-runtime-secret")); err != nil {
					t.Fatal(err)
				}
				if !afterApply {
					if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, a); !errors.Is(err, store.ErrConflict) {
						t.Fatalf("revoked secret admitted %v", err)
					}
					if _, err = f.pg.HostingPrepared(t.Context(), f.runner, prepared); err != nil {
						t.Fatal("prepared replay", err)
					}
					return
				}
				runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
				replay, err := f.pg.HostingApplyBegin(t.Context(), f.runner, a)
				if err != nil || replay.Plan.Hash != op.Plan.Hash || !replay.MutationHold {
					t.Fatalf("apply receipt replay %+v %v", replay, err)
				}
				wrong := a
				wrong.Fence = "changed"
				if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, wrong); !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("changed authority replay %v", err)
				}
				if _, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_remove(array_remove(capabilities,$2),$3) WHERE id=$1`, f.runner, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability); err != nil {
					t.Fatal(err)
				}
				complete := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "runtime-complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "metadata-runtime-container", ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, PlanHash: op.Plan.Hash}}
				settled, err := f.pg.HostingComplete(t.Context(), f.runner, complete)
				if err != nil || settled.State != "succeeded" || settled.MutationHold {
					t.Fatalf("historical completion %+v %v", settled, err)
				}
				if _, err = f.db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingComplete(t.Context(), f.runner, complete); err != nil {
					t.Fatal("historical receipt", err)
				}
			})
		}
	}
}

func TestPostgresHostingRuntimeEnvironmentQueuedDrift(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "image"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			f := newRuntimeAdmissionFixture(t, local)
			op, err := f.request(t, "queued-runtime-drift", "usr_bootstrap", 0)
			if err != nil {
				t.Fatal(err)
			}
			runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
			if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
				t.Fatal(err)
			}
			got, err := f.pg.GetHostingOperation(t.Context(), op.ID)
			if err != nil || got.State != "failed" || got.RunID != "" || got.MutationHold {
				t.Fatalf("queued config drift %+v %v", got, err)
			}
		})
	}
}

func TestPostgresHostingRuntimeEnvironmentLeaseGuard(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "image"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			f := newRuntimeAdmissionFixture(t, local)
			f.capabilities(t, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability)
			runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
			op, err := f.request(t, "runtime-lease-guard", "usr_bootstrap", 1)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
				t.Fatal(err)
			}
			op, err = f.pg.GetHostingOperation(t.Context(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, missing := range []string{domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability} {
				if _, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, missing); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("unsupported runner claimed %s: %v", missing, err)
				}
				// The database trigger independently rejects a raw candidate bypassing claim predicates.
				if _, err = f.db.Exec(t.Context(), `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES('raw-runtime-lease',$1,$2,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),1,'runtime-fence')`, op.RunID, f.runner); err == nil {
					t.Fatalf("raw lease omitted %s", missing)
				}
				f.capabilities(t, missing)
			}
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal("capable lease", err)
			}
			if claim.Run.ID != op.RunID {
				t.Fatal("substituted task")
			}
		})
	}
}

func TestPostgresHostingRuntimeEnvironmentUpgradeAndReferenceConstraints(t *testing.T) {
	f := newRuntimeAdmissionFixture(t, false)
	// Recreate the exact relevant 0052-era guard definitions without changing any
	// historical row or migration checksum, then apply the additive 0053 SQL.
	old, err := db.Files.ReadFile("migrations/0050_hosting_build_deployments.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldSQL := string(old)
	consistent := oldSQL[strings.Index(oldSQL, "CREATE FUNCTION nerocd_hosting_build_deployment_consistent"):strings.Index(oldSQL, "CREATE CONSTRAINT TRIGGER hosting_operation_build_consistency")]
	ready := oldSQL[strings.Index(oldSQL, "CREATE FUNCTION nerocd_hosting_build_deployment_schema_compatible"):strings.Index(oldSQL, "DO $migration$")]
	consistent = strings.Replace(consistent, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)
	ready = strings.Replace(ready, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)
	if _, err = f.db.Exec(t.Context(), `DROP TRIGGER hosting_runtime_environment_lease ON run_leases; DROP FUNCTION nerocd_hosting_runtime_environment_lease(),nerocd_hosting_runtime_environment_schema_compatible();`+consistent+ready); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(t.Context()); ok || err == nil {
		t.Fatalf("absent0053 readiness %v %v", ok, err)
	}
	if opened, err := store.OpenPostgres(t.Context(), f.db.Config().ConnConfig.ConnString()); err == nil {
		_ = opened.Close()
		t.Fatal("absent0053 startup")
	}
	oldOp, err := f.request(t, "old-runtime-upgrade", "usr_bootstrap", 0)
	if err != nil {
		t.Fatal("legacy admission", err)
	}
	var before []byte
	if err = f.db.QueryRow(t.Context(), `SELECT body FROM hosting_snapshots WHERE operation_id=$1`, oldOp.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(t.Context(), f.db, "0052_hosting_runtime_configuration.sql", "0053_hosting_runtime_environment_admission.sql"); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(t.Context()); !ok || err != nil {
		t.Fatalf("upgraded readiness %v %v", ok, err)
	}
	var after []byte
	if err = f.db.QueryRow(t.Context(), `SELECT body FROM hosting_snapshots WHERE operation_id=$1`, oldOp.ID).Scan(&after); err != nil || string(before) != string(after) {
		t.Fatalf("historical snapshot changed %v", err)
	}
	if _, err = f.db.Exec(t.Context(), `ALTER TABLE run_leases DISABLE TRIGGER hosting_runtime_environment_lease`); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(t.Context()); ok || err != nil {
		t.Fatalf("disabled runtime guard readiness %v %v", ok, err)
	}
	if _, err = f.db.Exec(t.Context(), `ALTER TABLE run_leases ENABLE TRIGGER hosting_runtime_environment_lease`); err != nil {
		t.Fatal(err)
	}
	runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
	for _, kind := range []string{"missing-reference", "zero-reference", "wrong-hash", "wrong-family", "old-family-with-reference", "extra-reference-field"} {
		t.Run(kind, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(before, &body); err != nil {
				t.Fatal(err)
			}
			body["policy_version"] = domain.HostingRuntimePolicyVersion
			ref := map[string]any{"generation": 1, "hash": "sha256:" + strings.Repeat("0", 64)}
			body["runtime_configuration"] = ref
			switch kind {
			case "missing-reference":
				delete(body, "runtime_configuration")
			case "zero-reference":
				ref["generation"] = 0
			case "wrong-family":
				body["policy_version"] = domain.HostingLocalBuildRuntimePolicyVersion
			case "old-family-with-reference":
				body["policy_version"] = domain.HostingReleasePolicyVersion
			case "extra-reference-field":
				ref["literal"] = "forbidden"
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			_, err = tx.Exec(t.Context(), `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT $1,project_id,resource_id,binding_id,$1,request_hash,'queued',created_by FROM hosting_operations WHERE id=$2`, kind, oldOp.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO hosting_snapshots(operation_id,body,body_hash) VALUES($1,$2,$3)`, kind, raw, domain.HostingDocumentHash(body))
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err == nil {
				t.Fatal("invalid raw snapshot committed")
			}
		})
	}
}

func TestPostgresHostingRuntimeEnvironmentReadPlanAndUncertainty(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "image"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			f := newRuntimeAdmissionFixture(t, local)
			f.capabilities(t, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability)
			runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
			op, err := f.request(t, "runtime-uncertainty", "usr_bootstrap", 1)
			if err != nil {
				t.Fatal(err)
			}
			op, a, _ := f.prepare(t, op)
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, a); err != nil {
				t.Fatal(err)
			}
			runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
			result := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "runtime-uncertain", Result: domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: op.Plan.Hash, DaemonID: op.Release.DaemonID}}
			uncertain, err := f.pg.HostingComplete(t.Context(), f.runner, result)
			if err != nil || !uncertain.MutationHold || uncertain.State != "reconciliation_required" {
				t.Fatalf("uncertainty lost %+v %v", uncertain, err)
			}
			if _, err = f.pg.RequestHostingReconciliation(t.Context(), op.ID, destinationEvent("runtime-reconcile")); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			observe := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			got, err := f.pg.HostingRunnerPlan(t.Context(), f.runner, observe)
			if err != nil || got.TaskMode != "observe" || !got.MutationHold {
				t.Fatalf("config drift blocked observation %+v %v", got, err)
			}
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, observe); err == nil {
				t.Fatal("observation acquired mutation permission")
			}
		})
	}
}

func TestPostgresHostingRuntimeEnvironmentFreshPreparationDrift(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, phase := range []string{"runner-plan", "prepared"} {
			name := "image-" + phase
			if local {
				name = "local-" + phase
			}
			t.Run(name, func(t *testing.T) {
				f := newRuntimeAdmissionFixture(t, local)
				f.capabilities(t, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability)
				runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
				op, err := f.request(t, "runtime-preparation-drift", "usr_bootstrap", 1)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
					t.Fatal(err)
				}
				claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
				if phase == "prepared" {
					if _, err = f.pg.HostingRunnerPlan(t.Context(), f.runner, a); err != nil {
						t.Fatal(err)
					}
				}
				runtimeSave(t, f.pg, f.resource, runtimeInput(2, 1))
				if phase == "runner-plan" {
					_, err = f.pg.HostingRunnerPlan(t.Context(), f.runner, a)
				} else {
					release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "daemon_image_host", OS: "linux", Architecture: "amd64"}
					release.Hash = domain.HostingDocumentHash(release)
					if local {
						release, err = domain.NewHostingLocalBuildRelease(*f.build.Artifact)
						if err != nil {
							t.Fatal(err)
						}
					}
					_, err = f.pg.HostingPrepared(t.Context(), f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "drift-prepared", Release: release})
				}
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("fresh %s ignored config drift: %v", phase, err)
				}
				got, err := f.pg.GetHostingOperation(t.Context(), op.ID)
				if err != nil || got.Plan != nil || got.Release != nil || got.MutationHold {
					t.Fatalf("rejected preparation persisted mutation %+v %v", got, err)
				}
			})
		}
	}
}

func TestPostgresHostingRuntimeEnvironmentCapabilityChangeDuringTargetWait(t *testing.T) {
	f := newRuntimeAdmissionFixture(t, false)
	f.capabilities(t, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability)
	runtimeSave(t, f.pg, f.resource, runtimeInput(1, 0))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port) SELECT id,destination_id,18080 FROM resource_bindings WHERE resource_id=$1`, f.resource); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT binding_id FROM hosting_binding_state FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		in := hostingDeploymentRequest()
		in.ExpectedRuntimeConfigurationGeneration = 1
		_, err := f.pg.RequestHostingDeployment(ctx, "capability-wait", f.resource, "usr_bootstrap", in, destinationEvent("capability-wait"))
		done <- err
	}()
	joined := false
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("request did not join")
			}
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	// Admission passed its initial capability read and is waiting for target
	// state, before the runner row lock. Revocation must win this window.
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingRuntimeEnvironmentCapability); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	joined = true
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("capability revoked while waiting admitted: %v", err)
	}
	runtimeCount(t, f.db, "hosting_operations", 0)
	runtimeCount(t, f.db, "hosting_snapshots", 0)
}
