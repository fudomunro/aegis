package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	baseURL := flag.String("base-url", "", "public base URL of this Guardian instance (reads GUARDIAN_BASE_URL env if empty)")
	apiKey := flag.String("api-key", "", "API key for authenticating requests (reads APERTURE_GUARDIAN_API_KEY env if empty; optional in tailnet/auto modes)")
	authMode := flag.String("auth-mode", "", "auth mode: auto, tailnet, or api-key (reads GUARDIAN_AUTH_MODE; default auto)")
	allowedLogins := flag.String("allowed-logins", "", "comma-separated Tailscale logins allowed to call the API (reads GUARDIAN_ALLOWED_LOGINS; empty allows any tailnet identity)")
	tsSocket := flag.String("ts-socket", "", "path to tailscaled LocalAPI socket for WhoIs (default: auto-detect /var/run/tailscale/tailscaled.sock)")
	configPath := flag.String("config", "guardian.json", "path to rules configuration file")
	tokenPath := flag.String("token-file", "tokens.json", "path to encrypted token storage")
	tokenPass := flag.String("token-pass", "", "passphrase for token encryption (reads GUARDIAN_TOKEN_PASS env if empty)")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "json", "log format: json or text")
	flag.Parse()

	logger := initLogger(*logLevel, *logFormat)
	slog.SetDefault(logger)

	if *apiKey == "" {
		*apiKey = os.Getenv("APERTURE_GUARDIAN_API_KEY")
	}
	if *authMode == "" {
		*authMode = os.Getenv("GUARDIAN_AUTH_MODE")
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
		*allowedLogins = os.Getenv("GUARDIAN_ALLOWED_LOGINS")
	}

	if *authMode == AuthModeAPIKey && *apiKey == "" {
		slog.Error("api-key is required in api-key auth mode (flag or APERTURE_GUARDIAN_API_KEY env)")
		os.Exit(1)
	}
	if *authMode == AuthModeAuto && *apiKey == "" {
		slog.Warn("no API key configured — only tailnet identity will be accepted")
	}

	if *baseURL == "" {
		*baseURL = os.Getenv("GUARDIAN_BASE_URL")
	}
	if *baseURL == "" {
		slog.Error("base-url is required (flag or GUARDIAN_BASE_URL env)")
		os.Exit(1)
	}

	if *tokenPass == "" {
		*tokenPass = os.Getenv("GUARDIAN_TOKEN_PASS")
	}
	if *tokenPass == "" {
		slog.Error("token-pass is required (flag or GUARDIAN_TOKEN_PASS env)")
		os.Exit(1)
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", *configPath, "services", len(cfg.Services))

	tokens, err := NewTokenStore(*tokenPath, *tokenPass)
	if err != nil {
		slog.Error("failed to load token store", "path", *tokenPath, "error", err)
		os.Exit(1)
	}

	identity := buildIdentityResolver(*authMode, *tsSocket)

	srv := &Server{
		apiKey:        *apiKey,
		authMode:      *authMode,
		allowedLogins: parseLoginList(*allowedLogins),
		identity:      identity,
		baseURL:       *baseURL,
		config:        cfg,
		cache:         NewCache(),
		tokens:        tokens,
		oauthStates:   NewStateStore(),
		connectors:    make(map[string]ServiceConnector),
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

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(srv.HandlePreRequest))
	for path, handler := range srv.libraryRouteHandlers() {
		mux.HandleFunc(path, srv.AuthMiddleware(handler))
	}
	mux.HandleFunc("POST /v1/library/mirror", srv.AuthMiddleware(srv.HandleLibraryMirror))
	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(srv.HandleServiceProxy))
	mux.HandleFunc("/v1/auth/status", srv.AuthMiddleware(srv.HandleAuthStatus))
	mux.HandleFunc("/v1/auth/", srv.handleAuthRoutes)
	mux.HandleFunc("/v1/setup", srv.HandleSetup)
	mux.HandleFunc("/v1/admin", srv.HandleAdmin)
	mux.HandleFunc("/v1/admin/", srv.handleAdminRoutes)

	slog.Info("starting server",
		"addr", *addr,
		"base_url", *baseURL,
		"auth_mode", *authMode,
		"api_key_configured", *apiKey != "",
		"allowed_logins", len(srv.allowedLogins),
	)
	if err := http.ListenAndServe(*addr, mux); err != nil {
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

// handleAdminRoutes dispatches /v1/admin. The HTML shell is public (no
// secrets in markup); JSON data endpoints require normal Guardian auth.
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
	case strings.HasPrefix(path, "services/") && strings.HasSuffix(path, "/permissions"):
		s.AuthMiddleware(s.HandleAdminServicePermissions)(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
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

func initLogger(level, format string) *slog.Logger {
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

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}
