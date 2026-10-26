package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

type archiveRuntimeRow struct {
	values []any
	err    error
}

func TestArchiveRestoredStorageIdentityPreservesInstantAndAttribution(t *testing.T) {
	original := domain.HostingStorage{ID: "storage", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Hash: "hash", CreatedBy: "owner", CreatedAt: time.Date(2026, time.October, 26, 12, 30, 0, 123000, time.UTC)}
	for _, scenario := range []string{"utc_location", "positive_offset", "negative_offset", "changed_instant", "changed_identity", "changed_attribution"} {
		t.Run(scenario, func(t *testing.T) {
			current := original
			want := true
			switch scenario {
			case "utc_location":
				current.CreatedAt = original.CreatedAt.In(time.FixedZone("local UTC", 0))
			case "positive_offset":
				current.CreatedAt = original.CreatedAt.In(time.FixedZone("east", 5*60*60))
			case "negative_offset":
				current.CreatedAt = original.CreatedAt.In(time.FixedZone("west", -7*60*60))
			case "changed_instant":
				current.CreatedAt = original.CreatedAt.Add(time.Microsecond)
				want = false
			case "changed_identity":
				current.BindingID = "different_binding"
				want = false
			case "changed_attribution":
				current.CreatedBy = "different_owner"
				want = false
			}
			if storageDeploymentSameRestoredStorage(original, current) != want {
				t.Fatalf("restored storage comparison changed %s contract", scenario)
			}
		})
	}
}

func (r archiveRuntimeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("fixture row mismatch")
	}
	for i, v := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

type archiveRuntimeDB struct {
	t                 *testing.T
	row               archiveRuntimeRow
	referenceErr      error
	reads, references int
}

func (d *archiveRuntimeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	d.t.Fatal("pinned read attempted mutation")
	return pgconn.CommandTag{}, nil
}
func (d *archiveRuntimeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	d.t.Fatal("unexpected unbounded query")
	return nil, nil
}
func (d *archiveRuntimeDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "GetHostingRuntimeVersion") {
		d.reads++
		if !reflect.DeepEqual(args, []any{"source", int64(7)}) {
			d.t.Fatal("read did not select pinned source generation")
		}
		return d.row
	}
	if strings.Contains(query, "ValidateHostingRuntimeSecretReference") {
		d.references++
		if !reflect.DeepEqual(args, []any{"secret", "project", int64(3)}) {
			d.t.Fatal("reference authority lost project/version")
		}
		return archiveRuntimeRow{values: []any{"secret"}, err: d.referenceErr}
	}
	d.t.Fatal("unexpected query, secret ciphertext must never be read")
	return archiveRuntimeRow{err: pgx.ErrNoRows}
}

func TestArchivePinnedRuntimeLoadsExactImmutableVersionAndAuthority(t *testing.T) {
	literal := "public input"
	entries := []domain.HostingRuntimeEntry{{Name: "PASSWORD", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: "secret", Version: 3}}, {Name: "APP", Kind: "literal", Value: &literal}}
	doc, _, err := domain.CanonicalHostingRuntimeDocument(entries)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(doc)
	hash := domain.HostingDocumentHash(doc)
	plan := domain.HostingStorageArchivePlan{ProjectID: "project", SourceHealthy: domain.HostingHealthy{Snapshot: domain.HostingSnapshot{ResourceID: "source", PolicyVersion: domain.HostingOwnedStorageRuntimePolicyVersion, RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 7, Hash: hash}}}}
	for _, scenario := range []string{"valid", "missing_document", "missing_secret_version", "revoked_secret", "wrong_project", "wrong_hash", "noncanonical", "malformed", "unknown_field", "old_policy"} {
		t.Run(scenario, func(t *testing.T) {
			d := &archiveRuntimeDB{t: t, row: archiveRuntimeRow{values: []any{"source", "project", int64(7), json.RawMessage(body), hash, "owner", time.Now()}}}
			p := plan
			switch scenario {
			case "missing_document":
				d.row.err = pgx.ErrNoRows
			case "missing_secret_version", "revoked_secret":
				d.referenceErr = pgx.ErrNoRows
			case "wrong_project":
				d.row.values[1] = "other"
			case "wrong_hash":
				d.row.values[4] = "sha256:" + strings.Repeat("b", 64)
			case "noncanonical":
				raw, _ := json.Marshal(domain.HostingRuntimeDocument{Entries: entries})
				d.row.values[3] = json.RawMessage(raw)
			case "malformed":
				d.row.values[3] = json.RawMessage(`{"entries":null}`)
			case "unknown_field":
				d.row.values[3] = json.RawMessage(strings.TrimSuffix(string(body), "}") + `,"unexpected":true}`)
			case "old_policy":
				p.SourceHealthy.Snapshot.PolicyVersion = domain.HostingOwnedStoragePolicyVersion
			}
			got, refs, err := archivePinnedRuntime(t.Context(), sqlcgen.New(d), p)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid pinned runtime admitted")
				}
				return
			}
			if err != nil || domain.HostingDocumentHash(got) != hash || len(refs) != 1 || refs[0].Version != 3 || d.reads != 1 || d.references != 1 {
				t.Fatal("exact public pinned configuration not returned", err)
			}
			*got.Entries[0].Value = "changed returned copy"
			if *doc.Entries[0].Value != literal {
				t.Fatal("returned document aliased source")
			}
		})
	}
	d := &archiveRuntimeDB{t: t}
	old := plan
	old.SourceHealthy.Snapshot.PolicyVersion = domain.HostingOwnedStoragePolicyVersion
	old.SourceHealthy.Snapshot.RuntimeConfiguration = domain.HostingRuntimeConfigurationRef{}
	if _, _, err := archivePinnedRuntime(t.Context(), sqlcgen.New(d), old); err != nil || d.reads != 0 {
		t.Fatal("old storage family required a runtime document")
	}
}
