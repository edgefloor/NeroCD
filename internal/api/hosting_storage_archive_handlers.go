package api

import (
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"

	"nerocd/internal/domain"
)

func hostingStorageArchivePath(path string) bool {
	p := strings.Split(path, "/")
	if len(p) < 5 || p[0] != "" || p[1] != "api" || p[2] != "v1" || p[4] == "" {
		return false
	}
	if len(p) == 5 {
		return p[3] == "storage-backups" || p[3] == "storage-restores"
	}
	return len(p) == 6 && (p[3] == "resources" && (p[5] == "storage-backups" || p[5] == "storage-backup-volumes") || p[3] == "storage-backups" && p[5] == "restores" || p[3] == "hosting-operations" && p[5] == "storage-archive-recovery")
}

func hostingStorageArchiveRunnerPath(path string) bool {
	switch path {
	case "/api/v1/runners/hosting/storage-archive/plan", "/api/v1/runners/hosting/storage-archive/prepared", "/api/v1/runners/hosting/storage-archive/apply-begin", "/api/v1/runners/hosting/storage-archive/progress", "/api/v1/runners/hosting/storage-archive/complete":
		return true
	default:
		return false
	}
}

func archiveRequestParameters(w http.ResponseWriter, r *http.Request, list bool) bool {
	valid := r.URL.RawQuery == ""
	if list {
		query, err := url.ParseQuery(r.URL.RawQuery)
		valid = err == nil
		for key := range query {
			if key != "limit" && key != "offset" {
				valid = false
			}
		}
	}
	if !valid || r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		writeErrorEnvelope(w, http.StatusBadRequest, "bad_request", "archive metadata accepts only declared parameters")
		return false
	}
	return true
}

func decodeStorageArchive(w http.ResponseWriter, r *http.Request, out any) bool {
	limit := 4096
	if hostingStorageArchiveRunnerPath(r.URL.Path) {
		limit = domain.HostingStorageArchiveJSONLimit
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if len(raw) > limit {
		writeErrorEnvelope(w, http.StatusRequestEntityTooLarge, "request_too_large", "archive document exceeds limit")
		return false
	}
	if err != nil || domain.DecodeHostingStorageArchiveJSON(raw, out) != nil {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "invalid archive JSON")
		return false
	}
	return true
}

func (s *Server) hostingStorageBackups(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, r.Method == http.MethodGet) {
		return
	}
	if r.Method == http.MethodGet {
		out, err := s.app.ListStorageBackups(r.Context(), r.PathValue("id"), pageFromRequest(r))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, paginatedResult(out))
		return
	}
	var in domain.HostingStorageBackupInput
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RequestStorageBackup(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) hostingStorageBackup(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	out, err := s.app.GetStorageBackup(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) requestStorageRestore(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageRestoreInput
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RequestStorageRestore(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) hostingStorageRestore(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	out, err := s.app.GetStorageRestore(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) requestStorageArchiveRecovery(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageArchiveRecoveryInput
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RequestStorageArchiveRecovery(r.Context(), r.PathValue("id"), in)
	proxyResponse(w, http.StatusAccepted, out, err)
}

func (s *Server) runnerStorageArchivePlan(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingAuthority
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	if in.OperationID == "" || in.RunID == "" || in.LeaseID == "" || in.Fence == "" || in.Attempt < 1 || in.Attempt > math.MaxInt32 {
		writeErrorEnvelope(w, http.StatusBadRequest, "invalid_request", "archive authority fields are required")
		return
	}
	out, err := s.app.RunnerStorageArchivePlan(r.Context(), in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerStorageArchivePrepared(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageArchivePreparedRequest
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RunnerStorageArchivePrepared(r.Context(), in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerStorageArchiveApplyBegin(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageArchiveApplyBeginRequest
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RunnerStorageArchiveApplyBegin(r.Context(), in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerStorageArchiveProgress(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageArchiveProgress
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RunnerStorageArchiveProgress(r.Context(), in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) runnerStorageArchiveComplete(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	var in domain.HostingStorageArchiveCompleteRequest
	if !decodeStorageArchive(w, r, &in) {
		return
	}
	out, err := s.app.RunnerStorageArchiveComplete(r.Context(), in)
	proxyResponse(w, http.StatusOK, out, err)
}

func (s *Server) hostingStorageBackupVolumes(w http.ResponseWriter, r *http.Request) {
	if !archiveRequestParameters(w, r, false) {
		return
	}
	out, err := s.app.StorageBackupVolumes(r.Context(), r.PathValue("id"))
	proxyResponse(w, http.StatusOK, out, err)
}
