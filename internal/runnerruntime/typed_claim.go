package runnerruntime

import (
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// Typed executors obtain executable instructions from their fenced protocols.
// A generic plan must never select a different path or provide extra effects.
func validateTypedExecutionClaim(claim domain.ClaimedRun) error {
	switch strings.TrimSpace(claim.Run.RunSpec.Type) {
	case domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy, domain.RunTypeLocalBuildDeploy,
		domain.RunTypeDatabaseDeploy, domain.RunTypeProxyApply, domain.HostingBuildTaskType,
		domain.RunTypeStorageArchive, domain.RunTypeStorageBackupTransfer, domain.RunTypeStorageBackupRetention,
		domain.RunTypeDatabaseBackup, domain.RunTypeDatabaseBackupTransfer, domain.RunTypeDatabaseBackupRetention,
		domain.RunTypeDatabaseNetworkCapture:
	default:
		return nil
	}
	if strings.TrimSpace(claim.Run.RunSpec.Type) != claim.Run.RunSpec.Type {
		return errors.New("typed task type is not canonical")
	}
	plan := claim.PrimitivePlan
	if plan.Process != nil || plan.Checkout != nil || len(plan.Artifacts) != 0 || (claim.Run.RunSpec.Type != domain.RunTypeComposeDeploy && len(plan.Secrets) != 0) {
		return errors.New("typed task contains generic execution instructions")
	}
	if claim.Run.ID == "" || claim.Lease.ID == "" || claim.Lease.RunID != claim.Run.ID || claim.Lease.RunnerID == "" || claim.Lease.Attempt <= 0 || claim.Lease.Fence == "" || claim.Lease.Status != domain.LeaseActive || !claim.Lease.ExpiresAt.After(time.Now()) || (plan.RunID != "" && plan.RunID != claim.Run.ID) {
		return errors.New("typed task is missing fenced lease authority")
	}
	return nil
}

func validateComposeExecutionClaim(claim domain.ClaimedRun) error {
	spec := claim.Run.RunSpec
	if spec.Process != nil || spec.Repository != nil || len(spec.Artifacts) != 0 || spec.Workflow != nil || len(claim.Run.Workflow.Steps) != 0 || spec.HostingBuildID != "" || spec.HostingOperationID != "" || spec.HostingMode != "" || spec.ProxyMode != "" || spec.ProxyOperationID != "" {
		return errors.New("compose deployment contains generic execution instructions")
	}
	id, ok := spec.Inputs["deployment_id"].(string)
	if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || len(id) > 128 {
		return errors.New("compose deployment is missing deployment authority")
	}
	for key, value := range spec.Inputs {
		switch key {
		case "deployment_id", "environment_id", "desired_revision_id", "rollback_of_id":
		default:
			return errors.New("compose deployment contains unsupported inputs")
		}
		v, ok := value.(string)
		if !ok || v == "" || strings.TrimSpace(v) != v || len(v) > 128 {
			return errors.New("compose deployment contains invalid authority inputs")
		}
	}
	return nil
}
