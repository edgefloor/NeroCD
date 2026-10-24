package domain

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Database execution is a private protocol family, not an agent qualification.
const (
	RunTypeDatabaseDeploy          = "database_deploy"
	HostingDatabaseCapability      = "hosting_database_v1"
	HostingDatabaseCompilerVersion = "database-hosting.private.owned-storage.v1"
	HostingDatabasePolicyVersion   = "database.private.owned-storage.v1"
)

// HostingDatabaseExecutionRef binds immutable nonsecret database configuration.
type HostingDatabaseExecutionRef struct {
	Engine            string `json:"engine"`
	RecipeVersion     string `json:"recipe_version"`
	ConfigurationHash string `json:"configuration_hash"`
}

// HostingDatabasePlan pins private networking and credential references only.
type HostingDatabasePlan struct {
	Reference   HostingDatabaseExecutionRef    `json:"reference"`
	NetworkName string                         `json:"network_name"`
	Credentials []HostingDatabaseCredentialRef `json:"credentials"`
	Network     *HostingDatabaseNetworkPlan    `json:"network,omitempty"`
}

// HostingDatabaseDeploymentInput selects the current saved database metadata.
type HostingDatabaseDeploymentInput struct {
	ExpectedGeneration int64  `json:"expected_generation"`
	IdempotencyKey     string `json:"idempotency_key"`
}

// Validate rejects caller-selected runtime, port, credential, or mount settings.
func (in HostingDatabaseDeploymentInput) Validate() error {
	if in.ExpectedGeneration < 1 || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 || strings.ContainsFunc(in.IdempotencyKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("expected_generation and a bounded idempotency_key are required")
	}
	return nil
}

// UnmarshalJSON accepts exactly the bounded public generation/idempotency input.
func (in *HostingDatabaseDeploymentInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseDeploymentInput
	var value wire
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) || DecodeHostingStorageProtocolJSON(raw, &value) != nil {
		return errors.New("invalid database deployment input")
	}
	if err := HostingDatabaseDeploymentInput(value).Validate(); err != nil {
		return err
	}
	*in = HostingDatabaseDeploymentInput(value)
	return nil
}

// HostingDatabaseNetworkName is a stable nonsecret identity independent of names
// and operations. The runtime must create/inspect an internal Docker network.
func HostingDatabaseNetworkName(binding string) string {
	if !hostingProxyID.MatchString(binding) {
		return ""
	}
	return "ncdn_" + HostingDocumentHash(struct {
		BindingID string `json:"binding_id"`
	}{binding})[7:39]
}

func hostingDatabaseSnapshotRecipe(s HostingSnapshot) (HostingDatabaseRecipe, error) {
	if s.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || s.PolicyVersion != HostingDatabasePolicyVersion || !validHostingStorageRef(s.Storage) || s.HostPort != 0 || s.Readiness != (HostingReadiness{}) || s.LocalBuild != (HostingLocalBuildSource{}) || s.RuntimeConfiguration != (HostingRuntimeConfigurationRef{}) || s.StorageRouteContinuity != (HostingStorageRouteContinuityRef{}) || s.ResourceGeneration < 1 {
		return HostingDatabaseRecipe{}, errors.New("invalid private database snapshot family")
	}
	for _, id := range []string{s.ResourceID, s.BindingID, s.DestinationID, s.ServerID, s.RunnerID} {
		if !hostingProxyID.MatchString(id) {
			return HostingDatabaseRecipe{}, errors.New("invalid database target identity")
		}
	}
	if len(s.RuntimeNamespace) < 28 || len(s.RuntimeNamespace) > 68 || !strings.HasPrefix(s.RuntimeNamespace, "ncd_") || strings.Trim(s.RuntimeNamespace[4:], "0123456789abcdef") != "" {
		return HostingDatabaseRecipe{}, errors.New("invalid database runtime namespace")
	}
	r, err := HostingDatabaseRecipeFor(s.Database.Engine, s.Database.RecipeVersion)
	if err != nil {
		return r, err
	}
	if s.ImageReference != r.ImageReference || s.ContainerPort != r.ContainerPort {
		return r, errors.New("database snapshot differs from pinned recipe")
	}
	return r, nil
}

// ValidateHostingDatabaseSnapshot rejects mixed families without changing old
// snapshots whose database reference is zero.
func ValidateHostingDatabaseSnapshot(s HostingSnapshot) error {
	if s.Database == (HostingDatabaseExecutionRef{}) {
		if s.PolicyVersion == HostingDatabasePolicyVersion {
			return errors.New("database policy requires configuration reference")
		}
		return nil
	}
	if !imageSHA256.MatchString(s.Database.ConfigurationHash) {
		return errors.New("invalid database configuration hash")
	}
	_, err := hostingDatabaseSnapshotRecipe(s)
	return err
}

func hostingDatabaseCredentials(s HostingSnapshot, refs []HostingDatabaseCredentialRef) ([]HostingDatabaseCredentialRef, error) {
	r, err := hostingDatabaseSnapshotRecipe(s)
	if err != nil {
		return nil, err
	}
	if len(refs) != len(r.Credentials) {
		return nil, errors.New("database requires exact recipe credential roles")
	}
	canonical := slices.Clone(refs)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Role < canonical[j].Role })
	for i, want := range r.Credentials {
		got := canonical[i]
		if got.Role != want.Role || got.Username != want.Username || got.SecretID != HostingDatabaseSecretID(s.ResourceID, want.Role) || got.Version < 1 {
			return nil, errors.New("database credential reference differs from recipe or resource")
		}
	}
	return canonical, nil
}

// HostingDatabaseConfigurationHash binds exact credential versions, storage,
// recipe/index and private binding/network identity. A positive version is a
// reference, not proof that its secret exists or may be read by this operation.
func HostingDatabaseConfigurationHash(s HostingSnapshot, refs []HostingDatabaseCredentialRef) (string, error) {
	canonical, err := hostingDatabaseCredentials(s, refs)
	if err != nil {
		return "", err
	}
	recipe, err := HostingDatabaseRecipeFor(s.Database.Engine, s.Database.RecipeVersion)
	if err != nil {
		return "", err
	}
	return HostingDocumentHash(struct {
		ResourceID     string                         `json:"resource_id"`
		BindingID      string                         `json:"binding_id"`
		Engine         string                         `json:"engine"`
		RecipeVersion  string                         `json:"recipe_version"`
		ImageReference string                         `json:"image_reference"`
		DatabaseName   string                         `json:"database_name"`
		MountPath      string                         `json:"mount_path"`
		ContainerPort  int                            `json:"container_port"`
		NetworkName    string                         `json:"network_name"`
		Storage        HostingStorageRef              `json:"storage"`
		Credentials    []HostingDatabaseCredentialRef `json:"credentials"`
	}{ResourceID: s.ResourceID, BindingID: s.BindingID, Engine: recipe.Engine, RecipeVersion: recipe.Version, ImageReference: recipe.ImageReference, DatabaseName: recipe.DatabaseName, MountPath: recipe.MountPath, ContainerPort: recipe.ContainerPort, NetworkName: HostingDatabaseNetworkName(s.BindingID), Storage: s.Storage, Credentials: canonical}), nil
}

// ValidateHostingDatabaseRelease binds the recipe index in the snapshot to its
// exact supported child manifest. ImageID remains the separately observed Docker
// config identity; it must never be substituted for a manifest digest.
func ValidateHostingDatabaseRelease(s HostingSnapshot, r HostingRelease) error {
	if err := ValidateHostingDatabaseSnapshot(s); err != nil {
		return err
	}
	recipe, err := hostingDatabaseSnapshotRecipe(s)
	if err != nil {
		return err
	}
	if r.LocalBuild != (HostingBuildArtifact{}) || ValidateHostingRelease(r) != nil {
		return errors.New("invalid database release evidence")
	}
	for _, platform := range recipe.Platforms {
		if r.OS == platform.OS && r.Architecture == platform.Architecture && r.Variant == platform.Variant && r.CanonicalImage == strings.Split(recipe.ImageReference, "@")[0]+"@"+platform.Digest {
			return nil
		}
	}
	return errors.New("database release differs from exact recipe child manifest/platform")
}

// Validate checks the complete canonical database plan descriptor.
func (p HostingDatabasePlan) Validate(s HostingSnapshot, r HostingRelease) error {
	if p.Network != nil || ValidateHostingDatabaseRelease(s, r) != nil || p.Reference != s.Database || p.NetworkName != HostingDatabaseNetworkName(s.BindingID) {
		return errors.New("database plan reference or network mismatch")
	}
	canonical, err := hostingDatabaseCredentials(s, p.Credentials)
	if err != nil || !slices.Equal(canonical, p.Credentials) {
		return errors.New("database credentials are not canonical")
	}
	hash, err := HostingDatabaseConfigurationHash(s, canonical)
	if err != nil || hash != s.Database.ConfigurationHash {
		return errors.New("database configuration hash mismatch")
	}
	return nil
}

// CompileHostingDatabasePlan pins one private writer and its immutable owned
// volume. PreviousHealthy is an allowed predecessor, never evidence it stopped.
func CompileHostingDatabasePlan(s HostingSnapshot, r HostingRelease, previous *HostingHealthy, storage HostingStoragePlan, database HostingDatabasePlan) (HostingApplyPlan, error) {
	storageID, volumeName := HostingStorageIdentity(s.BindingID)
	if s.Storage.StorageID != storageID || storage.VolumeName != volumeName {
		return HostingApplyPlan{}, errors.New("database requires its binding-derived owned volume")
	}
	canonical, err := hostingDatabaseCredentials(s, database.Credentials)
	if err != nil {
		return HostingApplyPlan{}, err
	}
	database.Credentials = canonical
	if database.Validate(s, r) != nil || storage.Validate(s, r) != nil {
		return HostingApplyPlan{}, errors.New("invalid database/storage plan")
	}
	recipe, err := HostingDatabaseRecipeFor(s.Database.Engine, s.Database.RecipeVersion)
	if err != nil || storage.MountPath != recipe.MountPath {
		return HostingApplyPlan{}, errors.New("database mount differs from recipe")
	}
	plan := HostingApplyPlan{Snapshot: s, Release: r, Storage: &storage, Database: &database, CompilerVersion: HostingDatabaseCompilerVersion}
	if previous != nil {
		p := *previous
		if !hostingProxyID.MatchString(p.OperationID) || !hostingProxyID.MatchString(p.ContainerID) || !imageSHA256.MatchString(p.PlanHash) || p.Snapshot.ResourceID != s.ResourceID || p.Snapshot.BindingID != s.BindingID || p.Snapshot.DestinationID != s.DestinationID || p.Snapshot.ServerID != s.ServerID || p.Snapshot.RunnerID != s.RunnerID || p.Snapshot.RuntimeNamespace != s.RuntimeNamespace || p.Snapshot.ResourceGeneration > s.ResourceGeneration || p.Snapshot.Database != s.Database || p.Release.DaemonID != r.DaemonID || ValidateHostingDatabaseRelease(p.Snapshot, p.Release) != nil || ValidateHostingHealthyStorage(p) != nil || p.Storage == nil || p.Storage.Plan != storage {
			return HostingApplyPlan{}, errors.New("database predecessor identity mismatch")
		}
		ownedStorage := *p.Storage
		p.Storage = &ownedStorage
		plan.PreviousHealthy = &p
	}
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

// ValidateHostingDatabasePlan checks the canonical compiler output and hash.
func ValidateHostingDatabasePlan(p HostingApplyPlan) error {
	if p.ApplicationDatabase != nil || p.Storage == nil || p.Database == nil || p.CompilerVersion != HostingDatabaseCompilerVersion || p.RetainPrevious || len(p.RetainedReleases) != 0 {
		return errors.New("invalid private database plan family")
	}
	if p.Database.Validate(p.Snapshot, p.Release) != nil {
		return errors.New("invalid canonical database descriptor")
	}
	expected, err := CompileHostingDatabasePlan(p.Snapshot, p.Release, p.PreviousHealthy, *p.Storage, *p.Database)
	if err != nil || expected.Hash != p.Hash {
		return errors.New("database plan differs from canonical compiler output")
	}
	return nil
}

// ValidateDatabaseDeploymentTask admits only typed deploy/observe identities;
// persisted target, membership, capability, and lease checks remain mandatory.
func ValidateDatabaseDeploymentTask(run TaskRun) error {
	spec := run.RunSpec
	if spec.Type != RunTypeDatabaseDeploy || !hostingProxyID.MatchString(spec.HostingOperationID) || (spec.HostingMode != "deploy" && spec.HostingMode != "observe") || spec.HostingBuildID != "" || spec.ProxyOperationID != "" || spec.ProxyMode != "" {
		return errors.New("invalid internal database task")
	}
	run.RunSpec.Type = RunTypeImageDeploy
	return ValidateImageDeploymentTask(run)
}
