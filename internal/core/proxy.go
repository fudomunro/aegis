package core

import (
	"log/slog"
	"time"
)

// GetValidToken returns a valid access token for the service's provider
// connection, refreshing if needed. Returns "" when there is no token or the
// connection lacks scopes required by this surface (issue #26).
func (s *Server) GetValidToken(serviceID string, logger *slog.Logger) string {
	if s.tokens == nil {
		return ""
	}

	connectionID := s.config.connectionIDForService(serviceID)
	tok := s.tokens.Get(connectionID)
	if tok == nil {
		return ""
	}

	if !scopesSatisfy(tok.Scopes, s.config.requiredScopes(serviceID)) {
		logger.Warn("OAuth connection missing required scopes for surface",
			"service", serviceID,
			"connection", connectionID,
			"required", s.config.requiredScopes(serviceID),
			"granted", tok.Scopes,
		)
		return ""
	}

	if !tok.IsExpired(30 * time.Second) {
		return tok.AccessToken
	}

	// Token is expired or about to expire — try to refresh.
	if tok.RefreshToken == "" {
		logger.Warn("OAuth token expired, no refresh token", "service", serviceID, "connection", connectionID)
		return ""
	}

	logger.Info("refreshing expired OAuth token", "service", serviceID, "connection", connectionID)
	refreshed, err := s.RefreshToken(connectionID)
	if err != nil {
		logger.Error("token refresh failed", "service", serviceID, "connection", connectionID, "error", err)
		return tok.AccessToken // try with expired token as fallback
	}

	if !scopesSatisfy(refreshed.Scopes, s.config.requiredScopes(serviceID)) {
		return ""
	}

	return refreshed.AccessToken
}
