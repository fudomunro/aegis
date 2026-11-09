package types

import (
	"testing"
)

func TestKindForPath_QueryAndSlash(t *testing.T) {
	if KindForPath("/me/tracks?limit=1") != "tracks" {
		t.Fatal("query strip")
	}
	if KindForPath("   ") != "" {
		t.Fatal("blank")
	}
	if KindForPath("/me/../secret") != "" {
		t.Fatal("dotdot must be non-canonical")
	}
}
