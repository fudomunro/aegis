package mail

import (
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// Sink is core's connectors.MailSink over the local archive. Connectors
// never see the archive type or its SQL.
type Sink struct{ A *Archive }

func (s Sink) HasMessage(service string, msg kinds.Message) (bool, error) {
	return s.A.hasSourceRecord(RecordFromMessage(service, "", msg, nil))
}

func (s Sink) Store(service string, msg kinds.Message, rawPayload []byte) error {
	gz, err := GzipPayload(rawPayload)
	if err != nil {
		return err
	}
	return s.A.Upsert(RecordFromMessage(service, "", msg, gz))
}

func (s Sink) LoadCursor(service string) (types.MailIngestCursor, error) {
	return s.A.LoadSyncCursor(service)
}

func (s Sink) SaveCursor(service string, cur types.MailIngestCursor) error {
	return s.A.SaveSyncCursor(service, cur)
}

func (s Sink) MetaGet(key string) (string, error) { return s.A.GetMeta(key) }

func (s Sink) MetaSet(key, value string) error { return s.A.SetMeta(key, value) }
