package domain

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestApplicationDatabaseStageOrderingAndUncertainty(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			op, _ := applicationExecutionFixture(t, engine)
			in, c, ep := executionCandidate(t, op)
			base := HostingApplicationDatabaseStage{Version: HostingApplicationDatabaseStageVersion, OperationID: op.ID, PlanHash: op.Plan.Hash, Candidate: c, AttachPlan: ep}
			var previous *HostingApplicationDatabaseStage
			var stages []HostingApplicationDatabaseStage
			for _, phase := range []string{"candidate_created", "attach_intent", "network_configured", "attached", "ready"} {
				stage := base
				stage.Phase = phase
				stage.ReceiptID = "stage_" + phase
				if phase == "attach_intent" || phase == "network_configured" {
					r := databaseBindingReceipt(ep, "attach_intent")
					stage.EndpointReceipt = &r
				}
				if phase == "attached" || phase == "ready" {
					r := databaseBindingReceipt(ep, "attached")
					stage.EndpointReceipt = &r
				}
				stage.Hash = stage.DocumentHash()
				if stage.ValidateTransition(previous, *op.Plan, in) != nil || stage.ValidateTransition(&stage, *op.Plan, in) != nil {
					t.Fatal("ordered stage rejected", phase)
				}
				raw := mustContractJSON(t, stage)
				var persisted HostingApplicationDatabaseStage
				if len(raw) > HostingApplicationDatabaseStageJSONLimit || json.Unmarshal(raw, &persisted) != nil || persisted.ValidateTransition(previous, *op.Plan, in) != nil {
					t.Fatal("persisted stage rejected", phase)
				}
				stages = append(stages, stage)
				previous = &stages[len(stages)-1]
			}
			for i := 0; i < len(stages)-2; i++ {
				if stages[i+2].ValidateTransition(&stages[i], *op.Plan, in) == nil {
					t.Fatal("stage skip accepted")
				}
			}
			for _, stage := range stages[:len(stages)-1] {
				uncertain := base
				uncertain.ReceiptID = "uncertain_" + stage.Phase
				uncertain.Phase = "uncertain"
				uncertain.FailureCode = "mutation_unconfirmed"
				uncertain.Hash = uncertain.DocumentHash()
				if uncertain.ValidateTransition(&stage, *op.Plan, in) != nil {
					t.Fatal("terminal uncertainty rejected")
				}
				if stages[4].ValidateTransition(&uncertain, *op.Plan, in) == nil {
					t.Fatal("uncertainty replayed as progress")
				}
			}
			attached := stages[3]
			r := *attached.EndpointReceipt
			observation := *r.Observation
			observation.Endpoint.EndpointID = "foreign"
			r.Observation = &observation
			r.Hash = r.DocumentHash()
			attached.EndpointReceipt = &r
			attached.Hash = attached.DocumentHash()
			if attached.Validate(*op.Plan, in) == nil {
				t.Fatal("forged attached endpoint accepted")
			}
			raw := mustContractJSON(t, stages[0])
			bad := append(append([]byte("{"), bytes.Repeat([]byte(" "), HostingApplicationDatabaseStageJSONLimit)...), raw[1:]...)
			if json.Unmarshal(bad, new(HostingApplicationDatabaseStage)) == nil {
				t.Fatal("oversized stage accepted")
			}
			envelope := struct {
				Plan  HostingApplyPlan                         `json:"plan"`
				Proof HostingApplicationDatabaseExecutionProof `json:"proof"`
				Stage HostingApplicationDatabaseStage          `json:"stage"`
			}{*op.Plan, *op.ApplicationDatabase, stages[4]}
			document := mustContractJSON(t, envelope)
			if len(document) > HostingApplicationDatabaseJournalJSONLimit {
				t.Fatal("future journal bound exceeded")
			}
			t.Logf("serialized execution proof=%d stage=%d composed=%d", len(mustContractJSON(t, op.ApplicationDatabase)), len(mustContractJSON(t, stages[4])), len(document))
		})
	}
}
