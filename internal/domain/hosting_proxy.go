package domain

import (
	"encoding/json"
	"errors"
	"net/mail"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/idna"
)

const (
	// RunTypeProxyApply is an internal exact-target proxy generation task.
	RunTypeProxyApply = "proxy_apply"
	// HostingProxyCompilerVersion freezes the typed proxy task protocol.
	HostingProxyCompilerVersion = "managed-traefik.file.v1"
	// HostingProxyMaxRoutes bounds whole-generation journal and report documents.
	HostingProxyMaxRoutes = 32
)

// HostingProxyConfiguration contains administrator settings without local paths.
type HostingProxyConfiguration struct {
	Email           string `json:"email"`
	CA              string `json:"ca"`
	ManagementPort  uint16 `json:"management_port"`
	GeneratedDomain string `json:"generated_domain,omitempty"`
}

// HostingProxyInput explicitly selects an existing control project and immutable settings.
type HostingProxyInput struct {
	ControlProjectID string                    `json:"control_project_id"`
	Configuration    HostingProxyConfiguration `json:"configuration"`
	IdempotencyKey   string                    `json:"idempotency_key"`
}

// HostingProxyMutationInput identifies an idempotent administrative generation request.
type HostingProxyMutationInput struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// HostingProxy is administrator-visible placement, desired state and ownership status.
type HostingProxy struct {
	ID                   string                    `json:"id"`
	ServerID             string                    `json:"server_id"`
	DestinationID        string                    `json:"destination_id"`
	RunnerID             string                    `json:"runner_id"`
	ControlProjectID     string                    `json:"control_project_id"`
	Configuration        HostingProxyConfiguration `json:"configuration"`
	Enabled              bool                      `json:"enabled"`
	DesiredGeneration    int64                     `json:"desired_generation"`
	LoadedGeneration     int64                     `json:"loaded_generation"`
	DaemonID             string                    `json:"daemon_id,omitempty"`
	StaticHash           string                    `json:"static_hash,omitempty"`
	ActiveOperationID    string                    `json:"active_operation_id,omitempty"`
	HoldOperationID      string                    `json:"hold_operation_id,omitempty"`
	RestoreOwnershipHeld bool                      `json:"restore_ownership_held"`
}

// HostingHostnameGrantInput delegates exact or descendant hostnames to one project.
type HostingHostnameGrantInput struct {
	ProjectID string `json:"project_id"`
	Hostname  string `json:"hostname"`
	Wildcard  bool   `json:"wildcard"`
}

// HostingHostnameGrant records explicit administrator hostname authority.
type HostingHostnameGrant struct {
	ID        string     `json:"id"`
	ProxyID   string     `json:"proxy_id"`
	ProjectID string     `json:"project_id"`
	Hostname  string     `json:"hostname"`
	Wildcard  bool       `json:"wildcard"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// HostingRouteInput accepts only route selection, never upstream addresses or ports.
type HostingRouteInput struct {
	ExpectedRouteVersion       int64  `json:"expected_route_version"`
	IdempotencyKey             string `json:"idempotency_key"`
	Hostname                   string `json:"hostname"`
	PathPrefix                 string `json:"path_prefix"`
	ExpectedHealthyOperationID string `json:"expected_healthy_operation_id"`
	GenerateHostname           bool   `json:"generate_hostname,omitempty"`
}

// HostingRouteDeletionInput withdraws only the expected version of one route.
type HostingRouteDeletionInput struct {
	ExpectedRouteVersion int64  `json:"expected_route_version"`
	IdempotencyKey       string `json:"idempotency_key"`
}

// HostingRoute is the project-visible desired route and its own loaded status.
type HostingRoute struct {
	ID                         string                  `json:"id"`
	ResourceID                 string                  `json:"resource_id"`
	Hostname                   string                  `json:"hostname"`
	PathPrefix                 string                  `json:"path_prefix"`
	Version                    int64                   `json:"version"`
	SelectedReleaseOperationID string                  `json:"selected_release_operation_id"`
	Enabled                    bool                    `json:"enabled"`
	OperationID                string                  `json:"operation_id,omitempty"`
	State                      string                  `json:"state"`
	HTTPReady                  bool                    `json:"http_ready"`
	HTTPSReady                 bool                    `json:"https_ready"`
	Certificate                HostingProxyCertificate `json:"certificate"`
}

// HostingProxyCertificate contains only public certificate observations.
type HostingProxyCertificate struct {
	Hostname    string    `json:"hostname"`
	Issuer      string    `json:"issuer,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Status      string    `json:"status"`
}

// HostingProxyRouteTarget is a server-derived release reference in an internal snapshot.
type HostingProxyRouteTarget struct {
	RouteID            string `json:"route_id"`
	ProjectID          string `json:"project_id"`
	ResourceID         string `json:"resource_id"`
	BindingID          string `json:"binding_id"`
	ReleaseOperationID string `json:"release_operation_id"`
	Hostname           string `json:"hostname"`
	PathPrefix         string `json:"path_prefix"`
	HostPort           uint16 `json:"host_port"`
}

// HostingProxySnapshot is a complete immutable desired generation, internal to dispatch.
type HostingProxySnapshot struct {
	ProxyID        string                    `json:"proxy_id"`
	ServerID       string                    `json:"server_id"`
	DestinationID  string                    `json:"destination_id"`
	RunnerID       string                    `json:"runner_id"`
	Generation     int64                     `json:"generation"`
	Configuration  HostingProxyConfiguration `json:"configuration"`
	Routes         []HostingProxyRouteTarget `json:"routes"`
	VerifiedSwitch *HostingVerifiedSwitch    `json:"verified_switch,omitempty"`
}

// HostingProxyPlan binds engine preparation and the committed predecessor to a snapshot.
type HostingProxyPlan struct {
	Snapshot           HostingProxySnapshot `json:"snapshot"`
	PreviousGeneration int64                `json:"previous_generation"`
	CompilerVersion    string               `json:"compiler_version"`
	DaemonID           string               `json:"daemon_id"`
	Hash               string               `json:"hash"`
}

// HostingProxyPreparedRequest pins local engine hashes without publishing operator paths.
type HostingProxyPreparedRequest struct {
	HostingAuthority
	ReceiptID string `json:"receipt_id"`
	HostingProxyPreparation
}

// HostingProxyPreparation contains committed nonsecret local engine identities.
type HostingProxyPreparation struct {
	DaemonID   string `json:"daemon_id"`
	EngineHash string `json:"engine_hash"`
	StaticHash string `json:"static_hash"`
	PlanHash   string `json:"plan_hash"`
}

// HostingProxyRouteObservation reports one generation's own routing and TLS status.
type HostingProxyRouteObservation struct {
	RouteID     string                  `json:"route_id"`
	HTTPReady   bool                    `json:"http_ready"`
	HTTPSReady  bool                    `json:"https_ready"`
	Certificate HostingProxyCertificate `json:"certificate"`
}

// HostingProxyResult is bounded nonsecret evidence retained before delivery.
type HostingProxyResult struct {
	Outcome          string                         `json:"outcome"`
	LoadedGeneration int64                          `json:"loaded_generation"`
	PlanHash         string                         `json:"plan_hash"`
	EngineHash       string                         `json:"engine_hash"`
	DaemonID         string                         `json:"daemon_id"`
	ContainerID      string                         `json:"container_id,omitempty"`
	Routes           []HostingProxyRouteObservation `json:"routes"`
}

// HostingProxyCompleteRequest reports external effects under one exact attempt.
type HostingProxyCompleteRequest struct {
	HostingAuthority
	ReceiptID string             `json:"receipt_id"`
	Result    HostingProxyResult `json:"result"`
}

// HostingProxyOperation is internal or server-administrator detail. Project reads
// must use HostingProxyOperationView and never serialize this whole snapshot.
type HostingProxyOperation struct {
	MetadataConflict     bool                     `json:"metadata_conflict,omitempty"`
	ID                   string                   `json:"id"`
	ProxyID              string                   `json:"proxy_id"`
	ProjectID            string                   `json:"project_id"`
	RouteID              string                   `json:"route_id,omitempty"`
	Generation           int64                    `json:"generation"`
	State                string                   `json:"state"`
	Snapshot             HostingProxySnapshot     `json:"snapshot"`
	Plan                 *HostingProxyPlan        `json:"plan,omitempty"`
	RunID                string                   `json:"run_id,omitempty"`
	MutationHold         bool                     `json:"mutation_hold"`
	RestoreOwnershipHeld bool                     `json:"restore_ownership_held"`
	PreparedReceipt      *HostingReceipt          `json:"prepared_receipt,omitempty"`
	CompleteReceipt      *HostingReceipt          `json:"complete_receipt,omitempty"`
	Preparation          *HostingProxyPreparation `json:"preparation,omitempty"`
	Result               *HostingProxyResult      `json:"result,omitempty"`
	CreatedBy            string                   `json:"created_by"`
}

// HostingProxyOperationView contains only an authorized project's route statuses.
type HostingProxyOperationView struct {
	MetadataConflict     bool           `json:"metadata_conflict,omitempty"`
	ID                   string         `json:"id"`
	State                string         `json:"state"`
	MutationHold         bool           `json:"mutation_hold"`
	RestoreOwnershipHeld bool           `json:"restore_ownership_held"`
	Routes               []HostingRoute `json:"routes"`
}

var proxyHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
var proxyLiteralPath = regexp.MustCompile(`^/[a-zA-Z0-9/_~.-]*$`)

// NormalizeHostingHostname applies IDNA lookup normalization before exact authorization.
func NormalizeHostingHostname(value string) (string, error) {
	if value != strings.TrimSpace(value) || strings.HasSuffix(value, ".") {
		return "", errors.New("hostname must not contain whitespace or a trailing dot")
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(value))
	if err != nil || len(host) > 253 || !proxyHostname.MatchString(host) {
		return "", errors.New("invalid route hostname")
	}
	return host, nil
}

// ValidateHostingRoutePath preserves literal segment matching and reserves engine paths.
func ValidateHostingRoutePath(value string) error {
	if len(value) > 256 || !proxyLiteralPath.MatchString(value) || path.Clean(value) != value || strings.Contains(value, "//") || value != "/" && strings.HasSuffix(value, "/") {
		return errors.New("path must be a canonical literal origin path")
	}
	for _, prefix := range []string{"/.well-known", "/dashboard", "/_nerocd"} {
		if strings.HasPrefix(value, prefix) {
			return errors.New("reserved route path")
		}
	}
	return nil
}

// ValidateHostingProxyConfiguration validates the closed ACME and operator setting contract.
func ValidateHostingProxyConfiguration(in HostingProxyConfiguration) error {
	a, err := mail.ParseAddress(in.Email)
	if err != nil || a.Address != in.Email || in.ManagementPort < 1024 || (in.CA != "letsencrypt" && in.CA != "letsencrypt-staging") {
		return errors.New("invalid proxy ACME or management configuration")
	}
	if in.GeneratedDomain != "" {
		host, err := NormalizeHostingHostname(in.GeneratedDomain)
		if err != nil || host != in.GeneratedDomain {
			return errors.New("generated domain must be canonical")
		}
	}
	return nil
}

// HostingHostnameGranted treats wildcard authority as an explicit descendant delegation.
// It never authorizes an apex hostname or a wildcard certificate request.
func HostingHostnameGranted(grant string, wildcard bool, hostname string) bool {
	if wildcard {
		return strings.HasSuffix(hostname, "."+grant)
	}
	return hostname == grant
}

// HostingHostnameGrantsOverlap detects cross-project delegation intersections.
func HostingHostnameGrantsOverlap(a string, aw bool, b string, bw bool) bool {
	return a == b || aw && HostingHostnameGranted(a, true, b) || bw && HostingHostnameGranted(b, true, a)
}

// CompileHostingProxyPlan freezes deterministic dispatch input before local preparation.
func CompileHostingProxyPlan(snapshot HostingProxySnapshot, previous int64, daemon string) (HostingProxyPlan, error) {
	if snapshot.Generation <= previous || previous < 0 || len(snapshot.Routes) > HostingProxyMaxRoutes || ValidateHostingProxyConfiguration(snapshot.Configuration) != nil {
		return HostingProxyPlan{}, errors.New("invalid proxy generation")
	}
	for _, id := range []string{snapshot.ProxyID, snapshot.ServerID, snapshot.DestinationID, snapshot.RunnerID} {
		if !hostingProxyID.MatchString(id) {
			return HostingProxyPlan{}, errors.New("invalid proxy placement identity")
		}
	}
	if len(daemon) > 128 || strings.ContainsFunc(daemon, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return HostingProxyPlan{}, errors.New("invalid proxy daemon identity")
	}
	snapshot.Routes = append([]HostingProxyRouteTarget{}, snapshot.Routes...)
	sort.Slice(snapshot.Routes, func(i, j int) bool { return snapshot.Routes[i].RouteID < snapshot.Routes[j].RouteID })
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range snapshot.Routes {
		for _, id := range []string{r.RouteID, r.ProjectID, r.ResourceID, r.BindingID, r.ReleaseOperationID} {
			if !hostingProxyID.MatchString(id) {
				return HostingProxyPlan{}, errors.New("invalid proxy route identity")
			}
		}
		host, err := NormalizeHostingHostname(r.Hostname)
		if err != nil || host != r.Hostname || ValidateHostingRoutePath(r.PathPrefix) != nil || ids[r.RouteID] || r.HostPort < 1024 || r.HostPort == snapshot.Configuration.ManagementPort || seen[r.Hostname+" "+r.PathPrefix] {
			return HostingProxyPlan{}, errors.New("invalid proxy route target")
		}
		seen[r.Hostname+" "+r.PathPrefix] = true
		ids[r.RouteID] = true
	}
	verification, err := canonicalHostingVerifiedSwitch(snapshot, previous)
	if err != nil {
		return HostingProxyPlan{}, err
	}
	snapshot.VerifiedSwitch = verification
	plan := HostingProxyPlan{Snapshot: snapshot, PreviousGeneration: previous, CompilerVersion: HostingProxyCompilerVersion, DaemonID: daemon}
	plan.Hash = HostingDocumentHash(plan)
	encoded, err := json.Marshal(plan)
	if err != nil || len(encoded) > 48*1024 {
		return HostingProxyPlan{}, errors.New("proxy plan exceeds journal document limit")
	}
	return plan, nil
}

var hostingProxyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ValidateHostingProxyPlan verifies the compiler version, canonical snapshot and hash.
func ValidateHostingProxyPlan(plan HostingProxyPlan) error {
	expected, err := CompileHostingProxyPlan(plan.Snapshot, plan.PreviousGeneration, plan.DaemonID)
	if err != nil {
		return err
	}
	if plan.CompilerVersion != expected.CompilerVersion || plan.Hash != expected.Hash || HostingDocumentHash(plan) != HostingDocumentHash(expected) {
		return errors.New("proxy plan is not canonical or its hash does not match")
	}
	return nil
}

// ValidateProxyApplyTask rejects embedded primitive or forged internal task input.
func ValidateProxyApplyTask(run TaskRun) error {
	spec := run.RunSpec
	if spec.Type != RunTypeProxyApply || spec.ProxyOperationID == "" || run.ID != "proxy_"+spec.ProxyOperationID || spec.ProxyMode != "apply" || spec.HostingOperationID != "" || spec.HostingMode != "" || spec.Process != nil || spec.Repository != nil || spec.Workflow != nil || len(spec.Inputs) != 0 || len(spec.Secrets) != 0 || len(spec.Artifacts) != 0 || len(run.Workflow.Steps) != 0 {
		return errors.New("invalid internal proxy apply task")
	}
	return nil
}
