package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HandleAuthLogin initiates the OAuth flow for a service surface.
// The surface is resolved to its provider connection (issue #26) so multi-
// surface providers share one token store entry.
// GET /v1/auth/{service_id}/login
func (s *Server) HandleAuthLogin(w http.ResponseWriter, r *http.Request) {
	serviceID := strings.TrimPrefix(r.URL.Path, "/v1/auth/")
	serviceID = strings.TrimSuffix(serviceID, "/login")

	svc := s.config.GetService(serviceID)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}

	connectionID, conn := s.config.ConnectionForService(serviceID)
	if connectionID == "" || conn == nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("service %q has no OAuth configuration", serviceID))
		return
	}

	state, err := generateState()
	if err != nil {
		slog.Error("failed to generate state", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Store state temporarily for validation on callback. Key stays the
	// service id so existing per-surface callback URIs remain valid.
	s.oauthStates.Set(serviceID+":"+state, true, 10*time.Minute)

	callbackURL := fmt.Sprintf("%s/v1/auth/%s/callback", strings.TrimRight(s.baseURL, "/"), serviceID)
	scopes := s.config.loginScopes(connectionID)

	params := url.Values{
		"client_id":     {conn.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {callbackURL},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
	}
	// Google: offline access + keep previously granted scopes so adding a
	// surface later does not drop the others. Only force consent when we
	// still need a refresh token — otherwise incremental grants can keep
	// the existing refresh token.
	if isGoogleAuthURL(conn.AuthURL) {
		params.Set("access_type", "offline")
		params.Set("include_granted_scopes", "true")
		existing := s.tokens.Get(connectionID)
		if existing == nil || existing.RefreshToken == "" {
			params.Set("prompt", "consent")
		}
	}

	authURL := conn.AuthURL + "?" + params.Encode()
	slog.Info("redirecting to OAuth provider",
		"service", serviceID,
		"connection", connectionID,
		"auth_url", conn.AuthURL,
		"scopes", scopes,
	)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// HandleAuthCallback handles the OAuth callback after the user authorizes.
// Tokens are stored under the provider connection ID, not the surface ID.
// GET /v1/auth/{service_id}/callback?code=...&state=...
func (s *Server) HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/auth/")
	serviceID := strings.TrimSuffix(path, "/callback")

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		writeJSONError(w, http.StatusBadRequest, "missing code or state parameter")
		return
	}

	stateKey := serviceID + ":" + state
	if !s.oauthStates.Delete(stateKey) {
		writeJSONError(w, http.StatusBadRequest, "invalid or expired state parameter")
		return
	}

	svc := s.config.GetService(serviceID)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured for OAuth", serviceID))
		return
	}

	connectionID, conn := s.config.ConnectionForService(serviceID)
	if connectionID == "" || conn == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured for OAuth", serviceID))
		return
	}

	callbackURL := fmt.Sprintf("%s/v1/auth/%s/callback", strings.TrimRight(s.baseURL, "/"), serviceID)

	tok, err := s.exchangeCode(conn.toOAuth(), code, callbackURL)
	if err != nil {
		slog.Error("token exchange failed", "service", serviceID, "connection", connectionID, "error", err)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}

	existing := s.tokens.Get(connectionID)
	if existing != nil {
		if tok.RefreshToken == "" {
			tok.RefreshToken = existing.RefreshToken
		}
		// Keep previously recorded scopes if the provider omitted them.
		if len(tok.Scopes) == 0 {
			tok.Scopes = existing.Scopes
		}
	}

	if err := s.tokens.Save(connectionID, tok); err != nil {
		slog.Error("failed to store token", "service", serviceID, "connection", connectionID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to store token")
		return
	}

	slog.Info("OAuth authorization complete",
		"service", serviceID,
		"connection", connectionID,
		"expiry", tok.Expiry,
		"scopes", tok.Scopes,
	)

	displayName := svc.Name
	if connectionID != serviceID {
		displayName = fmt.Sprintf("%s (via %s)", svc.Name, connectionID)
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Guardian — Authorized</title>
<style>body{font-family:system-ui;max-width:500px;margin:4rem auto;text-align:center;color:#e6edf3;background:#0d1117}
h1{color:#3fb950}p{color:#8b949e}</style></head>
<body><h1>%s Authorized</h1>
<p>Guardian now has access to %s. You can close this page.</p>
<p style="font-size:0.85rem;color:#8b949e">Token expires: %s</p>
</body></html>`,
		svc.Name, displayName, tok.Expiry.Format(time.RFC3339))
}

// HandleAuthStatus returns the OAuth status for all stored connections/tokens.
// GET /v1/auth/status
func (s *Server) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	status := s.tokens.Status()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// HandleAuthRevoke removes stored tokens for a service's provider connection.
// DELETE /v1/auth/{service_id}
func (s *Server) HandleAuthRevoke(w http.ResponseWriter, r *http.Request) {
	serviceID := strings.TrimPrefix(r.URL.Path, "/v1/auth/")
	connectionID := s.config.connectionIDForService(serviceID)

	if err := s.tokens.Delete(connectionID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}

	slog.Info("token revoked", "service", serviceID, "connection", connectionID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked", "service": serviceID, "connection": connectionID})
}

// exchangeCode exchanges an authorization code for tokens.
func (s *Server) exchangeCode(oauth *OAuthConfig, code, redirectURI string) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {oauth.ClientID},
		"client_secret": {oauth.ClientSecret},
	}

	return s.tokenRequest(oauth.TokenURL, data)
}

// RefreshToken refreshes an expired access token for a provider connection.
func (s *Server) RefreshToken(connectionID string) (*OAuthToken, error) {
	tok := s.tokens.Get(connectionID)
	if tok == nil || tok.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token for %s", connectionID)
	}

	oauth := s.config.OAuthForConnection(connectionID)
	if oauth == nil {
		return nil, fmt.Errorf("connection %s not configured for OAuth", connectionID)
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {oauth.ClientID},
		"client_secret": {oauth.ClientSecret},
	}

	newTok, err := s.tokenRequest(oauth.TokenURL, data)
	if err != nil {
		return nil, err
	}

	// Preserve the refresh token if the server didn't issue a new one.
	if newTok.RefreshToken == "" {
		newTok.RefreshToken = tok.RefreshToken
	}
	if len(newTok.Scopes) == 0 {
		newTok.Scopes = tok.Scopes
	}

	if err := s.tokens.Save(connectionID, newTok); err != nil {
		return nil, fmt.Errorf("storing refreshed token: %w", err)
	}

	slog.Info("token refreshed", "connection", connectionID, "expiry", newTok.Expiry)
	return newTok, nil
}

// tokenRequest performs the HTTP POST to a token endpoint and parses the response.
func (s *Server) tokenRequest(tokenURL string, data url.Values) (*OAuthToken, error) {
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		tokenURL,
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	var scopes []string
	if tr.Scope != "" {
		scopes = strings.Split(tr.Scope, " ")
	}

	return &OAuthToken{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scopes:       scopes,
	}, nil
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
