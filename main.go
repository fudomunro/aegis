package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	apiKey := flag.String("api-key", "", "API key for authenticating Aperture hooks (reads APERTURE_GUARDIAN_API_KEY env if empty)")
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

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", *configPath, "sources", len(cfg.Sources), "rules", len(cfg.Rules))

	srv := &Server{
		apiKey: *apiKey,
		config: cfg,
		cache:  NewCache(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pre-request", srv.AuthMiddleware(srv.HandlePreRequest))

	slog.Info("starting server", "addr", *addr)
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
