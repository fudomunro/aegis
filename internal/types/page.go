package types

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Shared agent-facing pagination for GET /v1/library/{kind} (issue #185).
// Every built-in and user-defined kind uses the same request/response contract:
//
//	request:  limit (default 100, max 500), cursor (opaque)
//	response: next_cursor (omitted at end of the filtered set)
//
// total is the full filtered count across the merge; truncated stays about
// upstream page-cap incompleteness, not the agent page window.
const (
	DefaultPageLimit = 100
	MaxPageLimit     = 500
)

// LibraryListMeta is the shared status/paging envelope embedded in every
// library list response. Per-kind item arrays stay kind-named (contacts,
// tracks, …); these fields are identical everywhere (issue #185).
type LibraryListMeta struct {
	// Total is the full filtered item count for the request (not the page).
	Total int `json:"total"`
	// Synced is true when multi-source merge ran for this kind.
	Synced bool `json:"synced,omitempty"`
	// Truncated is true when any service hit its upstream page cap with more
	// items still available. Independent of next_cursor paging.
	Truncated bool `json:"truncated,omitempty"`
	// NextCursor is the opaque cursor for the next page; omitted at the end.
	NextCursor string `json:"next_cursor,omitempty"`
	// ByService is set on aggregate responses only.
	ByService map[string]LibraryServiceStatus `json:"by_service,omitempty"`
}

// PageRequest is the agent-facing page window for a library list.
type PageRequest struct {
	// Limit is the max items in this page (1..MaxPageLimit).
	Limit int
	// Offset is the decoded cursor position into the stable-sorted list.
	Offset int
}

// PageResult describes the slice SlicePage returned.
type PageResult struct {
	Limit      int
	Offset     int
	Total      int
	NextCursor string
}

// ParsePageRequest reads limit/cursor query values. Empty limit uses
// DefaultPageLimit; out-of-range limits clamp to [1, MaxPageLimit]. An empty
// cursor starts at offset 0. Invalid cursor or non-numeric limit is an error
// so handlers can return 400 (client error — not a fail-open fetch miss).
func ParsePageRequest(limitRaw, cursorRaw string) (PageRequest, error) {
	req := PageRequest{Limit: DefaultPageLimit}
	limitRaw = strings.TrimSpace(limitRaw)
	if limitRaw != "" {
		n, err := strconv.Atoi(limitRaw)
		if err != nil {
			return PageRequest{}, fmt.Errorf("invalid limit: use an integer 1-%d", MaxPageLimit)
		}
		req.Limit = clampPageLimit(n)
	}
	cursorRaw = strings.TrimSpace(cursorRaw)
	if cursorRaw != "" {
		off, err := DecodeCursor(cursorRaw)
		if err != nil {
			return PageRequest{}, err
		}
		req.Offset = off
	}
	return req, nil
}

func clampPageLimit(n int) int {
	if n < 1 {
		return DefaultPageLimit
	}
	if n > MaxPageLimit {
		return MaxPageLimit
	}
	return n
}

// CacheSuffix distinguishes pages in an aggregate cache key. The default
// page (limit=DefaultPageLimit, offset=0) keeps the unprefixed kind key so
// unpaginated callers share one entry.
func (p PageRequest) CacheSuffix() string {
	if p.Limit == DefaultPageLimit && p.Offset == 0 {
		return ""
	}
	return fmt.Sprintf("|page:%d:%d", p.Limit, p.Offset)
}

// EncodeCursor wraps a stable offset as an opaque client token.
func EncodeCursor(offset int) string {
	if offset < 0 {
		offset = 0
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// DecodeCursor reverses EncodeCursor. Any non-integer payload is invalid.
func DecodeCursor(s string) (int, error) {
	raw := strings.TrimSpace(s)
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid cursor")
		}
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return n, nil
}

// SlicePage returns the stable-offset window for items plus paging metadata.
// items must already be filtered and in a stable sort order so cursors do not
// skip or duplicate across pages.
func SlicePage[T any](items []T, req PageRequest) ([]T, PageResult) {
	total := len(items)
	limit := clampPageLimit(req.Limit)
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := items[offset:end]
	if page == nil {
		page = []T{}
	}
	res := PageResult{Limit: limit, Offset: offset, Total: total}
	if end < total {
		res.NextCursor = EncodeCursor(end)
	}
	return page, res
}

// MetaForPage builds the shared envelope for a paged slice. synced is the
// kind's multi-source merge flag; truncated is the upstream page-cap flag.
func MetaForPage(pr PageResult, synced, truncated bool, bySvc map[string]LibraryServiceStatus) LibraryListMeta {
	return LibraryListMeta{
		Total:      pr.Total,
		Synced:     synced,
		Truncated:  truncated,
		NextCursor: pr.NextCursor,
		ByService:  bySvc,
	}
}
