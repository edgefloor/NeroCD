package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingCatalogPostgresWordPressCredentialsURLAndReplay(t *testing.T) {
	pg, database, service, ctx, base := catalogDatabaseFixture(t)
	in := domain.HostingCatalogInstallInput{RequestID: strings.Repeat("7", 32), ProjectID: base.ProjectID, ProjectEnvironmentID: base.ProjectEnvironmentID, DestinationID: base.DestinationID, Name: "WordPress blog", TemplateID: "wordpress-with-mysql", ImmutableVersion: domain.HostingCatalogWordPressVersion, PublicURL: "https://blog.example.test"}
	if _, err := database.Exec(ctx, `INSERT INTO service_accounts(id,project_id,owner_user_id,name,role,status) VALUES('sa_wordpress_viewer',$1,'usr_bootstrap','viewer','viewer','active')`, in.ProjectID); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "sa_wordpress_viewer", Provider: domain.PrincipalServiceAccount, Roles: []string{domain.RoleViewer}})
	before := hostingDatabaseCounts(t, database)
	if _, err := service.InstallHostingCatalog(viewer, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer installed WordPress", err)
	}
	if _, err := database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$1)`, domain.HostingStackCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := service.InstallHostingCatalog(ctx, in); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old runner admitted WordPress", err)
	}
	if hostingDatabaseCounts(t, database) != before {
		t.Fatal("denied WordPress install persisted components")
	}
	if _, err := database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$1)`, domain.HostingStackCapability); err != nil {
		t.Fatal(err)
	}
	var views [3]domain.HostingCatalogInstallation
	var failures [3]error
	var wg sync.WaitGroup
	for i := range views {
		wg.Go(func() { views[i], failures[i] = service.InstallHostingCatalog(ctx, in) })
	}
	wg.Wait()
	installed := views[0]
	for i, err := range failures {
		if err != nil || domain.HostingDocumentHash(views[i]) != domain.HostingDocumentHash(installed) {
			t.Fatal("concurrent WordPress install changed receipt", err)
		}
	}
	if installed.Resource.Source.Kind != "compose" || installed.Resource.Source.Compose == nil || *installed.Resource.Source.Compose != *installed.Template.Definition.Compose || installed.StorageRef != (domain.HostingStorageRef{}) || len(installed.Credentials) != 3 {
		t.Fatal("WordPress did not use ordinary managed Compose")
	}
	runtime, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_bootstrap", installed.Resource.ID)
	if err != nil || len(runtime.Entries) != 4 || domain.HostingDocumentHash(domain.HostingRuntimeDocument{Entries: runtime.Entries}) != installed.RuntimeConfiguration.Hash {
		t.Fatal("WordPress runtime document disconnected", err)
	}
	body, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	refs := map[string]string{}
	for _, entry := range runtime.Entries {
		if entry.Kind == "literal" {
			if entry.Name != "SERVICE_URL_WORDPRESS" || *entry.Value != in.PublicURL {
				t.Fatal("wrong public URL variable")
			}
			values[entry.Name] = *entry.Value
			continue
		}
		if entry.Secret == nil || entry.Secret.Version != 1 || entry.Secret.SecretID != domain.HostingCatalogSecretID(installed.Resource.ID, entry.Name) {
			t.Fatal("unpinned WordPress credential")
		}
		revealed, err := service.RevealManagedSecret(ctx, entry.Secret.SecretID, app.ManagedSecretRevealInput{Version: 1})
		if err != nil || strings.Contains(string(body), revealed.Value) {
			t.Fatal("generated credential unavailable or exposed", err)
		}
		if entry.Name == "SERVICE_USER_WORDPRESS" {
			if len(revealed.Value) != 20 || !regexp.MustCompile(`^ncd_[0-9a-f]{16}$`).MatchString(revealed.Value) {
				t.Fatal("MySQL username violates actual generation policy")
			}
		} else if len(revealed.Value) != 43 || strings.Trim(revealed.Value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
			t.Fatal("password generation bounds changed")
		}
		record, err := pg.GetManagedSecretRecord(ctx, entry.Secret.SecretID, 1)
		if err != nil || record.Envelope.Version != 1 || len(record.Envelope.Data) <= len(revealed.Value) || bytes.Contains(record.Envelope.Data, []byte(revealed.Value)) {
			t.Fatal("encrypted credential unavailable", err)
		}
		values[entry.Name] = revealed.Value
		refs[entry.Name] = entry.Secret.SecretID
	}
	if values["SERVICE_URL_WORDPRESS"] != in.PublicURL || values["SERVICE_PASSWORD_WORDPRESS"] == values["SERVICE_PASSWORD_ROOT"] || refs["SERVICE_PASSWORD_WORDPRESS"] == refs["SERVICE_PASSWORD_ROOT"] {
		t.Fatal("WordPress URL or separate password identity changed")
	}
	definition, err := domain.ParseHostingStack(*installed.Resource.Source.Compose)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range definition.Services {
		for _, expression := range component.Environment {
			if _, err := domain.ExpandHostingStackValue(expression, values, true); err != nil {
				t.Fatal("required WordPress environment is absent", err)
			}
		}
	}
	// Managed references never expose their generated username or password values.
	for _, entry := range installed.Credentials {
		if _, err := service.RevealManagedSecret(viewer, entry.Secret.SecretID, app.ManagedSecretRevealInput{Version: 1}); !errors.Is(err, auth.ErrForbidden) {
			t.Fatal("viewer revealed WordPress credentials", err)
		}
	}
	replay, err := service.InstallHostingCatalog(ctx, in)
	if err != nil || domain.HostingDocumentHash(replay) != domain.HostingDocumentHash(installed) {
		t.Fatal("immutable WordPress retry changed receipt", err)
	}
	got, err := service.GetHostingCatalogInstallation(ctx, installed.Resource.ID)
	if err != nil || domain.HostingDocumentHash(got) != domain.HostingDocumentHash(installed) {
		t.Fatal("persisted WordPress receipt changed", err)
	}
	changed := in
	changed.PublicURL = "https://other.example.test"
	if _, err := service.InstallHostingCatalog(ctx, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("retry replaced public URL", err)
	}
	requirements := domain.CatalogRequirements(installed.Template)
	if !slices.Contains(requirements.RequiredCapabilities, domain.HostingStackCapability) || !requirements.PublicURLRequired {
		t.Fatal("WordPress capability requirements changed")
	}
	deploy := hostingDeploymentRequest()
	deploy.ContainerPort = 80
	deploy.Readiness = installed.Template.Definition.Readiness
	deploy.ExpectedRuntimeConfigurationGeneration = 1
	deploy.ManagedStack = &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}
	op, err := pg.RequestHostingDeployment(ctx, "hop_wordpress_catalog", installed.Resource.ID, "usr_bootstrap", deploy, destinationEvent("wordpress_catalog"))
	if err != nil || op.Snapshot.RuntimeConfiguration != installed.RuntimeConfiguration || op.Snapshot.Stack == (domain.HostingStackRef{}) {
		t.Fatal("WordPress ordinary deployment lost exact runtime/source", err)
	}
}
