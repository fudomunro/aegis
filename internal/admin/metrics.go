package admin

import (
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/shared"
)

// metricsSnapshot returns aggregated stats; empty maps when metrics are off.
func (h *Handlers) MetricsSnapshot(window time.Duration) metrics.MetricsSnapshot {
	if h == nil || h.Metrics == nil {
		if window <= 0 {
			window = time.Hour
		}
		return metrics.MetricsSnapshot{
			Window:    window,
			Services:  make(map[string]metrics.MetricsEndpointStats),
			Libraries: make(map[string]metrics.MetricsEndpointStats),
		}
	}
	if window <= 0 {
		window = time.Hour
	}
	return h.Metrics.Snapshot(window)
}

func (h *Handlers) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body := h.Prom.PromText()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(body))
}
