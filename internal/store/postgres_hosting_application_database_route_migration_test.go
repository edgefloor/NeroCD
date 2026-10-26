package store_test

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/store"
)

func TestPostgresApplicationDatabaseRouteMigrationUpgrade(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL to run PostgreSQL migration upgrade")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal("open migration fixture")
	}
	schema := "nerocd_route_upgrade_" + strings.ToLower(rand.Text()[:16])
	created := false
	// Register cleanup before CREATE. A collision never grants schema ownership.
	t.Cleanup(func() {
		defer admin.Close()
		if !created {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := admin.Exec(cleanup, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); e != nil {
			t.Error("drop owned migration schema", schema, e)
		}
	})
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("create migration fixture", err)
	}
	created = true
	address = databaseURLWithSearchPath(t, address, schema)
	database, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal("open migration schema")
	}
	t.Cleanup(database.Close)
	if err = applyEmbeddedMigrations(ctx, database, "", "0072_hosting_application_database_connections.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	// The historical predecessor is valid for this upgrade, but lacks the
	// notification schema required by the current server.
	if predecessor, err := store.OpenPostgres(ctx, address); err == nil {
		closePostgresStore(t, predecessor)
		t.Fatal("current server accepted incomplete predecessor schema")
	}
	var before, after string
	const history = `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM projects p),(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM principals p),(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM resources p))::text`
	if err = database.QueryRow(ctx, history).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err = database.QueryRow(ctx, "SELECT to_regclass(current_schema()||'.hosting_application_database_route_continuations') IS NOT NULL").Scan(&present); err != nil || present {
		t.Fatal("old schema manufactured route continuation", err)
	}

	if err = applyEmbeddedMigrations(ctx, database, "0072_hosting_application_database_connections.sql", "0073_hosting_application_database_route_continuity.sql"); err != nil {
		t.Fatal(err)
	}
	var compatible bool
	if err = database.QueryRow(ctx, `SELECT nerocd_appdb_route_schema_compatible()`).Scan(&compatible); err != nil || !compatible {
		t.Fatal("new connection schema incompatible", err)
	}
	if err = database.QueryRow(ctx, history).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed historical resource or principal data", err)
	}
	// After proving the historical 0072-to-0073 upgrade, bring the fixture to
	// the current schema in migration order before opening the current server.
	if err = applyEmbeddedMigrations(ctx, database, "0073_hosting_application_database_route_continuity.sql", ""); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.OpenPostgres(ctx, address)
	if err != nil {
		t.Fatal("open fully compatible upgraded schema", err)
	}
	t.Cleanup(func() { closePostgresStore(t, upgraded) })
}
