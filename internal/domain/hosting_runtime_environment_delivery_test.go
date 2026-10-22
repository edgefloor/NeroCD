package domain

import (
	"strings"
	"testing"
)

func TestHostingRuntimeDeliveryID(t *testing.T) {
	for _, id := range []string{"", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("a", 64)} {
		if ValidHostingRuntimeDeliveryID(id) {
			t.Fatal("invalid ID accepted")
		}
	}
	if !ValidHostingRuntimeDeliveryID(strings.Repeat("a", 32)) {
		t.Fatal("valid ID rejected")
	}
}
