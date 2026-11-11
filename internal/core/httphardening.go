package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Inbound request body caps (issue #97). Generous for real agent payloads
// while bounding memory against a hostile client on the tailnet.
const (
	// maxHookBodyBytes is the pre-request hook payload (conversation + metadata).
	maxHookBodyBytes = 8 << 20 // 8 MiB
	// maxProxyBodyBytes covers service-proxy request bodies (write ops, uploads).
	maxProxyBodyBytes = 16 << 20 // 16 MiB
)

// withBodyLimit wraps r.Body with http.MaxBytesReader so oversized inbound
// bodies cannot be buffered into memory (issue #97). Callers that ReadAll
// should use shared.WriteBodyReadError / shared.ReadBodyLimited to map overflow to 413.
func withBodyLimit(n int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next(w, r)
	}
}

// constantTimeEqual reports whether two strings are equal without leaking
// the position of the first mismatch (issue #98).
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// Compare against self to keep work roughly constant, then fail.
		subtle.ConstantTimeCompare([]byte(a), []byte(a))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// securityHeadersMiddleware sets defense-in-depth headers on every response
// (issue #98). HTML is the primary concern; JSON benefits from nosniff too.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Admin SPA and OAuth pages use a single inline <script>/<style> block
		// and same-origin XHR only. No third-party origins.
		h.Set("Content-Security-Policy",
			"default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// httpServerTimeouts returns the explicit server timeouts (issue #96).
// WriteTimeout stays unlimited so library fan-out and proxy streams are not
// cut off mid-response; ReadHeaderTimeout stops Slowloris.
func httpServerTimeouts() (readHeader, read, write, idle time.Duration) {
	return 10 * time.Second, 5 * time.Minute, 0, 2 * time.Minute
}

// newAegisHTTPServer builds the explicit http.Server used in production
// (issue #96): documented timeouts, no implicit defaults.
func newAegisHTTPServer(addr string, handler http.Handler) *http.Server {
	readHeader, read, write, idle := httpServerTimeouts()
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeader,
		ReadTimeout:       read,
		WriteTimeout:      write,
		IdleTimeout:       idle,
	}
}

// gracefulStop drains in-flight requests with a fixed deadline (issue #96).
func gracefulStop(srv *http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return srv.Shutdown(ctx)
}

// namedListener is one bound surface of a split deployment (external
// access): the API listener and the admin listener each get their own
// address and handler chain.
type namedListener struct {
	name string // "api" or "admin", for logs
	addr string
	http.Handler
}

// runHTTPServer serves a single listener (single-listener deployments).
// Returns nil on graceful shutdown.
func runHTTPServer(addr string, handler http.Handler, cfg *Config) error {
	return runHTTPServers([]namedListener{{name: "api", addr: addr, Handler: handler}}, cfg)
}

// runHTTPServers serves every listener with explicit timeouts, drains
// in-flight requests on SIGINT/SIGTERM, and reloads config on SIGHUP
// (issue #96). Any listener failing to bind fails startup; any listener
// erroring at runtime shuts the rest down. Returns nil on graceful shutdown.
func runHTTPServers(listeners []namedListener, cfg *Config) error {
	servers := make([]*http.Server, 0, len(listeners))
	errCh := make(chan error, len(listeners))
	for _, l := range listeners {
		srv := newAegisHTTPServer(l.addr, l.Handler)
		servers = append(servers, srv)
		go func() {
			errCh <- srv.ListenAndServe()
		}()
		slog.Info("listener up", "name", l.name, "addr", l.addr)
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	stopAll := func() error {
		var firstErr error
		for _, srv := range servers {
			if err := gracefulStop(srv, 30*time.Second); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}

	for {
		select {
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			// One listener died: bring the others down so the split
			// never serves half a deployment.
			_ = stopAll()
			return err
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGHUP:
				if cfg == nil {
					slog.Info("SIGHUP received but config reload unavailable")
					continue
				}
				if err := cfg.Reload(); err != nil {
					slog.Error("config reload failed", "error", err)
				} else {
					slog.Info("config reloaded", "services", cfg.ServiceCount())
				}
			case syscall.SIGINT, syscall.SIGTERM:
				slog.Info("shutdown signal received", "signal", sig.String())
				if err := stopAll(); err != nil {
					slog.Error("graceful shutdown failed", "error", err)
					return err
				}
				slog.Info("shutdown complete")
				return nil
			}
		}
	}
}
