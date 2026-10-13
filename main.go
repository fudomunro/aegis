package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	baseURL := flag.String("base-url", "", "public base URL of this Aegis instance (reads AEGIS_BASE_URL env if empty)")
	apiKey := flag.String("api-key", "", "API key for authenticating requests (reads AEGIS_API_KEY env if empty; optional in tailnet/auto modes)")
	authMode := flag.String("auth-mode", "", "auth mode: auto, tailnet, or api-key (reads AEGIS_AUTH_MODE; default auto)")
	allowedLogins := flag.String("allowed-logins", "", "comma-separated Tailscale logins allowed to call the API (reads AEGIS_ALLOWED_LOGINS; empty allows any tailnet identity)")
	tsSocket := flag.String("ts-socket", "", "path to tailscaled LocalAPI socket for WhoIs (default: auto-detect /var/run/tailscale/tailscaled.sock)")
	dataDirFlag := flag.String("data-dir", "", "data directory for local state (reads AEGIS_DATA_DIR if empty; default: OS application data dir for Aegis)")
	configPath := flag.String("config", "", "path to rules configuration file (default: <data-dir>/aegis.json)")
	tokenPath := flag.String("token-file", "", "path to encrypted token storage (default: <data-dir>/tokens.json)")
	linkPath := flag.String("link-file", "", "path to user-confirmed entity links (default: <data-dir>/entity_links.json)")
	copyLedgerPath := flag.String("copy-ledger", "", "path to SQLite copy ledger (default: <data-dir>/copy_ledger.db; empty disables)")
	permissionsPath := flag.String("permissions-file", "", "path to admin permissions overlay (default: <data-dir>/permissions.json; admin saves write here instead of aegis.json)")
	mailArchivePath := flag.String("mail-archive", "", "path to local compressed mail archive (default: <data-dir>/mail-archive.db; empty disables /v1/mail)")
	quickenPath := flag.String("quicken-store", "", "path to local Quicken import store (default: <data-dir>/quicken.db; empty disables)")
	importQuicken := flag.String("import-quicken", "", "Quicken QIF/QFX file or directory to import into the local store, then exit (issue #141)")
	auditLogPath := flag.String("audit-log", "", "path to durable access audit log (default: <data-dir>/audit_log.db; empty disables)")
	auditRetentionDays := flag.Int("audit-retention-days", defaultAuditRetentionDays, "how many days of audit events to retain before pruning")
	importTakeout := flag.String("import-takeout", "", "read-only Google Takeout path (mbox/zip/dir) to sync into the mail archive, then exit")
	importTakeoutBatch := flag.Int("import-takeout-batch", 20000, "max messages per import-takeout call (repeat runs continue)")
	seedSample := flag.Bool("seed-sample", false, "write synthetic sample mail + library fixtures under the data directory, then exit (issue #77)")
	seedSampleCount := flag.Int("seed-sample-count", SampleMailCount, "mail messages to generate with -seed-sample (default small CI set)")
	seedSampleStress := flag.Bool("seed-sample-stress", false, "with -seed-sample, generate the large stress set (10k mail rows) for local perf")
	tokenPass := flag.String("token-pass", "", "passphrase for token encryption (reads AEGIS_TOKEN_PASS env if empty)")
	tokenNewPass := flag.String("token-new-pass", "", "if set, re-encrypt the token file under this passphrase (with current KDF parameters) and exit")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "json", "log format: json or text")
	logFile := flag.String("log-file", "", "optional process log file (default: <data-dir>/logs/aegis.log; empty disables file logging)")
	apertureURL := flag.String("aperture-url", "", "public base URL of the Aperture gateway this Aegis hooks into (reads AEGIS_APERTURE_URL env if empty; used for admin live links)")
	flag.Parse()

	setFlags := visitedFlags()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:            *dataDirFlag,
		Config:         *configPath,
		Permissions:    *permissionsPath,
		Tokens:         *tokenPath,
		Links:          *linkPath,
		CopyLedger:     *copyLedgerPath,
		CopyLedgerSet:  setFlags["copy-ledger"],
		MailArchive:    *mailArchivePath,
		MailArchiveSet: setFlags["mail-archive"],
		Quicken:        *quickenPath,
		QuickenSet:     setFlags["quicken-store"],
		AuditLog:       *auditLogPath,
		AuditLogSet:    setFlags["audit-log"],
		LogFile:        *logFile,
		LogFileSet:     setFlags["log-file"],
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "aegis: resolving data directory: %v\n", err)
		os.Exit(1)
	}
	if err := EnsureDataPaths(paths); err != nil {
		fmt.Fprintf(os.Stderr, "aegis: %v\n", err)
		os.Exit(1)
	}

	logOut := io.Writer(os.Stderr)
	if paths.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(paths.LogFile), 0o700); err == nil {
			f, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err == nil {
				defer f.Close()
				logOut = io.MultiWriter(os.Stderr, f)
			} else {
				fmt.Fprintf(os.Stderr, "aegis: cannot open log file %s: %v\n", paths.LogFile, err)
			}
		}
	}

	logger := initLogger(*logLevel, *logFormat, logOut)
	slog.SetDefault(logger)
	slog.Info("data directory ready",
		"path", paths.Dir,
		"config", paths.Config,
		"tokens", paths.Tokens,
		"permissions", paths.Permissions,
		"links", paths.Links,
		"copy_ledger", paths.CopyLedger,
		"mail_archive", paths.MailArchive,
		"quicken_store", paths.Quicken,
		"audit_log", paths.AuditLog,
		"log_file", paths.LogFile,
	)

	// One-shot sample seeding, Takeout ingest, and token rekey do not need
	// OAuth, base URL, or a listening server.
	if *seedSample {
		code := runSeedSampleCLI(paths.Dir, paths.MailArchive, *seedSampleCount, *seedSampleStress)
		os.Exit(code)
	}
	if strings.TrimSpace(*importTakeout) != "" {
		code := runImportTakeoutCLI(paths.MailArchive, paths.CopyLedger, *importTakeout, *importTakeoutBatch)
		os.Exit(code)
	}
	if strings.TrimSpace(*importQuicken) != "" {
		code := runImportQuickenCLI(paths.Quicken, *importQuicken)
		os.Exit(code)
	}
	if strings.TrimSpace(*tokenNewPass) != "" {
		if *tokenPass == "" {
			*tokenPass = os.Getenv("AEGIS_TOKEN_PASS")
		}
		code := runTokenRekeyCLI(paths.Tokens, *tokenPass, *tokenNewPass)
		os.Exit(code)
	}

	if *apiKey == "" {
		*apiKey = os.Getenv("AEGIS_API_KEY")
	}
	if *authMode == "" {
		*authMode = os.Getenv("AEGIS_AUTH_MODE")
	}
	if *authMode == "" {
		*authMode = AuthModeAuto
	}
	switch *authMode {
	case AuthModeAuto, AuthModeTailnet, AuthModeAPIKey:
	default:
		slog.Error("invalid auth-mode", "value", *authMode, "allowed", "auto, tailnet, api-key")
		os.Exit(1)
	}

	if *allowedLogins == "" {
		*allowedLogins = os.Getenv("AEGIS_ALLOWED_LOGINS")
	}

	if *authMode == AuthModeAPIKey && *apiKey == "" {
		slog.Error("api-key is required in api-key auth mode (flag or AEGIS_API_KEY env)")
		os.Exit(1)
	}
	if *authMode == AuthModeAuto && *apiKey == "" {
		slog.Warn("no API key configured — only tailnet identity will be accepted")
	}

	if *baseURL == "" {
		*baseURL = os.Getenv("AEGIS_BASE_URL")
	}
	if *baseURL == "" {
		slog.Error("base-url is required (flag or AEGIS_BASE_URL env)")
		os.Exit(1)
	}

	if *tokenPass == "" {
		*tokenPass = os.Getenv("AEGIS_TOKEN_PASS")
	}
	if *apertureURL == "" {
		*apertureURL = os.Getenv("AEGIS_APERTURE_URL")
	}
	*apertureURL = strings.TrimRight(strings.TrimSpace(*apertureURL), "/")
	if *tokenPass == "" {
		slog.Error("token-pass is required (flag or AEGIS_TOKEN_PASS env)")
		os.Exit(1)
	}

	cfg, err := LoadConfig(paths.Config)
	if err != nil {
		slog.Error("failed to load config", "path", paths.Config, "error", err)
		os.Exit(1)
	}
	if err := cfg.InitPermissionsStore(paths.Permissions); err != nil {
		slog.Error("failed to load permissions overlay", "path", paths.Permissions, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", paths.Config, "services", cfg.ServiceCount(), "permissions_file", paths.Permissions)

	tokens, err := NewTokenStore(paths.Tokens, *tokenPass)
	if err != nil {
		slog.Error("failed to load token store", "path", paths.Tokens, "error", err)
		os.Exit(1)
	}

	links, err := NewEntityLinkStore(paths.Links)
	if err != nil {
		slog.Error("failed to load entity link store", "path", paths.Links, "error", err)
		os.Exit(1)
	}

	apps, err := NewAppStore(paths.Apps)
	if err != nil {
		slog.Error("failed to load app registry", "path", paths.Apps, "error", err)
		os.Exit(1)
	}

	copies, err := OpenCopyLedger(paths.CopyLedger)
	if err != nil {
		slog.Error("failed to open copy ledger", "path", paths.CopyLedger, "error", err)
		os.Exit(1)
	}
	if copies != nil {
		defer copies.Close()
		slog.Info("copy ledger ready", "path", copies.Path(), "enabled", copies.Enabled())
	}

	mailArchive, err := OpenMailArchive(paths.MailArchive)
	if err != nil {
		slog.Error("failed to open mail archive", "path", paths.MailArchive, "error", err)
		os.Exit(1)
	}
	if mailArchive != nil {
		defer mailArchive.Close()
		slog.Info("mail archive ready", "path", mailArchive.Path())
	} else {
		slog.Info("mail archive disabled")
	}

	quickenStore, err := OpenQuickenStore(paths.Quicken)
	if err != nil {
		slog.Error("failed to open quicken store", "path", paths.Quicken, "error", err)
		os.Exit(1)
	}
	if quickenStore != nil {
		defer quickenStore.Close()
		slog.Info("quicken store ready", "path", quickenStore.Path())
		// File-based import source: not an operator-facing service card.
		// Inject an in-memory entry so /v1/library/transactions can fan in.
		cfg.EnsureService(Service{
			ID:          quickenServiceID,
			Name:        quickenServiceName,
			Action:      "inject",
			Description: "Locally imported Quicken QIF/QFX files (Advanced → Quicken import).",
			Permissions: Permissions{"transactions": AccessRead},
		})
	} else {
		slog.Info("quicken store disabled")
	}

	auditLog, err := OpenAuditLog(paths.AuditLog, *auditRetentionDays)
	if err != nil {
		slog.Error("failed to open audit log", "path", paths.AuditLog, "error", err)
		os.Exit(1)
	}
	if auditLog != nil {
		defer auditLog.Close()
		slog.Info("audit log ready", "path", auditLog.Path(), "enabled", auditLog.Enabled(),
			"retention_days", *auditRetentionDays)
	}

	identity := buildIdentityResolver(*authMode, *tsSocket)

	srv := &Server{
		apiKey:        *apiKey,
		authMode:      *authMode,
		allowedLogins: parseLoginList(*allowedLogins),
		identity:      identity,
		baseURL:       *baseURL,
		dataDir:       paths.Dir,
		config:        cfg,
		cache:         NewCache(),
		tokens:        tokens,
		oauthStates:   NewStateStore(),
		connectors:    make(map[string]ServiceConnector),
		metrics:       NewAccessMetrics(defaultMetricsCapacity),
		links:         links,
		apps:          apps,
		grants:        NewGrantStore(),
		copies:        copies,
		mail:          mailArchive,
		quicken:       quickenStore,
		hookHealth:    NewHookHealth(defaultHookObservationCapacity),
		audit:         auditLog,
		prom:          NewPromMetrics(),
		apertureURL:   *apertureURL,
	}

	// Register service connectors.
	spotifyConn := NewSpotifyConnector(srv)
	srv.connectors["spotify"] = spotifyConn
	ytMusicConn := NewYouTubeMusicConnector(srv)
	srv.connectors["youtube-music"] = ytMusicConn
	amzMusicConn := NewAmazonMusicConnector(srv)
	srv.connectors["amazon-music"] = amzMusicConn
	gPhotosConn := NewGooglePhotosConnector(srv)
	srv.connectors[googlePhotosServiceID] = gPhotosConn
	gContactsConn := NewGoogleContactsConnector(srv)
	srv.connectors[googleContactsServiceID] = gContactsConn
	immichConn := NewImmichConnector(srv)
	srv.connectors[immichServiceID] = immichConn
	gCalendarConn := NewGoogleCalendarConnector(srv)
	srv.connectors[googleCalendarServiceID] = gCalendarConn
	gGmailConn := NewGoogleGmailConnector(srv)
	srv.connectors[googleGmailServiceID] = gGmailConn
	// Financial transactions (issue #141): YNAB live API + local Quicken import.
	srv.connectors[ynabServiceID] = NewYNABConnector(srv)
	srv.connectors[quickenServiceID] = NewQuickenConnector(srv)
	// Synthetic calendar fixtures for demos / query testing (issue #77, #131).
	// A service with endpoint sample://calendar gets this connector instead of
	// a live provider so free/busy demos work without OAuth.
	for _, svc := range cfg.ServiceSnapshots() {
		if isSampleCalendarService(&svc) {
			srv.connectors[svc.ID] = NewSampleCalendarConnector()
		}
	}
	srv.connectors[sampleCalendarServiceID] = NewSampleCalendarConnector()

	mux := http.NewServeMux()
	registerBrandRoutes(mux, srv)
	// Observability (issue #105). Health probes are unauthenticated so
	// supervisors can poll cheaply; /metrics is auth-scoped like admin
	// because series include service ids and denial reasons.
	mux.HandleFunc("GET /healthz", srv.HandleHealthz)
	mux.HandleFunc("HEAD /healthz", srv.HandleHealthz)
	mux.HandleFunc("GET /readyz", srv.HandleReadyz)
	mux.HandleFunc("HEAD /readyz", srv.HandleReadyz)
	mux.HandleFunc("GET /metrics", srv.AuthMiddleware(srv.HandleMetrics))
	mux.HandleFunc("HEAD /metrics", srv.AuthMiddleware(srv.HandleMetrics))
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest)))
	// Third-party grant demo (issue #131). Consent GET + demo page are public
	// like OAuth authorize; grant POST and token exchange have their own rules.
	mux.HandleFunc("GET /demo/third-party", srv.HandleThirdPartyDemo)
	mux.HandleFunc("HEAD /demo/third-party", srv.HandleThirdPartyDemo)
	mux.HandleFunc("GET /v1/apps/authorize", srv.HandleAppAuthorize)
	mux.HandleFunc("POST /v1/apps/authorize", srv.AuthMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAppAuthorize)))
	mux.HandleFunc("POST /v1/apps/token", withBodyLimit(maxAdminBodyBytes, srv.HandleAppToken))
	mux.HandleFunc("POST /v1/apps/revoke", withBodyLimit(maxAdminBodyBytes, srv.HandleAppRevoke))
	for path, handler := range srv.libraryRouteHandlers() {
		mux.HandleFunc(path, srv.AuthMiddleware(handler))
	}
	mux.HandleFunc("POST /v1/library/mirror", srv.AuthMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleLibraryMirror)))
	mux.HandleFunc("/v1/mail/", srv.AuthMiddleware(withBodyLimit(maxMailBodyBytes, srv.handleMailRoutes)))
	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(withBodyLimit(maxProxyBodyBytes, srv.HandleServiceProxy)))
	mux.HandleFunc("/v1/auth/status", srv.AuthMiddleware(srv.HandleAuthStatus))
	mux.HandleFunc("/v1/auth/", srv.handleAuthRoutes)
	// Admin shell and JSON both require auth (issue #98): the SPA is markup
	// only, but unauthenticated peers should not learn the API surface.
	mux.HandleFunc("/v1/admin", srv.AuthMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAdmin)))
	mux.HandleFunc("/v1/admin/", srv.AuthMiddleware(withBodyLimit(maxAdminBodyBytes, srv.handleAdminRoutes)))

	handler := securityHeadersMiddleware(mux)

	slog.Info("starting server",
		"addr", *addr,
		"base_url", *baseURL,
		"auth_mode", *authMode,
		"api_key_configured", *apiKey != "",
		"allowed_logins", len(srv.allowedLogins),
		"data_dir", paths.Dir,
	)
	if err := runHTTPServer(*addr, handler, cfg); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// buildIdentityResolver wires Serve headers always, plus WhoIs when a
// tailscaled socket is available (or explicitly configured).
func buildIdentityResolver(authMode, tsSocket string) IdentityResolver {
	if authMode == AuthModeAPIKey {
		return nil
	}
	var resolvers []IdentityResolver
	resolvers = append(resolvers, ServeHeaderResolver{})

	who := WhoIsResolver{SocketPath: tsSocket}
	sock := tsSocket
	if sock == "" {
		sock = defaultTSocketPath()
	}
	if sock != "" {
		resolvers = append(resolvers, who)
		slog.Info("tailscaled WhoIs enabled", "socket", sock)
	} else if authMode == AuthModeTailnet {
		slog.Warn("tailscaled socket not found — tailnet identity will only work via Tailscale Serve headers")
	}
	return MultiResolver{Resolvers: resolvers}
}

// handleAdminRoutes dispatches /v1/admin. Registered under AuthMiddleware
// (issue #98): the HTML shell is markup-only with no secrets, but unauthenticated
// peers must not learn the API surface. JSON data endpoints keep normal Aegis auth.
func (s *Server) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/")
	path = strings.Trim(path, "/")

	switch {
	case path == "":
		s.HandleAdmin(w, r)
	case path == "summary":
		s.AuthMiddleware(s.HandleAdminSummary)(w, r)
	case path == "flow":
		s.AuthMiddleware(s.HandleAdminFlow)(w, r)
	case path == "cache":
		s.AuthMiddleware(s.HandleAdminCache)(w, r)
	case path == "cache/clear":
		s.AuthMiddleware(s.HandleAdminCacheClear)(w, r)
	case path == "hook-health":
		s.AuthMiddleware(s.HandleAdminHookHealth)(w, r)
	case path == "mail/status":
		s.AuthMiddleware(s.HandleAdminMailStatus)(w, r)
	case path == "mail/sync":
		s.AuthMiddleware(s.HandleAdminMailSync)(w, r)
	case path == "mail/import":
		s.AuthMiddleware(s.HandleAdminMailImport)(w, r)
	case strings.HasPrefix(path, "library/"):
		s.AuthMiddleware(s.HandleAdminLibrary)(w, r)
	case strings.HasPrefix(path, "services/") && strings.HasSuffix(path, "/permissions"):
		s.AuthMiddleware(s.HandleAdminServicePermissions)(w, r)
	case path == "apps" || strings.HasPrefix(path, "apps/"):
		s.AuthMiddleware(s.HandleAdminApps)(w, r)
	case path == "immich" || strings.HasPrefix(path, "immich/"):
		s.AuthMiddleware(s.HandleAdminImmichSetup)(w, r)
	case path == "ynab" || strings.HasPrefix(path, "ynab/"):
		s.AuthMiddleware(s.HandleAdminYNABSetup)(w, r)
	case path == "quicken" || strings.HasPrefix(path, "quicken/"):
		s.AuthMiddleware(s.HandleAdminQuicken)(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

// runSeedSampleCLI writes synthetic sample fixtures under the data directory
// and loads the mail set into the configured archive, then exits.
// Data is fictional; safe to regenerate. See README "Sample data".
func runSeedSampleCLI(dataDir, archivePath string, count int, stress bool) int {
	count = seedSampleCountOrDefault(count, stress)
	sampleDir := filepath.Join(dataDir, "sample")
	mboxPath, libraryPath, _, err := WriteSampleFiles(sampleDir, count)
	if err != nil {
		slog.Error("failed to write sample files", "dir", sampleDir, "error", err)
		return 1
	}
	set, err := SeedSampleArchive(archivePath, count)
	if err != nil {
		slog.Error("failed to seed mail archive", "path", archivePath, "error", err)
		return 1
	}
	slog.Info("sample data ready (all synthetic)",
		"mail_messages", len(set.Records),
		"oldest_id", set.OldestID,
		"newest_id", set.NewestID,
		"mail_archive", archivePath,
		"mbox", mboxPath,
		"library_json", libraryPath,
	)
	return 0
}

func seedSampleCountOrDefault(count int, stress bool) int {
	if stress {
		if count > 0 && count != SampleMailCount {
			return count
		}
		return SampleMailStressCount
	}
	if count <= 0 {
		return SampleMailCount
	}
	return count
}

// runImportTakeoutCLI syncs a read-only Takeout export into the mail archive
// and exits. Used for large local exports that should not go through HTTP.
func runImportTakeoutCLI(archivePath, copyLedgerPath, takeoutPath string, batch int) int {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		slog.Error("mail-archive path is required for -import-takeout")
		return 1
	}
	a, err := OpenMailArchive(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if a == nil {
		slog.Error("mail archive disabled — set -mail-archive")
		return 1
	}
	defer a.Close()
	copies, err := OpenCopyLedger(copyLedgerPath)
	if err != nil {
		slog.Error("failed to open copy ledger", "path", copyLedgerPath, "error", err)
		return 1
	}
	if copies != nil {
		defer copies.Close()
	}
	if err := a.SetTakeoutPath(takeoutPath); err != nil {
		slog.Error("failed to record takeout path", "path", takeoutPath, "error", err)
		return 1
	}
	if batch <= 0 {
		batch = 20000
	}
	var stored, skipped, scanned, failed int
	done := false
	for {
		res, err := a.ImportTakeout(takeoutPath, batch)
		stored += res.Stored
		skipped += res.Skipped
		scanned += res.Scanned
		failed += res.Failed
		slog.Info("takeout import batch",
			"path", takeoutPath,
			"scanned", res.Scanned,
			"stored", res.Stored,
			"skipped", res.Skipped,
			"failed", res.Failed,
			"done", res.Done,
			"error", res.Error,
		)
		if err != nil && res.Error != "" && res.Stored == 0 && res.Scanned == 0 {
			return 1
		}
		if res.Done {
			done = true
			break
		}
		if !res.Truncated {
			break
		}
	}
	copies.RecordTakeoutSnapshot(takeoutPath, scanned, stored, skipped, done)
	slog.Info("takeout import finished",
		"path", takeoutPath,
		"scanned", scanned,
		"stored", stored,
		"skipped", skipped,
		"failed", failed,
	)
	return 0
}

// runImportQuickenCLI imports a QIF/QFX file or directory into the local
// Quicken store, then exits (issue #141).
func runImportQuickenCLI(storePath, importPath string) int {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		slog.Error("quicken store path is required for -import-quicken")
		return 1
	}
	store, err := OpenQuickenStore(storePath)
	if err != nil {
		slog.Error("failed to open quicken store", "path", storePath, "error", err)
		return 1
	}
	if store == nil {
		slog.Error("quicken store disabled — set -quicken-store")
		return 1
	}
	defer store.Close()
	res, err := ImportQuickenPath(store, importPath)
	if err != nil {
		slog.Error("quicken import failed", "path", importPath, "error", err)
		return 1
	}
	slog.Info("quicken import finished",
		"path", importPath,
		"scanned", res.Scanned,
		"stored", res.Stored,
		"skipped", res.Skipped,
		"errors", res.Errors,
		"done", res.Done,
	)
	if res.Errors > 0 && res.Stored == 0 {
		return 1
	}
	return 0
}

// runTokenRekeyCLI re-encrypts the token store under a new passphrase with
// current KDF parameters, then exits (issue #101). Passing the same passphrase
// still rotates the master salt and raises PBKDF2 cost on legacy files.
func runTokenRekeyCLI(path, currentPass, newPass string) int {
	path = strings.TrimSpace(path)
	if path == "" {
		slog.Error("token file path is required for -token-new-pass")
		return 1
	}
	if currentPass == "" {
		slog.Error("token-pass (current) is required for -token-new-pass")
		return 1
	}
	if strings.TrimSpace(newPass) == "" {
		slog.Error("token-new-pass must not be empty")
		return 1
	}
	ts, err := NewTokenStore(path, currentPass)
	if err != nil {
		slog.Error("failed to load token store for rekey", "path", path, "error", err)
		return 1
	}
	if err := ts.ChangePassphrase(newPass); err != nil {
		slog.Error("failed to rekey token store", "path", path, "error", err)
		return 1
	}
	slog.Info("token store rekeyed",
		"path", path,
		"services", len(ts.Tokens),
		"iterations", ts.kdfIterations,
	)
	return 0
}

// handleAuthRoutes dispatches /v1/auth/* to the correct handler.
// Login and callback are public (no auth) to support OAuth redirects.
// Status and revoke require auth.
func (s *Server) handleAuthRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/auth/")

	switch {
	case strings.HasSuffix(path, "/login"):
		s.HandleAuthLogin(w, r)
	case strings.HasSuffix(path, "/callback"):
		s.HandleAuthCallback(w, r)
	case path == "status":
		s.AuthMiddleware(s.HandleAuthStatus)(w, r)
	default:
		if r.Method == http.MethodDelete {
			s.AuthMiddleware(s.HandleAuthRevoke)(w, r)
		} else {
			writeJSONError(w, http.StatusNotFound, "unknown auth endpoint")
		}
	}
}

func initLogger(level, format string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	if w == nil {
		w = os.Stderr
	}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler)
}

// visitedFlags returns the names of flags explicitly set on the command line.
// Needed for flags where empty is a meaningful override (e.g. -mail-archive "").
func visitedFlags() map[string]bool {
	set := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}
