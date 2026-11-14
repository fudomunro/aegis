package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// MicrosoftContactsConnector proxies Microsoft Graph and implements
// ContactProvider (issue #145). It lists the user's Microsoft 365 /
// Outlook.com address book as neutral Contact values so the aggregate
// library can merge it with Google Contacts and the other sources on
// email/phone identity keys. Contact content stays on Microsoft Graph
// (non-Aegis canonical).
type MicrosoftContactsConnector struct {
	host Host
}

// NewMicrosoftContactsConnector creates a connector wired to the given Host.
func NewMicrosoftContactsConnector(host Host) *MicrosoftContactsConnector {
	return &MicrosoftContactsConnector{host: host}
}

// MicrosoftContactsServiceID is the connector/service ID used in config,
// tokens, sources[], and admin. Documented name is "Microsoft Contacts".
const MicrosoftContactsServiceID = "microsoft-contacts"

const MicrosoftContactsServiceName = "Microsoft Contacts"

// MicrosoftGraphDefaultBase is the Graph v1.0 base used when config does not
// override the endpoint.
const MicrosoftGraphDefaultBase = "https://graph.microsoft.com/v1.0"

// microsoftContactsSelect is the Graph projection needed for neutral contacts.
const microsoftContactsSelect = "id,displayName,givenName,surname,emailAddresses,businessPhones,homePhones,mobilePhone,companyName"

// MicrosoftContactsUpstreamBase returns the configured Graph API base.
func (c *MicrosoftContactsConnector) MicrosoftContactsUpstreamBase() string {
	return c.host.UpstreamBaseFor(MicrosoftContactsServiceID, MicrosoftGraphDefaultBase)
}

// Capabilities returns what Microsoft Contacts kinds and operations are supported.
func (c *MicrosoftContactsConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "contacts",
				Description: "Address book — list contacts (metadata only)",
				Operations: []shared.Operation{
					{
						Name:        "read_list",
						Description: "List the user's contacts as neutral Contact values (paginated). Permission key: contacts.",
						Method:      "GET",
						Path:        "/me/contacts",
						Parameters: []shared.Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-1000)", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral contacts: name, emails, phones, handles, organization, sources[] (service, id, name, emails, phones)",
							Fields:      []string{"contacts", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single contact by its Microsoft Graph contact id",
						Method:      "GET",
						Path:        "/contacts/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Graph contact id", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral contact with service-local id and identifiers",
							Fields:      []string{"name", "emails", "phones", "organization", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Microsoft
// contacts are referenced.
func (c *MicrosoftContactsConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Outlook, Microsoft 365, or their address book, use Aegis's unified data API. Do not call Microsoft Graph directly.

GET %s/v1/library/contacts — contacts across every connected contact service (name, emails, phones, organization, sources[])

sources[] records where each contact lives for sync. Contacts use neutral identity matching (email → phone → handle → name) so people can be matched to photos, messages, and events.`, root)
}

// HandleProxy processes a Microsoft Graph contacts request through the connector.
func (c *MicrosoftContactsConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", MicrosoftContactsServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(MicrosoftContactsServiceID, logger)
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftContactsServiceID+"/login")
		return
	}

	// Agent-facing neutral library paths.
	if remainingPath == "/me/contacts" || remainingPath == "/me/contacts/" ||
		remainingPath == "/contacts" || remainingPath == "/contacts/" {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListContacts(w, r)
		return
	}

	if id, ok := ContactIDFromProxyPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetContact(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other Graph contact paths.
	StreamProxy(w, r, c.MicrosoftContactsUpstreamBase(), remainingPath, token, "Microsoft Contacts", logger)
}

func (c *MicrosoftContactsConnector) HandleListContacts(w http.ResponseWriter, r *http.Request) {
	contacts, truncated, err := c.ListUserContacts(r.Context())
	if err != nil {
		if err == kinds.ErrUnauthorized {
			shared.WriteJSONError(w, http.StatusUnauthorized,
				"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftContactsServiceID+"/login")
			return
		}
		slog.Warn("microsoft contacts list failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch contacts from Microsoft")
		return
	}
	if contacts == nil {
		contacts = []kinds.Contact{}
	}
	result := kinds.ContactList{
		Contacts: contacts,
		LibraryListMeta: kinds.LibraryListMeta{
			Total:     len(contacts),
			Synced:    true,
			Truncated: truncated,
			ByService: map[string]kinds.LibraryServiceStatus{
				MicrosoftContactsServiceID: {
					Status:    kinds.LibraryStatusOK,
					Count:     len(contacts),
					Truncated: truncated,
				},
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build contacts list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *MicrosoftContactsConnector) HandleGetContact(w http.ResponseWriter, r *http.Request, token, id string) {
	id = strings.Trim(strings.TrimSpace(id), "/")
	upstreamURL := microsoftGraphURL(c.MicrosoftContactsUpstreamBase(), "/me/contacts/"+id+"?$select="+microsoftContactsSelect)
	data, status, err := microsoftGraphRequest(r.Context(), token, http.MethodGet, upstreamURL, nil)
	if err != nil {
		slog.Error("microsoft contact request failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "Microsoft Graph request failed")
		return
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(data)
		return
	}
	var raw microsoftContact
	if err := json.Unmarshal(data, &raw); err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to parse Microsoft Graph response")
		return
	}
	contact := MapMicrosoftContact(raw)
	out, err := json.Marshal(contact)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode contact")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// microsoftContact is the subset of a Graph contact record we use.
type microsoftContact struct {
	ID             string `json:"id"`
	ETag           string `json:"@odata.etag"`
	DisplayName    string `json:"displayName"`
	GivenName      string `json:"givenName"`
	Surname        string `json:"surname"`
	EmailAddresses []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddresses"`
	BusinessPhones []string `json:"businessPhones"`
	HomePhones     []string `json:"homePhones"`
	MobilePhone    string   `json:"mobilePhone"`
	CompanyName    string   `json:"companyName"`
}

// ListUserContacts implements ContactProvider. Fetches /me/contacts and maps
// records into neutral Contact values for the unified library and for
// cross-type identity matching.
func (c *MicrosoftContactsConnector) ListUserContacts(ctx context.Context) ([]kinds.Contact, bool, error) {
	token := c.host.GetValidToken(MicrosoftContactsServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	base := c.MicrosoftContactsUpstreamBase()
	next := microsoftGraphURL(base, "/me/contacts?$top=100&$select="+microsoftContactsSelect)
	var all []kinds.Contact
	truncated := false
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, next, nil)
		if err != nil {
			return nil, false, err
		}
		if status != http.StatusOK {
			return nil, false, fmt.Errorf("microsoft contacts list returned %d", status)
		}
		var raw graphList[microsoftContact]
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, person := range raw.Value {
			mapped := MapMicrosoftContact(person)
			if kinds.PrimaryContactKey(mapped) != "" || len(mapped.Sources) > 0 {
				all = append(all, mapped)
			}
		}
		next = raw.NextLink
		if next == "" {
			break
		}
		if page == shared.LibraryPageCap-1 {
			truncated = true
		}
	}
	return all, truncated, nil
}

// ListUserContactsDelta implements kinds.ContactDeltaProvider over
// /me/contacts/delta (issue #145). deltaToken is the previously recorded
// deltaLink ("": a fresh feed, which returns the full collection).
// Deleted records the feed reports are dropped — convergence places and
// never deletes. nextToken is "" when the walk did not reach the feed's
// resume link, so the next run replays from the old token.
func (c *MicrosoftContactsConnector) ListUserContactsDelta(ctx context.Context, deltaToken string) ([]kinds.Contact, string, bool, error) {
	token := c.host.GetValidToken(MicrosoftContactsServiceID, slog.Default())
	if token == "" {
		return nil, "", false, kinds.ErrUnauthorized
	}
	next := strings.TrimSpace(deltaToken)
	if next == "" {
		next = microsoftGraphURL(c.MicrosoftContactsUpstreamBase(), "/me/contacts/delta?$select="+microsoftContactsSelect)
	}
	var all []kinds.Contact
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, next, nil)
		if err != nil {
			return nil, "", false, err
		}
		// 410 Gone: the recorded token expired — restart the feed.
		if status == http.StatusGone && deltaToken != "" {
			return c.ListUserContactsDelta(ctx, "")
		}
		if status != http.StatusOK {
			return nil, "", false, fmt.Errorf("microsoft contacts delta returned %d", status)
		}
		records, nextLink, deltaLink, err := decodeGraphDelta[microsoftContact](data)
		if err != nil {
			return nil, "", false, err
		}
		for _, person := range records {
			mapped := MapMicrosoftContact(person)
			if kinds.PrimaryContactKey(mapped) != "" || len(mapped.Sources) > 0 {
				all = append(all, mapped)
			}
		}
		if nextLink != "" {
			next = nextLink
			continue
		}
		return all, deltaLink, false, nil
	}
	// Page cap ended the walk before the resume link: report truncation
	// and no token so the next run replays from the previous one.
	return all, "", true, nil
}

// MapMicrosoftContact converts a Graph contact into the neutral shape.
func MapMicrosoftContact(raw microsoftContact) kinds.Contact {
	name := strings.TrimSpace(raw.DisplayName)
	if name == "" {
		name = strings.TrimSpace(strings.TrimSpace(raw.GivenName + " " + raw.Surname))
	}
	var emails []string
	for _, e := range raw.EmailAddresses {
		if strings.TrimSpace(e.Address) != "" {
			emails = append(emails, e.Address)
		}
	}
	var phones []string
	phones = append(phones, raw.BusinessPhones...)
	phones = append(phones, raw.HomePhones...)
	if strings.TrimSpace(raw.MobilePhone) != "" {
		phones = append(phones, raw.MobilePhone)
	}
	contact := kinds.NewContact(
		MicrosoftContactsServiceID,
		MicrosoftContactsServiceName,
		raw.ID,
		name,
		emails,
		phones,
	)
	if raw.CompanyName != "" {
		contact.Organization = raw.CompanyName
	}
	// The etag rides along as the freshness probe's watermark token
	// (issue #377) and the placement update path's If-Match value.
	if len(contact.Sources) > 0 && raw.ETag != "" {
		if contact.Sources[0].Extra == nil {
			contact.Sources[0].Extra = map[string]any{}
		}
		contact.Sources[0].Extra["etag"] = raw.ETag
	}
	return contact
}
