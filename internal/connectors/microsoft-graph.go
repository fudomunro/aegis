package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/shared"
)

// Microsoft Graph shared plumbing (issue #145): paging envelope, bearer
// requests with a UTC time preference, and time mapping. Microsoft calendar
// and contacts surfaces share one top-level "microsoft" connection and one
// Graph endpoint.

// MicrosoftGraphDefaultBase is the Graph v1.0 base used when config does not
// override the endpoint.
const MicrosoftGraphDefaultBase = "https://graph.microsoft.com/v1.0"

// graphList is the Graph collection envelope: `value` plus `@odata.nextLink`
// for paging and `@odata.deltaLink` for delta queries (issue #145).
type graphList[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// graphRemoved reports the `@removed` marker Graph attaches to deleted
// records on a delta feed.
func graphRemoved(raw map[string]json.RawMessage) bool {
	v, ok := raw["@removed"]
	if !ok {
		return false
	}
	var rm struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(v, &rm); err != nil {
		return true
	}
	return rm.Reason == "" || rm.Reason == "deleted" || rm.Reason == "changed"
}

// decodeGraphDelta decodes one delta-feed page: the changed records (the
// @removed deletions are dropped — convergence places and never deletes),
// the paging link while the feed still has pages, and the deltaLink to
// record as the resume token once the walk is complete.
func decodeGraphDelta[T any](data []byte) (records []T, nextLink, deltaLink string, err error) {
	var page struct {
		Value     []json.RawMessage `json:"value"`
		NextLink  string            `json:"@odata.nextLink"`
		DeltaLink string            `json:"@odata.deltaLink"`
	}
	if err = json.Unmarshal(data, &page); err != nil {
		return nil, "", "", err
	}
	for _, raw := range page.Value {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		if graphRemoved(probe) {
			continue
		}
		var rec T
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, page.NextLink, page.DeltaLink, nil
}

// microsoftGraphURL joins the configured Graph base with a path/query.
func microsoftGraphURL(base, path string) string {
	return shared.TrimRightSlash(base) + path
}

// microsoftGraphRequest performs one authenticated Graph call. Body may be
// nil. The Prefer header pins event times to UTC so dateTime values arrive
// zoneless-UTC instead of Windows time-zone names.
func microsoftGraphRequest(ctx context.Context, token, method, url string, body any) ([]byte, int, error) {
	return microsoftGraphRequestIfMatch(ctx, token, method, url, "", body)
}

// microsoftGraphRequestIfMatch is microsoftGraphRequest with optimistic
// concurrency: a non-empty etag is sent as If-Match on the write.
func microsoftGraphRequestIfMatch(ctx context.Context, token, method, url, etag string, body any) ([]byte, int, error) {
	data, _, status, err := microsoftGraphDo(ctx, token, method, url, etag, body, 1<<20)
	return data, status, err
}

// microsoftGraphDo is the one Graph request pipeline every surface rides:
// bearer + UTC Prefer + optional If-Match, capped response read. Headers are
// returned so callers can honor throttle hints; limit is the body cap (mail
// bodies are wider than the default).
func microsoftGraphDo(ctx context.Context, token, method, url, etag string, body any, limit int64) ([]byte, http.Header, int, error) {
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, nil, 0, err
		}
		payload = data
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", `outlook.timezone="UTC"`)
	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-Match", etag)
	}
	resp, err := shared.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	data, err := readAllLimit(resp.Body, limit)
	if err != nil {
		return nil, resp.Header, resp.StatusCode, err
	}
	return data, resp.Header, resp.StatusCode, nil
}

// graphWriteError renders a failed Graph write response body for skip reports.
func graphWriteError(status int, data []byte) error {
	return fmt.Errorf("microsoft graph returned %d: %s", status, truncateBytes(data, 200))
}

// microsoftTime is Graph's dateTime + timeZone pair.
type microsoftTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// microsoftInstantToRFC3339 maps a Graph instant string to RFC3339 UTC. With
// the UTC Prefer header Graph sends zoneless "2006-01-02T15:04:05.0000000";
// offset, whole-second, and date-only forms are accepted too. All-day-ness is
// the event's own isAllDay flag, not a property of the time.
func microsoftInstantToRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return raw
}

// microsoftTimeToRFC3339 maps a Graph dateTime/timeZone pair to RFC3339 UTC.
func microsoftTimeToRFC3339(t microsoftTime) string {
	return microsoftInstantToRFC3339(t.DateTime)
}

// rfc3339ToMicrosoftTime formats a neutral RFC3339 timestamp as a Graph
// dateTime in UTC.
func rfc3339ToMicrosoftTime(rfc string) microsoftTime {
	raw := strings.TrimSpace(rfc)
	if raw == "" {
		return microsoftTime{TimeZone: "UTC"}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return microsoftTime{
			DateTime: parsed.UTC().Format("2006-01-02T15:04:05"),
			TimeZone: "UTC",
		}
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return microsoftTime{
			DateTime: parsed.UTC().Format("2006-01-02T00:00:00"),
			TimeZone: "UTC",
		}
	}
	return microsoftTime{DateTime: raw, TimeZone: "UTC"}
}

// microsoftEventStatus maps Graph's showAs/isCancelled onto the neutral
// status vocabulary (confirmed, tentative, cancelled).
func microsoftEventStatus(showAs string, isCancelled bool) string {
	if isCancelled {
		return "cancelled"
	}
	switch strings.ToLower(strings.TrimSpace(showAs)) {
	case "tentative":
		return "tentative"
	default:
		return "confirmed"
	}
}

// graphStatusToMicrosoft maps the neutral status back onto Graph's
// isCancelled/showAs pair for placement.
func graphStatusToMicrosoft(status string) (isCancelled bool, showAs string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "cancelled":
		return true, "busy"
	case "tentative":
		return false, "tentative"
	default:
		return false, "busy"
	}
}
