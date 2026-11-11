package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/core/tokens"
)

func TestRunTokenRekeyCLI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	ts, err := tokens.NewStoreWithIterations(path, "old-pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save("spotify", &tokens.OAuthToken{
		AccessToken: "cli-token",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Wrong current passphrase must fail and leave the file alone.
	if code := TokenRekey(path, "wrong", "new-pass"); code == 0 {
		t.Fatal("rekey with wrong current passphrase should fail")
	}
	if _, err := tokens.NewStoreWithIterations(path, "old-pass", 1_000); err != nil {
		t.Fatalf("token file must survive failed rekey: %v", err)
	}

	if code := TokenRekey(path, "old-pass", "new-pass"); code != 0 {
		t.Fatal("rekey should succeed")
	}
	if _, err := tokens.NewStoreWithIterations(path, "old-pass", 1_000); err == nil {
		t.Fatal("old passphrase must stop working")
	}
	reloaded, err := tokens.NewStoreWithIterations(path, "new-pass", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("spotify"); got == nil || got.AccessToken != "cli-token" {
		t.Fatalf("token after CLI rekey = %+v", got)
	}

	if code := TokenRekey("", "old", "new"); code == 0 {
		t.Fatal("missing path should fail")
	}
	if code := TokenRekey(path, "", "new"); code == 0 {
		t.Fatal("missing current passphrase should fail")
	}
	if code := TokenRekey(path, "new-pass", "  "); code == 0 {
		t.Fatal("blank new passphrase should fail")
	}
}
