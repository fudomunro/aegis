package core

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
	"github.com/fudomunro/aegis/internal/core/copies"
)

// Contacts and events mirror (issue #146) is reconciliation over neutral
// models like the music path (issue #254): core reads via the type-level
// providers, places via ContactWriteProvider / CalendarEventWriteProvider,
// and stamps per-item etag/sync watermarks into the copy ledger. Conflicts
// are reported in MirrorResult instead of picking a winner.

// recordMirrorItemCopy stores per-item etag/sync watermarks (issue #146).
func (s *Server) recordMirrorItemCopy(kind, entityName, service, externalID, name, etag string) {
	if s == nil || s.copyLedger == nil {
		return
	}
	entity := kind + ":" + strings.TrimSpace(entityName)
	if strings.TrimSpace(externalID) == "" {
		return
	}
	_, _ = s.copyLedger.Upsert(copies.Record{
		Kind:          kind,
		EntityKey:     entity,
		SourceService: service,
		SourceID:      externalID,
		Service:       service,
		ExternalID:    externalID,
		Role:          copies.CopyRoleMirror,
		SyncMode:      copies.CopySyncModeFull,
		Provenance:    copies.CopyProvenanceMirror,
		Status:        copies.CopyStatusInSync,
		Watermark:     etag,
		Version: copies.Version{
			ExternalID: externalID,
			ETag:       etag,
			Name:       name,
		},
	})
}

// handleEntityMirror copies contacts or events between services
// (issue #146). Conflicts are reported; a newer remote edit is never
// overwritten. Sync watermarks (etag) land in the copy ledger.
func (s *Server) handleEntityMirror(w http.ResponseWriter, r *http.Request, req MirrorRequest) {
	logger := withPeerLog(slog.With("handler", "library_mirror",
		"kind", req.Kind, "target", req.TargetService, "dry_run", req.DryRun), r)

	if err := s.requireMirrorWritable(req.TargetService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	sourceService := req.SourceService
	if sourceService == "" {
		sourceService = s.defaultMirrorSource(req.Kind)
	}
	if sourceService == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service is required (no canonical home configured)")
		return
	}
	if sourceService == req.TargetService {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}
	if err := s.requireMirrorReadable(sourceService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	ctx := r.Context()
	var planned int
	var srcRef MirrorRef
	var truncated bool
	var result MirrorResult

	switch req.Kind {
	case MirrorKindContacts:
		src, err := mirrorOpFor[types.ContactProvider](s, sourceService, "contact mirror source")
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		dst, err := mirrorOpFor[types.ContactWriteProvider](s, req.TargetService, "contact mirror target")
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, tr, err := src.ListUserContacts(ctx)
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
		if req.DryRun {
			shared.WriteJSON(w, MirrorResult{
				Kind: req.Kind, Source: srcRef, DryRun: true, Planned: planned,
				Target:       MirrorRef{Service: req.TargetService, Name: "contacts"},
				Truncated:    truncated,
				IdentityNote: "Contact matching prefers email, then phone, then handle, then name. Conflicts are reported and never overwritten (If-Match / etag).",
			})
			return
		}
		res, err := dst.PlaceContacts(ctx, items)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		for _, rec := range res.Placed {
			s.recordMirrorItemCopy(MirrorKindContacts, rec.Name, req.TargetService, rec.ExternalID, rec.Name, rec.ETag)
		}
		s.recordMirrorCopy(MirrorKindContacts, sourceService, "", req.TargetService, "", "contacts", res.Copied, "")
		result = MirrorResult{
			Kind: req.Kind, Source: srcRef,
			Target:    MirrorRef{Service: req.TargetService, Name: "contacts", Count: res.Copied},
			Copied:    res.Copied,
			Skips:     toMirrorSkips(res.Skips),
			Skipped:   len(res.Skips),
			Conflicts: toMirrorConflicts(res.Conflicts),
			Planned:   planned,
			Truncated: truncated,
		}
	case MirrorKindEvents:
		src, err := mirrorOpFor[types.CalendarEventProvider](s, sourceService, "event mirror source")
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		dst, err := mirrorOpFor[types.CalendarEventWriteProvider](s, req.TargetService, "event mirror target")
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, tr, err := src.ListCalendarEvents(ctx)
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
		if req.DryRun {
			shared.WriteJSON(w, MirrorResult{
				Kind: req.Kind, Source: srcRef, DryRun: true, Planned: planned,
				Target:       MirrorRef{Service: req.TargetService, Name: "events"},
				Truncated:    truncated,
				IdentityNote: "Event matching prefers iCal UID, then title + start. Conflicts are reported and never overwritten. Mirrored events feed /v1/library/freebusy.",
			})
			return
		}
		res, err := dst.PlaceEvents(ctx, items)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		for _, rec := range res.Placed {
			s.recordMirrorItemCopy(MirrorKindEvents, rec.Name, req.TargetService, rec.ExternalID, rec.Name, rec.ETag)
		}
		s.recordMirrorCopy(MirrorKindEvents, sourceService, "", req.TargetService, "", "events", res.Copied, "")
		result = MirrorResult{
			Kind: req.Kind, Source: srcRef,
			Target:    MirrorRef{Service: req.TargetService, Name: "events", Count: res.Copied},
			Copied:    res.Copied,
			Skips:     toMirrorSkips(res.Skips),
			Skipped:   len(res.Skips),
			Conflicts: toMirrorConflicts(res.Conflicts),
			Planned:   planned,
			Truncated: truncated,
		}
	}

	result.IdentityNote = "Conflicts are reported and never overwritten (If-Match / etag). Sync watermarks are stored in the copy ledger."
	logger.Info("mirror complete",
		"source", sourceService, "target", req.TargetService,
		"planned", result.Planned, "copied", result.Copied,
		"skipped", result.Skipped, "conflicts", len(result.Conflicts))
	shared.WriteJSON(w, result)
}
