package core

import (
	"github.com/fudomunro/aegis/internal/shared"
)

// UpstreamBaseFor returns the configured endpoint for a service, or fallback.
func (s *Server) UpstreamBaseFor(serviceID, fallback string) string {
	if svc := s.config.GetService(serviceID); svc != nil && svc.Endpoint != "" {
		return shared.TrimRightSlash(svc.Endpoint)
	}
	return fallback
}
