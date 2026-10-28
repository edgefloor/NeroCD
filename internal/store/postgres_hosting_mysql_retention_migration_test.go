package store_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresMySQLRetentionMigrationUpgrade(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), address)
	if err != nil {
		t.Fatal("open owned migration database")
	}
	defer admin.Close()
	schema := "nerocd_mysql_retention_upgrade_" + strings.ToLower(rand.Text()[:16])
	created := false
	defer func() {
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
				t.Error("drop owned schema", e)
			}
		}
	}()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	created = true
	db, err := pgxpool.New(t.Context(), databaseURLWithSearchPath(t, address, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = applyEmbeddedMigrations(t.Context(), db, "", "0102_hosting_mysql_remote_restore.sql"); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(t.Context(), db, "0102_hosting_mysql_remote_restore.sql", "0103_hosting_mysql_backup_retention.sql"); err != nil {
		var detail *pgconn.PgError
		if errors.As(err, &detail) {
			t.Logf("migration SQL detail position=%d internal_position=%d query=%s", detail.Position, detail.InternalPosition, detail.InternalQuery)
		}
		t.Fatal(err)
	}
	var compatible bool
	if err = db.QueryRow(t.Context(), "SELECT nerocd_mysql_backup_retention_schema_compatible()").Scan(&compatible); err != nil || !compatible {
		t.Fatal("schema incompatible", err)
	}
}
