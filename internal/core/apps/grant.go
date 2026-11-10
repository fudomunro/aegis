package apps

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fudomunro/aegis/internal/core/policy"
)

const grantCodeTTL = 10 * time.Minute

// grantCodeMax is a loose cap on unredeemed codes (crash safety).
const grantCodeMax = 256

// GrantCode is a short-lived, single-use authorization code from the consent
// page (issue #131). Redeeming it issues (or rotates) a third-party app token.
type GrantCode struct {
	Code       string
	AppID      string
	AppName    string
	Scopes     policy.GrantMap
	RedirectTo string
	State      string
	Expires    time.Time
}

// GrantStore holds pending consent codes in memory only.
type GrantStore struct {
	mu     sync.Mutex
	byCode map[string]GrantCode
}

func NewGrantStore() *GrantStore {
	return &GrantStore{byCode: make(map[string]GrantCode)}
}

func (g *GrantStore) Issue(c GrantCode) (string, error) {
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

func (g *GrantStore) Consume(code string) (GrantCode, bool) {
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

// AuthorizeQuery is the consent request from a third-party "Connect Aegis"
// button (issue #131). Mirrors a tiny subset of OAuth authorize params.
type AuthorizeQuery struct {
	ClientID    string
	Name        string
	Scopes      string // comma or space separated: events:freebusy, contacts:read
	RedirectURI string
	State       string
}

func ParseAuthorizeQuery(q url.Values) (AuthorizeQuery, []PermissionEntry, error) {
	req := AuthorizeQuery{
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
		typ, level := f, policy.AccessRead
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
	perms := policy.GrantMap{}
	for _, e := range out {
		perms[e.Type] = e.Level
	}
	if err := policy.ValidateGrantMap(perms); err != nil {
		return nil, err
	}
	return out, nil
}

func ScopesFromEntries(entries []PermissionEntry) policy.GrantMap {
	p := policy.GrantMap{policy.DefaultKey: policy.AccessNone}
	for _, e := range entries {
		p[e.Type] = policy.NormalizeAccess(e.Level)
	}
	return p
}

// IsSafeRedirectURI keeps the demo/grant redirect on this Aegis origin or an
// explicit path. Full OAuth would use a registered allowlist (issue #131).
func IsSafeRedirectURI(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true // stay on the authorize page's own origin
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return true
	}
	return false
}

func RedirectWithCode(raw, code, state string) string {
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
