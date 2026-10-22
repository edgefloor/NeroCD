package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// Synthetic fixtures explicitly opt into private transport without claiming that
// the current shipped runtime implements or advertises this capability.
func setPrivateGitCapability(t *testing.T, f buildStoreFixture, enabled bool) {
	t.Helper()
	query := `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`
	if enabled {
		query = `UPDATE runners SET capabilities=array_append(array_remove(capabilities,$2),$2) WHERE id=$1`
	}
	if _, err := f.db.Exec(t.Context(), query, f.runner, domain.HostingPrivateGitCapability); err != nil {
		t.Fatal(err)
	}
}

func privateCapabilityInput(id string) domain.HostingBuildInput {
	return domain.HostingBuildInput{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: id, GrantID: "delivery_grant"}
}

func TestPostgresPrivateGitCapabilityAdmissionAndConfiguredReplay(t *testing.T) {
	f, _, _ := privateDeliveryFixture(t)
	grant, err := f.pg.RequestHostingSourceGrant(t.Context(), "usr_bootstrap", f.src.ResourceID, domain.HostingSourceGrantRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, SecretID: "delivery_secret", SecretVersion: 1, RequestID: strings.Repeat("c", 32)}, destinationEvent("capability_grant"))
	if err != nil {
		t.Fatal(err)
	}
	startBuildController(t, f)
	in := domain.HostingBuildRequest{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, IdempotencyKey: "capability-admission", GrantID: grant.ID}
	setPrivateGitCapability(t, f, false)
	if _, err = f.pg.RequestConfiguredHostingBuild(t.Context(), "private-capability", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("capability_missing")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("public runner admitted private build", err)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_builds WHERE id='private-capability'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unsupported private admission persisted", err, count)
	}
	setPrivateGitCapability(t, f, true)
	_, err = f.pg.RequestConfiguredHostingBuild(t.Context(), "private-capability", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("capability_present"))
	if err != nil {
		t.Fatal(err)
	}
	setPrivateGitCapability(t, f, false)
	if _, err = f.pg.RequestConfiguredHostingBuild(t.Context(), "private-capability-replay", f.src.ResourceID, "usr_bootstrap", in, destinationEvent("capability_removed_replay")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("private replay ignored current capability", err)
	}
}

func TestPostgresPrivateGitCapabilityDispatchAndClaim(t *testing.T) {
	for _, phase := range []string{"dispatch", "claim"} {
		t.Run(phase, func(t *testing.T) {
			f, _, _ := privateDeliveryFixture(t)
			id := "capability-" + phase
			if _, err := f.pg.RequestHostingBuild(t.Context(), id, f.src.ResourceID, "usr_bootstrap", privateCapabilityInput(id), buildPolicy(), buildEnqueue, destinationEvent(id)); err != nil {
				t.Fatal(err)
			}
			if phase == "dispatch" {
				setPrivateGitCapability(t, f, false)
				if _, err := f.pg.DispatchHostingBuild(t.Context(), id, buildDispatch, destinationEvent("deny_"+id)); !errors.Is(err, store.ErrConflict) {
					t.Fatal("public runner dispatched private build", err)
				}
				var count int
				if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM task_runs WHERE id=$1`, "build_run_"+id).Scan(&count); err != nil || count != 0 {
					t.Fatal("denied dispatch made task", err, count)
				}
				return
			}
			b, err := f.pg.DispatchHostingBuild(t.Context(), id, buildDispatch, destinationEvent("dispatch_"+id))
			if err != nil {
				t.Fatal(err)
			}
			setPrivateGitCapability(t, f, false)
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err == nil && claim.Run.ID == b.RunID {
				t.Fatal("public-only runner claimed private task")
			}
			var count int
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM run_leases WHERE run_id=$1`, b.RunID).Scan(&count); err != nil || count != 0 {
				t.Fatal("unsupported claim retained lease", err, count)
			}
		})
	}
}

func TestPostgresPrivateGitCapabilityDeliveryAndHistoricalReceipt(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery_replay", true: "historical_receipt"}[historical], func(t *testing.T) {
			f, b, a := privateDeliveryFixture(t)
			in := deliveryRequest(a)
			if historical {
				if _, err := f.pg.CommitHostingBuildSource(t.Context(), f.runner, a, "capability-source", sourceBuildReceipt(b)); err != nil {
					t.Fatal(err)
				}
			} else {
				allowed, err := f.pg.AuthorizeHostingSourceCredential(t.Context(), f.runner, in)
				if err != nil {
					t.Fatal(err)
				}
				clear(allowed.Record.Envelope.Data)
			}
			setPrivateGitCapability(t, f, false)
			if historical {
				status, err := f.pg.HostingBuildStatus(t.Context(), f.runner, a)
				if err != nil || status.MayExecute || len(status.Receipts) != 1 {
					t.Fatal("historical receipt confused with execution authority", err)
				}
			}
			denied, err := f.pg.AuthorizeHostingSourceCredential(t.Context(), f.runner, in)
			assertNoDelivery(t, denied, err)
			in.DeliveryID = strings.Repeat("e", 32)
			denied, err = f.pg.AuthorizeHostingSourceCredential(t.Context(), f.runner, in)
			assertNoDelivery(t, denied, err)
		})
	}
}
