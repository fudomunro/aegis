package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/syncjobs"
	"github.com/fudomunro/aegis/internal/shared"
)

// Maintenance signals (issue #86): the phase-1 observation bundle the sync
// maintenance loop collects from a live instance. Named "maintenance" to
// stay clear of the Signal messenger connector (signal.go / signal.db /
// /v1/admin/signal).
//
// Privacy posture matches the audit log and metrics: redacted metadata,
// never payloads. Failure text is scrubbed before it lands in a bundle, and
// bundles are written 0600 under <data-dir>/maintenance/ so the local
// scanner (and any later cloud compute) can read them as plain files.

const (
	// dataDirMaintenanceSubdir is the bundle directory under the data dir.
	dataDirMaintenanceSubdir = "maintenance"
	// signalErrorMax bounds failure text so a chatty upstream cannot bloat
	// bundles or smuggle payloads out of the instance.
	signalErrorMax = 280
	// signalFailureCap bounds the recent-failure list per bundle.
	signalFailureCap = 20
)

// MaintenanceSignals is one captured observation of instance health.
type MaintenanceSignals struct {
	CapturedAt time.Time          `json:"captured_at"`
	Hostname   string             `json:"hostname,omitempty"`
	Version    string             `json:"version"`
	Commit     string             `json:"commit"`
	Sync       *MaintenanceSync   `json:"sync,omitempty"`
	Freshness  []copies.Freshness `json:"freshness,omitempty"`
}

// MaintenanceSync is the sync-queue slice of a signal bundle.
type MaintenanceSync struct {
	Enabled   bool                           `json:"enabled"`
	Counts    syncjobs.SyncCounts            `json:"counts"`
	ByJobType map[string]syncjobs.SyncCounts `json:"by_job_type,omitempty"`
	ByKind    map[string]syncjobs.SyncCounts `json:"by_kind,omitempty"`
	ByService map[string]syncjobs.SyncCounts `json:"by_service,omitempty"`
	Failures  []MaintenanceFailure           `json:"failures,omitempty"`
}

// MaintenanceFailure is one recent failed sync job with scrubbed error text.
type MaintenanceFailure struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind,omitempty"`
	JobType    string    `json:"job_type"`
	Service    string    `json:"service,omitempty"`
	State      string    `json:"state"`
	ErrorClass string    `json:"error_class,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// MaintenanceCaptureResult is the POST /v1/admin/maintenance/signals
// response: where the bundle landed plus its contents.
type MaintenanceCaptureResult struct {
	Path    string             `json:"path"`
	Signals MaintenanceSignals `json:"signals"`
}

// CollectMaintenanceSignals snapshots the durable signal sources on this
// instance: sync-queue rollups and recent failures, freshness rollup, and
// the build stamp. In-memory only — writes happen via WriteMaintenanceSignals.
func (s *Server) CollectMaintenanceSignals() MaintenanceSignals {
	hostname, _ := os.Hostname()
	sig := MaintenanceSignals{
		CapturedAt: time.Now().UTC(),
		Hostname:   hostname,
		Version:    Version,
		Commit:     Commit,
	}
	if s.syncJobs != nil {
		sync := &MaintenanceSync{Enabled: s.syncJobs.Enabled()}
		if sync.Enabled {
			sync.Counts = s.syncJobs.Counts("", "")
			if b := s.syncJobs.Breakdown(); len(b.ByKind) > 0 || len(b.ByJobType) > 0 || len(b.ByService) > 0 {
				sync.ByKind = b.ByKind
				sync.ByJobType = b.ByJobType
				sync.ByService = b.ByService
			}
			for _, j := range s.syncJobs.List(syncjobs.SyncFilter{State: syncjobs.SyncJobFailed, Limit: signalFailureCap}) {
				sync.Failures = append(sync.Failures, MaintenanceFailure{
					ID:         j.ID,
					Kind:       j.Kind,
					JobType:    j.JobType,
					Service:    j.Service,
					State:      j.State,
					ErrorClass: j.LastErrorClass,
					Error:      scrubSignalError(j.LastError),
					Attempts:   j.Attempts,
					UpdatedAt:  j.UpdatedAt,
				})
			}
		}
		sig.Sync = sync
	}
	if s.copyLedger != nil {
		sig.Freshness = s.copyLedger.FreshnessRollup()
	}
	return sig
}

// WriteMaintenanceSignals writes sig as a 0600 JSON file under dir and
// returns its path. The file is self-contained: no local handles or
// references, so the scanner can move off-box later (issue #86).
func WriteMaintenanceSignals(dir string, sig MaintenanceSignals) (string, error) {
	if dir == "" {
		return "", errors.New("empty maintenance signals directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := "signals-" + sig.CapturedAt.UTC().Format("20060102T150405Z") + ".json"
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(sig, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// HandleAdminMaintenanceSignals reports (GET) or captures (POST) the signal
// bundle. Admin-scoped like the rest of /v1/admin — the bundle names services
// and failure modes and must not leak to agent credentials (issue #181).
func (s *Server) HandleAdminMaintenanceSignals(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shared.WriteJSON(w, s.CollectMaintenanceSignals())
	case http.MethodPost:
		if s.dataDir == "" {
			shared.WriteJSONError(w, http.StatusInternalServerError, "data directory not configured")
			return
		}
		sig := s.CollectMaintenanceSignals()
		path, err := WriteMaintenanceSignals(filepath.Join(s.dataDir, dataDirMaintenanceSubdir), sig)
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "capture failed: "+err.Error())
			return
		}
		shared.WriteJSON(w, MaintenanceCaptureResult{Path: path, Signals: sig})
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// signalSecretPatterns are the token-ish shapes scrubbed from failure text.
var (
	signalEmailRe    = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	signalBearerRe   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._\-+/=]{4,}`)
	signalSecretKVRe = regexp.MustCompile(`(?i)\b((?:access_)?token|api_?key|apikey|secret|signature|password|passwd)"?\s*[=:]\s*["']?[^\s"',;&]{2,}`)
)

// scrubSignalError redacts credentials and contact details from failure text
// and truncates it. Signal bundles leave the instance; error strings are the
// one place raw upstream text could ride along.
func scrubSignalError(s string) string {
	s = strings.TrimSpace(s)
	s = signalEmailRe.ReplaceAllString(s, "user@example.com")
	s = signalBearerRe.ReplaceAllString(s, "$1 REDACTED")
	s = signalSecretKVRe.ReplaceAllString(s, "${1}=REDACTED")
	runes := []rune(s)
	if len(runes) > signalErrorMax {
		s = string(runes[:signalErrorMax]) + "…"
	}
	return s
}
