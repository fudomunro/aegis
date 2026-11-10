package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// dashboardServiceManifestEntry is one row of the services manifest the
// tool carries: the build has no instance config to project page identity
// from.
type dashboardServiceManifestEntry struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	Connection       string `json:"connection,omitempty"`
	FailureInjection bool   `json:"failure_injection,omitempty"`
}

// LoadDashboardServiceMetas reads a services manifest (JSON array of
// {id, name, connection, failure_injection}): the pages to show and which of
// them back onto the drift-injection harness.
func LoadDashboardServiceMetas(path string) ([]DashboardServiceMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []dashboardServiceManifestEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse services manifest: %w", err)
	}
	metas := make([]DashboardServiceMeta, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		metas = append(metas, DashboardServiceMeta(e))
	}
	return metas, nil
}
