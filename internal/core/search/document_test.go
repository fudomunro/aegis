package search

import (
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

func TestBuildSearchDocument_FlattensDeclaredFields(t *testing.T) {
	item := map[string]any{
		"name":   "Apartment lease 2024",
		"tags":   []string{"housing", "legal"},
		"people": []map[string]any{{"name": "Dana Landlord", "id": "p1", "sources": []any{}}},
		"sources": []map[string]any{
			{"service": "paperless", "id": "1"},
			{"service": "drive", "id": "9"},
		},
	}
	spec := types.SearchSpec{Fields: []string{"name", "tags", "people"}, TitleField: "name"}
	doc, err := BuildSearchDocument("lease-2024", item, spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if doc.ItemID != "lease-2024" {
		t.Fatalf("item id = %q", doc.ItemID)
	}
	if doc.Title != "Apartment lease 2024" {
		t.Fatalf("title = %q", doc.Title)
	}
	for _, want := range []string{"Apartment lease 2024", "housing legal", "Dana Landlord"} {
		if !strings.Contains(doc.Body, want) {
			t.Fatalf("body %q missing %q", doc.Body, want)
		}
	}
	// Identity/transport keys never enter the text, so service ids cannot
	// masquerade as content matches.
	if strings.Contains(doc.Body, "paperless") || strings.Contains(doc.Body, "p1") {
		t.Fatalf("body leaked identity keys: %q", doc.Body)
	}
	if len(doc.Services) != 2 || doc.Services[0] != "paperless" || doc.Services[1] != "drive" {
		t.Fatalf("services = %v", doc.Services)
	}
	if len(doc.Payload) == 0 {
		t.Fatal("payload must carry the neutral item JSON")
	}
}

func TestBuildSearchDocument_TitleDefaultsToFirstField(t *testing.T) {
	item := map[string]any{"subject": "Quarterly taxes", "snippet": "due april"}
	doc, err := BuildSearchDocument("m1", item, types.SearchSpec{Fields: []string{"subject", "snippet"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if doc.Title != "Quarterly taxes" {
		t.Fatalf("title = %q", doc.Title)
	}
}

func TestBuildSearchDocument_UserDefinedFieldsMap(t *testing.T) {
	// User-defined items keep schema values under "fields".
	item := map[string]any{
		"name": "The Left Hand of Darkness",
		"fields": map[string]any{
			"author": "Ursula K. Le Guin",
			"status": "reading",
		},
	}
	spec := types.SearchSpec{Fields: []string{"name", "author", "status"}, TitleField: "name"}
	doc, err := BuildSearchDocument("b1", item, spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{"The Left Hand of Darkness", "Ursula K. Le Guin", "reading"} {
		if !strings.Contains(doc.Body, want) {
			t.Fatalf("body %q missing %q", doc.Body, want)
		}
	}
}

func TestBuildSearchDocument_NestedSkipKeys(t *testing.T) {
	item := map[string]any{
		"name": "Sunset",
		"place": map[string]any{
			"name":     "Gasworks Park",
			"address":  "2101 N Northlake Way",
			"locality": "Seattle",
			"lat":      47.645,
			"lng":      -122.334,
			"id":       "place-9",
		},
	}
	doc, err := BuildSearchDocument("v1", item, types.SearchSpec{Fields: []string{"name", "place"}, TitleField: "place"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{"Gasworks Park", "Seattle"} {
		if !strings.Contains(doc.Body, want) {
			t.Fatalf("body %q missing %q", doc.Body, want)
		}
	}
	if strings.Contains(doc.Body, "47.645") || strings.Contains(doc.Body, "place-9") {
		t.Fatalf("body leaked skip keys: %q", doc.Body)
	}
}

func TestSearchSourceServices_Dedups(t *testing.T) {
	services := searchSourceServices([]any{
		map[string]any{"service": "spotify"},
		map[string]any{"service": "spotify"},
		map[string]any{"service": "youtube-music"},
		map[string]any{"id": "no-service"},
	})
	if len(services) != 2 || services[0] != "spotify" || services[1] != "youtube-music" {
		t.Fatalf("services = %v", services)
	}
}
