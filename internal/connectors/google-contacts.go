package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// GoogleContactsConnector proxies the Google People API and implements
// ContactProvider. It lists the user's address-book contacts in Aegis's
// neutral Contact shape so photo.people[] and other cross-type links can
// share the same identity matching (email → phone → handle → name).
// https://github.com/fudomunro/aegis/issues/22
type GoogleContactsConnector struct {
	host Host
}

// NewGoogleContactsConnector creates a connector wired to the given Host (issue #15 seam).
func NewGoogleContactsConnector(host Host) *GoogleContactsConnector {
	return &GoogleContactsConnector{host: host}
}

// GoogleContactsServiceID is the connector/service ID used in config, tokens,
// sources[], and admin. Documented name is "Google Contacts".
const GoogleContactsServiceID = "google"

const GoogleContactsServiceName = "Google Contacts"

// personFields is the People API projection needed for neutral contacts.
const googleContactsPersonFields = "names,emailAddresses,phoneNumbers,organizations"

// GoogleContactsUpstreamBase returns the configured People API base.
func (c *GoogleContactsConnector) GoogleContactsUpstreamBase() string {
	return c.host.UpstreamBaseFor(GoogleContactsServiceID, "https://people.googleapis.com/v1")
}

// Capabilities returns what Google Contacts data types and operations are supported.
func (c *GoogleContactsConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "contacts",
				Description: "Address book — list contacts (metadata only)",
				Operations: []types.Operation{
					{
						Name:        "read_list",
						Description: "List the user's contacts as neutral Contact values (paginated). Permission key: contacts.",
						Method:      "GET",
						Path:        "/me/contacts",
						Parameters: []types.Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-1000)", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral contacts: name, emails, phones, handles, organization, sources[] (service, id, name, emails, phones)",
							Fields:      []string{"contacts", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single contact by People API resource name (people/…) or bare ID",
						Method:      "GET",
						Path:        "/contacts/{id}",
						Parameters: []types.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "People resource name (people/c123) or bare ID", Required: true},
						},
						Response: types.ResponseSpec{
							Description: "Neutral contact with service-local id and identifiers",
							Fields:      []string{"name", "emails", "phones", "organization", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Google Contacts is referenced.
func (c *GoogleContactsConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/" + GoogleContactsServiceID
	return fmt.Sprintf(`When the user's request involves Google Contacts or their address book, you MUST use the Aegis proxy at %s instead of calling Google People API directly.

For "my contacts" across every connected contact service, prefer the unified library endpoint first:
GET %s/v1/library/contacts — contacts in one neutral list (name, emails, phones, organization, sources[])

Google Contacts-specific endpoints (call the full proxy URL; Aegis authenticates you):
GET /me/contacts — list address-book contacts (neutral Contact metadata)
GET /contacts/{id} — one contact (id is people/… resource name or bare ID)

Contacts use Aegis's neutral identity matching (email → phone → handle → name). Photo.people[] entries are Contact values, so a person from this address book can be matched to someone listed on a photo when a connector reports them.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes a Google People API request through the connector.
func (c *GoogleContactsConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", GoogleContactsServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(GoogleContactsServiceID, logger)
	if token == "" {
		WriteJSONError(w, http.StatusUnauthorized,
			"no valid Google Contacts token — authorize at /v1/auth/"+GoogleContactsServiceID+"/login")
		return
	}

	// Agent-facing neutral library paths.
	if remainingPath == "/me/contacts" || remainingPath == "/me/contacts/" ||
		remainingPath == "/contacts" || remainingPath == "/contacts/" {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListContacts(w, r)
		return
	}

	if id, ok := ContactIDFromProxyPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetContact(w, r, token, id)
		return
	}

	// Generic authenticated pass-through for other People Library paths.
	upstreamURL := c.GoogleContactsUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Google Contacts request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ContactIDFromProxyPath extracts a contact id from /contacts/{id} or
// /me/contacts/{id}. The id may be a full resource name ("people/c123") or a
// bare ID ("c123"); the slash form is preserved as path segments.
func ContactIDFromProxyPath(remainingPath string) (string, bool) {
	p := remainingPath
	for _, prefix := range []string{"/me/contacts/", "/contacts/"} {
		if strings.HasPrefix(p, prefix) {
			id := strings.Trim(strings.TrimPrefix(p, prefix), "/")
			return id, id != ""
		}
	}
	return "", false
}

func (c *GoogleContactsConnector) HandleListContacts(w http.ResponseWriter, r *http.Request) {
	contacts, truncated, err := c.ListUserContacts(r.Context())
	if err != nil {
		if err == types.ErrUnauthorized {
			WriteJSONError(w, http.StatusUnauthorized,
				"no valid Google Contacts token — authorize at /v1/auth/"+GoogleContactsServiceID+"/login")
			return
		}
		slog.Warn("google contacts list failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "failed to fetch contacts from Google Contacts")
		return
	}
	if contacts == nil {
		contacts = []types.Contact{}
	}
	result := types.ContactList{
		Contacts:  contacts,
		Total:     len(contacts),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]types.LibraryServiceStatus{
			GoogleContactsServiceID: {
				Status:    types.LibraryStatusOK,
				Count:     len(contacts),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to build contacts list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleContactsConnector) HandleGetContact(w http.ResponseWriter, r *http.Request, token, id string) {
	resourceName := NormalizeGooglePeopleResourceName(id)
	logger := slog.With("service", GoogleContactsServiceID, "method", r.Method, "path", "/"+resourceName)
	// resourceName is already "people/{id}" — People API path is /v1/people/{id}.
	upstreamURL := c.GoogleContactsUpstreamBase() + "/" + resourceName +
		"?personFields=" + googleContactsPersonFields
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		logger.Error("failed to build contact request", "error", err)
		WriteJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		logger.Error("contact request failed", "error", err)
		WriteJSONError(w, http.StatusBadGateway, "Google Contacts request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to read Google Contacts response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}
	var person googlePeoplePerson
	if err := json.Unmarshal(data, &person); err != nil {
		WriteJSONError(w, http.StatusBadGateway, "failed to parse Google Contacts response")
		return
	}
	contact := MapGooglePerson(person)
	out, err := json.Marshal(contact)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "failed to encode contact")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// NormalizeGooglePeopleResourceName accepts "people/c123" or "c123" and
// returns the path segment form used by the People API.
func NormalizeGooglePeopleResourceName(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "/")
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "people/") {
		return id
	}
	return "people/" + id
}

// ListUserContacts implements ContactProvider. Fetches People API connections
// and maps them into neutral Contact values for the unified library and for
// photo.people[] cross-type matching.
func (c *GoogleContactsConnector) ListUserContacts(ctx context.Context) ([]types.Contact, bool, error) {
	token := c.host.GetValidToken(GoogleContactsServiceID, slog.Default())
	if token == "" {
		return nil, false, types.ErrUnauthorized
	}

	upstreamBase := c.GoogleContactsUpstreamBase()
	var all []types.Contact
	pageToken := ""
	truncated := false
	for page := 0; page < LibraryPageCap; page++ {
		u := upstreamBase + "/people/me/connections?pageSize=100&personFields=" + googleContactsPersonFields
		if pageToken != "" {
			u += "&pageToken=" + pageToken
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("google people connections returned %d", resp.StatusCode)
		}
		var raw googlePeopleConnectionsList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, person := range raw.Connections {
			mapped := MapGooglePerson(person)
			if types.PrimaryContactKey(mapped) != "" || len(mapped.Sources) > 0 {
				all = append(all, mapped)
			}
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == LibraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// googlePeoplePerson is the subset of a People API person we use.
type googlePeoplePerson struct {
	ResourceName string `json:"resourceName"`
	Names        []struct {
		DisplayName string `json:"displayName"`
		GivenName   string `json:"givenName"`
		FamilyName  string `json:"familyName"`
	} `json:"names"`
	EmailAddresses []struct {
		Value string `json:"value"`
	} `json:"emailAddresses"`
	PhoneNumbers []struct {
		Value string `json:"value"`
	} `json:"phoneNumbers"`
	Organizations []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"organizations"`
}

type googlePeopleConnectionsList struct {
	Connections   []googlePeoplePerson `json:"connections"`
	NextPageToken string               `json:"nextPageToken"`
	TotalItems    int                  `json:"totalItems"`
}

func MapGooglePerson(person googlePeoplePerson) types.Contact {
	name := ""
	if len(person.Names) > 0 {
		name = person.Names[0].DisplayName
		if name == "" {
			name = strings.TrimSpace(person.Names[0].GivenName + " " + person.Names[0].FamilyName)
		}
	}
	var emails, phones []string
	for _, e := range person.EmailAddresses {
		emails = append(emails, e.Value)
	}
	for _, p := range person.PhoneNumbers {
		phones = append(phones, p.Value)
	}
	contact := types.NewContact(
		GoogleContactsServiceID,
		GoogleContactsServiceName,
		person.ResourceName,
		name,
		emails,
		phones,
	)
	if len(person.Organizations) > 0 && person.Organizations[0].Name != "" {
		contact.Organization = person.Organizations[0].Name
	}
	// Keep the People resource name on every source so photo.people[] and
	// other cross-type links can resolve back to this Google contact.
	if person.ResourceName != "" && len(contact.Sources) > 0 {
		if contact.Sources[0].Extra == nil {
			contact.Sources[0].Extra = map[string]any{}
		}
		contact.Sources[0].Extra["resource_name"] = person.ResourceName
	}
	return contact
}
