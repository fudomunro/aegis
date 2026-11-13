package library

import (
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
)

// recordMirrorCopy persists a successful playlist/liked-tracks mirror
// (issue #76). Errors are logged by the caller and never fail the mirror.
// watermark carries the kind's own marker (the playlist shape's order
// watermark, issue #378) and is opaque to the ledger.
func (h *Handlers) RecordMirrorCopy(kind, sourceService, sourceID, targetService, targetID, name string, itemCount int, snapshotID, watermark string) {
	if h == nil || h.Copies == nil {
		return
	}
	entity := MirrorEntityKey(kind, sourceService, sourceID)
	_, err := h.Copies.Upsert(copies.Record{
		Kind:          kind,
		EntityKey:     entity,
		SourceService: sourceService,
		SourceID:      sourceID,
		Service:       targetService,
		ExternalID:    targetID,
		Role:          copies.CopyRoleMirror,
		SyncMode:      copies.SyncModeForKind(kind),
		Provenance:    copies.CopyProvenanceMirror,
		Status:        copies.CopyStatusInSync,
		Watermark:     watermark,
		Version: copies.Version{
			ExternalID: targetID,
			SnapshotID: snapshotID,
			ItemCount:  itemCount,
			Name:       name,
		},
	})
	if err != nil {
		// Fail-open: the copy already exists upstream; ledger is best-effort.
		return
	}
}

// mirrorEntityKey builds a stable logical id for a mirrored source list.
func MirrorEntityKey(kind, sourceService, sourceID string) string {
	kind = strings.TrimSpace(kind)
	sourceService = strings.TrimSpace(sourceService)
	sourceID = strings.TrimSpace(sourceID)
	if kind == MirrorKindTracks || sourceID == "" {
		return "liked:" + sourceService
	}
	return kind + ":" + sourceService + ":" + sourceID
}

// recordPhotoShareCopy persists a successful photo share as a durable copy
// (issue #20 PoC). The row is the provenance breadcrumb: content hash plus
// the public post locator. Errors are logged by the caller and never fail
// the share — the post already exists upstream.
func (h *Handlers) RecordPhotoShareCopy(sourceService, sourceID, targetService, postID, postURI, postURL, sha256, note string) {
	if h == nil || h.Copies == nil {
		return
	}
	entity := "photos:" + strings.TrimSpace(sourceService) + ":" + strings.TrimSpace(sourceID)
	_, err := h.Copies.Upsert(copies.Record{
		Kind:          "photos",
		EntityKey:     entity,
		SourceService: sourceService,
		SourceID:      sourceID,
		Service:       targetService,
		ExternalID:    postID,
		Role:          copies.CopyRoleMirror,
		SyncMode:      copies.SyncModeForKind("photos"),
		Provenance:    copies.CopyProvenanceShare,
		Status:        copies.CopyStatusInSync,
		Note:          note,
		Version: copies.Version{
			ExternalID:  postURI,
			ContentHash: sha256,
			Name:        postURL,
		},
	})
	_ = err // fail-open: ledger is best-effort
}
