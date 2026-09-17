package main

import (
	"fmt"
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache()

	c.Set("key1", "value1", 5*time.Minute)

	got, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != "value1" {
		t.Fatalf("got %q, want %q", got, "value1")
	}
}

func TestCache_Miss(t *testing.T) {
	c := NewCache()

	_, ok := c.Get("missing")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestCache_Expiration(t *testing.T) {
	c := NewCache()

	c.Set("short", "data", 50*time.Millisecond)

	got, ok := c.Get("short")
	if !ok || got != "data" {
		t.Fatal("expected hit before expiry")
	}

	time.Sleep(60 * time.Millisecond)

	_, ok = c.Get("short")
	if ok {
		t.Fatal("expected miss after expiry")
	}
}

func TestCache_Overwrite(t *testing.T) {
	c := NewCache()

	c.Set("k", "old", 5*time.Minute)
	c.Set("k", "new", 5*time.Minute)

	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit")
	}
	if got != "new" {
		t.Fatalf("got %q, want %q", got, "new")
	}
}

func TestCache_Concurrent(t *testing.T) {
	c := NewCache()

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			c.Set("concurrent", "val", time.Minute)
			c.Get("concurrent")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestCache_PurgesExpiredOnSet(t *testing.T) {
	c := NewCache()
	c.Set("stale", "old", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	c.Set("fresh", "new", time.Minute)

	live, total, _ := c.Stats()
	if live != 1 || total != 1 {
		t.Fatalf("stats = live %d total %d, want 1/1 (expired purged)", live, total)
	}
}

func TestCache_EntryCapEvictsSoonest(t *testing.T) {
	c := NewCache()
	// Fill beyond the entry cap; later keys get longer TTLs so earliest expire first.
	for i := 0; i < cacheMaxEntries+8; i++ {
		key := fmt.Sprintf("k-%03d", i)
		c.Set(key, "v", time.Duration(i+1)*time.Minute)
	}
	_, total, _ := c.Stats()
	if total > cacheMaxEntries {
		t.Fatalf("total entries = %d, want ≤ %d", total, cacheMaxEntries)
	}
}

func TestCache_ByteCapEvicts(t *testing.T) {
	c := NewCache()
	big := make([]byte, 1<<20) // 1 MiB
	for i := range big {
		big[i] = 'x'
	}
	payload := string(big)
	for i := 0; i < 40; i++ {
		c.Set(fmt.Sprintf("p-%02d", i), payload, time.Minute)
	}
	_, _, bytes := c.Stats()
	if bytes > cacheMaxBytes {
		t.Fatalf("live bytes = %d, want ≤ %d", bytes, cacheMaxBytes)
	}
}

func TestCache_OversizeValueNotStored(t *testing.T) {
	c := NewCache()
	huge := make([]byte, cacheMaxBytes+1)
	c.Set("huge", string(huge), time.Minute)
	if _, ok := c.Get("huge"); ok {
		t.Fatal("oversize value should not be cached")
	}
}
