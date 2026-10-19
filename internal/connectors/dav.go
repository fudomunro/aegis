package connectors

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// ErrConflict is returned by DAV writes when If-Match/If-None-Match fails
// because a newer remote edit exists. Callers report the conflict instead of
// picking a winner silently (issue #146).
var ErrConflict = errors.New("dav conflict: remote resource changed")

// Shared WebDAV client for CardDAV/CalDAV (issue #146). Covers discovery via
// .well-known, PROPFIND/REPORT multistatus parsing, GET/PUT with If-Match
// etags, and Basic auth from static service headers (Immich-style credentials).

const davDepthZero = "0"
const davDepthOne = "1"

// davAuth holds Basic credentials from service headers.
type davAuth struct {
	Username string
	Password string
}

// davCredentials extracts Basic-auth material from configured headers.
// Accepts either an Authorization header or X-DAV-Username / X-DAV-Password
// (and Username / Password aliases). Never logs the password.
func davCredentials(svc *ServiceInfo) davAuth {
	if svc == nil {
		return davAuth{}
	}
	var auth davAuth
	for k, v := range svc.Headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "authorization":
			v = strings.TrimSpace(v)
			if rest, ok := strings.CutPrefix(v, "Basic "); ok {
				if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest)); err == nil {
					if user, pass, ok := strings.Cut(string(raw), ":"); ok {
						auth.Username = user
						auth.Password = pass
					}
				}
			}
		case "x-dav-username", "username":
			auth.Username = strings.TrimSpace(v)
		case "x-dav-password", "password":
			auth.Password = strings.TrimSpace(v)
		}
	}
	return auth
}

func (a davAuth) apply(req *http.Request) {
	if a.Username == "" && a.Password == "" {
		return
	}
	req.SetBasicAuth(a.Username, a.Password)
}

func (a davAuth) configured() bool {
	return a.Username != "" || a.Password != ""
}

// davMultistatus is the RFC 4918 multistatus response body.
type davMultistatus struct {
	XMLName   xml.Name     `xml:"multistatus"`
	Responses []davResponse `xml:"response"`
	SyncToken string       `xml:"sync-token"`
}

// davResponse is one href + propstat block.
type davResponse struct {
	Href     string        `xml:"href"`
	Propstat []davPropstat `xml:"propstat"`
	// Sync-token responses put status on the response itself.
	Status string `xml:"status"`
}

type davPropstat struct {
	Prop   davProp  `xml:"prop"`
	Status string   `xml:"status"`
}

type davProp struct {
	DisplayName      string   `xml:"displayname"`
	CurrentUser      davHref  `xml:"current-user-principal>href"`
	AddressbookHome  davHref  `xml:"addressbook-home-set>href"`
	CalendarHome     davHref  `xml:"calendar-home-set>href"`
	ResourceType     struct {
		Collection bool `xml:"collection"`
		Addressbook bool `xml:"addressbook"`
		Calendar    bool `xml:"calendar"`
	} `xml:"resourcetype"`
	GetETag          string   `xml:"getetag"`
	GetCTag          string   `xml:"getctag"`
	GetContentType   string   `xml:"getcontenttype"`
	SyncToken        string   `xml:"sync-token"`
	CalendarData     string   `xml:"calendar-data"`
	AddressData      string   `xml:"address-data"`
	SupportedReport  []struct {
		Report struct {
			Name xml.Name `xml:",any"`
		} `xml:"report"`
	} `xml:"supported-report-set>supported-report"`
}

type davHref struct {
	Inner string `xml:",chardata"`
}

// davRequest issues an authenticated DAV request and returns the response.
func davRequest(ctx context.Context, method, rawURL string, auth davAuth, headers map[string]string, body string) (*http.Response, error) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	auth.apply(req)
	req.Header.Set("User-Agent", "Aegis-DAV/1.0")
	if body != "" {
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return HTTPClient.Do(req)
}

// parseMultistatus decodes a DAV multistatus XML body.
func parseMultistatus(data []byte) (*davMultistatus, error) {
	var ms davMultistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, fmt.Errorf("decode dav multistatus: %w", err)
	}
	return &ms, nil
}

func propstatOK(ps []davPropstat) (davProp, bool) {
	for _, p := range ps {
		if strings.Contains(p.Status, "200") || p.Status == "" {
			return p.Prop, true
		}
	}
	if len(ps) > 0 {
		return ps[0].Prop, true
	}
	return davProp{}, false
}

// davAbsolute resolves a possibly-relative href against base.
func davAbsolute(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	bu, err := url.Parse(base)
	if err != nil {
		return href
	}
	hu, err := url.Parse(href)
	if err != nil {
		return href
	}
	return bu.ResolveReference(hu).String()
}

// davDiscover walks .well-known → principal → home-set and returns the
// collection URL for the given kind ("carddav" or "caldav").
// endpoint is the configured service endpoint (server root or collection).
func davDiscover(ctx context.Context, endpoint, kind string, auth davAuth) (collection string, err error) {
	root := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if root == "" {
		return "", fmt.Errorf("dav endpoint is empty")
	}

	// If the endpoint already looks like a collection root, probe it directly.
	if col, ok := davProbeCollection(ctx, root+"/", kind, auth); ok {
		return col, nil
	}

	wellKnown := root + "/.well-known/" + kind
	if loc, ok := davFollowWellKnown(ctx, wellKnown, kind, auth); ok {
		if col, ok := davProbeCollection(ctx, loc, kind, auth); ok {
			return col, nil
		}
		// Well-known may land on the principal; continue from there.
		if col, err := davWalkToCollection(ctx, loc, kind, auth); err == nil {
			return col, nil
		}
	}

	// Walk from the configured root via principal → home-set.
	if col, err := davWalkToCollection(ctx, root+"/", kind, auth); err == nil {
		return col, nil
	}
	return "", fmt.Errorf("dav discovery failed for %s at %s", kind, root)
}

func davFollowWellKnown(ctx context.Context, wellKnown, kind string, auth davAuth) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return "", false
	}
	auth.apply(req)
	req.Header.Set("User-Agent", "Aegis-DAV/1.0")
	// Follow redirects manually so we can stop at a principal URL.
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			auth.apply(req)
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	loc := resp.Request.URL.String()
	if loc == "" {
		return "", false
	}
	return strings.TrimRight(loc, "/") + "/", true
}

// davWalkToCollection starts at a principal-or-root URL and finds the
// addressbook/calendar home-set collection.
func davWalkToCollection(ctx context.Context, start, kind string, auth davAuth) (string, error) {
	principal := start
	if p, ok := davCurrentUserPrincipal(ctx, start, auth); ok {
		principal = p
	}
	home, ok := davHomeSet(ctx, principal, kind, auth)
	if !ok {
		return "", fmt.Errorf("no %s home-set under %s", kind, principal)
	}
	col, ok := davFirstCollection(ctx, home, kind, auth)
	if !ok {
		// Some servers expose the home-set as the collection itself.
		return strings.TrimRight(home, "/") + "/", nil
	}
	return col, nil
}

func davCurrentUserPrincipal(ctx context.Context, href string, auth davAuth) (string, bool) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", href, auth, map[string]string{"Depth": davDepthZero}, body)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ms, err := parseMultistatus(data)
	if err != nil {
		return "", false
	}
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		if h := strings.TrimSpace(prop.CurrentUser.Inner); h != "" {
			u := davAbsolute(href, h)
			if !strings.HasSuffix(u, "/") {
				u += "/"
			}
			return u, true
		}
	}
	return "", false
}

func davHomeSet(ctx context.Context, principal, kind string, auth davAuth) (string, bool) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav" xmlns:a="urn:ietf:params:xml:ns:caldav">
<d:prop><c:addressbook-home-set/><a:calendar-home-set/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", principal, auth, map[string]string{"Depth": davDepthZero}, body)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ms, err := parseMultistatus(data)
	if err != nil {
		return "", false
	}
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		var h string
		if kind == "carddav" {
			h = strings.TrimSpace(prop.AddressbookHome.Inner)
		} else {
			h = strings.TrimSpace(prop.CalendarHome.Inner)
		}
		if h == "" {
			continue
		}
		u := davAbsolute(principal, h)
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		return u, true
	}
	return "", false
}

func davFirstCollection(ctx context.Context, home, kind string, auth davAuth) (string, bool) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
<d:prop><d:resourcetype/><d:displayname/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", home, auth, map[string]string{"Depth": davDepthOne}, body)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ms, err := parseMultistatus(data)
	if err != nil {
		return "", false
	}
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		isCol := prop.ResourceType.Collection || prop.ResourceType.Addressbook || prop.ResourceType.Calendar
		if !isCol {
			continue
		}
		href := strings.TrimSpace(r.Href)
		// Skip the home-set itself when children are listed.
		if strings.EqualFold(strings.TrimRight(href, "/"), strings.TrimRight(davAbsolute(home, href), "/")) {
			// still accept
		}
		u := davAbsolute(home, href)
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		// Prefer an addressbook/calendar type match; otherwise first collection.
		if kind == "carddav" && prop.ResourceType.Addressbook {
			return u, true
		}
		if kind == "caldav" && prop.ResourceType.Calendar {
			return u, true
		}
	}
	// Fall back to any collection child.
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		if !(prop.ResourceType.Collection || prop.ResourceType.Addressbook || prop.ResourceType.Calendar) {
			continue
		}
		u := davAbsolute(home, r.Href)
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		return u, true
	}
	return "", false
}

// davProbeCollection returns the collection when href already is one.
func davProbeCollection(ctx context.Context, href, kind string, auth davAuth) (string, bool) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:">
<d:prop><d:resourcetype/><d:displayname/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", href, auth, map[string]string{"Depth": davDepthZero}, body)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	ms, err := parseMultistatus(data)
	if err != nil {
		return "", false
	}
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		if prop.ResourceType.Addressbook || prop.ResourceType.Calendar || prop.ResourceType.Collection {
			u := href
			if !strings.HasSuffix(u, "/") {
				u += "/"
			}
			return u, true
		}
	}
	return "", false
}

// davReport issues a REPORT against collection and returns parsed multistatus.
func davReport(ctx context.Context, collection, reportXML string, auth davAuth) (*davMultistatus, error) {
	resp, err := davRequest(ctx, "REPORT", collection, auth, map[string]string{"Depth": davDepthOne}, reportXML)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, types.ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dav REPORT %s returned %d", collection, resp.StatusCode)
	}
	return parseMultistatus(data)
}

// davGetETag fetches one resource and returns body + ETag.
func davGetETag(ctx context.Context, href string, auth davAuth) (body []byte, etag string, err error) {
	resp, err := davRequest(ctx, http.MethodGet, href, auth, nil, "")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", types.ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("dav GET %s returned %d", href, resp.StatusCode)
	}
	return data, resp.Header.Get("ETag"), nil
}

// davPutIfMatch writes a resource. When etag is non-empty it sets If-Match so
// a newer remote edit is never overwritten (RFC 7232). Returns the new ETag.
// On 412 it returns ErrConflict.
func davPutIfMatch(ctx context.Context, href, etag, contentType, body string, auth davAuth) (newETag string, err error) {
	headers := map[string]string{"Content-Type": contentType}
	if strings.TrimSpace(etag) != "" {
		headers["If-Match"] = etag
	} else {
		headers["If-None-Match"] = "*"
	}
	resp, err := davRequest(ctx, http.MethodPut, href, auth, headers, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", types.ErrUnauthorized
	case resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrConflict
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("dav PUT %s returned %d", href, resp.StatusCode)
	}
	return strings.TrimSpace(resp.Header.Get("ETag")), nil
}

// davCollectionURL normalizes a collection href to end with "/".
func davCollectionURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

// davResourceURL joins a collection URL with a href/name.
func davResourceURL(collection, name string) string {
	return davCollectionURL(collection) + strings.TrimLeft(strings.TrimSpace(name), "/")
}
