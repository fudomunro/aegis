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

	"github.com/fudomunro/aegis/internal/types"
)

// udtAdminStatus is the GET /v1/admin/udt/status payload (issue #15).
type udtAdminStatus struct {
	Enabled    bool           `json:"enabled"`
	Overlay    string         `json:"overlay,omitempty"`
	UserTypes  string         `json:"user_types_dir,omitempty"`
	Kinds      []udtAdminKind `json:"kinds"`
	Hints      []string       `json:"hints,omitempty"`
	LibraryURL string         `json:"library_url,omitempty"`
}

type udtAdminKind struct {
	ID          string                            `json:"id"`
	Label       string                            `json:"label"`
	Path        string                            `json:"path"`
	LibraryURL  string                            `json:"library_url,omitempty"`
	Default     string                            `json:"default_access,omitempty"`
	Fields      map[string]types.UserDefinedField `json:"fields,omitempty"`
	FileSources []udtAdminFileSource              `json:"file_sources,omitempty"`
	ExtraFiles  []UDTExtraFile                    `json:"extra_files,omitempty"`
	SourceCount int                               `json:"source_count"`
}

type udtAdminFileSource struct {
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

type udtBrowseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

type udtBrowseResult struct {
	Path    string           `json:"path"`
	Parent  string           `json:"parent,omitempty"`
	Entries []udtBrowseEntry `json:"entries"`
	Hints   []string         `json:"hints,omitempty"`
}

type udtSourceUpdateRequest struct {
	// Paths selects one or more local JSON files for a schema file_json source.
	Paths []string `json:"paths,omitempty"`
	// Clear removes the overlay selection for the source in the URL.
	Clear bool `json:"clear,omitempty"`
}

type udtExtraRequest struct {
	Extra *UDTExtraFile `json:"extra,omitempty"`
	Clear bool          `json:"clear,omitempty"`
}

// HandleAdminUDT serves /v1/admin/udt/* — local file selection for
// user-defined types (issue #15). Registered under AuthMiddleware.
func (s *Server) HandleAdminUDT(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/udt"), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeUDTStatus(w)
	case path == "browse":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleUDTBrowse(w, r)
	case strings.HasPrefix(path, "kinds/"):
		s.handleUDTKindRoutes(w, r, strings.TrimPrefix(path, "kinds/"))
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) udtOverlayPath() string {
	if s == nil || s.udts == nil {
		return ""
	}
	return s.udts.overlayPath
}

func (s *Server) udtOverlay() (*UserTypesOverlay, error) {
	return loadUserTypesOverlay(s.udtOverlayPath())
}

func (s *Server) writeUDTStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.udtStatus())
}

func (s *Server) udtStatus() udtAdminStatus {
	st := udtAdminStatus{
		Enabled:   s.udts != nil && len(s.udts.All()) > 0,
		Overlay:   s.udtOverlayPath(),
	}
	if s.udts != nil {
		st.UserTypes = s.udts.baseDir
	}
	root := strings.TrimRight(s.baseURL, "/")
	if st.UserTypes == "" {
		st.Hints = append(st.Hints,
			"No user-types directory loaded. Copy a schema pack into <data-dir>/user-types/ (or set AEGIS_USER_TYPES_DIR) and restart.")
	}
	ov, err := s.udtOverlay()
	if err != nil {
		st.Hints = append(st.Hints, "Overlay error: "+err.Error())
		ov = &UserTypesOverlay{Kinds: map[string]UDTKindOverlay{}}
	}
	if s.udts == nil {
		return st
	}
	for _, schema := range s.udts.All() {
		kind := udtAdminKind{
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
			fs := udtAdminFileSource{
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

// handleUDTBrowse lists a directory so the admin UI can offer file selection.
// GET /v1/admin/udt/browse?path=
func (s *Server) handleUDTBrowse(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		if home, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(home, "Downloads")
		} else {
			raw = "."
		}
	}
	dir := expandUserPath(raw)
	if !filepath.IsAbs(dir) {
		writeJSONError(w, http.StatusBadRequest, "path must be absolute (or ~)")
		return
	}
	dir = filepath.Clean(dir)
	st, err := os.Stat(dir)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("cannot read %s: %v", dir, err))
		return
	}
	if !st.IsDir() {
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("cannot list %s: %v", dir, err))
		return
	}
	res := udtBrowseResult{Path: dir}
	if parent := filepath.Dir(dir); parent != dir {
		res.Parent = parent
	}
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		item := udtBrowseEntry{Name: name, Path: filepath.Join(dir, name), IsDir: ent.IsDir()}
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

// handleUDTKindRoutes dispatches:
//
//	PUT    /v1/admin/udt/kinds/{kind}/sources/{id}   {paths:[…]} or {clear:true}
//	DELETE /v1/admin/udt/kinds/{kind}/sources/{id}
//	POST   /v1/admin/udt/kinds/{kind}/extra          {extra:{…}}
//	DELETE /v1/admin/udt/kinds/{kind}/extra/{id}
func (s *Server) handleUDTKindRoutes(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	kindID := parts[0]
	switch parts[1] {
	case "sources":
		sourceID := ""
		if len(parts) >= 3 {
			sourceID = parts[2]
		}
		s.handleUDTSourceUpdate(w, r, kindID, sourceID)
	case "extra":
		extraID := ""
		if len(parts) >= 3 {
			extraID = parts[2]
		}
		s.handleUDTExtraUpdate(w, r, kindID, extraID)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) handleUDTSourceUpdate(w http.ResponseWriter, r *http.Request, kindID, sourceID string) {
	if sourceID == "" {
		writeJSONError(w, http.StatusBadRequest, "source id is required")
		return
	}
	var req udtSourceUpdateRequest
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	case http.MethodDelete:
		req.Clear = true
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.applyUDTFileSource(w, kindID, sourceID, req)
}

func (s *Server) handleUDTExtraUpdate(w http.ResponseWriter, r *http.Request, kindID, extraID string) {
	var req udtExtraRequest
	switch r.Method {
	case http.MethodPost, http.MethodPut:
		body, ok := readBodyLimited(w, r, maxAdminBodyBytes)
		if !ok {
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Extra != nil && extraID != "" {
			req.Extra.ID = extraID
		}
	case http.MethodDelete:
		req.Clear = true
		if req.Extra == nil {
			req.Extra = &UDTExtraFile{ID: extraID}
		}
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.applyUDTExtra(w, kindID, req)
}

func (s *Server) loadOrInitKindOverlay(kindID string) (*UserTypesOverlay, UDTKindOverlay, error) {
	ov := s.udts.Overlay()
	// Copy so concurrent writers do not share maps until save.
	cp := &UserTypesOverlay{Version: ov.Version, Kinds: map[string]UDTKindOverlay{}}
	for k, v := range ov.Kinds {
		cp.Kinds[k] = v
	}
	kindOV := cp.Kinds[kindID]
	if kindOV.FileSources == nil {
		kindOV.FileSources = map[string]UDTFileSourceOverlay{}
	}
	return cp, kindOV, nil
}

// saveUDTOverlayLocked writes user_types.json and refreshes the cache.
// Caller must hold s.udts.overlayMu.
func (s *Server) saveUDTOverlayLocked(ov *UserTypesOverlay) error {
	if s.udts == nil {
		return fmt.Errorf("user-defined types not loaded")
	}
	if err := saveUserTypesOverlay(s.udtOverlayPath(), ov); err != nil {
		return err
	}
	s.udts.SetOverlay(ov)
	return nil
}

func (s *Server) applyUDTFileSource(w http.ResponseWriter, kindID, sourceID string, req udtSourceUpdateRequest) {
	if s.udts == nil {
		writeJSONError(w, http.StatusBadRequest, "user-defined types not loaded")
		return
	}
	schema, ok := s.udts.Get(kindID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown user-defined kind")
		return
	}
	s.udts.overlayMu.Lock()
	defer s.udts.overlayMu.Unlock()
	ov, kindOV, err := s.loadOrInitKindOverlay(kindID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
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
			writeJSONError(w, http.StatusNotFound, "unknown file source for kind "+kindID)
			return
		}
		paths, perr := resolveUDTFilePaths(req.Paths)
		if perr != nil {
			writeJSONError(w, http.StatusBadRequest, perr.Error())
			return
		}
		kindOV.FileSources[sourceID] = UDTFileSourceOverlay{Paths: paths}
	} else {
		delete(kindOV.FileSources, sourceID)
	}
	ov.Kinds[kindID] = kindOV
	if err := s.saveUDTOverlayLocked(ov); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cache.Clear()
	slog.Info("udt file selection saved",
		"kind", kindID, "source", sourceID, "clear", req.Clear, "paths", req.Paths)
	s.writeUDTStatus(w)
}

func (s *Server) applyUDTExtra(w http.ResponseWriter, kindID string, req udtExtraRequest) {
	if s.udts == nil {
		writeJSONError(w, http.StatusBadRequest, "user-defined types not loaded")
		return
	}
	if _, ok := s.udts.Get(kindID); !ok {
		writeJSONError(w, http.StatusNotFound, "unknown user-defined kind")
		return
	}
	s.udts.overlayMu.Lock()
	defer s.udts.overlayMu.Unlock()
	ov, kindOV, err := s.loadOrInitKindOverlay(kindID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
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
		kept := make([]UDTExtraFile, 0, len(kindOV.ExtraFiles))
		for _, ef := range kindOV.ExtraFiles {
			if ef.ID != id {
				kept = append(kept, ef)
			}
		}
		kindOV.ExtraFiles = kept
	} else {
		paths, perr := resolveUDTFilePaths([]string{req.Extra.Path})
		if perr != nil {
			writeJSONError(w, http.StatusBadRequest, perr.Error())
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
	if err := s.saveUDTOverlayLocked(ov); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cache.Clear()
	slog.Info("udt extra file saved", "kind", kindID, "id", id, "clear", req.Clear)
	s.writeUDTStatus(w)
}
