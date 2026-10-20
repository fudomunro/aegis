package core

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/types"
)

// Contacts and events mirror adapters (issue #146). DAV ↔ Google writes use
// If-Match / etag optimistic concurrency so a newer remote edit is never
// overwritten. Conflicts are reported in MirrorResult instead of picking a
// winner. Sync watermarks (etag / sync-token) land in the copy ledger.

const (
	MirrorKindContacts = "contacts"
	MirrorKindEvents   = "events"
)

// MirrorConflict is one item the mirror refused to overwrite (issue #146).
type MirrorConflict struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	ID      string `json:"id,omitempty"`
	Reason  string `json:"reason"`
}

type mirrorContact struct {
	Name         string
	Emails       []string
	Phones       []string
	Organization string
	SourceID     string
	SourceETag   string
	SourceUID    string
}

type mirrorEvent struct {
	Title       string
	Description string
	StartAt     string
	EndAt       string
	AllDay      bool
	Location    string
	Status      string
	EntityID    string
	SourceID    string
	SourceETag  string
	SourceUID   string
	CalendarID  string
}

type contactMirrorSource interface {
	loadContacts(ctx context.Context) ([]mirrorContact, bool, error)
}

type contactMirrorTarget interface {
	writeContacts(ctx context.Context, contacts []mirrorContact) (copied int, skips []MirrorSkip, conflicts []MirrorConflict, err error)
}

type eventMirrorSource interface {
	loadEvents(ctx context.Context) ([]mirrorEvent, bool, error)
}

type eventMirrorTarget interface {
	writeEvents(ctx context.Context, events []mirrorEvent) (copied int, skips []MirrorSkip, conflicts []MirrorConflict, err error)
}

func (s *Server) contactMirrorSourceFor(serviceID string) (contactMirrorSource, error) {
	switch serviceID {
	case connectors.CardDAVServiceID:
		return &davContactMirror{server: s, serviceID: serviceID}, nil
	case connectors.GoogleContactsServiceID:
		return &googleContactMirror{server: s, serviceID: serviceID}, nil
	}
	return nil, fmt.Errorf("contact mirror source %q is not supported yet (supported: %s, %s)",
		serviceID, connectors.CardDAVServiceID, connectors.GoogleContactsServiceID)
}

func (s *Server) contactMirrorTargetFor(serviceID string) (contactMirrorTarget, error) {
	switch serviceID {
	case connectors.CardDAVServiceID:
		return &davContactMirror{server: s, serviceID: serviceID}, nil
	case connectors.GoogleContactsServiceID:
		return &googleContactMirror{server: s, serviceID: serviceID}, nil
	}
	return nil, fmt.Errorf("contact mirror target %q is not supported yet (supported: %s, %s)",
		serviceID, connectors.CardDAVServiceID, connectors.GoogleContactsServiceID)
}

func (s *Server) eventMirrorSourceFor(serviceID string) (eventMirrorSource, error) {
	switch serviceID {
	case connectors.CalDAVServiceID:
		return &davEventMirror{server: s, serviceID: serviceID}, nil
	case connectors.GoogleCalendarServiceID:
		return &googleEventMirror{server: s, serviceID: serviceID}, nil
	}
	return nil, fmt.Errorf("event mirror source %q is not supported yet (supported: %s, %s)",
		serviceID, connectors.CalDAVServiceID, connectors.GoogleCalendarServiceID)
}

func (s *Server) eventMirrorTargetFor(serviceID string) (eventMirrorTarget, error) {
	switch serviceID {
	case connectors.CalDAVServiceID:
		return &davEventMirror{server: s, serviceID: serviceID}, nil
	case connectors.GoogleCalendarServiceID:
		return &googleEventMirror{server: s, serviceID: serviceID}, nil
	}
	return nil, fmt.Errorf("event mirror target %q is not supported yet (supported: %s, %s)",
		serviceID, connectors.CalDAVServiceID, connectors.GoogleCalendarServiceID)
}

// --- DAV contacts ---

type davContactMirror struct {
	server    *Server
	serviceID string
}

func (m *davContactMirror) conn() *connectors.CardDAVConnector {
	c, _ := m.server.connectors[m.serviceID].(*connectors.CardDAVConnector)
	return c
}

func (m *davContactMirror) loadContacts(ctx context.Context) ([]mirrorContact, bool, error) {
	c := m.conn()
	if c == nil {
		return nil, false, fmt.Errorf("no CardDAV connector for %s", m.serviceID)
	}
	contacts, truncated, err := c.ListUserContacts(ctx)
	if err != nil {
		return nil, truncated, err
	}
	out := make([]mirrorContact, 0, len(contacts))
	for _, ct := range contacts {
		out = append(out, mirrorContact{
			Name:         ct.Name,
			Emails:       ct.Emails,
			Phones:       ct.Phones,
			Organization: ct.Organization,
			SourceID:     firstSourceID(ct.Sources),
			SourceETag:   sourceExtraString(ct.Sources, "etag"),
			SourceUID:    sourceExtraString(ct.Sources, "uid"),
		})
	}
	return out, truncated, nil
}

func (m *davContactMirror) writeContacts(ctx context.Context, contacts []mirrorContact) (int, []MirrorSkip, []MirrorConflict, error) {
	c := m.conn()
	if c == nil {
		return 0, nil, nil, fmt.Errorf("no CardDAV connector for %s", m.serviceID)
	}
	var copied int
	var skips []MirrorSkip
	var conflicts []MirrorConflict
	for _, mc := range contacts {
		name := mc.Name
		if name == "" && len(mc.Emails) > 0 {
			name = mc.Emails[0]
		}
		neutral := types.NewContact(m.serviceID, m.serviceID, mc.SourceID, mc.Name, mc.Emails, mc.Phones)
		neutral.Organization = mc.Organization
		if mc.SourceUID != "" {
			if neutral.Extra == nil {
				neutral.Extra = map[string]any{}
			}
			neutral.Extra["uid"] = mc.SourceUID
		}
		href := ""
		if mc.SourceID != "" && (strings.Contains(mc.SourceID, "/") || strings.HasSuffix(mc.SourceID, ".vcf")) {
			if strings.Contains(mc.SourceID, "/") {
				href = mc.SourceID
			}
		}
		etag := mc.SourceETag
		newETag, err := c.PutContact(ctx, href, etag, neutral)
		if err == connectors.ErrConflict {
			conflicts = append(conflicts, MirrorConflict{
				Name: name, Service: m.serviceID, ID: mc.SourceID,
				Reason: "remote contact changed (If-Match failed); not overwritten",
			})
			continue
		}
		if err != nil {
			skips = append(skips, MirrorSkip{Name: name, Reason: err.Error()})
			continue
		}
		copied++
		// Persist etag/sync watermark so the next run can do an incremental pass.
		m.server.recordMirrorItemCopy(MirrorKindContacts, name, m.serviceID, mc.SourceID, name, newETag)
	}
	return copied, skips, conflicts, nil
}

// --- Google contacts ---

type googleContactMirror struct {
	server    *Server
	serviceID string
}

func (m *googleContactMirror) conn() *connectors.GoogleContactsConnector {
	c, _ := m.server.connectors[m.serviceID].(*connectors.GoogleContactsConnector)
	return c
}

func (m *googleContactMirror) loadContacts(ctx context.Context) ([]mirrorContact, bool, error) {
	c := m.conn()
	if c == nil {
		return nil, false, fmt.Errorf("no Google Contacts connector for %s", m.serviceID)
	}
	contacts, truncated, err := c.ListUserContacts(ctx)
	if err != nil {
		return nil, truncated, err
	}
	out := make([]mirrorContact, 0, len(contacts))
	for _, ct := range contacts {
		out = append(out, mirrorContact{
			Name:         ct.Name,
			Emails:       ct.Emails,
			Phones:       ct.Phones,
			Organization: ct.Organization,
			SourceID:     firstSourceID(ct.Sources),
			SourceETag:   sourceExtraString(ct.Sources, "etag"),
		})
	}
	return out, truncated, nil
}

func (m *googleContactMirror) writeContacts(ctx context.Context, contacts []mirrorContact) (int, []MirrorSkip, []MirrorConflict, error) {
	c := m.conn()
	if c == nil {
		return 0, nil, nil, fmt.Errorf("no Google Contacts connector for %s", m.serviceID)
	}
	var copied int
	var skips []MirrorSkip
	var conflicts []MirrorConflict
	for _, mc := range contacts {
		name := mc.Name
		if name == "" && len(mc.Emails) > 0 {
			name = mc.Emails[0]
		}
		neutral := types.NewContact(m.serviceID, connectors.GoogleContactsServiceName, mc.SourceID, mc.Name, mc.Emails, mc.Phones)
		neutral.Organization = mc.Organization

		if mc.SourceID != "" && strings.Contains(mc.SourceID, "/") {
			// Update existing People resource.
			etag, err := c.UpdateGoogleContact(ctx, mc.SourceID, mc.SourceETag, neutral)
			if err == connectors.ErrConflict {
				conflicts = append(conflicts, MirrorConflict{
					Name: name, Service: m.serviceID, ID: mc.SourceID,
					Reason: "remote contact changed (etag mismatch); not overwritten",
				})
				continue
			}
			if err != nil {
				skips = append(skips, MirrorSkip{Name: name, Reason: err.Error()})
				continue
			}
			copied++
			m.server.recordMirrorItemCopy(MirrorKindContacts, name, m.serviceID, mc.SourceID, name, etag)
			continue
		}

		id, etag, err := c.CreateGoogleContact(ctx, neutral)
		if err == connectors.ErrConflict {
			conflicts = append(conflicts, MirrorConflict{
				Name: name, Service: m.serviceID,
				Reason: "remote contact changed; not overwritten",
			})
			continue
		}
		if err != nil {
			skips = append(skips, MirrorSkip{Name: name, Reason: err.Error()})
			continue
		}
		copied++
		m.server.recordMirrorItemCopy(MirrorKindContacts, name, m.serviceID, id, name, etag)
	}
	return copied, skips, conflicts, nil
}

// --- DAV events ---

type davEventMirror struct {
	server    *Server
	serviceID string
}

func (m *davEventMirror) conn() *connectors.CalDAVConnector {
	c, _ := m.server.connectors[m.serviceID].(*connectors.CalDAVConnector)
	return c
}

func (m *davEventMirror) loadEvents(ctx context.Context) ([]mirrorEvent, bool, error) {
	c := m.conn()
	if c == nil {
		return nil, false, fmt.Errorf("no CalDAV connector for %s", m.serviceID)
	}
	events, truncated, err := c.ListCalendarEvents(ctx)
	if err != nil {
		return nil, truncated, err
	}
	out := make([]mirrorEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, mirrorEvent{
			Title:       ev.Title,
			Description: ev.Description,
			StartAt:     ev.StartAt,
			EndAt:       ev.EndAt,
			AllDay:      ev.AllDay,
			Location:    ev.Location,
			Status:      ev.Status,
			EntityID:    ev.EntityID,
			SourceID:    firstSourceIDEvents(ev.Sources),
			SourceETag:  sourceExtraStringEvents(ev.Sources, "etag"),
			SourceUID:   sourceExtraStringEvents(ev.Sources, "uid"),
			CalendarID:  ev.CalendarID,
		})
	}
	return out, truncated, nil
}

func (m *davEventMirror) writeEvents(ctx context.Context, events []mirrorEvent) (int, []MirrorSkip, []MirrorConflict, error) {
	c := m.conn()
	if c == nil {
		return 0, nil, nil, fmt.Errorf("no CalDAV connector for %s", m.serviceID)
	}
	var copied int
	var skips []MirrorSkip
	var conflicts []MirrorConflict
	for _, me := range events {
		start, _ := parseRFC3339(me.StartAt)
		end, _ := parseRFC3339(me.EndAt)
		ev := connectors.ICalEvent{
			UID:         me.SourceUID,
			Summary:     me.Title,
			Description: me.Description,
			Location:    me.Location,
			Status:      me.Status,
			Start:       start,
			End:         end,
			AllDay:      me.AllDay,
			ETag:        me.SourceETag,
		}
		if ev.UID == "" {
			ev.UID = me.EntityID
		}
		if ev.UID == "" {
			ev.UID = me.SourceID
		}
		href := ""
		if me.SourceID != "" && (strings.Contains(me.SourceID, "/") || strings.HasSuffix(me.SourceID, ".ics")) {
			if strings.Contains(me.SourceID, "/") {
				href = me.SourceID
			}
		}
		newETag, err := c.PutEvent(ctx, href, me.SourceETag, ev)
		if err == connectors.ErrConflict {
			conflicts = append(conflicts, MirrorConflict{
				Name: me.Title, Service: m.serviceID, ID: me.SourceID,
				Reason: "remote event changed (If-Match failed); not overwritten",
			})
			continue
		}
		if err != nil {
			skips = append(skips, MirrorSkip{Name: me.Title, Reason: err.Error()})
			continue
		}
		copied++
		m.server.recordMirrorItemCopy(MirrorKindEvents, me.Title, m.serviceID, me.SourceID, me.Title, newETag)
	}
	return copied, skips, conflicts, nil
}

// --- Google events ---

type googleEventMirror struct {
	server    *Server
	serviceID string
}

func (m *googleEventMirror) conn() *connectors.GoogleCalendarConnector {
	c, _ := m.server.connectors[m.serviceID].(*connectors.GoogleCalendarConnector)
	return c
}

func (m *googleEventMirror) loadEvents(ctx context.Context) ([]mirrorEvent, bool, error) {
	c := m.conn()
	if c == nil {
		return nil, false, fmt.Errorf("no Google Calendar connector for %s", m.serviceID)
	}
	events, truncated, err := c.ListCalendarEvents(ctx)
	if err != nil {
		return nil, truncated, err
	}
	out := make([]mirrorEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, mirrorEvent{
			Title:       ev.Title,
			Description: ev.Description,
			StartAt:     ev.StartAt,
			EndAt:       ev.EndAt,
			AllDay:      ev.AllDay,
			Location:    ev.Location,
			Status:      ev.Status,
			EntityID:    ev.EntityID,
			SourceID:    firstSourceIDEvents(ev.Sources),
			SourceETag:  sourceExtraStringEvents(ev.Sources, "etag"),
			CalendarID:  ev.CalendarID,
		})
	}
	return out, truncated, nil
}

func (m *googleEventMirror) writeEvents(ctx context.Context, events []mirrorEvent) (int, []MirrorSkip, []MirrorConflict, error) {
	c := m.conn()
	if c == nil {
		return 0, nil, nil, fmt.Errorf("no Google Calendar connector for %s", m.serviceID)
	}
	var copied int
	var skips []MirrorSkip
	var conflicts []MirrorConflict
	for _, me := range events {
		neutral := types.CalendarEvent{
			Title:       me.Title,
			Description: me.Description,
			StartAt:     me.StartAt,
			EndAt:       me.EndAt,
			AllDay:      me.AllDay,
			Location:    me.Location,
			Status:      me.Status,
			CalendarID:  me.CalendarID,
			EntityID:    me.EntityID,
		}
		if me.SourceID != "" {
			etag, err := c.UpdateGoogleEvent(ctx, me.CalendarID, me.SourceID, me.SourceETag, neutral)
			if err == connectors.ErrConflict {
				conflicts = append(conflicts, MirrorConflict{
					Name: me.Title, Service: m.serviceID, ID: me.SourceID,
					Reason: "remote event changed (etag mismatch); not overwritten",
				})
				continue
			}
			if err != nil {
				skips = append(skips, MirrorSkip{Name: me.Title, Reason: err.Error()})
				continue
			}
			copied++
			m.server.recordMirrorItemCopy(MirrorKindEvents, me.Title, m.serviceID, me.SourceID, me.Title, etag)
			continue
		}
		id, link, etag, err := c.CreateGoogleEvent(ctx, me.CalendarID, neutral)
		if err == connectors.ErrConflict {
			conflicts = append(conflicts, MirrorConflict{
				Name: me.Title, Service: m.serviceID,
				Reason: "remote event changed; not overwritten",
			})
			continue
		}
		if err != nil {
			skips = append(skips, MirrorSkip{Name: me.Title, Reason: err.Error()})
			continue
		}
		copied++
		_ = link
		m.server.recordMirrorItemCopy(MirrorKindEvents, me.Title, m.serviceID, id, me.Title, etag)
	}
	return copied, skips, conflicts, nil
}

// --- helpers ---


func firstSourceIDEvents(sources []types.CalendarEventSource) string {
	if len(sources) > 0 {
		return sources[0].ID
	}
	return ""
}

func sourceExtraStringEvents(sources []types.CalendarEventSource, key string) string {
	for _, s := range sources {
		if s.Extra == nil {
			continue
		}
		if v, ok := s.Extra[key].(string); ok {
			return v
		}
	}
	return ""
}

func firstSourceID(sources []types.ContactSource) string {
	if len(sources) > 0 {
		return sources[0].ID
	}
	return ""
}

func sourceExtraString(sources []types.ContactSource, key string) string {
	for _, s := range sources {
		if s.Extra == nil {
			continue
		}
		if v, ok := s.Extra[key].(string); ok {
			return v
		}
	}
	return ""
}

// recordMirrorItemCopy stores per-item etag/sync watermarks (issue #146).
func (s *Server) recordMirrorItemCopy(kind, entityName, service, externalID, name, etag string) {
	if s == nil || s.copies == nil {
		return
	}
	entity := kind + ":" + strings.TrimSpace(entityName)
	if strings.TrimSpace(externalID) == "" {
		return
	}
	_, _ = s.copies.Upsert(CopyRecord{
		Kind:          kind,
		EntityKey:     entity,
		SourceService: service,
		SourceID:      externalID,
		Service:       service,
		ExternalID:    externalID,
		Role:          CopyRoleMirror,
		SyncMode:      CopySyncModeFull,
		Provenance:    CopyProvenanceMirror,
		Status:        CopyStatusInSync,
		Watermark:     etag,
		Version: CopyVersion{
			ExternalID: externalID,
			ETag:       etag,
			Name:       name,
		},
	})
}

func parseRFC3339(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// handleEntityMirror copies contacts or events between DAV and Google
// (issue #146). Conflicts are reported; a newer remote edit is never
// overwritten. Sync watermarks (etag) land in the copy ledger.
func (s *Server) handleEntityMirror(w http.ResponseWriter, r *http.Request, req MirrorRequest) {
	logger := withPeerLog(slog.With("handler", "library_mirror",
		"kind", req.Kind, "target", req.TargetService, "dry_run", req.DryRun), r)

	if err := s.requireMirrorWritable(req.TargetService, req.Kind); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	sourceService := req.SourceService
	if sourceService == "" {
		sourceService = s.defaultMirrorSource(req.Kind)
	}
	if sourceService == "" {
		writeJSONError(w, http.StatusBadRequest, "source_service is required (no canonical home configured)")
		return
	}
	if sourceService == req.TargetService {
		writeJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}
	if err := s.requireMirrorReadable(sourceService, req.Kind); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	ctx := r.Context()
	var planned int
	var srcRef MirrorRef
	var truncated bool
	var skips []MirrorSkip
	var conflicts []MirrorConflict
	var copied int
	var targetName string

	switch req.Kind {
	case MirrorKindContacts:
		src, err := s.contactMirrorSourceFor(sourceService)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		dst, err := s.contactMirrorTargetFor(req.TargetService)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, tr, err := src.loadContacts(ctx)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		if len(items) > mirrorTrackCap {
			items = items[:mirrorTrackCap]
			truncated = true
		}
		planned = len(items)
		srcRef = MirrorRef{Service: sourceService, Name: "contacts", Count: planned}
		targetName = "contacts"
		if req.DryRun {
			writeJSON(w, MirrorResult{
				Kind: req.Kind, Source: srcRef, DryRun: true, Planned: planned,
				Target:       MirrorRef{Service: req.TargetService, Name: targetName},
				Truncated:    truncated,
				IdentityNote: "Contact matching prefers email, then phone, then handle, then name. Conflicts are reported and never overwritten (If-Match / etag).",
			})
			return
		}
		copied, skips, conflicts, err = dst.writeContacts(ctx, items)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		s.recordMirrorCopy(MirrorKindContacts, sourceService, "", req.TargetService, "", targetName, copied, "")
	case MirrorKindEvents:
		src, err := s.eventMirrorSourceFor(sourceService)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		dst, err := s.eventMirrorTargetFor(req.TargetService)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, tr, err := src.loadEvents(ctx)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		if len(items) > mirrorTrackCap {
			items = items[:mirrorTrackCap]
			truncated = true
		}
		planned = len(items)
		srcRef = MirrorRef{Service: sourceService, Name: "events", Count: planned}
		targetName = "events"
		if req.DryRun {
			writeJSON(w, MirrorResult{
				Kind: req.Kind, Source: srcRef, DryRun: true, Planned: planned,
				Target:       MirrorRef{Service: req.TargetService, Name: targetName},
				Truncated:    truncated,
				IdentityNote: "Event matching prefers iCal UID, then title + start. Conflicts are reported and never overwritten. Mirrored events feed /v1/library/freebusy.",
			})
			return
		}
		copied, skips, conflicts, err = dst.writeEvents(ctx, items)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		s.recordMirrorCopy(MirrorKindEvents, sourceService, "", req.TargetService, "", targetName, copied, "")
	}

	result := MirrorResult{
		Kind:      req.Kind,
		Source:    srcRef,
		Target:    MirrorRef{Service: req.TargetService, Name: targetName, Count: copied},
		Copied:    copied,
		Skips:     skips,
		Skipped:   len(skips),
		Conflicts: conflicts,
		Planned:   planned,
		Truncated: truncated,
		IdentityNote: "Conflicts are reported and never overwritten (If-Match / etag). Sync watermarks are stored in the copy ledger.",
	}
	logger.Info("mirror complete",
		"source", sourceService, "target", req.TargetService,
		"planned", result.Planned, "copied", result.Copied,
		"skipped", result.Skipped, "conflicts", len(result.Conflicts))
	writeJSON(w, result)
}
