package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

var connectionAuthorityID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func connectionAuthorityReference(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func databaseConnectionRunnerPath(path string) bool {
	switch path {
	case "/api/v1/runners/hosting/database-binding/stage", "/api/v1/runners/hosting/database-binding/retirement-plan", "/api/v1/runners/hosting/database-binding/retirement", "/api/v1/runners/hosting/database-binding/authorize", "/api/v1/runners/hosting/database-binding/state":
		return true
	default:
		return false
	}
}

func databaseConnectionPath(path string) bool {
	if databaseConnectionRunnerPath(path) {
		return true
	}
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[0] != "" || parts[1] != "api" || parts[2] != "v1" || parts[3] != "resources" || parts[4] == "" {
		return false
	}
	if len(parts) == 6 {
		return parts[5] == "database-connections" || parts[5] == "database-connection"
	}
	return len(parts) == 7 && (parts[5] == "database-connections" && parts[6] == "options" || parts[5] == "database-connection" && (parts[6] == "deployments" || parts[6] == "recovery") || parts[5] == "database" && parts[6] == "maintenance")
}

// Domain protocol decoding rejects unknown/duplicate/case-aliased fields. The
// token pass also rejects null for shared request types without custom codecs.
func decodeDatabaseConnection(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "database connection accepts only a request body")
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingDatabaseConnectionJSONLimit+1))
	defer clear(raw)
	if len(raw) > domain.HostingDatabaseConnectionJSONLimit {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "database connection document exceeds limit")
		return false
	}
	if err != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid database connection document")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, e := decoder.Token()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil || token == nil {
			writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid database connection document")
			return false
		}
	}
	if domain.DecodeHostingStorageProtocolJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid database connection document")
		return false
	}
	return true
}

func databaseConnectionResponse(w http.ResponseWriter, status int, out any, err error) {
	if errors.Is(err, store.ErrHostingDatabaseConnectionUnavailable) {
		writeErrorEnvelope(w, http.StatusServiceUnavailable, "hosting_database_connection_unavailable", "database connections require a compatible controller backend")
		return
	}
	proxyResponse(w, status, out, err)
}

func (s *Server) connectHostingDatabase(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingDatabaseConnectionInput
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	out, err := s.app.ConnectHostingApplicationDatabase(r.Context(), r.PathValue("id"), in)
	databaseConnectionResponse(w, http.StatusAccepted, out, err)
}

func connectionReadRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "database connection read accepts only path parameters")
		return false
	}
	return true
}

func (s *Server) getHostingDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	if !connectionReadRequest(w, r) {
		return
	}
	out, err := s.app.GetHostingDatabaseConnection(r.Context(), r.PathValue("id"))
	databaseConnectionResponse(w, http.StatusOK, out, err)
}

func (s *Server) hostingDatabaseConnectionOptions(w http.ResponseWriter, r *http.Request) {
	if !connectionReadRequest(w, r) {
		return
	}
	items, err := s.app.ListEligibleHostingDatabases(r.Context(), r.PathValue("id"))
	if items == nil {
		items = []domain.HostingDatabaseConnectionOption{}
	}
	databaseConnectionResponse(w, http.StatusOK, map[string]any{"items": items}, err)
}

func (s *Server) deployHostingDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingDeploymentInput
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	out, err := s.app.RequestHostingApplicationDatabaseDeployment(r.Context(), r.PathValue("id"), in)
	databaseConnectionResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) recoverHostingDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingDatabaseConnectionRecoveryInput
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	out, err := s.app.RequestHostingDatabaseConnectionRecovery(r.Context(), r.PathValue("id"), in)
	databaseConnectionResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) maintainHostingDatabaseConnection(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingDatabaseMaintenanceInput
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	out, err := s.app.RequestHostingDatabaseMaintenance(r.Context(), r.PathValue("id"), in)
	databaseConnectionResponse(w, http.StatusAccepted, out, err)
}

func decodeDatabaseConnectionAuthority(w http.ResponseWriter, r *http.Request, in *domain.HostingAuthority) bool {
	if !decodeDatabaseConnection(w, r, in) {
		return false
	}
	if !connectionAuthorityID.MatchString(in.OperationID) || !connectionAuthorityID.MatchString(in.RunID) || !connectionAuthorityReference(in.LeaseID) || !connectionAuthorityReference(in.Fence) || in.Attempt < 1 || in.Attempt > math.MaxInt32 {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "exact operation authority is required")
		return false
	}
	return true
}

func (s *Server) runnerDatabaseConnectionStage(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingApplicationDatabaseStageRequest
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseStage(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerDatabaseConnectionRetirementPlan(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeDatabaseConnectionAuthority(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseRetirementPlan(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerDatabaseConnectionRetirement(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingApplicationDatabaseRetirementRequest
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseRetirement(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerDatabaseConnectionAuthorize(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingApplicationDatabaseEffectRequest
	if !decodeDatabaseConnection(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerAuthorizeHostingApplicationDatabaseEffect(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerDatabaseConnectionState(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeDatabaseConnectionAuthority(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingApplicationDatabaseState(r.Context(), token, in)
	databaseConnectionResponse(w, http.StatusOK, out, err)
}
