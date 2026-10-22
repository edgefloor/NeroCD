package hostingbuildprotocol

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func authorityFixture() domain.HostingBuildAuthority {
	return domain.HostingBuildAuthority{BuildID: "build-one", RunID: "build_run_build-one", LeaseID: "lease_one", Attempt: 1, Fence: "fence_one"}
}

// This fixture deliberately has no store dependency. Its declaration preserves
// the persisted completion body's field order and optional fields independently.
type persistedCompletionFixture struct {
	State       string                       `json:"state"`
	FailureCode string                       `json:"failure_code,omitempty"`
	Artifact    *domain.HostingBuildArtifact `json:"artifact,omitempty"`
}

func TestCompletionGoldenEncodingAndHash(t *testing.T) {
	artifact := &domain.HostingBuildArtifact{Origin: "local_build", BuildID: "build-one", SourceID: "source_one", InputID: "input_one", SourceReceiptHash: "sourcehash", ContextDigest: "context", RequestDigest: "request", DaemonID: "daemon", ImageID: "image", IndexDigest: "index", ManifestDigest: "manifest", ConfigDigest: "config", Platform: "linux/amd64"}
	// Artifact strings below exercise encoding only, not provenance validation.
	for _, test := range []struct {
		name       string
		body       Completion
		json, hash string
	}{
		{"failure", Completion{State: "failed", FailureCode: "build_failed"}, `{"state":"failed","failure_code":"build_failed"}`, "sha256:a9b53507ed4589e935495bb8f1ef74ae31f66a53787b746a50e2192434f93abf"},
		{"cleanup", Completion{State: "cleanup_required", FailureCode: "cleanup_unconfirmed"}, `{"state":"cleanup_required","failure_code":"cleanup_unconfirmed"}`, "sha256:e5edaa38bade133dd50c06afbd86c53b4ac27c662ed7e41ecabde75d02e681ec"},
		{"artifact", Completion{State: "succeeded", Artifact: artifact}, `{"state":"succeeded","artifact":{"origin":"local_build","build_id":"build-one","source_id":"source_one","input_id":"input_one","source_receipt_hash":"sourcehash","context_digest":"context","request_digest":"request","daemon_id":"daemon","image_id":"image","index_digest":"index","manifest_digest":"manifest","config_digest":"config","platform":"linux/amd64"}}`, "sha256:24077ddb2e8128ed1808c19bf85287961a98300faf42a0311d5c9fbbd643b1f9"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := persistedCompletionFixture{test.body.State, test.body.FailureCode, test.body.Artifact}
			encoded, err := json.Marshal(test.body)
			if err != nil || string(encoded) != test.json {
				t.Fatalf("wire JSON drift: %s %v", encoded, err)
			}
			stored, err := json.Marshal(fixture)
			if err != nil || string(stored) != test.json || domain.HostingDocumentHash(fixture) != test.hash || CompletionHash(test.body) != test.hash {
				t.Fatal("persisted and wire body hash differ", err)
			}
			request := CompleteRequest{Authority: authorityFixture(), ReceiptID: "receipt_one", ExpectedRevision: 2, Completion: test.body}
			before := CompletionHash(request.Completion)
			request.ExpectedRevision = 7
			request.ReceiptID = "receipt_other"
			if CompletionHash(request.Completion) != before || domain.HostingDocumentHash(request) == before {
				t.Fatal("completion hash includes request envelope")
			}
		})
	}
}

func TestAcknowledgmentRequiresExactRetainedIntent(t *testing.T) {
	authority := authorityFixture()
	receipt := domain.HostingBuildSourceReceipt{BuildID: authority.BuildID, SourceID: "source_one", InputID: "input_one", Commit: strings.Repeat("a", 40), PolicyHash: "sha256:" + strings.Repeat("b", 64), TreeDigest: "sha256:" + strings.Repeat("c", 64)}
	hash := SourceReceiptHash(receipt)
	request := SourceCommitRequest{Authority: authority, ReceiptID: "receipt alpha/β", Receipt: receipt}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	ack := ReceiptAcknowledgment{Authority: authority, Phase: "source", ReceiptID: request.ReceiptID, BodyHash: hash}
	if err := ValidateAcknowledgment(ack, authority, "source", request.ReceiptID, hash); err != nil {
		t.Fatal("valid committed proof rejected", err)
	}
	for _, mutate := range []struct {
		name   string
		change func(*ReceiptAcknowledgment)
	}{
		{"build", func(a *ReceiptAcknowledgment) { a.Authority.BuildID = "other" }},
		{"run", func(a *ReceiptAcknowledgment) { a.Authority.RunID = "other" }},
		{"lease", func(a *ReceiptAcknowledgment) { a.Authority.LeaseID = "other" }},
		{"attempt", func(a *ReceiptAcknowledgment) { a.Authority.Attempt++ }},
		{"fence", func(a *ReceiptAcknowledgment) { a.Authority.Fence = "other" }},
		{"phase", func(a *ReceiptAcknowledgment) { a.Phase = "complete" }},
		{"id", func(a *ReceiptAcknowledgment) { a.ReceiptID = "other" }},
		{"hash", func(a *ReceiptAcknowledgment) { a.BodyHash = "sha256:" + strings.Repeat("d", 64) }},
		{"invalid digest", func(a *ReceiptAcknowledgment) { a.BodyHash = "sha256:" + strings.Repeat("G", 64) }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			changed := ack
			mutate.change(&changed)
			if ValidateAcknowledgment(changed, authority, "source", request.ReceiptID, hash) == nil {
				t.Fatal("altered proof accepted")
			}
		})
	}
	changed := receipt
	changed.Commit = strings.Repeat("f", 40)
	if ValidateAcknowledgment(ack, authority, "source", request.ReceiptID, SourceReceiptHash(changed)) == nil {
		t.Fatal("altered receipt body acknowledged")
	}
	if ValidateAcknowledgment(ack, authority, "result", request.ReceiptID, hash) == nil {
		t.Fatal("journal result kind accepted as store receipt phase")
	}
	if ValidateAcknowledgment(ack, authority, "source", request.ReceiptID, "sha256:"+strings.Repeat("A", 64)) == nil {
		t.Fatal("noncanonical expected digest accepted")
	}
	completion := Completion{State: "cleanup_required", FailureCode: "cleanup_unconfirmed"}
	phase, err := CompletionPhase(completion)
	if err != nil || phase != "cleanup" {
		t.Fatal("cleanup receipt phase drift")
	}
	completeAck := ReceiptAcknowledgment{Authority: authority, Phase: phase, ReceiptID: "cleanup_receipt", BodyHash: CompletionHash(completion)}
	if ValidateAcknowledgment(completeAck, authority, phase, "cleanup_receipt", CompletionHash(completion)) != nil {
		t.Fatal("valid cleanup receipt rejected")
	}
	completion.FailureCode = "authority_lost"
	if ValidateAcknowledgment(completeAck, authority, phase, "cleanup_receipt", CompletionHash(completion)) == nil {
		t.Fatal("changed completion body acknowledged")
	}
}

func TestRequestShapesAndStoreBounds(t *testing.T) {
	authority := authorityFixture()
	for _, id := range []string{"a", "0", "a-", strings.Repeat("a", 63)} {
		a := authority
		a.BuildID = id
		a.RunID = "build_run_" + id
		if ValidateAuthority(a) != nil {
			t.Fatal("valid build ID rejected")
		}
	}
	for _, id := range []string{"", "-bad", "Upper", "has_underscore", strings.Repeat("a", 64), "é", " space"} {
		a := authority
		a.BuildID = id
		a.RunID = "build_run_" + id
		if ValidateAuthority(a) == nil {
			t.Fatal("invalid build ID accepted")
		}
	}
	aboveMaximum := int64(math.MaxInt32) + 1
	for _, attempt := range []int{0, -1, int(aboveMaximum)} {
		a := authority
		a.Attempt = attempt
		if ValidateAuthority(a) == nil {
			t.Fatal("invalid attempt accepted")
		}
	}
	a := authority
	a.Attempt = math.MaxInt32
	if ValidateAuthority(a) != nil {
		t.Fatal("store attempt boundary rejected")
	}
	for _, id := range []string{"", " receipt", "receipt ", "receipt\n", strings.Repeat("a", 129)} {
		if validReceiptID(id) {
			t.Fatal("invalid receipt ID accepted")
		}
	}
	if !validReceiptID(strings.Repeat("β", 64)) {
		t.Fatal("byte-bounded receipt identity rejected")
	}
	for _, revision := range []int64{1, math.MaxInt64} {
		if (SourceBeginRequest{Authority: authority, ExpectedRevision: revision}).Validate() != nil {
			t.Fatal("positive int64 revision rejected")
		}
	}
	if (SourceBeginRequest{Authority: authority}).Validate() == nil {
		t.Fatal("zero revision accepted")
	}
	if (StatusRequest{Authority: authority}).Validate() != nil {
		t.Fatal("status authority rejected")
	}
	if (PrepareRequest{Authority: authority, ExpectedRevision: 1, DaemonID: "daemon"}).Validate() != nil {
		t.Fatal("valid prepare rejected")
	}
	for _, daemon := range []string{"", " daemon", "daemon\n", strings.Repeat("a", 257)} {
		if (PrepareRequest{Authority: authority, ExpectedRevision: 1, DaemonID: daemon}).Validate() == nil {
			t.Fatal("invalid daemon accepted")
		}
	}
	if (SourceCommitRequest{Authority: authority, ReceiptID: "source_receipt", Receipt: domain.HostingBuildSourceReceipt{BuildID: "other"}}).Validate() == nil {
		t.Fatal("foreign source build accepted")
	}
	valid := CompleteRequest{Authority: authority, ReceiptID: "completion_receipt", ExpectedRevision: 1, Completion: Completion{State: "failed", FailureCode: "build_failed"}}
	if valid.Validate() != nil {
		t.Fatal("valid completion rejected")
	}
	for _, body := range []Completion{{State: "unknown"}, {State: "succeeded"}, {State: "succeeded", FailureCode: "build_failed", Artifact: &domain.HostingBuildArtifact{}}, {State: "failed", FailureCode: "raw stderr"}, {State: "canceled", FailureCode: "canceled", Artifact: &domain.HostingBuildArtifact{}}} {
		if body.Validate() == nil {
			t.Fatal("invalid completion combination accepted")
		}
	}
	for _, state := range []string{"failed", "canceled", "cleanup_required"} {
		body := Completion{State: state, FailureCode: "authority_lost"}
		if body.Validate() != nil {
			t.Fatal("store-compatible failure combination rejected")
		}
	}
	valid.Completion = Completion{State: "succeeded", Artifact: &domain.HostingBuildArtifact{BuildID: "other"}}
	if valid.Validate() == nil {
		t.Fatal("foreign artifact build accepted")
	}
}

func TestWireFieldsAndHistoricalProofRemainSeparate(t *testing.T) {
	request := SourceCommitRequest{Authority: authorityFixture(), ReceiptID: "source_receipt", Receipt: domain.HostingBuildSourceReceipt{BuildID: "build-one"}}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 3 || fields["authority"] == nil || fields["receipt_id"] == nil || fields["receipt"] == nil {
		t.Fatal("source commit wire fields drifted")
	}
	// A committed proof is deliberately representable without permission to execute.
	response := StatusResponse{Build: domain.HostingBuild{ID: "build-one"}, MayExecute: false, CancelRequested: true, Receipts: []ReceiptAcknowledgment{{Authority: authorityFixture(), Phase: "source", ReceiptID: "source_receipt", BodyHash: SourceReceiptHash(request.Receipt)}}}
	encoded, err = json.Marshal(response)
	if err != nil || !strings.Contains(string(encoded), `"may_execute":false`) || !strings.Contains(string(encoded), `"cancel_requested":true`) {
		t.Fatal("historical proof granted or hid current authority")
	}
	var omitted PhaseResponse
	encoded, err = json.Marshal(omitted)
	if err != nil || strings.Contains(string(encoded), "acknowledgment") {
		t.Fatal("optional acknowledgment was not omitted")
	}
}
