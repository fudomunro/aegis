package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Default file names under the data directory (issue #74).
const (
	dataDirAppName         = "Aegis"
	dataDirName            = "aegis"
	defaultConfigName      = "aegis.json"
	defaultPermissionsName = "permissions.json"
	defaultTokensName      = "tokens.json"
	defaultLinksName       = "entity_links.json"
	defaultMailArchiveName = "mail-archive.db"
	defaultLogFileName     = "aegis.log"
	dataDirLogsSubdir      = "logs"
	dataDirCacheSubdir     = "cache"
)

// DataPaths is the resolved local state layout for one Aegis process.
// Explicit flag overrides are kept as-is; omitted names live under Dir.
type DataPaths struct {
	// Dir is the data directory (created 0700 on startup).
	Dir string
	// Config is the bootstrap rules file (aegis.json).
	Config string
	// Permissions is the admin permissions overlay (non-secret).
	Permissions string
	// Tokens is the encrypted OAuth token store.
	Tokens string
	// Links is the entity-link store (playlists).
	Links string
	// MailArchive is the SQLite+FTS mail archive path. Empty disables /v1/mail.
	MailArchive string
	// LogFile is the optional process log path. Empty logs to stderr only.
	LogFile string
	// LogsDir / CacheDir are reserved layout slots under Dir.
	LogsDir  string
	CacheDir string
}

// StatePathOverrides holds CLI/env values before defaults are applied.
// Empty strings mean "use the data-dir default", except MailArchiveSet,
// which distinguishes an omitted -mail-archive (default under data dir)
// from an explicitly empty one (disable /v1/mail).
type StatePathOverrides struct {
	Dir            string
	Config         string
	Permissions    string
	Tokens         string
	Links          string
	MailArchive    string
	MailArchiveSet bool
	LogFile        string
	LogFileSet     bool
}

// defaultDataDir returns the per-OS application data location for Aegis.
// Does not consult AEGIS_DATA_DIR or -data-dir; those are resolved first.
//
//	macOS:   ~/Library/Application Support/Aegis
//	Windows: %AppData%\Aegis (falls back to home)
//	other:   $XDG_DATA_HOME/aegis or ~/.local/share/aegis
func defaultDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if app := strings.TrimSpace(os.Getenv("AppData")); app != "" {
			return filepath.Join(app, dataDirAppName), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", dataDirAppName), nil
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", dataDirAppName), nil
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
			return filepath.Join(xdg, dataDirName), nil
		}
		return filepath.Join(home, ".local", "share", dataDirName), nil
	}
}

// expandUserPath expands a leading ~ or ~/ to the user's home directory.
func expandUserPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// resolveDataDir picks the data directory: flag, then AEGIS_DATA_DIR, then OS default.
func resolveDataDir(flagValue string) (string, error) {
	if v := expandUserPath(flagValue); v != "" {
		return filepath.Clean(v), nil
	}
	if v := expandUserPath(os.Getenv("AEGIS_DATA_DIR")); v != "" {
		return filepath.Clean(v), nil
	}
	dir, err := defaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(dir), nil
}

// resolveUnderDataDir returns explicit when set, otherwise name under dataDir.
func resolveUnderDataDir(explicit, dataDir, name string) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return expandUserPath(v)
	}
	return filepath.Join(dataDir, name)
}

// ResolveStatePaths turns CLI overrides into the concrete on-disk layout.
func ResolveStatePaths(ov StatePathOverrides) (DataPaths, error) {
	dir, err := resolveDataDir(ov.Dir)
	if err != nil {
		return DataPaths{}, err
	}
	paths := DataPaths{
		Dir:         dir,
		Config:      resolveUnderDataDir(ov.Config, dir, defaultConfigName),
		Permissions: resolveUnderDataDir(ov.Permissions, dir, defaultPermissionsName),
		Tokens:      resolveUnderDataDir(ov.Tokens, dir, defaultTokensName),
		Links:       resolveUnderDataDir(ov.Links, dir, defaultLinksName),
		LogsDir:     filepath.Join(dir, dataDirLogsSubdir),
		CacheDir:    filepath.Join(dir, dataDirCacheSubdir),
	}
	// Mail archive: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.MailArchiveSet:
		paths.MailArchive = filepath.Join(dir, defaultMailArchiveName)
	case strings.TrimSpace(ov.MailArchive) == "":
		paths.MailArchive = ""
	default:
		paths.MailArchive = expandUserPath(ov.MailArchive)
	}
	// Log file: omitted → logs/aegis.log under data dir; explicitly empty → stderr only.
	switch {
	case !ov.LogFileSet:
		paths.LogFile = filepath.Join(paths.LogsDir, defaultLogFileName)
	case strings.TrimSpace(ov.LogFile) == "":
		paths.LogFile = ""
	default:
		paths.LogFile = expandUserPath(ov.LogFile)
	}
	return paths, nil
}

// EnsureDataDir creates the data directory tree with 0700 (secrets live here).
// Existing directories are tightened to 0700 when possible; a failed chmod is
// logged but does not abort (network volumes, managed home dirs).
func EnsureDataDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("data dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating data dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "aegis: could not set data dir mode 0700 on %s: %v\n", dir, err)
	}
	for _, sub := range []string{dataDirLogsSubdir, dataDirCacheSubdir} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("creating data subdir %s: %w", p, err)
		}
		_ = os.Chmod(p, 0o700)
	}
	return nil
}

// EnsureDataPaths creates the data directory tree for the resolved layout.
func EnsureDataPaths(paths DataPaths) error {
	return EnsureDataDir(paths.Dir)
}
