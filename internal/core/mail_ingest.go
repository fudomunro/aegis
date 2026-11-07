package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
)

// Mail ingest plumbing (issue #286; docs/mail-ingest.md): core owns the
// archive store and hands ingests a narrow write surface; connectors own
// the walk and the provider semantics.

// wireMailIngests registers the mail ingest sources beside the connector
// map. The Gmail entry is the connector itself (it implements
// connectors.MailIngest); Takeout is ingest-only — a sync location, not an
// agent-callable service.
func (s *Server) wireMailIngests() {
	s.mailIngests = make(map[string]connectors.MailIngest)
	if c, ok := s.connectors[connectors.GoogleGmailServiceID]; ok {
		if m, ok := c.(connectors.MailIngest); ok {
			s.mailIngests[connectors.GoogleGmailServiceID] = m
		}
	}
	s.mailIngests[connectors.TakeoutServiceID] = connectors.NewTakeoutIngest()
}

// mailIngestFor returns the ingest registered for a service id, or nil.
func (s *Server) mailIngestFor(service string) connectors.MailIngest {
	return s.mailIngests[service]
}
