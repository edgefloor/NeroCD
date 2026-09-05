package store

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"nerocd/internal/domain"
)

// Claiming examines at most this many candidates per request. Workflow
// readiness lives in Go, so bounded keyset scans retain that exact behavior
// without allowing an incompatible queue prefix to starve later work.
const (
	claimCandidateBatchSize  = 32
	claimCandidateMaxBatches = 8
	claimCandidateLimit      = claimCandidateBatchSize * claimCandidateMaxBatches
	// MaxPageLimit is enforced below the HTTP boundary too, so callers such as
	// workers and future transports cannot accidentally turn a list operation
	// into an unbounded database read.
	MaxPageLimit  = 100
	MaxPageOffset = 100_000
)

type memoryClaimCursor struct {
	claimOrderAt time.Time
	runID        string
}

func stringPointer(value string) *string { return &value }

// auditMetadata copies caller metadata before adding store-authoritative facts.
// It prevents a caller from retaining a map that later appears to change under
// it, while ensuring lease and approval identifiers come from the committed
// state rather than an API request.
func auditMetadata(metadata map[string]any, authoritative map[string]any) map[string]any {
	result := make(map[string]any, len(metadata)+len(authoritative))
	for key, value := range metadata {
		result[key] = value
	}
	for key, value := range authoritative {
		result[key] = value
	}
	return result
}

func (s *MemoryStore) auditIDAvailableLocked(id string) bool {
	if id == "" {
		return true
	}
	for _, event := range s.auditEvents {
		if event.ID == id {
			return false
		}
	}
	return true
}

func immutableGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Page specifies a bounded offset page for a list operation.
type Page struct {
	Limit   int
	Offset  int
	Enabled bool
}

// PageResult contains a page of items and the total available count.
type PageResult[T any] struct {
	Items  []T
	Limit  int
	Offset int
	Total  int
}

// MemoryStore is an in-memory implementation of the store repository contracts.
type MemoryStore struct {
	mu                    sync.RWMutex
	users                 []domain.User
	sessions              []domain.Session
	oidcIdentities        []domain.OIDCExternalIdentity
	oidcLoginFlows        []domain.OIDCLoginFlow
	apiTokens             []domain.APIToken
	tokenHashBySessionID  map[string]string
	projects              []domain.Project
	templates             []domain.TaskTemplate
	repositories          []domain.Repository
	accessKeys            []domain.AccessKey
	inventories           []domain.Inventory
	projectMembers        []domain.ProjectMember
	runs                  []domain.TaskRun
	runners               []domain.Runner
	runnerEnrollments     []domain.RunnerEnrollment
	leases                []domain.RunLease
	claimCursors          map[string]memoryClaimCursor
	claimOrderByRun       map[string]time.Time
	nextAttemptByRun      map[string]int
	logs                  []domain.RunLog
	artifacts             []domain.ArtifactRecord
	approvals             []domain.Approval
	auditEvents           []domain.AuditEvent
	services              []domain.Service
	serviceByID           map[string]int
	environments          []domain.Environment
	environmentByID       map[string]int
	revisions             []domain.Revision
	deployments           []domain.Deployment
	deploymentByID        map[string]int
	deploymentAttempts    []domain.DeploymentAttempt
	deploymentTransitions map[string]domain.DeploymentTransitionRequest
	deploymentCancels     map[string]domain.DeploymentCancelRequest
	provenanceReplays     map[string]memoryProvenanceReplay
	policyConfigurations  map[string]memoryPolicyConfiguration
	runnerObservations    map[string]memoryRunnerObservation
	retentionPolicy       domain.RunLogRetentionPolicy
	retentionReceipts     map[string]domain.RunLogRetentionExecution
}

type memoryPolicyConfiguration struct{ actorID, policyHash string }
type memoryRunnerObservation struct {
	observedAt                              time.Time
	journalDepth, retryCount, renewFailures int
}

type memoryProvenanceReplay struct {
	deploymentID, resolutionID, runID, leaseID, runnerID, fence string
	attempt                                                     int
	commit, hash                                                string
	digests                                                     []string
	revision                                                    domain.Revision
}

// NewMemoryStore deliberately starts empty. It is useful for isolated tests
// and explicitly requested disposable development sessions, but it never
// creates an administrator or any credentials on its own.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tokenHashBySessionID:  map[string]string{},
		claimCursors:          map[string]memoryClaimCursor{},
		claimOrderByRun:       map[string]time.Time{},
		policyConfigurations:  map[string]memoryPolicyConfiguration{},
		nextAttemptByRun:      map[string]int{},
		deploymentTransitions: map[string]domain.DeploymentTransitionRequest{},
		deploymentCancels:     map[string]domain.DeploymentCancelRequest{},
		provenanceReplays:     map[string]memoryProvenanceReplay{},
		runnerObservations:    map[string]memoryRunnerObservation{},
		retentionReceipts:     map[string]domain.RunLogRetentionExecution{},
		retentionPolicy:       domain.RunLogRetentionPolicy{KeepDays: 30, BatchSize: 1000, Version: 1},
		serviceByID:           map[string]int{},
		environmentByID:       map[string]int{},
		deploymentByID:        map[string]int{},
	}
}

func covers(values []string, required []string) bool {
	for _, item := range required {
		if !contains(values, item) {
			return false
		}
	}
	return true
}

func claimRunType(run domain.TaskRun) string {
	if len(run.Workflow.Steps) == 0 {
		return run.RunSpec.Type
	}
	statusByID := map[string]string{}
	for _, step := range run.WorkflowState.Steps {
		statusByID[step.ID] = step.Status
	}
	for _, step := range run.Workflow.Steps {
		status := statusByID[step.ID]
		if status == "" {
			status = domain.WorkflowPending
		}
		if status != domain.WorkflowPending {
			continue
		}
		ready := true
		for _, dependency := range step.DependsOn {
			if statusByID[dependency] != domain.RunSucceeded {
				ready = false
				break
			}
		}
		if ready {
			return step.RunSpec.Type
		}
	}
	return run.RunSpec.Type
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func primitivePlanForRun(run domain.TaskRun) domain.RunnerPrimitivePlan {
	plan := domain.RunnerPrimitivePlan{RunID: run.ID, Process: run.RunSpec.Process, Artifacts: run.RunSpec.Artifacts, Secrets: run.RunSpec.Secrets}
	if run.RunSpec.Repository != nil {
		dest := run.RunSpec.Repository.Path
		if dest == "" {
			dest = "workspace"
		}
		plan.Checkout = &domain.CheckoutPlan{Repository: *run.RunSpec.Repository, DestPath: dest}
	}
	return plan
}

func newLeaseToken(prefix string) (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}

func paginateSlice[T any](items []T, page Page) PageResult[T] {
	total := len(items)
	limit := page.Limit
	offset := page.Offset
	if !page.Enabled {
		limit = total
		offset = 0
	}
	if page.Enabled && limit == 0 {
		limit = total
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > MaxPageOffset {
		offset = MaxPageOffset
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return PageResult[T]{Items: items[offset:end], Limit: limit, Offset: offset, Total: total}
}
