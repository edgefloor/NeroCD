package api

import (
	"net/http"
	"strconv"
	"strings"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/store"
)

func (s *Server) runnersHandler(w http.ResponseWriter, r *http.Request) {
	runners, err := s.app.ListRunners(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginated(r, runners))
}

func (s *Server) runnerByID(w http.ResponseWriter, r *http.Request) {
	runner, err := s.app.RunnerByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runner)
}

func (s *Server) registerRunner(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerInput
	if !decodeBody(w, r, &req) {
		return
	}
	registered, err := s.app.RegisterRunner(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, registered)
}

func (s *Server) createRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerEnrollmentInput
	if !decodeBody(w, r, &req) {
		return
	}
	created, err := s.app.CreateRunnerEnrollment(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) revokeRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerEnrollmentRevokeInput
	if !decodeBody(w, r, &req) {
		return
	}
	revoked, err := s.app.RevokeRunnerEnrollment(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revoked)
}

func (s *Server) consumeRunnerEnrollment(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, auth.ErrUnauthenticated)
		return
	}
	var req app.RunnerEnrollmentConsumeInput
	if !decodeBody(w, r, &req) {
		return
	}
	consumed, err := s.app.ConsumeRunnerEnrollment(r.Context(), token, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, consumed)
}

func (s *Server) rotateRunnerToken(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerTokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	rotated, err := s.app.RotateRunnerToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rotated)
}

func (s *Server) revokeRunnerToken(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerTokenInput
	if !decodeBody(w, r, &req) {
		return
	}
	runner, err := s.app.RevokeRunnerToken(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runner)
}

func (s *Server) heartbeatRunner(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeBody(w, r, &req) {
		return
	}
	runner, err := s.app.HeartbeatRunner(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runner)
}

func (s *Server) runnerOperationalTelemetry(w http.ResponseWriter, r *http.Request) {
	var req app.RunnerOperationalTelemetry
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.app.RecordRunnerOperationalTelemetry(r.Context(), req); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) claimRun(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeBody(w, r, &req) {
		return
	}
	claim, err := s.app.ClaimRun(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claim)
}

func (s *Server) renewLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeaseID string `json:"lease_id"`
		Fence   string `json:"fence"`
		Attempt int    `json:"attempt"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	lease, err := s.app.RenewLease(r.Context(), req.LeaseID, req.Fence, req.Attempt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) runnerLease(w http.ResponseWriter, r *http.Request) {
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt is required"})
		return
	}
	lease, err := s.app.RunnerLease(r.Context(), r.URL.Query().Get("lease_id"), attempt, r.URL.Query().Get("fence"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) appendRunLog(w http.ResponseWriter, r *http.Request) {
	var req app.RunLogInput
	if !decodeBody(w, r, &req) {
		return
	}
	log, err := s.app.AppendRunLog(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, log)
}

func (s *Server) runnerDeploymentPlan(w http.ResponseWriter, r *http.Request) {
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if err != nil || attempt <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt is required"})
		return
	}
	v, err := s.app.RunnerDeploymentPlan(r.Context(), app.DeploymentPlanInput{DeploymentID: r.URL.Query().Get("deployment_id"), RunID: r.URL.Query().Get("run_id"), LeaseID: r.URL.Query().Get("lease_id"), Attempt: attempt, Fence: r.URL.Query().Get("fence")})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) runnerDeploymentStatus(w http.ResponseWriter, r *http.Request) {
	attempt, err := strconv.Atoi(r.URL.Query().Get("attempt"))
	if err != nil || attempt <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt is required"})
		return
	}
	v, err := s.app.RunnerDeploymentStatus(r.Context(), app.DeploymentPlanInput{DeploymentID: r.URL.Query().Get("deployment_id"), RunID: r.URL.Query().Get("run_id"), LeaseID: r.URL.Query().Get("lease_id"), Attempt: attempt, Fence: r.URL.Query().Get("fence")})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) resolveDeploymentProvenance(w http.ResponseWriter, r *http.Request) {
	var req app.ProvenanceResolutionInput
	if !decodeBody(w, r, &req) {
		return
	}
	v, err := s.app.ResolveDeploymentProvenance(r.Context(), req)
	if err != nil {
		s.logProvenanceConflict(err)
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) logProvenanceConflict(err error) {
	if reason := store.ProvenanceConflictReason(err); reason != "" {
		s.logger.Warn("provenance conflict", "provenance_conflict_class", reason)
	}
}

func (s *Server) failDeploymentAndCreateRollback(w http.ResponseWriter, r *http.Request) {
	var req app.DeploymentFailureRollbackInput
	if !decodeBody(w, r, &req) {
		return
	}
	v, err := s.app.FailDeploymentAndCreateRollback(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) appendRunEvents(w http.ResponseWriter, r *http.Request) {
	var req app.RunEventBatchInput
	if !decodeBody(w, r, &req) {
		return
	}
	ack, err := s.app.AppendRunEvents(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

func (s *Server) authorizeSecretAccess(w http.ResponseWriter, r *http.Request) {
	var req app.SecretAccessInput
	if !decodeBody(w, r, &req) {
		return
	}
	grant, err := s.app.AuthorizeSecretAccess(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	var req app.ArtifactInput
	if !decodeBody(w, r, &req) {
		return
	}
	artifact, err := s.app.CreateArtifact(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) completeLease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LeaseID       string `json:"lease_id"`
		Status        string `json:"status"`
		Fence         string `json:"fence"`
		Attempt       int    `json:"attempt"`
		CompletionKey string `json:"completion_key"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Attempt <= 0 || req.Fence == "" || strings.TrimSpace(req.CompletionKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attempt, fence, and completion_key are required"})
		return
	}
	lease, err := s.app.CompleteLease(r.Context(), req.LeaseID, req.Status, req.Attempt, req.Fence, req.CompletionKey)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) runLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.app.ListRunLogsPage(r.Context(), r.URL.Query().Get("run_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(logs))
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	artifacts, err := s.app.ListArtifactsPage(r.Context(), r.URL.Query().Get("run_id"), pageFromRequest(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, paginatedResult(artifacts))
}

func (s *Server) runnerPrimitivePlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.app.RunnerPrimitivePlan(r.Context(), r.URL.Query().Get("run_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
