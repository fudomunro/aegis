package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Signal bundle input (issue #86, issue #335): the tool reads captured
// signals-*.json files as plain data — the same self-contained files the
// instance writes under <data-dir>/maintenance/ and the scanner reads. The
// shapes below mirror that file format (documented in
// docs/sync-maintenance.md) so the application code owes the tool nothing
// beyond keeping the format; nothing here imports the service.

// SyncCounts is the cumulative sync-queue rollup recorded in a bundle.
type SyncCounts struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
	Paused    int `json:"paused"`
}

// Total is the sum of all state counts.
func (c SyncCounts) Total() int {
	return c.Queued + c.Running + c.Succeeded + c.Failed + c.Canceled + c.Paused
}

// Freshness is one per-kind copy rollup recorded in a bundle.
type Freshness struct {
	Kind        string `json:"kind"`
	InSync      int    `json:"in_sync"`
	Stale       int    `json:"stale"`
	Missing     int    `json:"missing"`
	Unknown     int    `json:"unknown"`
	Unchecked   int    `json:"unchecked"`
	Checked     int    `json:"checked"`
	Total       int    `json:"total"`
	LastChecked string `json:"last_checked_at,omitempty"`
}

// BundleFailure is one recent failed sync job carried in a bundle, with the
// error text the instance scrubbed at capture time.
type BundleFailure struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind,omitempty"`
	JobType    string    `json:"job_type"`
	Service    string    `json:"service,omitempty"`
	State      string    `json:"state"`
	ErrorClass string    `json:"error_class,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// BundleSync is the sync-queue slice of a bundle.
type BundleSync struct {
	Enabled   bool                   `json:"enabled"`
	Counts    SyncCounts             `json:"counts"`
	ByService map[string]SyncCounts  `json:"by_service,omitempty"`
	Failures  []BundleFailure        `json:"failures,omitempty"`
}

// SignalBundle is one captured observation read from a signals-*.json file.
type SignalBundle struct {
	CapturedAt time.Time   `json:"captured_at"`
	Hostname   string      `json:"hostname,omitempty"`
	Version    string      `json:"version"`
	Commit     string      `json:"commit"`
	Sync       *BundleSync `json:"sync,omitempty"`
	Freshness  []Freshness `json:"freshness,omitempty"`
	// sourceName is the file the bundle was read from; ticket observations
	// cite it.
	sourceName string
}

// loadSignalBundles reads every signals-*.json under dir, oldest first. An
// empty directory yields an empty slice — callers decide whether "no
// bundles" is an error (the build's explicit -bundles flag) or a no-op (a
// scan pass). Unknown fields are ignored so the instance may add bundle
// detail without breaking the tool.
func loadSignalBundles(dir string) ([]SignalBundle, error) {
	if dir == "" {
		return nil, fmt.Errorf("empty signal bundle directory")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "signals-*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]SignalBundle, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var sig SignalBundle
		if err := json.Unmarshal(raw, &sig); err != nil {
			return nil, fmt.Errorf("parse signal bundle %s: %w", p, err)
		}
		sig.sourceName = filepath.Base(p)
		out = append(out, sig)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CapturedAt.Before(out[j].CapturedAt)
	})
	return out, nil
}
