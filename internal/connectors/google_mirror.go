package connectors

import (
	"context"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

// Google entity placement operations (issue #254): create-or-update with
// etag optimistic concurrency. A record carrying a People resource name /
// event id (and etag) for this service is updated and reported as a
// PlaceConflict when the remote changed; others are created.

var (
	_ kinds.ContactWriteProvider       = (*GoogleContactsConnector)(nil)
	_ kinds.CalendarEventWriteProvider = (*GoogleCalendarConnector)(nil)
)

// PlaceContacts implements kinds.ContactWriteProvider over Google People.
func (c *GoogleContactsConnector) PlaceContacts(ctx context.Context, contacts []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	var result kinds.PlaceEntitiesResult
	for _, ct := range contacts {
		name := placeContactDisplay(ct)
		id, etag := placeContactRef(ct, GoogleContactsServiceID)

		if id != "" && strings.Contains(id, "/") {
			// Update the existing People resource.
			newETag, err := c.UpdateGoogleContact(ctx, id, etag, ct)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
					Name: name, Service: GoogleContactsServiceID, ID: id,
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

		createdID, createdETag, err := c.CreateGoogleContact(ctx, ct)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: GoogleContactsServiceID,
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
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: createdID, ETag: createdETag, EntityName: kinds.ContactItemKey(ct)})
	}
	return result, nil
}

// PlaceEvents implements kinds.CalendarEventWriteProvider over Google
// Calendar.
func (c *GoogleCalendarConnector) PlaceEvents(ctx context.Context, events []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	var result kinds.PlaceEntitiesResult
	for _, ev := range events {
		name := ev.Title
		id, etag := placeEventRef(ev, GoogleCalendarServiceID)

		if id != "" {
			newETag, err := c.UpdateGoogleEvent(ctx, ev.CalendarID, id, etag, ev)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
					Name: name, Service: GoogleCalendarServiceID, ID: id,
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

		createdID, _, createdETag, err := c.CreateGoogleEvent(ctx, ev.CalendarID, ev)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: GoogleCalendarServiceID,
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
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: createdID, ETag: createdETag, EntityName: kinds.EventItemKey(ev)})
	}
	return result, nil
}
