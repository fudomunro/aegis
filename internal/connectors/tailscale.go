package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Tailscale Admin API client (issue #124). Shared machinery for any connector
// that hosts a VIP Service — the localstore.go analogue for tailnet setup.
// Aegis holds an operator API key (SetupSecretTailscaleAPIKey) and uses it to
// check a walkthrough step and fix only the ones that still need work.
//
// Nothing here may mutate tailnet topology from the pre-request hook: only
// explicit admin apply calls (the onboarding seam's ActionApply verbs) reach
// the write methods.

// TailscaleAdminAPIBase is the public Admin API root. Tests override this to
// point at an httptest.Server.
var TailscaleAdminAPIBase = "https://api.tailscale.com/api/v2"

// TailscaleAdminClient talks to api.tailscale.com with an operator access
// token. Auth is HTTP Basic (key as username, empty password) for tskey-api
// tokens; Bearer is used for OAuth-style access tokens.
type TailscaleAdminClient struct {
	base    string
	apiKey  string
	tailnet string
	http    *http.Client
}

// NewTailscaleAdminClient builds a client for the given operator token and
// tailnet ("-" selects the default tailnet).
func NewTailscaleAdminClient(apiKey, tailnet string) *TailscaleAdminClient {
	t := strings.TrimSpace(tailnet)
	if t == "" {
		t = "-"
	}
	return &TailscaleAdminClient{
		base:    TailscaleAdminAPIBase,
		apiKey:  strings.TrimSpace(apiKey),
		tailnet: t,
		http:    HTTPClient,
	}
}

// TailnetPath returns the tailnet path segment ("-" for the default tailnet).
func (c *TailscaleAdminClient) TailnetPath() string {
	if c == nil {
		return "-"
	}
	return c.tailnet
}

// Do performs the request and decodes a JSON response into out.
func (c *TailscaleAdminClient) Do(ctx context.Context, method, path string, body any, out any) (int, error) {
	code, data, err := c.DoRaw(ctx, method, path, body)
	if err != nil {
		return code, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return code, err
		}
	}
	return code, nil
}

// DoRaw performs the request and returns the raw body. The policy endpoint
// serves HuJSON (comments + trailing commas), so callers parse that themselves.
func (c *TailscaleAdminClient) DoRaw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if c == nil || c.apiKey == "" {
		return 0, nil, fmt.Errorf("tailscale api key not configured")
	}
	var rdr io.Reader
	if body != nil {
		var raw []byte
		var err error
		switch b := body.(type) {
		case []byte:
			raw = b
		case string:
			raw = []byte(b)
		default:
			raw, err = json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
		}
		rdr = bytes.NewReader(raw)
	}
	url := c.base + path
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// tskey-api tokens use HTTP Basic with an empty password. OAuth-style
	// access tokens (no tskey- prefix) use Bearer.
	if strings.HasPrefix(c.apiKey, "tskey-") {
		req.SetBasicAuth(c.apiKey, "")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, data, fmt.Errorf("tailscale api %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return resp.StatusCode, data, nil
}

// HuJSONToJSON strips // and /* */ comments and trailing commas so the policy
// file (HuJSON) can be decoded with encoding/json. Enough for Tailscale ACL
// text; not a general HuJSON implementation.
func HuJSONToJSON(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inStr := false
	esc := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inStr {
			out = append(out, ch)
			if esc {
				esc = false
				continue
			}
			if ch == '\\' {
				esc = true
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			out = append(out, ch)
			continue
		}
		// line comment
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
			continue
		}
		// block comment
		if ch == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip '/'
			continue
		}
		// trailing comma before } or ]
		if ch == ',' {
			j := i + 1
			for j < len(src) {
				c := src[j]
				if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
					j++
					continue
				}
				// skip comments after the comma
				if c == '/' && j+1 < len(src) && src[j+1] == '/' {
					for j < len(src) && src[j] != '\n' {
						j++
					}
					continue
				}
				if c == '/' && j+1 < len(src) && src[j+1] == '*' {
					j += 2
					for j+1 < len(src) && !(src[j] == '*' && src[j+1] == '/') {
						j++
					}
					j++
					continue
				}
				break
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue // drop trailing comma
			}
		}
		out = append(out, ch)
	}
	return out
}

// --- API payload shapes (subset of api.tailscale.com /api/v2) ---

// TailscaleService is a VIP Service record.
type TailscaleService struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// TailscaleDevice is a node in the tailnet.
type TailscaleDevice struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Label is the device's human name for pickers and status copy.
func (d TailscaleDevice) Label() string {
	if d.Hostname != "" {
		return d.Hostname
	}
	if d.Name != "" {
		return d.Name
	}
	return d.ID
}

// TailscalePolicy is a partial view of the tailnet policy file (tagOwners only).
type TailscalePolicy struct {
	TagOwners map[string][]string `json:"tagOwners"`
}

// TagOwnersOf returns the declared owners of a host tag, or nil when the tag
// is not declared. Create/tag/approve all reject undeclared tags, so this is
// the prerequisite every other fix checks first.
func TagOwnersOf(pol map[string]any, tag string) []string {
	raw, ok := pol["tagOwners"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m[tag]
	if !ok {
		return nil
	}
	var out []string
	switch v := list.(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, v...)
	}
	return out
}

// LoadPolicy reads the tailnet policy as a generic JSON object. GET /acl
// returns HuJSON; comments are stripped before decoding.
func (c *TailscaleAdminClient) LoadPolicy(ctx context.Context) (map[string]any, []byte, error) {
	path := "/tailnet/" + c.TailnetPath() + "/acl"
	_, raw, err := c.DoRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	normalized := HuJSONToJSON(raw)
	var pol map[string]any
	if err := json.Unmarshal(normalized, &pol); err != nil {
		return nil, nil, fmt.Errorf("parse policy: %w", err)
	}
	return pol, normalized, nil
}

// ListDevices returns the tailnet's nodes.
func (c *TailscaleAdminClient) ListDevices(ctx context.Context) ([]TailscaleDevice, error) {
	path := "/tailnet/" + c.TailnetPath() + "/devices"
	var body struct {
		Devices []TailscaleDevice `json:"devices"`
	}
	if _, err := c.Do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Devices, nil
}

// DeclareTag adds tag → [autogroup:admin] to tagOwners, preserving the rest
// of the policy. A no-op when the tag is already declared.
func (c *TailscaleAdminClient) DeclareTag(ctx context.Context, tag string) error {
	pol, _, err := c.LoadPolicy(ctx)
	if err != nil {
		return err
	}
	if len(TagOwnersOf(pol, tag)) > 0 {
		return nil
	}
	owners, _ := pol["tagOwners"].(map[string]any)
	if owners == nil {
		owners = map[string]any{}
	}
	owners[tag] = []string{"autogroup:admin"}
	pol["tagOwners"] = owners
	body, err := json.Marshal(pol)
	if err != nil {
		return err
	}
	path := "/tailnet/" + c.TailnetPath() + "/acl"
	_, _, err = c.DoRaw(ctx, http.MethodPost, path, body)
	return err
}
