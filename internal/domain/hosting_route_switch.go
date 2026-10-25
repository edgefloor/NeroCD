package domain

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
)

// HostingProxyVerifiedSwitchCapability enables the opt-in verified switch protocol.
const HostingProxyVerifiedSwitchCapability = "hosting_proxy_verified_switch_v1"

// HostingRouteSwitchTargetPageSize bounds each authoritative target page.
const HostingRouteSwitchTargetPageSize = 50

// HostingRouteSwitchOptions is explanatory state, never admission authority.
type HostingRouteSwitchOptions struct {
	LoadedProxyOperationID *string                    `json:"loaded_proxy_operation_id"`
	ResourceID             string                     `json:"resource_id"`
	LoadedGeneration       *int64                     `json:"loaded_generation"`
	Routes                 []HostingRouteSwitchRoute  `json:"routes"`
	Targets                []HostingRouteSwitchTarget `json:"targets"`
	Available              bool                       `json:"available"`
	Reason                 string                     `json:"reason"`
	PendingOperationID     string                     `json:"pending_operation_id,omitempty"`
	NextAfterOperationID   *string                    `json:"next_after_operation_id"`
}

// HostingRouteSwitchRoute contains only the resource's enabled desired selection.
type HostingRouteSwitchRoute struct {
	ID                         string `json:"id"`
	Hostname                   string `json:"hostname"`
	PathPrefix                 string `json:"path_prefix"`
	Version                    int64  `json:"version"`
	SelectedReleaseOperationID string `json:"selected_release_operation_id"`
}

// HostingRouteSwitchTarget exposes immutable display identity without engine details.
type HostingRouteSwitchTarget struct {
	OperationID           string `json:"operation_id"`
	Kind                  string `json:"kind"`
	ResolvedImageIdentity string `json:"resolved_image_identity,omitempty"`
	SourceGeneration      int64  `json:"source_generation,omitempty"`
}

// HostingRouteSwitchSummary preserves the requested transition independently of current state.
type HostingRouteSwitchSummary struct {
	ExpectedLoadedGeneration int64                      `json:"expected_loaded_generation"`
	Routes                   []HostingRouteSwitchChange `json:"routes"`
}

// HostingRouteSwitchChange records one selected route's immutable transition.
type HostingRouteSwitchChange struct {
	RouteID                  string `json:"route_id"`
	BeforeReleaseOperationID string `json:"before_release_operation_id"`
	AfterReleaseOperationID  string `json:"after_release_operation_id"`
}

// HostingRouteSwitchOperationView returns a safe view only for an exact resource switch.
// The snapshot, engine result and other resources are never exposed, including to admins.
func HostingRouteSwitchOperationView(op HostingProxyOperation, resource, project string, routes []HostingRoute) (HostingProxyOperationView, bool) {
	v := op.Snapshot.VerifiedSwitch
	if v == nil || v.Version != 1 || v.ExpectedLoadedGeneration < 1 || len(v.Routes) == 0 || len(v.Routes) > HostingProxyMaxRoutes || len(op.Snapshot.Routes) > HostingProxyMaxRoutes || op.ProjectID != project {
		return HostingProxyOperationView{}, false
	}
	view := HostingProxyOperationView{ID: op.ID, State: op.State, MetadataConflict: op.MetadataConflict, MutationHold: op.MutationHold, RestoreOwnershipHeld: op.RestoreOwnershipHeld, Routes: make([]HostingRoute, 0), Switch: &HostingRouteSwitchSummary{ExpectedLoadedGeneration: v.ExpectedLoadedGeneration, Routes: make([]HostingRouteSwitchChange, 0, len(v.Routes))}}
	seen := make(map[string]bool, len(v.Routes))
	for _, prior := range v.Routes {
		if seen[prior.RouteID] || prior.PriorVersion < 1 || prior.PriorTarget.RouteID != prior.RouteID || !hostingProxyID.MatchString(prior.RouteID) || !hostingProxyID.MatchString(prior.PriorTarget.ReleaseOperationID) || prior.PriorTarget.ResourceID != resource || prior.PriorTarget.ProjectID != project {
			return HostingProxyOperationView{}, false
		}
		seen[prior.RouteID] = true
		found := false
		for _, target := range op.Snapshot.Routes {
			if target.RouteID == prior.RouteID && target.ResourceID == resource && target.ProjectID == project && hostingProxyID.MatchString(target.ReleaseOperationID) {
				view.Switch.Routes = append(view.Switch.Routes, HostingRouteSwitchChange{RouteID: prior.RouteID, BeforeReleaseOperationID: prior.PriorTarget.ReleaseOperationID, AfterReleaseOperationID: target.ReleaseOperationID})
				found = true
				break
			}
		}
		if !found {
			return HostingProxyOperationView{}, false
		}
	}
	for _, route := range routes {
		if route.ResourceID == resource {
			view.Routes = append(view.Routes, route)
		}
	}
	if op.Result != nil {
		switch op.Result.Outcome {
		case "applied", "rolled_back", "refused", "uncertain":
			view.EngineOutcome = op.Result.Outcome
		}
	}
	return view, true
}

// HostingRouteSwitchInput identifies settled releases and existing route versions.
// Hostnames, placement, grants and ports are always derived by the controller.
type HostingRouteSwitchInput struct {
	IdempotencyKey           string                        `json:"idempotency_key"`
	ExpectedLoadedGeneration int64                         `json:"expected_loaded_generation"`
	TargetReleaseOperationID string                        `json:"target_release_operation_id"`
	Routes                   []HostingRouteSwitchSelection `json:"routes"`
}

// HostingRouteSwitchSelection is one exact desired-route compare-and-swap.
type HostingRouteSwitchSelection struct {
	RouteID                            string `json:"route_id"`
	ExpectedVersion                    int64  `json:"expected_version"`
	ExpectedSelectedReleaseOperationID string `json:"expected_selected_release_operation_id"`
}

// HostingVerifiedSwitch binds rollback authority to the last applied generation.
type HostingVerifiedSwitch struct {
	Version                      int                          `json:"version"`
	ExpectedLoadedGeneration     int64                        `json:"expected_loaded_generation"`
	PriorAppliedProxyOperationID string                       `json:"prior_applied_proxy_operation_id"`
	Routes                       []HostingVerifiedSwitchRoute `json:"routes"`
}

// HostingVerifiedSwitchRoute retains the complete original route and desired version.
type HostingVerifiedSwitchRoute struct {
	RouteID      string                  `json:"route_id"`
	PriorVersion int64                   `json:"prior_version"`
	PriorTarget  HostingProxyRouteTarget `json:"prior_target"`
}

// CanonicalHostingRouteSwitchInput validates and copies selections in route-ID order.
func CanonicalHostingRouteSwitchInput(in HostingRouteSwitchInput) (HostingRouteSwitchInput, error) {
	if in.ExpectedLoadedGeneration < 1 || !hostingProxyID.MatchString(in.TargetReleaseOperationID) || len(in.Routes) == 0 || len(in.Routes) > HostingProxyMaxRoutes {
		return HostingRouteSwitchInput{}, errors.New("invalid verified route switch target or scope")
	}
	in.Routes = append([]HostingRouteSwitchSelection(nil), in.Routes...)
	sort.Slice(in.Routes, func(i, j int) bool { return in.Routes[i].RouteID < in.Routes[j].RouteID })
	for n, r := range in.Routes {
		if !hostingProxyID.MatchString(r.RouteID) || !hostingProxyID.MatchString(r.ExpectedSelectedReleaseOperationID) || r.ExpectedVersion < 1 || n > 0 && in.Routes[n-1].RouteID == r.RouteID {
			return HostingRouteSwitchInput{}, errors.New("verified route switch requires unique existing routes and positive versions")
		}
	}
	return in, nil
}

func canonicalHostingVerifiedSwitch(snapshot HostingProxySnapshot, previous int64) (*HostingVerifiedSwitch, error) {
	if snapshot.VerifiedSwitch == nil {
		return nil, nil
	}
	v := *snapshot.VerifiedSwitch
	if v.Version != 1 || previous < 1 || v.ExpectedLoadedGeneration != previous || !hostingProxyID.MatchString(v.PriorAppliedProxyOperationID) || len(v.Routes) == 0 || len(v.Routes) > len(snapshot.Routes) {
		return nil, errors.New("invalid verified switch predecessor")
	}
	v.Routes = append([]HostingVerifiedSwitchRoute(nil), v.Routes...)
	sort.Slice(v.Routes, func(i, j int) bool { return v.Routes[i].RouteID < v.Routes[j].RouteID })
	targets := make(map[string]HostingProxyRouteTarget, len(snapshot.Routes))
	for _, r := range snapshot.Routes {
		targets[r.RouteID] = r
	}
	var resource, project, binding, release string
	for n, r := range v.Routes {
		candidate, ok := targets[r.RouteID]
		prior := r.PriorTarget
		if !ok || prior.RouteID != r.RouteID || r.PriorVersion < 1 || n > 0 && v.Routes[n-1].RouteID == r.RouteID || !hostingProxyID.MatchString(prior.ReleaseOperationID) || prior.HostPort < 1024 || prior.HostPort == snapshot.Configuration.ManagementPort {
			return nil, errors.New("invalid verified switch selected route")
		}
		if n == 0 {
			resource, project, binding, release = candidate.ResourceID, candidate.ProjectID, candidate.BindingID, candidate.ReleaseOperationID
		}
		if candidate.ResourceID != resource || candidate.ProjectID != project || candidate.BindingID != binding || candidate.ReleaseOperationID != release {
			return nil, errors.New("verified switch must select one resource, binding, project and target release")
		}
		candidate.ReleaseOperationID = prior.ReleaseOperationID
		candidate.HostPort = prior.HostPort
		if candidate != prior {
			return nil, errors.New("verified switch changes immutable route identity or scope")
		}
	}
	return &v, nil
}

// RouteIDs returns copied canonical selected IDs from a compiled descriptor.
func (v HostingVerifiedSwitch) RouteIDs() []string {
	ids := make([]string, len(v.Routes))
	for n, r := range v.Routes {
		ids[n] = r.RouteID
	}
	return ids
}

// HostingProxyPriorRoutes returns the complete predecessor route set without aliasing.
func HostingProxyPriorRoutes(snapshot HostingProxySnapshot) []HostingProxyRouteTarget {
	routes := append([]HostingProxyRouteTarget(nil), snapshot.Routes...)
	if snapshot.VerifiedSwitch == nil {
		return routes
	}
	prior := make(map[string]HostingProxyRouteTarget, len(snapshot.VerifiedSwitch.Routes))
	for _, r := range snapshot.VerifiedSwitch.Routes {
		prior[r.RouteID] = r.PriorTarget
	}
	for n, r := range routes {
		if old, ok := prior[r.RouteID]; ok {
			routes[n] = old
		}
	}
	return routes
}

// ValidateHostingVerifiedSwitchResult validates strict result scope and TLS evidence.
// A zero acceptedAt checks historical journal evidence without claiming fresh TLS.
func ValidateHostingVerifiedSwitchResult(plan HostingProxyPlan, result HostingProxyResult, acceptedAt time.Time) error {
	v := plan.Snapshot.VerifiedSwitch
	if v == nil {
		return nil
	}
	if result.PlanHash != plan.Hash {
		return errors.New("verified switch result plan changed")
	}
	switch result.Outcome {
	case "uncertain":
		return nil
	case "refused":
		if result.LoadedGeneration != plan.PreviousGeneration || len(result.Routes) != 0 {
			return errors.New("verified switch refusal claims route effects")
		}
		return nil
	case "applied":
		if result.LoadedGeneration != plan.Snapshot.Generation {
			return errors.New("verified switch candidate generation differs")
		}
	case "rolled_back":
		if result.LoadedGeneration != plan.PreviousGeneration {
			return errors.New("verified switch rollback generation differs")
		}
	default:
		return errors.New("invalid verified switch outcome")
	}
	if len(result.Routes) != len(plan.Snapshot.Routes) {
		return errors.New("verified switch result requires the complete route set")
	}
	targets := make(map[string]HostingProxyRouteTarget, len(plan.Snapshot.Routes))
	for _, r := range plan.Snapshot.Routes {
		targets[r.RouteID] = r
	}
	selected := make(map[string]bool, len(v.Routes))
	for _, r := range v.Routes {
		selected[r.RouteID] = true
	}
	seen := make(map[string]bool, len(result.Routes))
	for _, r := range result.Routes {
		target, ok := targets[r.RouteID]
		if !ok || seen[r.RouteID] || r.Certificate.Hostname != target.Hostname {
			return errors.New("verified switch result route scope differs")
		}
		seen[r.RouteID] = true
		if selected[r.RouteID] {
			certificate := r.Certificate
			_, err := hex.DecodeString(certificate.Fingerprint)
			if !r.HTTPReady || !r.HTTPSReady || certificate.Status != "trusted" || len(certificate.Fingerprint) != 64 || strings.ToLower(certificate.Fingerprint) != certificate.Fingerprint || err != nil || certificate.ExpiresAt.IsZero() || !acceptedAt.IsZero() && !certificate.ExpiresAt.After(acceptedAt) {
				return errors.New("verified switch selected route lacks trusted HTTPS evidence")
			}
		}
	}
	return nil
}
