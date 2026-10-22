package domain

import (
	"strings"
	"testing"
)

func TestHostingSourceDeliveryCanonicalIdentity(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{{strings.Repeat("a", 32), true}, {strings.Repeat("f", 64), true}, {strings.Repeat("A", 32), false}, {strings.Repeat("a", 31), false}, {strings.Repeat("a", 33), false}, {strings.Repeat("g", 32), false}, {" " + strings.Repeat("a", 32), false}, {"", false}} {
		if ValidHostingSourceDeliveryID(test.value) != test.valid {
			t.Fatal("delivery ID canonical validation mismatch")
		}
	}
}
