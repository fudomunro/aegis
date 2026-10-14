package types

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NormalizeMessageID strips angle brackets and lowercases an RFC Message-ID.
func NormalizeMessageID(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	return strings.ToLower(strings.TrimSpace(raw))
}

// NormalizePhotoTakenAt parses RFC3339 (or RFC3339Nano) and re-emits it at
// second precision in UTC so the same capture matches across services.
func NormalizePhotoTakenAt(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

// NewPhoto builds a single-source neutral photo. canonical should be true
// when this service holds the authoritative bytes (the usual case for photos).
func NewPhoto(service, serviceName, id, filename, mimeType, takenAt string, width, height int) Photo {
	src := PhotoSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return Photo{
		TakenAt:  takenAt,
		Width:    width,
		Height:   height,
		MimeType: mimeType,
		Filename: filename,
		Sources:  []PhotoSource{src},
	}
}

// NewPlaceVisit builds a single-source neutral visit.
func NewPlaceVisit(service, serviceName, id string, v PlaceVisit) PlaceVisit {
	out := ClonePlaceVisit(v)
	out.Kind = NormalizePlaceVisitKind(out.Kind)
	out.Mode = NormalizeTripMode(out.Mode)
	out.StartAt = NormalizePlaceVisitTime(out.StartAt)
	out.EndAt = NormalizePlaceVisitTime(out.EndAt)
	out.Sources = []PlaceVisitSource{{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}}
	return out
}

// PhotoSourceDiscipline is injected with photo guidance so agents do not fall
// back to local Photos libraries, Immich Docker/Postgres, or a direct :2283
// call when the prompt is vague (issue #125).
func PhotoSourceDiscipline() string {
	return "Photo source discipline: obtain photo metadata and bytes only through Aegis " +
		"(/v1/library/photos or /v1/services/...). Do not open Photos.app, " +
		"Photos Library.photoslibrary, Photo Booth, or ~/Pictures as a photo source; " +
		"do not query Immich at :2283 or via Docker/Postgres; do not read Immich .env " +
		"or compose files for credentials. Aegis owns photo credentials and the proxy URL."
}

// NormalizePlaceVisitKind maps provider labels onto visit/trip.
func NormalizePlaceVisitKind(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case PlaceKindVisit, "placevisit", "place_visit", "stay", "dwell", "stop":
		return PlaceKindVisit
	case PlaceKindTrip, "activitysegment", "activity_segment", "travel", "journey":
		return PlaceKindTrip
	case "":
		return PlaceKindVisit
	default:
		return PlaceKindVisit
	}
}

// NormalizePlaceVisitTime re-emits a provider timestamp at RFC3339 second
// precision so the same window matches across services.
func NormalizePlaceVisitTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02T15:04:05", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	// Unix seconds as decimal string.
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 1_000_000_000 && n < 4_000_000_000 {
		return time.Unix(n, 0).UTC().Format(time.RFC3339)
	}
	return raw
}

// NormalizeTripMode maps provider travel modes onto the neutral mode list.
func NormalizeTripMode(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	switch s {
	case "":
		return ""
	case "flight", "flying", "in_airplane", "airplane", "air", "plane":
		return TripModeFlight
	case "drive", "driving", "in_passenger_vehicle", "in_vehicle", "car", "vehicle", "motorcycle":
		return TripModeDrive
	case "walk", "walking", "on_foot", "foot":
		return TripModeWalk
	case "bicycle", "biking", "cycling", "on_bicycle":
		return TripModeBicycle
	case "train", "rail", "in_train":
		return TripModeTrain
	case "bus", "in_bus":
		return TripModeBus
	default:
		return TripModeUnknown
	}
}

func ClonePlaceVisit(v PlaceVisit) PlaceVisit {
	cp := v
	cp.Sources = append([]PlaceVisitSource(nil), v.Sources...)
	if len(v.Extra) > 0 {
		cp.Extra = make(map[string]any, len(v.Extra))
		for k, val := range v.Extra {
			cp.Extra[k] = val
		}
	}
	return cp
}

// Transaction status values (normalized lowercase).
// Transaction status values.
const (
	TransactionStatusPending = "pending"
	TransactionStatusPosted  = "posted"
)

// NewCalendarEvent builds a single-source neutral calendar event.
func NewCalendarEvent(service, serviceName, id, title, startAt, endAt string, allDay bool) CalendarEvent {
	src := CalendarEventSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}
	return CalendarEvent{
		Title:   title,
		StartAt: startAt,
		EndAt:   endAt,
		AllDay:  allDay,
		Sources: []CalendarEventSource{src},
	}
}

// FormatMilliunitsAmount converts YNAB-style milliunits (1/1000 of a currency
// unit) to a signed decimal string. 45670 → "45.67".
func FormatMilliunitsAmount(milliunits int64) string {
	sign := ""
	v := milliunits
	if v < 0 {
		sign = "-"
		v = -v
	}
	units := v / 1000
	frac := v % 1000
	// milliunits carry three digits; money is two. Round half-up at mills.
	cents := frac / 10
	if frac%10 >= 5 {
		cents++
	}
	if cents >= 100 {
		units++
		cents -= 100
	}
	return fmt.Sprintf("%s%d.%02d", sign, units, cents)
}

// NormalizeTransactionDate re-emits a provider date at date or RFC3339 UTC
// second precision so the same day matches across services.
func NormalizeTransactionDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Quicken/OFX compact dates: YYYYMMDD or YYYYMMDDHHMMSS.
	if len(raw) == 8 && raw[0] >= '1' && raw[0] <= '3' {
		if t, err := time.Parse("20060102", raw); err == nil {
			return t.UTC().Format("2006-01-02")
		}
	}
	if len(raw) == 14 {
		if t, err := time.Parse("20060102150405", raw); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse("01/02/2006", raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse("1/2/2006", raw); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return raw
}

// NewTransaction builds a single-source neutral transaction.
func NewTransaction(service, serviceName, id string, tx Transaction) Transaction {
	out := CloneTransaction(tx)
	out.Sources = []TransactionSource{{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		Canonical:   true,
	}}
	return out
}

func CloneTransaction(t Transaction) Transaction {
	cp := t
	cp.Sources = append([]TransactionSource(nil), t.Sources...)
	if t.Account != nil {
		acc := *t.Account
		cp.Account = &acc
	}
	if len(t.Category) > 0 {
		cp.Category = append([]string(nil), t.Category...)
	}
	if len(t.Extra) > 0 {
		cp.Extra = make(map[string]any, len(t.Extra))
		for k, v := range t.Extra {
			cp.Extra[k] = v
		}
	}
	return cp
}
