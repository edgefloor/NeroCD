package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLocalImportUploadMetadataAndLegacyWire(t *testing.T) {
	input := HostingDatabaseImportUploadInput{RequestID: "request", Component: "mysql", ExpectedDeployedOperationID: "deployment", ExpectedResourceGeneration: 1, Filename: "literal ü.sql", SourceBytes: 42}
	if input.Validate() != nil {
		t.Fatal("valid metadata denied")
	}
	for _, name := range []string{"", "../x.sql", `x\y.sql`, "x\n.sql", string([]byte{0xff}), strings.Repeat("ü", 128)} {
		bad := input
		bad.Filename = name
		if bad.Validate() == nil {
			t.Fatal("invalid display name accepted")
		}
	}
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	stage := HostingDatabaseImportStage{ID: "stage", Version: HostingDatabaseImportUploadVersion, TargetResourceID: "resource", Component: "mysql", ExpectedDeployedOperationID: "deployment", ExpectedResourceGeneration: 1, State: "queued", CreatedAt: now, UpdatedAt: now, UploadSource: &HostingDatabaseImportUploadSource{Filename: input.Filename, Bytes: 42}, UploadHash: "sha256:" + strings.Repeat("a", 64), UploadDeadline: &deadline}
	if stage.Validate() != nil {
		t.Fatal("valid local metadata stage denied")
	}
	raw, err := json.Marshal(stage)
	if err != nil || strings.Contains(string(raw), "s3_source") || strings.Contains(string(raw), "source_identity") {
		t.Fatal("local stage fabricated S3 identity")
	}
	var roundtrip HostingDatabaseImportStage
	if json.Unmarshal(raw, &roundtrip) != nil || roundtrip.Validate() != nil {
		t.Fatal("local stage cannot replay")
	}
	stage.UploadDeadline = &now
	if stage.Validate() == nil {
		t.Fatal("invalid admission deadline")
	}
	legacy := HostingDatabaseImportStage{ID: "stage", Version: HostingDatabaseImportStageVersion, S3Source: HostingDatabaseImportS3Source{TargetID: "s3", Revision: 1, ObjectKey: "literal.sql"}}
	type oldWire struct {
		ID                          string                        `json:"id"`
		Version                     string                        `json:"version"`
		TargetResourceID            string                        `json:"target_resource_id"`
		Component                   string                        `json:"component"`
		ExpectedDeployedOperationID string                        `json:"expected_deployed_operation_id"`
		ExpectedResourceGeneration  int64                         `json:"expected_resource_generation"`
		S3Source                    HostingDatabaseImportS3Source `json:"s3_source"`
		State                       string                        `json:"state"`
		CreatedAt                   time.Time                     `json:"created_at"`
		UpdatedAt                   time.Time                     `json:"updated_at"`
	}
	expected, _ := json.Marshal(oldWire{ID: legacy.ID, Version: legacy.Version, S3Source: legacy.S3Source})
	got, _ := json.Marshal(legacy)
	if string(got) != string(expected) {
		t.Fatal("legacy S3 serialized bytes changed")
	}
}
