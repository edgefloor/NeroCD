package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestWordPressCatalogFixtureRequestContract(t *testing.T) {
	input := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("c", 32), ProjectID: "proj_platform", ProjectEnvironmentID: "env_fixture", DestinationID: "dest_fixture", Name: "WordPress acceptance", TemplateID: "wordpress-with-mysql", ImmutableVersion: "coolify-b12d1386.wordpress-7.1.3-mysql-8.4.13.v1", PublicURL: "https://catalog-runtime.example.test:25443"}
	raw, _ := json.Marshal(input)
	var decoded domain.HostingCatalogInstallInput
	if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/hosting-catalog/installations", bytes.NewReader(raw)), &decoded) || !reflect.DeepEqual(input, decoded) || decoded.Validate() != nil {
		t.Fatal("WordPress fixture does not match public catalog decoder")
	}
}

func TestWordPressFixtureExactTemplateAndDeploymentContract(t *testing.T) {
	template, ok := domain.LookupHostingCatalogTemplate("wordpress-with-mysql", domain.HostingCatalogWordPressVersion)
	if !ok || template.Definition.Compose == nil {
		t.Fatal("WordPress template unavailable")
	}
	const image = "wordpress:7.1.3-php8.3-apache@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834"
	if strings.Count(template.Definition.Compose.Original, image) != 1 {
		t.Fatal("WordPress fault must replace exactly one parsed public image")
	}
	changed := *template.Definition.Compose
	changed.Original = strings.Replace(changed.Original, image, "127.0.0.1:1/wordpress@sha256:"+strings.Repeat("a", 64), 1)
	definition, err := domain.ParseHostingStack(changed)
	if err != nil || definition.PublicComponent != "wordpress" || definition.ContainerPort != 80 {
		t.Fatal("preparation-fault source invalid")
	}
	input := domain.HostingDeploymentInput{ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, ExpectedGeneration: 1, ExpectedRuntimeConfigurationGeneration: 1, IdempotencyKey: "wordpress-deploy", ContainerPort: 80, HostPort: 18080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}}
	for _, mode := range []string{"deploy", "replace", "start"} {
		if mode != "deploy" {
			input.StorageRouteContinuity = &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_fixture", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_fixture", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_previous"}}}
		}
		if mode == "start" {
			input.ManagedStack.ExpectedStoppedOperationID = "hop_stopped"
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded domain.HostingDeploymentInput
		if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_fixture/deployments", bytes.NewReader(raw)), &decoded) || domain.ValidateHostingDeployment(decoded) != nil || !reflect.DeepEqual(decoded, input) {
			t.Fatalf("WordPress%s decoder changed", mode)
		}
	}
}

func TestWordPressComponentDatabaseFixtureRequests(t *testing.T) {
	backup := domain.HostingDatabaseBackupInput{RequestID: strings.Repeat("a", 32), Component: "mysql", ExpectedResourceGeneration: 3, ExpectedDeployedOperationID: "hop_source"}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	var got domain.HostingDatabaseBackupInput
	if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_source/database-backups", bytes.NewReader(raw)), &got) || !reflect.DeepEqual(backup, got) || got.Validate() != nil {
		t.Fatal("MySQL component backup fixture decoder contract changed")
	}
	restore := domain.HostingDatabaseBackupRestoreInput{RequestID: strings.Repeat("b", 32), ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: "res_target", Component: "mysql", ExpectedDeployedOperationID: "hop_target", ExpectedResourceGeneration: 2, AcknowledgeReplacement: true}}
	for _, tc := range []struct {
		name   string
		change func(*domain.HostingDatabaseBackupRestoreInput)
		valid  bool
	}{
		{"exact distinct reviewed target", func(*domain.HostingDatabaseBackupRestoreInput) {}, true},
		{"missing destructive acknowledgement", func(in *domain.HostingDatabaseBackupRestoreInput) { in.ComponentTarget.AcknowledgeReplacement = false }, false},
		{"missing generation", func(in *domain.HostingDatabaseBackupRestoreInput) { in.ComponentTarget.ExpectedResourceGeneration = 0 }, false},
		{"missing deployed identity", func(in *domain.HostingDatabaseBackupRestoreInput) {
			in.ComponentTarget.ExpectedDeployedOperationID = ""
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := restore
			target := *restore.ComponentTarget
			in.ComponentTarget = &target
			tc.change(&in)
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var got domain.HostingDatabaseBackupRestoreInput
			if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/database-backups/dbb_fixture/restores", bytes.NewReader(raw)), &got) || !reflect.DeepEqual(in, got) || (got.Validate() == nil) != tc.valid {
				t.Fatal("MySQL import fixture public decoder/authority fields changed")
			}
		})
	}
}
