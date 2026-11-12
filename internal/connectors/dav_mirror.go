package connectors

import (
	"context"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
)

// DAV entity placement operations (issue #254): create-or-update with
// If-Match optimistic concurrency so a newer remote edit is never
// overwritten — conflicts come back as PlaceConflict for the caller to
// report. Etag watermarks ride along in PlaceRecord so core can stamp the
// copy ledger.

var (
	_ kinds.ContactWriteProvider       = (*CardDAVConnector)(nil)
	_ kinds.CalendarEventWriteProvider = (*CalDAVConnector)(nil)
)

// PlaceContacts implements kinds.ContactWriteProvider over CardDAV.
func (c *CardDAVConnector) PlaceContacts(ctx context.Context, contacts []kinds.Contact) (kinds.PlaceEntitiesResult, error) {
	var result kinds.PlaceEntitiesResult
	for _, ct := range contacts {
		name := placeContactDisplay(ct)
		id, etag := placeContactRef(ct, CardDAVServiceID)

		neutral := ct
		if uid := kinds.ContactUID(ct); uid != "" {
			if neutral.Extra == nil {
				neutral.Extra = map[string]any{}
			}
			neutral.Extra["uid"] = uid
		}

		href := ""
		if strings.Contains(id, "/") {
			href = id
		}
		newETag, err := c.PutContact(ctx, href, etag, neutral)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: CardDAVServiceID, ID: id,
				Reason: "remote contact changed (If-Match failed); not overwritten",
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: id, ETag: newETag, MatchedBy: "ref"})
	}
	return result, nil
}

// PlaceEvents implements kinds.CalendarEventWriteProvider over CalDAV.
func (c *CalDAVConnector) PlaceEvents(ctx context.Context, events []kinds.CalendarEvent) (kinds.PlaceEntitiesResult, error) {
	var result kinds.PlaceEntitiesResult
	for _, ev := range events {
		name := ev.Title
		id, etag := placeEventRef(ev, CalDAVServiceID)

		start, _ := parseRFC3339(ev.StartAt)
		end, _ := parseRFC3339(ev.EndAt)
		ical := ICalEvent{
			UID:         placeEventUID(ev),
			Summary:     ev.Title,
			Description: ev.Description,
			Location:    ev.Location,
			Status:      ev.Status,
			Start:       start,
			End:         end,
			AllDay:      ev.AllDay,
			ETag:        etag,
		}
		href := ""
		if strings.Contains(id, "/") {
			href = id
		}
		newETag, err := c.PutEvent(ctx, href, etag, ical)
		if err == ErrConflict {
			result.Conflicts = append(result.Conflicts, kinds.PlaceConflict{
				Name: name, Service: CalDAVServiceID, ID: id,
				Reason: "remote event changed (If-Match failed); not overwritten",
			})
			continue
		}
		if err != nil {
			result.Skips = append(result.Skips, kinds.PlaceSkip{Name: name, Reason: err.Error()})
			continue
		}
		result.Copied++
		result.Placed = append(result.Placed, kinds.PlaceRecord{Name: name, ExternalID: id, ETag: newETag, MatchedBy: "ref"})
	}
	return result, nil
}
