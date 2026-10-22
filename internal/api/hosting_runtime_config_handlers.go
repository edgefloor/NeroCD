package api

import (
	"nerocd/internal/domain"
	"net/http"
)

func (s *Server) hostingRuntimeConfiguration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var result domain.HostingRuntimeConfiguration
	var err error
	if r.Method == http.MethodGet {
		result, err = s.app.GetHostingRuntimeConfiguration(r.Context(), r.PathValue("id"))
	} else {
		var in domain.HostingRuntimeConfigurationInput
		if !decodeBody(w, r, &in) {
			return
		}
		result, err = s.app.PutHostingRuntimeConfiguration(r.Context(), r.PathValue("id"), in)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime_configuration": result})
}
