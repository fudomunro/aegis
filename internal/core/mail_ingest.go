package core

import (
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// Mail ingest plumbing (issue #286; docs/mail-ingest.md): core owns the
// archive store and hands ingests a narrow write surface; connectors own
// the walk and the provider semantics.

// mailSink is core's connectors.MailSink over the local archive. Connectors
// never see the archive type or its SQL.
type mailSink struct{ a *MailArchive }

func (s mailSink) HasMessage(service string, msg types.Message) (bool, error) {
	return s.a.hasSourceRecord(RecordFromMessage(service, "", msg, nil))
}

func (s mailSink) Store(service string, msg types.Message, rawPayload []byte) error {
	gz, err := GzipPayload(rawPayload)
	if err != nil {
		return err
	}
	return s.a.Upsert(RecordFromMessage(service, "", msg, gz))
}

func (s mailSink) LoadCursor(service string) (types.MailIngestCursor, error) {
	return s.a.LoadSyncCursor(service)
}

func (s mailSink) SaveCursor(service string, cur types.MailIngestCursor) error {
	return s.a.SaveSyncCursor(service, cur)
}

func (s mailSink) MetaGet(key string) (string, error) { return s.a.getMeta(key) }

func (s mailSink) MetaSet(key, value string) error { return s.a.setMeta(key, value) }

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
