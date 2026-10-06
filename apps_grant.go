package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/app_authorize.html
var appAuthorizeHTML string

//go:embed web/third_party_demo.html
var thirdPartyDemoHTML string

// grantCodeTTL bounds how long a consent code may be exchanged for an app token.
const grantCodeTTL = 10 * time.Minute

// grantCodeMax is a loose cap on unredeemed codes (crash safety).
const grantCodeMax = 256

// GrantCode is a short-lived, single-use authorization code from the consent
// page (issue #131). Redeeming it issues (or rotates) a third-party app token.
type GrantCode struct {
	Code       string
	AppID      string
	AppName    string
	Scopes     Permissions
	RedirectTo string
	State      string
	Expires    time.Time
}

// GrantStore holds pending consent codes in memory only.
type GrantStore struct {
	mu    sync.Mutex
	byCode map[string]GrantCode
}

func NewGrantStore() *GrantStore {
	return &GrantStore{byCode: make(map[string]GrantCode)}
}

func (g *GrantStore) issue(c GrantCode) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	c.Code = "aeg_grant_" + base64.RawURLEncoding.EncodeToString(raw)
	c.Expires = time.Now().Add(grantCodeTTL)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.byCode) >= grantCodeMax {
		for k, v := range g.byCode {
			if time.Now().After(v.Expires) {
				delete(g.byCode, k)
			}
		}
	}
	g.byCode[c.Code] = c
	return c.Code, nil
}

func (g *GrantStore) consume(code string) (GrantCode, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.byCode[code]
	if !ok {
		return GrantCode{}, false
	}
	delete(g.byCode, code)
	if time.Now().After(c.Expires) {
		return GrantCode{}, false
	}
	return c, true
}

// authorizeQuery is the consent request from a third-party "Connect Aegis"
// button (issue #131). Mirrors a tiny subset of OAuth authorize params.
type authorizeQuery struct {
	ClientID    string
	Name        string
	Scopes      string // comma or space separated: events:freebusy, contacts:read
	RedirectURI string
	State       string
}

func parseAuthorizeQuery(q url.Values) (authorizeQuery, []PermissionEntry, error) {
	req := authorizeQuery{
		ClientID:    strings.TrimSpace(q.Get("client_id")),
		Name:        strings.TrimSpace(q.Get("name")),
		Scopes:      strings.TrimSpace(q.Get("scopes")),
		RedirectURI: strings.TrimSpace(q.Get("redirect_uri")),
		State:       strings.TrimSpace(q.Get("state")),
	}
	if req.ClientID == "" {
		return req, nil, fmt.Errorf("client_id is required")
	}
	if err := validateAppID(req.ClientID); err != nil {
		return req, nil, err
	}
	entries, err := parseScopeList(req.Scopes)
	if err != nil {
		return req, nil, err
	}
	return req, entries, nil
}

// PermissionEntry is one "type:level" pair from a scopes query parameter.
type PermissionEntry struct {
	Type  string
	Level string
}

func parseScopeList(s string) ([]PermissionEntry, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("scopes is required (e.g. events:freebusy)")
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '+'
	})
	var out []PermissionEntry
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		// Accept "events:freebusy" or bare "events" → read.
		typ, level := f, AccessRead
		if i := strings.IndexByte(f, ':'); i >= 0 {
			typ = strings.TrimSpace(f[:i])
			level = strings.TrimSpace(f[i+1:])
		}
		if typ == "" {
			return nil, fmt.Errorf("invalid scope %q", f)
		}
		out = append(out, PermissionEntry{Type: typ, Level: level})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("scopes is required (e.g. events:freebusy)")
	}
	perms := Permissions{}
	for _, e := range out {
		perms[e.Type] = e.Level
	}
	if err := ValidatePermissions(perms); err != nil {
		return nil, err
	}
	return out, nil
}

func scopesFromEntries(entries []PermissionEntry) Permissions {
	p := Permissions{PermissionDefaultKey: AccessNone}
	for _, e := range entries {
		p[e.Type] = normalizeAccess(e.Level)
	}
	return p
}

func describeScopes(p Permissions) []string {
	var out []string
	keys := make([]string, 0, len(p))
	for k := range p {
		if k == PermissionDefaultKey {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		level := normalizeAccess(p[k])
		if level == AccessNone {
			continue
		}
		switch level {
		case AccessFreeBusy:
			out = append(out, fmt.Sprintf("%s — availability only (free/busy)", k))
		case AccessRead:
			out = append(out, fmt.Sprintf("%s — read", k))
		case AccessWrite:
			out = append(out, fmt.Sprintf("%s — write", k))
		case AccessFull:
			out = append(out, fmt.Sprintf("%s — read & write", k))
		default:
			out = append(out, fmt.Sprintf("%s — %s", k, level))
		}
	}
	return out
}

// isSafeRedirectURI keeps the demo/grant redirect on this Aegis origin or an
// explicit path. Full OAuth would use a registered allowlist (issue #131).
func isSafeRedirectURI(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true // stay on the authorize page's own origin
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return true
	}
	return false
}

func redirectWithCode(raw, code, state string) string {
	if raw == "" {
		raw = "/demo/third-party"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "/demo/third-party?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// HandleAppAuthorize shows the consent page (GET) or records a grant (POST).
//
// GET  /v1/apps/authorize?client_id=&name=&scopes=&redirect_uri=&state=
// POST /v1/apps/authorize  (authenticated; same params as form or JSON)
//
// POST redirects to redirect_uri with a one-time code (issue #131).
func (s *Server) HandleAppAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		if _, _, err := parseAuthorizeQuery(q); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !isSafeRedirectURI(q.Get("redirect_uri")) {
			writeJSONError(w, http.StatusBadRequest, "redirect_uri must be a path on this host")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, appAuthorizeHTML)
	case http.MethodPost:
		s.handleAppAuthorizeGrant(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleAppAuthorizeGrant(w http.ResponseWriter, r *http.Request) {
	if s.apps == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	// Accept form post (from the consent page) or JSON.
	var q url.Values
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		var raw map[string]string
		if err := json.Unmarshal(body, &raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		q = url.Values{}
		for k, v := range raw {
			q.Set(k, v)
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid form body")
			return
		}
		q = r.Form
	}
	req, entries, err := parseAuthorizeQuery(q)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !isSafeRedirectURI(req.RedirectURI) {
		writeJSONError(w, http.StatusBadRequest, "redirect_uri must be a path on this host")
		return
	}
	scopes := scopesFromEntries(entries)
	if req.Name == "" {
		req.Name = req.ClientID
	}

	// Persist/refresh the app registration and mint a fresh token for this grant.
	ensure := AppCreateRequest{ID: req.ClientID, Name: req.Name, Scopes: scopes}
	if _, ok := s.apps.Get(req.ClientID); ok {
		name := req.Name
		if _, err := s.apps.Update(req.ClientID, AppUpdateRequest{Name: &name, Scopes: &scopes}); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if _, _, err := s.apps.Create(ensure); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if s.grants == nil {
		s.grants = NewGrantStore()
	}
	code, err := s.grants.issue(GrantCode{
		AppID:      req.ClientID,
		AppName:    req.Name,
		Scopes:     scopes,
		RedirectTo: req.RedirectURI,
		State:      req.State,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to issue grant code")
		return
	}
	s.cache.Clear()
	withPeerLog(slog.With("handler", "app_authorize"), r).
		Info("third-party data grant", "app_id", req.ClientID, "scopes", scopes)

	if strings.Contains(ct, "application/json") {
		writeJSON(w, map[string]any{
			"code":  code,
			"state": req.State,
			"app":   mustAppView(s.apps, req.ClientID),
		})
		return
	}
	target := redirectWithCode(req.RedirectURI, code, req.State)
	http.Redirect(w, r, target, http.StatusFound)
}

func mustAppView(store *AppStore, id string) AppView {
	v, _ := store.Get(id)
	return v
}

// HandleAppToken redeems a one-time grant code for a third-party app token.
//
// POST /v1/apps/token  {"code":"aeg_grant_…"}
func (s *Server) HandleAppToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.apps == nil || s.grants == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeJSONError(w, http.StatusBadRequest, "code is required")
		return
	}
	grant, ok := s.grants.consume(code)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid or expired grant code")
		return
	}
	view, token, err := s.apps.Rotate(grant.AppID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"app":          view,
		"scopes":       view.Scopes,
	})
}

// HandleThirdPartyDemo serves the hosted-SaaS demo page (issue #131).
// Public like a real third-party marketing/app surface: the consent call
// still requires Aegis owner auth.
//
// GET /demo/third-party
func (s *Server) HandleThirdPartyDemo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprint(w, thirdPartyDemoHTML)
}
