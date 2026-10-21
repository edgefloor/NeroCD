package app

import (
	"testing"

	"nerocd/internal/store"
)

func TestPaginateForServiceDistinguishesExplicitZeroFromOmittedLimit(t *testing.T) {
	items := make([]int, 130)

	explicitZero := paginateForService(items, store.Page{Enabled: true, LimitSpecified: true})
	if explicitZero.Total != 130 || explicitZero.Offset != 0 || explicitZero.Limit != 0 || len(explicitZero.Items) != 0 {
		t.Fatalf("explicit-zero page = %#v, want total 130 and no items", explicitZero)
	}

	offsetOnly := paginateForService(items, store.Page{Enabled: true, Offset: 100})
	if offsetOnly.Total != 130 || offsetOnly.Offset != 100 || offsetOnly.Limit != store.MaxPageLimit || len(offsetOnly.Items) != 30 {
		t.Fatalf("offset-only page = %#v, want bounded default and 30 later-page items", offsetOnly)
	}
}
