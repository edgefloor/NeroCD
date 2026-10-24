package domain

import (
	"math"
	"strings"
	"testing"
)

func TestDatabaseCredentialRequestAuthority(t *testing.T) {
	valid := HostingDatabaseCredentialRequest{Authority: HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), Mode: "deploy"}
	for _, mode := range []string{"deploy", "observe"} {
		in := valid
		in.Mode = mode
		if mode == "observe" {
			in.Authority.RunID = "observe_hop"
		}
		if err := in.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"mode", "run", "attempt", "overflow", "delivery", "hash", "whitespace", "unicode"} {
		t.Run(name, func(t *testing.T) {
			in := valid
			switch name {
			case "mode":
				in.Mode = "cleanup"
			case "run":
				in.Authority.RunID = "observe_hop"
			case "attempt":
				in.Authority.Attempt = 0
			case "overflow":
				in.Authority.Attempt = math.MaxInt32 + 1
			case "delivery":
				in.DeliveryID = strings.Repeat("A", 32)
			case "hash":
				in.PlanHash = "sha256:" + strings.Repeat("z", 64)
			case "whitespace":
				in.Authority.Fence = "fence\n"
			case "unicode":
				in.Authority.LeaseID = string([]byte{255})
			}
			if in.Validate() == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}
