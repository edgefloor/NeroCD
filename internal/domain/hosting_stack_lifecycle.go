package domain

import "errors"

// HostingStackLifecycleCapability admits explicit whole-stack stop and recreation.
const HostingStackLifecycleCapability = "hosting_stack_lifecycle_v1"

// HostingStackStopMode selects the bounded whole-stack removal task.
const HostingStackStopMode = "stack_stop"

// HostingStackStopCompiler versions immutable removal plans.
const HostingStackStopCompiler = "managed-compose.stop.v1"

// HostingStackStopInput binds the current deployment and reviewed configuration.
type HostingStackStopInput struct {
	IdempotencyKey              string `json:"idempotency_key"`
	ExpectedDeployedOperationID string `json:"expected_deployed_operation_id"`
	ExpectedGeneration          int64  `json:"expected_generation"`
}

// Validate checks the explicit deployment and configuration selection.
func (in HostingStackStopInput) Validate() error {
	if !hostingProxyID.MatchString(in.IdempotencyKey) || !hostingProxyID.MatchString(in.ExpectedDeployedOperationID) || in.ExpectedGeneration < 1 {
		return errors.New("invalid stack stop request")
	}
	return nil
}

// HostingStackLifecycle is settled operation evidence, never a live health sample.
type HostingStackLifecycle struct {
	ResourceID          string `json:"resource_id"`
	ResourceGeneration  int64  `json:"resource_generation"`
	State               string `json:"state"`
	DeployedOperationID string `json:"deployed_operation_id,omitempty"`
	StopOperationID     string `json:"stop_operation_id,omitempty"`
}

// HostingStackStopped is the explicit retained-storage predecessor for a new start.
type HostingStackStopped struct {
	StopOperationID string         `json:"stop_operation_id"`
	Target          HostingHealthy `json:"target"`
}

// Validate checks the retained stopped predecessor identity.
func (s HostingStackStopped) Validate() error {
	if !hostingProxyID.MatchString(s.StopOperationID) || s.StopOperationID == s.Target.OperationID || ValidateHostingStackHealthy(s.Target) != nil {
		return errors.New("invalid confirmed stopped stack")
	}
	return nil
}

// HostingStackStopPlan selects only an exact server-owned deployed generation.
type HostingStackStopPlan struct {
	Target          HostingHealthy   `json:"target"`
	TargetPlan      HostingApplyPlan `json:"target_plan"`
	CompilerVersion string           `json:"compiler_version"`
	Hash            string           `json:"hash"`
}

// CompileHostingStackStopPlan freezes the exact settled generation for removal.
func CompileHostingStackStopPlan(target HostingHealthy, plan HostingApplyPlan) (HostingStackStopPlan, error) {
	if ValidateHostingStackHealthy(target) != nil || plan.Stack == nil || target.Snapshot != plan.Snapshot || target.Release != plan.Release || target.PlanHash != plan.Hash {
		return HostingStackStopPlan{}, errors.New("invalid deployed stack stop target")
	}
	expected := plan
	expected.Hash = ""
	if plan.Hash != HostingDocumentHash(expected) || ValidateHostingStackState(plan, target.OperationID, *target.Stack, true) != nil {
		return HostingStackStopPlan{}, errors.New("stack stop target plan changed")
	}
	p := HostingStackStopPlan{Target: target, TargetPlan: plan, CompilerVersion: HostingStackStopCompiler}
	p.Hash = HostingDocumentHash(p)
	return p, nil
}

// Validate verifies the complete immutable removal plan hash.
func (p HostingStackStopPlan) Validate() error {
	expected, err := CompileHostingStackStopPlan(p.Target, p.TargetPlan)
	if err != nil || expected.Hash != p.Hash || p.CompilerVersion != HostingStackStopCompiler {
		return errors.New("invalid stack stop plan")
	}
	return nil
}

// ValidateHostingStackStoppedState admits only complete absence with retained origins.
func ValidateHostingStackStoppedState(p HostingStackStopPlan, state HostingStackState) error {
	if p.Validate() != nil || !state.ObservationComplete || len(state.Components) != 0 || state.PublicComponent != p.Target.Stack.PublicComponent || state.NetworkID != p.Target.Stack.NetworkID || len(state.Volumes) != len(p.Target.Stack.Volumes) {
		return errors.New("stack removal is not confirmed")
	}
	for i, volume := range state.Volumes {
		if volume != p.Target.Stack.Volumes[i] {
			return errors.New("stopped stack volume origin changed")
		}
	}
	return nil
}
