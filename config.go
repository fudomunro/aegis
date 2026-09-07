package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	mu       sync.RWMutex
	Services []Service `json:"services"`
	path     string
}

// Service represents a named external service (e.g. Spotify, YouTube Music)
// that Guardian can intercept and enrich requests for.
type Service struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Action      string   `json:"action"` // "inject" or "block"
	Endpoint    string   `json:"endpoint,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	CacheTTL    Duration          `json:"cache_ttl"`
	Description string            `json:"description,omitempty"`
}

// Duration wraps time.Duration for JSON marshalling.
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.path = path
	return &cfg, nil
}

func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var fresh Config
	if err := json.Unmarshal(data, &fresh); err != nil {
		return err
	}
	c.Services = fresh.Services
	slog.Info("config reloaded", "path", c.path, "services", len(fresh.Services))
	return nil
}

// FindMatchingServices returns all services whose name or aliases appear
// as substrings in the user message (case-insensitive).
func (c *Config) FindMatchingServices(userMessage string) []Service {
	c.mu.RLock()
	defer c.mu.RUnlock()

	lower := strings.ToLower(userMessage)
	var matched []Service
	for _, svc := range c.Services {
		if matchesService(svc, lower) {
			matched = append(matched, svc)
		}
	}
	return matched
}

func matchesService(svc Service, lowerMessage string) bool {
	if strings.Contains(lowerMessage, strings.ToLower(svc.Name)) {
		return true
	}
	for _, alias := range svc.Aliases {
		if strings.Contains(lowerMessage, strings.ToLower(alias)) {
			return true
		}
	}
	return false
}

// GetService returns the service with the given ID, or nil.
func (c *Config) GetService(id string) *Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.Services {
		if c.Services[i].ID == id {
			return &c.Services[i]
		}
	}
	return nil
}
