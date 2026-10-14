package core

import (
	"net/http"
)

// Health and readiness probes (issue #105). Cheap, unauthenticated, and
// deliberately free of config/token detail so any tailnet peer (or a local
// process supervisor) can poll them without learning the API surface.

// HandleHealthz reports liveness: the process is up and the HTTP stack works.
func (s *Server) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// HandleReadyz reports readiness: config and the token store are loaded.
// Returns 503 while those are missing so orchestrators wait instead of
// sending traffic into a half-built Server.
func (s *Server) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ready := s != nil && s.config != nil && s.tokens != nil
	status := http.StatusOK
	body := map[string]string{"status": "ready"}
	if !ready {
		status = http.StatusServiceUnavailable
		body = map[string]string{"status": "not_ready"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	writeJSON(w, body)
}
