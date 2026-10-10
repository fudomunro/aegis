package main

import (
	"log/slog"
	"os"
	"strings"
)

// Secret indirection (issue #102). Config values that hold credentials may
// reference the environment instead of embedding the secret:
//
//	"client_secret": "$ENV:SPOTIFY_CLIENT_SECRET"
//	"client_secret": "${SPOTIFY_CLIENT_SECRET}"
//
// Literals without those forms are used as-is. Command substitution is
// intentionally unsupported — config must not execute processes.
//
// Resolution runs at config load/reload. A missing or empty target logs a
// warning and yields an empty secret so the operator sees the gap at startup
// instead of failing later with a blank credential.

// resolveSecret expands one config secret. field is used only for logs
// (e.g. "connections.spotify.client_secret").
func resolveSecret(field, value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return value
	}
	name, ok := secretRefName(raw)
	if !ok {
		return value
	}
	resolved, ok := os.LookupEnv(name)
	if !ok {
		slog.Warn("secret indirection env var is not set",
			"field", field,
			"env", name,
		)
		return ""
	}
	if resolved == "" {
		slog.Warn("secret indirection env var is empty",
			"field", field,
			"env", name,
		)
		return ""
	}
	slog.Debug("resolved secret from env", "field", field, "env", name)
	return resolved
}

// secretRefName reports the environment variable named by an indirection
// reference ($ENV:NAME or ${NAME}). ok is false for literals and empty names.
func secretRefName(raw string) (name string, ok bool) {
	if after, found := strings.CutPrefix(raw, "$ENV:"); found {
		name = strings.TrimSpace(after)
		return name, name != ""
	}
	if strings.HasPrefix(raw, "${") && strings.HasSuffix(raw, "}") {
		name = strings.TrimSpace(raw[2 : len(raw)-1])
		return name, name != ""
	}
	return "", false
}

// resolveConfigSecrets expands secret indirection on connection client
// secrets and service header values after JSON load.
func resolveConfigSecrets(cfg *Config) {
	if cfg == nil {
		return
	}
	for id, conn := range cfg.Connections {
		if conn.ClientSecret == "" {
			continue
		}
		conn.ClientSecret = resolveSecret("connections."+id+".client_secret", conn.ClientSecret)
		cfg.Connections[id] = conn
	}
	for i := range cfg.Services {
		svc := &cfg.Services[i]
		if len(svc.Headers) == 0 {
			continue
		}
		for k, v := range svc.Headers {
			svc.Headers[k] = resolveSecret("services."+svc.ID+".headers."+k, v)
		}
	}
}

// resolveServiceConnectionSecrets expands secret indirection on admin overlay
// header values (service_connections.json may hand-edit $ENV: refs).
func resolveServiceConnectionSecrets(services map[string]serviceConnectionEntry) {
	for id, entry := range services {
		if len(entry.Headers) == 0 {
			continue
		}
		for k, v := range entry.Headers {
			entry.Headers[k] = resolveSecret("service_connections."+id+".headers."+k, v)
		}
		services[id] = entry
	}
}
