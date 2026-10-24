package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDatabaseConnectionClosedRequests(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			op, _ := applicationExecutionFixture(t, engine)
			_, candidate, endpoint := executionCandidate(t, op)
			stage := HostingApplicationDatabaseStage{Version: HostingApplicationDatabaseStageVersion, ReceiptID: "stage_created", OperationID: op.ID, PlanHash: op.Plan.Hash, Phase: "candidate_created", Candidate: candidate, AttachPlan: endpoint}
			stage.Hash = stage.DocumentHash()
			a := HostingAuthority{OperationID: op.ID, RunID: "run_" + op.ID, LeaseID: "lease_connection", Attempt: 1, Fence: "fence_connection"}
			connection := HostingDatabaseConnectionInput{Binding: HostingDatabaseBindingInput{DatabaseResourceID: "db_resource", ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 0, ExpectedDatabaseGeneration: 1, RequestID: strings.Repeat("a", 32)}, Deployment: HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "connect", ContainerPort: 8080, Readiness: HostingReadiness{Path: "/ready", TimeoutSeconds: 10}}}
			cases := []struct {
				name  string
				value any
				fresh func() any
			}{
				{"connection", connection, func() any { return new(HostingDatabaseConnectionInput) }},
				{"maintenance", HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: 2, IdempotencyKey: "maintain"}, func() any { return new(HostingDatabaseMaintenanceInput) }},
				{"recovery", HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: op.ID, StageHash: stage.Hash, CandidateHash: candidate.Hash, ExpectedMembershipRevision: 1, IdempotencyKey: "observe"}, func() any { return new(HostingDatabaseConnectionRecoveryInput) }},
				{"stage", HostingApplicationDatabaseStageRequest{HostingAuthority: a, Stage: stage}, func() any { return new(HostingApplicationDatabaseStageRequest) }},
				{"retirement", HostingApplicationDatabaseRetirementRequest{HostingAuthority: a, PlanHash: endpoint.Hash, Receipt: databaseBindingReceipt(endpoint, "attach_intent")}, func() any { return new(HostingApplicationDatabaseRetirementRequest) }},
				{"effect", HostingApplicationDatabaseEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("b", 32), Action: "create"}, func() any { return new(HostingApplicationDatabaseEffectRequest) }},
				{"effect_authorization", HostingApplicationDatabaseEffectAuthorization{AuthorizationID: strings.Repeat("b", 32), OperationID: a.OperationID, PlanHash: op.Plan.Hash, Action: "create", LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, MembershipRevision: 1, UseUntil: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, func() any { return new(HostingApplicationDatabaseEffectAuthorization) }},
				{"state", HostingApplicationDatabaseExecutionState{OperationID: a.OperationID, PlanHash: op.Plan.Hash, Stage: &stage, MembershipRevision: 1, HoldOperationID: a.OperationID}, func() any { return new(HostingApplicationDatabaseExecutionState) }},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					raw, err := json.Marshal(tc.value)
					if err != nil || json.Unmarshal(raw, tc.fresh()) != nil {
						t.Fatal("valid closed request rejected", err)
					}
					var fields map[string]json.RawMessage
					if err = json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					for key, value := range fields {
						bad := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"`+key+`":`+string(value)+`}`)...)
						if json.Unmarshal(bad, tc.fresh()) == nil {
							t.Fatal("duplicate field accepted", key)
						}
						bad = bytes.Replace(raw, []byte(`"`+key+`":`), []byte(`"`+strings.ToUpper(key)+`":`), 1)
						if json.Unmarshal(bad, tc.fresh()) == nil {
							t.Fatal("case alias accepted", key)
						}
						bad = bytes.Replace(raw, []byte(`"`+key+`":`+string(value)), []byte(`"`+key+`":null`), 1)
						if json.Unmarshal(bad, tc.fresh()) == nil {
							t.Fatal("null field accepted", key)
						}
					}
					for _, bad := range [][]byte{[]byte("null"), []byte("[]"), []byte("{}"), append(append([]byte{}, raw...), []byte(" {}")...), append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"credential":"private_canary"}`)...), append(append([]byte{'{'}, bytes.Repeat([]byte(" "), HostingDatabaseConnectionJSONLimit)...), raw[1:]...)} {
						if json.Unmarshal(bad, tc.fresh()) == nil {
							t.Fatal("malformed or open request accepted")
						}
					}
				})
			}
		})
	}
}

func TestDatabaseConnectionSelectionAndRecoveryLimits(t *testing.T) {
	in := HostingDatabaseConnectionInput{Binding: HostingDatabaseBindingInput{DatabaseResourceID: "db_resource", ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: 2, ExpectedDatabaseGeneration: 1, RequestID: strings.Repeat("a", 32)}, Deployment: HostingDeploymentInput{ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 2, IdempotencyKey: "connect", ContainerPort: 8080, Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 10}}}
	if in.Validate() != nil {
		t.Fatal("same pre-injection CAS rejected")
	}
	in.Deployment.ExpectedRuntimeConfigurationGeneration++
	if in.Validate() == nil {
		t.Fatal("ambiguous pre/post-injection generation accepted")
	}
	r := HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: "original_operation", ExpectedMembershipRevision: 1, IdempotencyKey: "observe_unknown"}
	if r.Validate() != nil {
		t.Fatal("unknown-candidate observation request rejected")
	}
	r.Action = "resume"
	if !errors.Is(r.Validate(), ErrHostingDatabaseRecoveryObserveOnly) {
		t.Fatal("effectful recovery did not explain its unsupported scope")
	}
	for _, action := range []string{"configure", "start", "retire"} {
		req := HostingApplicationDatabaseEffectRequest{HostingAuthority: HostingAuthority{OperationID: "op", RunID: "run_op", LeaseID: "lease", Attempt: 1, Fence: "fence"}, AuthorizationID: strings.Repeat("b", 32), Action: action}
		if req.Validate() == nil {
			t.Fatal("effect request omitted expected stage", action)
		}
		req.ExpectedStageHash = "sha256:" + strings.Repeat("c", 64)
		if req.Validate() != nil {
			t.Fatal("stage-bound effect request rejected", action)
		}
	}
}

func TestDatabaseConnectionEffectIndependentIdentifierBounds(t *testing.T) {
	for _, length := range []int{124, 125, 126, 127, 128} {
		operation := strings.Repeat("a", length)
		request := HostingApplicationDatabaseEffectRequest{HostingAuthority: HostingAuthority{OperationID: operation, RunID: "independent_run", LeaseID: "lease", Attempt: 1, Fence: "fence"}, AuthorizationID: strings.Repeat("b", 32), Action: "create"}
		response := HostingApplicationDatabaseEffectAuthorization{AuthorizationID: request.AuthorizationID, OperationID: operation, PlanHash: "sha256:" + strings.Repeat("c", 64), Action: request.Action, LeaseID: request.LeaseID, Attempt: request.Attempt, Fence: request.Fence, MembershipRevision: 1, UseUntil: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
		if request.Validate() != nil || response.Validate() != nil {
			t.Fatal("independent bounded identifier rejected", length)
		}
		raw, err := json.Marshal(request)
		var accepted HostingApplicationDatabaseEffectRequest
		if err != nil || json.Unmarshal(raw, &accepted) != nil || accepted != request {
			t.Fatal("request boundary did not round trip", length, err)
		}
		raw, err = json.Marshal(response)
		var authorized HostingApplicationDatabaseEffectAuthorization
		if err != nil || json.Unmarshal(raw, &authorized) != nil || authorized != response {
			t.Fatal("response derived an unprovided run ID", length, err)
		}
	}
}
