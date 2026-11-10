package core

import (
	"github.com/fudomunro/aegis/internal/core/hookhealth"
)

// CheckApertureConfig validates a pasted Aperture policy/hooks JSON document
// against the live Aegis base URL (thin *Server wrapper over the hookhealth
// engine).
func (s *Server) CheckApertureConfig(raw []byte) hookhealth.HookHealthReport {
	return s.hookHealth.CheckApertureConfig(raw, s.baseURL)
}
