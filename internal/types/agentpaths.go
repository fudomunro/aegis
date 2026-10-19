package types

import "strings"

// IsAgentContentPath reports whether a canonical service-proxy path is a
// justified agent-facing transport under issue #119.
//
// Library (/v1/library/{kind}) is the only data-list surface. A /v1/services
// path stays open only when it delivers user-data content that library lists
// omit (bytes, full bodies, item detail) or ingests a local export the user
// already owns. Provider-shaped list mirrors, playback control, catalog
// search, and generic OAuth pass-through are not product API — use library.
func IsAgentContentPath(canonical string) bool {
	if canonical == "" || canonical == "/" {
		return false
	}
	switch {
	// Local export ingest (Signal, Quicken, …).
	case canonical == "/import",
		canonical == "/import/status",
		strings.HasPrefix(canonical, "/import/"):
		return true
	// Google Photos Picker: user-selected items that feed the photos library.
	case canonical == "/picker", strings.HasPrefix(canonical, "/picker/"):
		return true
	// Photo/asset bytes — Aegis never stores media; library is metadata-only.
	case strings.HasPrefix(canonical, "/assets/"):
		return true
	// Bluesky image blobs referenced by sources[].blob_url.
	case canonical == "/xrpc/com.atproto.sync.getBlob",
		strings.HasPrefix(canonical, "/xrpc/com.atproto.sync.getBlob?"):
		return true
	// Full message/thread bodies (library lists carry snippet only).
	case detailSegment(canonical, "/messages/"),
		detailSegment(canonical, "/threads/"):
		return true
	// Playlist items (library entries carry track_count only).
	case detailSegment(canonical, "/playlists/"):
		return true
	// Document file bytes / previews — library lists carry metadata + excerpt
	// only; full text is gated on /v1/library/documents/{service}/{id}/text.
	// Bare /documents/{id} is not content transport (it includes OCR text).
	case strings.HasSuffix(canonical, "/download"),
		strings.HasSuffix(canonical, "/preview"),
		strings.HasSuffix(canonical, "/thumb"):
		if strings.HasPrefix(canonical, "/documents/") || strings.HasPrefix(canonical, "/api/documents/") {
			return true
		}
		return false
	// Google Docs content export (HTML or plain text; markdown deferred).
	// Bare /files and /files/{id} stay library-only (issue #143).
	case strings.HasSuffix(canonical, "/export"):
		if strings.HasPrefix(canonical, "/files/") || strings.HasPrefix(canonical, "/drive/files/") {
			return true
		}
		return false
	default:
		return false
	}
}

// detailSegment reports whether canonical is /prefix/{id} or deeper — not a
// bare collection list at the prefix itself.
func detailSegment(canonical, prefix string) bool {
	return strings.HasPrefix(canonical, prefix) && len(canonical) > len(prefix)
}
