package library

import (
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
	"github.com/fudomunro/aegis/internal/kinds"
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

// RecordMirrorItemCopy stores per-item etag/sync watermarks (issue #146).
// entityName is the item's identity key where it has one (issue #384) and
// the display name otherwise; a row written under the older display-name
// key is retired here so landing the identity-key convention migrates it
// instead of leaving two rows for one copy.
func (h *Handlers) RecordMirrorItemCopy(kind, entityName, service, externalID, name, etag string) {
	if h == nil || h.Copies == nil {
		return
	}
	entity := kinds.ItemEntityKey(kind, entityName)
	if strings.TrimSpace(externalID) == "" {
		return
	}
	_, _ = h.Copies.Upsert(copies.Record{
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
	if legacy := kinds.ItemEntityKey(kind, name); legacy != entity {
		_ = h.Copies.Delete(kind, legacy, service, externalID)
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
