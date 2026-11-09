package core

import (
	"strings"

	"github.com/fudomunro/aegis/internal/core/copies"
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

// recordMirrorCopy persists a successful playlist/liked-tracks mirror
// (issue #76). Errors are logged by the caller and never fail the mirror.
func (s *Server) recordMirrorCopy(kind, sourceService, sourceID, targetService, targetID, name string, itemCount int, snapshotID string) {
	if s == nil || s.copyLedger == nil {
		return
	}
	entity := mirrorEntityKey(kind, sourceService, sourceID)
	_, err := s.copyLedger.Upsert(copies.Record{
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
func mirrorEntityKey(kind, sourceService, sourceID string) string {
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
func (s *Server) recordPhotoShareCopy(sourceService, sourceID, targetService, postID, postURI, postURL, sha256, note string) {
	if s == nil || s.copyLedger == nil {
		return
	}
	entity := "photos:" + strings.TrimSpace(sourceService) + ":" + strings.TrimSpace(sourceID)
	_, err := s.copyLedger.Upsert(copies.Record{
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

// recordTakeoutSnapshot stores/refreshes the optimistic snapshot row for a
// Google Takeout import into the mail archive (issue #76).
func (s *Server) recordTakeoutSnapshot(path string, scanned, stored, skipped int, done bool, serviceID string) {
	if s == nil || s.copyLedger == nil {
		return
	}
	s.copyLedger.RecordTakeoutSnapshot(path, scanned, stored, skipped, done, serviceID)
}
