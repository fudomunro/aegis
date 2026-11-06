package shared

import "testing"

func TestHaversineMeters(t *testing.T) {
	// ~111m per 0.001° latitude.
	d := HaversineMeters(44.0, -63.0, 44.001, -63.0)
	if d < 100 || d > 130 {
		t.Fatalf("haversine = %f", d)
	}
}

func TestIso8601DurationMs(t *testing.T) {
	if got := Iso8601DurationMs("PT3M42S"); got != 222000 {
		t.Fatalf("PT3M42S = %d", got)
	}
	if got := Iso8601DurationMs("PT1H2M3S"); got != 3723000 {
		t.Fatalf("PT1H2M3S = %d", got)
	}
	if got := Iso8601DurationMs("bogus"); got != 0 {
		t.Fatalf("bogus = %d", got)
	}
}

func TestTrimRightSlash(t *testing.T) {
	if got := TrimRightSlash("https://example.com/"); got != "https://example.com" {
		t.Fatalf("got = %q", got)
	}
	if got := TrimRightSlash("https://example.com"); got != "https://example.com" {
		t.Fatalf("got = %q", got)
	}
	if got := TrimRightSlash(""); got != "" {
		t.Fatalf("got = %q", got)
	}
}
