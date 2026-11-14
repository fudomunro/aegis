package shared

import (
	"testing"
	"time"
)

func TestStateStore_SetAndDelete(t *testing.T) {
	s := NewStateStore()
	s.Set("svc:abc", "verifier", time.Minute)

	val, ok := s.Delete("svc:abc")
	if !ok {
		t.Fatal("expected Delete to return true for a fresh key")
	}
	if val != "verifier" {
		t.Fatalf("value = %q, want verifier", val)
	}
	if _, ok := s.Delete("svc:abc"); ok {
		t.Fatal("expected Delete to return false after first consume")
	}
}

func TestStateStore_DeleteMissing(t *testing.T) {
	s := NewStateStore()
	if _, ok := s.Delete("missing"); ok {
		t.Fatal("expected Delete of missing key to be false")
	}
}

func TestStateStore_Expired(t *testing.T) {
	s := NewStateStore()
	s.Set("svc:old", "v", 20*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	if _, ok := s.Delete("svc:old"); ok {
		t.Fatal("expected expired state to be rejected")
	}
}

func TestStateStore_PurgesExpiredOnSet(t *testing.T) {
	s := NewStateStore()
	s.Set("abandoned", "v", 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	s.Set("fresh", "v", time.Minute)

	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1 (expired purged)", s.Len())
	}
	if _, ok := s.Delete("fresh"); !ok {
		t.Fatal("expected fresh key to delete cleanly")
	}
}
