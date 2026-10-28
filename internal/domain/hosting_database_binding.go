package domain

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
)

// Binding documents are a separate versioned family, not v1 DB apply-plan fields.
const (
	HostingDatabaseBindingVersion     = "database-binding.private.v1"
	HostingDatabaseBindingHoldPurpose = "database_binding_network_effect"
	HostingDatabaseBindingJSONLimit   = 16 << 10
)

// ErrInvalidHostingDatabaseBinding contains no credential or daemon output.
var ErrInvalidHostingDatabaseBinding = errors.New("invalid hosting database binding")

// HostingDatabaseBindingInput selects saved metadata, never networking or secrets.
// ExpectedRuntimeGeneration may be zero only for the prior untouched document.
type HostingDatabaseBindingInput struct {
	DatabaseResourceID         string `json:"database_resource_id"`
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
	ExpectedRuntimeGeneration  int64  `json:"expected_runtime_generation"`
	ExpectedDatabaseGeneration int64  `json:"expected_database_generation"`
	RequestID                  string `json:"request_id"`
}

// Validate checks selection and generation CAS shape without loading saved state.
func (in HostingDatabaseBindingInput) Validate() error {
	if !hostingProxyID.MatchString(in.DatabaseResourceID) || !hostingStorageRequest.MatchString(in.RequestID) || in.ExpectedResourceGeneration < 1 || in.ExpectedRuntimeGeneration < 0 || in.ExpectedDatabaseGeneration < 1 {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// UnmarshalJSON requires all five selection fields and rejects server-owned input.
func (in *HostingDatabaseBindingInput) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBindingInput
	var value wire
	var fields map[string]json.RawMessage
	if decodeDatabaseBinding(raw, &value) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return ErrInvalidHostingDatabaseBinding
	}
	if err := HostingDatabaseBindingInput(value).Validate(); err != nil {
		return err
	}
	*in = HostingDatabaseBindingInput(value)
	return nil
}

// HostingDatabaseBinding is immutable desired input. It does not prove current
// authorization, credential availability, network ownership or attached endpoints.
type HostingDatabaseBinding struct {
	Version                       string                         `json:"version"`
	ID                            string                         `json:"id"`
	Generation                    int64                          `json:"generation"`
	ApplicationResourceID         string                         `json:"application_resource_id"`
	ApplicationResourceGeneration int64                          `json:"application_resource_generation"`
	ApplicationBindingID          string                         `json:"application_binding_id"`
	DatabaseResourceID            string                         `json:"database_resource_id"`
	DatabaseResourceGeneration    int64                          `json:"database_resource_generation"`
	DatabaseBindingID             string                         `json:"database_binding_id"`
	ProjectID                     string                         `json:"project_id"`
	DestinationID                 string                         `json:"destination_id"`
	ServerID                      string                         `json:"server_id"`
	RunnerID                      string                         `json:"runner_id"`
	Database                      HostingDatabaseExecutionRef    `json:"database"`
	Credential                    HostingDatabaseCredentialRef   `json:"credential"`
	NetworkName                   string                         `json:"network_name"`
	Alias                         string                         `json:"alias"`
	EnvironmentNames              []string                       `json:"environment_names"`
	RuntimeConfiguration          HostingRuntimeConfigurationRef `json:"runtime_configuration"`
	Hash                          string                         `json:"hash"`
}

// HostingDatabaseBindingID remains stable across application/database redeploys.
func HostingDatabaseBindingID(applicationBinding, databaseBinding string) string {
	if !hostingProxyID.MatchString(applicationBinding) || !hostingProxyID.MatchString(databaseBinding) || applicationBinding == databaseBinding {
		return ""
	}
	return "hdb_" + HostingDocumentHash(struct{ Application, Database string }{applicationBinding, databaseBinding})[7:39]
}

// HostingDatabaseBindingAlias is a DNS-safe database identity, not a container name.
func HostingDatabaseBindingAlias(databaseBinding string) string {
	if !hostingProxyID.MatchString(databaseBinding) {
		return ""
	}
	return "ncdb-" + HostingDocumentHash(struct{ Binding string }{databaseBinding})[7:39]
}

// HostingDatabaseBindingEnvironmentNames returns a fresh, sorted owned profile.
// The controller must reject collisions with saved runtime entries or bindings.
func HostingDatabaseBindingEnvironmentNames(engine string) []string {
	switch engine {
	case "postgresql":
		return []string{"PGDATABASE", "PGHOST", "PGPASSWORD", "PGPORT", "PGUSER"}
	case "mysql":
		return []string{"MYSQL_DATABASE", "MYSQL_HOST", "MYSQL_PASSWORD", "MYSQL_PORT", "MYSQL_USER"}
	case "redis":
		return []string{"REDIS_HOST", "REDIS_PASSWORD", "REDIS_PORT", "REDIS_USERNAME"}
	default:
		return nil
	}
}

// DocumentHash hashes the complete desired document with its hash field empty.
func (b HostingDatabaseBinding) DocumentHash() string {
	b.Hash = ""
	return HostingDocumentHash(b)
}

// Validate checks canonical identities, exact recipe app credentials and profiles.
func (b HostingDatabaseBinding) Validate() error {
	for _, id := range []string{b.ApplicationResourceID, b.ApplicationBindingID, b.DatabaseResourceID, b.DatabaseBindingID, b.ProjectID, b.DestinationID, b.ServerID, b.RunnerID} {
		if !hostingProxyID.MatchString(id) {
			return ErrInvalidHostingDatabaseBinding
		}
	}
	if b.Version != HostingDatabaseBindingVersion || !hostingProxyID.MatchString(b.ID) || b.ID != HostingDatabaseBindingID(b.ApplicationBindingID, b.DatabaseBindingID) || b.ApplicationResourceID == b.DatabaseResourceID || b.Generation < 1 || b.ApplicationResourceGeneration < 1 || b.DatabaseResourceGeneration < 1 || b.RuntimeConfiguration.Generation < 1 || !imageSHA256.MatchString(b.RuntimeConfiguration.Hash) || !imageSHA256.MatchString(b.Database.ConfigurationHash) || b.NetworkName != HostingDatabaseNetworkName(b.DatabaseBindingID) || b.Alias != HostingDatabaseBindingAlias(b.DatabaseBindingID) || !slices.Equal(b.EnvironmentNames, HostingDatabaseBindingEnvironmentNames(b.Database.Engine)) || b.Hash != b.DocumentHash() {
		return ErrInvalidHostingDatabaseBinding
	}
	recipe, err := HostingDatabaseRecipeFor(b.Database.Engine, b.Database.RecipeVersion)
	if err != nil || b.Credential.Role != "app" || b.Credential.SecretID != HostingDatabaseSecretID(b.DatabaseResourceID, "app") || b.Credential.Version < 1 {
		return ErrInvalidHostingDatabaseBinding
	}
	for _, role := range recipe.Credentials {
		if role.Role == "app" && role.Username == b.Credential.Username {
			return nil
		}
	}
	return ErrInvalidHostingDatabaseBinding
}

// The first binding adapter accepts only public images with runtime input, no
// application storage, local builds, route continuity or database sidecars.
func databaseBindingApplicationSnapshot(s HostingSnapshot, b HostingDatabaseBinding) bool {
	policy := s.PolicyVersion == HostingRuntimePolicyVersion && s.ApplicationDatabase == (HostingApplicationDatabaseRef{}) || (s.PolicyVersion == HostingApplicationDatabasePolicyVersion || s.PolicyVersion == HostingApplicationDatabaseStoragePolicyVersion) && s.ApplicationDatabase == (HostingApplicationDatabaseRef{BindingID: b.ID, BindingHash: b.Hash})
	return policy && s.LocalBuild == (HostingLocalBuildSource{}) && (s.Storage == (HostingStorageRef{}) || s.PolicyVersion == HostingApplicationDatabaseStoragePolicyVersion && validHostingStorageRef(s.Storage)) && s.StorageRouteContinuity == (HostingStorageRouteContinuityRef{}) && s.Database == (HostingDatabaseExecutionRef{}) && ValidateHostingRuntimeConfigurationRef(s) == nil
}

// ValidateTargets links desired metadata to exact saved targets. Project IDs are
// explicit because v1 HostingSnapshot intentionally contains no project field.
// The controller still checks live owner/grants, generations and secret existence.
func (b HostingDatabaseBinding) ValidateTargets(appProject string, app HostingSnapshot, databaseProject string, database HostingApplyPlan) error {
	db := database.Snapshot
	if b.Validate() != nil || appProject != b.ProjectID || databaseProject != b.ProjectID || !databaseBindingApplicationSnapshot(app, b) || app.RuntimeConfiguration != b.RuntimeConfiguration || ValidateHostingDatabasePlan(database) != nil || app.ResourceID != b.ApplicationResourceID || app.ResourceGeneration != b.ApplicationResourceGeneration || app.BindingID != b.ApplicationBindingID || db.ResourceID != b.DatabaseResourceID || db.ResourceGeneration != b.DatabaseResourceGeneration || db.BindingID != b.DatabaseBindingID || db.Database != b.Database {
		return ErrInvalidHostingDatabaseBinding
	}
	for _, target := range []HostingSnapshot{app, db} {
		if target.DestinationID != b.DestinationID || target.ServerID != b.ServerID || target.RunnerID != b.RunnerID {
			return ErrInvalidHostingDatabaseBinding
		}
	}
	if !slices.Contains(database.Database.Credentials, b.Credential) {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// UnmarshalJSON decodes one closed, canonically hashed desired document.
func (b *HostingDatabaseBinding) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBinding
	var value wire
	if decodeDatabaseBinding(raw, &value) != nil || HostingDatabaseBinding(value).Validate() != nil {
		return ErrInvalidHostingDatabaseBinding
	}
	*b = HostingDatabaseBinding(value)
	return nil
}

// HostingDatabaseBindingEndpointTarget pins an application container and daemon
// network. These expected identities are not evidence of an existing endpoint.
type HostingDatabaseBindingEndpointTarget struct {
	ContainerID          string `json:"container_id"`
	ContainerOperationID string `json:"container_operation_id"`
	ImageID              string `json:"image_id"`
	ReleaseHash          string `json:"release_hash"`
	ContainerPlanHash    string `json:"container_plan_hash"`
	NetworkID            string `json:"network_id"`
	NetworkName          string `json:"network_name"`
	DaemonID             string `json:"daemon_id"`
}

func (e HostingDatabaseBindingEndpointTarget) valid() bool {
	return databaseBindingDockerID(e.ContainerID) && databaseBindingDockerID(e.NetworkID) && hostingProxyID.MatchString(e.ContainerOperationID) && imageSHA256.MatchString(e.ImageID) && imageSHA256.MatchString(e.ReleaseHash) && imageSHA256.MatchString(e.ContainerPlanHash) && hostingProxyID.MatchString(e.NetworkName) && validRuntimeReferenceID(e.DaemonID)
}

// HostingDatabaseBindingEndpointPlan is separate from all existing v1 app/DB
// apply plans. MembershipRevision is a controller CAS over the desired member set.
type HostingDatabaseBindingEndpointPlan struct {
	Version            string                               `json:"version"`
	OperationID        string                               `json:"operation_id"`
	BindingHash        string                               `json:"binding_hash"`
	MembershipRevision int64                                `json:"membership_revision"`
	Action             string                               `json:"action"`
	Target             HostingDatabaseBindingEndpointTarget `json:"target"`
	ExpectedEndpointID string                               `json:"expected_endpoint_id,omitempty"`
	Hash               string                               `json:"hash"`
}

// DocumentHash binds the operation, membership revision and expected endpoint.
func (p HostingDatabaseBindingEndpointPlan) DocumentHash() string {
	p.Hash = ""
	return HostingDocumentHash(p)
}

func (p HostingDatabaseBindingEndpointPlan) validShape() bool {
	return p.Version == HostingDatabaseBindingVersion && hostingProxyID.MatchString(p.OperationID) && imageSHA256.MatchString(p.BindingHash) && p.MembershipRevision > 0 && p.Target.valid() && (p.Action == "attach" && p.ExpectedEndpointID == "" || p.Action == "retire" && databaseBindingDockerID(p.ExpectedEndpointID)) && p.Hash == p.DocumentHash()
}

// Validate links one canonical network-effect plan to its immutable desired input.
func (p HostingDatabaseBindingEndpointPlan) Validate(b HostingDatabaseBinding) error {
	if b.Validate() != nil || !p.validShape() || p.BindingHash != b.Hash || p.Target.NetworkName != b.NetworkName {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// ValidateApplication checks the expected container against retained platform
// metadata. The runner must independently inspect labels, image and network users.
func (p HostingDatabaseBindingEndpointPlan) ValidateApplication(b HostingDatabaseBinding, h HostingHealthy) error {
	s := h.Snapshot
	if p.Validate(b) != nil || !databaseBindingApplicationSnapshot(s, b) || (h.Storage != nil && (s.PolicyVersion != HostingApplicationDatabaseStoragePolicyVersion || ValidateHostingHealthyStorage(h) != nil) || h.Storage == nil && s.Storage != (HostingStorageRef{})) || h.Release.LocalBuild != (HostingBuildArtifact{}) || ValidateHostingRelease(h.Release) != nil || s.ResourceID != b.ApplicationResourceID || s.ResourceGeneration != b.ApplicationResourceGeneration || s.BindingID != b.ApplicationBindingID || s.DestinationID != b.DestinationID || s.ServerID != b.ServerID || s.RunnerID != b.RunnerID || s.RuntimeConfiguration != b.RuntimeConfiguration || p.Target.ContainerID != h.ContainerID || p.Target.ContainerOperationID != h.OperationID || p.Target.ContainerPlanHash != h.PlanHash || p.Target.ImageID != h.Release.ImageID || p.Target.ReleaseHash != h.Release.Hash || p.Target.DaemonID != h.Release.DaemonID {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// UnmarshalJSON rejects open fields and ambiguous attach/retire endpoint shapes.
func (p *HostingDatabaseBindingEndpointPlan) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBindingEndpointPlan
	var value wire
	if decodeDatabaseBinding(raw, &value) != nil || !HostingDatabaseBindingEndpointPlan(value).validShape() {
		return ErrInvalidHostingDatabaseBinding
	}
	*p = HostingDatabaseBindingEndpointPlan(value)
	return nil
}

// HostingDatabaseBindingEndpoint includes an endpoint ID only after observation
// or when pinning an already observed endpoint for retirement.
type HostingDatabaseBindingEndpoint struct {
	Target     HostingDatabaseBindingEndpointTarget `json:"target"`
	EndpointID string                               `json:"endpoint_id,omitempty"`
}

// HostingDatabaseBindingEndpointObservation records an exact endpoint present or
// absent. Detached observations retain the retired ID; they never authorize reuse.
type HostingDatabaseBindingEndpointObservation struct {
	Endpoint HostingDatabaseBindingEndpoint `json:"endpoint"`
	Present  bool                           `json:"present"`
}

// UnmarshalJSON requires explicit presence, including false for detachment.
func (o *HostingDatabaseBindingEndpointObservation) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBindingEndpointObservation
	var value wire
	var fields map[string]json.RawMessage
	if decodeDatabaseBinding(raw, &value) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 2 || fields["endpoint"] == nil || fields["present"] == nil {
		return ErrInvalidHostingDatabaseBinding
	}
	*o = HostingDatabaseBindingEndpointObservation(value)
	return nil
}

// HostingDatabaseBindingEndpointReceipt stores no bearer token or lease authority.
// Full request authority is checked live; its historical hash may be retained by
// the existing receipt mechanism without reviving mutation authority on replay.
type HostingDatabaseBindingEndpointReceipt struct {
	Version            string                                     `json:"version"`
	ReceiptID          string                                     `json:"receipt_id"`
	OperationID        string                                     `json:"operation_id"`
	PlanHash           string                                     `json:"plan_hash"`
	BindingHash        string                                     `json:"binding_hash"`
	MembershipRevision int64                                      `json:"membership_revision"`
	Phase              string                                     `json:"phase"`
	Intent             *HostingDatabaseBindingEndpoint            `json:"intent,omitempty"`
	Observation        *HostingDatabaseBindingEndpointObservation `json:"observation,omitempty"`
	FailureCode        string                                     `json:"failure_code,omitempty"`
	Hash               string                                     `json:"hash"`
}

// DocumentHash binds the complete phase payload without live lease credentials.
func (r HostingDatabaseBindingEndpointReceipt) DocumentHash() string {
	r.Hash = ""
	return HostingDocumentHash(r)
}

func (r HostingDatabaseBindingEndpointReceipt) validShape() bool {
	if r.Version != HostingDatabaseBindingVersion || !hostingProxyID.MatchString(r.OperationID) || !validRuntimeReferenceID(r.ReceiptID) || !imageSHA256.MatchString(r.PlanHash) || !imageSHA256.MatchString(r.BindingHash) || r.MembershipRevision < 1 || r.Hash != r.DocumentHash() {
		return false
	}
	switch r.Phase {
	case "attach_intent", "retire_intent":
		return r.Intent != nil && r.Intent.Target.valid() && (r.Phase == "attach_intent" && r.Intent.EndpointID == "" || r.Phase == "retire_intent" && databaseBindingDockerID(r.Intent.EndpointID)) && r.Observation == nil && r.FailureCode == ""
	case "attached", "detached":
		return r.Intent == nil && r.Observation != nil && r.Observation.Endpoint.Target.valid() && databaseBindingDockerID(r.Observation.Endpoint.EndpointID) && r.Observation.Present == (r.Phase == "attached") && r.FailureCode == ""
	case "uncertain":
		return r.Intent == nil && r.Observation == nil && slices.Contains([]string{"authority_lost", "ownership_mismatch", "inspection_failed", "mutation_unconfirmed", "interrupted"}, r.FailureCode)
	default:
		return false
	}
}

// Validate checks phase exclusivity and exact operation/plan/revision linkage.
func (r HostingDatabaseBindingEndpointReceipt) Validate(p HostingDatabaseBindingEndpointPlan, b HostingDatabaseBinding) error {
	if p.Validate(b) != nil || !r.validShape() || r.OperationID != p.OperationID || r.PlanHash != p.Hash || r.BindingHash != b.Hash || r.MembershipRevision != p.MembershipRevision {
		return ErrInvalidHostingDatabaseBinding
	}
	if r.Phase == "uncertain" {
		return nil
	}
	if r.Intent != nil {
		if r.Intent.Target != p.Target || r.Intent.EndpointID != p.ExpectedEndpointID || r.Phase != p.Action+"_intent" {
			return ErrInvalidHostingDatabaseBinding
		}
	} else if r.Observation.Endpoint.Target != p.Target || p.Action == "attach" && r.Phase != "attached" || p.Action == "retire" && (r.Phase != "detached" || r.Observation.Endpoint.EndpointID != p.ExpectedEndpointID) {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// ValidateTransition permits identical receipt replay and one observed settlement
// after durable intent. Uncertainty is terminal for this authority/plan; recovery
// requires a separately admitted current operation, not a guessed effect replay.
func (r HostingDatabaseBindingEndpointReceipt) ValidateTransition(previous *HostingDatabaseBindingEndpointReceipt, p HostingDatabaseBindingEndpointPlan, b HostingDatabaseBinding) error {
	if r.Validate(p, b) != nil {
		return ErrInvalidHostingDatabaseBinding
	}
	if previous == nil {
		if r.Phase != p.Action+"_intent" {
			return ErrInvalidHostingDatabaseBinding
		}
		return nil
	}
	if previous.Validate(p, b) != nil {
		return ErrInvalidHostingDatabaseBinding
	}
	if r.Hash == previous.Hash {
		return nil
	}
	if r.ReceiptID == previous.ReceiptID || previous.Phase != p.Action+"_intent" || r.Phase != "uncertain" && r.Phase != "attached" && r.Phase != "detached" {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// ObservedMembership deliberately excludes intent, uncertainty and detachment.
func (r HostingDatabaseBindingEndpointReceipt) ObservedMembership(p HostingDatabaseBindingEndpointPlan, b HostingDatabaseBinding) (HostingDatabaseBindingEndpoint, bool) {
	if r.Validate(p, b) != nil || r.Phase != "attached" {
		return HostingDatabaseBindingEndpoint{}, false
	}
	return r.Observation.Endpoint, true
}

// UnmarshalJSON rejects unknown fields, null payloads and mixed phase evidence.
func (r *HostingDatabaseBindingEndpointReceipt) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBindingEndpointReceipt
	var value wire
	var fields map[string]json.RawMessage
	if decodeDatabaseBinding(raw, &value) != nil || json.Unmarshal(raw, &fields) != nil || !HostingDatabaseBindingEndpointReceipt(value).validShape() {
		return ErrInvalidHostingDatabaseBinding
	}
	_, intent := fields["intent"]
	_, observation := fields["observation"]
	_, failure := fields["failure_code"]
	if intent != (value.Intent != nil) || observation != (value.Observation != nil) || failure != (value.Phase == "uncertain") {
		return ErrInvalidHostingDatabaseBinding
	}
	*r = HostingDatabaseBindingEndpointReceipt(value)
	return nil
}

// HostingDatabaseBindingEndpointRequest reuses the existing hosting authority
// tuple. Validate checks shape only; the controller must check its current lease.
type HostingDatabaseBindingEndpointRequest struct {
	HostingAuthority
	Receipt HostingDatabaseBindingEndpointReceipt `json:"receipt"`
}

// Validate matches the existing authority tuple shape and contextual receipt.
func (in HostingDatabaseBindingEndpointRequest) Validate(p HostingDatabaseBindingEndpointPlan, b HostingDatabaseBinding) error {
	if !in.validShape() || in.OperationID != p.OperationID || in.Receipt.Validate(p, b) != nil {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

func (in HostingDatabaseBindingEndpointRequest) validShape() bool {
	a := in.HostingAuthority
	return hostingProxyID.MatchString(a.OperationID) && hostingProxyID.MatchString(a.RunID) && validRuntimeReferenceID(a.LeaseID) && validRuntimeReferenceID(a.Fence) && a.Attempt > 0 && a.Attempt <= math.MaxInt32 && in.Receipt.validShape() && a.OperationID == in.Receipt.OperationID
}

// UnmarshalJSON decodes a closed authority request without accepting credentials.
func (in *HostingDatabaseBindingEndpointRequest) UnmarshalJSON(raw []byte) error {
	type wire HostingDatabaseBindingEndpointRequest
	var value wire
	if decodeDatabaseBinding(raw, &value) != nil || !HostingDatabaseBindingEndpointRequest(value).validShape() {
		return ErrInvalidHostingDatabaseBinding
	}
	*in = HostingDatabaseBindingEndpointRequest(value)
	return nil
}

// HostingDatabaseBindingNetworkHold pins an unsettled effect to its durable
// intent/uncertain receipt. It is neither endpoint ownership nor a live lease.
type HostingDatabaseBindingNetworkHold struct {
	Purpose            string `json:"purpose"`
	OperationID        string `json:"operation_id"`
	PlanHash           string `json:"plan_hash"`
	BindingHash        string `json:"binding_hash"`
	MembershipRevision int64  `json:"membership_revision"`
	ReceiptID          string `json:"receipt_id"`
	ReceiptHash        string `json:"receipt_hash"`
}

// Validate permits this hold purpose only for exact unsettled receipt evidence.
func (h HostingDatabaseBindingNetworkHold) Validate(p HostingDatabaseBindingEndpointPlan, b HostingDatabaseBinding, r HostingDatabaseBindingEndpointReceipt) error {
	if r.Validate(p, b) != nil || h.Purpose != HostingDatabaseBindingHoldPurpose || h.OperationID != p.OperationID || h.PlanHash != p.Hash || h.BindingHash != b.Hash || h.MembershipRevision != p.MembershipRevision || h.ReceiptID != r.ReceiptID || h.ReceiptHash != r.Hash || r.Phase != "attach_intent" && r.Phase != "retire_intent" && r.Phase != "uncertain" {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

// DecodeHostingDatabaseBindingJSON rejects unknown/duplicate/case-aliased fields
// and malformed Unicode. Contextual Validate methods are still required afterward.
func DecodeHostingDatabaseBindingJSON(raw []byte, out any) error {
	return decodeDatabaseBinding(raw, out)
}

func decodeDatabaseBinding(raw []byte, out any) error {
	var fields any
	if len(raw) > HostingDatabaseBindingJSONLimit || !validRuntimeJSONUnicode(raw) || json.Unmarshal(raw, &fields) != nil || databaseBindingHasNull(fields) || DecodeHostingStorageProtocolJSON(raw, out) != nil {
		return ErrInvalidHostingDatabaseBinding
	}
	return nil
}

func databaseBindingHasNull(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range v {
			if databaseBindingHasNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if databaseBindingHasNull(child) {
				return true
			}
		}
	}
	return false
}

func databaseBindingDockerID(id string) bool {
	return imageSHA256.MatchString("sha256:" + id)
}
