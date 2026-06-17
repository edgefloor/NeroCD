package store

import (
	"context"
	"testing"
)

func TestPostgresPlaceholdersToBob(t *testing.T) {
	query := `
		SELECT id
		FROM projects
		WHERE id = $1 AND name = $2 AND archived_at IS NULL
	`
	want := `
		SELECT id
		FROM projects
		WHERE id = ? AND name = ? AND archived_at IS NULL
	`
	got, args, err := postgresPlaceholdersToBob(query, "prj_1", "Production")
	if err != nil {
		t.Fatalf("convert query: %v", err)
	}
	if got != want {
		t.Fatalf("converted query = %q, want %q", got, want)
	}
	if len(args) != 2 || args[0] != "prj_1" || args[1] != "Production" {
		t.Fatalf("args = %#v", args)
	}
}

func TestPostgresPlaceholdersToBobDuplicatesReusedArgs(t *testing.T) {
	query, args, err := postgresPlaceholdersToBob(`UPDATE api_tokens SET last_used_at = $2 WHERE token_hash = $1 AND expires_at > $2`, "hash", "now")
	if err != nil {
		t.Fatalf("convert query: %v", err)
	}
	if query != `UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ? AND expires_at > ?` {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 3 || args[0] != "now" || args[1] != "hash" || args[2] != "now" {
		t.Fatalf("args = %#v", args)
	}
}

func TestBobSQLBuildsPostgresPlaceholders(t *testing.T) {
	query, args, err := bobSQL(context.Background(), `
		SELECT id
		FROM projects
		WHERE id = $1 AND name = $2
	`, "prj_1", "Production")
	if err != nil {
		t.Fatalf("build SQL: %v", err)
	}
	if query != `
		SELECT id
		FROM projects
		WHERE id = $1 AND name = $2
	` {
		t.Fatalf("query = %q", query)
	}
	if len(args) != 2 || args[0] != "prj_1" || args[1] != "Production" {
		t.Fatalf("args = %#v", args)
	}
}
