package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func logBatch() HostingBuildLogBatch {
	return HostingBuildLogBatch{Authority: HostingBuildAuthority{BuildID: "a", RunID: "build_run_a", LeaseID: "lease", Attempt: 1, Fence: "fence"}, Events: []HostingBuildLogEvent{{EventKey: "key", Sequence: 1, Component: "build", Stream: "stdout", Message: "hello"}}}
}
func TestHostingBuildLogCanonicalIdentity(t *testing.T) {
	in := logBatch()
	ts := time.Date(2026, 1, 1, 2, 3, 4, 123456789, time.FixedZone("offset", 3600))
	in.Events[0].SourceTimestamp = &ts
	got, err := CanonicalHostingBuildLogs(in)
	if err != nil {
		t.Fatal(err)
	}
	want := ts.UTC().Truncate(time.Microsecond)
	if *got.Events[0].SourceTimestamp != want || ts.Nanosecond() != 123456789 {
		t.Fatal("timestamp canonicalization mutated input")
	}
	hash := HostingBuildLogBodyHash(got.Authority, got.Events[0])
	copy := got
	copy.Events = append([]HostingBuildLogEvent(nil), got.Events...)
	copy.Events[0].Component = "buildkit"
	if hash == HostingBuildLogBodyHash(copy.Authority, copy.Events[0]) {
		t.Fatal("component not bound")
	}
	copy.Authority.Fence = "other"
	if hash == HostingBuildLogBodyHash(copy.Authority, got.Events[0]) {
		t.Fatal("authority not bound")
	}
}
func TestHostingBuildLogBounds(t *testing.T) {
	for _, mutate := range []func(*HostingBuildLogBatch){func(b *HostingBuildLogBatch) { b.Events = nil }, func(b *HostingBuildLogBatch) { b.Events[0].Sequence = math.MaxInt32 + 1 }, func(b *HostingBuildLogBatch) {
		b.Events[0].Message = strings.Repeat("a", HostingBuildLogMessageLimit+1)
	}, func(b *HostingBuildLogBatch) { b.Events[0].EventKey = "bad key" }, func(b *HostingBuildLogBatch) { b.Events[0].Component = "shell" }, func(b *HostingBuildLogBatch) { b.Events[0].Message = string([]byte{255}) }, func(b *HostingBuildLogBatch) { b.Events = append(b.Events, b.Events[0]) }, func(b *HostingBuildLogBatch) {
		for i := 0; i < 20; i++ {
			b.Events = append(b.Events, HostingBuildLogEvent{EventKey: string(rune('A' + i)), Sequence: 1, Component: "build", Stream: "stdout", Message: strings.Repeat("<", 16000)})
		}
	}} {
		b := logBatch()
		mutate(&b)
		if _, err := CanonicalHostingBuildLogs(b); err == nil {
			t.Fatalf("accepted %+v", b.Authority)
		}
	}
}
func TestRunLogOptionalFieldsPreserveLegacyJSON(t *testing.T) {
	b, err := json.Marshal(RunLog{})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"id":"","run_id":"","sequence":0,"stream":"","message":"","created_at":"0001-01-01T00:00:00Z"}` {
		t.Fatalf("legacy JSON bytes changed: %s", b)
	}
	if strings.Contains(string(b), "component") || strings.Contains(string(b), "source_timestamp") {
		t.Fatalf("legacy JSON changed: %s", b)
	}
}
func TestContainsHostingBuildExecution(t *testing.T) {
	w := &Workflow{}
	w.Steps = []WorkflowStep{{RunSpec: RunSpec{Workflow: w}}, {RunSpec: RunSpec{Type: "shell", HostingBuildID: "forged"}}}
	if !ContainsHostingBuildExecution(RunSpec{Workflow: w}, Workflow{}) {
		t.Fatal("nested build field accepted")
	}
	if ContainsHostingBuildExecution(RunSpec{Type: RunTypeImageDeploy}, Workflow{}) {
		t.Fatal("image logs denied")
	}
}
