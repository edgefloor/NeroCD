package api

import (
	"io"
	"math"
	"net/http"
	"regexp"

	"nerocd/internal/domain"
)

var captureReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var captureID = regexp.MustCompile(`^hnc_[0-9a-f]{32}$`)

func databaseNetworkCapturePath(path string) bool {
	switch path {
	case "/api/v1/runners/database-network-captures/claim", "/api/v1/runners/database-network-captures/claim-next", "/api/v1/runners/database-network-captures/plan", "/api/v1/runners/database-network-captures/origin":
		return true
	default:
		return false
	}
}

func decodeDatabaseNetworkCapture(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.URL.RawQuery != "" {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "capture accepts only a request body")
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, domain.HostingDatabaseBindingJSONLimit+1))
	defer clear(raw)
	if len(raw) > domain.HostingDatabaseBindingJSONLimit {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "capture document exceeds limit")
		return false
	}
	if err != nil || domain.DecodeHostingStorageProtocolJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid capture document")
		return false
	}
	return true
}

func (s *Server) runnerClaimDatabaseNetworkCapture(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CaptureID string `json:"capture_id"`
	}
	if !decodeDatabaseNetworkCapture(w, r, &in) {
		return
	}
	if !captureID.MatchString(in.CaptureID) {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "exact capture ID is required")
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerClaimHostingDatabaseNetworkCapture(r.Context(), token, in.CaptureID)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerClaimNextDatabaseNetworkCapture(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decodeDatabaseNetworkCapture(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerClaimNextHostingDatabaseNetworkCapture(r.Context(), token)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerDatabaseNetworkCapturePlan(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingAuthority
	if !decodeDatabaseNetworkCapture(w, r, &in) {
		return
	}
	if !captureID.MatchString(in.OperationID) || !captureReference.MatchString(in.RunID) || !captureReference.MatchString(in.LeaseID) || !captureReference.MatchString(in.Fence) || in.Attempt < 1 || in.Attempt > math.MaxInt32 {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "exact capture authority is required")
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerHostingDatabaseNetworkCapturePlan(r.Context(), token, in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerRecordDatabaseNetworkOrigin(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingDatabaseNetworkOriginRequest
	if !decodeDatabaseNetworkCapture(w, r, &in) {
		return
	}
	token, _ := bearerToken(r.Header.Get("Authorization"))
	out, err := s.app.RunnerRecordHostingDatabaseNetworkOrigin(r.Context(), token, in)
	proxyResponse(w, http.StatusOK, out, err)
}
