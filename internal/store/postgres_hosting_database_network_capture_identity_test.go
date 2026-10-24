package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func networkCaptureIdentityFixture(t *testing.T, engine string, created time.Time) domain.HostingDatabaseNetworkCapturePlan {
	t.Helper()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal("identity recipe unavailable")
	}
	id, volume := domain.HostingStorageIdentity("binding_database")
	storage := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource_database", BindingID: "binding_database", DestinationID: "destination_database", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: recipe.MountPath, AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "owner", CreatedAt: created}
	storage.Hash = storage.DocumentHash()
	snapshot := domain.HostingSnapshot{ResourceID: storage.ResourceID, ResourceGeneration: 1, BindingID: storage.BindingID, DestinationID: storage.DestinationID, ServerID: "server_database", RunnerID: "runner_database", RuntimeNamespace: "ncd_" + strings.Repeat("a", 32), ImageReference: recipe.ImageReference, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion, Storage: storage.Reference(), Database: domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}}
	credentials := slices.Clone(recipe.Credentials)
	for i := range credentials {
		credentials[i].SecretID = domain.HostingDatabaseSecretID(snapshot.ResourceID, credentials[i].Role)
		credentials[i].Version = int64(i + 1)
	}
	snapshot.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(snapshot, credentials)
	if err != nil {
		t.Fatal("identity configuration invalid")
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "daemon_database"}
	release.Hash = domain.HostingDocumentHash(release)
	sp := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: volume, MountPath: recipe.MountPath, CreationOperationID: "hop_database_origin", DaemonID: release.DaemonID}
	sp.CreationNonce = domain.HostingStorageCreationNonce(sp.Reference, sp.CreationOperationID, release.DaemonID)
	plan, err := domain.CompileHostingDatabasePlan(snapshot, release, nil, sp, domain.HostingDatabasePlan{Reference: snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(snapshot.BindingID), Credentials: credentials})
	if err != nil {
		t.Fatal("identity source plan invalid")
	}
	healthy := domain.HostingHealthy{OperationID: sp.CreationOperationID, Snapshot: snapshot, Release: release, PlanHash: plan.Hash, ContainerID: strings.Repeat("a", 64), Storage: &domain.HostingHealthyStorage{Plan: sp, CreationPlanHash: plan.Hash}}
	receipt := domain.HostingReceipt{ID: "materialized", BodyHash: "sha256:" + strings.Repeat("b", 64), CreatedAt: created}
	m := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "materialized", CreationOperationID: sp.CreationOperationID, CreationPlanHash: plan.Hash, CreationNonce: sp.CreationNonce, DaemonID: release.DaemonID, MaterializedReceipt: &receipt}
	m.Labels = domain.ExpectedHostingStorageLabels(storage, m)
	p := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(healthy.OperationID), SourceOperationID: healthy.OperationID, ProjectID: storage.ProjectID, ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, ServerID: snapshot.ServerID, RunnerID: snapshot.RunnerID, SourcePlan: plan, SourceHealthy: healthy, Storage: storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: volume, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	if p.Validate() != nil {
		t.Fatal("identity capture plan invalid")
	}
	return p
}

func cloneNetworkCaptureIdentity(t *testing.T, p domain.HostingDatabaseNetworkCapturePlan) domain.HostingDatabaseNetworkCapturePlan {
	t.Helper()
	raw, err := json.Marshal(p)
	var copy domain.HostingDatabaseNetworkCapturePlan
	if err != nil || json.Unmarshal(raw, &copy) != nil {
		t.Fatal("identity JSON roundtrip failed")
	}
	return copy
}

// A subprocess initializes the real time.Local from TZ=UTC without racing
// other tests by replacing the process-global time.Local pointer.
func TestNetworkCaptureIdentityUTC(t *testing.T) {
	if os.Getenv("NEROCD_CAPTURE_IDENTITY_UTC_CHILD") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNetworkCaptureIdentityUTC$", "-test.v")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TZ=") && !strings.HasPrefix(value, "NEROCD_CAPTURE_IDENTITY_UTC_CHILD=") && !strings.HasPrefix(value, "NEROCD_TEST_DATABASE_URL=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TZ=UTC", "NEROCD_CAPTURE_IDENTITY_UTC_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("UTC identity regression failed: %s", output)
		}
		return
	}
	local := time.Unix(1792800000, 123456000)
	if local.Location() != time.Local || local.Location() == time.UTC {
		t.Fatal("UTC subprocess did not retain Local representation")
	}
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			p := networkCaptureIdentityFixture(t, engine, local)
			copy := cloneNetworkCaptureIdentity(t, p)
			if !p.Storage.CreatedAt.Equal(copy.Storage.CreatedAt) || copy.Storage.CreatedAt.Location() != time.UTC || reflect.DeepEqual(p, copy) || p.Hash != copy.Hash {
				t.Fatal("fixture did not reproduce equal instant with distinct Go locations")
			}
			if !networkCapturePlanEqual(p, copy) {
				t.Error("equivalent full capture document rejected after UTC JSON roundtrip")
			}
			if !networkCaptureDocumentEqual(p.Storage, copy.Storage) {
				t.Error("equivalent historical storage rejected after UTC JSON roundtrip")
			}
			if !networkCaptureDocumentEqual(p.Materialization, copy.Materialization) {
				t.Error("equivalent historical materialization rejected after UTC JSON roundtrip")
			}
			for name, change := range map[string]func(*domain.HostingDatabaseNetworkCapturePlan){
				"storage_time": func(p *domain.HostingDatabaseNetworkCapturePlan) {
					p.Storage.CreatedAt = p.Storage.CreatedAt.Add(time.Microsecond)
				},
				"receipt_time": func(p *domain.HostingDatabaseNetworkCapturePlan) {
					p.Creation.Receipt.CreatedAt = p.Creation.Receipt.CreatedAt.Add(time.Microsecond)
					p.Materialization.MaterializedReceipt.CreatedAt = p.Creation.Receipt.CreatedAt
				},
				"receipt_id": func(p *domain.HostingDatabaseNetworkCapturePlan) {
					p.Creation.Receipt.ID = "different_receipt"
					p.Materialization.MaterializedReceipt.ID = p.Creation.Receipt.ID
				},
				"owner":            func(p *domain.HostingDatabaseNetworkCapturePlan) { p.Storage.CreatedBy = "different_owner" },
				"target":           func(p *domain.HostingDatabaseNetworkCapturePlan) { p.RunnerID = "different_runner" },
				"generation":       func(p *domain.HostingDatabaseNetworkCapturePlan) { p.SourcePlan.Snapshot.ResourceGeneration++ },
				"storage_identity": func(p *domain.HostingDatabaseNetworkCapturePlan) { p.Storage.ID = "different_storage" },
				"credential":       func(p *domain.HostingDatabaseNetworkCapturePlan) { p.SourcePlan.Database.Credentials[0].Version++ },
				"nonce": func(p *domain.HostingDatabaseNetworkCapturePlan) {
					p.Materialization.CreationNonce = strings.Repeat("f", 32)
				},
			} {
				changed := cloneNetworkCaptureIdentity(t, p)
				change(&changed)
				changed.Hash = changed.DocumentHash()
				if networkCapturePlanEqual(p, changed) {
					t.Errorf("changed full identity accepted: %s", name)
				}
				if name == "storage_time" && networkCaptureDocumentEqual(p.Storage, changed.Storage) {
					t.Error("changed storage time accepted")
				}
				if (name == "receipt_time" || name == "receipt_id") && networkCaptureDocumentEqual(p.Materialization, changed.Materialization) {
					t.Error("changed materialization receipt accepted")
				}
			}
		})
	}
}
