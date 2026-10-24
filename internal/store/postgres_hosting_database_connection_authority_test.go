package store

import (
	"testing"

	"nerocd/internal/domain"
)

func TestDatabaseConnectionRetirementsAreRelativeToActualPredecessor(t *testing.T) {
	old := domain.HostingDatabaseNetworkRetirementEvidence{Attached: domain.HostingDatabaseNetworkMemberEvidence{EndpointPlan: domain.HostingDatabaseBindingEndpointPlan{Hash: "old_plan"}, Receipt: domain.HostingDatabaseBindingEndpointReceipt{Hash: "old_receipt"}}}
	removed := domain.HostingDatabaseNetworkRetirementEvidence{Attached: domain.HostingDatabaseNetworkMemberEvidence{EndpointPlan: domain.HostingDatabaseBindingEndpointPlan{Hash: "removed_plan"}, Receipt: domain.HostingDatabaseBindingEndpointReceipt{Hash: "removed_receipt"}}}
	ledger := []domain.HostingDatabaseNetworkRetirementEvidence{old, removed}
	previous := domain.HostingApplyPlan{Database: &domain.HostingDatabasePlan{Network: &domain.HostingDatabaseNetworkPlan{Members: []domain.HostingDatabaseNetworkMember{{EndpointPlanHash: "removed_plan", ReceiptHash: "removed_receipt"}}}}}
	if got := connectionTransitionRetirements(previous, nil, ledger); len(got) != 1 || got[0].Attached.EndpointPlan.Hash != "removed_plan" {
		t.Fatal("historical retirement was treated as a new removal")
	}
	if got := connectionTransitionRetirements(previous, []domain.HostingDatabaseNetworkMemberEvidence{removed.Attached}, ledger); len(got) != 0 {
		t.Fatal("unchanged member was retired")
	}
	previous.Database.Network.Members = nil
	if got := connectionTransitionRetirements(previous, nil, ledger); len(got) != 0 {
		t.Fatal("first maintenance replayed pre-baseline retirement")
	}
}

func TestDatabaseConnectionEffectRequiresExactCurrentStage(t *testing.T) {
	for _, action := range []string{"create", "configure", "start", "retire"} {
		if action != "create" && connectionEffectStage(action, "", nil, nil) {
			t.Fatal("missing stage authorized effect", action)
		}
	}
	for _, phase := range []string{"candidate_created", "attach_intent", "network_configured", "attached", "ready", "uncertain"} {
		stage := &domain.HostingApplicationDatabaseStage{Phase: phase, Hash: "exact"}
		for _, action := range []string{"create", "configure", "start", "retire"} {
			want := action == "configure" && phase == "attach_intent" || action == "start" && phase == "network_configured"
			if got := connectionEffectStage(action, "exact", stage, nil); got != want {
				t.Fatal("stage acknowledgement changed effect permission", phase, action)
			}
			if connectionEffectStage(action, "foreign", stage, &domain.HostingDatabaseBindingEndpointReceipt{Phase: "retire_intent"}) {
				t.Fatal("foreign expected stage authorized effect")
			}
		}
	}
	stage := &domain.HostingApplicationDatabaseStage{Phase: "ready", Hash: "exact"}
	if !connectionEffectStage("retire", "exact", stage, &domain.HostingDatabaseBindingEndpointReceipt{Phase: "retire_intent"}) || connectionEffectStage("retire", "exact", stage, &domain.HostingDatabaseBindingEndpointReceipt{Phase: "detached"}) {
		t.Fatal("retirement needs exact unsettled controller intent")
	}
	if connectionJSONMatches([]byte(`{"revision":9007199254740993}`), map[string]int64{"revision": 9007199254740992}) {
		t.Fatal("JSONB comparison rounded a membership revision")
	}
}
