package core

import (
	"context"
	"net/http"
	"time"

	"github.com/fudomunro/aegis/internal/admin/apps"
	"github.com/fudomunro/aegis/internal/admin/scopes"
	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/metrics"
)

func agentForRequest(r *http.Request) string {
	if r != nil {
		if sc := scopes.From(r); sc != nil && sc.ID != "" {
			// External principals (invite tokens) audit by scope id, never
			// the shared agent-key label (issue #253).
			return "scope:" + sc.ID
		}
		if app := apps.AppIdentityFrom(r); app != nil && app.ID != "" {
			return "app:" + app.ID
		}
		if id := PeerIdentityFrom(r); id != nil && id.LoginName != "" {
			return id.LoginName
		}
	}
	return "api-key"
}

// recordAccess appends a redacted access event to the in-memory ring, the
// durable audit log, and the Prometheus counters (issue #105). Fail-open:
// a disabled metrics/audit sink never blocks the request path.
func (s *Server) recordAccess(ev metrics.AccessEvent) {
	if s == nil {
		return
	}
	if ev.Agent == "" {
		ev.Agent = "api-key"
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	if ev.Decision == "" {
		ev.Decision = audit.DecisionAllow
	}
	if s.metrics != nil {
		s.metrics.Record(ev)
	}
	if s.audit != nil {
		s.audit.Write(audit.Event{
			At:       ev.At,
			Agent:    ev.Agent,
			Surface:  ev.Surface,
			Target:   ev.Target,
			Kind:     ev.Kind,
			Decision: ev.Decision,
			Reason:   ev.Reason,
			Cache:    ev.Cache,
			Method:   ev.Method,
			Status:   ev.Status,
			ClientIP: ev.ClientIP,
		})
	}
	if s.prom != nil {
		s.prom.ObserveAccess(ev)
	}
}

type skipAccessMetricsKey struct{}

// withSkipAccessMetrics marks a request as admin/preview traffic. Admin
// Library views share the agent aggregate pipeline but must not inflate
// Home (map) access counts (issue #62).
func withSkipAccessMetrics(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), skipAccessMetricsKey{}, true))
}

func skipAccessMetrics(r *http.Request) bool {
	if r == nil {
		return false
	}
	skip, _ := r.Context().Value(skipAccessMetricsKey{}).(bool)
	return skip
}

// recordAccessFrom records agent access unless the request is admin UI
// preview traffic (issue #62).
func (s *Server) recordAccessFrom(r *http.Request, ev metrics.AccessEvent) {
	if skipAccessMetrics(r) {
		return
	}
	if ev.ClientIP == "" {
		ev.ClientIP = s.clientIPFor(r)
	}
	s.recordAccess(ev)
}
