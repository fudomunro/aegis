package connectors

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// CalDAVConnector lists a CalDAV calendar as neutral CalendarEvent values
// (issue #146). VEVENT bodies are parsed to the neutral shape and RRULE
// occurrences are expanded inside the requested window so free/busy (issue
// #128) and "my calendar" stay bounded. Auth is HTTP Basic from static
// service headers. Event content stays on the DAV server.
type CalDAVConnector struct {
	host Host
}

// NewCalDAVConnector creates a connector wired to the given Host.
func NewCalDAVConnector(host Host) *CalDAVConnector {
	return &CalDAVConnector{host: host}
}

const (
	CalDAVServiceID   = "dav-calendar"
	CalDAVServiceName = "DAV Calendar"
	calDAVWellKnown   = "caldav"
)

// CalDAVUpstreamBase returns the configured CalDAV endpoint.
func (c *CalDAVConnector) CalDAVUpstreamBase() string {
	return c.host.UpstreamBaseFor(CalDAVServiceID, "https://localhost/.well-known/caldav")
}

// ApplyCalDAVAuth copies static headers and/or Basic credentials onto req.
func (c *CalDAVConnector) ApplyCalDAVAuth(req *http.Request, logger *slog.Logger) {
	if svc := c.host.ServiceConfig(CalDAVServiceID); svc != nil {
		for k, v := range svc.Headers {
			req.Header.Set(k, v)
		}
	}
	if auth := c.auth(); auth.configured() {
		auth.apply(req)
	}
}

func (c *CalDAVConnector) auth() davAuth {
	return davCredentials(c.host.ServiceConfig(CalDAVServiceID))
}

// Capabilities returns CalDAV calendar operations.
func (c *CalDAVConnector) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{
		DataTypes: []types.DataTypeCapability{
			{
				Name:        "events",
				Description: "CalDAV calendar — iCalendar VEVENT entries (metadata only; content stays on the DAV server)",
				Operations: []types.Operation{
					{
						Name:        "read_list",
						Description: "List CalDAV events as neutral CalendarEvent values (permission key: events). RRULE occurrences expand in the window.",
						Method:      "GET",
						Path:        "/me/events",
						Parameters: []types.Parameter{
							{Name: "timeMin", In: "query", Type: "string", Description: "RFC3339 lower bound (default: 30 days ago)"},
							{Name: "timeMax", In: "query", Type: "string", Description: "RFC3339 upper bound (default: 90 days ahead)"},
						},
						Response: types.ResponseSpec{
							Description: "Neutral events: title, description, start_at, end_at, all_day, location, status, organizer, attendees, sources[]",
							Fields:      []string{"events", "total"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when CalDAV is referenced.
func (c *CalDAVConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves CalDAV, Nextcloud calendar, or iCloud Calendar, use Aegis's unified data API. Do not call the DAV server directly.

GET %s/v1/library/events — calendar events across every connected service (title, start_at, end_at, location, sources[])
GET %s/v1/library/freebusy — busy intervals only (scheduling-safe)

sources[] records where each event lives (service, id, etag for conflict-safe writes).`, root, root)
}

// HandleProxy is library-only for CalDAV.
func (c *CalDAVConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	_ = remainingPath
	WriteJSONError(w, http.StatusNotFound,
		"CalDAV is library-only — use GET /v1/library/events or /v1/library/freebusy")
}

// ListCalendarEvents fetches the default window via CalDAV REPORT.
func (c *CalDAVConnector) ListCalendarEvents(ctx context.Context) ([]types.CalendarEvent, bool, error) {
	now := time.Now()
	start, end := CalendarWindow(now)
	return c.ListEventsWithWindow(ctx, start, end)
}

// ListEventsWithWindow expands VEVENTs intersecting [start, end).
func (c *CalDAVConnector) ListEventsWithWindow(ctx context.Context, start, end time.Time) ([]types.CalendarEvent, bool, error) {
	logger := slog.With("service", CalDAVServiceID)
	auth := c.auth()
	if !auth.configured() {
		return nil, false, types.ErrUnauthorized
	}
	if end.IsZero() {
		_, end = CalendarWindow(time.Now())
	}
	if start.IsZero() {
		start, _ = CalendarWindow(time.Now())
	}

	collection, err := c.resolveCollection(ctx, auth)
	if err != nil {
		return nil, false, err
	}

	report := calDAVQueryReport(start, end)
	ms, err := davReport(ctx, collection, report, auth)
	if err != nil {
		logger.Warn("caldav calendar-query failed, falling back to PROPFIND", "error", err)
		return c.listViaPropfind(ctx, collection, auth, start, end)
	}

	calendarName := ""
	var events []types.CalendarEvent
	for _, r := range ms.Responses {
		prop, ok := propstatOK(r.Propstat)
		if !ok {
			continue
		}
		body := prop.CalendarData
		if strings.TrimSpace(body) == "" {
			continue
		}
		href := davAbsolute(collection, r.Href)
		etag := strings.TrimSpace(prop.GetETag)
		events = append(events, c.mapVCalendar(body, href, etag, calendarName, start, end)...)
	}
	return events, false, nil
}

func (c *CalDAVConnector) mapVCalendar(body, href, etag, calendarName string, start, end time.Time) []types.CalendarEvent {
	parsed := ParseICalendar(body)
	if len(parsed) == 0 {
		return nil
	}
	id := davHrefName(href)
	var out []types.CalendarEvent
	for _, ev := range parsed {
		ev.ETag = etag
		ev.Href = href
		if ev.UID == "" {
			ev.UID = id
		}
		for _, occ := range ExpandICalEvent(ev, start, end) {
			out = append(out, MapICalEvent(CalDAVServiceID, CalDAVServiceName, "", calendarName, occ))
		}
	}
	return out
}

func (c *CalDAVConnector) listViaPropfind(ctx context.Context, collection string, auth davAuth, start, end time.Time) ([]types.CalendarEvent, bool, error) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:getcontenttype/><d:resourcetype/><d:displayname/></d:prop></d:propfind>`
	resp, err := davRequest(ctx, "PROPFIND", collection, auth, map[string]string{"Depth": davDepthOne}, body)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, false, types.ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("caldav PROPFIND returned %d", resp.StatusCode)
	}
	data, err := readAllLimit(resp.Body, 4<<20)
	if err != nil {
		return nil, false, err
	}
	ms, err := parseMultistatus(data)
	if err != nil {
		return nil, false, err
	}

	var events []types.CalendarEvent
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
		href := davAbsolute(collection, r.Href)
		raw, etag, err := davGetETag(ctx, href, auth)
		if err != nil {
			continue
		}
		n++
		if n > LibraryPageCap*5 {
			truncated = true
			break
		}
		events = append(events, c.mapVCalendar(string(raw), href, etag, "", start, end)...)
	}
	return events, truncated, nil
}

func (c *CalDAVConnector) resolveCollection(ctx context.Context, auth davAuth) (string, error) {
	if cached, ok := c.host.CacheGet("caldav:collection"); ok && cached != "" {
		return cached, nil
	}
	col, err := davDiscover(ctx, c.CalDAVUpstreamBase(), calDAVWellKnown, auth)
	if err != nil {
		return "", err
	}
	c.host.CacheSet("caldav:collection", col, 30*time.Minute)
	return col, nil
}

// PutEvent writes one VEVENT with If-Match. ErrConflict on 412.
func (c *CalDAVConnector) PutEvent(ctx context.Context, href, etag string, ev ICalEvent) (string, error) {
	auth := c.auth()
	if !auth.configured() {
		return "", types.ErrUnauthorized
	}
	if strings.TrimSpace(href) == "" {
		col, err := c.resolveCollection(ctx, auth)
		if err != nil {
			return "", err
		}
		name := eventETag(ev)
		if name == "" {
			name = ev.UID
		}
		if name == "" {
			name = "event-" + fmt.Sprintf("%d", ev.Start.Unix())
		}
		href = davResourceURL(col, sanitizeHrefName(name)+".ics")
	}
	body := EncodeICalendar(ev)
	return davPutIfMatch(ctx, href, etag, "text/calendar; charset=utf-8", body, auth)
}

// calDAVQueryReport builds a calendar-query REPORT restricted to the window.
func calDAVQueryReport(start, end time.Time) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:prop>
    <d:getetag/>
    <c:calendar-data/>
  </d:prop>
  <c:filter>
    <c:comp-filter name="VCALENDAR">
      <c:comp-filter name="VEVENT">
        <c:time-range start="%s" end="%s"/>
      </c:comp-filter>
    </c:comp-filter>
  </c:filter>
</c:calendar-query>`, fmtICalUTC(start), fmtICalUTC(end))
}
