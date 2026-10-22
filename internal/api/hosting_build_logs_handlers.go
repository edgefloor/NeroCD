package api

import (
	"errors"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"net/http"
	"strconv"
)

func (s *Server) runnerBuildEvents(w http.ResponseWriter, r *http.Request) {
	var in domain.HostingBuildLogBatch
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.AppendHostingBuildLogs(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) hostingBuildLogs(w http.ResponseWriter, r *http.Request) {
	after, limit := 0, domain.HostingBuildLogReadLimit
	values := r.URL.Query()
	for key := range values {
		if (key != "after_sequence" && key != "limit") || len(values[key]) != 1 {
			writeError(w, app.InvalidInput(errors.New("invalid log query")))
			return
		}
	}
	var err error
	if value, ok := values["after_sequence"]; ok {
		after, err = strconv.Atoi(value[0])
		if err != nil {
			writeError(w, app.InvalidInput(err))
			return
		}
	}
	if value, ok := values["limit"]; ok {
		limit, err = strconv.Atoi(value[0])
		if err != nil {
			writeError(w, app.InvalidInput(err))
			return
		}
	}
	result, err := s.app.HostingBuildLogs(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
