package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHostingRuntimeLogClosedBoundedInput(t *testing.T) {
	open := HostingRuntimeLogOpen{RequestID: strings.Repeat("a", 32), RequestExpiresAt: time.Now()}
	if _, err := open.Canonical(); err != nil {
		t.Fatal(err)
	}
	batch := HostingRuntimeLogBatch{SessionID: "rls_test", InstanceID: strings.Repeat("b", 32), CollectionEpoch: strings.Repeat("c", 32), BatchID: strings.Repeat("d", 32), State: "following", Events: []HostingRuntimeLogEvent{{Component: "application", Stream: "stdout", Message: "hello"}}}
	if _, err := batch.Canonical(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*HostingRuntimeLogBatch){func(v *HostingRuntimeLogBatch) { v.Events = nil }, func(v *HostingRuntimeLogBatch) { v.State = "error: daemon failed" }, func(v *HostingRuntimeLogBatch) { v.Events[0].Stream = "system" }, func(v *HostingRuntimeLogBatch) { v.Events[0].Component = "build" }, func(v *HostingRuntimeLogBatch) {
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
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingRuntimeLogBatch
	if err = DecodeHostingRuntimeLogsJSON(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(raw), `"stream":"stdout"`, `"stream":"stdout","stream":"stderr"`, 1)
	if bad != string(raw) && DecodeHostingRuntimeLogsJSON([]byte(bad), &decoded) == nil {
		t.Fatal("duplicate nested key accepted")
	}
}
