package types

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func TestParsePageRequestDefaults(t *testing.T) {
	req, err := ParsePageRequest("", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit != DefaultPageLimit || req.Offset != 0 {
		t.Fatalf("req = %+v", req)
	}
}

func TestParsePageRequestClampsLimit(t *testing.T) {
	req, err := ParsePageRequest("0", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit != DefaultPageLimit {
		t.Fatalf("limit 0 => %d", req.Limit)
	}
	req, err = ParsePageRequest("9999", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit != MaxPageLimit {
		t.Fatalf("limit 9999 => %d", req.Limit)
	}
	req, err = ParsePageRequest("25", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Limit != 25 {
		t.Fatalf("limit 25 => %d", req.Limit)
	}
}

func TestParsePageRequestInvalid(t *testing.T) {
	if _, err := ParsePageRequest("abc", ""); err == nil {
		t.Fatal("want error for non-numeric limit")
	}
	if _, err := ParsePageRequest("", "!!!not-base64!!!"); err == nil {
		t.Fatal("want error for invalid cursor")
	}
	neg := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(-5)))
	if _, err := ParsePageRequest("", neg); err == nil {
		t.Fatal("want error for negative cursor offset")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	for _, off := range []int{0, 1, 99, 1000} {
		got, err := DecodeCursor(EncodeCursor(off))
		if err != nil {
			t.Fatalf("off %d: %v", off, err)
		}
		if got != off {
			t.Fatalf("off %d => %d", off, got)
		}
	}
	if _, err := DecodeCursor("hello"); err == nil {
		t.Fatal("want invalid cursor error")
	}
}

func TestSlicePageWalksFullSet(t *testing.T) {
	items := make([]int, 250)
	for i := range items {
		items[i] = i
	}
	req := PageRequest{Limit: 100}
	seen := map[int]bool{}
	pages := 0
	for {
		page, pr := SlicePage(items, req)
		if pr.Total != 250 {
			t.Fatalf("total = %d", pr.Total)
		}
		for _, v := range page {
			if seen[v] {
				t.Fatalf("duplicate %d", v)
			}
			seen[v] = true
		}
		pages++
		if pr.NextCursor == "" {
			break
		}
		off, err := DecodeCursor(pr.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		req = PageRequest{Limit: 100, Offset: off}
		if pages > 10 {
			t.Fatal("too many pages")
		}
	}
	if len(seen) != 250 {
		t.Fatalf("saw %d items, want 250", len(seen))
	}
	if pages != 3 {
		t.Fatalf("pages = %d, want 3", pages)
	}
}

func TestSlicePageEmptyAndOverflow(t *testing.T) {
	page, pr := SlicePage([]int{}, PageRequest{Limit: 10})
	if len(page) != 0 || pr.Total != 0 || pr.NextCursor != "" {
		t.Fatalf("empty => page=%v pr=%+v", page, pr)
	}
	page, pr = SlicePage([]int{1, 2, 3}, PageRequest{Limit: 10, Offset: 99})
	if len(page) != 0 || pr.Total != 3 || pr.NextCursor != "" {
		t.Fatalf("overflow => page=%v pr=%+v", page, pr)
	}
	page, pr = SlicePage([]int{1, 2, 3}, PageRequest{Limit: 2, Offset: 2})
	if len(page) != 1 || page[0] != 3 || pr.NextCursor != "" {
		t.Fatalf("last page => page=%v pr=%+v", page, pr)
	}
}

func TestPageRequestCacheSuffix(t *testing.T) {
	if s := (PageRequest{Limit: DefaultPageLimit}).CacheSuffix(); s != "" {
		t.Fatalf("default suffix = %q", s)
	}
	s := (PageRequest{Limit: 50, Offset: 10}).CacheSuffix()
	if !strings.Contains(s, "50") || !strings.Contains(s, "10") {
		t.Fatalf("suffix = %q", s)
	}
}

func TestMetaForPage(t *testing.T) {
	m := MetaForPage(PageResult{Total: 7, NextCursor: "c"}, true, true, nil)
	if m.Total != 7 || !m.Synced || !m.Truncated || m.NextCursor != "c" {
		t.Fatalf("meta = %+v", m)
	}
}
