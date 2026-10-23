package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"nerocd/internal/store"
)

type pushStoreFixture struct {
	buildStoreFixture
	policy domain.GitHubPushPolicy
}

func pushUUID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012x", n) }
func newPushStoreFixture(t *testing.T) pushStoreFixture {
	t.Helper()
	f := newBuildStoreFixture(t)
	if err := f.pg.ConfigureGitHubPushProcessor(func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	stop := startBuildController(t, f)
	stopCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	triggerCapability(t, f, true)
	src := f.src
	src.Generation = 2
	src.RepositoryURL = "https://github.com/acme/push-fixture.git"
	src.Policy.AllowedHosts = []string{"github.com"}
	changed, err := configureBuildFixtureSource(f.pg, t.Context(), "usr_bootstrap", 1, src, destinationEvent("push_source"))
	if err != nil {
		t.Fatal(err)
	}
	f.src = changed
	if _, err = f.db.Exec(t.Context(), `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('push_bot','proj_platform','usr_bootstrap','push bot','maintainer','active')`); err != nil {
		t.Fatal(err)
	}
	item, record, audit := managedFixture(t, "push_signing", 1)
	if _, err = f.pg.CreateManagedSecret(t.Context(), item, record, audit); err != nil {
		t.Fatal(err)
	}
	in := domain.GitHubPushConfigure{ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, RequestID: pushUUID(1), ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: "push_bot", WebhookSecret: domain.GitHubPushSecretRef{ID: item.ID, Version: 1}}}
	p, err := f.pg.ConfigureGitHubPushPolicy(t.Context(), "usr_bootstrap", src.ResourceID, in, destinationEvent("push_configure"))
	if err != nil {
		t.Fatal(err)
	}
	return pushStoreFixture{f, p}
}
func pushEvidence(p domain.GitHubPushPolicy, head string) githubpush.Evidence {
	e := githubpush.Evidence{RepositoryID: 42, Repository: p.Repository, Branch: p.Branch, Head: head}
	if p.DesiredCommit == "" {
		e.Status = "initial"
	} else {
		e.Base = p.DesiredCommit
		e.MergeBase = p.DesiredCommit
		e.Status = "identical"
		if head != p.DesiredCommit {
			e.Status = "ahead"
			e.AheadBy = 1
		}
	}
	return e
}
func (f *pushStoreFixture) enable(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	a, err := f.pg.BeginGitHubPushVerification(ctx, "usr_bootstrap", f.src.ResourceID, domain.GitHubPushCommand{ExpectedPolicyGeneration: f.policy.Generation, RequestID: pushUUID(2)})
	if err != nil {
		t.Fatal(err)
	}
	f.policy, err = f.pg.FinishGitHubPushVerification(ctx, a, pushEvidence(a.Policy, strings.Repeat("a", 40)), "")
	if err != nil {
		t.Fatal(err)
	}
	f.policy, err = f.pg.SetGitHubPushEnabled(ctx, "usr_bootstrap", f.src.ResourceID, domain.GitHubPushEnable{ExpectedPolicyGeneration: f.policy.Generation, RequestID: pushUUID(3), Enabled: true}, destinationEvent("push_enable"))
	if err != nil {
		t.Fatal(err)
	}
}
func (f pushStoreFixture) accept(t *testing.T, n int, commit string) store.GitHubPushAuthorization {
	t.Helper()
	ctx := t.Context()
	a, err := f.pg.GitHubPushWebhookAuthority(ctx, f.policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(n), "sha256:"+strings.Repeat(fmt.Sprintf("%x", n%16), 64), commit, false)
	if err != nil || receipt.State != "accepted" {
		t.Fatal(receipt, err)
	}
	var id string
	if err = f.db.QueryRow(ctx, `SELECT id FROM hosting_github_push_deliveries WHERE delivery_id=$1`, pushUUID(n)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	out, err := f.pg.BeginGitHubPushDelivery(ctx, id)
	if err != nil || out.IntentID == "" {
		t.Fatal("claim", err)
	}
	return out
}
func TestPostgresGitHubPushAtomicPinnedAdmissionAndDelayedDelivery(t *testing.T) {
	f := newPushStoreFixture(t)
	f.enable(t)
	ctx := t.Context()
	commit := strings.Repeat("b", 40)
	a := f.accept(t, 10, commit)
	if err := f.pg.CommitGitHubPushDelivery(ctx, a, pushEvidence(a.Policy, commit), ""); err != nil {
		t.Fatal(err)
	}
	p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	if err != nil || p.DesiredCommit != commit || p.LastBuildID == "" {
		t.Fatal(p, err)
	}
	b, err := f.pg.GetHostingBuild(ctx, "usr_bootstrap", p.LastBuildID)
	if err != nil || b.CreatedBy != "push_bot" || b.Snapshot.TriggerRevision == nil || b.Snapshot.TriggerRevision.ExpectedCommit != commit || b.Snapshot.Source.RequestedRef != "main" {
		t.Fatal("pinned admission", err)
	}
	var jobs int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='hosting.prepare_build.v1' AND args->>'build_id'=$1`, b.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("atomic River enqueue", jobs, err)
	}
	delayed := f.accept(t, 11, strings.Repeat("a", 40))
	if err = f.pg.CommitGitHubPushDelivery(ctx, delayed, pushEvidence(delayed.Policy, commit), ""); err != nil {
		t.Fatal(err)
	}
	duplicate := f.accept(t, 12, commit)
	if err = f.pg.CommitGitHubPushDelivery(ctx, duplicate, pushEvidence(duplicate.Policy, commit), ""); err != nil {
		t.Fatal(err)
	}
	page, err := f.pg.ListGitHubPushDeliveries(ctx, "usr_bootstrap", f.src.ResourceID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]domain.GitHubPushDelivery{}
	for _, row := range page.Items {
		states[row.ID] = row
	}
	if d := states[pushUUID(11)]; d.State != "ignored" || d.Reason != "obsolete" || d.AfterCommit != strings.Repeat("a", 40) || d.VerifiedCommit != commit || d.BuildID != "" {
		t.Fatal("delayed event substituted", d)
	}
	if d := states[pushUUID(12)]; d.State != "coalesced" || d.BuildID != b.ID {
		t.Fatal("duplicate admission", d)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// Physical terminal receipt cleanup cannot remove the stable commit identity.
	if _, err = f.db.Exec(ctx, `DELETE FROM hosting_github_push_deliveries`); err != nil {
		t.Fatal(err)
	}
	retained := f.accept(t, 13, commit)
	if err = f.pg.CommitGitHubPushDelivery(ctx, retained, pushEvidence(retained.Policy, commit), ""); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 1 {
		t.Fatal("dedup lost after receipt cleanup", count, err)
	}
}
func TestPostgresGitHubPushConcurrentDedupAndChangedBody(t *testing.T) {
	f := newPushStoreFixture(t)
	f.enable(t)
	ctx := t.Context()
	a, err := f.pg.GitHubPushWebhookAuthority(ctx, f.policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	const hash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, e := f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(20), hash, strings.Repeat("b", 40), false)
			results <- e
		})
	}
	wg.Wait()
	close(results)
	for err = range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(20), "sha256:"+strings.Repeat("b", 64), strings.Repeat("b", 40), false); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed duplicate", err)
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_github_push_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestPostgresGitHubPushCurrentAuthorityAndAttemptExpiry(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"actor", `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='push_bot'`},
		{"owner", `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`},
		{"project", `UPDATE projects SET archived_at=clock_timestamp() WHERE id='proj_platform'`},
		{"secret", `UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id='push_signing'`},
		{"destination", `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id='dest_build'`},
		{"capability", `UPDATE runners SET capabilities=ARRAY['hosting_build_v1']::text[] WHERE id='runner_build'`},
		{"enrollment", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id='runner_build'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPushStoreFixture(t)
			f.enable(t)
			a := f.accept(t, 30, strings.Repeat("b", 40))
			if _, err := f.db.Exec(t.Context(), test.sql); err != nil {
				t.Fatal(err)
			}
			if err := f.pg.CommitGitHubPushDelivery(t.Context(), a, pushEvidence(a.Policy, strings.Repeat("b", 40)), ""); err == nil {
				t.Fatal("revoked authority admitted")
			}
			var count int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
	t.Run("expired token", func(t *testing.T) {
		f := newPushStoreFixture(t)
		f.enable(t)
		a := f.accept(t, 31, strings.Repeat("b", 40))
		if _, err := f.db.Exec(t.Context(), `UPDATE hosting_github_push_deliveries SET token_expires_at=clock_timestamp()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if err := f.pg.CommitGitHubPushDelivery(t.Context(), a, pushEvidence(a.Policy, strings.Repeat("b", 40)), ""); !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
	})
}
func TestPostgresGitHubPushSchemaCompatibilityRejectsMalformedObjects(t *testing.T) {
	for _, sql := range []string{
		`ALTER TABLE hosting_github_push_deliveries DROP COLUMN token_expires_at`,
		`ALTER TABLE hosting_github_push_commit_intents DROP CONSTRAINT hosting_github_push_commit_intents_pkey`,
		`ALTER TABLE hosting_github_push_commands DROP CONSTRAINT hosting_github_push_commands_kind_check`,
	} {
		t.Run(sql, func(t *testing.T) {
			f := newBuildStoreFixture(t)
			if _, err := f.db.Exec(t.Context(), sql); err != nil {
				t.Fatal(err)
			}
			var valid bool
			if err := f.db.QueryRow(t.Context(), `SELECT nerocd_hosting_github_push_schema_compatible()`).Scan(&valid); err != nil || valid {
				t.Fatal("malformed schema accepted", valid, err)
			}
		})
	}
}
func TestPostgresGitHubPushProviderFailureDoesNotAdvanceDesired(t *testing.T) {
	f := newPushStoreFixture(t)
	f.enable(t)
	a := f.accept(t, 40, strings.Repeat("b", 40))
	if err := f.pg.CommitGitHubPushDelivery(t.Context(), a, githubpush.Evidence{}, "rate_limited"); err != nil {
		t.Fatal(err)
	}
	p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID)
	if err != nil || p.DesiredCommit != strings.Repeat("a", 40) || p.Status != "blocked" || p.Reason != "rate_limited" {
		t.Fatal(p, err)
	}
	page, err := f.pg.ListGitHubPushDeliveries(t.Context(), "usr_bootstrap", f.src.ResourceID, "", 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "retry_wait" || page.Items[0].Attempts != 1 || page.Items[0].NextRetryAt == nil {
		t.Fatal(page, err)
	}
	retry := domain.GitHubPushRetry{ExpectedPolicyGeneration: p.Generation, RequestID: pushUUID(41), DeliveryID: pushUUID(40)}
	first, err := f.pg.RetryGitHubPushDelivery(t.Context(), "usr_bootstrap", f.src.ResourceID, retry, destinationEvent("retry"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.RetryGitHubPushDelivery(t.Context(), "usr_bootstrap", f.src.ResourceID, retry, destinationEvent("retry_again"))
	if err != nil || !first.ExpiresAt.Equal(replay.ExpiresAt) {
		t.Fatal("retry extended window", err)
	}
}

func TestPostgresGitHubPushDeadlineAfterResourceAndBuildWriteWait(t *testing.T) {
	for _, stage := range []string{"resource", "build_write"} {
		t.Run(stage, func(t *testing.T) {
			f := newPushStoreFixture(t)
			f.enable(t)
			a := f.accept(t, 50, strings.Repeat("b", 40))
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			if err := f.db.QueryRow(ctx, `UPDATE hosting_github_push_deliveries SET token_expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING token_expires_at`, a.IntentID).Scan(&a.Until); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if stage == "resource" {
				_, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.src.ResourceID)
			} else {
				if _, err = f.db.Exec(ctx, `CREATE FUNCTION push_fixture_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(9630056); RETURN NEW; END $$; CREATE TRIGGER push_fixture_wait BEFORE INSERT ON hosting_builds FOR EACH ROW EXECUTE FUNCTION push_fixture_wait()`); err != nil {
					t.Fatal(err)
				}
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(9630056)`)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- f.pg.CommitGitHubPushDelivery(ctx, a, pushEvidence(a.Policy, strings.Repeat("b", 40)), "")
			}()
			until := time.Now().Add(5 * time.Second)
			waiting := false
			for !waiting && time.Now().Before(until) {
				err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND pid<>pg_backend_pid() AND query LIKE $1)`, map[string]string{"resource": "%LockHosting%Resource%", "build_write": "%InsertHostingBuild%"}[stage]).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if !waiting {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if !waiting {
				t.Fatal("operation did not reach blocking boundary")
			}
			expired := false
			for !expired && time.Now().Before(until) {
				if err = f.db.QueryRow(ctx, `SELECT token_expires_at<=clock_timestamp() FROM hosting_github_push_deliveries WHERE id=$1`, a.IntentID).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if !expired {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if !expired {
				t.Fatal("DB token never expired")
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if !errors.Is(err, store.ErrConflict) {
					t.Fatal("expired attempt committed", err)
				}
			case <-ctx.Done():
				t.Fatal("blocked operation did not join")
			}
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_builds`).Scan(&count); err != nil || count != 0 {
				t.Fatal("build write survived expired transaction", count, err)
			}
			p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
			if err != nil || p.DesiredCommit != strings.Repeat("a", 40) {
				t.Fatal("desired advanced after expiry", err)
			}
		})
	}
}
func TestPostgresGitHubPushBoundedPendingAndExhaustion(t *testing.T) {
	f := newPushStoreFixture(t)
	f.enable(t)
	ctx := t.Context()
	a, err := f.pg.GitHubPushWebhookAuthority(ctx, f.policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	for n := 100; n < 116; n++ {
		if _, err = f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(n), "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 40), false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(116), "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 40), false); !errors.Is(err, store.ErrGitHubPushCapacity) {
		t.Fatal("resource pending quota", err)
	}
	var id string
	if err = f.db.QueryRow(ctx, `UPDATE hosting_github_push_deliveries SET attempts=32 WHERE delivery_id=$1 RETURNING id`, pushUUID(100)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	exhausted, err := f.pg.BeginGitHubPushDelivery(ctx, id)
	if err != nil || exhausted.IntentID != "" {
		t.Fatal(err)
	}
	var state string
	if err = f.db.QueryRow(ctx, `SELECT state FROM hosting_github_push_deliveries WHERE id=$1`, id).Scan(&state); err != nil || state != "blocked_exhausted" {
		t.Fatal(state, err)
	}
	if _, err = f.pg.AcceptGitHubPushDelivery(ctx, a, pushUUID(116), "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 40), false); err != nil {
		t.Fatal("exhaustion did not release pending capacity", err)
	}
}
func TestPostgresGitHubPushConcurrentPolicyChangeRejectsOldProviderProof(t *testing.T) {
	f := newPushStoreFixture(t)
	f.enable(t)
	ctx := t.Context()
	a := f.accept(t, 60, strings.Repeat("b", 40))
	cfg := *f.policy.OwnerConfiguration
	in := domain.GitHubPushConfigure{ExpectedPolicyGeneration: f.policy.Generation, ExpectedResourceGeneration: 2, ExpectedSourceGeneration: 2, RequestID: pushUUID(61), ResetBranchBaseline: true, Configuration: cfg}
	changed, err := f.pg.ConfigureGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID, in, destinationEvent("reconfigure"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.CommitGitHubPushDelivery(ctx, a, pushEvidence(a.Policy, strings.Repeat("b", 40)), ""); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old generation proof committed", err)
	}
	if changed.Enabled || changed.DesiredCommit != "" || changed.Reason != "baseline_reset" {
		t.Fatal("reset did not require new baseline")
	}
}
