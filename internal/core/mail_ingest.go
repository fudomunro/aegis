package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
)

// Mail ingest plumbing (issue #286; docs/mail-ingest.md): core owns the
// archive store and hands ingests a narrow write surface; connectors own
// the walk and the provider semantics.

// wireMailIngests registers the mail ingest sources beside the connector
// map. The Gmail and Microsoft Mail entries are the connectors themselves
// (they implement connectors.MailIngest); Takeout and the protocol mail
// transports (IMAP, POP3 — issue #420) are ingest-only — sync locations,
// not agent-callable services.
func (s *Server) wireMailIngests() {
	s.mailIngests = make(map[string]connectors.MailIngest)
	if c, ok := s.connectors[connectors.GoogleGmailServiceID]; ok {
		if m, ok := c.(connectors.MailIngest); ok {
			s.mailIngests[connectors.GoogleGmailServiceID] = m
		}
	}
	if c, ok := s.connectors[connectors.MicrosoftMailServiceID]; ok {
		if m, ok := c.(connectors.MailIngest); ok {
			s.mailIngests[connectors.MicrosoftMailServiceID] = m
		}
	}
	s.mailIngests[connectors.TakeoutServiceID] = connectors.NewTakeoutIngest()
	s.mailIngests[connectors.ImapMailServiceID] = connectors.NewImapMailConnector(s)
	s.mailIngests[connectors.Pop3MailServiceID] = connectors.NewPop3MailConnector(s)
}

// mailIngestFor returns the ingest registered for a service id, or nil.
func (s *Server) mailIngestFor(service string) connectors.MailIngest {
	return s.mailIngests[service]
}
