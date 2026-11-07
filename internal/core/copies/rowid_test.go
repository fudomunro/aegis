package copies

import (
	"testing"
)

func TestParseSyncRowID(t *testing.T) {
	if got := parseSyncRowID("cpy_a"); got != 10 {
		t.Fatalf("parseSyncRowID(cpy_a) = %d, want 10 (hex)", got)
	}
	if got := parseSyncRowID("cpy_2a"); got != 42 {
		t.Fatalf("parseSyncRowID(cpy_2a) = %d, want 42", got)
	}
	if got := parseSyncRowID("cpy_"); got != 0 {
		t.Fatalf("parseSyncRowID(cpy_) = %d, want 0", got)
	}
	if got := parseSyncRowID("nope"); got != 0 {
		t.Fatalf("parseSyncRowID(nope) = %d, want 0", got)
	}
	if got := parseSyncRowID(""); got != 0 {
		t.Fatalf("parseSyncRowID() = %d, want 0", got)
	}
}
