package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GoogleCalendarConnector proxies the Google Calendar API and implements
// CalendarEventProvider. It lists the user's calendar entries as neutral
// CalendarEvent values so agents can ask "what's on my calendar?" through
// /v1/library/events. Event content stays on Google Calendar (non-Guardian
// canonical, issue #16). Read-only in this phase — agent write ops are not
// advertised so permissions stay constrained (issue #61).
// https://github.com/fudomunro/aperture-guardian/issues/65
type GoogleCalendarConnector struct {
	server *Server
}

// NewGoogleCalendarConnector creates a connector wired to the given server.
func NewGoogleCalendarConnector(srv *Server) *GoogleCalendarConnector {
	return &GoogleCalendarConnector{server: srv}
}

// googleCalendarServiceID is the connector/service ID used in config, tokens,
// sources[], and admin. Documented name is "Google Calendar".
const googleCalendarServiceID = "google-calendar"

const googleCalendarServiceName = "Google Calendar"

// Default library window: recent past + near future, so "my calendar" answers
// cover both last month's meetings and upcoming plans without unbounded scans.
const (
	googleCalendarDefaultPastDays   = 30
	googleCalendarDefaultFutureDays = 90
	googleCalendarMaxCalendars      = 20
)

// googleCalendarUpstreamBase returns the configured Calendar API base.
func (c *GoogleCalendarConnector) googleCalendarUpstreamBase() string {
	const defaultBase = "https://www.googleapis.com/calendar/v3"
	if svc := c.server.config.GetService(googleCalendarServiceID); svc != nil && svc.Endpoint != "" {
		return strings.TrimRight(svc.Endpoint, "/")
	}
	return defaultBase
}

// Capabilities returns what Google Calendar data types and operations are supported.
func (c *GoogleCalendarConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "events",
				Description: "Calendar events — list calendars and events (metadata only; event content stays on Google Calendar)",
				Operations: []Operation{
					{
						Name:        "read_list",
						Description: "List the user's calendar events as neutral CalendarEvent values (paginated). Permission key: events.",
						Method:      "GET",
						Path:        "/me/events",
						Parameters: []Parameter{
							{Name: "timeMin", In: "query", Type: "string", Description: "RFC3339 lower bound (default: 30 days ago)"},
							{Name: "timeMax", In: "query", Type: "string", Description: "RFC3339 upper bound (default: 90 days ahead)"},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page (1-2500)", Default: 250},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Neutral events: title, description, start_at, end_at, all_day, location, status, organizer, attendees, sources[]",
							Fields:      []string{"events", "total"},
						},
					},
					{
						Name:        "list_calendars",
						Description: "List the user's calendars (id, summary, access role, primary)",
						Method:      "GET",
						Path:        "/me/calendars",
						Parameters: []Parameter{
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max calendars per page (1-250)", Default: 250},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Calendars: id, summary, primary, access_role",
							Fields:      []string{"calendars", "total"},
						},
					},
					{
						Name:        "list_calendar_events",
						Description: "List events for one calendar as neutral CalendarEvent values",
						Method:      "GET",
						Path:        "/calendars/{calendarId}/events",
						Parameters: []Parameter{
							{Name: "calendarId", In: "path", Type: "string", Description: "Calendar ID or 'primary'", Required: true},
							{Name: "timeMin", In: "query", Type: "string", Description: "RFC3339 lower bound"},
							{Name: "timeMax", In: "query", Type: "string", Description: "RFC3339 upper bound"},
							{Name: "pageSize", In: "query", Type: "integer", Description: "Max items per page", Default: 250},
							{Name: "pageToken", In: "query", Type: "string", Description: "Page token for pagination"},
						},
						Response: ResponseSpec{
							Description: "Neutral events for the calendar",
							Fields:      []string{"events", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single calendar event by service-local ID",
						Method:      "GET",
						Path:        "/events/{id}",
						Parameters: []Parameter{
							{Name: "id", In: "path", Type: "string", Description: "Service-local event ID", Required: true},
						},
						Response: ResponseSpec{
							Description: "Neutral calendar event with service-local id and sources[]",
							Fields:      []string{"title", "start_at", "end_at", "location", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when Google Calendar is referenced.
func (c *GoogleCalendarConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	base := root + "/v1/services/" + googleCalendarServiceID
	return fmt.Sprintf(`When the user's request involves Google Calendar, their schedule, meetings, or calendar events, you MUST use the Guardian proxy at %s instead of calling Google Calendar API directly.

For "my calendar" across every connected calendar service, prefer the unified library endpoint first:
GET %s/v1/library/events — calendar events in one neutral list (title, start_at, end_at, all_day, location, organizer, attendees, sources[])

Google Calendar-specific endpoints (call the full proxy URL; Guardian authenticates you):
GET /me/events — list calendar events (neutral CalendarEvent metadata)
GET /me/calendars — list the user's calendars
GET /calendars/{calendarId}/events — events for one calendar (calendarId may be "primary")
GET /events/{id} — one event by service-local ID

Calendar events use Guardian's neutral identity matching for organizer/attendees (email → phone → handle → name), so people on a meeting can be matched to /v1/library/contacts when a contact service is connected.
Event content stays on Google Calendar — Guardian does not store calendars.
Always include the full proxy base URL in your requests.`, base, root)
}

// HandleProxy processes a Google Calendar API request through the connector.
func (c *GoogleCalendarConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", googleCalendarServiceID, "method", r.Method, "path", remainingPath)

	token := c.server.getValidToken(googleCalendarServiceID, logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Google Calendar token — authorize at /v1/auth/"+googleCalendarServiceID+"/login")
		return
	}

	if remainingPath == "/me/events" || remainingPath == "/me/events/" ||
		remainingPath == "/events" || remainingPath == "/events/" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListEvents(w, r)
		return
	}

	if remainingPath == "/me/calendars" || remainingPath == "/me/calendars/" ||
		remainingPath == "/calendars" || remainingPath == "/calendars/" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleListCalendars(w, r)
		return
	}

	if calendarID, eventID, ok := calendarEventPathParts(remainingPath); ok {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if eventID != "" {
			c.handleGetEvent(w, r, token, calendarID, eventID)
			return
		}
		c.handleListCalendarEvents(w, r, calendarID)
		return
	}

	if id, ok := bareEventIDFromProxyPath(remainingPath); ok {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		c.handleGetEvent(w, r, token, "primary", id)
		return
	}

	// Generic authenticated pass-through for other Calendar API paths.
	upstreamURL := c.googleCalendarUpstreamBase() + remainingPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		logger.Error("failed to build upstream request", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		logger.Error("upstream request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "Google Calendar request failed")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// calendarEventPathParts parses /calendars/{id}/events and
// /calendars/{id}/events/{eventId} (also under /me/calendars/...).
// eventID is empty for list requests.
func calendarEventPathParts(remainingPath string) (calendarID, eventID string, ok bool) {
	p := strings.Trim(remainingPath, "/")
	if p == "" {
		return "", "", false
	}
	parts := strings.Split(p, "/")
	// calendars/{id}/events[/{eventId}]
	if len(parts) >= 3 && parts[0] == "calendars" && parts[2] == "events" {
		calID, _ := url.PathUnescape(parts[1])
		if calID == "" {
			return "", "", false
		}
		if len(parts) == 3 {
			return calID, "", true
		}
		if len(parts) == 4 && parts[3] != "" {
			evID, _ := url.PathUnescape(parts[3])
			return calID, evID, true
		}
		return "", "", false
	}
	// me/calendars/{id}/events[/{eventId}]
	if len(parts) >= 4 && parts[0] == "me" && parts[1] == "calendars" && parts[3] == "events" {
		calID, _ := url.PathUnescape(parts[2])
		if calID == "" {
			return "", "", false
		}
		if len(parts) == 4 {
			return calID, "", true
		}
		if len(parts) == 5 && parts[4] != "" {
			evID, _ := url.PathUnescape(parts[4])
			return calID, evID, true
		}
	}
	return "", "", false
}

// bareEventIDFromProxyPath extracts an event id from /events/{id} or /me/events/{id}.
func bareEventIDFromProxyPath(remainingPath string) (string, bool) {
	p := remainingPath
	for _, prefix := range []string{"/me/events/", "/events/"} {
		if strings.HasPrefix(p, prefix) {
			id := strings.Trim(strings.TrimPrefix(p, prefix), "/")
			return id, id != ""
		}
	}
	return "", false
}

func (c *GoogleCalendarConnector) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, truncated, err := c.listEventsWithWindow(r.Context(), r.URL.Query())
	if err != nil {
		if err == ErrUnauthorized {
			writeJSONError(w, http.StatusUnauthorized,
				"no valid Google Calendar token — authorize at /v1/auth/"+googleCalendarServiceID+"/login")
			return
		}
		slog.Warn("google calendar list events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch events from Google Calendar")
		return
	}
	if events == nil {
		events = []CalendarEvent{}
	}
	result := CalendarEventList{
		Events:    events,
		Total:     len(events),
		Synced:    true,
		Truncated: truncated,
		ByService: map[string]LibraryServiceStatus{
			googleCalendarServiceID: {
				Status:    LibraryStatusOK,
				Count:     len(events),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build events list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleCalendarConnector) handleListCalendarEvents(w http.ResponseWriter, r *http.Request, calendarID string) {
	token := c.server.getValidToken(googleCalendarServiceID, slog.Default())
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Google Calendar token — authorize at /v1/auth/"+googleCalendarServiceID+"/login")
		return
	}
	events, truncated, err := c.listEventsForCalendar(r.Context(), token, calendarID, r.URL.Query())
	if err != nil {
		slog.Warn("google calendar list calendar events failed", "calendar", calendarID, "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch events from Google Calendar")
		return
	}
	if events == nil {
		events = []CalendarEvent{}
	}
	result := CalendarEventList{
		Events: events,
		Total:  len(events),
		Synced: true,
		ByService: map[string]LibraryServiceStatus{
			googleCalendarServiceID: {
				Status:    LibraryStatusOK,
				Count:     len(events),
				Truncated: truncated,
			},
		},
		Truncated: truncated,
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build events list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleCalendarConnector) handleListCalendars(w http.ResponseWriter, r *http.Request) {
	token := c.server.getValidToken(googleCalendarServiceID, slog.Default())
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid Google Calendar token — authorize at /v1/auth/"+googleCalendarServiceID+"/login")
		return
	}
	calendars, truncated, err := c.listCalendars(r.Context(), token, r.URL.Query())
	if err != nil {
		slog.Warn("google calendar list calendars failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "failed to fetch calendars from Google Calendar")
		return
	}
	result := map[string]any{
		"calendars": calendars,
		"total":     len(calendars),
		"truncated": truncated,
		"by_service": map[string]LibraryServiceStatus{
			googleCalendarServiceID: {
				Status:    LibraryStatusOK,
				Count:     len(calendars),
				Truncated: truncated,
			},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build calendars list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (c *GoogleCalendarConnector) handleGetEvent(w http.ResponseWriter, r *http.Request, token, calendarID, eventID string) {
	upstreamURL := c.googleCalendarUpstreamBase() + "/calendars/" + url.PathEscape(calendarID) +
		"/events/" + url.PathEscape(eventID) + "?singleEvents=true"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to build request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Google Calendar request failed")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to read Google Calendar response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
		return
	}
	var raw googleCalendarEvent
	if err := json.Unmarshal(data, &raw); err != nil {
		writeJSONError(w, http.StatusBadGateway, "failed to parse Google Calendar response")
		return
	}
	ev := mapGoogleCalendarEvent(raw, calendarID, googleCalendarServiceName)
	out, err := json.Marshal(ev)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to encode event")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ListCalendarEvents implements CalendarEventProvider. Fetches calendarList,
// then events from each readable calendar within the default time window.
func (c *GoogleCalendarConnector) ListCalendarEvents(ctx context.Context) ([]CalendarEvent, bool, error) {
	return c.listEventsWithWindow(ctx, nil)
}

func (c *GoogleCalendarConnector) listEventsWithWindow(ctx context.Context, q url.Values) ([]CalendarEvent, bool, error) {
	token := c.server.getValidToken(googleCalendarServiceID, slog.Default())
	if token == "" {
		return nil, false, ErrUnauthorized
	}
	calendars, calTruncated, err := c.listCalendars(ctx, token, nil)
	if err != nil {
		return nil, false, err
	}
	var all []CalendarEvent
	truncated := calTruncated
	for _, cal := range calendars {
		events, tr, err := c.listEventsForCalendar(ctx, token, cal.ID, q)
		if err != nil {
			// Fail-open per calendar: one unreadable calendar must not empty the library.
			slog.Warn("google calendar events fetch failed", "calendar", cal.ID, "error", err)
			continue
		}
		if tr {
			truncated = true
		}
		all = append(all, events...)
	}
	return all, truncated, nil
}

// googleCalendarListItem is a calendarList entry.
type googleCalendarListItem struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	Primary    bool   `json:"primary"`
	AccessRole string `json:"accessRole"`
	Deleted    bool   `json:"deleted"`
}

type googleCalendarList struct {
	Items         []googleCalendarListItem `json:"items"`
	NextPageToken string                   `json:"nextPageToken"`
}

func (c *GoogleCalendarConnector) listCalendars(ctx context.Context, token string, q url.Values) ([]googleCalendarListItem, bool, error) {
	base := c.googleCalendarUpstreamBase() + "/users/me/calendarList"
	pageToken := ""
	if q != nil {
		pageToken = q.Get("pageToken")
	}
	var all []googleCalendarListItem
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		u := base + "?maxResults=250&showHidden=true"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("google calendar list returned %d", resp.StatusCode)
		}
		var raw googleCalendarList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, item := range raw.Items {
			if item.Deleted || item.ID == "" {
				continue
			}
			// freeBusyReader calendars expose availability only, not event bodies.
			if item.AccessRole == "freeBusyReader" {
				continue
			}
			all = append(all, item)
			if len(all) >= googleCalendarMaxCalendars {
				if raw.NextPageToken != "" {
					truncated = true
				}
				return all, truncated, nil
			}
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == libraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// googleCalendarEventTime is Google's start/end payload (dateTime or date).
type googleCalendarEventTime struct {
	DateTime string `json:"dateTime"`
	Date     string `json:"date"`
	TimeZone string `json:"timeZone"`
}

type googleCalendarEventAttendee struct {
	Email          string `json:"email"`
	DisplayName    string `json:"displayName"`
	ResponseStatus string `json:"responseStatus"`
	Self           bool   `json:"self"`
	Organizer      bool   `json:"organizer"`
}

type googleCalendarEvent struct {
	ID          string                  `json:"id"`
	ICalUID     string                  `json:"iCalUID"`
	Status      string                  `json:"status"`
	Summary     string                  `json:"summary"`
	Description string                  `json:"description"`
	Location    string                  `json:"location"`
	Start       googleCalendarEventTime `json:"start"`
	End         googleCalendarEventTime `json:"end"`
	Organizer   *struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"organizer"`
	Attendees   []googleCalendarEventAttendee `json:"attendees"`
	HTMLLink    string                        `json:"htmlLink"`
	CalendarUID string                        `json:"-"` // injected by Guardian when mapping from a calendar
}

type googleCalendarEventsList struct {
	Items         []googleCalendarEvent `json:"items"`
	NextPageToken string                `json:"nextPageToken"`
}

func (c *GoogleCalendarConnector) listEventsForCalendar(ctx context.Context, token, calendarID string, q url.Values) ([]CalendarEvent, bool, error) {
	base := c.googleCalendarUpstreamBase() + "/calendars/" + url.PathEscape(calendarID) + "/events"
	timeMin, timeMax := googleCalendarDefaultWindow()
	if q != nil {
		if v := strings.TrimSpace(q.Get("timeMin")); v != "" {
			timeMin = v
		}
		if v := strings.TrimSpace(q.Get("timeMax")); v != "" {
			timeMax = v
		}
	}
	pageToken := ""
	if q != nil {
		pageToken = q.Get("pageToken")
	}
	pageSize := "250"
	if q != nil {
		if v := strings.TrimSpace(q.Get("pageSize")); v != "" {
			pageSize = v
		}
	}

	calName := calendarID
	var all []CalendarEvent
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		values := url.Values{}
		values.Set("singleEvents", "true")
		values.Set("orderBy", "startTime")
		values.Set("maxResults", pageSize)
		if timeMin != "" {
			values.Set("timeMin", timeMin)
		}
		if timeMax != "" {
			values.Set("timeMax", timeMax)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		u := base + "?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, false, fmt.Errorf("google calendar events returned %d", resp.StatusCode)
		}
		var raw googleCalendarEventsList
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, false, err
		}
		for _, item := range raw.Items {
			mapped := mapGoogleCalendarEvent(item, calendarID, calName)
			if mapped.Title == "" && mapped.EntityID == "" && len(mapped.Sources) == 0 {
				continue
			}
			all = append(all, mapped)
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			break
		}
		if page == libraryPageCap-1 && pageToken != "" {
			truncated = true
		}
	}
	return all, truncated, nil
}

// googleCalendarDefaultWindow is the library listing window (past + future).
func googleCalendarDefaultWindow() (timeMin, timeMax string) {
	now := time.Now().UTC()
	return now.AddDate(0, 0, -googleCalendarDefaultPastDays).Format(time.RFC3339),
		now.AddDate(0, 0, googleCalendarDefaultFutureDays).Format(time.RFC3339)
}

// mapGoogleCalendarEvent converts a Google Calendar event into the neutral shape.
func mapGoogleCalendarEvent(raw googleCalendarEvent, calendarID, calendarName string) CalendarEvent {
	startAt, allDay := googleCalendarTimeToRFC3339(raw.Start)
	endAt, _ := googleCalendarTimeToRFC3339(raw.End)

	src := CalendarEventSource{
		Service:     googleCalendarServiceID,
		ServiceName: googleCalendarServiceName,
		ID:          raw.ID,
		CalendarID:  calendarID,
		URL:         raw.HTMLLink,
		Status:      raw.Status,
		Canonical:   true,
	}
	extra := map[string]any{}
	if raw.ICalUID != "" {
		extra["ical_uid"] = raw.ICalUID
	}
	if raw.Start.TimeZone != "" {
		extra["start_time_zone"] = raw.Start.TimeZone
	}
	if calendarName != "" && calendarName != calendarID {
		extra["calendar_name"] = calendarName
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	ev := CalendarEvent{
		Title:        strings.TrimSpace(raw.Summary),
		Description:  raw.Description,
		StartAt:      startAt,
		EndAt:        endAt,
		AllDay:       allDay,
		Location:     raw.Location,
		Status:       raw.Status,
		CalendarID:   calendarID,
		CalendarName: calendarName,
		EntityID:     raw.ICalUID,
		Sources:      []CalendarEventSource{src},
	}

	if raw.Organizer != nil {
		org := newContact(
			googleCalendarServiceID,
			googleCalendarServiceName,
			"",
			raw.Organizer.DisplayName,
			[]string{raw.Organizer.Email},
			nil,
		)
		ev.Organizer = &org
	}

	for _, att := range raw.Attendees {
		if strings.TrimSpace(att.Email) == "" && strings.TrimSpace(att.DisplayName) == "" {
			continue
		}
		c := newContact(
			googleCalendarServiceID,
			googleCalendarServiceName,
			"",
			att.DisplayName,
			[]string{att.Email},
			nil,
		)
		if len(c.Sources) > 0 && att.ResponseStatus != "" {
			if c.Sources[0].Extra == nil {
				c.Sources[0].Extra = map[string]any{}
			}
			c.Sources[0].Extra["response_status"] = att.ResponseStatus
			if att.Self {
				c.Sources[0].Extra["self"] = true
			}
		}
		ev.Attendees = append(ev.Attendees, c)
	}
	return ev
}

// googleCalendarTimeToRFC3339 maps Google dateTime or date to RFC3339 UTC.
// All-day events use the date at midnight UTC and set allDay=true.
func googleCalendarTimeToRFC3339(t googleCalendarEventTime) (string, bool) {
	if strings.TrimSpace(t.DateTime) != "" {
		raw := strings.TrimSpace(t.DateTime)
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UTC().Format(time.RFC3339), false
		}
		return raw, false
	}
	d := strings.TrimSpace(t.Date)
	if d == "" {
		return "", false
	}
	if parsed, err := time.Parse("2006-01-02", d); err == nil {
		return parsed.UTC().Format(time.RFC3339), true
	}
	return d + "T00:00:00Z", true
}
