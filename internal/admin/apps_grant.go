package admin

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/admin/apps"
	"github.com/fudomunro/aegis/internal/shared"
)

// csrfTokenPlaceholder is substituted once per GET /v1/apps/authorize render.
const csrfTokenPlaceholder = "{{CSRF_TOKEN}}"

//go:embed web/app_authorize.html
var appAuthorizeHTML string

//go:embed web/third_party_demo.html
var thirdPartyDemoHTML string

// grantCodeTTL bounds how long a consent code may be exchanged for an app token.
// HandleAppAuthorize shows the consent page (GET) or records a grant (POST).
//
// GET  /v1/apps/authorize?client_id=&name=&scopes=&redirect_uri=&state=
// POST /v1/apps/authorize  (authenticated; same params as form or JSON)
//
// POST redirects to redirect_uri with a one-time code (issue #131).
func (h *Handlers) HandleAppAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		if _, _, err := apps.ParseAuthorizeQuery(q); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !apps.IsSafeRedirectURI(q.Get("redirect_uri")) {
			shared.WriteJSONError(w, http.StatusBadRequest, "redirect_uri must be a path on this host")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		token, err := h.Host.IssueCSRFToken()
		if err != nil {
			shared.WriteJSONError(w, http.StatusInternalServerError, "failed to issue CSRF token")
			return
		}
		fmt.Fprint(w, strings.Replace(appAuthorizeHTML, csrfTokenPlaceholder, token, 1))
	case http.MethodPost:
		h.handleAppAuthorizeGrant(w, r)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handlers) handleAppAuthorizeGrant(w http.ResponseWriter, r *http.Request) {
	if h.Apps == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	// Accept form post (from the consent page) or JSON.
	var q url.Values
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		body, ok := shared.ReadBodyLimited(w, r, shared.MaxAdminBodyBytes)
		if !ok {
			return
		}
		var raw map[string]string
		if err := json.Unmarshal(body, &raw); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		q = url.Values{}
		for k, v := range raw {
			q.Set(k, v)
		}
	} else {
		if err := r.ParseForm(); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid form body")
			return
		}
		q = r.Form
		// Form posts are CSRF-prone (simple request, no preflight). The
		// consent page carries a per-render token (issue #182).
		if !h.Host.RequireFormCSRF(w, r, q) {
			return
		}
	}
	req, entries, err := apps.ParseAuthorizeQuery(q)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !apps.IsSafeRedirectURI(req.RedirectURI) {
		shared.WriteJSONError(w, http.StatusBadRequest, "redirect_uri must be a path on this host")
		return
	}
	scopes := apps.ScopesFromEntries(entries)
	if req.Name == "" {
		req.Name = req.ClientID
	}

	// Persist/refresh the app registration and mint a fresh token for this grant.
	ensure := apps.AppCreateRequest{ID: req.ClientID, Name: req.Name, Scopes: scopes}
	if _, ok := h.Apps.Get(req.ClientID); ok {
		name := req.Name
		if err := h.Apps.Enable(req.ClientID); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := h.Apps.Update(req.ClientID, apps.AppUpdateRequest{Name: &name, Scopes: &scopes}); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if _, _, err := h.Apps.Create(ensure); err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if h.Grants == nil {
		h.Grants = apps.NewGrantStore()
	}
	code, err := h.Grants.Issue(apps.GrantCode{
		AppID:      req.ClientID,
		AppName:    req.Name,
		Scopes:     scopes,
		RedirectTo: req.RedirectURI,
		State:      req.State,
	})
	if err != nil {
		shared.WriteJSONError(w, http.StatusInternalServerError, "failed to issue grant code")
		return
	}
	h.Host.InvalidateCaches()
	h.Host.Logger(r, "app_authorize").
		Info("third-party data grant", "app_id", req.ClientID, "scopes", scopes)

	if strings.Contains(ct, "application/json") {
		shared.WriteJSON(w, map[string]any{
			"code":  code,
			"state": req.State,
			"app":   mustAppView(h.Apps, req.ClientID),
		})
		return
	}
	target := apps.RedirectWithCode(req.RedirectURI, code, req.State)
	http.Redirect(w, r, target, http.StatusFound)
}

func mustAppView(store *apps.Store, id string) apps.AppView {
	v, _ := store.Get(id)
	return v
}

// HandleAppToken redeems a one-time grant code for a third-party app token.
//
// POST /v1/apps/token  {"code":"aeg_grant_…"}
func (h *Handlers) HandleAppToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.Apps == nil || h.Grants == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	body, ok := shared.ReadBodyLimited(w, r, shared.MaxAdminBodyBytes)
	if !ok {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "code is required")
		return
	}
	grant, ok := h.Grants.Consume(code)
	if !ok {
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid or expired grant code")
		return
	}
	view, token, err := h.Apps.Rotate(grant.AppID)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	shared.WriteJSON(w, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"app":          view,
		"scopes":       view.Scopes,
	})
}

// HandleAppRevoke lets a third party disconnect itself (issue #131).
// The app token identifies the app; on success the token is destroyed and the
// app is disabled until the owner grants again.
//
// POST /v1/apps/revoke   Authorization: Bearer aeg_app_…
func (h *Handlers) HandleAppRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.Apps == nil {
		shared.WriteJSONError(w, http.StatusServiceUnavailable, "app registry disabled")
		return
	}
	authz := r.Header.Get("Authorization")
	token := ""
	if strings.HasPrefix(authz, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if token == "" || !strings.HasPrefix(token, apps.TokenPrefix) {
		shared.WriteJSONError(w, http.StatusUnauthorized, "app token required")
		return
	}
	id, err := h.Apps.RevokeByToken(token)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.Host.InvalidateCaches()
	h.Host.Logger(r, "app_revoke").
		Info("third-party app disconnected", "app_id", id)
	shared.WriteJSON(w, map[string]any{"status": "revoked", "app_id": id})
}

// HandleThirdPartyDemo serves the hosted-SaaS demo page (issue #131).
// Public like a real third-party marketing/app surface: the consent call
// still requires Aegis owner auth.
//
// GET /demo/third-party
func (h *Handlers) HandleThirdPartyDemo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprint(w, thirdPartyDemoHTML)
}
