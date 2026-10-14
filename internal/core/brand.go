package core

import (
	"embed"
	"io/fs"
	"net/http"
	"strconv"
)

// Brand marks for favicon / logo (issue #87). Production derivatives live
// under web/; aegis_logo.svg at the repo root is the source lineart.
//
//go:embed web/favicon.ico web/favicon.svg web/favicon-16.png web/favicon-32.png web/apple-touch-icon.png web/logo.svg
var brandFS embed.FS

// brandAsset is static image metadata for public (unauthenticated) serving.
type brandAsset struct {
	name        string
	contentType string
}

// brandAssets maps request paths (root-relative) to embedded files.
// Browsers request /favicon.ico from the origin root, so keep these absolute.
var brandAssets = map[string]brandAsset{
	"/favicon.ico":          {"web/favicon.ico", "image/x-icon"},
	"/favicon.svg":          {"web/favicon.svg", "image/svg+xml"},
	"/favicon-16.png":       {"web/favicon-16.png", "image/png"},
	"/favicon-32.png":       {"web/favicon-32.png", "image/png"},
	"/apple-touch-icon.png": {"web/apple-touch-icon.png", "image/png"},
	"/logo.svg":             {"web/logo.svg", "image/svg+xml"},
}

// HandleBrandAsset serves a public favicon/logo image. No auth: browsers
// request these without credentials, and the files contain no secrets.
func (s *Server) HandleBrandAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	asset, ok := brandAssets[r.URL.Path]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown brand asset")
		return
	}
	data, err := fs.ReadFile(brandFS, asset.name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "brand asset missing")
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}

// registerBrandRoutes wires favicon/logo paths onto the mux.
func registerBrandRoutes(mux *http.ServeMux, s *Server) {
	for path := range brandAssets {
		mux.HandleFunc(path, s.HandleBrandAsset)
	}
}
