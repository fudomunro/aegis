package main

import (
	"testing"
	"time"
)

func TestStateStore_SetAndDelete(t *testing.T) {
	s := NewStateStore()
	s.Set("svc:abc", true, time.Minute)

	if !s.Delete("svc:abc") {
		t.Fatal("expected Delete to return true for a fresh key")
	}
	if s.Delete("svc:abc") {
		t.Fatal("expected Delete to return false after first consume")
	}
}

func TestStateStore_DeleteMissing(t *testing.T) {
	s := NewStateStore()
	if s.Delete("missing") {
		t.Fatal("expected Delete of missing key to be false")
	}
}

func TestStateStore_Expired(t *testing.T) {
	s := NewStateStore()
	s.Set("svc:old", true, 20*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	if s.Delete("svc:old") {
		t.Fatal("expected expired state to be rejected")
	}
}

func TestStateStore_PurgesExpiredOnSet(t *testing.T) {
	s := NewStateStore()
	s.Set("abandoned", true, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	s.Set("fresh", true, time.Minute)

	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1 (expired purged)", s.Len())
	}
	if !s.Delete("fresh") {
		t.Fatal("expected fresh key to delete cleanly")
	}
}
