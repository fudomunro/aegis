package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Google People / Calendar write helpers used by the contacts and events
// mirror (issue #146). Optimistic concurrency uses the People etag and the
// Calendar event etag; callers surface conflicts instead of overwriting.

// googlePeopleCreate is the People API createContact body subset.
type googlePeopleCreate struct {
	Names []struct {
		GivenName   string `json:"givenName,omitempty"`
		FamilyName  string `json:"familyName,omitempty"`
		DisplayName string `json:"displayName"`
	} `json:"names,omitempty"`
	EmailAddresses []struct {
		Value string `json:"value"`
	} `json:"emailAddresses,omitempty"`
	PhoneNumbers []struct {
		Value string `json:"value"`
	} `json:"phoneNumbers,omitempty"`
	Organizations []struct {
		Name string `json:"name"`
	} `json:"organizations,omitempty"`
}

type googlePeopleUpdate struct {
	Person           googlePeopleCreate `json:"person"`
	UpdatePersonFields string           `json:"updatePersonFields"`
}

// googleCalendarEventWrite is the Calendar events.insert/update body subset.
type googleCalendarEventWrite struct {
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Status      string `json:"status,omitempty"`
	Start       *struct {
		DateTime string `json:"dateTime,omitempty"`
		Date     string `json:"date,omitempty"`
	} `json:"start,omitempty"`
	End *struct {
		DateTime string `json:"dateTime,omitempty"`
		Date     string `json:"date,omitempty"`
	} `json:"end,omitempty"`
}

type googleCalendarEventID struct {
	ID      string `json:"id"`
	HTMLLink string `json:"htmlLink"`
	ETag    string `json:"etag"`
}

// CreateGoogleContact creates a person on Google Contacts and returns the
// resource name and etag.
func (c *GoogleContactsConnector) CreateGoogleContact(ctx context.Context, contact types.Contact) (resourceName, etag string, err error) {
	token := c.host.GetValidToken(GoogleContactsServiceID, nil)
	if token == "" {
		return "", "", types.ErrUnauthorized
	}
	body := googlePeopleCreate{}
	name := strings.TrimSpace(contact.Name)
	if name != "" {
		given, family := "", name
		if i := strings.LastIndex(name, " "); i >= 0 {
			given, family = name[:i], name[i+1:]
		} else {
			given = name
			family = ""
		}
		body.Names = []struct {
			GivenName   string `json:"givenName,omitempty"`
			FamilyName  string `json:"familyName,omitempty"`
			DisplayName string `json:"displayName"`
		}{{
			GivenName:   given,
			FamilyName:  family,
			DisplayName: name,
		}}
	}
	for _, e := range contact.Emails {
		body.EmailAddresses = append(body.EmailAddresses, struct {
			Value string `json:"value"`
		}{Value: e})
	}
	for _, p := range contact.Phones {
		body.PhoneNumbers = append(body.PhoneNumbers, struct {
			Value string `json:"value"`
		}{Value: p})
	}
	if contact.Organization != "" {
		body.Organizations = []struct {
			Name string `json:"name"`
		}{{Name: contact.Organization}}
	}

	u := c.GoogleContactsUpstreamBase() + "/people:createContact"
	var out googlePeoplePerson
	if err := c.postJSON(ctx, token, u, body, &out); err != nil {
		return "", "", err
	}
	return out.ResourceName, out.ETag, nil
}

// UpdateGoogleContact updates a person. When etag is non-empty it is sent for
// optimistic concurrency; a 409 returns ErrConflict.
func (c *GoogleContactsConnector) UpdateGoogleContact(ctx context.Context, resourceName, etag string, contact types.Contact) (newETag string, err error) {
	token := c.host.GetValidToken(GoogleContactsServiceID, nil)
	if token == "" {
		return "", types.ErrUnauthorized
	}
	if resourceName == "" {
		return "", fmt.Errorf("google contact update requires a resource name")
	}
	payload := googlePeopleUpdate{
		UpdatePersonFields: "names,emailAddresses,phoneNumbers,organizations",
	}
	name := strings.TrimSpace(contact.Name)
	if name != "" {
		given, family := "", name
		if i := strings.LastIndex(name, " "); i >= 0 {
			given, family = name[:i], name[i+1:]
		}
		payload.Person.Names = []struct {
			GivenName   string `json:"givenName,omitempty"`
			FamilyName  string `json:"familyName,omitempty"`
			DisplayName string `json:"displayName"`
		}{{
			GivenName:   given,
			FamilyName:  family,
			DisplayName: name,
		}}
	}
	for _, e := range contact.Emails {
		payload.Person.EmailAddresses = append(payload.Person.EmailAddresses, struct {
			Value string `json:"value"`
		}{Value: e})
	}
	for _, p := range contact.Phones {
		payload.Person.PhoneNumbers = append(payload.Person.PhoneNumbers, struct {
			Value string `json:"value"`
		}{Value: p})
	}
	if contact.Organization != "" {
		payload.Person.Organizations = []struct {
			Name string `json:"name"`
		}{{Name: contact.Organization}}
	}

	u := c.GoogleContactsUpstreamBase() + "/" + strings.TrimPrefix(resourceName, "/") + ":updateContact"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(mustJSON(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := readAllLimit(resp.Body, 1<<20)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", types.ErrUnauthorized
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrConflict
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("google contact update returned %d: %s", resp.StatusCode, truncateBytes(body, 200))
	}
	var out googlePeoplePerson
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	return out.ETag, nil
}

// CreateGoogleEvent inserts an event on the given calendar (or "primary").
// Returns the event id, htmlLink, and etag.
func (c *GoogleCalendarConnector) CreateGoogleEvent(ctx context.Context, calendarID string, ev types.CalendarEvent) (id, htmlLink, etag string, err error) {
	token := c.host.GetValidToken(GoogleCalendarServiceID, nil)
	if token == "" {
		return "", "", "", types.ErrUnauthorized
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	body := googleCalendarEventWrite{
		Summary:     ev.Title,
		Description: ev.Description,
		Location:    ev.Location,
		Status:      ev.Status,
	}
	if ev.AllDay {
		d := strings.TrimSuffix(ev.StartAt[:10], "")
		if len(ev.StartAt) >= 10 {
			d = ev.StartAt[:10]
		}
		endD := d
		if len(ev.EndAt) >= 10 {
			endD = ev.EndAt[:10]
		}
		body.Start = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{Date: d}
		body.End = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{Date: endD}
	} else {
		body.Start = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{DateTime: ev.StartAt}
		body.End = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{DateTime: ev.EndAt}
	}

	u := c.GoogleCalendarUpstreamBase() + "/calendars/" + url.PathEscape(calendarID) + "/events"
	var out googleCalendarEventID
	if err := c.postJSON(ctx, token, u, body, &out); err != nil {
		return "", "", "", err
	}
	return out.ID, out.HTMLLink, out.ETag, nil
}

// UpdateGoogleEvent replaces one event. When etag is non-empty it is sent as
// If-Match; 409/412 map to ErrConflict.
func (c *GoogleCalendarConnector) UpdateGoogleEvent(ctx context.Context, calendarID, eventID, etag string, ev types.CalendarEvent) (newETag string, err error) {
	token := c.host.GetValidToken(GoogleCalendarServiceID, nil)
	if token == "" {
		return "", types.ErrUnauthorized
	}
	if calendarID == "" {
		calendarID = "primary"
	}
	if eventID == "" {
		return "", fmt.Errorf("google event update requires an event id")
	}
	body := googleCalendarEventWrite{
		Summary:     ev.Title,
		Description: ev.Description,
		Location:    ev.Location,
		Status:      ev.Status,
	}
	if ev.AllDay && len(ev.StartAt) >= 10 {
		endD := ev.StartAt[:10]
		if len(ev.EndAt) >= 10 {
			endD = ev.EndAt[:10]
		}
		body.Start = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{Date: ev.StartAt[:10]}
		body.End = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{Date: endD}
	} else {
		body.Start = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{DateTime: ev.StartAt}
		body.End = &struct {
			DateTime string `json:"dateTime,omitempty"`
			Date     string `json:"date,omitempty"`
		}{DateTime: ev.EndAt}
	}

	u := c.GoogleCalendarUpstreamBase() + "/calendars/" + url.PathEscape(calendarID) + "/events/" + url.PathEscape(eventID)
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := readAllLimit(resp.Body, 1<<20)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", types.ErrUnauthorized
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrConflict
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("google event update returned %d: %s", resp.StatusCode, truncateBytes(data, 200))
	}
	var out googleCalendarEventID
	if err := json.Unmarshal(data, &out); err != nil {
		return resp.Header.Get("ETag"), nil
	}
	if out.ETag != "" {
		return out.ETag, nil
	}
	return resp.Header.Get("ETag"), nil
}

func (c *GoogleContactsConnector) postJSON(ctx context.Context, token, u string, body, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := readAllLimit(resp.Body, 1<<20)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return types.ErrUnauthorized
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed:
		return ErrConflict
	case resp.StatusCode >= 400:
		return fmt.Errorf("google contacts POST %s returned %d: %s", u, resp.StatusCode, truncateBytes(data, 200))
	}
	return json.Unmarshal(data, out)
}

func (c *GoogleCalendarConnector) postJSON(ctx context.Context, token, u string, body, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := readAllLimit(resp.Body, 1<<20)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return types.ErrUnauthorized
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed:
		return ErrConflict
	case resp.StatusCode >= 400:
		return fmt.Errorf("google calendar POST %s returned %d: %s", u, resp.StatusCode, truncateBytes(data, 200))
	}
	return json.Unmarshal(data, out)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// googlePeoplePerson.ETag is used for optimistic concurrency on update.
// Declared here so the write helpers can share the read-side struct without
// exporting it.
var _ = googlePeoplePerson{}
