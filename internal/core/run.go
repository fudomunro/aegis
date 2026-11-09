package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"

	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fudomunro/aegis/internal/core/audit"
	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/core/links"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/ownership"
	"github.com/fudomunro/aegis/internal/core/scopes"
	"github.com/fudomunro/aegis/internal/core/search"
	"github.com/fudomunro/aegis/internal/core/tokens"
	"github.com/fudomunro/aegis/internal/shared"
)

func Run() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (loopback by default; with Serve identity trust — auto/tailnet auth mode — a wildcard or LAN bind is refused at startup: bind loopback behind tailscale serve, the Tailscale IP, or use -auth-mode api-key)")
	adminAddr := flag.String("admin-addr", "", "listen address for admin surfaces (tailnet/loopback only; empty = single-listener mode where admin shares -addr)")
	adminBaseURL := flag.String("admin-base-url", "", "public base URL of the admin listener (required with -admin-addr; used for OAuth redirects and CSRF origin checks; reads AEGIS_ADMIN_BASE_URL env if empty)")
	externalAccess := flag.Bool("external-access", false, "publish the API listener for external principals: invite/app tokens only on -addr (no ambient identity, no agent key), admin surfaces only on -admin-addr")
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
	syncJobsPath := flag.String("sync-jobs", "", "path to SQLite sync job queue (default: the copy-ledger path, so job completion and copy stamps share one transaction; empty disables the sync worker)")
	syncWorkers := flag.Int("sync-workers", 2, "sync worker pool size (0 disables the sync worker)")
	syncServiceConcurrency := flag.String("sync-service-concurrency", "", "per-service sync concurrency caps, e.g. spotify=1,youtube-music=2 (default 1 for every service)")
	dataGrantsPath := flag.String("data-grants-file", "", "path to admin data-grant overlay (default: <data-dir>/data_grants.json; admin saves write here instead of aegis.json)")
	serviceConnsPath := flag.String("service-connections-file", "", "path to admin service-connections overlay (default: <data-dir>/service_connections.json; secrets live in the encrypted token store)")
	mailArchivePath := flag.String("mail-archive", "", "path to local compressed mail archive (default: <data-dir>/mail-archive.db; empty disables the local mail source)")
	searchIndexPath := flag.String("search-index", "", "path to local library search index (default: <data-dir>/search-index.db; empty disables q= search)")
	quickenPath := flag.String("quicken-store", "", "path to local Quicken import store (default: <data-dir>/quicken.db; empty disables)")
	importQuicken := flag.String("import-quicken", "", "Quicken QIF/QFX file or directory to import into the local store, then exit")
	timelinePath := flag.String("timeline-store", "", "path to local Google Timeline import store (default: <data-dir>/timeline.db; empty disables)")
	importTimeline := flag.String("import-timeline", "", "Google Timeline JSON file, directory, or Takeout zip to import into the local store, then exit")
	signalPath := flag.String("signal-store", "", "path to local Signal import store (default: <data-dir>/signal.db; empty disables)")
	importSignal := flag.String("import-signal", "", "Signal export file or directory (JSONL/JSON or plaintext SQLite) to import into the local store, then exit")
	auditLogPath := flag.String("audit-log", "", "path to durable access audit log (default: <data-dir>/audit_log.db; empty disables)")
	maintenanceRepo := flag.String("maintenance-repo", "", "GitHub repo (owner/name) where sync-maintenance tickets are opened as issues (reads AEGIS_MAINTENANCE_REPO if empty; empty disables ticketing)")
	fakeUpstream := flag.Bool("fake-upstream", false, "dev/test only: serve an injectable fake provider at /demo/fake-upstream and seed placeholder tokens for services pointed at it (never enable on a real instance)")
	auditRetentionDays := flag.Int("audit-retention-days", audit.DefaultRetentionDays, "how many days of audit events to retain before pruning")
	importTakeout := flag.String("import-takeout", "", "read-only Google Takeout path (mbox/zip/dir) to sync into the mail archive, then exit")
	importTakeoutBatch := flag.Int("import-takeout-batch", 20000, "max messages per import-takeout call (repeat runs continue)")
	seedSample := flag.Bool("seed-sample", false, "write synthetic sample mail + library fixtures under the data directory, then exit")
	seedSampleCount := flag.Int("seed-sample-count", SampleMailCount, "mail messages to generate with -seed-sample (default small CI set)")
	seedSampleStress := flag.Bool("seed-sample-stress", false, "with -seed-sample, generate the large stress set (10k mail rows) for local perf")
	seedLotR := flag.Bool("seed-lotr", false, "write named demo dataset packs (shire, gondor, lincoln) under the data directory, then exit")
	seedDemoDatasets := flag.Bool("seed-demo-datasets", false, "same as -seed-lotr: write all named demo dataset packs")
	demoMode := flag.Bool("demo", false, "demo mode: api-key + data scopes only, sample:// fixtures only, read-only surface (AEGIS_DEMO=1)")
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
		SyncJobs:           *syncJobsPath,
		SyncJobsSet:        setFlags["sync-jobs"],
		MailArchive:        *mailArchivePath,
		MailArchiveSet:     setFlags["mail-archive"],
		SearchIndex:        *searchIndexPath,
		SearchIndexSet:     setFlags["search-index"],
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
		"sync_jobs", paths.SyncJobs,
		"mail_archive", paths.MailArchive,
		"search_index", paths.SearchIndex,
		"quicken_store", paths.Quicken,
		"timeline_store", paths.Timeline,
		"signal_store", paths.Signal,
		"audit_log", paths.AuditLog,
		"log_file", paths.LogFile,
	)

	// One-shot sample seeding, Takeout ingest, and token rekey do not need
	// OAuth, base URL, or a listening server.
	if code, done := runCLIMode(cliModeOptions{
		seedSample:         *seedSample,
		seedSampleCount:    *seedSampleCount,
		seedSampleStress:   *seedSampleStress,
		seedLotR:           *seedLotR,
		seedDemoDatasets:   *seedDemoDatasets,
		importTakeout:      *importTakeout,
		importTakeoutBatch: *importTakeoutBatch,
		importQuicken:      *importQuicken,
		importTimeline:     *importTimeline,
		importSignal:       *importSignal,
		tokenNewPass:       *tokenNewPass,
		tokenPass:          *tokenPass,
	}, paths); done {
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
	if *maintenanceRepo == "" {
		*maintenanceRepo = os.Getenv("AEGIS_MAINTENANCE_REPO")
	}
	if err := validateAuthCredentials(*authMode, *apiKey, *adminAPIKey); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	// Under external access the API listener resolves no ambient identity
	// (tokens only), so the Serve-header forgery risk that bind validation
	// guards against does not apply — the public bind is the point. The
	// admin listener is bound by validateSplitListeners instead.
	if !*externalAccess {
		if err := validateListenAddr(*addr, *authMode); err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
	}

	if *baseURL == "" {
		*baseURL = os.Getenv("AEGIS_BASE_URL")
	}
	if *baseURL == "" {
		slog.Error("base-url is required (flag or AEGIS_BASE_URL env)")
		os.Exit(1)
	}
	if *adminBaseURL == "" {
		*adminBaseURL = os.Getenv("AEGIS_ADMIN_BASE_URL")
	}
	if err := validateSplitListeners(*adminAddr, *adminBaseURL, *externalAccess); err != nil {
		slog.Error(err.Error())
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
	if err := validateEdgeTrust(*authMode, *externalAccess, cfg.trustedNets()); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if err := validatePublicCredentials(cfg.trustedNets(), *apiKey, *adminAPIKey); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if len(cfg.trustedNets()) == 0 {
		slog.Warn("trusted_proxies is empty — per-IP rate limits key on the peer address and are skipped for loopback peers (a same-host edge would share one bucket); set trusted_proxies when Aegis sits behind a reverse proxy")
	}
	if err := cfg.InitDataGrantsStore(paths.DataGrants); err != nil {
		slog.Error("failed to load data grants overlay", "path", paths.DataGrants, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", paths.Config, "services", cfg.ServiceCount(), "data_grants_file", paths.DataGrants)

	tokens, err := tokens.NewTokenStore(paths.Tokens, *tokenPass)
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

	linkStore, err := links.NewStore(paths.Links)
	if err != nil {
		slog.Error("failed to load entity link store", "path", paths.Links, "error", err)
		os.Exit(1)
	}

	ownership, err := ownership.NewStore(paths.Ownership)
	if err != nil {
		slog.Error("failed to load ownership state", "path", paths.Ownership, "error", err)
		os.Exit(1)
	}

	apps, err := NewAppStore(paths.Apps)
	if err != nil {
		slog.Error("failed to load app registry", "path", paths.Apps, "error", err)
		os.Exit(1)
	}

	scopes, err := scopes.NewStore(paths.DataScopes)
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

	copyLedger, err := copies.Open(paths.CopyLedger)
	if err != nil {
		slog.Error("failed to open copy ledger", "path", paths.CopyLedger, "error", err)
		os.Exit(1)
	}
	if copyLedger != nil {
		defer copyLedger.Close()
		slog.Info("copy ledger ready", "path", copyLedger.Path(), "enabled", copyLedger.Enabled())
	}

	// Sync job queue (issue #227). Defaults to the copy-ledger file so a
	// job's completion and its copy stamp can commit in one transaction.
	// Shares the ledger handle only when the paths actually match; an
	// explicit -sync-jobs opens its own file.
	var syncShared *sql.DB
	if paths.SyncJobs != "" && paths.SyncJobs == paths.CopyLedger {
		syncShared = copyLedger.DB()
	}
	syncStore, err := OpenJobStore(syncShared, paths.SyncJobs)
	if err != nil {
		slog.Error("failed to open sync job store", "path", paths.SyncJobs, "error", err)
		os.Exit(1)
	}
	if syncStore != nil {
		defer syncStore.Close()
		slog.Info("sync job store ready", "path", paths.SyncJobs, "enabled", syncStore.Enabled())
	}

	// Recurring sync templates live beside the jobs they feed, so a schedule
	// and its job share one handle and one backup.
	scheduleStore, err := OpenScheduleStore(syncStore.DB(), paths.SyncJobs)
	if err != nil {
		slog.Error("failed to open sync schedule store", "path", paths.SyncJobs, "error", err)
		os.Exit(1)
	}
	if scheduleStore != nil {
		defer scheduleStore.Close()
		if scheduleStore.Enabled() {
			if added, err := scheduleStore.SeedDefaults(); err == nil && added > 0 {
				slog.Info("seeded default sync schedules", "count", added)
			}
			slog.Info("sync schedule store ready", "count", len(scheduleStore.List()))
		}
	}

	mailArchive, err := mail.Open(paths.MailArchive)
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

	searchIndex, err := search.Open(paths.SearchIndex)
	if err != nil {
		slog.Error("failed to open search index", "path", paths.SearchIndex, "error", err)
		os.Exit(1)
	}
	if searchIndex != nil {
		defer searchIndex.Close()
		slog.Info("search index ready", "path", searchIndex.Path())
	} else {
		slog.Info("search index disabled")
	}

	quickenStore, err := connectors.OpenQuickenStore(paths.Quicken)
	if err != nil {
		slog.Error("failed to open quicken store", "path", paths.Quicken, "error", err)
		os.Exit(1)
	}
	if quickenStore != nil {
		defer quickenStore.Close()
		slog.Info("quicken store ready", "path", quickenStore.Path())
		// File-based import source: not an operator-facing service card.
		// Inject an in-memory entry so /v1/library/transactions can fan in.
		ensureLocalStoreService(cfg, connectors.QuickenService())
	} else {
		slog.Info("quicken store disabled")
	}

	timelineStore, err := connectors.OpenTimelineStore(paths.Timeline)
	if err != nil {
		slog.Error("failed to open timeline store", "path", paths.Timeline, "error", err)
		os.Exit(1)
	}
	if timelineStore != nil {
		defer timelineStore.Close()
		slog.Info("timeline store ready", "path", timelineStore.Path())
		// File-based import source. Places is opt-in (default none) — enable
		// city/read/precise on this service or another places provider.
		ensureLocalStoreService(cfg, connectors.TimelineService())
	} else {
		slog.Info("timeline store disabled")
	}

	signalStore, err := connectors.OpenSignalStore(paths.Signal)
	if err != nil {
		slog.Error("failed to open signal store", "path", paths.Signal, "error", err)
		os.Exit(1)
	}
	if signalStore != nil {
		defer signalStore.Close()
		slog.Info("signal store ready", "path", signalStore.Path())
		// File-based import source. Contacts + messages from a local export.
		ensureLocalStoreService(cfg, connectors.SignalService())
	} else {
		slog.Info("signal store disabled")
	}

	auditLog, err := audit.Open(paths.AuditLog, *auditRetentionDays)
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
		mbCache:       NewCache(),
		tokens:        tokens,
		oauthStates:   shared.NewStateStore(),
		rl:            newRateLimiter(),
		connectors:    make(map[string]connectors.ServiceConnector),
		metrics:       NewAccessMetrics(defaultMetricsCapacity),
		links:         linkStore,
		ownership:     ownership,
		apps:          apps,
		scopes:        scopes,
		demoMode:      *demoMode,
		grants:        NewGrantStore(),
		csrfTokens:    shared.NewStateStore(),
		copyLedger:    copyLedger,
		syncJobs:      syncStore,
		syncSchedules: scheduleStore,
		mail:          mailArchive,
		search:        searchIndex,
		quicken:       quickenStore,
		timeline:      timelineStore,
		signal:        signalStore,
		hookHealth:    NewHookHealth(defaultHookObservationCapacity),
		audit:         auditLog,
		prom:          NewPromMetrics(),
		apertureURL:   *apertureURL,
	}

	// The kinds aggregate engine owns fetch coalescing and search
	// write-behind over the shared cache and index (issue #312).
	srv.kindEngine = kinds.NewEngine(srv.cache, srv.search)

	// Drain async search-index write-behind before searchIndex.Close (its
	// defer runs later) — registered after the store's defer on purpose.
	defer srv.waitIndex()

	// Sync-maintenance tickets are GitHub issues (issue #86). An empty repo
	// disables ticketing: scans still cluster and report, nothing external.
	if *maintenanceRepo != "" {
		srv.maintenanceIssues = NewGhIssueTracker(*maintenanceRepo)
		slog.Info("maintenance ticketing enabled", "repo", *maintenanceRepo)
	}

	// User-defined types (issue #15): load schema packs from the user-types
	// directory (default <data-dir>/user-types). Fail-open — a bad schema is
	// skipped and logged; built-in kinds always stay available.
	userTypesDir := filepath.Join(paths.Dir, "user-types")
	if v := strings.TrimSpace(os.Getenv("AEGIS_USER_TYPES_DIR")); v != "" {
		userTypesDir = shared.ExpandUserPath(v)
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
	srv.connectors[connectors.QuickenServiceID] = connectors.NewQuickenConnector(quickenStore)
	// Location history (issue #142): OwnTracks live + local Google Timeline import.
	srv.connectors[connectors.OwnTracksServiceID] = connectors.NewOwnTracksConnector(srv)
	srv.connectors[connectors.BlueskyServiceID] = connectors.NewBlueskyConnector(srv)
	srv.connectors[connectors.PaperlessServiceID] = connectors.NewPaperlessConnector(srv)
	srv.connectors[connectors.GoogleDriveServiceID] = connectors.NewGoogleDriveConnector(srv)
	// CalDAV/CardDAV (issue #146): Nextcloud, iCloud, Fastmail contacts + calendar.
	srv.connectors[connectors.CardDAVServiceID] = connectors.NewCardDAVConnector(srv)
	srv.connectors[connectors.CalDAVServiceID] = connectors.NewCalDAVConnector(srv)
	srv.connectors[connectors.TimelineServiceID] = connectors.NewTimelineConnector(timelineStore)
	// Signal contacts + messages (issue #168): local file-based import only.
	srv.connectors[connectors.SignalServiceID] = connectors.NewSignalConnector(signalStore)
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
		set, ok := GenerateDemoDataset(svc.Dataset)
		if !ok {
			set = GenerateSampleLibraryFor(svc.Dataset)
		}
		srv.connectors[svc.ID] = NewSampleLibraryConnector(svc.ID, svc.Dataset, set)
	}

	// Mail ingest sources (issue #286) beside the connector map: the Gmail
	// connector is its own ingest; Takeout is ingest-only.
	srv.wireMailIngests()

	// Sync worker (issue #227): drains the job queue in-process. Runners
	// need the connector map above, so this starts after registration. The
	// registry lives on the Server too, so admin enqueue can validate a
	// request even when -sync-workers 0 leaves the pool off.
	// Fail-open: a disabled store or zero workers leaves the queue idle and
	// the request-driven sync surfaces keep working.
	srv.syncRunners = newSyncRunnerRegistry(srv)
	registerFreshnessRunners(srv.syncRunners, srv)
	registerMirrorRunners(srv.syncRunners, srv)
	registerCanaryRunners(srv.syncRunners, srv)
	if syncStore.Enabled() && *syncWorkers > 0 {
		srv.syncWorker = NewSyncWorker(syncStore, srv.syncRunners, SyncWorkerOptions{
			Workers:            *syncWorkers,
			Owner:              "aegis",
			Logger:             slog.Default(),
			Prom:               srv.prom,
			Schedules:          scheduleStore,
			ServiceConcurrency: parseServiceConcurrency(*syncServiceConcurrency),
			DefaultConcurrency: 1,
			// Ingest changes what /v1/library/* would return; drop cached
			// aggregates (and that service's transport entries) instead of
			// waiting out the TTL. MBID lookups survive (issue #273).
			OnDone: func(job SyncJob, succeeded bool) {
				if succeeded {
					srv.invalidateService(job.Service)
				}
			},
		})
		srv.syncWorker.Start()
		defer srv.syncWorker.Stop()
	} else {
		slog.Info("sync worker disabled", "queue_enabled", syncStore.Enabled(), "workers", *syncWorkers)
	}

	mux := http.NewServeMux()
	var fu *connectors.FakeUpstream
	if *fakeUpstream {
		fu = connectors.NewFakeUpstream()
		seedFakeUpstreamTokens(srv)
		slog.Warn("fake upstream enabled — dev/test only", "mount", fakeUpstreamMount)
	}
	registerRoutes(mux, srv, fu)

	handler := securityHeadersMiddleware(mux)
	// Demo lockdown is the outermost gate: synthetic + read-only profile
	// (issue #217) refuses mutations and OAuth regardless of which wrapper
	// a route uses.
	handler = srv.demoLockdown(handler)

	slog.Info("starting server",
		"addr", *addr,
		"admin_addr", *adminAddr,
		"external_access", *externalAccess,
		"base_url", *baseURL,
		"admin_base_url", *adminBaseURL,
		"auth_mode", *authMode,
		"api_key_configured", *apiKey != "",
		"admin_api_key_configured", *adminAPIKey != "",
		"allowed_logins", len(srv.allowedLogins),
		"admin_logins", len(srv.adminLogins),
		"data_dir", paths.Dir,
	)
	var serveErr error
	if *adminAddr == "" {
		// Single-listener mode (tailnet-only or edge-enforced split like the
		// demo): one surface serves every route, as before.
		serveErr = runHTTPServer(*addr, handler, cfg)
	} else {
		// Split listeners (docs/external-access.md): the API listener serves
		// only the agent/external allowlist — unlisted routes 404 there and
		// default to the admin side. The admin listener serves everything
		// (admin tier + agent tier for tailnet callers) and stays in-tailnet.
		apiHandler := listenerMiddleware(*baseURL, *externalAccess,
			srv.demoLockdown(securityHeadersMiddleware(apiAllowlistGate(mux))))
		adminHandler := listenerMiddleware(*adminBaseURL, false, handler)
		serveErr = runHTTPServers([]namedListener{
			{name: "api", addr: *addr, Handler: apiHandler},
			{name: "admin", addr: *adminAddr, Handler: adminHandler},
		}, cfg)
	}
	if serveErr != nil {
		slog.Error("server failed", "error", serveErr)
		os.Exit(1)
	}
}

// tailscaleListenNets are the address ranges Tailscale assigns to nodes
// (CGNAT for IPv4, the Tailscale ULA for IPv6). Binding there keeps the
// listener off LAN interfaces while still reachable over the tailnet.
var tailscaleListenNets = func() []*net.IPNet {
	nets := make([]*net.IPNet, 0, 2)
	for _, cidr := range []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"} {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, n)
	}
	return nets
}()

func isTailnetListenIP(ip net.IP) bool {
	for _, n := range tailscaleListenNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validateListenAddr refuses binds that would expose Serve identity headers
// to peers that can forge them. Serve/WhoIs trust (auth modes auto and
// tailnet) is only sound when the listener is not reachable from a network
// where clients arrive unproxied: loopback (behind `tailscale serve`), a
// Tailscale IP (tailnet-only), or an equivalent local proxy. A wildcard or
// LAN bind would let any peer on that network present a forged
// `Tailscale-User-Login` header directly to the port (the default admin rule
// turns one forged header into full admin). api-key mode resolves no ambient
// identity, so any bind is fine there.
func validateListenAddr(addr, authMode string) error {
	if authMode == AuthModeAPIKey {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -addr %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing -addr %q: with Serve identity trust (auth-mode %s) bind an IP literal on loopback or the Tailscale IP, not %q", addr, authMode, host)
	}
	if ip.IsLoopback() || isTailnetListenIP(ip) {
		return nil
	}
	return fmt.Errorf("refusing -addr %q: with Serve identity trust (auth-mode %s) a non-loopback bind would accept forged Tailscale identity headers from the LAN — bind 127.0.0.1 and front with `tailscale serve`, bind the Tailscale IP, or use -auth-mode api-key", addr, authMode)
}

// minPublicCredentialLen is the minimum -api-key / -admin-api-key length
// accepted behind a trusted edge (issue #253). On a published listener
// those keys are the perimeter, and the rate caps only slow guessing — a
// short operator-chosen key is brute-forceable regardless of damping.
const minPublicCredentialLen = 32

// validatePublicCredentials refuses weak operator-chosen credentials in a
// deployment whose edge is trusted (public shape). Generated credentials
// (invite/app tokens) are high-entropy and not affected.
func validatePublicCredentials(trusted []net.IPNet, apiKey, adminAPIKey string) error {
	if len(trusted) == 0 {
		return nil
	}
	for name, key := range map[string]string{"api-key": apiKey, "admin-api-key": adminAPIKey} {
		if key == "" {
			continue
		}
		if len(key) < minPublicCredentialLen {
			return fmt.Errorf("refusing %s shorter than %d characters behind trusted_proxies: on a published listener this key is the perimeter and rate caps only slow guessing — generate a long random key", name, minPublicCredentialLen)
		}
	}
	return nil
}

// validateEdgeTrust refuses a generic-edge deployment whose edge can forge
// Serve identity headers into the listener (issue #253). trusted_proxies
// means a non-Tailscale edge (nginx, Caddy, a tunnel) is in front: those
// pass client-supplied Tailscale-User-Login straight through, while the
// local-peer gate still trusts the loopback hop — one forged header is full
// admin under the default admin rule. Sound behind such an edge: api-key
// mode (no ambient identity at all), or -external-access (tokens only on
// the published API listener; the admin listener stays tailnet-bound and is
// never edge-facing).
func validateEdgeTrust(authMode string, externalAccess bool, trusted []net.IPNet) error {
	if len(trusted) == 0 || authMode == AuthModeAPIKey || externalAccess {
		return nil
	}
	return fmt.Errorf("refusing trusted_proxies with Serve identity trust (auth-mode %s): a generic edge forwards client-supplied Tailscale-User-Login and the local-peer gate would trust it into admin — use -auth-mode api-key or -external-access behind an edge, or drop trusted_proxies (see docs/external-access.md)", authMode)
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
		slog.Warn("admin-api-key equals api-key — agent credentials can use admin surfaces; set a distinct admin-api-key")
	}
	if authMode == AuthModeAPIKey && adminAPIKey == "" {
		slog.Warn("no admin-api-key in api-key mode — admin surfaces are unreachable; set -admin-api-key or use tailnet admin logins")
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

// cliModeOptions holds the flag values that select a one-shot CLI mode
// (issue #274). Extracted from Run so the dispatch is testable.
type cliModeOptions struct {
	seedSample         bool
	seedSampleCount    int
	seedSampleStress   bool
	seedLotR           bool
	seedDemoDatasets   bool
	importTakeout      string
	importTakeoutBatch int
	importQuicken      string
	importTimeline     string
	importSignal       string
	tokenNewPass       string
	tokenPass          string
}

// runCLIMode checks whether a one-shot CLI flag was given and, if so, runs
// the corresponding mode and returns its exit code. The second result is
// true when the caller should exit immediately (issue #274).
func runCLIMode(opts cliModeOptions, paths DataPaths) (code int, handled bool) {
	if opts.seedSample {
		return runSeedSampleCLI(paths.Dir, paths.MailArchive, opts.seedSampleCount, opts.seedSampleStress), true
	}
	if opts.seedLotR || opts.seedDemoDatasets {
		return runSeedDemoDatasetsCLI(paths.Dir, paths.MailArchive), true
	}
	if strings.TrimSpace(opts.importTakeout) != "" {
		return runImportTakeoutCLI(paths.MailArchive, paths.CopyLedger, opts.importTakeout, opts.importTakeoutBatch), true
	}
	if strings.TrimSpace(opts.importQuicken) != "" {
		return runImportQuickenCLI(paths.Quicken, opts.importQuicken), true
	}
	if strings.TrimSpace(opts.importTimeline) != "" {
		return runImportTimelineCLI(paths.Timeline, opts.importTimeline), true
	}
	if strings.TrimSpace(opts.importSignal) != "" {
		return runImportSignalCLI(paths.Signal, opts.importSignal), true
	}
	if strings.TrimSpace(opts.tokenNewPass) != "" {
		pass := opts.tokenPass
		if pass == "" {
			pass = os.Getenv("AEGIS_TOKEN_PASS")
		}
		return runTokenRekeyCLI(paths.Tokens, pass, opts.tokenNewPass), true
	}
	return 0, false
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

// parseServiceConcurrency reads "service=N,service=M" caps for the sync
// worker. Unparseable entries are ignored so a typo cannot stop the queue.
func parseServiceConcurrency(raw string) map[string]int {
	out := make(map[string]int)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n <= 0 {
			continue
		}
		out[strings.TrimSpace(k)] = n
	}
	return out
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

// runSeedDemoDatasetsCLI writes named demo packs (shire, gondor, lincoln)
// under <data-dir>/sample/datasets/ and loads each partition's mail into the
// archive. Content rules vary by pack; see docs/demo-datasets.md (issue #217).
func runSeedDemoDatasetsCLI(dataDir, archivePath string) int {
	outDir := filepath.Join(dataDir, "sample", "datasets")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		slog.Error("failed to create demo dataset dir", "dir", outDir, "error", err)
		return 1
	}
	archive, err := mail.Open(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if archive != nil {
		defer archive.Close()
	}
	for _, dataset := range demoDatasetIDs() {
		lib, ok := GenerateDemoDataset(dataset)
		if !ok {
			continue
		}
		libPath := filepath.Join(outDir, dataset+"-library.json")
		if err := WriteSampleLibraryJSON(libPath, lib); err != nil {
			slog.Error("failed to write demo library", "path", libPath, "error", err)
			return 1
		}
		mail := GenerateDemoMail(dataset)
		if archive != nil {
			for _, rec := range mail.Records {
				if err := archive.Upsert(rec); err != nil {
					slog.Error("failed to seed demo mail", "dataset", dataset, "error", err)
					return 1
				}
			}
		}
		slog.Info("demo dataset ready",
			"dataset", dataset,
			"mail_messages", len(mail.Records),
			"library_json", libPath,
		)
	}
	slog.Info("demo dataset packs written", "dir", outDir, "mail_archive", archivePath)
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
	a, err := mail.Open(archivePath)
	if err != nil {
		slog.Error("failed to open mail archive", "path", archivePath, "error", err)
		return 1
	}
	if a == nil {
		slog.Error("mail archive disabled — set -mail-archive")
		return 1
	}
	defer a.Close()
	copyLedger, err := copies.Open(copyLedgerPath)
	if err != nil {
		slog.Error("failed to open copy ledger", "path", copyLedgerPath, "error", err)
		return 1
	}
	if copyLedger != nil {
		defer copyLedger.Close()
	}
	if batch <= 0 {
		batch = 20000
	}
	ingest := connectors.NewTakeoutIngest()
	sink := mail.Sink{A: a}
	var stored, skipped, scanned, failed int
	done := false
	for {
		// Path persists the export location (the CLI's -import-takeout contract).
		res, err := ingest.IngestMail(context.Background(), sink,
			shared.MailIngestOptions{Path: takeoutPath, MaxMessages: batch})
		stored += res.Stored
		skipped += res.Skipped
		scanned += res.Listed
		failed += res.Failed
		slog.Info("takeout import batch",
			"path", takeoutPath,
			"scanned", res.Listed,
			"stored", res.Stored,
			"skipped", res.Skipped,
			"failed", res.Failed,
			"done", res.Done,
			"error", res.Error,
		)
		if err != nil && res.Error != "" && res.Stored == 0 && res.Listed == 0 {
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
	copyLedger.RecordTakeoutSnapshot(takeoutPath, scanned, stored, skipped, done, connectors.TakeoutServiceID)
	slog.Info("takeout import finished",
		"path", takeoutPath,
		"scanned", scanned,
		"stored", stored,
		"skipped", skipped,
		"failed", failed,
	)
	return 0
}

// ensureLocalStoreService injects a local import store's service card into
// config so /v1/library fan-in sees it as a source (issue #266).
func ensureLocalStoreService(cfg *Config, d connectors.LocalStoreService) {
	cfg.EnsureService(Service{
		ID:          d.ID,
		Name:        d.Name,
		Action:      d.Action,
		Description: d.Description,
		GrantLimits: d.GrantLimits,
	})
}

// runImportCLI runs a one-shot import against any local import store, then
// exits (issue #266). Flag parsing stays here; the store owns the run.
func runImportCLI(name, storePath, importPath string, open func(string) (connectors.ImportStore, error), run func(connectors.ImportStore, string) (connectors.ImportOutcome, error)) int {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		slog.Error(name + " store path is required for -import-" + name)
		return 1
	}
	store, err := open(storePath)
	if err != nil {
		slog.Error("failed to open "+name+" store", "path", storePath, "error", err)
		return 1
	}
	if store == nil || !store.Enabled() {
		slog.Error(name + " store disabled — set -" + name + "-store")
		return 1
	}
	defer store.Close()
	res, err := run(store, importPath)
	if err != nil {
		slog.Error(name+" import failed", "path", importPath, "error", err)
		return 1
	}
	slog.Info(name+" import finished",
		append([]any{"path", importPath}, res.LogAttrs()...)...,
	)
	if res.Failed() {
		return 1
	}
	return 0
}

// runImportQuickenCLI imports a QIF/QFX file or directory into the local
// Quicken store, then exits (issue #141).
func runImportQuickenCLI(storePath, importPath string) int {
	return runImportCLI("quicken", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenQuickenStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportQuickenPath(st.(*connectors.QuickenStore), p)
		})
}

// runImportTimelineCLI imports a Google Timeline JSON file/directory/zip into
// the local store, then exits (issue #142).
func runImportTimelineCLI(storePath, importPath string) int {
	return runImportCLI("timeline", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenTimelineStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportTimelinePath(st.(*connectors.TimelineStore), p)
		})
}

// runImportSignalCLI imports a Signal export file or directory into the local
// store, then exits (issue #168).
func runImportSignalCLI(storePath, importPath string) int {
	return runImportCLI("signal", storePath, importPath,
		func(p string) (connectors.ImportStore, error) {
			return connectors.OpenSignalStore(p)
		},
		func(st connectors.ImportStore, p string) (connectors.ImportOutcome, error) {
			return connectors.ImportSignalPath(st.(*connectors.SignalStore), p)
		})
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
	ts, err := tokens.NewTokenStore(path, currentPass)
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
		"iterations", ts.KDFIterations(),
	)
	return 0
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
