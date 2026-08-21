package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// provisionAppRole is deliberately idempotent: the migration owner remains
// schema owner, while the runtime credential is constrained to DML.
func provisionAppRole(args []string) error {
	fs := flag.NewFlagSet("provision-app-role", flag.ExitOnError)
	ownerFile := fs.String("owner-file", "", "owner database URL file")
	appFile := fs.String("app-file", "", "application database URL file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ownerRaw, err := readOwnerOnlyProductionSecret(strings.TrimSpace(*ownerFile))
	if err != nil {
		return err
	}
	appRaw, err := readOwnerOnlyProductionSecret(strings.TrimSpace(*appFile))
	if err != nil {
		return err
	}
	ownerURL, appURL := strings.TrimSpace(string(ownerRaw)), strings.TrimSpace(string(appRaw))
	if err := validateProductionCredentialPair(ownerURL, appURL, os.Getenv("NEROCD_OWNER_DATABASE_USER"), os.Getenv("NEROCD_APP_DATABASE_USER")); err != nil {
		return err
	}
	parsed, _ := parseProductionDatabaseURL(appURL)
	password := parsed.password
	pool, err := pgxpool.New(context.Background(), ownerURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	role := parsed.username
	if _, err = pool.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=%s) THEN EXECUTE format('CREATE ROLE %%I LOGIN PASSWORD %%L', %s, %s); ELSE EXECUTE format('ALTER ROLE %%I LOGIN PASSWORD %%L', %s, %s); END IF; END $$`, quoteLiteral(role), quoteLiteral(role), quoteLiteral(password), quoteLiteral(role), quoteLiteral(password))); err != nil {
		return err
	}
	// Functions are deliberately granted explicitly as well as tables and
	// sequences.  Future migration-owned functions must remain usable by the
	// application without making the runtime role a schema owner.
	_, err = pool.Exec(ctx, `GRANT CONNECT ON DATABASE `+quoteIdent(parsed.database)+` TO `+quoteIdent(role)+`; GRANT USAGE ON SCHEMA public TO `+quoteIdent(role)+`; GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO `+quoteIdent(role)+`; GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO `+quoteIdent(role)+`; GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO `+quoteIdent(role)+`; REVOKE UPDATE, DELETE ON audit_events FROM `+quoteIdent(role)+`; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO `+quoteIdent(role)+`; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO `+quoteIdent(role)+`; ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT EXECUTE ON FUNCTIONS TO `+quoteIdent(role))
	return err
}

func quoteIdent(v string) string   { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func quoteLiteral(v string) string { return `'` + strings.ReplaceAll(v, `'`, `''`) + `'` }
