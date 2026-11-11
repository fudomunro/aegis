package syncjobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/core/copies"
)

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
	Enabled   bool                  `json:"enabled"`
	Counts    SyncCounts            `json:"counts"`
	ByJobType map[string]SyncCounts `json:"by_job_type,omitempty"`
	ByKind    map[string]SyncCounts `json:"by_kind,omitempty"`
	ByService map[string]SyncCounts `json:"by_service,omitempty"`
	Failures  []MaintenanceFailure  `json:"failures,omitempty"`
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

// CollectMaintenanceSignals snapshots the durable signal sources on this
// instance: sync-queue rollups and recent failures, freshness rollup, and
// the build stamp. In-memory only — writes happen through
// WriteMaintenanceSignals.
func (r *Runners) CollectMaintenanceSignals() MaintenanceSignals {
	hostname, _ := os.Hostname()
	sig := MaintenanceSignals{
		CapturedAt: time.Now().UTC(),
		Hostname:   hostname,
		Version:    r.Version,
		Commit:     r.Commit,
	}
	if r.Jobs != nil {
		sync := &MaintenanceSync{Enabled: r.Jobs.Enabled()}
		if sync.Enabled {
			sync.Counts = r.Jobs.Counts("", "")
			if b := r.Jobs.Breakdown(); len(b.ByKind) > 0 || len(b.ByJobType) > 0 || len(b.ByService) > 0 {
				sync.ByKind = b.ByKind
				sync.ByJobType = b.ByJobType
				sync.ByService = b.ByService
			}
			for _, j := range r.Jobs.List(SyncFilter{State: SyncJobFailed, Limit: signalFailureCap}) {
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
	if r.Copies != nil {
		sig.Freshness = r.Copies.FreshnessRollup()
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

// WriteMaintenanceSignals writes sig as a 0600 JSON file under the
// instance's maintenance directory and returns its path. The file is
// self-contained: no local handles or references, so the scanner can move
// off-box later (issue #86).
func (r *Runners) WriteMaintenanceSignals(sig MaintenanceSignals) (string, error) {
	return writeMaintenanceSignals(filepath.Join(r.DataDir, dataDirMaintenanceSubdir), sig)
}

func writeMaintenanceSignals(dir string, sig MaintenanceSignals) (string, error) {
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
