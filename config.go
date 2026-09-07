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
	mu      sync.RWMutex
	Sources []Source `json:"sources"`
	Rules   []Rule   `json:"rules"`
	path    string
}

type Source struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"` // "api", "file", "database"
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	CacheTTL    Duration          `json:"cache_ttl"`
	Description string            `json:"description,omitempty"`
}

type Rule struct {
	ID          string   `json:"id"`
	Match       Match    `json:"match"`
	Sources     []string `json:"source_ids"`
	Action      string   `json:"action"` // "inject", "block"
	Description string   `json:"description,omitempty"`
}

type Match struct {
	Keywords []string `json:"keywords,omitempty"`
	Models   []string `json:"models,omitempty"`
	Users    []string `json:"users,omitempty"`
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
	c.Sources = fresh.Sources
	c.Rules = fresh.Rules
	slog.Info("config reloaded", "path", c.path, "sources", len(fresh.Sources), "rules", len(fresh.Rules))
	return nil
}

func (c *Config) FindMatchingRules(model, user, userMessage string) []Rule {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var matched []Rule
	for _, r := range c.Rules {
		if r.Match.Models != nil && !contains(r.Match.Models, model) && !contains(r.Match.Models, "*") {
			continue
		}
		if r.Match.Users != nil && !contains(r.Match.Users, user) && !contains(r.Match.Users, "*") {
			continue
		}
		if r.Match.Keywords != nil && !anyKeywordIn(r.Match.Keywords, userMessage) {
			continue
		}
		matched = append(matched, r)
	}
	return matched
}

func (c *Config) GetSource(id string) *Source {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.Sources {
		if c.Sources[i].ID == id {
			return &c.Sources[i]
		}
	}
	return nil
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func anyKeywordIn(keywords []string, text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
