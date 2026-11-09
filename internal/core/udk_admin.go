package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/core/udk"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// udkAdminStatus is the GET /v1/admin/udk/status payload (issue #15).
type udkAdminStatus struct {
	Enabled    bool           `json:"enabled"`
	Overlay    string         `json:"overlay,omitempty"`
	UserKinds  string         `json:"user_kinds_dir,omitempty"`
	Kinds      []udkAdminKind `json:"kinds"`
	Hints      []string       `json:"hints,omitempty"`
	LibraryURL string         `json:"library_url,omitempty"`
}

type udkAdminKind struct {
	ID          string                            `json:"id"`
	Label       string                            `json:"label"`
	Path        string                            `json:"path"`
	LibraryURL  string                            `json:"library_url,omitempty"`
	Default     string                            `json:"default_access,omitempty"`
	Fields      map[string]kinds.UserDefinedField `json:"fields,omitempty"`
	FileSources []udkAdminFileSource              `json:"file_sources,omitempty"`
	ExtraFiles  []udk.ExtraFile                   `json:"extra_files,omitempty"`
	SourceCount int                               `json:"source_count"`
}

type udkAdminFileSource struct {
	ID string `json:"id"`
	// Name is the human label for the source.
	Name string `json:"name,omitempty"`
	// SchemaPath is the path declared in the schema pack (may be relative).
	SchemaPath string `json:"schema_path,omitempty"`
	// Paths are admin-selected absolute local files (overlay).
	Paths []string `json:"paths,omitempty"`
	// Selected is true when the overlay has chosen files for this source.
	Selected  bool   `json:"selected"`
	ItemsPath string `json:"items_path,omitempty"`
}

type udkBrowseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

type udkBrowseResult struct {
	Path    string           `json:"path"`
	Parent  string           `json:"parent,omitempty"`
	Entries []udkBrowseEntry `json:"entries"`
	Hints   []string         `json:"hints,omitempty"`
}

type udkSourceUpdateRequest struct {
	// Paths selects one or more local JSON files for a schema file_json source.
	Paths []string `json:"paths,omitempty"`
	// Clear removes the overlay selection for the source in the URL.
	Clear bool `json:"clear,omitempty"`
}

type udkExtraRequest struct {
	Extra *udk.ExtraFile `json:"extra,omitempty"`
	Clear bool           `json:"clear,omitempty"`
}

// HandleAdminUDK serves /v1/admin/udk/* — local file selection for
// user-defined types (issue #15). Registered under AuthMiddleware.
func (s *Server) HandleAdminUDK(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/udk"), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeUDKStatus(w)
	case path == "browse":
		if r.Method != http.MethodGet {
			shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleUDKBrowse(w, r)
	case strings.HasPrefix(path, "kinds/"):
		s.handleUDKKindRoutes(w, r, strings.TrimPrefix(path, "kinds/"))
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) udkOverlayPath() string {
	if s == nil || s.udks == nil {
		return ""
	}
	return s.udks.OverlayPath()
}

func (s *Server) udkOverlay() (*udk.UserKindsOverlay, error) {
	return udk.LoadOverlay(s.udkOverlayPath())
}

func (s *Server) writeUDKStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.udkStatus())
}

func (s *Server) udkStatus() udkAdminStatus {
	st := udkAdminStatus{
		Enabled: s.udks != nil && len(s.udks.All()) > 0,
		Overlay: s.udkOverlayPath(),
	}
	if s.udks != nil {
		st.UserKinds = s.udks.BaseDir()
	}
	root := strings.TrimRight(s.baseURL, "/")
	if st.UserKinds == "" {
		st.Hints = append(st.Hints,
			"No user-types directory loaded. Copy a schema pack into <data-dir>/user-types/ (or set AEGIS_USER_TYPES_DIR) and restart.")
	}
	ov, err := s.udkOverlay()
	if err != nil {
		st.Hints = append(st.Hints, "Overlay error: "+err.Error())
		ov = &udk.UserKindsOverlay{Kinds: map[string]udk.KindOverlay{}}
	}
	if s.udks == nil {
		return st
	}
	for _, schema := range s.udks.All() {
		kind := udkAdminKind{
			ID:      schema.ID,
			Label:   schema.Label,
			Path:    schema.Path,
			Default: schema.DefaultAccess,
			Fields:  schema.Fields,
		}
		kindOV := ov.Kinds[schema.ID]
		for _, src := range schema.Sources {
			kind.SourceCount++
			if src.Kind != "file_json" {
				continue
			}
			fs := udkAdminFileSource{
				ID:         src.ID,
				Name:       src.Name,
				SchemaPath: src.Path,
				ItemsPath:  src.ItemsPath,
			}
			if sel, ok := kindOV.FileSources[src.ID]; ok && len(sel.Paths) > 0 {
				fs.Paths = sel.Paths
				fs.Selected = true
			}
			kind.FileSources = append(kind.FileSources, fs)
		}
		kind.ExtraFiles = append(kind.ExtraFiles, kindOV.ExtraFiles...)
		kind.SourceCount += len(kindOV.ExtraFiles)
		if kind.Path != "" {
			kind.LibraryURL = root + kind.Path
			st.LibraryURL = kind.LibraryURL
		}
		st.Kinds = append(st.Kinds, kind)
	}
	sort.Slice(st.Kinds, func(i, j int) bool { return st.Kinds[i].ID < st.Kinds[j].ID })
	if st.Enabled {
		st.Hints = append(st.Hints,
			"Select local JSON exports for each file source — the schema field_map still applies.",
			"Browse lists .json files and folders on this machine; click a file to fill the path.",
			"Extra files are optional feeds with name-matched fields (or an explicit field_map).",
			"Schema packs are never rewritten — selections save to "+filepath.Base(st.Overlay)+".")
	}
	return st
}

// handleUDKBrowse lists a directory so the admin UI can offer file selection.
// GET /v1/admin/udk/browse?path=
func (s *Server) handleUDKBrowse(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, "Downloads")
		} else {
			raw = "."
		}
	}
	dir := shared.ExpandUserPath(raw)
	if !filepath.IsAbs(dir) {
		shared.WriteJSONError(w, http.StatusBadRequest, "path must be absolute (or ~)")
		return
	}
	dir = filepath.Clean(dir)
	st, err := os.Stat(dir)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("cannot read %s: %v", dir, err))
		return
	}
	if !st.IsDir() {
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, fmt.Sprintf("cannot list %s: %v", dir, err))
		return
	}
	res := udkBrowseResult{Path: dir}
	if parent := filepath.Dir(dir); parent != dir {
		res.Parent = parent
	}
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		item := udkBrowseEntry{Name: name, Path: filepath.Join(dir, name), IsDir: ent.IsDir()}
		if ent.IsDir() {
			res.Entries = append(res.Entries, item)
			continue
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".json") && !strings.HasSuffix(lower, ".geojson") {
			continue
		}
		if info, err := ent.Info(); err == nil {
			item.Size = info.Size()
		}
		res.Entries = append(res.Entries, item)
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		if res.Entries[i].IsDir != res.Entries[j].IsDir {
			return res.Entries[i].IsDir
		}
		return strings.ToLower(res.Entries[i].Name) < strings.ToLower(res.Entries[j].Name)
	})
	if len(res.Entries) == 0 {
		res.Hints = append(res.Hints, "No folders or .json files here. Navigate up or choose another directory.")
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// handleUDKKindRoutes dispatches:
//
//	PUT    /v1/admin/udk/kinds/{kind}/sources/{id}   {paths:[…]} or {clear:true}
//	DELETE /v1/admin/udk/kinds/{kind}/sources/{id}
//	POST   /v1/admin/udk/kinds/{kind}/extra          {extra:{…}}
//	DELETE /v1/admin/udk/kinds/{kind}/extra/{id}
func (s *Server) handleUDKKindRoutes(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	kindID := parts[0]
	switch parts[1] {
	case "sources":
		sourceID := ""
		if len(parts) >= 3 {
			sourceID = parts[2]
		}
		s.handleUDKSourceUpdate(w, r, kindID, sourceID)
	case "extra":
		extraID := ""
		if len(parts) >= 3 {
			extraID = parts[2]
		}
		s.handleUDKExtraUpdate(w, r, kindID, extraID)
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) handleUDKSourceUpdate(w http.ResponseWriter, r *http.Request, kindID, sourceID string) {
	if sourceID == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source id is required")
		return
	}
	var req udkSourceUpdateRequest
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	case http.MethodDelete:
		req.Clear = true
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.applyUDKFileSource(w, kindID, sourceID, req)
}

func (s *Server) handleUDKExtraUpdate(w http.ResponseWriter, r *http.Request, kindID, extraID string) {
	var req udkExtraRequest
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		body, ok := shared.ReadBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Extra != nil && extraID != "" {
			req.Extra.ID = extraID
		}
	case http.MethodDelete:
		req.Clear = true
		if req.Extra == nil {
			req.Extra = &udk.ExtraFile{ID: extraID}
		}
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.applyUDKExtra(w, kindID, req)
}

func (s *Server) loadOrInitKindOverlay(kindID string) (*udk.UserKindsOverlay, udk.KindOverlay, error) {
	ov := s.udks.Overlay()
	// Copy so concurrent writers do not share maps until save.
	cp := &udk.UserKindsOverlay{Version: ov.Version, Kinds: map[string]udk.KindOverlay{}}
	for k, v := range ov.Kinds {
		cp.Kinds[k] = v
	}
	kindOV := cp.Kinds[kindID]
	if kindOV.FileSources == nil {
		kindOV.FileSources = map[string]udk.FileSourceOverlay{}
	}
	return cp, kindOV, nil
}

// saveUDKOverlayLocked writes user_types.json and refreshes the cache.
// Caller must hold s.udks.overlayMu.
func (s *Server) saveUDKOverlayLocked(ov *udk.UserKindsOverlay) error {
	if s.udks == nil {
		return fmt.Errorf("user-defined types not loaded")
	}
	if err := udk.SaveOverlay(s.udkOverlayPath(), ov); err != nil {
		return err
	}
	s.udks.SetOverlay(ov)
	return nil
}

func (s *Server) applyUDKFileSource(w http.ResponseWriter, kindID, sourceID string, req udkSourceUpdateRequest) {
	if s.udks == nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "user-defined types not loaded")
		return
	}
	schema, ok := s.udks.Get(kindID)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown user-defined kind")
		return
	}
	s.udks.LockOverlayWrite()
	defer s.udks.UnlockOverlayWrite()
	ov, kindOV, err := s.loadOrInitKindOverlay(kindID)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !req.Clear {
		var known bool
		for _, src := range schema.Sources {
			if src.ID == sourceID && src.Kind == "file_json" {
				known = true
				break
			}
		}
		if !known {
			shared.WriteJSONError(w, http.StatusNotFound, "unknown file source for kind "+kindID)
			return
		}
		paths, perr := udk.ResolveFilePaths(req.Paths)
		if perr != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, perr.Error())
			return
		}
		kindOV.FileSources[sourceID] = udk.FileSourceOverlay{Paths: paths}
	} else {
		delete(kindOV.FileSources, sourceID)
	}
	ov.Kinds[kindID] = kindOV
	if err := s.saveUDKOverlayLocked(ov); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.invalidateLibraryKind(kindID)
	slog.Info("udk file selection saved",
		"kind", kindID, "source", sourceID, "clear", req.Clear, "paths", req.Paths)
	s.writeUDKStatus(w)
}

func (s *Server) applyUDKExtra(w http.ResponseWriter, kindID string, req udkExtraRequest) {
	if s.udks == nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "user-defined types not loaded")
		return
	}
	if _, ok := s.udks.Get(kindID); !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown user-defined kind")
		return
	}
	s.udks.LockOverlayWrite()
	defer s.udks.UnlockOverlayWrite()
	ov, kindOV, err := s.loadOrInitKindOverlay(kindID)
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := ""
	if req.Extra != nil {
		id = strings.TrimSpace(req.Extra.ID)
	}
	if id == "" {
		// Unique across restarts without relying on a tiny clock modulus.
		id = fmt.Sprintf("extra-%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	if req.Clear || req.Extra == nil || strings.TrimSpace(req.Extra.Path) == "" {
		kept := make([]udk.ExtraFile, 0, len(kindOV.ExtraFiles))
		for _, ef := range kindOV.ExtraFiles {
			if ef.ID != id {
				kept = append(kept, ef)
			}
		}
		kindOV.ExtraFiles = kept
	} else {
		paths, perr := udk.ResolveFilePaths([]string{req.Extra.Path})
		if perr != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, perr.Error())
			return
		}
		ef := *req.Extra
		ef.ID = id
		ef.Path = paths[0]
		if ef.Name == "" {
			ef.Name = filepath.Base(ef.Path)
		}
		replaced := false
		for i := range kindOV.ExtraFiles {
			if kindOV.ExtraFiles[i].ID == id {
				kindOV.ExtraFiles[i] = ef
				replaced = true
				break
			}
		}
		if !replaced {
			kindOV.ExtraFiles = append(kindOV.ExtraFiles, ef)
		}
	}
	ov.Kinds[kindID] = kindOV
	if err := s.saveUDKOverlayLocked(ov); err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.invalidateLibraryKind(kindID)
	slog.Info("udk extra file saved", "kind", kindID, "id", id, "clear", req.Clear)
	s.writeUDKStatus(w)
}
