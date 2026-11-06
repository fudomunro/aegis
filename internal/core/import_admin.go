package core

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
)

// importStoreAdmin is one admin surface over a local import store: GET
// status and POST import with per-store DTOs. The route shape and response
// fields stay per-store (admin.html reads them); the HTTP plumbing lives
// here once (issue #266).
type importStoreAdmin struct {
	prefix     string // URL prefix, e.g. "/v1/admin/quicken"
	flagName   string // disabling flag, e.g. "-quicken-store"
	pathHint   string // error copy for a missing import path
	serviceID  string // service whose caches invalidate after import
	store      connectors.ImportStore
	status     func() any
	importPath func(path string) (any, error)
	logAttrs   func(res any) []any
	// bareResult returns the import result without the {result,status}
	// envelope (Timeline's historical response shape).
	bareResult bool
}

// HandleAdminImportStore serves GET <prefix>/status and POST <prefix>/import.
func (s *Server) HandleAdminImportStore(w http.ResponseWriter, r *http.Request, spec importStoreAdmin) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, spec.prefix), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(spec.status())
	case path == "import":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.runAdminImportStore(w, r, spec)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) runAdminImportStore(w http.ResponseWriter, r *http.Request, spec importStoreAdmin) {
	if spec.store == nil || !spec.store.Enabled() {
		writeJSONError(w, http.StatusBadRequest, "store disabled — set "+spec.flagName)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required — "+spec.pathHint)
		return
	}
	res, err := spec.importPath(req.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Library cache must not serve the old empty list.
	s.invalidateService(spec.serviceID)
	attrs := []any{"path", req.Path}
	if spec.logAttrs != nil {
		attrs = append(attrs, spec.logAttrs(res)...)
	}
	slog.Info(strings.TrimPrefix(spec.prefix, "/v1/admin/")+" import from admin UI", attrs...)
	w.Header().Set("Content-Type", "application/json")
	if spec.bareResult {
		json.NewEncoder(w).Encode(res)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"status": spec.status(),
	})
}
