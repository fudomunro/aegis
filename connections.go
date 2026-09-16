package main

import "strings"

// connectionIDForService returns the token-store / OAuth connection key for a
// service surface (issue #26). Shared connections use the named connection ID;
// legacy 1:1 services keep using the service ID.
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

// connectionFromOAuth builds an in-memory Connection from a legacy inline
// OAuth block so 1:1 services keep working without a top-level connections map.
func connectionFromOAuth(oauth *OAuthConfig) *Connection {
	if oauth == nil {
		return nil
	}
	return &Connection{
		ClientID:     oauth.ClientID,
		ClientSecret: oauth.ClientSecret,
		AuthURL:      oauth.AuthURL,
		TokenURL:     oauth.TokenURL,
		Scopes:       oauth.Scopes,
	}
}

// OAuthForConnection returns OAuth credentials/endpoints for a connection ID.
// Prefers a top-level connection; falls back to a legacy service whose ID
// matches the connection and still carries inline oauth.
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
	if svc := c.serviceByID(connectionID); svc != nil && svc.OAuth != nil {
		return svc.OAuth
	}
	return nil
}

// ConnectionForService resolves the provider connection used by a service
// surface. Prefers an explicit top-level connection; falls back to the
// service's own ID + inline oauth.
func (c *Config) ConnectionForService(serviceID string) (connectionID string, conn *Connection) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	svc := c.serviceByID(serviceID)
	if svc == nil {
		return "", nil
	}
	if svc.Connection != "" {
		if existing, ok := c.Connections[svc.Connection]; ok {
			cp := existing
			return svc.Connection, &cp
		}
		// Connection id may name a service that still carries inline oauth.
		if other := c.serviceByID(svc.Connection); other != nil && other.OAuth != nil {
			return svc.Connection, connectionFromOAuth(other.OAuth)
		}
	}
	if svc.OAuth != nil {
		return svc.ID, connectionFromOAuth(svc.OAuth)
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
	if len(svc.OAuthScopes) > 0 {
		return svc.OAuthScopes
	}
	if svc.OAuth != nil {
		return svc.OAuth.Scopes
	}
	return nil
}

// loginScopes returns the scope set requested at OAuth login for a connection.
// Prefers the connection's declared union; otherwise unions surface scopes
// that share the connection.
func (c *Config) loginScopes(connectionID, initiatingServiceID string) []string {
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
		switch {
		case len(svc.OAuthScopes) > 0:
			out = unionScopes(out, svc.OAuthScopes)
		case svc.OAuth != nil:
			out = unionScopes(out, svc.OAuth.Scopes)
		}
	}
	if len(out) > 0 {
		return out
	}
	if initiatingServiceID != "" {
		if svc := c.serviceByID(initiatingServiceID); svc != nil && svc.OAuth != nil {
			return svc.OAuth.Scopes
		}
	}
	if svc := c.serviceByID(connectionID); svc != nil && svc.OAuth != nil {
		return svc.OAuth.Scopes
	}
	return nil
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
