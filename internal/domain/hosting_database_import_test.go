package domain

import (
	"strings"
	"testing"
	"time"
)

func TestExternalDatabaseImportRequestsAreTargetBoundAndLiteral(t *testing.T) {
	in := HostingDatabaseImportStageInput{RequestID: "inspect_1", Component: "mysql", ExpectedDeployedOperationID: "operation_1", ExpectedResourceGeneration: 1, S3Source: HostingDatabaseImportS3Source{TargetID: "target_1", Revision: 2, ObjectKey: "imports/SQL literal ü%2f file.sql"}}
	if in.Validate() != nil {
		t.Fatal("literal UTF-8 key rejected")
	}
	original := in.S3Source.ObjectKey
	for name, mutate := range map[string]func(*HostingDatabaseImportStageInput){
		"missing target revision": func(v *HostingDatabaseImportStageInput) { v.S3Source.Revision = 0 },
		"wrong engine":            func(v *HostingDatabaseImportStageInput) { v.Component = "postgres" },
		"missing deployment":      func(v *HostingDatabaseImportStageInput) { v.ExpectedDeployedOperationID = "" },
		"control key":             func(v *HostingDatabaseImportStageInput) { v.S3Source.ObjectKey = "file\n.sql" },
		"oversize UTF8 bytes":     func(v *HostingDatabaseImportStageInput) { v.S3Source.ObjectKey = strings.Repeat("ü", 513) },
		"invalid UTF8":            func(v *HostingDatabaseImportStageInput) { v.S3Source.ObjectKey = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid inspection admitted")
			}
		})
	}
	if in.S3Source.ObjectKey != original {
		t.Fatal("validation normalized object key")
	}
	execute := HostingDatabaseImportInput{RequestID: "execute_1", StageID: "stage_1", ExpectedStageHash: "sha256:" + strings.Repeat("a", 64), ComponentTarget: HostingDatabaseBackupComponentTarget{ResourceID: "resource_1", Component: "mysql", ExpectedDeployedOperationID: "operation_1", ExpectedResourceGeneration: 1, AcknowledgeReplacement: true}}
	if execute.Validate() != nil {
		t.Fatal("qualified confirmation rejected")
	}
	execute.ComponentTarget.AcknowledgeReplacement = false
	if execute.Validate() == nil {
		t.Fatal("missing confirmation admitted")
	}
}

func TestExternalDatabaseStageHistoricalStateDoesNotApplyLiveExpiry(t *testing.T) {
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := now.Add(HostingDatabaseImportStageTTL)
	stage := HostingDatabaseImportStage{ID: "stage_1", Version: HostingDatabaseImportStageVersion, TargetResourceID: "resource_1", Component: "mysql", ExpectedDeployedOperationID: "operation_1", ExpectedResourceGeneration: 1, S3Source: HostingDatabaseImportS3Source{TargetID: "target_1", Revision: 1, ObjectKey: "literal.sql"}, State: "ready", SourceBytes: 100, Compression: "gzip", StagedBlob: &HostingDatabaseImportBlob{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 1024, Format: "mysql_sql"}, StageHash: "sha256:" + strings.Repeat("c", 64), ExpiresAt: &expires, CreatedAt: now, UpdatedAt: now}
	for _, state := range []string{"ready", "expired", "consumed"} {
		stage.State = state
		if stage.Validate() != nil {
			t.Fatalf("historical %s became unreadable", state)
		}
	}
	stage.SourceIdentity = &HostingDatabaseImportSourceIdentity{}
	if stage.Validate() == nil {
		t.Fatal("invented empty provider identity accepted")
	}
	stage.SourceIdentity = nil
	stage.StagedBlob.Bytes = HostingDatabaseImportMaxBytes + 1
	if stage.Validate() == nil {
		t.Fatal("decoded size limit bypassed")
	}
}

func TestExternalDatabaseImportUncertainCannotResumeWriters(t *testing.T) {
	now := time.Now().UTC()
	operation := HostingDatabaseImportOperation{ID: "import_1", Version: HostingDatabaseImportVersion, StageID: "stage_1", TargetResourceID: "resource_1", TargetComponent: "mysql", TargetDeployedOperationID: "operation_1", State: "uncertain", OwnershipHeld: true, FailureCode: "execution_failed", CreatedAt: now, UpdatedAt: now}
	if operation.Validate() != nil {
		t.Fatal("held uncertainty rejected")
	}
	operation.WritersResumed = true
	if operation.Validate() == nil {
		t.Fatal("uncertain import claimed writer resumption")
	}
}
