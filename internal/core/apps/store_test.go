package apps

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func createTestApp(t *testing.T, store *Store, id string, scopes shared.GrantMap) (view AppView, token string) {
	t.Helper()
	view, token, err := store.Create(AppCreateRequest{ID: id, Name: id, Scopes: scopes})
	if err != nil {
		t.Fatalf("Create(%s): %v", id, err)
	}
	return view, token
}

func TestStore_CreateReturnsTokenOnce(t *testing.T) {
	store := testStore(t)
	view, token, err := store.Create(AppCreateRequest{
		ID:     "eco",
		Name:   "Event Coordinator",
		Scopes: shared.GrantMap{"freebusy": shared.AccessRead},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("token prefix = %q", token)
	}
	if view.TokenHint == "" || view.TokenHint == token {
		t.Fatalf("hint should be short and non-secret: %q", view.TokenHint)
	}
	if strings.Contains(view.TokenHint, token[len(TokenPrefix):]) {
		t.Fatalf("hint too long: %q", view.TokenHint)
	}
	// List/get never return plaintext.
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list[0].TokenHint != view.TokenHint {
		t.Fatalf("list hint mismatch")
	}
	got, ok := store.Get("eco")
	if !ok || got.Scopes["freebusy"] != shared.AccessRead {
		t.Fatalf("get = %+v ok=%v", got, ok)
	}
	// Scopes default to deny-all when not listed.
	if got.Scopes[shared.DefaultKey] != shared.AccessNone {
		t.Fatalf("default scope = %q, want none", got.Scopes[shared.DefaultKey])
	}
}

func TestStore_FindByTokenAndRotate(t *testing.T) {
	store := testStore(t)
	_, token := createTestApp(t, store, "eco", shared.GrantMap{"events": shared.AccessRead})
	if store.FindByToken(token) == nil {
		t.Fatal("expected token to resolve")
	}
	if store.FindByToken(TokenPrefix+"nope") != nil {
		t.Fatal("unknown token should not resolve")
	}

	_, newToken, err := store.Rotate("eco")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if store.FindByToken(token) != nil {
		t.Fatal("old token should be invalid after rotate")
	}
	if store.FindByToken(newToken) == nil {
		t.Fatal("new token should resolve")
	}
}

func TestStore_DeleteAndDisable(t *testing.T) {
	store := testStore(t)
	_, token := createTestApp(t, store, "eco", shared.GrantMap{"events": shared.AccessRead})
	disabled := true
	if _, err := store.Update("eco", AppUpdateRequest{Disabled: &disabled}); err != nil {
		t.Fatalf("Update disabled: %v", err)
	}
	if store.FindByToken(token) != nil {
		t.Fatal("disabled app must not authenticate")
	}
	if err := store.Delete("eco"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := store.Get("eco"); ok {
		t.Fatal("app should be gone")
	}
}

func TestStore_CreateRejectsBadIDAndScopes(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.Create(AppCreateRequest{ID: "bad id", Scopes: shared.GrantMap{"events": shared.AccessRead}}); err == nil {
		t.Fatal("expected bad id rejected")
	}
	if _, _, err := store.Create(AppCreateRequest{ID: "ok", Scopes: shared.GrantMap{"tracks": "freebusy"}}); err == nil {
		t.Fatal("expected freebusy on tracks rejected")
	}
	if _, _, err := store.Create(AppCreateRequest{ID: "ok", Scopes: shared.GrantMap{"events": "write-only-oops"}}); err == nil {
		t.Fatal("expected unknown level rejected")
	}
}
