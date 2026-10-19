package connectors

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// CardDAVConnector lists a CardDAV address book as neutral Contact values
// (issue #146). One adapter covers Nextcloud, iCloud Contacts (app-specific
// password), Fastmail, Radicale, and Baïkal. Auth is HTTP Basic from static
// service headers (Immich-style credentials). Contact bytes stay on the
// server — Aegis never becomes the address-book home.
type CardDAVConnector struct {
	host Host
}

// NewCardDAVConnector creates a connector wired to the given Host.
func NewCardDAVConnector(host Host) *CardDAVConnector {
	return &CardDAVConnector{host: host}
}

const (
	CardDAVServiceID   = "dav-contacts"
	CardDAVServiceName = "DAV Contacts"
	cardDAVWellKnown   = "carddav"
)

// CardDAVUpstreamBase returns the configured CardDAV endpoint.
func (c *CardDAVConnector) CardDAVUpstreamBase() string {
	return c.host.UpstreamBaseFor(CardDAVServiceID, "https://localhost/.well-known/carddav")
}

// ApplyCardDAVAuth copies static headers and/or Basic credentials onto req.
func (c *CardDAVConnector) ApplyCardDAVAuth(req *http.Request, logger *slog.Logger) {
	if svc := c.host.ServiceConfig(CardDAVServiceID); svc != nil {
		for k, v := range svc.Headers {
			req.Header.Set(k, v)
		}
	}
	if auth := c.auth(); auth.configured() {
		auth.apply(req)
	}
}

func (c *CardDAVConnector) auth() davAuth {
	return davCredentials(c.host.ServiceConfig(CardDAVServiceID))
}

// Capabilities returns CardDAV contact operations.
func (c *CardDAVConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "contacts",
				Description: "CardDAV address book — vCard 3/4 contacts (metadata only)",
				Operations: []types.Operation{
					{
						Name:        "read_list",
						Description: "List CardDAV contacts as neutral Contact values (permission key: contacts)",
						Method:      "GET",
						Path:        "/me/contacts",
						Response: types.ResponseSpec{
							Description: "Neutral contacts: name, emails, phones, organization, sources[] (service, id, etag)",
							Fields:      []string{"contacts", "total"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when CardDAV is referenced.
func (c *CardDAVConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves CardDAV, Nextcloud contacts, or iCloud Contacts, use Aegis's unified data API. Do not call the DAV server directly.

GET %s/v1/library/contacts — contacts across every connected contact service (name, emails, phones, organization, sources[])

sources[] records where each contact lives (service, id, etag for conflict-safe writes).`, root)
}

// HandleProxy serves the small agent-facing surface; DAV itself is library-only.
func (c *CardDAVConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	_ = remainingPath
	WriteJSONError(w, http.StatusNotFound,
		"CardDAV is library-only — use GET /v1/library/contacts")
}

// ListUserContacts fetches the address book via CardDAV REPORT.
func (c *CardDAVConnector) ListUserContacts(ctx context.Context) ([]types.Contact, bool, error) {
	logger := slog.With("service", CardDAVServiceID)
	auth := c.auth()
	if !auth.configured() {
		return nil, false, types.ErrUnauthorized
	}

	collection, err := c.resolveCollection(ctx, auth)
	if err != nil {
		return nil, false, err
	}

	report := `<?xml version="1.0" encoding="UTF-8"?>
<c:addressbook-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <d:getetag/>
    <c:address-data/>
  </d:prop>
</c:addressbook-query>`

	ms, err := davReport(ctx, collection, report, auth)
	if err != nil {
		// Fall back to PROPFIND + GET when addressbook-query is unsupported.
		return c.listViaPropfind(ctx, collection, auth, logger)
	}

	var contacts []types.Contact
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		body := prop.AddressData
		if strings.TrimSpace(body) == "" {
			continue
		}
		href := davAbsolute(collection, r.Href)
		id := davHrefName(href)
		contact := ParseVCard(CardDAVServiceID, CardDAVServiceName, id, body, strings.TrimSpace(prop.GetETag))
		if contact.Name == "" && len(contact.Emails) == 0 && len(contact.Phones) == 0 {
			continue
		}
		contacts = append(contacts, contact)
	}
	_ = logger
	return contacts, false, nil
}

func (c *CardDAVConnector) listViaPropfind(ctx context.Context, collection string, auth davAuth, logger *slog.Logger) ([]types.Contact, bool, error) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:getcontenttype/><d:resourcetype/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", collection, auth, map[string]string{"Depth": davDepthOne}, body)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, false, types.ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("carddav PROPFIND returned %d", resp.StatusCode)
	}
	data, err := readAllLimit(resp.Body, 4<<20)
	if err != nil {
		return nil, false, err
	}
	ms, err := parseMultistatus(data)
	if err != nil {
		return nil, false, err
	}

	var contacts []types.Contact
	truncated := false
	n := 0
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		if prop.ResourceType.Collection || prop.ResourceType.Addressbook || prop.ResourceType.Calendar {
			continue
		}
		ct := strings.ToLower(prop.GetContentType)
		if ct != "" && !strings.Contains(ct, "vcard") && !strings.Contains(ct, "text/x-vcard") {
			continue
		}
		href := davAbsolute(collection, r.Href)
		// Prefer REPORT payload; fetch individual vCards when needed.
		raw, etag, err := davGetETag(ctx, href, auth)
		if err != nil {
			logger.Warn("carddav get failed", "href", href, "error", err)
			continue
		}
		n++
		if n > LibraryPageCap*5 {
			truncated = true
			break
		}
		contact := ParseVCard(CardDAVServiceID, CardDAVServiceName, davHrefName(href), string(raw), etag)
		if contact.Name == "" && len(contact.Emails) == 0 && len(contact.Phones) == 0 {
			continue
		}
		contacts = append(contacts, contact)
	}
	return contacts, truncated, nil
}

func (c *CardDAVConnector) resolveCollection(ctx context.Context, auth davAuth) (string, error) {
	// Cache discovery per process — collection URLs are stable for a session.
	if cached, ok := c.host.CacheGet("carddav:collection"); ok && cached != "" {
		return cached, nil
	}
	col, err := davDiscover(ctx, c.CardDAVUpstreamBase(), cardDAVWellKnown, auth)
	if err != nil {
		return "", err
	}
	c.host.CacheSet("carddav:collection", col, 30*time.Minute)
	return col, nil
}

// PutContact writes one vCard with If-Match (or If-None-Match for create).
// Returns the new ETag. ErrConflict means a newer remote edit exists.
func (c *CardDAVConnector) PutContact(ctx context.Context, href, etag string, contact types.Contact) (string, error) {
	auth := c.auth()
	if !auth.configured() {
		return "", types.ErrUnauthorized
	}
	if strings.TrimSpace(href) == "" {
		col, err := c.resolveCollection(ctx, auth)
		if err != nil {
			return "", err
		}
		name := davHrefName(contactETag(contact))
		if name == "" {
			name = contactCardName(contact)
		}
		href = davResourceURL(col, name)
	}
	body := EncodeVCard(contact)
	return davPutIfMatch(ctx, href, etag, "text/vcard; charset=utf-8", body, auth)
}

// DeleteContact removes a vCard. Unused by the mirror path but kept for
// symmetry with write adapters.
func (c *CardDAVConnector) DeleteContact(ctx context.Context, href, etag string) error {
	auth := c.auth()
	if !auth.configured() {
		return types.ErrUnauthorized
	}
	headers := map[string]string{}
	if strings.TrimSpace(etag) != "" {
		headers["If-Match"] = etag
	}
	resp, err := davRequest(ctx, http.MethodDelete, href, auth, headers, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return types.ErrUnauthorized
	}
	if resp.StatusCode == http.StatusPreconditionFailed {
		return ErrConflict
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("carddav DELETE returned %d", resp.StatusCode)
	}
	return nil
}

func contactCardName(c types.Contact) string {
	if uid := contactUID(c); uid != "" {
		return sanitizeHrefName(uid) + ".vcf"
	}
	n := strings.TrimSpace(c.Name)
	if n == "" && len(c.Emails) > 0 {
		n = c.Emails[0]
	}
	n = sanitizeHrefName(n)
	if n == "" {
		n = "contact"
	}
	if !strings.HasSuffix(strings.ToLower(n), ".vcf") {
		n += ".vcf"
	}
	return n
}

func sanitizeHrefName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ' || r == '@' || r == '+':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func davHrefName(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	href = strings.TrimRight(href, "/")
	if i := strings.LastIndexByte(href, '/'); i >= 0 {
		href = href[i+1:]
	}
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	return href
}

func readAllLimit(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}
