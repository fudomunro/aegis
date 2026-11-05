package connectors

import (
	"context"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Google entity placement operations (issue #254): create-or-update with
// etag optimistic concurrency. A record carrying a People resource name /
// event id (and etag) for this service is updated and reported as a
// PlaceConflict when the remote changed; others are created.

var (
	_ types.ContactWriteProvider       = (*GoogleContactsConnector)(nil)
	_ types.CalendarEventWriteProvider = (*GoogleCalendarConnector)(nil)
)

// PlaceContacts implements types.ContactWriteProvider over Google People.
func (c *GoogleContactsConnector) PlaceContacts(ctx context.Context, contacts []types.Contact) (types.PlaceEntitiesResult, error) {
	var result types.PlaceEntitiesResult
	for _, ct := range contacts {
		name := placeContactDisplay(ct)
		id, etag := placeContactRef(ct, GoogleContactsServiceID)

		if id != "" && strings.Contains(id, "/") {
			// Update the existing People resource.
			newETag, err := c.UpdateGoogleContact(ctx, id, etag, ct)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, types.PlaceConflict{
					Name: name, Service: GoogleContactsServiceID, ID: id,
					Reason: "remote contact changed (etag mismatch); not overwritten",
				})
				continue
			}
			if err != nil {
				result.Skips = append(result.Skips, types.PlaceSkip{Name: name, Reason: err.Error()})
				continue
			}
			result.Copied++
			result.Placed = append(result.Placed, types.PlaceRecord{Name: name, ExternalID: id, ETag: newETag})
			continue
		}

		createdID, createdETag, err := c.CreateGoogleContact(ctx, ct)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, types.PlaceConflict{
				Name: name, Service: GoogleContactsServiceID,
				Reason: "remote contact changed; not overwritten",
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, types.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, types.PlaceRecord{Name: name, ExternalID: createdID, ETag: createdETag})
	}
	return result, nil
}

// PlaceEvents implements types.CalendarEventWriteProvider over Google
// Calendar.
func (c *GoogleCalendarConnector) PlaceEvents(ctx context.Context, events []types.CalendarEvent) (types.PlaceEntitiesResult, error) {
	var result types.PlaceEntitiesResult
	for _, ev := range events {
		name := ev.Title
		id, etag := placeEventRef(ev, GoogleCalendarServiceID)

		if id != "" {
			newETag, err := c.UpdateGoogleEvent(ctx, ev.CalendarID, id, etag, ev)
			if err == ErrConflict {
				result.Conflicts = append(result.Conflicts, types.PlaceConflict{
					Name: name, Service: GoogleCalendarServiceID, ID: id,
					Reason: "remote event changed (etag mismatch); not overwritten",
				})
				continue
			}
			if err != nil {
				result.Skips = append(result.Skips, types.PlaceSkip{Name: name, Reason: err.Error()})
				continue
			}
			result.Copied++
			result.Placed = append(result.Placed, types.PlaceRecord{Name: name, ExternalID: id, ETag: newETag})
			continue
		}

		createdID, _, createdETag, err := c.CreateGoogleEvent(ctx, ev.CalendarID, ev)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, types.PlaceConflict{
				Name: name, Service: GoogleCalendarServiceID,
				Reason: "remote event changed; not overwritten",
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, types.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, types.PlaceRecord{Name: name, ExternalID: createdID, ETag: createdETag})
	}
	return result, nil
}
