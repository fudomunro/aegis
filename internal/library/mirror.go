package library

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Mirror kinds: the reconciliation kind ids. The sync engine's mirror and
// repair jobs cover playlists and tracks; albums, artists, contacts, and
// events join through convergence (issue #375).
const (
	MirrorKindPlaylists = "playlists"
	MirrorKindTracks    = "tracks"
	MirrorKindContacts  = "contacts"
	MirrorKindEvents    = "events"
	MirrorKindAlbums    = "albums"
	MirrorKindArtists   = "artists"
)

// MirrorTrackCap bounds a single mirror step so a huge library cannot
// turn into an unbounded write storm. Chained job steps continue where the
// previous one stopped.
const MirrorTrackCap = 200

// MirrorRef identifies one service-local item involved in a mirror.
type MirrorRef struct {
	Service string `json:"service"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Count   int    `json:"count,omitempty"`
}

// RequireMirrorWritable checks the target allows writes for the kind under
// the given grant map.
func (h *Handlers) RequireMirrorWritable(grants shared.GrantMap, serviceID, kind string) error {
	svc, ok := h.Host.Service(serviceID)
	if !ok {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !grants.AllowsWrite(kind) {
		return fmt.Errorf("denied: data grant for agent write access to %s on %s is %q",
			kind, serviceID, grants.Level(kind))
	}
	return nil
}

// RequireMirrorReadable checks the source allows reads for the kind under
// the given grant map.
func (h *Handlers) RequireMirrorReadable(grants shared.GrantMap, serviceID, kind string) error {
	svc, ok := h.Host.Service(serviceID)
	if !ok {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !grants.AllowsRead(kind) {
		return fmt.Errorf("denied: data grant for agent read access to %s on %s is %q",
			kind, serviceID, grants.Level(kind))
	}
	return nil
}

// DefaultMirrorSource picks the canonical home for kind, else "".
func (h *Handlers) DefaultMirrorSource(kind string) string {
	for _, svc := range h.Host.Services() {
		if svc.IsCanonicalFor(kind) {
			return svc.ID
		}
	}
	return ""
}

// MirrorSource is the read surface the mirror engine needs from a service:
// the type-level list operations reconciliation plans against.
type MirrorSource interface {
	kinds.PlaylistItemsProvider
	kinds.LikedTrackProvider
	kinds.LikedTrackPageProvider
}

// MirrorTarget is the write surface: the type-level placement operations
// (issue #254). Appending to a known playlist is what lets a mirror chain
// spread across job steps without creating a playlist per batch
// (issue #227 phase 3).
type MirrorTarget interface {
	kinds.PlaylistItemWriteProvider
	kinds.LikedTrackWriteProvider
}

func (h *Handlers) MirrorSourceFor(serviceID, kind string) (MirrorSource, error) {
	if h.MirrorSourceFn != nil {
		return h.MirrorSourceFn(serviceID, kind)
	}
	if conn, ok := h.Connectors[serviceID].(MirrorSource); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror source %q is not supported yet (supported: %s)",
		serviceID, MirrorSupportedFor[MirrorSource](h))
}

func (h *Handlers) MirrorTargetFor(serviceID, kind string) (MirrorTarget, error) {
	if h.MirrorTargetFn != nil {
		return h.MirrorTargetFn(serviceID, kind)
	}
	if conn, ok := h.Connectors[serviceID].(MirrorTarget); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror target %q is not supported yet (supported: %s)",
		serviceID, MirrorSupportedFor[MirrorTarget](h))
}

// mirrorSupportedFor lists registered services implementing P, so
// "not supported yet" copy stays accurate as connectors gain operations.
func MirrorSupportedFor[P any](h *Handlers) string {
	ids := make([]string, 0, len(h.Connectors))
	for id, conn := range h.Connectors {
		if _, ok := any(conn).(P); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// mirrorRefFromPlaylist projects a neutral playlist ref onto the mirror ref
// shape used for progress and naming.
func MirrorRefFromPlaylist(service string, p kinds.Playlist, count int) MirrorRef {
	ref := MirrorRef{Service: service, Name: p.Name, Count: count}
	if len(p.Sources) > 0 {
		ref.ID = p.Sources[0].ID
		ref.URL = p.Sources[0].URL
	}
	return ref
}
