package connectors

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Minimal iCalendar VEVENT reader for CalDAV (issue #146). Maps SUMMARY,
// DESCRIPTION, DTSTART/DTEND (and DATE forms), LOCATION, STATUS, UID,
// ORGANIZER, ATTENDEE, RRULE, and EXDATE. RRULE expansion is windowed: only
// occurrences that intersect [windowStart, windowEnd) are emitted so free/busy
// and "my calendar" stay bounded (issue #128).

const (
	icalTimeLayoutDate = "20060102"
	icalTimeLayoutUTC  = "20060102T150405Z"
	icalTimeLayoutLoc  = "20060102T150405"
)

// ICalEvent is one parsed VEVENT before neutral mapping.
type ICalEvent struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Status      string
	Start       time.Time
	End         time.Time
	AllDay      bool
	RRule       string
	EXDates     []time.Time
	Organizer   string
	OrganizerCN string
	Attendees   []ICalAttendee
	CalendarID  string
	ETag        string
	Href        string
}

// ICalAttendee is one ATTENDEE line.
type ICalAttendee struct {
	Email string
	CN    string
	Role  string
}

// ParseICalendar extracts VEVENTs from a VCALENDAR body.
func ParseICalendar(body string) []ICalEvent {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	lines := unfoldVCard(body) // same folding rules as vCard
	var events []ICalEvent
	var cur *ICalEvent
	inEvent := false

	for _, line := range lines {
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		if upper == "BEGIN:VEVENT" {
			inEvent = true
			cur = &ICalEvent{}
			continue
		}
		if upper == "END:VEVENT" {
			if cur != nil {
				if !cur.End.IsZero() && !cur.Start.IsZero() && cur.End.Before(cur.Start) {
					cur.End = cur.Start
				}
				events = append(events, *cur)
			}
			inEvent = false
			cur = nil
			continue
		}
		if !inEvent || cur == nil {
			continue
		}
		name, params, value := splitVCardProp(line)
		upperName := strings.ToUpper(name)
		// Strip property group prefix (e.g. "item1.EMAIL").
		if i := strings.IndexByte(upperName, '.'); i >= 0 {
			upperName = upperName[i+1:]
		}
		switch upperName {
		case "UID":
			cur.UID = strings.TrimSpace(value)
		case "SUMMARY":
			cur.Summary = unescapeVCard(value)
		case "DESCRIPTION":
			cur.Description = unescapeVCard(value)
		case "LOCATION":
			cur.Location = unescapeVCard(value)
		case "STATUS":
			cur.Status = strings.ToLower(strings.TrimSpace(value))
		case "DTSTART":
			t, allDay := parseICalTime(value, params)
			cur.Start = t
			if allDay {
				cur.AllDay = true
			}
		case "DTEND":
			t, allDay := parseICalTime(value, params)
			cur.End = t
			if allDay {
				cur.AllDay = true
			}
		case "DURATION":
			if cur.End.IsZero() && !cur.Start.IsZero() {
				if d, ok := parseICalDuration(value); ok {
					cur.End = cur.Start.Add(d)
				}
			}
		case "RRULE":
			cur.RRule = strings.TrimSpace(value)
		case "EXDATE":
			t, _ := parseICalTime(value, params)
			if !t.IsZero() {
				cur.EXDates = append(cur.EXDates, t)
			}
		case "ORGANIZER":
			cur.Organizer = mailtoValue(value)
			cur.OrganizerCN = paramValue(params, "CN")
		case "ATTENDEE":
			att := ICalAttendee{
				Email: mailtoValue(value),
				CN:    paramValue(params, "CN"),
				Role:  paramValue(params, "ROLE"),
			}
			if att.Email != "" || att.CN != "" {
				cur.Attendees = append(cur.Attendees, att)
			}
		}
	}
	return events
}

// ExpandICalEvent emits concrete occurrences of one VEVENT intersecting
// [start, end). Non-recurring events return themselves when they overlap.
func ExpandICalEvent(ev ICalEvent, start, end time.Time) []ICalEvent {
	if ev.Start.IsZero() {
		return nil
	}
	dur := ev.End.Sub(ev.Start)
	if dur <= 0 {
		dur = time.Hour
	}

	if strings.TrimSpace(ev.RRule) == "" {
		occEnd := ev.Start.Add(dur)
		if overlaps(ev.Start, occEnd, start, end) {
			return []ICalEvent{ev}
		}
		return nil
	}

	occurrences := expandRRule(ev.Start, ev.RRule, ev.EXDates, start, end)
	out := make([]ICalEvent, 0, len(occurrences))
	for _, t := range occurrences {
		copy := ev
		copy.Start = t
		copy.End = t.Add(dur)
		copy.RRule = "" // expanded
		out = append(out, copy)
	}
	return out
}

// MapICalEvent converts one occurrence into a neutral CalendarEvent.
func MapICalEvent(service, serviceName, calendarID, calendarName string, ev ICalEvent) types.CalendarEvent {
	startAt := ev.Start.UTC().Format(time.RFC3339)
	endAt := ev.End.UTC().Format(time.RFC3339)
	if ev.AllDay {
		// Date-only: midnight UTC of that date.
		startAt = ev.Start.UTC().Format("2006-01-02") + "T00:00:00Z"
		endAt = ev.End.UTC().Format("2006-01-02") + "T00:00:00Z"
	}

	src := types.CalendarEventSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          ev.UID,
		CalendarID:  calendarID,
		URL:         ev.Href,
		Status:      ev.Status,
		Canonical:   true,
	}
	extra := map[string]any{}
	if ev.UID != "" {
		extra["uid"] = ev.UID
	}
	if ev.ETag != "" {
		extra["etag"] = ev.ETag
	}
	if ev.Href != "" {
		extra["href"] = ev.Href
	}
	if len(extra) > 0 {
		src.Extra = extra
	}

	out := types.CalendarEvent{
		Title:        ev.Summary,
		Description:  ev.Description,
		StartAt:      startAt,
		EndAt:        endAt,
		AllDay:       ev.AllDay,
		Location:     ev.Location,
		Status:       ev.Status,
		CalendarID:   calendarID,
		CalendarName: calendarName,
		EntityID:     ev.UID,
		Sources:      []types.CalendarEventSource{src},
	}
	if ev.Organizer != "" || ev.OrganizerCN != "" {
		org := types.NewContact(service, serviceName, "", ev.OrganizerCN, []string{ev.Organizer}, nil)
		out.Organizer = &org
	}
	for _, a := range ev.Attendees {
		c := types.NewContact(service, serviceName, "", a.CN, []string{a.Email}, nil)
		if len(c.Sources) > 0 && a.Role != "" {
			if c.Sources[0].Extra == nil {
				c.Sources[0].Extra = map[string]any{}
			}
			c.Sources[0].Extra["response_status"] = strings.ToLower(a.Role)
		}
		out.Attendees = append(out.Attendees, c)
	}
	return out
}

// EncodeICalendar builds a single-event VCALENDAR for PUT.
func EncodeICalendar(ev ICalEvent) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Aegis//CalDAV//EN\r\nBEGIN:VEVENT\r\n")
	uid := ev.UID
	if uid == "" {
		uid = "aegis-" + strconv.FormatInt(ev.Start.Unix(), 10)
	}
	b.WriteString("UID:" + uid + "\r\n")
	if ev.Summary != "" {
		b.WriteString("SUMMARY:" + escapeICalText(ev.Summary) + "\r\n")
	}
	if ev.Description != "" {
		b.WriteString("DESCRIPTION:" + escapeICalText(ev.Description) + "\r\n")
	}
	if ev.Location != "" {
		b.WriteString("LOCATION:" + escapeICalText(ev.Location) + "\r\n")
	}
	if ev.Status != "" {
		b.WriteString("STATUS:" + strings.ToUpper(ev.Status) + "\r\n")
	}
	if !ev.Start.IsZero() {
		if ev.AllDay {
			b.WriteString("DTSTART;VALUE=DATE:" + ev.Start.UTC().Format(icalTimeLayoutDate) + "\r\n")
			if !ev.End.IsZero() {
				b.WriteString("DTEND;VALUE=DATE:" + ev.End.UTC().Format(icalTimeLayoutDate) + "\r\n")
			}
		} else {
			b.WriteString("DTSTART:" + ev.Start.UTC().Format(icalTimeLayoutUTC) + "\r\n")
			if !ev.End.IsZero() {
				b.WriteString("DTEND:" + ev.End.UTC().Format(icalTimeLayoutUTC) + "\r\n")
			}
		}
	}
	if ev.Organizer != "" {
		cn := ""
		if ev.OrganizerCN != "" {
			cn = `;CN=` + quoteICalParam(ev.OrganizerCN)
		}
		b.WriteString("ORGANIZER" + cn + ":mailto:" + ev.Organizer + "\r\n")
	}
	for _, a := range ev.Attendees {
		email := a.Email
		if email == "" {
			continue
		}
		cn := ""
		if a.CN != "" {
			cn = `;CN=` + quoteICalParam(a.CN)
		}
		b.WriteString("ATTENDEE" + cn + ":mailto:" + email + "\r\n")
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

func eventETag(ev ICalEvent) string { return ev.ETag }

func parseICalTime(value string, params []string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	// Prefer explicit RFC3339 first so dashes cannot confuse the compact
	// iCalendar layouts below.
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), false
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), false
	}
	if t, err := time.Parse(icalTimeLayoutUTC, value); err == nil {
		return t.UTC(), false
	}
	if t, err := time.Parse(icalTimeLayoutDate, value); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(icalTimeLayoutLoc, value); err == nil {
		// Floating / TZID local — interpret as UTC so ordering stays stable.
		return t.UTC(), false
	}
	_ = params
	return time.Time{}, false
}

func parseICalDuration(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	sign := 1
	if strings.HasPrefix(value, "-") {
		sign = -1
		value = value[1:]
	} else if strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	if !strings.HasPrefix(strings.ToUpper(value), "P") {
		return 0, false
	}
	value = value[1:]
	var total time.Duration
	var num strings.Builder
	days := 0
	inTime := false
	flush := func(unit byte) {
		if num.Len() == 0 {
			return
		}
		n, err := strconv.Atoi(num.String())
		num.Reset()
		if err != nil {
			return
		}
		switch unit {
		case 'W':
			days += 7 * n
		case 'D':
			days += n
		case 'H':
			total += time.Duration(n) * time.Hour
		case 'M':
			if inTime {
				total += time.Duration(n) * time.Minute
			} else {
				// Months in durations are rare; approximate as 30 days.
				days += 30 * n
			}
		case 'S':
			total += time.Duration(n) * time.Second
		}
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch == 'T' || ch == 't':
			inTime = true
		case ch >= '0' && ch <= '9':
			num.WriteByte(ch)
		default:
			flush(ch)
		}
	}
	total += time.Duration(days) * 24 * time.Hour
	return time.Duration(sign) * total, true
}

func mailtoValue(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "mailto:")
	v = strings.TrimPrefix(v, "MAILTO:")
	return strings.TrimSpace(v)
}

func paramValue(params []string, key string) string {
	key = strings.ToUpper(key)
	for _, p := range params {
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), key) {
			val = strings.TrimSpace(val)
			val = strings.Trim(val, `"`)
			return unescapeVCard(val)
		}
	}
	return ""
}

func escapeICalText(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	v = strings.ReplaceAll(v, "\r", "")
	v = strings.ReplaceAll(v, ",", "\\,")
	v = strings.ReplaceAll(v, ";", "\\;")
	return v
}

func quoteICalParam(v string) string {
	if strings.ContainsAny(v, `",;:`) {
		return `"` + strings.ReplaceAll(v, `"`, `'`) + `"`
	}
	return v
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// expandRRule expands a limited RRULE subset within [windowStart, windowEnd).
// Supported: FREQ=DAILY|WEEKLY|MONTHLY|YEARLY, INTERVAL, COUNT, UNTIL, BYDAY
// (WEEKLY), WKST ignored. Unknown parts are ignored rather than failing.
func expandRRule(start time.Time, rrule string, exdates []time.Time, windowStart, windowEnd time.Time) []time.Time {
	if windowStart.IsZero() {
		windowStart = start
	}
	if windowEnd.IsZero() {
		windowEnd = start.AddDate(0, 3, 0)
	}
	// Expand slightly before the window so multi-day spans still overlap.
	floor := windowStart.Add(-90 * 24 * time.Hour)

	fields := map[string]string{}
	for _, part := range strings.Split(rrule, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		fields[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	freq := strings.ToUpper(fields["FREQ"])
	if freq == "" {
		freq = "DAILY"
	}
	interval := 1
	if s := fields["INTERVAL"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			interval = n
		}
	}
	count := 0
	if s := fields["COUNT"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			count = n
		}
	}
	var until time.Time
	if s := fields["UNTIL"]; s != "" {
		if t, ok := parseICalUntil(s); ok {
			until = t
		}
	}
	byday := fields["BYDAY"]

	exset := map[int64]bool{}
	for _, t := range exdates {
		exset[t.UTC().Unix()] = true
	}

	var out []time.Time
	capOccurrences := 500
	occurrence := 0
	t := start.UTC()
	// Normalize weekly BYDAY weekdays.
	weekdays := parseBYDay(byday)

	for i := 0; i < capOccurrences; i++ {
		if !until.IsZero() && t.After(until) {
			break
		}
		if !t.Before(floor) && t.Before(windowEnd) && !exset[t.Unix()] {
			if count == 0 || occurrence < count {
				// Count applies to generated occurrences overall.
				if count > 0 || occurrence >= 0 {
					out = append(out, t)
				}
			}
		}
		occurrence++
		if count > 0 && occurrence >= count {
			break
		}
		if !t.Before(windowEnd) && t.After(start) {
			// Past the window and not a multi-step reclaim — stop once we
			// have also collected in-window hits.
			if t.After(windowEnd) {
				break
			}
		}

		switch freq {
		case "DAILY":
			t = t.AddDate(0, 0, interval)
		case "WEEKLY":
			if len(weekdays) == 0 {
				t = t.AddDate(0, 0, 7*interval)
			} else {
				// Step day-by-day across the week; jump interval weeks at wrap.
				next := t.AddDate(0, 0, 1)
				if next.Weekday() == time.Sunday && t.Weekday() != time.Saturday {
					// wrapped
				}
				// Move to the next matching weekday inside the interval weeks.
				t = nextWeekdayOccurrence(t, weekdays, interval)
			}
		case "MONTHLY":
			t = t.AddDate(0, interval, 0)
		case "YEARLY":
			t = t.AddDate(interval, 0, 0)
		default:
			t = t.AddDate(0, 0, interval)
		}
	}
	// Sort + filter to the requested window (inclusive of start-spanning).
	var filtered []time.Time
	for _, occ := range out {
		if !occ.Before(windowStart) && occ.Before(windowEnd) {
			filtered = append(filtered, occ)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Before(filtered[j]) })
	// Deduplicate (weekly BYDAY can emit the same instant).
	var uniq []time.Time
	seen := map[int64]bool{}
	for _, occ := range filtered {
		k := occ.Unix()
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, occ)
	}
	return uniq
}

func parseICalUntil(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(icalTimeLayoutUTC, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(icalTimeLayoutDate, s); err == nil {
		return t.UTC().Add(24*time.Hour - time.Second), true
	}
	if t, err := time.Parse(icalTimeLayoutLoc, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func parseBYDay(s string) []time.Weekday {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	// Accept "MO", "MO,WE,FR", "1MO", "-1SU" (ordinal prefix ignored).
	names := map[string]time.Weekday{
		"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday,
		"WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday,
		"SA": time.Saturday,
	}
	var out []time.Weekday
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(strings.ToUpper(part))
		if p == "" {
			continue
		}
		// Strip optional ordinal prefix (e.g. 2FR, -1SU).
		for len(p) > 0 && (p[0] == '-' || (p[0] >= '0' && p[0] <= '9')) {
			p = p[1:]
		}
		if wd, ok := names[p]; ok {
			out = append(out, wd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func nextWeekdayOccurrence(from time.Time, weekdays []time.Weekday, intervalWeeks int) time.Time {
	// Search the next 28 days for the nearest target weekday.
	for i := 1; i <= 28; i++ {
		cand := from.AddDate(0, 0, i)
		for _, wd := range weekdays {
			if cand.Weekday() == wd {
				// Respect interval by counting whole weeks from the start weekday.
				if intervalWeeks <= 1 {
					return cand
				}
				weeks := i / 7
				if weeks%intervalWeeks == 0 || i%7 == 0 && (i/7)%intervalWeeks == 0 {
					return cand
				}
				// For interval>1, only emit when the week index matches.
				if i >= 7 && (i/7)%intervalWeeks == 0 {
					return cand
				}
				if i < 7 {
					// Within the first week always allow BYDAY hits.
					return cand
				}
			}
		}
	}
	return from.AddDate(0, 0, 7*intervalWeeks)
}

// CalendarWindow is the default list window for DAV calendar queries.
func CalendarWindow(now time.Time) (time.Time, time.Time) {
	return now.AddDate(0, 0, -30), now.AddDate(0, 0, 90)
}

func fmtICalUTC(t time.Time) string {
	return t.UTC().Format(icalTimeLayoutUTC)
}

var _ = fmt.Sprintf
