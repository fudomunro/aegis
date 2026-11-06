package types

import "testing"

func TestPhotoContentKey_MergesOnHashAndTime(t *testing.T) {
	a := NewPhoto("google-photos", "Google Photos", "a", "a.jpg", "image/jpeg", "2024-06-01T12:00:00Z", 100, 80)
	a.Extra = map[string]any{"content_hash": "ABC123"}
	b := NewPhoto("other", "Other", "b", "copy.jpg", "image/jpeg", "2024-06-01T12:00:00Z", 100, 80)
	b.Extra = map[string]any{"content_hash": "abc123"}

	if photoContentKey(a) != photoContentKey(b) {
		t.Fatalf("hash keys should match case-insensitively: %q vs %q", photoContentKey(a), photoContentKey(b))
	}

	c := NewPhoto("google-photos", "Google Photos", "c", "c.jpg", "image/jpeg", "2024-06-01T12:00:00Z", 100, 80)
	d := NewPhoto("other", "Other", "d", "d.jpg", "image/jpeg", "2024-06-01T12:00:00.500Z", 100, 80)
	if photoContentKey(c) != photoContentKey(d) {
		t.Fatalf("same second + dims should match: %q vs %q", photoContentKey(c), photoContentKey(d))
	}

	// Different dimensions must not merge even with the same timestamp.
	e := NewPhoto("google-photos", "Google Photos", "e", "e.jpg", "image/jpeg", "2024-06-01T12:00:00Z", 200, 80)
	if photoContentKey(c) == photoContentKey(e) {
		t.Fatal("different dimensions should not share a content key")
	}

	// Filename alone is never an identity.
	f := NewPhoto("google-photos", "Google Photos", "f", "IMG_0001.jpg", "image/jpeg", "", 0, 0)
	g := NewPhoto("google-photos", "Google Photos", "g", "IMG_0001.jpg", "image/jpeg", "", 0, 0)
	if PhotoIdentityKey(f) == PhotoIdentityKey(g) {
		t.Fatal("photos with no content key must stay distinct via service:id")
	}
}

func TestMergePhotos_CombinesSourcesAndKeepsCanonical(t *testing.T) {
	a := NewPhoto("google-photos", "Google Photos", "gp1", "beach.jpg", "image/jpeg", "2023-07-04T18:30:00Z", 4032, 3024)
	if len(a.Sources) != 1 || !a.Sources[0].Canonical {
		t.Fatalf("newPhoto should mark canonical source: %+v", a.Sources)
	}
	b := NewPhoto("icloud", "iCloud Photos", "ic1", "beach.jpg", "image/jpeg", "2023-07-04T18:30:00Z", 4032, 3024)
	b.Sources[0].Canonical = false

	merged := MergePhotos([]Photo{a, b})
	if len(merged) != 1 {
		t.Fatalf("got %d photos, want 1: %+v", len(merged), merged)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("want 2 sources, got %+v", merged[0].Sources)
	}
	if !merged[0].Sources[0].Canonical {
		t.Fatalf("canonical source should sort first: %+v", merged[0].Sources)
	}
}

func TestMergePhotos_Empty(t *testing.T) {
	if MergePhotos(nil) != nil {
		t.Fatal("MergePhotos(nil) should be nil")
	}
}

func TestMergePhotoPeople_UnionsSharedIdentity(t *testing.T) {
	a := Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"ada@example.com"},
		Sources: []ContactSource{{Service: "google", ServiceName: "Google Contacts", ID: "people/c1"}},
	}
	b := Contact{
		Name:   "Ada L.",
		Emails: []string{"ADA@example.com"},
		Phones: []string{"+15551234567"},
		Sources: []ContactSource{
			{Service: "google-photos", ServiceName: "Google Photos", ID: "tag1"},
		},
	}
	merged := mergePhotoPeople([]Contact{a}, []Contact{b})
	if len(merged) != 1 {
		t.Fatalf("want 1 person, got %+v", merged)
	}
	if merged[0].Name != "Ada Lovelace" {
		t.Fatalf("name = %q", merged[0].Name)
	}
	if len(merged[0].Emails) != 1 || merged[0].Emails[0] != "ada@example.com" {
		t.Fatalf("emails = %v", merged[0].Emails)
	}
	if len(merged[0].Phones) != 1 {
		t.Fatalf("phones = %v", merged[0].Phones)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}
}

func TestMergePhotoPeople_DistinctPeopleStaySeparate(t *testing.T) {
	a := Contact{Name: "Alex Kim", Emails: []string{"a@example.com"}}
	b := Contact{Name: "Alex Kim", Emails: []string{"b@example.com"}}
	merged := mergePhotoPeople([]Contact{a}, []Contact{b})
	if len(merged) != 2 {
		t.Fatalf("distinct emails must not merge by name: %+v", merged)
	}
}

func TestMergePhotos_PreservesPeopleAcrossSources(t *testing.T) {
	p1 := NewPhoto("google-photos", "Google Photos", "p1", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 10, 10)
	p1.People = []Contact{{Name: "Ada", Emails: []string{"ada@example.com"}}}
	p2 := NewPhoto("icloud", "iCloud Photos", "p2", "a.jpg", "image/jpeg", "2024-01-01T00:00:00Z", 10, 10)
	p2.People = []Contact{
		{Name: "Ada", Emails: []string{"ada@example.com"}, Phones: []string{"+15550001111"}},
		{Name: "Grace", Emails: []string{"grace@example.com"}},
	}

	merged := MergePhotos([]Photo{p1, p2})
	if len(merged) != 1 {
		t.Fatalf("want 1 photo, got %d", len(merged))
	}
	people := merged[0].People
	if len(people) != 2 {
		t.Fatalf("people = %+v", people)
	}
}
