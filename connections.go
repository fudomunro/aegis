package main

import "strings"

// connectionIDForService returns the token-store / OAuth connection key for a
// service surface (issue #26). Shared connections use the named connection ID;
// surfaces without a connection keep using the service ID.
func (c *Config) connectionIDForService(serviceID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	svc := c.serviceByID(serviceID)
	if svc == nil {
		return serviceID
	}
	if svc.Connection != "" {
		return svc.Connection
	}
	return serviceID
}

// toOAuth converts a Connection into the OAuthConfig shape used by the
// token-exchange helpers.
func (conn *Connection) toOAuth() *OAuthConfig {
	if conn == nil {
		return nil
	}
	return &OAuthConfig{
		ClientID:     conn.ClientID,
		ClientSecret: conn.ClientSecret,
		AuthURL:      conn.AuthURL,
		TokenURL:     conn.TokenURL,
		Scopes:       conn.Scopes,
	}
}

// OAuthForConnection returns OAuth credentials/endpoints for a connection ID.
func (c *Config) OAuthForConnection(connectionID string) *OAuthConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if conn, ok := c.Connections[connectionID]; ok {
		return &OAuthConfig{
			ClientID:     conn.ClientID,
			ClientSecret: conn.ClientSecret,
			AuthURL:      conn.AuthURL,
			TokenURL:     conn.TokenURL,
			Scopes:       conn.Scopes,
		}
	}
	return nil
}

// ConnectionForService resolves the provider connection used by a service
// surface.
func (c *Config) ConnectionForService(serviceID string) (connectionID string, conn *Connection) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	svc := c.serviceByID(serviceID)
	if svc == nil || svc.Connection == "" {
		return "", nil
	}
	if existing, ok := c.Connections[svc.Connection]; ok {
		cp := existing
		return svc.Connection, &cp
	}
	return "", nil
}

// requiredScopes returns the OAuth scopes a surface needs from its connection.
// Empty means the surface accepts any valid token on that connection.
func (c *Config) requiredScopes(serviceID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	svc := c.serviceByID(serviceID)
	if svc == nil {
		return nil
	}
	return svc.OAuthScopes
}

// loginScopes returns the scope set requested at OAuth login for a connection.
// Prefers the connection's declared union; otherwise unions surface scopes
// that share the connection.
func (c *Config) loginScopes(connectionID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if conn, ok := c.Connections[connectionID]; ok && len(conn.Scopes) > 0 {
		return conn.Scopes
	}

	var out []string
	for i := range c.Services {
		svc := &c.Services[i]
		id := svc.ID
		if svc.Connection != "" {
			id = svc.Connection
		}
		if id != connectionID {
			continue
		}
		out = unionScopes(out, svc.OAuthScopes)
	}
	return out
}

// scopesSatisfy reports whether every required scope appears in granted.
func scopesSatisfy(granted, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make(map[string]bool, len(granted))
	for _, s := range granted {
		have[s] = true
	}
	for _, r := range required {
		if !have[r] {
			return false
		}
	}
	return true
}

func unionScopes(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range b {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func isGoogleAuthURL(authURL string) bool {
	return strings.Contains(authURL, "accounts.google.com")
}
