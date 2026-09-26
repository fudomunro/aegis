package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
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
	// maxAdminBodyBytes covers admin JSON (permissions, hook-health paste, mirror).
	maxAdminBodyBytes = 4 << 20 // 4 MiB
	// maxMailBodyBytes covers mail import / sync control bodies.
	maxMailBodyBytes = 1 << 20 // 1 MiB
)

// withBodyLimit wraps r.Body with http.MaxBytesReader so oversized inbound
// bodies cannot be buffered into memory (issue #97). Callers that ReadAll
// should use writeBodyReadError / readBodyLimited to map overflow to 413.
func withBodyLimit(n int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next(w, r)
	}
}

// readBodyLimited reads r.Body under a cap. On overflow writes 413 and
// returns ok=false. On other read errors writes 400 and returns ok=false.
func readBodyLimited(w http.ResponseWriter, r *http.Request, max int64) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeBodyReadError(w, err)
		return nil, false
	}
	return data, true
}

// writeBodyReadError maps body-read failures to 413 (too large) or 400.
func writeBodyReadError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit))
		return
	}
	writeJSONError(w, http.StatusBadRequest, "failed to read request body")
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

// runHTTPServer serves mux with explicit timeouts, drains in-flight requests
// on SIGINT/SIGTERM, and reloads config on SIGHUP (issue #96).
// Returns nil on graceful shutdown.
func runHTTPServer(addr string, handler http.Handler, cfg *Config) error {
	srv := newAegisHTTPServer(addr, handler)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
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
				if err := gracefulStop(srv, 30*time.Second); err != nil {
					slog.Error("graceful shutdown failed", "error", err)
					return err
				}
				slog.Info("shutdown complete")
				return nil
			}
		}
	}
}
