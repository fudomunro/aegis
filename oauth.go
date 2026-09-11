package main

import (
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

// HandleAuthLogin initiates the OAuth flow for a service.
// GET /v1/auth/{service_id}/login
func (s *Server) HandleAuthLogin(w http.ResponseWriter, r *http.Request) {
	serviceID := strings.TrimPrefix(r.URL.Path, "/v1/auth/")
	serviceID = strings.TrimSuffix(serviceID, "/login")

	svc := s.config.GetService(serviceID)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured", serviceID))
		return
	}
	if svc.OAuth == nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("service %q has no OAuth configuration", serviceID))
		return
	}

	state, err := generateState()
	if err != nil {
		slog.Error("failed to generate state", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Store state temporarily for validation on callback.
	s.oauthStates.Set(serviceID+":"+state, true, 10*time.Minute)

	callbackURL := fmt.Sprintf("%s/v1/auth/%s/callback", strings.TrimRight(s.baseURL, "/"), serviceID)

	params := url.Values{
		"client_id":     {svc.OAuth.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {callbackURL},
		"scope":         {strings.Join(svc.OAuth.Scopes, " ")},
		"state":         {state},
	}
	// Google only returns a refresh_token when offline access is requested.
	// prompt=consent ensures a refresh token even on re-authorization.
	if strings.Contains(svc.OAuth.AuthURL, "accounts.google.com") {
		params.Set("access_type", "offline")
		params.Set("prompt", "consent")
	}

	authURL := svc.OAuth.AuthURL + "?" + params.Encode()
	slog.Info("redirecting to OAuth provider", "service", serviceID, "auth_url", svc.OAuth.AuthURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// HandleAuthCallback handles the OAuth callback after the user authorizes.
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

	// Validate state.
	stateKey := serviceID + ":" + state
	if !s.oauthStates.Delete(stateKey) {
		writeJSONError(w, http.StatusBadRequest, "invalid or expired state parameter")
		return
	}

	svc := s.config.GetService(serviceID)
	if svc == nil || svc.OAuth == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("service %q not configured for OAuth", serviceID))
		return
	}

	callbackURL := fmt.Sprintf("%s/v1/auth/%s/callback", strings.TrimRight(s.baseURL, "/"), serviceID)

	// Exchange code for tokens.
	tok, err := s.exchangeCode(svc.OAuth, code, callbackURL)
	if err != nil {
		slog.Error("token exchange failed", "service", serviceID, "error", err)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange failed: %v", err))
		return
	}

	if err := s.tokens.Save(serviceID, tok); err != nil {
		slog.Error("failed to store token", "service", serviceID, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to store token")
		return
	}

	slog.Info("OAuth authorization complete", "service", serviceID, "expiry", tok.Expiry)

	// Return a simple success page.
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Guardian — Authorized</title>
<style>body{font-family:system-ui;max-width:500px;margin:4rem auto;text-align:center;color:#e6edf3;background:#0d1117}
h1{color:#3fb950}p{color:#8b949e}</style></head>
<body><h1>%s Authorized</h1>
<p>Guardian now has access to %s. You can close this page.</p>
<p style="font-size:0.85rem;color:#8b949e">Token expires: %s</p>
</body></html>`,
		svc.Name, svc.Name, tok.Expiry.Format(time.RFC3339))
}

// HandleAuthStatus returns the OAuth status for all services.
// GET /v1/auth/status
func (s *Server) HandleAuthStatus(w http.ResponseWriter, r *http.Request) {
	status := s.tokens.Status()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// HandleAuthRevoke removes stored tokens for a service.
// DELETE /v1/auth/{service_id}
func (s *Server) HandleAuthRevoke(w http.ResponseWriter, r *http.Request) {
	serviceID := strings.TrimPrefix(r.URL.Path, "/v1/auth/")

	if err := s.tokens.Delete(serviceID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to revoke token")
		return
	}

	slog.Info("token revoked", "service", serviceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked", "service": serviceID})
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

// RefreshToken refreshes an expired access token using the refresh token.
func (s *Server) RefreshToken(serviceID string) (*OAuthToken, error) {
	tok := s.tokens.Get(serviceID)
	if tok == nil || tok.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token for %s", serviceID)
	}

	svc := s.config.GetService(serviceID)
	if svc == nil || svc.OAuth == nil {
		return nil, fmt.Errorf("service %s not configured for OAuth", serviceID)
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {svc.OAuth.ClientID},
		"client_secret": {svc.OAuth.ClientSecret},
	}

	newTok, err := s.tokenRequest(svc.OAuth.TokenURL, data)
	if err != nil {
		return nil, err
	}

	// Preserve the refresh token if the server didn't issue a new one.
	if newTok.RefreshToken == "" {
		newTok.RefreshToken = tok.RefreshToken
	}

	if err := s.tokens.Save(serviceID, newTok); err != nil {
		return nil, fmt.Errorf("storing refreshed token: %w", err)
	}

	slog.Info("token refreshed", "service", serviceID, "expiry", newTok.Expiry)
	return newTok, nil
}

// tokenRequest performs the HTTP POST to a token endpoint and parses the response.
func (s *Server) tokenRequest(tokenURL string, data url.Values) (*OAuthToken, error) {
	resp, err := http.PostForm(tokenURL, data)
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
