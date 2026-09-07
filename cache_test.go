package main

import (
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
