package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// userTypesFileVersion is the on-disk schema for user_types.json.
const userTypesFileVersion = 1

// defaultUserTypesOverlayPath returns the overlay path beside aegis.json.
func defaultUserTypesOverlayPath(configPath string) string {
	dir := strings.TrimSpace(configPath)
	if dir == "" {
		return "user_types.json"
	}
	if i := strings.LastIndexAny(dir, `/\`); i >= 0 {
		return dir[:i+1] + "user_types.json"
	}
	return "user_types.json"
}

// UDTFileSourceOverlay points a schema file_json source at local files chosen
// in the admin UI (issue #15). Paths replaces the schema's single Path.
type UDTFileSourceOverlay struct {
	Paths []string `json:"paths"`
}

// UDTExtraFile is an admin-added local JSON file for a kind. When FieldMap is
// empty the mapper name-matches schema fields to top-level JSON keys.
type UDTExtraFile struct {
	ID          string            `json:"id"`
	Name        string            `json:"name,omitempty"`
	Path        string            `json:"path"`
	ItemsPath   string            `json:"items_path,omitempty"`
	FieldMap    map[string]string `json:"field_map,omitempty"`
	IDField     string            `json:"id_field,omitempty"`
	URLTemplate string            `json:"url_template,omitempty"`
}

// UDTKindOverlay is the admin-owned source configuration for one UDT kind.
type UDTKindOverlay struct {
	FileSources map[string]UDTFileSourceOverlay `json:"file_sources,omitempty"`
	ExtraFiles  []UDTExtraFile                  `json:"extra_files,omitempty"`
}

// UserTypesOverlay is the admin-edited overlay (user_types.json). It never
// rewrites schema packs — only local file selection / extra feeds.
type UserTypesOverlay struct {
	Version int                       `json:"version"`
	Kinds   map[string]UDTKindOverlay `json:"kinds"`
}

func loadUserTypesOverlay(path string) (*UserTypesOverlay, error) {
	ov := &UserTypesOverlay{
		Version: userTypesFileVersion,
		Kinds:   map[string]UDTKindOverlay{},
	}
	if strings.TrimSpace(path) == "" {
		return ov, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ov, nil
		}
		return nil, fmt.Errorf("reading user-types overlay: %w", err)
	}
	if err := json.Unmarshal(data, ov); err != nil {
		return nil, fmt.Errorf("parsing user-types overlay: %w", err)
	}
	if ov.Kinds == nil {
		ov.Kinds = map[string]UDTKindOverlay{}
	}
	return ov, nil
}

func saveUserTypesOverlay(path string, ov *UserTypesOverlay) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("user-types overlay has no path; cannot save")
	}
	out := UserTypesOverlay{
		Version: userTypesFileVersion,
		Kinds:   ov.Kinds,
	}
	if out.Kinds == nil {
		out.Kinds = map[string]UDTKindOverlay{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// 0606-equivalent local config; paths are not secrets but keep 0600
	// consistent with other admin overlays.
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

// applyUserTypesOverlay returns a copy of schema with admin-selected local
// file paths and extra files applied. Schema packs stay untouched on disk.
func applyUserTypesOverlay(schema types.UserDefinedTypeSchema, ov UDTKindOverlay) types.UserDefinedTypeSchema {
	out := schema
	out.Sources = append([]types.UserDefinedSourceSpec(nil), schema.Sources...)
	for i := range out.Sources {
		src := &out.Sources[i]
		if src.Kind != "file_json" {
			continue
		}
		if fs, ok := ov.FileSources[src.ID]; ok && len(fs.Paths) > 0 {
			// Multi-file selection: first path is the primary Path; the rest
			// are fetched as additional sources with the same mapping.
			src.Path = expandUserPath(fs.Paths[0])
			for i, extra := range fs.Paths[1:] {
				clone := *src
				// Unique source id (basename alone collides across folders).
				clone.ID = fmt.Sprintf("%s#%d", src.ID, i+2)
				clone.Path = expandUserPath(extra)
				clone.Name = src.Name + " (" + filepath.Base(clone.Path) + ")"
				out.Sources = append(out.Sources, clone)
			}
		}
	}
	for _, ef := range ov.ExtraFiles {
		path := expandUserPath(strings.TrimSpace(ef.Path))
		if path == "" || strings.TrimSpace(ef.ID) == "" {
			continue
		}
		src := types.UserDefinedSourceSpec{
			ID:          ef.ID,
			Name:        ef.Name,
			Kind:        "file_json",
			Path:        path,
			ItemsPath:   ef.ItemsPath,
			FieldMap:    ef.FieldMap,
			IDField:     ef.IDField,
			URLTemplate: ef.URLTemplate,
		}
		if src.Name == "" {
			src.Name = ef.ID
		}
		if src.FieldMap == nil {
			src.FieldMap = nameMatchFieldMap(out.Fields)
		}
		out.Sources = append(out.Sources, src)
	}
	return out
}

// nameMatchFieldMap maps each schema field to a same-named JSON key.
// Used when an admin-added file has no explicit field_map.
func nameMatchFieldMap(fields map[string]types.UserDefinedField) map[string]string {
	out := make(map[string]string, len(fields))
	for name := range fields {
		out[name] = name
	}
	return out
}

// validateHTTPJSONHeaders is kept as a thin wrapper for admin-time checks.
func validateHTTPJSONHeaders(headers map[string]string) error {
	return types.ValidateHTTPJSONHeaders(headers)
}

func resolveUDTFilePaths(paths []string) (out []string, err error) {
	for _, p := range paths {
		p = expandUserPath(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("path must be absolute (or ~): %q", p)
		}
		out = append(out, filepath.Clean(p))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one file path is required")
	}
	return out, nil
}
