package store_test

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// hostingResourceSchemaFixture retains only objects lost or replaced when the
// readiness test drops resources and replays 0040. Definitions come from the
// fully migrated fixture, so later migrations keep their original protections.
type hostingResourceSchemaFixture struct {
	constraints []string
	references  []string
	triggers    []string
	functions   []string
}

func captureHostingResourceSchema(t *testing.T, database *pgxpool.Pool) hostingResourceSchemaFixture {
	t.Helper()
	var fixture hostingResourceSchemaFixture
	for _, query := range []struct {
		sql    string
		target *[]string
	}{
		{`SELECT coalesce(array_agg(format('ALTER TABLE %s ADD CONSTRAINT %I %s',conrelid::regclass,conname,pg_get_constraintdef(oid)) ORDER BY CASE WHEN contype IN ('p','u') THEN 0 ELSE 1 END,conname),ARRAY[]::text[]) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.resources') AND contype<>'t'`, &fixture.constraints},
		{`SELECT coalesce(array_agg(format('ALTER TABLE %s ADD CONSTRAINT %I %s',conrelid::regclass,conname,pg_get_constraintdef(oid)) ORDER BY conrelid::regclass::text,conname),ARRAY[]::text[]) FROM pg_constraint WHERE confrelid=to_regclass(current_schema()||'.resources') AND conrelid<>to_regclass(current_schema()||'.resources')`, &fixture.references},
		{`SELECT coalesce(array_agg(pg_get_triggerdef(oid)||'; '||format('ALTER TABLE resources %s TRIGGER %I',CASE tgenabled WHEN 'O' THEN 'ENABLE' WHEN 'D' THEN 'DISABLE' WHEN 'R' THEN 'ENABLE REPLICA' WHEN 'A' THEN 'ENABLE ALWAYS' END,tgname) ORDER BY tgname),ARRAY[]::text[]) FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.resources') AND NOT tgisinternal`, &fixture.triggers},
		{`SELECT array_agg(pg_get_functiondef(oid) ORDER BY proname) FROM pg_proc WHERE pronamespace=current_schema()::regnamespace AND proname IN ('nerocd_hosting_resource_update','nerocd_hosting_schema_compatible') AND pronargs=0`, &fixture.functions},
	} {
		if err := database.QueryRow(t.Context(), query.sql).Scan(query.target); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.functions) != 2 {
		t.Fatal("resource update or compatibility function missing from fixture")
	}
	return fixture
}

func (fixture hostingResourceSchemaFixture) restore(t *testing.T, database *pgxpool.Pool) {
	t.Helper()
	// Keep identical constraints recreated by the embedded migrations. In
	// particular, deparsing and reparsing a BETWEEN check changes its expression
	// tree's grouping. Restore only missing or different constraint definitions.
	current := captureHostingResourceSchema(t, database)
	var drops []string
	if err := database.QueryRow(t.Context(), `SELECT array_agg(statement ORDER BY statement) FROM (
 SELECT format('ALTER TABLE resources DROP CONSTRAINT %I',conname) AS statement FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.resources') AND contype<>'t'
 AND NOT format('ALTER TABLE %s ADD CONSTRAINT %I %s',conrelid::regclass,conname,pg_get_constraintdef(oid))=ANY($1::text[])
 UNION ALL SELECT format('DROP TRIGGER %I ON resources',tgname) FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.resources') AND NOT tgisinternal
) objects`, fixture.constraints).Scan(&drops); err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{drops, fixture.constraints, fixture.functions, fixture.triggers, fixture.references} {
		for _, statement := range statements {
			if slices.Contains(current.constraints, statement) {
				continue
			}
			if _, err := database.Exec(t.Context(), statement); err != nil {
				t.Fatalf("restore resources schema fixture using %s: %v", statement, err)
			}
		}
	}
}
