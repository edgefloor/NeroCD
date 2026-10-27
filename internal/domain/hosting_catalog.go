package domain

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// HostingCatalogLinkdingVersion identifies the catalog linkding document format.
const HostingCatalogLinkdingVersion = "coolify-b12d1386.linkding-1.47.0.v1"

// HostingCatalogLinkdingImage pins the Linkding catalog image by digest.
const HostingCatalogLinkdingImage = "docker.io/sissbruecker/linkding@sha256:e35cb50e0581178f245125ffaa909c565416c94c9f22ace305a7d234a4345522"

// HostingCatalogUptimeKumaVersion identifies the pinned Uptime Kuma catalog translation.
const HostingCatalogUptimeKumaVersion = "coolify-b12d1386.uptime-kuma-2.5.8.v1"

// HostingCatalogUptimeKumaImage pins the upstream v2 image index by digest.
const HostingCatalogUptimeKumaImage = "docker.io/louislam/uptime-kuma@sha256:20abea8c48a163cf0a7a5d5936ab418e4b742d908704e1f58f6c06e56a9c6b67"

// HostingCatalogActualBudgetVersion identifies the pinned Actual Budget translation.
const HostingCatalogActualBudgetVersion = "coolify-b12d1386.actualbudget-26.10.0.v1"

// HostingCatalogActualBudgetImage pins the Actual Budget image index by digest.
const HostingCatalogActualBudgetImage = "docker.io/actualbudget/actual-server@sha256:24645e971da6bb1a1f953d6859e307a0b29be1e8b4130a35b3220c209b5b860c"

//go:embed hosting_catalog_actualbudget.yaml
var hostingCatalogActualBudgetOriginal string

//go:embed hosting_catalog_uptime_kuma.yaml
var hostingCatalogUptimeKumaOriginal string

//go:embed hosting_catalog_linkding.yaml
var hostingCatalogLinkdingOriginal string

// HostingCatalogDefinition translates a pinned template into image or managed Compose hosting.
type HostingCatalogDefinition struct {
	ImageReference     string              `json:"image_reference"`
	ContainerPort      int                 `json:"container_port"`
	MountPath          string              `json:"mount_path"`
	Readiness          HostingReadiness    `json:"readiness"`
	CredentialNames    []string            `json:"credential_names"`
	RuntimeEnvironment map[string]string   `json:"runtime_environment,omitempty"`
	Compose            *HostingStackSource `json:"compose,omitempty"`
}

// HostingCatalogVikunjaVersion identifies the pinned managed Compose translation.
const HostingCatalogVikunjaVersion = "coolify-b12d1386.vikunja-2.7.1.v1"

//go:embed testdata/vikunja-with-postgresql.yaml
var hostingCatalogVikunjaOriginal string

//go:embed hosting_catalog_vikunja.yaml
var hostingCatalogVikunjaCompose string

// HostingCatalogTemplate describes a versioned catalog template and its resource definition.
// Category and architecture restrictions are pinned upstream declarations.
// Omitting absent metadata preserves historical installation hashes.
type HostingCatalogTemplate struct {
	ID                 string                   `json:"id"`
	Version            string                   `json:"immutable_version"`
	Name               string                   `json:"name"`
	Category           string                   `json:"category,omitempty"`
	AMDOnly            bool                     `json:"amd_only,omitempty"`
	ARMOnly            bool                     `json:"arm_only,omitempty"`
	UpstreamRepository string                   `json:"upstream_repository"`
	UpstreamCommit     string                   `json:"upstream_commit"`
	UpstreamPath       string                   `json:"upstream_path"`
	Original           string                   `json:"original"`
	OriginalHash       string                   `json:"original_hash"`
	Definition         HostingCatalogDefinition `json:"definition"`
	DefinitionHash     string                   `json:"definition_hash"`
}

// HostingCatalogTemplates returns fresh owned documents. Installed copies never refresh in place.
func HostingCatalogTemplates() []HostingCatalogTemplate {
	d := HostingCatalogDefinition{ImageReference: HostingCatalogLinkdingImage, ContainerPort: 9090, MountPath: "/etc/linkding/data", Readiness: HostingReadiness{Path: "/health", TimeoutSeconds: 120}, CredentialNames: []string{"LD_SUPERUSER_NAME", "LD_SUPERUSER_PASSWORD"}}
	sum := sha256.Sum256([]byte(hostingCatalogLinkdingOriginal))
	templates := []HostingCatalogTemplate{{ID: "linkding", Version: HostingCatalogLinkdingVersion, Name: "Linkding", Category: "RSS", UpstreamRepository: "https://github.com/coollabsio/coolify", UpstreamCommit: "b12d1386f3d458618b4c579babcd1e339c6c40b3", UpstreamPath: "templates/compose/linkding.yaml", Original: hostingCatalogLinkdingOriginal, OriginalHash: "sha256:" + hex.EncodeToString(sum[:]), Definition: d, DefinitionHash: HostingDocumentHash(d)}}
	kuma := HostingCatalogDefinition{ImageReference: HostingCatalogUptimeKumaImage, ContainerPort: 3001, MountPath: "/app/data", Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 120}, CredentialNames: []string{}}
	kumaSum := sha256.Sum256([]byte(hostingCatalogUptimeKumaOriginal))
	templates = append(templates, HostingCatalogTemplate{ID: "uptime-kuma", Version: HostingCatalogUptimeKumaVersion, Name: "Uptime Kuma", Category: "monitoring", UpstreamRepository: "https://github.com/coollabsio/coolify", UpstreamCommit: "b12d1386f3d458618b4c579babcd1e339c6c40b3", UpstreamPath: "templates/compose/uptime-kuma.yaml", Original: hostingCatalogUptimeKumaOriginal, OriginalHash: "sha256:" + hex.EncodeToString(kumaSum[:]), Definition: kuma, DefinitionHash: HostingDocumentHash(kuma)})
	actual := HostingCatalogDefinition{ImageReference: HostingCatalogActualBudgetImage, ContainerPort: 5006, MountPath: "/data", Readiness: HostingReadiness{Path: "/health", TimeoutSeconds: 120}, CredentialNames: []string{}, RuntimeEnvironment: map[string]string{"ACTUAL_LOGIN_METHOD": "password"}}
	actualSum := sha256.Sum256([]byte(hostingCatalogActualBudgetOriginal))
	templates = append(templates, HostingCatalogTemplate{ID: "actualbudget", Version: HostingCatalogActualBudgetVersion, Name: "Actual Budget", Category: "finance", UpstreamRepository: "https://github.com/coollabsio/coolify", UpstreamCommit: "b12d1386f3d458618b4c579babcd1e339c6c40b3", UpstreamPath: "templates/compose/actualbudget.yaml", Original: hostingCatalogActualBudgetOriginal, OriginalHash: "sha256:" + hex.EncodeToString(actualSum[:]), Definition: actual, DefinitionHash: HostingDocumentHash(actual)})
	vikunja := HostingCatalogDefinition{ContainerPort: 3456, Readiness: HostingReadiness{Path: "/health", TimeoutSeconds: 120}, CredentialNames: []string{"SERVICE_PASSWORD_JWTSECRET", "SERVICE_PASSWORD_POSTGRESQL"}, RuntimeEnvironment: map[string]string{"SERVICE_USER_POSTGRESQL": "vikunja", "POSTGRESQL_DATABASE": "vikunja"}, Compose: &HostingStackSource{Original: hostingCatalogVikunjaCompose, PublicComponent: "vikunja", ContainerPort: 3456}}
	vikunjaSum := sha256.Sum256([]byte(hostingCatalogVikunjaOriginal))
	return append(templates, HostingCatalogTemplate{ID: "vikunja-with-postgresql", Version: HostingCatalogVikunjaVersion, Name: "Vikunja with PostgreSQL", Category: "productivity", UpstreamRepository: "https://github.com/coollabsio/coolify", UpstreamCommit: "b12d1386f3d458618b4c579babcd1e339c6c40b3", UpstreamPath: "templates/compose/vikunja-with-postgresql.yaml", Original: hostingCatalogVikunjaOriginal, OriginalHash: "sha256:" + hex.EncodeToString(vikunjaSum[:]), Definition: vikunja, DefinitionHash: HostingDocumentHash(vikunja)})
}

// LookupHostingCatalogTemplate selects one exact current template version.
// Historical install replay uses its saved copy rather than this live catalog.
func LookupHostingCatalogTemplate(id, version string) (HostingCatalogTemplate, bool) {
	for _, template := range HostingCatalogTemplates() {
		if template.ID == id && template.Version == version {
			return template, true
		}
	}
	return HostingCatalogTemplate{}, false
}

// HostingCatalogInstallInput describes the requested catalog install.
type HostingCatalogInstallInput struct {
	RequestID            string `json:"request_id"`
	ProjectID            string `json:"project_id"`
	ProjectEnvironmentID string `json:"project_environment_id"`
	DestinationID        string `json:"destination_id"`
	Name                 string `json:"name"`
	TemplateID           string `json:"template_id"`
	ImmutableVersion     string `json:"immutable_version"`
	PublicURL            string `json:"public_url,omitempty"`
}

// ValidateShape is independent of current catalog contents so installed requests
// remain decodable and exactly replayable after a catalog refresh.
func (in HostingCatalogInstallInput) ValidateShape() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || !hostingProxyID.MatchString(in.ProjectID) || !hostingProxyID.MatchString(in.ProjectEnvironmentID) || !hostingProxyID.MatchString(in.DestinationID) || ValidateHostingName(in.Name) != nil || !validRuntimeReferenceID(in.TemplateID) || !validRuntimeReferenceID(in.ImmutableVersion) {
		return errors.New("invalid catalog install input")
	}
	if in.PublicURL != "" && (ValidateBackupRemoteEndpoint(in.PublicURL) != nil || strings.ContainsAny(in.PublicURL, "\x00\r\n\\?#")) {
		return errors.New("invalid catalog public URL")
	}
	return nil
}

// Validate checks the catalog install input fields and invariants.
func (in HostingCatalogInstallInput) Validate() error {
	if err := in.ValidateShape(); err != nil {
		return err
	}
	template, ok := LookupHostingCatalogTemplate(in.TemplateID, in.ImmutableVersion)
	if !ok {
		return errors.New("catalog template is unavailable; arbitrary Compose import is incomplete")
	}
	if (template.Definition.Compose != nil) != (in.PublicURL != "") {
		return errors.New("public URL is required only for Compose catalog applications")
	}
	return nil
}

// UnmarshalJSON decodes the catalog install input document.
func (in *HostingCatalogInstallInput) UnmarshalJSON(raw []byte) error {
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) {
		return errors.New("invalid catalog install input")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return errors.New("invalid catalog install input")
	}
	seen := map[string]bool{}
	for d.More() {
		t, err = d.Token()
		k, ok := t.(string)
		if err != nil || !ok || seen[k] {
			return errors.New("invalid catalog install input")
		}
		switch k {
		case "request_id", "project_id", "project_environment_id", "destination_id", "name", "template_id", "immutable_version", "public_url":
		default:
			return errors.New("invalid catalog install input")
		}
		seen[k] = true
		var value string
		if d.Decode(&value) != nil || value == "" {
			return errors.New("invalid catalog install input")
		}
	}
	requiredCount := len(seen)
	if seen["public_url"] {
		requiredCount--
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') || requiredCount != 7 || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid catalog install input")
	}
	type wire HostingCatalogInstallInput
	var v wire
	if strictSourceJSON(raw, &v) != nil {
		return errors.New("invalid catalog install input")
	}
	*in = HostingCatalogInstallInput(v)
	return in.ValidateShape()
}

// HostingCatalogInstallation retains original attribution and component references.
// Credentials are revealed only through the ordinary managed-secret reveal API.
type HostingCatalogInstallation struct {
	ID                   string                         `json:"id"`
	Request              HostingCatalogInstallInput     `json:"request"`
	Template             HostingCatalogTemplate         `json:"template"`
	Resource             Resource                       `json:"resource"`
	Binding              ResourceBinding                `json:"binding"`
	StorageRef           HostingStorageRef              `json:"storage_ref"`
	RuntimeConfiguration HostingRuntimeConfigurationRef `json:"runtime_configuration"`
	Credentials          []HostingRuntimeEntry          `json:"credentials"`
	CreatedBy            string                         `json:"created_by"`
	CreatedAt            time.Time                      `json:"created_at"`
}

// HostingCatalogResourceID derives the stable resource identity for an installation request.
func HostingCatalogResourceID(project, request string) string {
	return "res_catalog_" + HostingDocumentHash(struct{ Project, Request string }{project, request})[7:39]
}

// HostingCatalogSecretID derives a credential identity within a catalog resource.
func HostingCatalogSecretID(resource, name string) string {
	return "msec_catalog_" + HostingDocumentHash(struct{ Resource, Name string }{resource, name})[7:39]
}
