package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestHostingCatalogWordPressPinnedSourceAndRuntimeContract(t *testing.T) {
	entry, ok := LookupHostingCatalogTemplate("wordpress-with-mysql", HostingCatalogWordPressVersion)
	if !ok || entry.Category != "cms" || entry.Definition.Compose == nil || entry.Definition.ContainerPort != 80 || entry.Definition.Readiness != (HostingReadiness{Path: "/", TimeoutSeconds: 120}) || entry.Definition.PublicURLRuntimeVariable() != "SERVICE_URL_WORDPRESS" {
		t.Fatal("WordPress catalog profile differs")
	}
	original, err := os.ReadFile("testdata/wordpress-with-mysql.yaml")
	hash := sha256.Sum256(original)
	if err != nil || entry.Original != string(original) || entry.OriginalHash != "sha256:"+hex.EncodeToString(hash[:]) || entry.OriginalHash != "sha256:819478592769b7b93795474dd4c1544899b1d548ddcd26d1f42c6578ff19905f" || entry.UpstreamPath != "templates/compose/wordpress-with-mysql.yaml" {
		t.Fatal("pinned upstream bytes changed", err)
	}
	translated, err := os.ReadFile("hosting_catalog_wordpress.yaml")
	if err != nil || string(translated) != entry.Definition.Compose.Original || entry.DefinitionHash != HostingDocumentHash(entry.Definition) {
		t.Fatal("translated source differs", err)
	}
	parsed, err := ParseHostingStack(*entry.Definition.Compose)
	if err != nil || parsed.PublicComponent != "wordpress" || parsed.ContainerPort != 80 || len(parsed.Services) != 2 || !slices.Equal(parsed.Volumes, []string{"mysql-data", "wordpress-files"}) {
		t.Fatal("invalid managed WordPress Compose", err)
	}
	services := map[string]HostingStackService{}
	for _, service := range parsed.Services {
		services[service.Name] = service
	}
	wordpress, mysql := services["wordpress"], services["mysql"]
	if wordpress.Image != "docker.io/library/wordpress:7.1.3-php8.3-apache@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834" || mysql.Image != "docker.io/library/mysql:8.4.13-oraclelinux9@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7" || wordpress.DependsOn["mysql"] != "service_healthy" {
		t.Fatal("images or DB readiness dependency changed")
	}
	if !slices.Equal(wordpress.Mounts, []HostingStackMount{{Volume: "wordpress-files", Target: "/var/www/html"}}) || !slices.Equal(mysql.Mounts, []HostingStackMount{{Volume: "mysql-data", Target: "/var/lib/mysql"}}) {
		t.Fatal("upstream data mounts changed")
	}
	if wordpress.Healthcheck == nil || !slices.Equal(wordpress.Healthcheck.Test, []string{"CMD", "curl", "-f", "http://127.0.0.1"}) || mysql.Healthcheck == nil || !slices.Equal(mysql.Healthcheck.Test, []string{"CMD", "mysqladmin", "ping", "-h", "127.0.0.1"}) {
		t.Fatal("upstream health commands changed")
	}
	values := map[string]string{"SERVICE_URL_WORDPRESS": "https://blog.example.test", "SERVICE_USER_WORDPRESS": "ncd_0123456789abcdef", "SERVICE_PASSWORD_WORDPRESS": "app-secret", "SERVICE_PASSWORD_ROOT": "independent-root-secret"}
	for _, service := range parsed.Services {
		for _, value := range service.Environment {
			if _, err := ExpandHostingStackValue(value, values, true); err != nil {
				t.Fatal("catalog environment cannot expand", err)
			}
		}
	}
	if wordpress.Environment["WORDPRESS_DB_NAME"] != "wordpress" || mysql.Environment["MYSQL_DATABASE"] != "wordpress" || wordpress.Environment["WORDPRESS_DB_HOST"] != "mysql" {
		t.Fatal("upstream application connection profile changed")
	}
	requirements := CatalogRequirements(entry)
	if !requirements.PublicURLRequired || requirements.SourceKind != "compose" || !slices.Equal(requirements.MountPaths, []string{"/var/lib/mysql", "/var/www/html"}) || !slices.Equal(requirements.ManagedCredentialNames, []string{"SERVICE_USER_WORDPRESS", "SERVICE_PASSWORD_WORDPRESS", "SERVICE_PASSWORD_ROOT"}) {
		t.Fatal("catalog requirements disconnected")
	}
	entry.Definition.Compose.Original = "changed"
	entry.Definition.CredentialNames[0] = "changed"
	fresh, _ := LookupHostingCatalogTemplate(entry.ID, entry.Version)
	if fresh.Definition.Compose.Original != string(translated) || fresh.Definition.CredentialNames[0] != "SERVICE_USER_WORDPRESS" {
		t.Fatal("shared mutable catalog definition")
	}
}

func TestHostingCatalogWordPressRealDecoderAndHistoricalURLMetadata(t *testing.T) {
	in := HostingCatalogInstallInput{RequestID: strings.Repeat("b", 32), ProjectID: "project", ProjectEnvironmentID: "environment", DestinationID: "destination", Name: "Blog", TemplateID: "wordpress-with-mysql", ImmutableVersion: HostingCatalogWordPressVersion, PublicURL: "https://blog.example.test"}
	raw, err := json.Marshal(in)
	var decoded HostingCatalogInstallInput
	if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded != in || decoded.Validate() != nil {
		t.Fatal("real WordPress decoder rejected request", err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"public_url":"https://blog.example.test"`, `"public_url":"https://blog.example.test","public_url":"https://other.test"`, 1), strings.TrimSuffix(string(raw), "}") + `,"password":"caller"}`, strings.Replace(string(raw), `"public_url":"https://blog.example.test"`, `"public_url":null`, 1)} {
		if json.Unmarshal([]byte(bad), &decoded) == nil {
			t.Fatal("WordPress decoder accepted ambiguous or caller credential fields")
		}
	}
	for _, change := range []func(*HostingCatalogInstallInput){func(v *HostingCatalogInstallInput) { v.PublicURL = "" }, func(v *HostingCatalogInstallInput) { v.ImmutableVersion = HostingCatalogVikunjaVersion }, func(v *HostingCatalogInstallInput) { v.PublicURL = "https://blog.example.test/private" }} {
		bad := in
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid WordPress install accepted")
		}
	}
	historical := map[string]string{"linkding": "sha256:7db2c1bf04209cdaab977f3f09d33d893cfb15e7879404dbe04c7e928a845be3", "uptime-kuma": "sha256:0bf1574847028a69e3cfaa26e7d7b24edd1ace8e0e9f80edd6a585267c939274", "actualbudget": "sha256:8dc4a6754a28f5959fc5a3956f54662fbdcaef0c9c151873f6d8b3e948b52cc1", "vikunja-with-postgresql": "sha256:b2c306ee344eb81dd17a1a45400109e4bcdeaf5d8a804eb23bd0afa0ee376451"}
	for _, entry := range HostingCatalogTemplates() {
		want, ok := historical[entry.ID]
		if !ok {
			continue
		}
		if HostingDocumentHash(entry) != want || entry.Definition.PublicURLVariable != "" {
			t.Fatal("accepted historical catalog hash changed", entry.ID)
		}
		encoded, err := json.Marshal(entry.Definition)
		if err != nil || strings.Contains(string(encoded), "public_url_variable") {
			t.Fatal("absent URL metadata changed historical encoding")
		}
		if entry.Definition.Compose != nil && entry.Definition.PublicURLRuntimeVariable() != "SERVICE_URL_VIKUNJA" {
			t.Fatal("historical Compose URL behavior changed")
		}
	}
}
