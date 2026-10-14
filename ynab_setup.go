package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// YNAB admin setup (issue #141). Saves a Personal Access Token into
// service_connections.json (never rewrites aegis.json) so operators can
// connect YNAB from the admin UI without hand-editing config.

// YNABSetupStatus is GET /v1/admin/ynab/setup.
type YNABSetupStatus struct {
	ServiceID      string   `json:"service_id"`
	DisplayName    string   `json:"display_name"`
	Endpoint       string   `json:"endpoint"`
	Configured     bool     `json:"configured"`
	HasToken       bool     `json:"has_token"`
	TokenSource    string   `json:"token_source,omitempty"` // "header" | "oauth" | ""
	BudgetCount    int      `json:"budget_count,omitempty"`
	ProbeStatus    string   `json:"probe_status,omitempty"` // ok | unauthorized | error | unchecked
	ProbeDetail    string   `json:"probe_detail,omitempty"`
	LibraryReady   bool     `json:"library_ready"`
	SampleQuestion string   `json:"sample_question,omitempty"`
	Hints          []string `json:"hints,omitempty"`
}

// YNABConfigRequest is POST /v1/admin/ynab/config.
type YNABConfigRequest struct {
	Endpoint string `json:"endpoint,omitempty"`
	Token    string `json:"token,omitempty"` // Personal Access Token
}

// YNABTestResult is POST /v1/admin/ynab/test.
type YNABTestResult struct {
	OK          bool   `json:"ok"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	BudgetCount int    `json:"budget_count,omitempty"`
}

// HandleAdminYNABSetup serves GET /v1/admin/ynab/setup and POST sub-actions.
func (s *Server) HandleAdminYNABSetup(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/ynab"), "/")
	switch {
	case path == "" || path == "setup" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeYNABStatus(w, r)
	case path == "config":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleYNABConfig(w, r)
	case path == "test":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleYNABTest(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) ynabEndpoint() string {
	svc := s.config.GetService(connectors.YNABServiceID)
	if svc == nil || strings.TrimSpace(svc.Endpoint) == "" {
		return "https://api.ynab.com/v1"
	}
	return strings.TrimRight(svc.Endpoint, "/")
}

func (s *Server) ynabHasToken() (bool, string) {
	svc := s.config.GetService(connectors.YNABServiceID)
	if svc != nil {
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-YNAB-Token") {
				if strings.TrimSpace(v) != "" {
					return true, "header"
				}
			}
		}
	}
	if s.GetValidToken(connectors.YNABServiceID, slog.Default()) != "" {
		return true, "oauth"
	}
	return false, ""
}

func (s *Server) ynabSetupStatus(withProbe bool) YNABSetupStatus {
	svc := s.config.GetService(connectors.YNABServiceID)
	status := YNABSetupStatus{
		ServiceID:      connectors.YNABServiceID,
		DisplayName:    connectors.YNABServiceName,
		Endpoint:       s.ynabEndpoint(),
		SampleQuestion: "did we go over on groceries this month?",
	}
	if svc != nil {
		status.Configured = true
		if strings.TrimSpace(svc.Name) != "" {
			status.DisplayName = svc.Name
		}
	}
	has, src := s.ynabHasToken()
	status.HasToken = has
	status.TokenSource = src
	status.LibraryReady = has && LibraryKindByID("transactions") != nil &&
		s.hasProvider[types.TransactionProvider]() && s.libraryReadableFor("transactions")

	if !status.Configured {
		status.Hints = append(status.Hints, "Add a ynab service (this page can save it for you).")
	}
	if !has {
		status.Hints = append(status.Hints,
			"Create a Personal Access Token in YNAB → Account Settings → Developer Settings, then paste it below.")
	}
	if withProbe && has {
		res := s.probeYNAB(rContextOrBackground())
		status.ProbeStatus = res.Status
		status.ProbeDetail = res.Detail
		status.BudgetCount = res.BudgetCount
		if res.OK {
			status.Hints = append(status.Hints,
				fmt.Sprintf("Connected — %d budget(s) visible. Try GET /v1/library/transactions.", res.BudgetCount))
		}
	} else if withProbe {
		status.ProbeStatus = "unchecked"
	}
	return status
}

func rContextOrBackground() context.Context { return context.Background() }

func (s *Server) writeYNABStatus(w http.ResponseWriter, r *http.Request) {
	status := s.ynabSetupStatus(true)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleYNABConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req YNABConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.ynab.com/v1"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		writeJSONError(w, http.StatusBadRequest, "endpoint must be an http(s) URL")
		return
	}

	entry := serviceConnectionEntry{
		Name:        "YNAB",
		Action:      "inject",
		Endpoint:    strings.TrimRight(endpoint, "/"),
		CacheTTL:    "5m",
		Description: "Live YNAB budget transactions for /v1/library/transactions. Amounts map from milliunits to signed decimal strings.",
		Aliases:     []string{"ynab", "you need a budget", "budget"},
		Headers:     map[string]string{},
	}
	if tok := strings.TrimSpace(req.Token); tok != "" {
		// Personal Access Token. Agents never see it; Aegis injects Bearer.
		if strings.HasPrefix(strings.ToLower(tok), "bearer ") {
			tok = strings.TrimSpace(tok[len("bearer "):])
		}
		entry.Headers["Authorization"] = "Bearer " + tok
	}
	if err := s.config.SetServiceConnection(connectors.YNABServiceID, entry); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.cache.Clear()
	slog.Info("ynab connection saved from admin UI", "endpoint", entry.Endpoint, "token_set", req.Token != "")

	// Probe immediately so the operator sees success/failure without another click.
	status := s.ynabSetupStatus(true)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) probeYNAB(ctx context.Context) YNABTestResult {
	c := connectors.NewYNABConnector(s)
	var out connectors.BudgetsResponse
	err := c.APIGet(ctx, slog.With("handler", "admin_ynab"), "/budgets", nil, &out)
	switch {
	case err == types.ErrUnauthorized:
		return YNABTestResult{OK: false, Status: "unauthorized", Detail: "YNAB rejected the token — check the Personal Access Token"}
	case err != nil:
		return YNABTestResult{OK: false, Status: "error", Detail: err.Error()}
	}
	n := len(out.Data.Budgets)
	return YNABTestResult{
		OK:          true,
		Status:      "ok",
		Detail:      fmt.Sprintf("YNAB API reachable — %d budget(s)", n),
		BudgetCount: n,
	}
}

func (s *Server) handleYNABTest(w http.ResponseWriter, r *http.Request) {
	has, _ := s.ynabHasToken()
	if !has {
		writeJSON(w, YNABTestResult{OK: false, Status: "unauthorized", Detail: "no YNAB token configured"})
		return
	}
	writeJSON(w, s.probeYNAB(r.Context()))
}

// Quicken admin import status / run (issue #141).
// QuickenImportStatus is GET /v1/admin/quicken/status.
type QuickenImportStatus struct {
	Enabled    bool     `json:"enabled"`
	Path       string   `json:"path,omitempty"`
	Count      int      `json:"count"`
	StoreEmpty bool     `json:"store_empty"`
	LibraryURL string   `json:"library_url,omitempty"`
	SamplePath string   `json:"sample_hint,omitempty"`
	Hints      []string `json:"hints,omitempty"`
}

// QuickenImportRequest is POST /v1/admin/quicken/import.
type QuickenImportRequest struct {
	Path string `json:"path"`
}

// HandleAdminQuicken serves /v1/admin/quicken/*.
func (s *Server) HandleAdminQuicken(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/quicken"), "/")
	switch {
	case path == "" || path == "status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeQuickenStatus(w)
	case path == "import":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleQuickenImport(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) quickenStatus() QuickenImportStatus {
	store := s.quicken
	st := QuickenImportStatus{
		Enabled:    store != nil && store.Enabled(),
		SamplePath: "~/Downloads/Quicken (QIF / QFX / OFX file or directory)",
	}
	if st.Enabled {
		st.Path = store.Path()
		if n, err := store.Count(); err == nil {
			st.Count = n
		}
		st.StoreEmpty = st.Count == 0
	}
	root := strings.TrimRight(s.baseURL, "/")
	st.LibraryURL = root + "/v1/library/transactions"
	if !st.Enabled {
		st.Hints = append(st.Hints, "Quicken store is disabled — restart with -quicken-store <path> (default under the data dir).")
	} else if st.StoreEmpty {
		st.Hints = append(st.Hints,
			"Export QIF or QFX from Quicken (File → Export), then import the file or its folder below.",
			"Re-import is safe: rows already stored (by FITID) are skipped.")
	} else {
		st.Hints = append(st.Hints,
			fmt.Sprintf("%d line item(s) imported. Agents can query them via %s.", st.Count, st.LibraryURL))
	}
	return st
}

func (s *Server) writeQuickenStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.quickenStatus())
}

func (s *Server) handleQuickenImport(w http.ResponseWriter, r *http.Request) {
	store := s.quicken
	if store == nil || !store.Enabled() {
		writeJSONError(w, http.StatusBadRequest, "quicken store disabled — set -quicken-store")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req QuickenImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required — a QIF/QFX file or directory")
		return
	}
	res, err := ImportQuickenPath(store, req.Path)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Library cache must not serve the old empty list.
	s.cache.Clear()
	slog.Info("quicken import from admin UI",
		"path", res.Path, "scanned", res.Scanned, "stored", res.Stored,
		"skipped", res.Skipped, "errors", res.Errors)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"result": res,
		"status": s.quickenStatus(),
	})
}
