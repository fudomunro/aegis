package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultDataDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// Isolate from host env.
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AppData", "")
	t.Setenv("AEGIS_DATA_DIR", "")

	dir, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		want := filepath.Join(home, "Library", "Application Support", "Aegis")
		if dir != want {
			t.Fatalf("darwin data dir = %q, want %q", dir, want)
		}
	case "windows":
		want := filepath.Join(home, "AppData", "Roaming", "Aegis")
		if dir != want {
			t.Fatalf("windows data dir = %q, want %q", dir, want)
		}
	default:
		want := filepath.Join(home, ".local", "share", "aegis")
		if dir != want {
			t.Fatalf("linux data dir = %q, want %q", dir, want)
		}
	}
}

func TestDefaultDataDir_XDG(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("XDG_DATA_HOME applies to non-darwin/windows")
	}
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	t.Setenv("AEGIS_DATA_DIR", "")
	dir, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join("/tmp/xdg-data", "aegis") {
		t.Fatalf("dir = %q, want /tmp/xdg-data/aegis", dir)
	}
}

func TestResolveDataDir_Precedence(t *testing.T) {
	t.Setenv("AEGIS_DATA_DIR", "/tmp/from-env")
	t.Setenv("XDG_DATA_HOME", "")

	got, err := resolveDataDir("/tmp/from-flag")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/from-flag" {
		t.Fatalf("flag should win: got %q", got)
	}

	got, err = resolveDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/from-env" {
		t.Fatalf("env should apply when flag empty: got %q", got)
	}
}

func TestResolveDataDir_ExpandHome(t *testing.T) {
	t.Setenv("AEGIS_DATA_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveDataDir("~/aegis-data")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "aegis-data")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveStatePaths_DefaultsUnderDataDir(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if paths.Dir != dir {
		t.Fatalf("Dir = %q, want %q", paths.Dir, dir)
	}
	checks := map[string]string{
		"Config":      filepath.Join(dir, "aegis.json"),
		"Permissions": filepath.Join(dir, "permissions.json"),
		"Tokens":      filepath.Join(dir, "tokens.json"),
		"Links":       filepath.Join(dir, "entity_links.json"),
		"CopyLedger":  filepath.Join(dir, "copy_ledger.db"),
		"MailArchive": filepath.Join(dir, "mail-archive.db"),
		"LogFile":     filepath.Join(dir, "logs", "aegis.log"),
		"LogsDir":     filepath.Join(dir, "logs"),
	}
	// Reflect without getters: compare via struct fields explicitly.
	if paths.Config != checks["Config"] {
		t.Fatalf("Config = %q, want %q", paths.Config, checks["Config"])
	}
	if paths.Permissions != checks["Permissions"] {
		t.Fatalf("Permissions = %q, want %q", paths.Permissions, checks["Permissions"])
	}
	if paths.Tokens != checks["Tokens"] {
		t.Fatalf("Tokens = %q, want %q", paths.Tokens, checks["Tokens"])
	}
	if paths.Links != checks["Links"] {
		t.Fatalf("Links = %q, want %q", paths.Links, checks["Links"])
	}
	if paths.CopyLedger != checks["CopyLedger"] {
		t.Fatalf("CopyLedger = %q, want %q", paths.CopyLedger, checks["CopyLedger"])
	}
	if paths.MailArchive != checks["MailArchive"] {
		t.Fatalf("MailArchive = %q, want %q", paths.MailArchive, checks["MailArchive"])
	}
	if paths.LogFile != checks["LogFile"] {
		t.Fatalf("LogFile = %q, want %q", paths.LogFile, checks["LogFile"])
	}
	if paths.LogsDir != checks["LogsDir"] {
		t.Fatalf("LogsDir = %q, want %q", paths.LogsDir, checks["LogsDir"])
	}
}

func TestResolveStatePaths_ExplicitOverrides(t *testing.T) {
	dir := t.TempDir()
	ext := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:         dir,
		Config:      filepath.Join(ext, "custom.json"),
		Permissions: filepath.Join(ext, "perms.json"),
		Tokens:      filepath.Join(ext, "tok.json"),
		Links:       filepath.Join(ext, "links.json"),
		CopyLedger:  filepath.Join(ext, "copy.db"),
		// CopyLedgerSet true with non-empty path
		CopyLedgerSet: true,
		MailArchive:   filepath.Join(ext, "mail.db"),
		// MailArchiveSet true with non-empty path
		MailArchiveSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != filepath.Join(ext, "custom.json") {
		t.Fatalf("Config = %q", paths.Config)
	}
	if paths.Permissions != filepath.Join(ext, "perms.json") {
		t.Fatalf("Permissions = %q", paths.Permissions)
	}
	if paths.Tokens != filepath.Join(ext, "tok.json") {
		t.Fatalf("Tokens = %q", paths.Tokens)
	}
	if paths.Links != filepath.Join(ext, "links.json") {
		t.Fatalf("Links = %q", paths.Links)
	}
	if paths.CopyLedger != filepath.Join(ext, "copy.db") {
		t.Fatalf("CopyLedger = %q", paths.CopyLedger)
	}
	if paths.MailArchive != filepath.Join(ext, "mail.db") {
		t.Fatalf("MailArchive = %q", paths.MailArchive)
	}
	// Defaults still under data dir when not overridden.
	if paths.LogsDir != filepath.Join(dir, "logs") {
		t.Fatalf("LogsDir = %q", paths.LogsDir)
	}
}

func TestResolveStatePaths_MailArchiveExplicitEmptyDisables(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:            dir,
		MailArchive:    "  ",
		MailArchiveSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.MailArchive != "" {
		t.Fatalf("MailArchive = %q, want empty (disabled)", paths.MailArchive)
	}
}

func TestResolveStatePaths_CopyLedgerExplicitEmptyDisables(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:           dir,
		CopyLedger:    "  ",
		CopyLedgerSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.CopyLedger != "" {
		t.Fatalf("CopyLedger = %q, want empty (disabled)", paths.CopyLedger)
	}
}

func TestResolveStatePaths_LogFileExplicitEmptyDisables(t *testing.T) {
	dir := t.TempDir()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:        dir,
		LogFile:    "",
		LogFileSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paths.LogFile != "" {
		t.Fatalf("LogFile = %q, want empty (disabled)", paths.LogFile)
	}
}

func TestEnsureDataDir_CreatesPrivateTree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "Aegis")
	if err := EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "logs")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", p)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", p, info.Mode().Perm())
		}
	}
	// Response cache is in-memory only; no spill directory (issue #104).
	if _, err := os.Stat(filepath.Join(dir, "cache")); !os.IsNotExist(err) {
		t.Fatalf("cache/ should not be created, err = %v", err)
	}
}

func TestEnsureDataDir_Empty(t *testing.T) {
	if err := EnsureDataDir(""); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandUserPath("~/x.json"); got != filepath.Join(home, "x.json") {
		t.Fatalf("got %q", got)
	}
	if got := expandUserPath("~"); got != home {
		t.Fatalf("got %q", got)
	}
	if got := expandUserPath("/abs/path"); got != "/abs/path" {
		t.Fatalf("got %q", got)
	}
	if got := expandUserPath("  "); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestAdminSummary_IncludesDataDir(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.dataDir = "/tmp/aegis-data-test"

	req := httptest.NewRequest("GET", "/v1/admin/summary", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w := httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminSummary)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var payload AdminSummary
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Runtime.DataDir != "/tmp/aegis-data-test" {
		t.Fatalf("data_dir = %q", payload.Runtime.DataDir)
	}
}

func TestAdminUI_ShowsDataDir(t *testing.T) {
	data, err := os.ReadFile("web/admin.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "data_dir") {
		t.Fatal("admin UI should surface runtime.data_dir")
	}
}
