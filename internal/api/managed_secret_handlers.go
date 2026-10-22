package api

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"nerocd/internal/app"
)

func (s *Server) managedSecretsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListManagedSecrets(r.Context(), r.URL.Query().Get("project_id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in app.ManagedSecretInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.CreateManagedSecret(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) managedSecretByID(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.GetManagedSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) managedSecretVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := s.app.ListManagedSecretVersions(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(result))
		return
	}
	var in app.ManagedSecretRotateInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RotateManagedSecret(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
func (s *Server) managedSecretRevoke(w http.ResponseWriter, r *http.Request) {
	var in app.ManagedSecretRevokeInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RevokeManagedSecret(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) managedSecretReveal(w http.ResponseWriter, r *http.Request) {
	var in app.ManagedSecretRevealInput
	if !decodeBody(w, r, &in) {
		return
	}
	result, err := s.app.RevealManagedSecret(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// encoding/json substitutes invalid Unicode. Managed values must preserve exact valid UTF-8 bytes.
func validManagedJSONUnicode(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		code, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
