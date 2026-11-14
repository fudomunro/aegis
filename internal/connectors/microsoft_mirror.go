package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

// Microsoft entity placement operations (issue #145): create-or-update with
// @odata.etag optimistic concurrency. A record carrying a Microsoft
// contact/event id (and etag) for this service is updated with If-Match and
// reported as a PlaceConflict when the remote changed; others are created on
// the default target.

var (
	_ kinds.ContactWriteProvider       = (*MicrosoftContactsConnector)(nil)
	_ kinds.CalendarEventWriteProvider = (*MicrosoftCalendarConnector)(nil)
	_ kinds.ContactDeltaProvider       = (*MicrosoftContactsConnector)(nil)
	_ kinds.CalendarEventDeltaProvider = (*MicrosoftCalendarConnector)(nil)
)

// microsoftContactWrite is the Graph create/update contact body subset.
type microsoftContactWrite struct {
	DisplayName    string `json:"displayName,omitempty"`
	GivenName      string `json:"givenName,omitempty"`
	Surname        string `json:"surname,omitempty"`
	EmailAddresses []struct {
		Address string `json:"address"`
	} `json:"emailAddresses,omitempty"`
	BusinessPhones []string `json:"businessPhones,omitempty"`
	MobilePhone    string   `json:"mobilePhone,omitempty"`
	CompanyName    string   `json:"companyName,omitempty"`
}

// microsoftEventWrite is the Graph create/update event body subset.
type microsoftEventWrite struct {
	Subject     string         `json:"subject,omitempty"`
	BodyPreview string         `json:"bodyPreview,omitempty"`
	Start       *microsoftTime `json:"start,omitempty"`
	End         *microsoftTime `json:"end,omitempty"`
	IsAllDay    bool           `json:"isAllDay,omitempty"`
	Location    *struct {
		DisplayName string `json:"displayName"`
	} `json:"location,omitempty"`
	ShowAs string `json:"showAs,omitempty"`
}

// microsoftEntityRef is the Graph id + etag response subset.
type microsoftEntityRef struct {
	ID   string `json:"id"`
	ETag string `json:"@odata.etag"`
}

// PlaceContacts implements kinds.ContactWriteProvider over Microsoft Graph.
func (c *MicrosoftContactsConnector) PlaceContacts(ctx context.Context, contacts []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	token := c.host.GetValidToken(MicrosoftContactsServiceID, nil)
	if token == "" {
		return kinds.PlaceEntitiesResult{}, kinds.ErrUnauthorized
	}
	var result kinds.PlaceEntitiesResult
	for _, ct := range contacts {
		name := placeContactDisplay(ct)
		id, etag := placeContactRef(ct, MicrosoftContactsServiceID)
		body := microsoftContactFromBody(ct)

		if id != "" {
			newETag, err := microsoftPatchEntity(ctx, c.MicrosoftContactsUpstreamBase(), token, "/me/contacts/"+id, etag, body)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
					Name: name, Service: MicrosoftContactsServiceID, ID: id,
					Reason:     "remote contact changed (etag mismatch); not overwritten",
					EntityName: kinds.ContactItemKey(ct),
				})
				continue
			}
			if err != nil {
				result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
				continue
			}
			result.Copied++
			result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: id, ETag: newETag, MatchedBy: "ref", EntityName: kinds.ContactItemKey(ct)})
			continue
		}

		ref, err := microsoftPostEntity(ctx, c.MicrosoftContactsUpstreamBase(), token, "/me/contacts", body)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: MicrosoftContactsServiceID,
				Reason:     "remote contact changed; not overwritten",
				EntityName: kinds.ContactItemKey(ct),
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: ref.ID, ETag: ref.ETag, EntityName: kinds.ContactItemKey(ct)})
	}
	return result, nil
}

// PlaceEvents implements kinds.CalendarEventWriteProvider over Microsoft
// Graph. Events are placed on the Microsoft calendar the event already
// carries for this service, else the user's default calendar — a foreign
// service's calendar id is never trusted here.
func (c *MicrosoftCalendarConnector) PlaceEvents(ctx context.Context, events []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	token := c.host.GetValidToken(MicrosoftCalendarServiceID, nil)
	if token == "" {
		return kinds.PlaceEntitiesResult{}, kinds.ErrUnauthorized
	}
	var result kinds.PlaceEntitiesResult
	for _, ev := range events {
		name := ev.Title
		id, etag := placeEventRef(ev, MicrosoftCalendarServiceID)
		body := microsoftEventFromBody(ev)

		if id != "" {
			newETag, err := microsoftPatchEntity(ctx, c.MicrosoftCalendarUpstreamBase(), token, "/me/events/"+id, etag, body)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
					Name: name, Service: MicrosoftCalendarServiceID, ID: id,
					Reason:     "remote event changed (etag mismatch); not overwritten",
					EntityName: kinds.EventItemKey(ev),
				})
				continue
			}
			if err != nil {
				result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
				continue
			}
			result.Copied++
			result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: id, ETag: newETag, MatchedBy: "ref", EntityName: kinds.EventItemKey(ev)})
			continue
		}

		path := "/me/events"
		if calID := placeEventCalendar(ev, MicrosoftCalendarServiceID); calID != "" {
			path = "/me/calendars/" + calID + "/events"
		}
		ref, err := microsoftPostEntity(ctx, c.MicrosoftCalendarUpstreamBase(), token, path, body)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: MicrosoftCalendarServiceID,
				Reason:     "remote event changed; not overwritten",
				EntityName: kinds.EventItemKey(ev),
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: ref.ID, ETag: ref.ETag, EntityName: kinds.EventItemKey(ev)})
	}
	return result, nil
}

// postEntity creates one Graph record and returns its id + etag.
func microsoftPostEntity(ctx context.Context, base, token, path string, body any) (microsoftEntityRef, error) {
	data, status, err := microsoftGraphRequest(ctx, token, http.MethodPost, microsoftGraphURL(base, path), body)
	if err != nil {
		return microsoftEntityRef{}, err
	}
	switch {
	case status == http.StatusUnauthorized:
		return microsoftEntityRef{}, kinds.ErrUnauthorized
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		return microsoftEntityRef{}, ErrConflict
	case status >= 400:
		return microsoftEntityRef{}, graphWriteError(status, data)
	}
	var ref microsoftEntityRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return microsoftEntityRef{}, err
	}
	return ref, nil
}

// patchEntity updates one Graph record with If-Match and returns the new etag.
func microsoftPatchEntity(ctx context.Context, base, token, path, etag string, body any) (string, error) {
	data, status, err := microsoftGraphRequestIfMatch(ctx, token, http.MethodPatch, microsoftGraphURL(base, path), etag, body)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized:
		return "", kinds.ErrUnauthorized
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		return "", ErrConflict
	case status >= 400:
		return "", graphWriteError(status, data)
	}
	var ref microsoftEntityRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return "", err
	}
	return ref.ETag, nil
}

// microsoftContactFromBody maps a neutral contact onto the Graph write body.
func microsoftContactFromBody(ct kinds.Contact) microsoftContactWrite {
	body := microsoftContactWrite{
		DisplayName: ct.Name,
		CompanyName: ct.Organization,
	}
	if fields := strings.Fields(ct.Name); len(fields) > 1 {
		body.GivenName = fields[0]
		body.Surname = strings.Join(fields[1:], " ")
	} else if len(fields) == 1 {
		body.GivenName = fields[0]
	}
	for _, e := range ct.Emails {
		body.EmailAddresses = append(body.EmailAddresses, struct {
			Address string `json:"address"`
		}{Address: e})
	}
	if len(ct.Phones) > 0 {
		body.BusinessPhones = append(body.BusinessPhones, ct.Phones...)
		body.MobilePhone = ct.Phones[0]
	}
	return body
}

// microsoftEventFromBody maps a neutral event onto the Graph write body.
func microsoftEventFromBody(ev kinds.CalendarEvent) microsoftEventWrite {
	isCancelled, showAs := graphStatusToMicrosoft(ev.Status)
	if isCancelled {
		// Graph has no writable cancelled flag on a plain create/update;
		// placing a cancelled block is a skip, not a busy block.
		showAs = ""
	}
	body := microsoftEventWrite{
		Subject:     ev.Title,
		BodyPreview: ev.Description,
		IsAllDay:    ev.AllDay,
		ShowAs:      showAs,
	}
	if ev.StartAt != "" {
		t := rfc3339ToMicrosoftTime(ev.StartAt)
		body.Start = &t
	}
	if ev.EndAt != "" {
		t := rfc3339ToMicrosoftTime(ev.EndAt)
		body.End = &t
	}
	if ev.Location != "" {
		body.Location = &struct {
			DisplayName string `json:"displayName"`
		}{DisplayName: ev.Location}
	}
	return body
}
