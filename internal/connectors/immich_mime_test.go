package connectors

import "testing"

func TestImmichMimeForType(t *testing.T) {
	cases := []struct {
		assetType, filename, want string
	}{
		{"", "photo.JPG", "image/jpeg"},
		{"", "a.jpeg", "image/jpeg"},
		{"", "a.png", "image/png"},
		{"", "a.heic", "image/heic"},
		{"", "a.HEIF", "image/heic"},
		{"", "a.webp", "image/webp"},
		{"", "a.gif", "image/gif"},
		{"", "clip.mp4", "video/mp4"},
		{"", "clip.mov", "video/quicktime"},
		{"VIDEO", "noext", "video/mp4"},
		{"IMAGE", "noext", "image/jpeg"},
		{"", "doc.pdf", "image/jpeg"},
	}
	for _, c := range cases {
		if got := ImmichMimeForType(c.assetType, c.filename); got != c.want {
			t.Fatalf("ImmichMimeForType(%q,%q)=%q want %q", c.assetType, c.filename, got, c.want)
		}
	}
}
