package api

import (
	"net/http"

	"nerocd/internal/app"
)

func (s *Server) runLogRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status, err := s.app.RunLogRetentionStatus(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPut:
		var input app.RunLogRetentionPolicyInput
		if !decodeBody(w, r, &input) {
			return
		}
		status, err := s.app.UpdateRunLogRetentionPolicy(r.Context(), input)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) runLogRetentionPreview(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decodeBody(w, r, &body) {
		return
	}
	status, err := s.app.RunLogRetentionStatus(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status.Preview)
}

func (s *Server) runLogRetentionExecute(w http.ResponseWriter, r *http.Request) {
	var input app.RunLogRetentionExecuteInput
	if !decodeBody(w, r, &input) {
		return
	}
	execution, err := s.app.ExecuteRunLogRetention(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, execution)
}
