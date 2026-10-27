package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingObservationStorageValidation(t *testing.T) {
	valid := HostingObservationStorage{VolumeName: "ncdv_" + strings.Repeat("a", 32), MountPath: "/data"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*HostingObservationStorage){
		func(s *HostingObservationStorage) { s.VolumeName = "foreign" },
		func(s *HostingObservationStorage) { s.VolumeName = "ncdv_" + strings.Repeat("A", 32) },
		func(s *HostingObservationStorage) { s.VolumeName += ",type=bind" },
		func(s *HostingObservationStorage) { s.MountPath = "/" },
		func(s *HostingObservationStorage) { s.MountPath = "/proc/host" },
		func(s *HostingObservationStorage) { s.MountPath = "/data/../host" },
		func(s *HostingObservationStorage) { s.MountPath = "" },
	} {
		got := valid
		change(&got)
		if got.Validate() == nil {
			t.Fatalf("invalid mount authority accepted: %+v", got)
		}
	}
}

func TestHostingObservationClosedEvidence(t *testing.T) {
	valid := HostingRuntimeObservation{BindingID: "binding", SampleID: "sample", ContainerState: "running", Readiness: "ready"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*HostingRuntimeObservation){
		func(v *HostingRuntimeObservation) { v.SampleDurationMS = 5001 },
		func(v *HostingRuntimeObservation) { v.ContainerState = "healthy" },
		func(v *HostingRuntimeObservation) { v.CollectionError = "target_mismatch" },
		func(v *HostingRuntimeObservation) { v.ExitCode = new(int) },
		func(v *HostingRuntimeObservation) { v.Readiness = "http://example.com" },
		func(v *HostingRuntimeObservation) { v.ContainerState = "exited" },
	} {
		v := valid
		change(&v)
		if v.Validate() == nil {
			t.Fatalf("accepted invalid %+v", v)
		}
	}
	for _, state := range []string{"running", "exited", "missing", "unknown"} {
		v := valid
		v.ContainerState = state
		v.Readiness = "not_checked"
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	v := valid
	v.ContainerState = "unknown"
	v.Readiness = "unknown"
	v.CollectionError = "daemon_mismatch"
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHostingObservationJSONStrict(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"limit":1,"socket":"/var/run/docker.sock"}`, `{"limit":1,"Limit":2}`, `{"limit":1,"limit":2}`, `{"limit":null}`, `{"limit":1} {}`, `{"limit":1,"after_binding_id":"\ud800"}`, strings.Repeat(" ", 8193) + `{"limit":1}`} {
		var in HostingObservationTargetsInput
		if DecodeHostingObservationJSON([]byte(body), &in) == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	var in HostingObservationTargetsInput
	if err := DecodeHostingObservationJSON([]byte(`{"limit":50}`), &in); err != nil || in.Validate() != nil {
		t.Fatal(in, err)
	}
	var report HostingRuntimeObservation
	if err := DecodeHostingObservationJSON([]byte(`{"binding_id":"b","sample_id":"s","sample_duration_ms":0,"container_state":"missing","readiness":"not_checked","collection_error":""}`), &report); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > HostingObservationJSONLimit {
		t.Fatal(err)
	}
}
