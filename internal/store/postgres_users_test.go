package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func offboardingRequest(userID, suffix string, now time.Time) store.UserOffboardingRequest {
	return store.UserOffboardingRequest{
		UserID: userID, RevokedAt: now,
		Audit:       domain.AuditEvent{ID: "audit_offboard_" + suffix, ActorID: "usr_bootstrap", Action: "identity.user.offboard", TargetID: userID, CreatedAt: now},
		DeniedAudit: domain.AuditEvent{ID: "audit_offboard_denied_" + suffix, ActorID: "usr_bootstrap", Action: "identity.user.offboard.denied", TargetID: userID, CreatedAt: now},
	}
}

func TestPostgresOffboardUserIsAtomicAndPreservesHistory(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "user_offboard_atomic")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	if _, err := database.Exec(ctx, `INSERT INTO oidc_external_identities (issuer, subject, user_id) VALUES ('https://issuer.example', 'viewer-subject', 'usr_viewer')`); err != nil {
		t.Fatal(err)
	}
	if err := pg.CreateSession(ctx, domain.Session{ID: "session_offboard", UserID: "usr_viewer", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "session-offboard-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateAPIToken(ctx, domain.APIToken{ID: "token_offboard", Name: "viewer automation", Kind: domain.TokenKindServiceAccount, TokenHash: "token-offboard-hash", Roles: []string{domain.RoleViewer}, Status: domain.TokenActive, CreatedBy: "usr_viewer", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := pg.CreateAuditEvent(ctx, domain.AuditEvent{ID: "duplicate_offboard_audit", ActorID: "usr_bootstrap", Action: "test", TargetID: "test", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	failed := offboardingRequest("usr_viewer", "rollback", now)
	failed.Audit.ID = "duplicate_offboard_audit"
	if _, err := pg.OffboardUser(ctx, failed); err == nil {
		t.Fatal("offboarding with duplicate audit unexpectedly succeeded")
	}
	var status string
	var activeSessions, activeTokens int
	if err := database.QueryRow(ctx, `SELECT status FROM users WHERE id='usr_viewer'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id='session_offboard' AND revoked_at IS NULL`).Scan(&activeSessions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE id='token_offboard' AND status='active' AND revoked_at IS NULL`).Scan(&activeTokens); err != nil {
		t.Fatal(err)
	}
	if status != domain.UserActive || activeSessions != 1 || activeTokens != 1 {
		t.Fatalf("failed offboarding leaked mutation: status=%s sessions=%d tokens=%d", status, activeSessions, activeTokens)
	}

	result, err := pg.OffboardUser(ctx, offboardingRequest("usr_viewer", "success", now))
	if err != nil {
		t.Fatal(err)
	}
	if result.User.Status != domain.UserDisabled || result.RevokedSessions != 1 || result.RevokedAPITokens != 1 {
		t.Fatalf("result=%#v", result)
	}
	var mappings, members int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM oidc_external_identities WHERE user_id='usr_viewer'`).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM project_members WHERE user_id='usr_viewer'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if mappings != 1 || members != 1 {
		t.Fatalf("historical links mappings=%d project_members=%d", mappings, members)
	}
	var auditSessions, auditTokens int
	if err := database.QueryRow(ctx, `SELECT (metadata->>'revoked_sessions')::int, (metadata->>'revoked_api_tokens')::int FROM audit_events WHERE id='audit_offboard_success'`).Scan(&auditSessions, &auditTokens); err != nil {
		t.Fatal(err)
	}
	if auditSessions != 1 || auditTokens != 1 {
		t.Fatalf("audit counts sessions=%d tokens=%d", auditSessions, auditTokens)
	}
	if _, err := pg.GetPrincipalBySessionTokenHash(ctx, "session-offboard-hash", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled session lookup error=%v", err)
	}
	if _, err := pg.GetAPITokenByHash(ctx, "token-offboard-hash", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled token lookup error=%v", err)
	}
}

func TestPostgresConcurrentOffboardingPreservesOneActiveAdministrator(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "user_offboard_admin_race")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	if _, err := database.Exec(ctx, `INSERT INTO users (id, email, name, status, global_role, password_hash) VALUES ('usr_admin_two', 'admin-two@example.local', 'Admin Two', 'active', 'system_admin', 'hash')`); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for i, userID := range []string{"usr_bootstrap", "usr_admin_two"} {
		group.Add(1)
		go func(i int, userID string) {
			defer group.Done()
			<-start
			_, err := pg.OffboardUser(ctx, offboardingRequest(userID, fmt.Sprintf("admin_race_%d", i), now))
			errs <- err
		}(i, userID)
	}
	close(start)
	group.Wait()
	close(errs)
	successes, denied := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrLastAdministrator):
			denied++
		default:
			t.Fatalf("concurrent offboarding error=%v", err)
		}
	}
	var activeAdmins, successAudits, deniedAudits int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM users WHERE status='active' AND global_role='system_admin'`).Scan(&activeAdmins); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='identity.user.offboard' AND target_id IN ('usr_bootstrap','usr_admin_two')`).Scan(&successAudits); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='identity.user.offboard.denied' AND metadata->>'reason'='last_active_system_administrator'`).Scan(&deniedAudits); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || denied != 1 || activeAdmins != 1 || successAudits != 1 || deniedAudits != 1 {
		t.Fatalf("successes=%d denied=%d active_admins=%d success_audits=%d denied_audits=%d", successes, denied, activeAdmins, successAudits, deniedAudits)
	}
}

func TestPostgresOffboardingSerializesCredentialIssuance(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*testing.T, *pgxpool.Pool)
		issue  func(context.Context, *store.PostgresStore, time.Time) error
		query  string
		active func(*testing.T, *pgxpool.Pool) int
	}{
		{
			name: "session",
			issue: func(ctx context.Context, pg *store.PostgresStore, now time.Time) error {
				return pg.CreateSession(ctx, domain.Session{ID: "session_race", UserID: "usr_viewer", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "session-race-hash")
			},
			query: "FOR SHARE",
			active: func(t *testing.T, database *pgxpool.Pool) int {
				return queryCount(t, database, `SELECT count(*) FROM sessions WHERE id='session_race' AND revoked_at IS NULL`)
			},
		},
		{
			name: "api_token",
			issue: func(ctx context.Context, pg *store.PostgresStore, now time.Time) error {
				_, err := pg.CreateAPIToken(ctx, domain.APIToken{ID: "token_race", Name: "race", Kind: domain.TokenKindServiceAccount, TokenHash: "token-race-hash", Roles: []string{domain.RoleViewer}, Status: domain.TokenActive, CreatedBy: "usr_viewer", CreatedAt: now})
				return err
			},
			query: "FOR SHARE",
			active: func(t *testing.T, database *pgxpool.Pool) int {
				return queryCount(t, database, `SELECT count(*) FROM api_tokens WHERE id='token_race' AND status='active' AND revoked_at IS NULL`)
			},
		},
		{
			name: "oidc_session",
			setup: func(t *testing.T, database *pgxpool.Pool) {
				if _, err := database.Exec(t.Context(), `INSERT INTO oidc_external_identities (issuer, subject, user_id) VALUES ('https://issuer.example', 'viewer-race', 'usr_viewer')`); err != nil {
					t.Fatal(err)
				}
			},
			issue: func(ctx context.Context, pg *store.PostgresStore, now time.Time) error {
				_, err := pg.CreateOIDCSession(ctx, "https://issuer.example", "viewer-race", domain.Session{ID: "oidc_session_race", ExpiresAt: now.Add(time.Hour), CreatedAt: now}, "oidc-session-race-hash", domain.AuditEvent{ID: "audit_oidc_session_race", Action: "oidc.login", CreatedAt: now})
				return err
			},
			query: "FOR SHARE OF users",
			active: func(t *testing.T, database *pgxpool.Pool) int {
				return queryCount(t, database, `SELECT count(*) FROM sessions WHERE id='oidc_session_race' AND revoked_at IS NULL`)
			},
		},
	} {
		for _, order := range []string{"issuance_first", "offboarding_first"} {
			t.Run(test.name+"/"+order, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				seededStore, database := openPostgresIntegrationStore(t, ctx, "user_offboard_"+test.name+"_"+order)
				defer closePostgresStore(t, seededStore)
				now := time.Now().UTC()
				if test.setup != nil {
					test.setup(t, database)
				}
				blocker, err := database.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = blocker.Exec(ctx, `SELECT id FROM users WHERE id='usr_viewer' FOR UPDATE`); err != nil {
					t.Fatal(err)
				}
				baseURL := database.Config().ConnString()
				issueApplication := "offboarding_issue_" + test.name + "_" + order
				offboardApplication := "offboarding_mutate_" + test.name + "_" + order
				issueStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, baseURL, issueApplication))
				if err != nil {
					t.Fatal(err)
				}
				defer closePostgresStore(t, issueStore)
				offboardStore, err := store.OpenPostgres(ctx, databaseURLWithApplicationName(t, baseURL, offboardApplication))
				if err != nil {
					t.Fatal(err)
				}
				defer closePostgresStore(t, offboardStore)
				issueResult := make(chan error, 1)
				offboardResult := make(chan error, 1)
				startIssue := func() { go func() { issueResult <- test.issue(ctx, issueStore, now) }() }
				startOffboard := func() {
					go func() {
						_, err := offboardStore.OffboardUser(ctx, offboardingRequest("usr_viewer", test.name+"_"+order, now))
						offboardResult <- err
					}()
				}
				if order == "issuance_first" {
					startIssue()
					waitForPostgresLockWait(t, ctx, database, issueApplication, test.query)
					startOffboard()
					waitForPostgresLockWait(t, ctx, database, offboardApplication, "FOR UPDATE")
				} else {
					startOffboard()
					waitForPostgresLockWait(t, ctx, database, offboardApplication, "FOR UPDATE")
					startIssue()
					waitForPostgresLockWait(t, ctx, database, issueApplication, test.query)
				}
				if err = blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				issueErr := <-issueResult
				offboardErr := <-offboardResult
				if offboardErr != nil {
					t.Fatalf("offboarding error=%v", offboardErr)
				}
				if order == "offboarding_first" && !errors.Is(issueErr, store.ErrNotFound) {
					t.Fatalf("issuance after queued offboarding error=%v, want ErrNotFound", issueErr)
				}
				if order == "issuance_first" && issueErr != nil {
					t.Fatalf("queued issuance error=%v", issueErr)
				}
				if active := test.active(t, database); active != 0 {
					t.Fatalf("active credentials after race=%d", active)
				}
			})
		}
	}
}

func queryCount(t *testing.T, database *pgxpool.Pool, query string) int {
	t.Helper()
	var count int
	if err := database.QueryRow(t.Context(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
