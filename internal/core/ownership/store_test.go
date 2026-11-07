package ownership

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipStore_ConfirmReopenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	entry, err := store.Confirm("messages", "work mail only")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !entry.Confirmed || entry.ConfirmedAt == "" || entry.Note != "work mail only" {
		t.Fatalf("entry = %+v, want confirmed with note and timestamp", entry)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get("messages")
	if !got.Confirmed || got.Note != "work mail only" {
		t.Fatalf("reloaded = %+v, want confirmed entry", got)
	}

	// Reopen keeps the note but clears the confirmation.
	reopened, err := reloaded.Reopen("messages")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Confirmed || reopened.ConfirmedAt != "" {
		t.Fatalf("reopened = %+v, want unconfirmed", reopened)
	}
	if reopened.Note != "work mail only" {
		t.Fatalf("reopened note = %q, want kept note", reopened.Note)
	}

	again, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload after reopen: %v", err)
	}
	if got := again.Get("messages"); got.Confirmed {
		t.Fatalf("persisted reopen still confirmed: %+v", got)
	}
}

func TestOwnershipStore_EmptyPathStaysInMemory(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.Confirm("photos", ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !store.Get("photos").Confirmed {
		t.Fatal("expected in-memory confirmation")
	}
}

func TestOwnershipStore_RejectsBadInput(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "ownership.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.Confirm("  ", ""); err == nil {
		t.Fatal("expected error for empty kind")
	}
	path := filepath.Join(t.TempDir(), "ownership.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"ownership":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("expected unsupported-version error")
	}
}
