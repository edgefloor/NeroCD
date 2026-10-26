package domain

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const HostingCatalogLinkdingVersion = "coolify-b12d1386.linkding-1.47.0.v1"
const HostingCatalogLinkdingImage = "docker.io/sissbruecker/linkding@sha256:e35cb50e0581178f245125ffaa909c565416c94c9f22ace305a7d234a4345522"

//go:embed hosting_catalog_linkding.yaml
var hostingCatalogLinkdingOriginal string

// HostingCatalogDefinition is a closed typed translation into ordinary image hosting.
// It deliberately cannot carry arbitrary Compose fields or execution scripts.
type HostingCatalogDefinition struct {
	ImageReference  string           `json:"image_reference"`
	ContainerPort   int              `json:"container_port"`
	MountPath       string           `json:"mount_path"`
	Readiness       HostingReadiness `json:"readiness"`
	CredentialNames []string         `json:"credential_names"`
}

type HostingCatalogTemplate struct {
	ID                 string                   `json:"id"`
	Version            string                   `json:"immutable_version"`
	Name               string                   `json:"name"`
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
	return []HostingCatalogTemplate{{ID: "linkding", Version: HostingCatalogLinkdingVersion, Name: "Linkding", UpstreamRepository: "https://github.com/coollabsio/coolify", UpstreamCommit: "b12d1386f3d458618b4c579babcd1e339c6c40b3", UpstreamPath: "templates/compose/linkding.yaml", Original: hostingCatalogLinkdingOriginal, OriginalHash: "sha256:" + hex.EncodeToString(sum[:]), Definition: d, DefinitionHash: HostingDocumentHash(d)}}
}

type HostingCatalogInstallInput struct {
	RequestID            string `json:"request_id"`
	ProjectID            string `json:"project_id"`
	ProjectEnvironmentID string `json:"project_environment_id"`
	DestinationID        string `json:"destination_id"`
	Name                 string `json:"name"`
	TemplateID           string `json:"template_id"`
	ImmutableVersion     string `json:"immutable_version"`
}

// ValidateShape is independent of current catalog contents so installed requests
// remain decodable and exactly replayable after a catalog refresh.
func (in HostingCatalogInstallInput) ValidateShape() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || !hostingProxyID.MatchString(in.ProjectID) || !hostingProxyID.MatchString(in.ProjectEnvironmentID) || !hostingProxyID.MatchString(in.DestinationID) || ValidateHostingName(in.Name) != nil || !validRuntimeReferenceID(in.TemplateID) || !validRuntimeReferenceID(in.ImmutableVersion) {
		return errors.New("invalid catalog install input")
	}
	return nil
}
func (in HostingCatalogInstallInput) Validate() error {
	if err := in.ValidateShape(); err != nil {
		return err
	}
	if in.TemplateID != "linkding" || in.ImmutableVersion != HostingCatalogLinkdingVersion {
		return errors.New("catalog template is unavailable; arbitrary Compose import is incomplete")
	}
	return nil
}

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
		case "request_id", "project_id", "project_environment_id", "destination_id", "name", "template_id", "immutable_version":
		default:
			return errors.New("invalid catalog install input")
		}
		seen[k] = true
		var value string
		if d.Decode(&value) != nil || value == "" {
			return errors.New("invalid catalog install input")
		}
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') || len(seen) != 7 || d.Decode(new(any)) != io.EOF {
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

func HostingCatalogResourceID(project, request string) string {
	return "res_catalog_" + HostingDocumentHash(struct{ Project, Request string }{project, request})[7:39]
}
func HostingCatalogSecretID(resource, name string) string {
	return "msec_catalog_" + HostingDocumentHash(struct{ Resource, Name string }{resource, name})[7:39]
}
