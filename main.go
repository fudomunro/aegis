package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	baseURL := flag.String("base-url", "", "public base URL of this Guardian instance (reads GUARDIAN_BASE_URL env if empty)")
	apiKey := flag.String("api-key", "", "API key for authenticating requests (reads APERTURE_GUARDIAN_API_KEY env if empty)")
	configPath := flag.String("config", "guardian.json", "path to rules configuration file")
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

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", *configPath, "services", len(cfg.Services))

	srv := &Server{
		apiKey:  *apiKey,
		baseURL: *baseURL,
		config:  cfg,
		cache:   NewCache(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(srv.HandlePreRequest))
	mux.HandleFunc("/v1/services/", srv.AuthMiddleware(srv.HandleServiceProxy))

	slog.Info("starting server", "addr", *addr, "base_url", *baseURL)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
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
