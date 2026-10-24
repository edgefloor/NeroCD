package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDatabaseConnectionContextPinsOriginalCredential(t *testing.T) {
	ctx := t.Context()
	if _, err := databaseConnectionContextCredential(ctx, "runner"); !errors.Is(err, ErrConflict) {
		t.Fatal("missing context inferred current identity")
	}
	fingerprint := strings.Repeat("a", 64)
	bound, err := BindHostingDatabaseConnectionRunnerContext(ctx, "runner", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := databaseConnectionContextCredential(bound, "runner"); err != nil || got != fingerprint {
		t.Fatal("original fingerprint was not retained", err)
	}
	if _, err = databaseConnectionContextCredential(bound, "foreign"); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign runner consumed context")
	}
	if _, err = BindHostingDatabaseConnectionRunnerContext(bound, "runner", strings.Repeat("b", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("request refreshed a rotated credential")
	}
	if same, err := BindHostingDatabaseConnectionRunnerContext(bound, "runner", fingerprint); err != nil || same != bound {
		t.Fatal("identical binding changed the context", err)
	}
	for _, bad := range []string{"", "bearer_secret_canary", strings.Repeat("g", 64), strings.Repeat("a", 62)} {
		if _, err = BindHostingDatabaseConnectionRunnerContext(ctx, "runner", bad); !errors.Is(err, ErrConflict) {
			t.Fatal("nonfingerprint accepted")
		}
	}
	if _, err = BindHostingDatabaseConnectionRunnerContext(context.Background(), "", fingerprint); !errors.Is(err, ErrConflict) {
		t.Fatal("empty identity bound")
	}
}
