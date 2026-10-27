package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestHostingRuntimeLogClosedBoundedInput(t *testing.T) {
	open := HostingRuntimeLogOpen{RequestID: strings.Repeat("a", 32), RequestExpiresAt: time.Now()}
	if _, err := open.Canonical(); err != nil {
		t.Fatal(err)
	}
	batch := HostingRuntimeLogBatch{BatchSequence: 1, SessionID: "rls_test", InstanceID: strings.Repeat("b", 32), CollectionEpoch: strings.Repeat("c", 32), BatchID: strings.Repeat("d", 32), State: "following", Events: []HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "hello"}}}
	if _, err := batch.Canonical(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*HostingRuntimeLogBatch){func(v *HostingRuntimeLogBatch) { v.Events = nil }, func(v *HostingRuntimeLogBatch) { v.State = "error: daemon failed" }, func(v *HostingRuntimeLogBatch) { v.Events[0].Stream = "system" }, func(v *HostingRuntimeLogBatch) { v.Events[0].Component = "bad/name" }, func(v *HostingRuntimeLogBatch) {
		v.Events[0].Message = strings.Repeat("x", HostingRuntimeLogMessageLimit+1)
	}, func(v *HostingRuntimeLogBatch) { v.ExpectedSequence = -1 }, func(v *HostingRuntimeLogBatch) { v.InstanceID = "not-random" }} {
		copy := batch
		copy.Events = append([]HostingRuntimeLogEvent(nil), batch.Events...)
		change(&copy)
		if _, err := copy.Canonical(); err == nil {
			t.Fatalf("invalid batch accepted %+v", copy)
		}
	}
	batch.Events = []HostingRuntimeLogEvent{}
	batch.State = "unsupported_driver"
	if _, err := batch.Canonical(); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`null`, `{"request_id":"x","request_id":"y","request_expires_at":"2026-10-23T00:00:00Z"}`, `{"Request_ID":"x","request_expires_at":"2026-10-23T00:00:00Z"}`, `{"request_id":"x","request_expires_at":null}`, `{"request_id":"x","request_expires_at":"2026-10-23T00:00:00Z","container_id":"foreign"}`} {
		var in HostingRuntimeLogOpen
		if DecodeHostingRuntimeLogsJSON([]byte(body), &in) == nil {
			t.Fatal("strict boundary accepted", body)
		}
	}
	batch.Events = []HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "nested boundary"}}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingRuntimeLogBatch
	if err = DecodeHostingRuntimeLogsJSON(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), `"stream":"stdout"`, `"stream":"stdout","stream":"stderr"`, 1)
	if bad == string(raw) {
		t.Fatal("nested duplicate fixture was not constructed")
	}
	if DecodeHostingRuntimeLogsJSON([]byte(bad), &decoded) == nil {
		t.Fatal("duplicate nested key accepted")
	}
}

func TestHostingRuntimeLogComponentSelection(t *testing.T) {
	base := HostingRuntimeLogOpen{RequestID: strings.Repeat("a", 32), RequestExpiresAt: time.Now()}
	for _, tc := range []struct {
		component, operation string
		valid                bool
	}{
		{valid: true}, {component: "app", operation: "hop_settled", valid: true},
		{component: "init", operation: "hop_settled", valid: true},
		{component: "app"}, {operation: "hop_settled"},
		{component: "../foreign", operation: "hop_settled"},
	} {
		input := base
		input.Component, input.ExpectedOperationID = tc.component, tc.operation
		_, err := input.Canonical()
		if (err == nil) != tc.valid {
			t.Fatalf("component %q operation %q: %v", tc.component, tc.operation, err)
		}
	}
}

func TestHostingRuntimeLogBatchSequenceBoundaryAndHash(t *testing.T) {
	batch := HostingRuntimeLogBatch{SessionID: "rls_test", InstanceID: strings.Repeat("a", 32), CollectionEpoch: strings.Repeat("b", 32), BatchID: strings.Repeat("c", 32), BatchSequence: 1, State: "following", Events: []HostingRuntimeLogEvent{}}
	first, err := batch.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	batch.BatchSequence = 2
	second, err := batch.Canonical()
	if err != nil || HostingDocumentHash(first) == HostingDocumentHash(second) {
		t.Fatal("counter excluded from canonical hash", err)
	}
	batch.BatchSequence = math.MaxInt32
	if _, err = batch.Canonical(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"missing", "null", "0", "-1", "2147483648", "1.5", `"1"`} {
		body := strings.Replace(string(raw), `"batch_sequence":1`, `"batch_sequence":`+value, 1)
		if value == "missing" {
			body = strings.Replace(string(raw), `"batch_sequence":1,`, "", 1)
		}
		var in HostingRuntimeLogBatch
		err := DecodeHostingRuntimeLogsJSON([]byte(body), &in)
		if err == nil {
			_, err = in.Canonical()
		}
		if err == nil {
			t.Fatal("invalid batch_sequence accepted", value)
		}
	}
}
