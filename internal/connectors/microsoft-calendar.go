package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// MicrosoftCalendarConnector proxies Microsoft Graph and implements
// CalendarEventProvider (issue #145). It lists the user's Microsoft 365 /
// Outlook.com calendar entries as neutral CalendarEvent values so agents can
// ask "what's on my calendar?" through /v1/library/events and the two homes
// can sync — including the free/busy projection onto Google Calendar. Event
// content stays on Microsoft Graph (non-Aegis canonical).
type MicrosoftCalendarConnector struct {
	host Host
}

// NewMicrosoftCalendarConnector creates a connector wired to the given Host.
func NewMicrosoftCalendarConnector(host Host) *MicrosoftCalendarConnector {
	return &MicrosoftCalendarConnector{host: host}
}

// MicrosoftCalendarServiceID is the connector/service ID used in config,
// tokens, sources[], and admin. Documented name is "Microsoft Calendar".
const MicrosoftCalendarServiceID = "microsoft-calendar"

const MicrosoftCalendarServiceName = "Microsoft Calendar"

// Default library window mirrors the Google Calendar surface: recent past +
// near future, so "my calendar" answers cover both directions.
const (
	microsoftCalendarDefaultPastDays   = 30
	microsoftCalendarDefaultFutureDays = 90
)

// microsoftEventsSelect is the Graph projection needed for neutral events.
const microsoftEventsSelect = "id,iCalUId,subject,bodyPreview,start,end,isAllDay,isCancelled,showAs,location,organizer,attendees,webLink,type"

// MicrosoftCalendarUpstreamBase returns the configured Graph API base.
func (c *MicrosoftCalendarConnector) MicrosoftCalendarUpstreamBase() string {
	return c.host.UpstreamBaseFor(MicrosoftCalendarServiceID, MicrosoftGraphDefaultBase)
}

// Capabilities returns what Microsoft Calendar kinds and operations are supported.
func (c *MicrosoftCalendarConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "events",
				Description: "Calendar events — list calendars and events (metadata only; event content stays on Microsoft 365)",
				Operations: []shared.Operation{
					{
						Name:        "read_list",
						Description: "List the user's calendar events as neutral CalendarEvent values (paginated). Permission key: events.",
						Method:      "GET",
						Path:        "/me/events",
						Parameters: []shared.Parameter{
							{Name: "timeMin", In: "query", Type: "string", Description: "RFC3339 lower bound (default: 30 days ago)"},
							{Name: "timeMax", In: "query", Type: "string", Description: "RFC3339 upper bound (default: 90 days ahead)"},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-1000)", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral events: title, description, start_at, end_at, all_day, location, status, organizer, attendees, sources[]",
							Fields:      []string{"events", "total"},
						},
					},
					{
						Name:        "list_calendars",
						Description: "List the user's calendars (id, name, default flag)",
						Method:      "GET",
						Path:        "/me/calendars",
						Parameters: []shared.Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max calendars per page (1-250)", Default: 250},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Calendars: id, name, is_default",
							Fields:      []string{"calendars", "total"},
						},
					},
					{
						Name:        "list_calendar_events",
						Description: "List events for one calendar as neutral CalendarEvent values",
						Method:      "GET",
						Path:        "/calendars/{calendarId}/events",
						Parameters: []shared.Parameter{
							{Name: "calendarId", In: "path", Type: "string", Description: "Calendar ID", Required: true},
							{Name: "timeMin", In: "query", Type: "string", Description: "RFC3339 lower bound"},
							{Name: "timeMax", In: "query", Type: "string", Description: "RFC3339 upper bound"},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page", Default: 100},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral events for the calendar",
							Fields:      []string{"events", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single calendar event by service-local ID",
						Method:      "GET",
						Path:        "/events/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local event ID", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral calendar event with service-local id and sources[]",
							Fields:      []string{"title", "start_at", "end_at", "location", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Microsoft
// calendar is referenced.
func (c *MicrosoftCalendarConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves Outlook, Microsoft 365, their schedule, meetings, or calendar events, use Aegis's unified data API. Do not call Microsoft Graph directly.

GET %s/v1/library/events — calendar events across every connected calendar service (title, start_at, end_at, all_day, location, organizer, attendees, sources[])
GET %s/v1/library/freebusy — busy intervals only for scheduling (no titles or attendees)

sources[] records where each event lives for sync. Event content stays on Microsoft 365 — Aegis does not store calendars.
Organizer/attendees use neutral identity matching so people on a meeting can match /v1/library/contacts.`, root, root)
}

// HandleProxy processes a Microsoft Graph calendar request through the connector.
func (c *MicrosoftCalendarConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", MicrosoftCalendarServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(MicrosoftCalendarServiceID, logger)
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftCalendarServiceID+"/login")
		return
	}

	if remainingPath == "/me/events" || remainingPath == "/me/events/" ||
		remainingPath == "/events" || remainingPath == "/events/" {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListEvents(w, r)
		return
	}

	if remainingPath == "/me/calendars" || remainingPath == "/me/calendars/" ||
		remainingPath == "/calendars" || remainingPath == "/calendars/" {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleListCalendars(w, r)
		return
	}

	if calendarID, eventID, ok := CalendarEventPathParts(remainingPath); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if eventID != "" {
			c.HandleGetEvent(w, r, token, calendarID, eventID)
			return
		}
		c.HandleListCalendarEvents(w, r, calendarID)
		return
	}

	if id, ok := BareEventIDFromProxyPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.HandleGetEvent(w, r, token, "", id)
		return
	}

	// Generic authenticated pass-through for other Graph calendar paths.
	StreamProxy(w, r, c.MicrosoftCalendarUpstreamBase(), remainingPath, token, "Microsoft Calendar", logger)
}

func (c *MicrosoftCalendarConnector) HandleListEvents(w http.ResponseWriter, r *http.Request) {
	events, truncated, err := c.ListEventsWithWindow(r.Context(), r.URL.Query())
	if err != nil {
		if err == kinds.ErrUnauthorized {
			shared.WriteJSONError(w, http.StatusUnauthorized,
				"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftCalendarServiceID+"/login")
			return
		}
		slog.Warn("microsoft calendar list events failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch events from Microsoft")
		return
	}
	if events == nil {
		events = []kinds.CalendarEvent{}
	}
	result := kinds.CalendarEventList{
		Events:    events,
		Total:     len(events),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]kinds.LibraryServiceStatus{
			MicrosoftCalendarServiceID: {
				Status:    kinds.LibraryStatusOK,
				Count:     len(events),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build events list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// microsoftCalendarItem is a Graph calendar summary.
type microsoftCalendarItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefaultCalendar"`
}

// HandleListCalendars lists the user's calendars in a neutral envelope.
func (c *MicrosoftCalendarConnector) HandleListCalendars(w http.ResponseWriter, r *http.Request) {
	token := c.host.GetValidToken(MicrosoftCalendarServiceID, slog.Default())
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftCalendarServiceID+"/login")
		return
	}
	calendars, truncated, err := c.ListCalendars(r.Context(), token)
	if err != nil {
		slog.Warn("microsoft calendar list failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch calendars from Microsoft")
		return
	}
	type cal struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Default bool   `json:"is_default"`
	}
	out := make([]cal, 0, len(calendars))
	for _, item := range calendars {
		out = append(out, cal{ID: item.ID, Name: item.Name, Default: item.IsDefault})
	}
	data, err := json.Marshal(map[string]any{
		"calendars": out,
		"total":     len(out),
		"truncated": truncated,
	})
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build calendars list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *MicrosoftCalendarConnector) HandleListCalendarEvents(w http.ResponseWriter, r *http.Request, calendarID string) {
	token := c.host.GetValidToken(MicrosoftCalendarServiceID, slog.Default())
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid Microsoft token — authorize at /v1/auth/"+MicrosoftCalendarServiceID+"/login")
		return
	}
	events, truncated, err := c.ListEventsForCalendar(r.Context(), token, calendarID, r.URL.Query())
	if err != nil {
		slog.Warn("microsoft calendar events fetch failed", "calendar", calendarID, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch events from Microsoft")
		return
	}
	if events == nil {
		events = []kinds.CalendarEvent{}
	}
	result := kinds.CalendarEventList{
		Events:    events,
		Total:     len(events),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]kinds.LibraryServiceStatus{
			MicrosoftCalendarServiceID: {
				Status:    kinds.LibraryStatusOK,
				Count:     len(events),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to build events list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// HandleGetEvent fetches one event and maps it to the neutral shape.
func (c *MicrosoftCalendarConnector) HandleGetEvent(w http.ResponseWriter, r *http.Request, token, calendarID, eventID string) {
	eventID = strings.Trim(strings.TrimSpace(eventID), "/")
	path := "/me/events/" + url.PathEscape(eventID) + "?$select=" + microsoftEventsSelect
	if calendarID != "" {
		path = "/me/calendars/" + url.PathEscape(calendarID) + "/events/" + url.PathEscape(eventID) + "?$select=" + microsoftEventsSelect
	}
	data, status, err := microsoftGraphRequest(r.Context(), token, http.MethodGet, microsoftGraphURL(c.MicrosoftCalendarUpstreamBase(), path), nil)
	if err != nil {
		slog.Error("microsoft event request failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "Microsoft Graph request failed")
		return
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(data)
		return
	}
	var raw microsoftEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to parse Microsoft Graph response")
		return
	}
	ev := MapMicrosoftCalendarEvent(raw, calendarID, "")
	out, err := json.Marshal(ev)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to encode event")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// microsoftEvent is the subset of a Graph event record we use. Times arrive
// in UTC via the Prefer header (see microsoftGraphRequest).
type microsoftEvent struct {
	ID          string        `json:"id"`
	ICalUId     string        `json:"iCalUId"`
	ETag        string        `json:"@odata.etag"`
	Subject     string        `json:"subject"`
	BodyPreview string        `json:"bodyPreview"`
	Start       microsoftTime `json:"start"`
	End         microsoftTime `json:"end"`
	IsAllDay    bool          `json:"isAllDay"`
	IsCancelled bool          `json:"isCancelled"`
	ShowAs      string        `json:"showAs"`
	Location    struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Organizer *struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"organizer"`
	Attendees []struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
		Status struct {
			Response string `json:"response"`
		} `json:"status"`
	} `json:"attendees"`
	WebLink string `json:"webLink"`
	Type    string `json:"type"`
}

// ListCalendarEvents implements CalendarEventProvider. Fetches the user's
// calendarView within the default window (every readable calendar at once).
func (c *MicrosoftCalendarConnector) ListCalendarEvents(ctx context.Context) ([]kinds.CalendarEvent, bool, error) {
	return c.ListEventsWithWindow(ctx, nil)
}

// ListCalendarEventsDelta implements kinds.CalendarEventDeltaProvider over
// /me/calendarView/delta (issue #145). deltaToken is the previously
// recorded deltaLink ("": a fresh feed over the default window, which
// returns the full collection). Deleted records the feed reports are
// dropped — convergence places and never deletes. nextToken is "" when
// the walk did not reach the feed's resume link, so the next run replays
// from the old token.
func (c *MicrosoftCalendarConnector) ListCalendarEventsDelta(ctx context.Context, deltaToken string) ([]kinds.CalendarEvent, string, bool, error) {
	token := c.host.GetValidToken(MicrosoftCalendarServiceID, slog.Default())
	if token == "" {
		return nil, "", false, kinds.ErrUnauthorized
	}
	next := strings.TrimSpace(deltaToken)
	if next == "" {
		timeMin, timeMax := MicrosoftCalendarDefaultWindow()
		values := url.Values{}
		values.Set("startDateTime", timeMin)
		values.Set("endDateTime", timeMax)
		values.Set("$select", microsoftEventsSelect)
		next = microsoftGraphURL(c.MicrosoftCalendarUpstreamBase(), "/me/calendarView/delta?"+values.Encode())
	}
	var all []kinds.CalendarEvent
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, next, nil)
		if err != nil {
			return nil, "", false, err
		}
		// 410 Gone: the recorded token expired — restart the feed.
		if status == http.StatusGone && deltaToken != "" {
			return c.ListCalendarEventsDelta(ctx, "")
		}
		if status != http.StatusOK {
			return nil, "", false, fmt.Errorf("microsoft calendar delta returned %d", status)
		}
		records, nextLink, deltaLink, err := decodeGraphDelta[microsoftEvent](data)
		if err != nil {
			return nil, "", false, err
		}
		for _, item := range records {
			mapped := MapMicrosoftCalendarEvent(item, "", "")
			if mapped.Title == "" && mapped.EntityID == "" && len(mapped.Sources) == 0 {
				continue
			}
			all = append(all, mapped)
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

// ListEventsWithWindow lists events across the user's calendars.
func (c *MicrosoftCalendarConnector) ListEventsWithWindow(ctx context.Context, q url.Values) ([]kinds.CalendarEvent, bool, error) {
	token := c.host.GetValidToken(MicrosoftCalendarServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	timeMin, timeMax := MicrosoftCalendarDefaultWindow()
	if q != nil {
		if v := strings.TrimSpace(q.Get("timeMin")); v != "" {
			timeMin = v
		}
		if v := strings.TrimSpace(q.Get("timeMax")); v != "" {
			timeMax = v
		}
	}
	next := c.calendarViewURL("", timeMin, timeMax, q)
	var all []kinds.CalendarEvent
	truncated := false
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		events, nextLink, tr, err := c.fetchEventPage(ctx, token, next, "")
		if err != nil {
			return nil, false, err
		}
		all = append(all, events...)
		if tr {
			truncated = true
		}
		next = nextLink
		if page == shared.LibraryPageCap-1 && next != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// ListEventsForCalendar lists events for one calendar.
func (c *MicrosoftCalendarConnector) ListEventsForCalendar(ctx context.Context, token, calendarID string, q url.Values) ([]kinds.CalendarEvent, bool, error) {
	timeMin, timeMax := MicrosoftCalendarDefaultWindow()
	if q != nil {
		if v := strings.TrimSpace(q.Get("timeMin")); v != "" {
			timeMin = v
		}
		if v := strings.TrimSpace(q.Get("timeMax")); v != "" {
			timeMax = v
		}
	}
	next := c.calendarViewURL(calendarID, timeMin, timeMax, q)
	var all []kinds.CalendarEvent
	truncated := false
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		events, nextLink, tr, err := c.fetchEventPage(ctx, token, next, calendarID)
		if err != nil {
			return nil, false, err
		}
		all = append(all, events...)
		if tr {
			truncated = true
		}
		next = nextLink
		if page == shared.LibraryPageCap-1 && next != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// calendarViewURL builds the first calendarView page URL (all calendars or
// one collection).
func (c *MicrosoftCalendarConnector) calendarViewURL(calendarID, timeMin, timeMax string, q url.Values) string {
	values := url.Values{}
	values.Set("startDateTime", timeMin)
	values.Set("endDateTime", timeMax)
	values.Set("$select", microsoftEventsSelect)
	values.Set("$top", "100")
	if q != nil {
		if v := strings.TrimSpace(q.Get("pageSize")); v != "" {
			values.Set("$top", v)
		}
	}
	path := "/me/calendarView"
	if calendarID != "" {
		path = "/me/calendars/" + url.PathEscape(calendarID) + "/calendarView"
	}
	return microsoftGraphURL(c.MicrosoftCalendarUpstreamBase(), path+"?"+values.Encode())
}

// fetchEventPage reads one calendarView page and maps it.
func (c *MicrosoftCalendarConnector) fetchEventPage(ctx context.Context, token, pageURL, calendarID string) ([]kinds.CalendarEvent, string, bool, error) {
	data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	if status != http.StatusOK {
		return nil, "", false, fmt.Errorf("microsoft calendarView returned %d", status)
	}
	var raw graphList[microsoftEvent]
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, "", false, err
	}
	calName := ""
	out := make([]kinds.CalendarEvent, 0, len(raw.Value))
	for _, item := range raw.Value {
		mapped := MapMicrosoftCalendarEvent(item, calendarID, calName)
		if mapped.Title == "" && mapped.EntityID == "" && len(mapped.Sources) == 0 {
			continue
		}
		out = append(out, mapped)
	}
	return out, raw.NextLink, false, nil
}

// ListCalendars lists the user's calendars (id/name/default).
func (c *MicrosoftCalendarConnector) ListCalendars(ctx context.Context, token string) ([]microsoftCalendarItem, bool, error) {
	next := microsoftGraphURL(c.MicrosoftCalendarUpstreamBase(), "/me/calendars?$select=id,name,isDefaultCalendar&$top=100")
	var all []microsoftCalendarItem
	truncated := false
	for page := 0; page < shared.LibraryPageCap && next != ""; page++ {
		data, status, err := microsoftGraphRequest(ctx, token, http.MethodGet, next, nil)
		if err != nil {
			return nil, false, err
		}
		if status != http.StatusOK {
			return nil, false, fmt.Errorf("microsoft calendars list returned %d", status)
		}
		var raw graphList[microsoftCalendarItem]
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		all = append(all, raw.Value...)
		next = raw.NextLink
		if page == shared.LibraryPageCap-1 && next != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// MicrosoftCalendarDefaultWindow is the library listing window (past + future),
// mirroring the Google Calendar surface.
func MicrosoftCalendarDefaultWindow() (timeMin, timeMax string) {
	now := time.Now().UTC()
	return now.AddDate(0, 0, -microsoftCalendarDefaultPastDays).Format(time.RFC3339),
		now.AddDate(0, 0, microsoftCalendarDefaultFutureDays).Format(time.RFC3339)
}

// MapMicrosoftCalendarEvent converts a Graph event into the neutral shape.
// iCalUId becomes the durable cross-service EntityID so Microsoft and Google
// events merge on iCalUID (issue #145).
func MapMicrosoftCalendarEvent(raw microsoftEvent, calendarID, calendarName string) kinds.CalendarEvent {
	startAt := microsoftTimeToRFC3339(raw.Start)
	endAt := microsoftTimeToRFC3339(raw.End)

	src := kinds.CalendarEventSource{
		Service:     MicrosoftCalendarServiceID,
		ServiceName: MicrosoftCalendarServiceName,
		ID:          raw.ID,
		CalendarID:  calendarID,
		URL:         raw.WebLink,
		Status:      microsoftEventStatus(raw.ShowAs, raw.IsCancelled),
		Canonical:   true,
	}
	extra := map[string]any{}
	if raw.ICalUId != "" {
		extra["ical_uid"] = raw.ICalUId
	}
	if raw.ETag != "" {
		// The freshness probe's watermark token (issue #377).
		extra["etag"] = raw.ETag
	}
	if raw.Type != "" {
		extra["event_type"] = raw.Type
	}
	if calendarName != "" && calendarName != calendarID {
		extra["calendar_name"] = calendarName
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	ev := kinds.CalendarEvent{
		Title:        strings.TrimSpace(raw.Subject),
		Description:  raw.BodyPreview,
		StartAt:      startAt,
		EndAt:        endAt,
		AllDay:       raw.IsAllDay,
		Location:     strings.TrimSpace(raw.Location.DisplayName),
		Status:       microsoftEventStatus(raw.ShowAs, raw.IsCancelled),
		CalendarID:   calendarID,
		CalendarName: calendarName,
		EntityID:     raw.ICalUId,
		Sources:      []kinds.CalendarEventSource{src},
	}

	if raw.Organizer != nil && (raw.Organizer.EmailAddress.Address != "" || raw.Organizer.EmailAddress.Name != "") {
		org := kinds.NewContact(
			MicrosoftCalendarServiceID,
			MicrosoftCalendarServiceName,
			"",
			raw.Organizer.EmailAddress.Name,
			[]string{raw.Organizer.EmailAddress.Address},
			nil,
		)
		ev.Organizer = &org
	}

	for _, att := range raw.Attendees {
		if strings.TrimSpace(att.EmailAddress.Address) == "" && strings.TrimSpace(att.EmailAddress.Name) == "" {
			continue
		}
		c := kinds.NewContact(
			MicrosoftCalendarServiceID,
			MicrosoftCalendarServiceName,
			"",
			att.EmailAddress.Name,
			[]string{att.EmailAddress.Address},
			nil,
		)
		if len(c.Sources) > 0 && att.Status.Response != "" {
			if c.Sources[0].Extra == nil {
				c.Sources[0].Extra = map[string]any{}
			}
			c.Sources[0].Extra["response_status"] = att.Status.Response
		}
		ev.Attendees = append(ev.Attendees, c)
	}
	return ev
}
