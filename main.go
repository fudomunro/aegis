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
	apiKey := flag.String("api-key", "", "API key for authenticating requests (reads APERTURE_GUARDIAN_API_KEY env if empty)")
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
	if *apiKey == "" {
		slog.Error("api-key is required (flag or APERTURE_GUARDIAN_API_KEY env)")
		os.Exit(1)
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

	srv := &Server{
		apiKey:      *apiKey,
		baseURL:     *baseURL,
		config:      cfg,
		cache:       NewCache(),
		tokens:      tokens,
		oauthStates: NewStateStore(),
		connectors:  make(map[string]ServiceConnector),
	}

	// Register service connectors.
	spotifyConn := NewSpotifyConnector(srv)
	srv.connectors["spotify"] = spotifyConn
	ytMusicConn := NewYouTubeMusicConnector(srv)
	srv.connectors["youtube-music"] = ytMusicConn
	amzMusicConn := NewAmazonMusicConnector(srv)
	srv.connectors["amazon-music"] = amzMusicConn

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(srv.HandlePreRequest))
	mux.HandleFunc("/v1/library/playlists", srv.AuthMiddleware(srv.HandleLibraryPlaylists))
	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(srv.HandleServiceProxy))
	mux.HandleFunc("/v1/auth/status", srv.AuthMiddleware(srv.HandleAuthStatus))
	mux.HandleFunc("/v1/auth/", srv.handleAuthRoutes)
	mux.HandleFunc("/v1/setup", srv.HandleSetup)

	slog.Info("starting server", "addr", *addr, "base_url", *baseURL)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
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
