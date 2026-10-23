package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func qualifyingReadiness() ServerReadinessReport {
	return ServerReadinessReport{SampleID: "srd_" + strings.Repeat("a", 32), Profile: ServerReadinessProfile, DurationMS: 1, OS: "linux", Architecture: "amd64", Execution: "enabled", Configuration: "matched", Docker: "available", DockerAPIVersion: "1.54", DaemonID: "daemon", DaemonOS: "linux", DaemonArchitecture: "amd64", CLI: "available", WorkRoot: "accessible", JournalRoot: "accessible", WorkFreeBytes: 1 << 30, JournalFreeBytes: 1 << 30}
}

func TestServerReadinessQualification(t *testing.T) {
	if !qualifyingReadiness().Qualifies() {
		t.Fatal("valid fixture blocked")
	}
	for _, change := range []func(*ServerReadinessReport){
		func(v *ServerReadinessReport) { v.OS = "unsupported" }, func(v *ServerReadinessReport) { v.Architecture = "unsupported" }, func(v *ServerReadinessReport) { v.DaemonArchitecture = "arm64" }, func(v *ServerReadinessReport) { v.Execution = "disabled" }, func(v *ServerReadinessReport) { v.Configuration = "mismatch" }, func(v *ServerReadinessReport) { v.Docker = "unreachable" }, func(v *ServerReadinessReport) { v.DockerAPIVersion = "1.55" }, func(v *ServerReadinessReport) { v.DaemonID = "" }, func(v *ServerReadinessReport) { v.CLI = "unsafe" }, func(v *ServerReadinessReport) { v.WorkRoot = "unsafe" }, func(v *ServerReadinessReport) { v.JournalRoot = "inaccessible" }, func(v *ServerReadinessReport) { v.WorkFreeBytes-- }, func(v *ServerReadinessReport) { v.JournalFreeBytes-- }, func(v *ServerReadinessReport) { v.Error = "timeout" },
	} {
		v := qualifyingReadiness()
		change(&v)
		if v.Validate() != nil || v.Qualifies() {
			t.Fatalf("qualification %+v", v)
		}
	}
	v := qualifyingReadiness()
	v.Architecture = "arm64"
	v.DaemonArchitecture = "arm64"
	if !v.Qualifies() {
		t.Fatal("native arm64 blocked")
	}
}

func TestServerReadinessStrictJSONAndBounds(t *testing.T) {
	raw, err := json.Marshal(qualifyingReadiness())
	if err != nil {
		t.Fatal(err)
	}
	var out ServerReadinessReport
	if err = DecodeServerReadinessJSON(raw, &out); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`null`, `[]`, `{}`, string(raw) + ` {}`, strings.Replace(string(raw), `"profile":`, `"profile":"basic_image_hosting.v1","profile":`, 1), strings.Replace(string(raw), `"profile":`, `"Profile":`, 1), strings.Replace(string(raw), `"duration_ms":1`, `"duration_ms":null`, 1), strings.Replace(string(raw), `"duration_ms":1`, `"duration_ms":1.5`, 1), strings.Replace(string(raw), `"work_free_bytes":1073741824`, `"work_free_bytes":9223372036854775808`, 1), strings.Repeat(" ", 8193) + string(raw)} {
		if DecodeServerReadinessJSON([]byte(body), &out) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, change := range []func(*ServerReadinessReport){func(v *ServerReadinessReport) { v.DurationMS = 5001 }, func(v *ServerReadinessReport) { v.WorkFreeBytes = -1 }, func(v *ServerReadinessReport) { v.JournalFreeBytes = -1 }, func(v *ServerReadinessReport) { v.DaemonID = strings.Repeat("a", 129) }, func(v *ServerReadinessReport) { v.DaemonID = "a\nb" }, func(v *ServerReadinessReport) { v.DockerAPIVersion = "https://daemon" }, func(v *ServerReadinessReport) { v.Error = "stderr" }} {
		v := qualifyingReadiness()
		change(&v)
		if v.Validate() == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
	var empty struct{}
	if DecodeServerReadinessJSON([]byte(`{}`), &empty) != nil || DecodeServerReadinessJSON([]byte(`{"server_id":"client"}`), &empty) == nil {
		t.Fatal("challenge fields")
	}
}
