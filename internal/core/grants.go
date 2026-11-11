package core

import (
	"net/http"

	"github.com/fudomunro/aegis/internal/shared"
)

// ownerGrants is the owner-tier rule map: the configured data grants
// materialized as kind → level, with the permissive default prefill. The
// legacy "omit = unrestricted" semantics live in that prefill, not in the
// resolution rule, which stays deny-by-default.
func (s *Server) ownerGrants() shared.GrantMap {
	if s == nil || s.config == nil {
		return nil
	}
	return s.config.DataGrants().Levels().PrefillDefault()
}

// requestGrants returns the rule map for the request's key (see Grants).
func (s *Server) requestGrants(r *http.Request) shared.GrantMap {
	g, _ := s.Grants(r)
	return g
}
