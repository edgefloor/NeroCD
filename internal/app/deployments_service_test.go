package app

import (
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestValidImageReferenceRequiresUntaggedRepositoryDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, value := range []string{
		"registry.example/app@sha256:" + digest,
		"registry.example:5443/ns/app@sha256:" + digest,
	} {
		if !validImageReference(value) {
			t.Fatalf("validImageReference(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"sha256:" + digest,
		"registry.example/app:latest@sha256:" + digest,
		"registry.example/app@sha256:" + strings.Repeat("A", 64),
	} {
		if validImageReference(value) {
			t.Fatalf("validImageReference(%q) = true, want false", value)
		}
	}
}

func TestServerOwnedRollbackIDsAreStableAndScoped(t *testing.T) {
	depA, runA := domain.RollbackObjectIDs("dep_source", "failure_receipt")
	depB, runB := domain.RollbackObjectIDs("dep_source", "failure_receipt")
	if depA != depB || runA != runB || depA == "" || runA == "" {
		t.Fatalf("rollback IDs are not stable: %q/%q %q/%q", depA, runA, depB, runB)
	}
	if depC, _ := domain.RollbackObjectIDs("dep_source", "other_receipt"); depC == depA {
		t.Fatal("request IDs must not share recovery objects")
	}
}

func TestComposeRunnerPoolRejectsGenericShell(t *testing.T) {
	if !composeRunnerHasGenericShell([]string{domain.RunTypeComposeDeploy, domain.RunTypeShell}) {
		t.Fatal("compose pool must reject generic shell")
	}
	if composeRunnerHasGenericShell([]string{domain.RunTypeComposeDeploy}) {
		t.Fatal("compose-only runner should be valid")
	}
}

func TestFencedDeploymentTransitionTable(t *testing.T) {
	statuses := []domain.DeploymentStatus{
		domain.DeploymentQueued, domain.DeploymentWaitingConfirmation, domain.DeploymentAssigned,
		domain.DeploymentPreparing, domain.DeploymentApplying, domain.DeploymentVerifying,
		domain.DeploymentCancelRequested, domain.DeploymentRollingBack, domain.DeploymentSucceeded,
		domain.DeploymentFailed, domain.DeploymentCanceled, domain.DeploymentRolledBack,
		domain.DeploymentRollbackFailed, domain.DeploymentManualIntervention,
	}
	allowed := map[[2]domain.DeploymentStatus]bool{
		{domain.DeploymentQueued, domain.DeploymentWaitingConfirmation}:         true,
		{domain.DeploymentQueued, domain.DeploymentAssigned}:                    true,
		{domain.DeploymentQueued, domain.DeploymentFailed}:                      true,
		{domain.DeploymentQueued, domain.DeploymentCanceled}:                    true,
		{domain.DeploymentWaitingConfirmation, domain.DeploymentFailed}:         true,
		{domain.DeploymentWaitingConfirmation, domain.DeploymentCanceled}:       true,
		{domain.DeploymentAssigned, domain.DeploymentPreparing}:                 true,
		{domain.DeploymentAssigned, domain.DeploymentFailed}:                    true,
		{domain.DeploymentAssigned, domain.DeploymentCanceled}:                  true,
		{domain.DeploymentPreparing, domain.DeploymentApplying}:                 true,
		{domain.DeploymentPreparing, domain.DeploymentFailed}:                   true,
		{domain.DeploymentPreparing, domain.DeploymentCanceled}:                 true,
		{domain.DeploymentApplying, domain.DeploymentVerifying}:                 true,
		{domain.DeploymentApplying, domain.DeploymentCancelRequested}:           true,
		{domain.DeploymentVerifying, domain.DeploymentSucceeded}:                true,
		{domain.DeploymentVerifying, domain.DeploymentCancelRequested}:          true,
		{domain.DeploymentCancelRequested, domain.DeploymentManualIntervention}: true,
	}
	for _, from := range statuses {
		for _, to := range statuses {
			want := allowed[[2]domain.DeploymentStatus{from, to}]
			if got := fencedDeploymentTransitionAllowed(false, from, to); got != want {
				t.Errorf("transition %s -> %s allowed=%t, want %t", from, to, got, want)
			}
		}
	}
	if !fencedDeploymentTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentRolledBack) || !fencedDeploymentTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentRollbackFailed) || fencedDeploymentTransitionAllowed(true, domain.DeploymentVerifying, domain.DeploymentSucceeded) || fencedDeploymentTransitionAllowed(true, domain.DeploymentPreparing, domain.DeploymentFailed) {
		t.Fatal("rollback-child table admitted an ordinary terminal route")
	}
}
