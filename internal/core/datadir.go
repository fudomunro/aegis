package core

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
	defaultDataGrantsName   = "data_grants.json"
	defaultTokensName      = "tokens.json"
	defaultAppsName        = "apps.json"
	defaultLinksName       = "entity_links.json"
	defaultCopyLedgerName  = "copy_ledger.db"
	defaultMailArchiveName = "mail-archive.db"
	defaultQuickenName     = "quicken.db"
	defaultTimelineName    = "timeline.db"
	defaultSignalName      = "signal.db"
	defaultAuditLogName    = "audit_log.db"
	defaultLogFileName     = "aegis.log"
	dataDirLogsSubdir      = "logs"
)

// DataPaths is the resolved local state layout for one Aegis process.
// Explicit flag overrides are kept as-is; omitted names live under Dir.
type DataPaths struct {
	// Dir is the data directory (created 0700 on startup).
	Dir string
	// Config is the bootstrap rules file (aegis.json).
	Config string
	// DataGrants is the admin data-grant overlay (data_grants.json, non-secret).
	DataGrants string
	// Tokens is the encrypted OAuth token store.
	Tokens string
	// Apps is the third-party application registry (issue #131).
	Apps string
	// Links is the entity-link store (playlists).
	Links string
	// CopyLedger is the SQLite copy-ledger path (issue #76). Empty disables it.
	CopyLedger string
	// MailArchive is the SQLite+FTS mail archive path. Empty disables /v1/mail.
	MailArchive string
	// Quicken is the SQLite Quicken import store path (issue #141).
	// Empty disables the local Quicken source.
	Quicken string
	// Timeline is the SQLite Google Timeline import store path (issue #142).
	// Empty disables the local Timeline source.
	Timeline string
	// Signal is the SQLite Signal import store path (issue #168).
	// Empty disables the local Signal source.
	Signal string
	// AuditLog is the SQLite access-audit path (issue #105). Empty disables it.
	AuditLog string
	// LogFile is the optional process log path. Empty logs to stderr only.
	LogFile string
	// LogsDir is the process log directory under Dir.
	LogsDir string
}

// StatePathOverrides holds CLI/env values before defaults are applied.
// Empty strings mean "use the data-dir default", except MailArchiveSet,
// which distinguishes an omitted -mail-archive (default under data dir)
// from an explicitly empty one (disable /v1/mail).
type StatePathOverrides struct {
	Dir            string
	Config         string
	DataGrants     string
	Tokens         string
	Links          string
	CopyLedger     string
	CopyLedgerSet  bool
	MailArchive    string
	MailArchiveSet bool
	Quicken        string
	QuickenSet     bool
	Timeline       string
	TimelineSet    bool
	Signal         string
	SignalSet      bool
	AuditLog       string
	AuditLogSet    bool
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
		DataGrants:  resolveUnderDataDir(ov.DataGrants, dir, defaultDataGrantsName),
		Tokens:      resolveUnderDataDir(ov.Tokens, dir, defaultTokensName),
		Apps:        filepath.Join(dir, defaultAppsName),
		Links:       resolveUnderDataDir(ov.Links, dir, defaultLinksName),
		LogsDir:     filepath.Join(dir, dataDirLogsSubdir),
	}
	// Copy ledger: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.CopyLedgerSet:
		paths.CopyLedger = filepath.Join(dir, defaultCopyLedgerName)
	case strings.TrimSpace(ov.CopyLedger) == "":
		paths.CopyLedger = ""
	default:
		paths.CopyLedger = expandUserPath(ov.CopyLedger)
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
	// Quicken import store: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.QuickenSet:
		paths.Quicken = filepath.Join(dir, defaultQuickenName)
	case strings.TrimSpace(ov.Quicken) == "":
		paths.Quicken = ""
	default:
		paths.Quicken = expandUserPath(ov.Quicken)
	}
	// Timeline import store: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.TimelineSet:
		paths.Timeline = filepath.Join(dir, defaultTimelineName)
	case strings.TrimSpace(ov.Timeline) == "":
		paths.Timeline = ""
	default:
		paths.Timeline = expandUserPath(ov.Timeline)
	}
	// Signal import store: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.SignalSet:
		paths.Signal = filepath.Join(dir, defaultSignalName)
	case strings.TrimSpace(ov.Signal) == "":
		paths.Signal = ""
	default:
		paths.Signal = expandUserPath(ov.Signal)
	}
	// Audit log: omitted → default under data dir; explicitly empty → disabled.
	switch {
	case !ov.AuditLogSet:
		paths.AuditLog = filepath.Join(dir, defaultAuditLogName)
	case strings.TrimSpace(ov.AuditLog) == "":
		paths.AuditLog = ""
	default:
		paths.AuditLog = expandUserPath(ov.AuditLog)
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
	logsDir := filepath.Join(dir, dataDirLogsSubdir)
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return fmt.Errorf("creating data subdir %s: %w", logsDir, err)
	}
	_ = os.Chmod(logsDir, 0o700)
	return nil
}

// EnsureDataPaths creates the data directory tree for the resolved layout.
func EnsureDataPaths(paths DataPaths) error {
	return EnsureDataDir(paths.Dir)
}
