package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Database recipes pin desired images; engine execution remains unqualified.
const (
	HostingDatabasePostgresRecipe = "postgresql-17.11-bookworm.v1"
	HostingDatabaseRedisRecipe    = "redis-8.2.10-bookworm.v1"
	HostingDatabaseUnqualified    = "adapter_unqualified"
)

// HostingDatabaseCreateInput selects one server-owned immutable recipe.
type HostingDatabaseCreateInput struct {
	RequestID            string `json:"request_id"`
	ProjectID            string `json:"project_id"`
	ProjectEnvironmentID string `json:"project_environment_id"`
	Name                 string `json:"name"`
	DestinationID        string `json:"destination_id"`
	Engine               string `json:"engine"`
	RecipeVersion        string `json:"recipe_version"`
}

// HostingDatabaseCredentialRef identifies a generated encrypted credential.
// Plaintext is available only through the existing authorized reveal operation.
type HostingDatabaseCredentialRef struct {
	Role     string `json:"role"`
	Username string `json:"username"`
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
}

// HostingDatabase reports saved metadata, never a running engine or endpoint.
type HostingDatabase struct {
	Resource      Resource                       `json:"resource"`
	Binding       ResourceBinding                `json:"binding"`
	StorageRef    HostingStorageRef              `json:"storage_ref"`
	Engine        string                         `json:"engine"`
	RecipeVersion string                         `json:"recipe_version"`
	Credentials   []HostingDatabaseCredentialRef `json:"credentials"`
	RuntimeState  string                         `json:"runtime_state"`
}

// HostingDatabaseImagePlatform records an observed official manifest identity.
// Its presence does not qualify an engine adapter for execution.
type HostingDatabaseImagePlatform struct {
	OS           string
	Architecture string
	Variant      string
	Digest       string
}

// HostingDatabaseRecipe is server-owned metadata for later adapter qualification.
// Credentials hold role/user identities only, without secret IDs or plaintext.
type HostingDatabaseRecipe struct {
	Engine         string
	Version        string
	ImageReference string
	MountPath      string
	ContainerPort  int
	DatabaseName   string
	Credentials    []HostingDatabaseCredentialRef
	Platforms      []HostingDatabaseImagePlatform
}

// HostingDatabaseRecipeFor returns a fresh copy of one pinned official recipe.
func HostingDatabaseRecipeFor(engine, version string) (HostingDatabaseRecipe, error) {
	switch {
	case engine == "postgresql" && version == HostingDatabasePostgresRecipe:
		return HostingDatabaseRecipe{Engine: engine, Version: version, ImageReference: "docker.io/library/postgres@sha256:858bb5e98eed02eb8cafbcdc007f9a65d1b613972028944a8ec2133ccaf80d27", MountPath: "/var/lib/postgresql/data", ContainerPort: 5432, DatabaseName: "app", Credentials: []HostingDatabaseCredentialRef{{Role: "admin", Username: "postgres"}, {Role: "app", Username: "ncd_app"}}, Platforms: []HostingDatabaseImagePlatform{{OS: "linux", Architecture: "amd64", Digest: "sha256:ec4dfc6571f04fe0731ce948c1500d676af49a7224392dcc6995cc1ee33a94bb"}, {OS: "linux", Architecture: "arm64", Variant: "v8", Digest: "sha256:f97ca6038b7641359fef23126949d824ac68edd8a7081b424d6a78e1f7076f8e"}}}, nil
	case engine == "redis" && version == HostingDatabaseRedisRecipe:
		return HostingDatabaseRecipe{Engine: engine, Version: version, ImageReference: "docker.io/library/redis@sha256:e5f23377739f95a2f530b9c99f93accb4f634577f09d00a7d8e0638f2fdf69e0", MountPath: "/data", ContainerPort: 6379, Credentials: []HostingDatabaseCredentialRef{{Role: "app", Username: "ncd_app"}, {Role: "probe", Username: "ncd_probe"}}, Platforms: []HostingDatabaseImagePlatform{{OS: "linux", Architecture: "amd64", Digest: "sha256:dfb838e40392b4142485b89a51241c635e9e04e4f30bd9d3db4dabf1c1429e00"}, {OS: "linux", Architecture: "arm64", Variant: "v8", Digest: "sha256:b9fe60acc13c30e9a4ed29b9022ceea7b468fa2f032a173787a55bb8f8998f03"}}}, nil
	default:
		return HostingDatabaseRecipe{}, errors.New("unsupported database engine or recipe version")
	}
}

// Validate confines creation to exact supported metadata recipes and identities.
func (in HostingDatabaseCreateInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || !hostingProxyID.MatchString(in.ProjectID) || !hostingProxyID.MatchString(in.ProjectEnvironmentID) || !hostingProxyID.MatchString(in.DestinationID) {
		return errors.New("invalid database request identity")
	}
	if err := ValidateHostingName(in.Name); err != nil {
		return err
	}
	_, err := HostingDatabaseRecipeFor(in.Engine, in.RecipeVersion)
	return err
}

// UnmarshalJSON rejects duplicate, missing, unknown and server-owned fields.
func (in *HostingDatabaseCreateInput) UnmarshalJSON(raw []byte) error {
	invalid := errors.New("invalid database creation input")
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalid
	}
	seen := make(map[string]bool, 7)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return invalid
		}
		switch key {
		case "request_id", "project_id", "project_environment_id", "name", "destination_id", "engine", "recipe_version":
		default:
			return invalid
		}
		seen[key] = true
		var value string
		if d.Decode(&value) != nil || value == "" {
			return invalid
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != 7 || d.Decode(new(any)) != io.EOF {
		return invalid
	}
	type wire HostingDatabaseCreateInput
	var value wire
	if strictSourceJSON(raw, &value) != nil {
		return invalid
	}
	*in = HostingDatabaseCreateInput(value)
	return in.Validate()
}

// HostingDatabaseResourceID binds retries to one project-scoped request identity.
func HostingDatabaseResourceID(project, request string) string {
	return "res_db_" + HostingDocumentHash(struct{ Project, Request string }{project, request})[7:39]
}

// HostingDatabaseSecretID derives one stable credential identity per resource role.
func HostingDatabaseSecretID(resource, role string) string {
	return "msec_db_" + HostingDocumentHash(struct{ Resource, Role string }{resource, role})[7:39]
}
