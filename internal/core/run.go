package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

func Run() {
	addr := flag.String("addr", ":8080", "listen address")
	baseURL := flag.String("base-url", "", "public base URL of this Aegis instance (reads AEGIS_BASE_URL env if empty)")
	apiKey := flag.String("api-key", "", "API key for LLM agents / library callers (reads AEGIS_API_KEY env if empty; optional in tailnet/auto modes; never admin)")
	adminAPIKey := flag.String("admin-api-key", "", "API key for admin surfaces (reads AEGIS_ADMIN_API_KEY env if empty; distinct from -api-key so agents cannot rewrite data grants)")
	authMode := flag.String("auth-mode", "", "auth mode: auto, tailnet, or api-key (reads AEGIS_AUTH_MODE; default auto)")
	allowedLogins := flag.String("allowed-logins", "", "comma-separated Tailscale logins allowed to call the API (reads AEGIS_ALLOWED_LOGINS; empty allows any tailnet identity)")
	adminLogins := flag.String("admin-logins", "", "comma-separated Tailscale logins allowed to use admin surfaces (reads AEGIS_ADMIN_LOGINS; empty: allowed user logins are admin, tagged devices are not)")
	tsSocket := flag.String("ts-socket", "", "path to tailscaled LocalAPI socket for WhoIs (default: auto-detect /var/run/tailscale/tailscaled.sock)")
	dataDirFlag := flag.String("data-dir", "", "data directory for local state (reads AEGIS_DATA_DIR if empty; default: OS application data dir for Aegis)")
	configPath := flag.String("config", "", "path to rules configuration file (default: <data-dir>/aegis.json)")
	tokenPath := flag.String("token-file", "", "path to encrypted token storage (default: <data-dir>/tokens.json)")
	linkPath := flag.String("link-file", "", "path to user-confirmed entity links (default: <data-dir>/entity_links.json)")
	copyLedgerPath := flag.String("copy-ledger", "", "path to SQLite copy ledger (default: <data-dir>/copy_ledger.db; empty disables)")
	dataGrantsPath := flag.String("data-grants-file", "", "path to admin data-grant overlay (default: <data-dir>/data_grants.json; admin saves write here instead of aegis.json)")
	serviceConnsPath := flag.String("service-connections-file", "", "path to admin service-connections overlay (default: <data-dir>/service_connections.json; secrets live in the encrypted token store)")
	mailArchivePath := flag.String("mail-archive", "", "path to local compressed mail archive (default: <data-dir>/mail-archive.db; empty disables /v1/mail)")
	quickenPath := flag.String("quicken-store", "", "path to local Quicken import store (default: <data-dir>/quicken.db; empty disables)")
	importQuicken := flag.String("import-quicken", "", "Quicken QIF/QFX file or directory to import into the local store, then exit (issue #141)")
	timelinePath := flag.String("timeline-store", "", "path to local Google Timeline import store (default: <data-dir>/timeline.db; empty disables)")
	importTimeline := flag.String("import-timeline", "", "Google Timeline JSON file, directory, or Takeout zip to import into the local store, then exit (issue #142)")
	signalPath := flag.String("signal-store", "", "path to local Signal import store (default: <data-dir>/signal.db; empty disables)")
	importSignal := flag.String("import-signal", "", "Signal export file or directory (JSONL/JSON or plaintext SQLite) to import into the local store, then exit (issue #168)")
	auditLogPath := flag.String("audit-log", "", "path to durable access audit log (default: <data-dir>/audit_log.db; empty disables)")
	auditRetentionDays := flag.Int("audit-retention-days", defaultAuditRetentionDays, "how many days of audit events to retain before pruning")
	importTakeout := flag.String("import-takeout", "", "read-only Google Takeout path (mbox/zip/dir) to sync into the mail archive, then exit")
	importTakeoutBatch := flag.Int("import-takeout-batch", 20000, "max messages per import-takeout call (repeat runs continue)")
	seedSample := flag.Bool("seed-sample", false, "write synthetic sample mail + library fixtures under the data directory, then exit (issue #77)")
	seedSampleCount := flag.Int("seed-sample-count", SampleMailCount, "mail messages to generate with -seed-sample (default small CI set)")
	seedSampleStress := flag.Bool("seed-sample-stress", false, "with -seed-sample, generate the large stress set (10k mail rows) for local perf")
	seedLotR := flag.Bool("seed-lotr", false, "write Lord of the Rings demo dataset packs (shire + gondor) under the data directory, then exit (issue #217)")
	demoMode := flag.Bool("demo", false, "demo mode: api-key + data scopes only, sample:// fixtures only, read-only surface (issue #217; AEGIS_DEMO=1)")
	tokenPass := flag.String("token-pass", "", "passphrase for token encryption (reads AEGIS_TOKEN_PASS env if empty)")
	tokenNewPass := flag.String("token-new-pass", "", "if set, re-encrypt the token file under this passphrase (with current KDF parameters) and exit")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "json", "log format: json or text")
	logFile := flag.String("log-file", "", "optional process log file (default: <data-dir>/logs/aegis.log; empty disables file logging)")
	apertureURL := flag.String("aperture-url", "", "public base URL of the Aperture gateway this Aegis hooks into (reads AEGIS_APERTURE_URL env if empty; used for admin live links)")
	flag.Parse()

	setFlags := visitedFlags()
	paths, err := ResolveStatePaths(StatePathOverrides{
		Dir:                *dataDirFlag,
		Config:             *configPath,
		DataGrants:         *dataGrantsPath,
		Tokens:             *tokenPath,
		ServiceConnections: *serviceConnsPath,
		Links:              *linkPath,
		CopyLedger:         *copyLedgerPath,
		CopyLedgerSet:      setFlags["copy-ledger"],
		MailArchive:        *mailArchivePath,
		MailArchiveSet:     setFlags["mail-archive"],
		Quicken:            *quickenPath,
		QuickenSet:         setFlags["quicken-store"],
		Timeline:           *timelinePath,
		TimelineSet:        setFlags["timeline-store"],
		Signal:             *signalPath,
		SignalSet:          setFlags["signal-store"],
		AuditLog:           *auditLogPath,
		AuditLogSet:        setFlags["audit-log"],
		LogFile:            *logFile,
		LogFileSet:         setFlags["log-file"],
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
		"data_grants", paths.DataGrants,
		"service_connections", paths.ServiceConnections,
		"links", paths.Links,
		"copy_ledger", paths.CopyLedger,
		"mail_archive", paths.MailArchive,
		"quicken_store", paths.Quicken,
		"timeline_store", paths.Timeline,
		"signal_store", paths.Signal,
		"audit_log", paths.AuditLog,
		"log_file", paths.LogFile,
	)

	// One-shot sample seeding, Takeout ingest, and token rekey do not need
	// OAuth, base URL, or a listening server.
	if *seedSample {
		code := runSeedSampleCLI(paths.Dir, paths.MailArchive, *seedSampleCount, *seedSampleStress)
		os.Exit(code)
	}
	if *seedLotR {
		code := runSeedLotRCLI(paths.Dir, paths.MailArchive)
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
	if strings.TrimSpace(*importTimeline) != "" {
		code := runImportTimelineCLI(paths.Timeline, *importTimeline)
		os.Exit(code)
	}
	if strings.TrimSpace(*importSignal) != "" {
		code := runImportSignalCLI(paths.Signal, *importSignal)
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
	if *adminAPIKey == "" {
		*adminAPIKey = os.Getenv("AEGIS_ADMIN_API_KEY")
	}
	if *authMode == "" {
		*authMode = os.Getenv("AEGIS_AUTH_MODE")
	}
	if *authMode == "" {
		*authMode = AuthModeAuto
	}
	if resolveDemoMode(*demoMode) {
		*demoMode = true
		// Demo hosts are key-auth only (issue #217). Tailnet identity is
		// irrelevant for a public synthetic instance.
		*authMode = AuthModeAPIKey
		slog.Info("demo mode enabled", "profile", "api-key + data scopes, synthetic only, read-only")
	}
	if *allowedLogins == "" {
		*allowedLogins = os.Getenv("AEGIS_ALLOWED_LOGINS")
	}
	if *adminLogins == "" {
		*adminLogins = os.Getenv("AEGIS_ADMIN_LOGINS")
	}
	if err := validateAuthCredentials(*authMode, *apiKey, *adminAPIKey); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
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
	if err := cfg.InitDataGrantsStore(paths.DataGrants); err != nil {
		slog.Error("failed to load data grants overlay", "path", paths.DataGrants, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", paths.Config, "services", cfg.ServiceCount(), "data_grants_file", paths.DataGrants)

	tokens, err := NewTokenStore(paths.Tokens, *tokenPass)
	if err != nil {
		slog.Error("failed to load token store", "path", paths.Tokens, "error", err)
		os.Exit(1)
	}
	// Static credentials from service_connections migrate into the encrypted
	// token store (issue #184). Wrong -token-pass already refused above.
	if err := cfg.InitServiceConnectionsStore(paths.ServiceConnections, tokens); err != nil {
		slog.Error("failed to load service connections overlay", "path", paths.ServiceConnections, "error", err)
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

	scopes, err := NewDataScopeStore(paths.DataScopes)
	if err != nil {
		slog.Error("failed to load data scopes", "path", paths.DataScopes, "error", err)
		os.Exit(1)
	}
	if n := len(scopes.All()); n > 0 {
		slog.Info("data scopes loaded", "count", n, "path", paths.DataScopes)
	}
	if *demoMode {
		if err := validateDemoConfig(cfg, scopes); err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
		scopes.ForceReadOnlyAll()
		slog.Info("demo mode: all data scopes forced read-only")
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
			GrantLimits: types.Permissions{"transactions": types.AccessRead},
		})
	} else {
		slog.Info("quicken store disabled")
	}

	timelineStore, err := OpenTimelineStore(paths.Timeline)
	if err != nil {
		slog.Error("failed to open timeline store", "path", paths.Timeline, "error", err)
		os.Exit(1)
	}
	if timelineStore != nil {
		defer timelineStore.Close()
		slog.Info("timeline store ready", "path", timelineStore.Path())
		// File-based import source. Places is opt-in (default none) — enable
		// city/read/precise on this service or another places provider.
		cfg.EnsureService(Service{
			ID:          timelineServiceID,
			Name:        timelineServiceName,
			Action:      "inject",
			Description: "Locally imported Google Maps Timeline exports (Advanced → Timeline import).",
			GrantLimits: types.Permissions{"places": types.AccessRead},
		})
	} else {
		slog.Info("timeline store disabled")
	}

	signalStore, err := OpenSignalStore(paths.Signal)
	if err != nil {
		slog.Error("failed to open signal store", "path", paths.Signal, "error", err)
		os.Exit(1)
	}
	if signalStore != nil {
		defer signalStore.Close()
		slog.Info("signal store ready", "path", signalStore.Path())
		// File-based import source. Contacts + messages from a local export.
		cfg.EnsureService(Service{
			ID:          signalServiceID,
			Name:        signalServiceName,
			Action:      "inject",
			Description: "Locally imported Signal contacts and messages (Advanced → Signal import).",
			GrantLimits: types.Permissions{
				"contacts": types.AccessRead,
				"messages": types.AccessRead,
			},
		})
	} else {
		slog.Info("signal store disabled")
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
		adminAPIKey:   *adminAPIKey,
		authMode:      *authMode,
		allowedLogins: parseLoginList(*allowedLogins),
		adminLogins:   parseLoginList(*adminLogins),
		identity:      identity,
		baseURL:       *baseURL,
		dataDir:       paths.Dir,
		config:        cfg,
		cache:         NewCache(),
		tokens:        tokens,
		oauthStates:   NewStateStore(),
		connectors:    make(map[string]connectors.ServiceConnector),
		metrics:       NewAccessMetrics(defaultMetricsCapacity),
		links:         links,
		apps:          apps,
		scopes:        scopes,
		demoMode:      *demoMode,
		grants:        NewGrantStore(),
		csrfTokens:    NewStateStore(),
		copies:        copies,
		mail:          mailArchive,
		quicken:       quickenStore,
		timeline:      timelineStore,
		signal:        signalStore,
		hookHealth:    NewHookHealth(defaultHookObservationCapacity),
		audit:         auditLog,
		prom:          NewPromMetrics(),
		apertureURL:   *apertureURL,
	}

	// User-defined types (issue #15): load schema packs from the user-types
	// directory (default <data-dir>/user-types). Fail-open — a bad schema is
	// skipped and logged; built-in kinds always stay available.
	userTypesDir := filepath.Join(paths.Dir, "user-types")
	if v := strings.TrimSpace(os.Getenv("AEGIS_USER_TYPES_DIR")); v != "" {
		userTypesDir = expandUserPath(v)
	}
	udtReg, udtLoaded := LoadUserDefinedTypes(userTypesDir, defaultUserTypesOverlayPath(paths.Config), slog.Default())
	srv.udts = udtReg
	srv.registerUserDefinedKinds()
	if len(udtLoaded) > 0 {
		slog.Info("user-defined types ready", "dir", userTypesDir, "count", len(udtLoaded))
	}

	// Register service connectors.
	spotifyConn := connectors.NewSpotifyConnector(srv)
	srv.connectors["spotify"] = spotifyConn
	ytMusicConn := connectors.NewYouTubeMusicConnector(srv)
	srv.connectors["youtube-music"] = ytMusicConn
	amzMusicConn := connectors.NewAmazonMusicConnector(srv)
	srv.connectors["amazon-music"] = amzMusicConn
	gPhotosConn := connectors.NewGooglePhotosConnector(srv)
	srv.connectors[connectors.GooglePhotosServiceID] = gPhotosConn
	gContactsConn := connectors.NewGoogleContactsConnector(srv)
	srv.connectors[connectors.GoogleContactsServiceID] = gContactsConn
	immichConn := connectors.NewImmichConnector(srv)
	srv.connectors[connectors.ImmichServiceID] = immichConn
	gCalendarConn := connectors.NewGoogleCalendarConnector(srv)
	srv.connectors[connectors.GoogleCalendarServiceID] = gCalendarConn
	gGmailConn := connectors.NewGoogleGmailConnector(srv)
	srv.connectors[connectors.GoogleGmailServiceID] = gGmailConn
	// Financial transactions (issue #141): YNAB live API + local Quicken import.
	srv.connectors[connectors.YNABServiceID] = connectors.NewYNABConnector(srv)
	srv.connectors[quickenServiceID] = NewQuickenConnector(srv, quickenStore)
	// Location history (issue #142): OwnTracks live + local Google Timeline import.
	srv.connectors[connectors.OwnTracksServiceID] = connectors.NewOwnTracksConnector(srv)
	srv.connectors[connectors.BlueskyServiceID] = connectors.NewBlueskyConnector(srv)
	srv.connectors[connectors.PaperlessServiceID] = connectors.NewPaperlessConnector(srv)
	srv.connectors[connectors.GoogleDriveServiceID] = connectors.NewGoogleDriveConnector(srv)
	// CalDAV/CardDAV (issue #146): Nextcloud, iCloud, Fastmail contacts + calendar.
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors[connectors.CalDAVServiceID] = connectors.NewCalDAVConnector(srv)
	srv.connectors[timelineServiceID] = NewTimelineConnector(timelineStore)
	// Signal contacts + messages (issue #168): local file-based import only.
	srv.connectors[signalServiceID] = NewSignalConnector(signalStore)
	// Synthetic calendar fixtures for demos / query testing (issue #77, #131).
	// A service with endpoint sample://calendar gets this connector instead of
	// a live provider so free/busy demos work without OAuth.
	for _, svc := range cfg.ServiceSnapshots() {
		if connectors.IsSampleCalendarService(&connectors.ServiceInfo{ID: svc.ID, Endpoint: svc.Endpoint}) {
			srv.connectors[svc.ID] = connectors.NewSampleCalendarConnector()
		}
	}
	srv.connectors[connectors.SampleCalendarServiceID] = connectors.NewSampleCalendarConnector()
	// Synthetic library fixtures per data-scope dataset (docs/data-scopes.md).
	// A service with endpoint sample://library serves one partition's
	// SampleLibrarySet; Service.Dataset binds it to a principal. Named LotR
	// demo packs (issue #217) win over the generic namespaced sample set.
	for _, svc := range cfg.ServiceSnapshots() {
		if !IsSampleLibraryService(&Service{ID: svc.ID, Endpoint: svc.Endpoint}) {
			continue
		}
		set, ok := GenerateLotRDataset(svc.Dataset)
		if !ok {
			set = GenerateSampleLibraryFor(svc.Dataset)
		}
		srv.connectors[svc.ID] = NewSampleLibraryConnector(svc.ID, svc.Dataset, set)
	}

	mux := http.NewServeMux()
	registerBrandRoutes(mux, srv)
	// Observability (issue #105). Health probes are unauthenticated so
	// supervisors can poll cheaply; /metrics is admin-scoped (issue #181)
	// because series include service ids and denial reasons.
	mux.HandleFunc("GET /healthz", srv.HandleHealthz)
	mux.HandleFunc("HEAD /healthz", srv.HandleHealthz)
	mux.HandleFunc("GET /readyz", srv.HandleReadyz)
	mux.HandleFunc("HEAD /readyz", srv.HandleReadyz)
	mux.HandleFunc("GET /metrics", srv.AdminMiddleware(srv.HandleMetrics))
	mux.HandleFunc("HEAD /metrics", srv.AdminMiddleware(srv.HandleMetrics))
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(withBodyLimit(maxHookBodyBytes, srv.HandlePreRequest)))
	// Third-party grant demo (issue #131). Consent GET + demo page are public
	// like OAuth authorize; grant POST and token exchange have their own rules.
	mux.HandleFunc("GET /demo/third-party", srv.HandleThirdPartyDemo)
	mux.HandleFunc("HEAD /demo/third-party", srv.HandleThirdPartyDemo)
	mux.HandleFunc("GET /v1/apps/authorize", srv.HandleAppAuthorize)
	// Minting an app token is admin-equivalent (issue #181).
	mux.HandleFunc("POST /v1/apps/authorize", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAppAuthorize)))
	mux.HandleFunc("POST /v1/apps/token", withBodyLimit(maxAdminBodyBytes, srv.HandleAppToken))
	mux.HandleFunc("POST /v1/apps/revoke", withBodyLimit(maxAdminBodyBytes, srv.HandleAppRevoke))
	for path, handler := range srv.libraryRouteHandlers() {
		mux.HandleFunc(path, srv.AuthMiddleware(handler))
	}
	// Library writes are admin (issue #181); GET stays on AuthMiddleware above.
	mux.HandleFunc("POST /v1/library/mirror", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleLibraryMirror)))
	// Photo share / ownership-claim PoC (issue #20).
	mux.HandleFunc("POST /v1/library/photos/share", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandlePhotoShare)))
	mux.HandleFunc("POST /v1/library/links", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleEntityLinks)))
	mux.HandleFunc("DELETE /v1/library/links/", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleEntityLinks)))
	mux.HandleFunc("/v1/mail/", srv.AuthMiddleware(withBodyLimit(maxMailBodyBytes, srv.handleMailRoutes)))
	// Mail sync mutates the archive — admin only (issue #181).
	mux.HandleFunc("POST /v1/mail/sync", srv.AdminMiddleware(withBodyLimit(maxMailBodyBytes, srv.HandleMailSync)))
	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(withBodyLimit(maxProxyBodyBytes, srv.HandleServiceProxy)))
	mux.HandleFunc("/v1/auth/status", srv.AuthMiddleware(srv.HandleAuthStatus))
	mux.HandleFunc("/v1/auth/", srv.handleAuthRoutes)
	// Admin shell and JSON both require admin auth (issues #98, #181): the
	// SPA is markup only, but unauthenticated peers should not learn the API
	// surface and agent credentials must not open the admin UI.
	// Friendly SPA routes (issue #150) are the durable URLs the client uses
	// with history.pushState so refresh keeps the same section.
	for _, path := range []string{
		"/{$}",
		"/home",
		"/home/{$}",
		"/library",
		"/library/{$}",
		"/services",
		"/services/{$}",
		"/advanced",
		"/advanced/{$}",
	} {
		mux.HandleFunc("GET "+path, srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAdmin)))
		mux.HandleFunc("HEAD "+path, srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAdmin)))
	}
	mux.HandleFunc("/v1/admin", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.HandleAdmin)))
	mux.HandleFunc("/v1/admin/", srv.AdminMiddleware(withBodyLimit(maxAdminBodyBytes, srv.handleAdminRoutes)))

	handler := securityHeadersMiddleware(mux)
	// Demo lockdown is the outermost gate: synthetic + read-only profile
	// (issue #217) refuses mutations and OAuth regardless of which wrapper
	// a route uses.
	handler = srv.demoLockdown(handler)

	slog.Info("starting server",
		"addr", *addr,
		"base_url", *baseURL,
		"auth_mode", *authMode,
		"api_key_configured", *apiKey != "",
		"admin_api_key_configured", *adminAPIKey != "",
		"allowed_logins", len(srv.allowedLogins),
		"admin_logins", len(srv.adminLogins),
		"data_dir", paths.Dir,
	)
	if err := runHTTPServer(*addr, handler, cfg); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// validateAuthCredentials checks auth-mode and the agent/admin key split
// (issue #181). Returns a fatal error for unusable configs and logs warnings
// for configs that work but weaken the admin boundary.
func validateAuthCredentials(authMode, apiKey, adminAPIKey string) error {
	switch authMode {
	case AuthModeAuto, AuthModeTailnet, AuthModeAPIKey:
	default:
		return fmt.Errorf("invalid auth-mode %q (allowed: auto, tailnet, api-key)", authMode)
	}
	if authMode == AuthModeAPIKey && apiKey == "" && adminAPIKey == "" {
		return fmt.Errorf("api-key or admin-api-key is required in api-key auth mode (flag or env)")
	}
	if authMode == AuthModeAuto && apiKey == "" && adminAPIKey == "" {
		slog.Warn("no API key configured — only tailnet identity will be accepted")
	}
	if adminAPIKey != "" && apiKey != "" && constantTimeEqual(adminAPIKey, apiKey) {
		slog.Warn("admin-api-key equals api-key — agent credentials can use admin surfaces; set a distinct admin-api-key (issue #181)")
	}
	if authMode == AuthModeAPIKey && adminAPIKey == "" {
		slog.Warn("no admin-api-key in api-key mode — admin surfaces are unreachable; set -admin-api-key or use tailnet admin logins (issue #181)")
	}
	return nil
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

// handleAdminRoutes dispatches /v1/admin. Registered under AdminMiddleware
// (issues #98, #181): the HTML shell is markup-only with no secrets, but
// unauthenticated peers must not learn the API surface and agent credentials
// must not reach admin writes. Inner AdminMiddleware keeps direct handler
// tests and future call sites honest.
func (s *Server) handleAdminRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/admin/")
	path = strings.Trim(path, "/")

	switch {
	case path == "":
		s.HandleAdmin(w, r)
	case path == "summary":
		s.AdminMiddleware(s.HandleAdminSummary)(w, r)
	case path == "flow":
		s.AdminMiddleware(s.HandleAdminFlow)(w, r)
	case path == "cache":
		s.AdminMiddleware(s.HandleAdminCache)(w, r)
	case path == "cache/clear":
		s.AdminMiddleware(s.HandleAdminCacheClear)(w, r)
	case path == "hook-health":
		s.AdminMiddleware(s.HandleAdminHookHealth)(w, r)
	case path == "mail/status":
		s.AdminMiddleware(s.HandleAdminMailStatus)(w, r)
	case path == "mail/sync":
		s.AdminMiddleware(s.HandleAdminMailSync)(w, r)
	case path == "mail/import":
		s.AdminMiddleware(s.HandleAdminMailImport)(w, r)
	case strings.HasPrefix(path, "library/"):
		s.AdminMiddleware(s.HandleAdminLibrary)(w, r)
	case path == "permissions":
		s.AdminMiddleware(s.HandleAdminPermissionsOverview)(w, r)
	case path == "data-grants":
		s.AdminMiddleware(s.HandleAdminDataGrants)(w, r)
	case strings.HasPrefix(path, "services/") && strings.HasSuffix(path, "/grant-limits"):
		s.AdminMiddleware(s.HandleAdminServiceGrantLimits)(w, r)
	case path == "apps" || strings.HasPrefix(path, "apps/"):
		s.AdminMiddleware(s.HandleAdminApps)(w, r)
	case path == "immich" || strings.HasPrefix(path, "immich/"):
		s.AdminMiddleware(s.HandleAdminImmichSetup)(w, r)
	case path == "ynab" || strings.HasPrefix(path, "ynab/"):
		s.AdminMiddleware(s.HandleAdminYNABSetup)(w, r)
	case path == "quicken" || strings.HasPrefix(path, "quicken/"):
		s.AdminMiddleware(s.HandleAdminQuicken)(w, r)
	case path == "timeline" || strings.HasPrefix(path, "timeline/"):
		s.AdminMiddleware(s.HandleAdminTimeline)(w, r)
	case path == "signal" || strings.HasPrefix(path, "signal/"):
		s.AdminMiddleware(s.HandleAdminSignal)(w, r)
	case path == "udt" || strings.HasPrefix(path, "udt/"):
		s.AdminMiddleware(s.HandleAdminUDT)(w, r)
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

// runSeedLotRCLI writes the LotR demo packs (shire + gondor) under
// <data-dir>/sample/lotr/ and loads both partitions' mail into the archive.
// All correspondence is original invented text (issue #217).
func runSeedLotRCLI(dataDir, archivePath string) int {
	outDir := filepath.Join(dataDir, "sample", "lotr")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		slog.Error("failed to create lotr sample dir", "dir", outDir, "error", err)
		return 1
	}
	archive, err := OpenMailArchive(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if archive != nil {
		defer archive.Close()
	}
	for _, dataset := range []string{LotRDatasetShire, LotRDatasetGondor} {
		lib, ok := GenerateLotRDataset(dataset)
		if !ok {
			continue
		}
		libPath := filepath.Join(outDir, dataset+"-library.json")
		if err := WriteSampleLibraryJSON(libPath, lib); err != nil {
			slog.Error("failed to write lotr library", "path", libPath, "error", err)
			return 1
		}
		mail := GenerateLotRMail(dataset)
		if archive != nil {
			for _, rec := range mail.Records {
				if err := archive.Upsert(rec); err != nil {
					slog.Error("failed to seed lotr mail", "dataset", dataset, "error", err)
					return 1
				}
			}
		}
		slog.Info("lotr dataset ready (original invented text)",
			"dataset", dataset,
			"mail_messages", len(mail.Records),
			"library_json", libPath,
		)
	}
	slog.Info("lotr demo packs written", "dir", outDir, "mail_archive", archivePath)
	return 0
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

// runImportTimelineCLI imports a Google Timeline JSON file/directory/zip into
// the local store, then exits (issue #142).
func runImportTimelineCLI(storePath, importPath string) int {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		slog.Error("timeline store path is required for -import-timeline")
		return 1
	}
	store, err := OpenTimelineStore(storePath)
	if err != nil {
		slog.Error("failed to open timeline store", "path", storePath, "error", err)
		return 1
	}
	if store == nil {
		slog.Error("timeline store disabled — set -timeline-store")
		return 1
	}
	defer store.Close()
	res, err := ImportTimelinePath(store, importPath)
	if err != nil {
		slog.Error("timeline import failed", "path", importPath, "error", err)
		return 1
	}
	slog.Info("timeline import finished",
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

// runImportSignalCLI imports a Signal export file or directory into the local
// store, then exits (issue #168).
func runImportSignalCLI(storePath, importPath string) int {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		slog.Error("signal store path is required for -import-signal")
		return 1
	}
	store, err := OpenSignalStore(storePath)
	if err != nil {
		slog.Error("failed to open signal store", "path", storePath, "error", err)
		return 1
	}
	if store == nil {
		slog.Error("signal store disabled — set -signal-store")
		return 1
	}
	defer store.Close()
	res, err := ImportSignalPath(store, importPath)
	if err != nil {
		slog.Error("signal import failed", "path", importPath, "error", err)
		return 1
	}
	slog.Info("signal import finished",
		"path", importPath,
		"format", res.Format,
		"contacts_scanned", res.ContactsScanned,
		"contacts_stored", res.ContactsStored,
		"contacts_skipped", res.ContactsSkipped,
		"messages_scanned", res.MessagesScanned,
		"messages_stored", res.MessagesStored,
		"messages_skipped", res.MessagesSkipped,
		"errors", res.Errors,
		"done", res.Done,
	)
	if res.Errors > 0 && res.ContactsStored == 0 && res.MessagesStored == 0 {
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
		"secrets", len(ts.Secrets),
		"iterations", ts.kdfIterations,
	)
	return 0
}

// handleAuthRoutes dispatches /v1/auth/* to the correct handler.
// Login and callback are public (no auth) to support OAuth redirects.
// Status requires agent auth; revoke is admin (issue #181).
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
			s.AdminMiddleware(s.HandleAuthRevoke)(w, r)
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
