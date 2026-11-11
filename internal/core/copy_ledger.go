package core

import (
	"github.com/fudomunro/aegis/internal/kinds"
)

// copyInfoFor looks up a stamp for one service-local location. Returns nil
// when disabled or unknown — library responses stay unchanged (fail-open).
func (s *Server) copyInfoFor(kind, service, externalID string) *kinds.CopyInfo {
	if s == nil || s.copyLedger == nil || service == "" || externalID == "" {
		return nil
	}
	rec, ok := s.copyLedger.Get(kind, service, externalID)
	if !ok {
		return nil
	}
	return rec.ToCopyInfo()
}
