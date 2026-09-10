package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// PeerIdentity is the Tailscale identity of the caller, resolved either from
// Tailscale Serve identity headers or from tailscaled's LocalAPI WhoIs.
type PeerIdentity struct {
	// LoginName is the user login (e.g. alice@example.com) or a synthetic
	// tag identity for tagged devices (e.g. tag:guardian-client).
	LoginName string `json:"login_name"`
	// DisplayName is the human-readable name, when known.
	DisplayName string `json:"display_name,omitempty"`
	// Tags are node tags for tagged devices (empty for user-owned devices).
	Tags []string `json:"tags,omitempty"`
	// Source is how identity was resolved: "serve-headers" or "whois".
	Source string `json:"source"`
}

// IdentityResolver extracts Tailscale peer identity from an inbound request.
// Implementations must not trust values the client could have forged when
// Guardian is exposed directly; Serve headers are only trustworthy when
// requests arrive through Tailscale Serve, and WhoIs is only trustworthy
// when the connection is on the tailnet.
type IdentityResolver interface {
	// Resolve returns the peer identity, or (nil, nil) when the request
	// carries no Tailscale identity. Non-nil errors are for resolver
	// failures, not "no identity".
	Resolve(ctx context.Context, r *http.Request) (*PeerIdentity, error)
}

// ServeHeaderResolver trusts Tailscale Serve identity headers. Only use when
// Guardian is not directly reachable except via `tailscale serve` (or an
// equivalent reverse proxy that strips and re-sets these headers).
type ServeHeaderResolver struct{}

// well-known Serve identity headers.
// https://tailscale.com/kb/1312/serve#identity-headers
const (
	headerLogin    = "Tailscale-User-Login"
	headerName     = "Tailscale-User-Name"
	headerTailnet  = "Tailscale-Tailnet"
	headerTags     = "Tailscale-Node-Tags"
	headerIdentity = "Tailscale-Identity-User"
)

func (ServeHeaderResolver) Resolve(_ context.Context, r *http.Request) (*PeerIdentity, error) {
	login := strings.TrimSpace(r.Header.Get(headerLogin))
	if login == "" {
		login = strings.TrimSpace(r.Header.Get(headerIdentity))
	}
	if login == "" {
		// Tagged devices may not set a user login; tags alone still prove
		// the request came through Tailscale.
		if tags := parseTagsHeader(r.Header.Get(headerTags)); len(tags) > 0 {
			return &PeerIdentity{
				LoginName: strings.Join(tags, ","),
				Tags:      tags,
				Source:    "serve-headers",
			}, nil
		}
		return nil, nil
	}
	id := &PeerIdentity{
		LoginName:   login,
		DisplayName: strings.TrimSpace(r.Header.Get(headerName)),
		Source:      "serve-headers",
	}
	if tags := parseTagsHeader(r.Header.Get(headerTags)); len(tags) > 0 {
		id.Tags = tags
	}
	return id, nil
}

func parseTagsHeader(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	// Accept comma or space separated tag lists.
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// WhoIsResolver queries tailscaled's LocalAPI over a Unix socket. This works
// when Guardian shares a host with Tailscale (system daemon or compatible),
// without embedding tsnet.
type WhoIsResolver struct {
	// SocketPath is the tailscaled LocalAPI socket. Empty uses default.
	SocketPath string
	// Client is optional; defaults to a Unix-socket HTTP client.
	Client *http.Client
}

func defaultTSocketPath() string {
	for _, p := range []string{
		"/var/run/tailscale/tailscaled.sock",
		"/run/tailscale/tailscaled.sock",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (w WhoIsResolver) Resolve(ctx context.Context, r *http.Request) (*PeerIdentity, error) {
	sock := w.SocketPath
	if sock == "" {
		sock = defaultTSocketPath()
	}
	if sock == "" {
		return nil, nil // no local tailscaled
	}

	remote := r.RemoteAddr
	if remote == "" {
		return nil, nil
	}
	// Strip port if present; WhoIs accepts ip or ip:port.
	u := &url.URL{
		Scheme:   "http",
		Host:     "local-tailscaled.sock",
		Path:     "/localapi/v0/whois",
		RawQuery: url.Values{"addr": {remote}}.Encode(),
	}

	client := w.Client
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
			Timeout: 2 * time.Second,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		// Not a tailnet peer (or unknown addr).
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whois returned %d", resp.StatusCode)
	}

	var body struct {
		Node struct {
			Tags []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName   string `json:"LoginName"`
			DisplayName string `json:"DisplayName"`
		} `json:"UserProfile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	id := &PeerIdentity{Source: "whois"}
	id.LoginName = body.UserProfile.LoginName
	id.DisplayName = body.UserProfile.DisplayName
	id.Tags = body.Node.Tags
	if id.LoginName == "" && len(id.Tags) > 0 {
		// Synthetic identity for tagged devices, matching Aperture's model.
		id.LoginName = strings.Join(id.Tags, ",")
	}
	if id.LoginName == "" {
		return nil, nil
	}
	return id, nil
}

// MultiResolver tries each resolver in order and returns the first identity.
type MultiResolver struct {
	Resolvers []IdentityResolver
}

func (m MultiResolver) Resolve(ctx context.Context, r *http.Request) (*PeerIdentity, error) {
	var lastErr error
	for _, res := range m.Resolvers {
		if res == nil {
			continue
		}
		id, err := res.Resolve(ctx, r)
		if err != nil {
			lastErr = err
			continue
		}
		if id != nil {
			return id, nil
		}
	}
	return nil, lastErr
}

// loginAllowed reports whether the identity passes an optional allowlist.
// Empty allowlist means any authenticated tailnet identity is accepted.
func loginAllowed(id *PeerIdentity, allowed []string) bool {
	if id == nil {
		return false
	}
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(a, id.LoginName) {
			return true
		}
		// Tagged identities may be stored as "tag:a,tag:b".
		if strings.EqualFold(a, strings.Join(id.Tags, ",")) {
			return true
		}
	}
	return false
}

func parseLoginList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
