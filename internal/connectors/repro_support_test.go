package connectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Repro fixtures (issue #86): scrubbed request/response captures that make a
// sync failure reproducible offline. A fixture is both the evidence attached
// to a maintenance ticket and the regression test a fix PR turns green —
// replayed through httptest with the service endpoint pointed at the replay
// server, the same seam as every other connector test.
//
// Capture happens on-instance and must be scrubbed before it travels
// anywhere: SaveReproFixture always runs ScrubFixture. Authoring rules that
// no generic scrubber can enforce (truncate message bodies and photo bytes
// to the shape-revealing prefix; synthetic fixtures when scrubbing cannot
// make a capture safe) live in docs/sync-maintenance.md.

// ReproFixture is one recorded provider exchange set for a failure signature.
type ReproFixture struct {
	Service    string          `json:"service"`
	Signature  string          `json:"signature"`
	CapturedAt string          `json:"captured_at,omitempty"`
	Notes      string          `json:"notes,omitempty"`
	Exchanges  []ReproExchange `json:"exchanges"`
}

// ReproExchange is one request/response pair, in recorded order.
type ReproExchange struct {
	Request  ReproRequest  `json:"request"`
	Response ReproResponse `json:"response"`
}

// ReproRequest is the recorded outbound request. Path carries the request
// URI (path + scrubbed query), never the origin — fixtures are endpoint-agnostic.
type ReproRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// ReproResponse is the recorded upstream response.
type ReproResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// droppedHeaders never travel into a fixture.
var droppedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
}

// secretNameRe matches header names whose values are credentials.
var secretNameRe = regexp.MustCompile(`(?i)(token|key|secret|auth|password|passwd|sig)`)

var (
	reproEmailRe    = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	reproBearerRe   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9._\-+/=]{4,}`)
	reproSecretKVRe = regexp.MustCompile(`(?i)\b((?:access_)?token|api_?key|apikey|secret|signature|password|passwd)"?\s*[=:]\s*["']?[^\s"',;&]{2,}`)
)

// ScrubText redacts credentials and contact details from captured text.
func ScrubText(s string) string {
	s = reproEmailRe.ReplaceAllString(s, "user@example.com")
	s = reproBearerRe.ReplaceAllString(s, "$1 REDACTED")
	s = reproSecretKVRe.ReplaceAllString(s, "${1}=REDACTED")
	return s
}

// ScrubFixture returns a deep-scrubbed copy of f: credential headers dropped
// or redacted, secret-looking values scrubbed in paths, headers, and bodies.
func ScrubFixture(f *ReproFixture) *ReproFixture {
	if f == nil {
		return nil
	}
	out := *f
	out.CapturedAt = ScrubText(f.CapturedAt)
	out.Notes = ScrubText(f.Notes)
	out.Exchanges = make([]ReproExchange, 0, len(f.Exchanges))
	for _, ex := range f.Exchanges {
		out.Exchanges = append(out.Exchanges, ReproExchange{
			Request: ReproRequest{
				Method:  ex.Request.Method,
				Path:    ScrubText(ex.Request.Path),
				Headers: scrubHeaderMap(ex.Request.Headers),
				Body:    ScrubText(ex.Request.Body),
			},
			Response: ReproResponse{
				Status:  ex.Response.Status,
				Headers: scrubHeaderMap(ex.Response.Headers),
				Body:    ScrubText(ex.Response.Body),
			},
		})
	}
	return &out
}

// scrubHeaderMap drops credential headers outright and redacts secret-looking
// names; remaining values pass through ScrubText.
func scrubHeaderMap(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		if droppedHeaders[lk] {
			continue
		}
		if secretNameRe.MatchString(lk) {
			out[k] = "REDACTED"
			continue
		}
		out[k] = ScrubText(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LoadReproFixture reads a fixture file.
func LoadReproFixture(path string) (*ReproFixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f ReproFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse repro fixture %s: %w", path, err)
	}
	if len(f.Exchanges) == 0 {
		return nil, fmt.Errorf("repro fixture %s has no exchanges", path)
	}
	return &f, nil
}

// SaveReproFixture writes f as indented JSON, scrubbed first — a capture must
// never land on disk (or in git) with credentials in it.
func SaveReproFixture(path string, f *ReproFixture) error {
	safe := ScrubFixture(f)
	if safe.Signature == "" {
		return fmt.Errorf("repro fixture needs a signature")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReproRecorder is an http.RoundTripper that records exchanges for a fixture.
// Point a connector's client at it during a live capture run, then
// Fixture() the scrubbed snapshot. Bodies are buffered and restored so the
// caller's request/response stream is untouched.
type ReproRecorder struct {
	Service   string
	Signature string
	Notes     string
	// Next is the transport that actually performs the call. Nil uses
	// http.DefaultTransport.
	Next http.RoundTripper

	mu        sync.Mutex
	exchanges []ReproExchange
}

// RoundTrip records the exchange and delegates to Next.
func (r *ReproRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	next := r.Next
	if next == nil {
		next = http.DefaultTransport
	}
	reqBody, err := readAndRestoreBody(&req.Body)
	if err != nil {
		return nil, err
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	respBody, err := readAndRestoreBody(&resp.Body)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.exchanges = append(r.exchanges, ReproExchange{
		Request: ReproRequest{
			Method:  req.Method,
			Path:    req.URL.RequestURI(),
			Headers: headerValues(req.Header),
			Body:    string(reqBody),
		},
		Response: ReproResponse{
			Status:  resp.StatusCode,
			Headers: headerValues(resp.Header),
			Body:    string(respBody),
		},
	})
	r.mu.Unlock()
	return resp, nil
}

// Fixture returns the scrubbed snapshot of everything recorded so far.
func (r *ReproRecorder) Fixture() *ReproFixture {
	r.mu.Lock()
	exchanges := make([]ReproExchange, len(r.exchanges))
	copy(exchanges, r.exchanges)
	r.mu.Unlock()
	return ScrubFixture(&ReproFixture{
		Service:    r.Service,
		Signature:  r.Signature,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Notes:      r.Notes,
		Exchanges:  exchanges,
	})
}

func readAndRestoreBody(body *io.ReadCloser) ([]byte, error) {
	if body == nil || *body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(*body)
	(*body).Close()
	if err != nil {
		return nil, err
	}
	*body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

func headerValues(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[k] = strings.Join(vs, ", ")
	}
	return out
}

// NewReplayServer serves a fixture's exchanges over HTTP so a connector can
// be pointed at it through the configured service endpoint. Requests match
// recorded exchanges by method and request URI (path + query), then by
// method and path; unmatched requests get 404. Response bodies that point at
// other recorded request URIs (Spotify's "next", OData @odata.nextLink) are
// rewritten to this server so pagination replays offline.
func NewReplayServer(fx *ReproFixture) *httptest.Server {
	type slot struct {
		ex   ReproExchange
		used bool
	}
	var mu sync.Mutex
	exact := map[string][]*slot{}
	byPath := map[string][]*slot{}
	recorded := map[string]string{}
	for _, ex := range fx.Exchanges {
		s := &slot{ex: ex}
		key := ex.Request.Method + " " + ex.Request.Path
		exact[key] = append(exact[key], s)
		p := ex.Request.Path
		if i := strings.Index(p, "?"); i >= 0 {
			p = p[:i]
		}
		byPath[ex.Request.Method+" "+p] = append(byPath[ex.Request.Method+" "+p], s)
		recorded[ex.Request.Path] = ex.Request.Path
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri := r.URL.RequestURI()
		mu.Lock()
		var s *slot
		for _, cand := range exact[r.Method+" "+uri] {
			if !cand.used {
				s = cand
				break
			}
		}
		if s == nil {
			p := uri
			if i := strings.Index(p, "?"); i >= 0 {
				p = p[:i]
			}
			for _, cand := range byPath[r.Method+" "+p] {
				if !cand.used {
					s = cand
					break
				}
			}
		}
		if s != nil {
			s.used = true
		}
		mu.Unlock()
		if s == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"no repro exchange for %s %s in fixture %s"}`,
				r.Method, uri, fx.Signature)
			return
		}
		for k, v := range s.ex.Response.Headers {
			w.Header().Set(k, v)
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(s.ex.Response.Status)
		// Rewrite links against the request's own host so pagination stays
		// inside the replay without the handler racing the server pointer.
		body := rewriteRecordedLinks(s.ex.Response.Body, "http://"+r.Host, recorded)
		_, _ = w.Write([]byte(body))
	}))
}

// rewriteRecordedLinks rewrites absolute URLs in a JSON body to the replay
// server when their request URI matches a recorded exchange, so pagination
// pointers stay inside the replay.
func rewriteRecordedLinks(body, base string, recorded map[string]string) string {
	if len(recorded) == 0 || body == "" {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	if !rewriteLinksValue(v, base, recorded) {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return string(out)
}

func rewriteLinksValue(v any, base string, recorded map[string]string) bool {
	switch t := v.(type) {
	case map[string]any:
		changed := false
		for k, val := range t {
			if s, ok := val.(string); ok {
				if repl, hit := rewriteLink(s, base, recorded); hit {
					t[k] = repl
					changed = true
					continue
				}
			}
			if rewriteLinksValue(val, base, recorded) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, val := range t {
			if rewriteLinksValue(val, base, recorded) {
				changed = true
			}
		}
		return changed
	}
	return false
}

func rewriteLink(s, base string, recorded map[string]string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return s, false
	}
	if target, ok := recorded[u.RequestURI()]; ok {
		return base + target, true
	}
	return s, false
}
