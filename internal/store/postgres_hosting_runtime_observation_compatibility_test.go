package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func settledStorageObservationFixture(t *testing.T) (storageMaterializationFixture, observationFixture) {
	t.Helper()
	f := newStorageMaterializationFixture(t)
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	f.complete(t, "succeeded", false)
	observer := observationFixture{pg: f.pg, db: f.db, resource: f.resource, server: domain.Server{RunnerID: f.runner}}
	// Reuse the enrollment credential from the hosting execution fixture.
	if err = f.db.QueryRow(t.Context(), `SELECT token_hash FROM runners WHERE id=$1`, f.runner).Scan(&observer.token); err != nil {
		t.Fatal(err)
	}
	observer.exec(t, `UPDATE runners SET capabilities=capabilities || $2::text[] WHERE id=$1`, f.runner, []string{domain.HostingObservationCapability, domain.HostingStorageObservationCapability})
	return f, observer
}

func addStatelessObservationPlacement(t *testing.T, f observationFixture, destination, suffix string, port int) string {
	t.Helper()
	resource := createDestinationResource(t, f.pg, "observation_"+suffix, f.resource.ProjectID)
	binding := "binding_" + suffix
	_, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: binding, ResourceID: resource.ID, DestinationID: destination, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("observation_bind_"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	in := hostingDeploymentRequest()
	in.IdempotencyKey, in.HostPort = "observation_"+suffix, port
	op, err := f.pg.RequestHostingDeployment(t.Context(), "hop_observation_"+suffix, resource.ID, "usr_bootstrap", in, destinationEvent("observation_deploy_"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	authority, prepared := prepareHostingAttempt(t, f.pg, op, f.server.RunnerID)
	admitted, err := f.pg.HostingApplyBegin(t.Context(), f.server.RunnerID, authority)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pg.HostingComplete(t.Context(), f.server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: authority, ReceiptID: "observation_complete_" + suffix, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "container_" + suffix, ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestPostgresHostingObservationLegacyMixedPageCompatibility(t *testing.T) {
	mounted, f := settledStorageObservationFixture(t)
	// Cache a storage-bearing challenge before downgrading the runner.
	old := f.target(t)
	if old.Storage == nil {
		t.Fatal("storage fixture did not issue mounted challenge")
	}
	first := addStatelessObservationPlacement(t, f, mounted.op.Snapshot.DestinationID, "a_stateless", 18101)
	last := addStatelessObservationPlacement(t, f, mounted.op.Snapshot.DestinationID, "z_stateless", 18102)
	f.exec(t, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingStorageObservationCapability)

	page, err := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err = decodeLegacyObservationPage(raw); err != nil {
		t.Fatalf("legacy runner rejected controller response: %s: %v", raw, err)
	}
	if len(page.Targets) != 1 || page.Targets[0].BindingID != first || page.Targets[0].Storage != nil || page.NextCursor != old.BindingID || page.EndOfSweep {
		t.Fatalf("mixed page did not cross skipped storage: %+v", page)
	}
	if _, err = f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(page.Targets[0])); err != nil {
		t.Fatal("stateless report after downgrade", err)
	}

	// A page containing only the skipped storage candidate must advance too.
	skipped, err := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{AfterBindingID: first, Limit: 1})
	if err != nil || len(skipped.Targets) != 0 || skipped.NextCursor != old.BindingID || skipped.EndOfSweep {
		t.Fatalf("skipped-only page: %+v %v", skipped, err)
	}
	raw, err = json.Marshal(skipped)
	if err != nil {
		t.Fatal(err)
	}
	if err = decodeLegacyObservationPage(raw); err != nil {
		t.Fatal("legacy runner rejected empty advancing page", err)
	}
	next, err := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{AfterBindingID: page.NextCursor, Limit: 2})
	if err != nil || len(next.Targets) != 1 || next.Targets[0].BindingID != last || !next.EndOfSweep {
		t.Fatalf("later stateless target stalled: %+v %v", next, err)
	}
	raw, err = json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if err = decodeLegacyObservationPage(raw); err != nil {
		t.Fatal("legacy runner rejected later page", err)
	}
	if _, err = f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, observationBody(old)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("cached mounted challenge admitted after downgrade", err)
	}
	// Prove the frozen decoder detects the original incompatibility.
	raw, err = json.Marshal(domain.HostingObservationTargets{Targets: []domain.HostingObservationTarget{page.Targets[0], old}, NextCursor: old.BindingID, EndOfSweep: false})
	if err != nil {
		t.Fatal(err)
	}
	if decodeLegacyObservationPage(raw) == nil {
		t.Fatal("legacy decoder accepted added storage field")
	}
}

func TestPostgresHostingObservationStorageCapabilityDowngrade(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "pending"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			mounted, f := settledStorageObservationFixture(t)
			target := f.target(t)
			want := domain.HostingObservationStorage{VolumeName: mounted.storage.VolumeName, MountPath: mounted.storage.MountPath}
			if target.Storage == nil || *target.Storage != want {
				t.Fatalf("incorrect mount authority: %+v", target)
			}
			if repeated := f.target(t); !reflect.DeepEqual(target, repeated) {
				t.Fatal("capable runner could not reuse exact challenge")
			}
			body := observationBody(target)
			if accepted {
				if _, err := f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); err != nil {
					t.Fatal(err)
				}
				if f.status(t).Freshness != "fresh" {
					t.Fatal("capable mounted evidence not fresh")
				}
			}
			f.exec(t, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingStorageObservationCapability)
			page, err := f.pg.HostingObservationTargets(t.Context(), f.server.RunnerID, f.token, domain.HostingObservationTargetsInput{Limit: 4})
			if err != nil || len(page.Targets) != 0 || page.NextCursor != target.BindingID {
				t.Fatalf("downgraded page: %+v %v", page, err)
			}
			if _, err = f.pg.ReportHostingObservation(t.Context(), f.server.RunnerID, f.token, body); !errors.Is(err, store.ErrConflict) {
				t.Fatal("downgraded mounted report admitted", err)
			}
			status := f.status(t)
			if status.Freshness != "blocked" || status.ContainerState != "unknown" || (status.LastObservation != nil) != accepted {
				t.Fatalf("downgraded status: %+v", status)
			}
			// Logs retain their independent capability, even on this mounted placement.
			f.exec(t, `UPDATE runners SET capabilities=array_append(array_remove(capabilities,$2),$3) WHERE id=$1`, f.server.RunnerID, domain.HostingObservationCapability, domain.HostingRuntimeLogsCapability)
			session, err := f.pg.OpenRuntimeLogSession(t.Context(), "usr_bootstrap", f.resource.ID, runtimeLogOpenInput(1))
			if err != nil {
				t.Fatal("mounted logs required observation capability", err)
			}
			claim, err := f.pg.ClaimRuntimeLogSessions(t.Context(), f.server.RunnerID, f.token, domain.HostingRuntimeLogClaimInput{InstanceID: "00000000000000000000000000000001", Limit: 4})
			if err != nil || len(claim.Targets) != 1 || claim.Targets[0].SessionID != session.ID {
				t.Fatalf("mounted log authority changed: %+v %v", claim, err)
			}
		})
	}
}

type legacyObservationTarget struct {
	SampleID             string    `json:"sample_id"`
	IssuedAt             time.Time `json:"issued_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	ResourceID           string    `json:"resource_id"`
	BindingID            string    `json:"binding_id"`
	DestinationID        string    `json:"destination_id"`
	ServerID             string    `json:"server_id"`
	RunnerID             string    `json:"runner_id"`
	OperationID          string    `json:"operation_id"`
	DaemonID             string    `json:"daemon_id"`
	ContainerID          string    `json:"container_id"`
	ImageID              string    `json:"image_id"`
	ReleaseHash          string    `json:"release_hash"`
	PlanHash             string    `json:"plan_hash"`
	RuntimeNamespace     string    `json:"runtime_namespace"`
	ContainerPort        int       `json:"container_port"`
	HostPort             int       `json:"host_port"`
	ReadinessPath        string    `json:"readiness_path"`
	SampleTimeoutSeconds int       `json:"sample_timeout_seconds"`
}

// decodeLegacyObservationPage freezes the closed v1 target shape and decoding
// behavior from f4844b6e599aada47516d751dde1ed37282d5d94. It deliberately does
// not use the current target type or response decoder, which understand storage.
func decodeLegacyObservationPage(raw []byte) error {
	if len(raw) > 512<<10 || !legacyObservationUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return errors.New("invalid legacy response")
	}
	if !legacyObservationObject(raw, "targets", "next_cursor", "end_of_sweep") {
		return errors.New("invalid legacy page")
	}
	var page struct {
		Targets    []legacyObservationTarget `json:"targets"`
		NextCursor string                    `json:"next_cursor"`
		EndOfSweep bool                      `json:"end_of_sweep"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("legacy response requires one JSON document")
	}
	var object struct {
		Targets []json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	for _, target := range object.Targets {
		if !legacyObservationObject(target, "sample_id", "issued_at", "expires_at", "resource_id", "binding_id", "destination_id", "server_id", "runner_id", "operation_id", "daemon_id", "container_id", "image_id", "release_hash", "plan_hash", "runtime_namespace", "container_port", "host_port", "readiness_path", "sample_timeout_seconds") {
			return errors.New("invalid legacy target")
		}
	}
	return nil
}

func legacyObservationObject(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	return true
}

func legacyObservationUniqueJSON(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !legacyObservationUniqueJSON(d) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !legacyObservationUniqueJSON(d) {
				return false
			}
		}
	default:
		return false
	}
	_, err = d.Token()
	return err == nil
}
