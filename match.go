package main

import (
	"sort"
	"strings"
)

// NormalizeIdentityKey produces a comparable key for cross-service identity
// matching. It is intentionally simple (case/whitespace, YouTube " - Topic"):
// good enough for v1, not a durable music-graph ID. Tracked in
// https://github.com/fudomunro/aperture-guardian/issues/4
func NormalizeIdentityKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " - topic", "")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// newArtist builds a unified artist from one service spelling.
func newArtist(service, serviceName, id, name string) Artist {
	name = strings.TrimSpace(name)
	if name == "" {
		return Artist{}
	}
	return Artist{
		Name: name,
		Sources: []ArtistSource{{
			Service:     service,
			ServiceName: serviceName,
			ID:          id,
			Name:        name,
		}},
	}
}

// newAlbum builds a unified album from one service spelling.
func newAlbum(service, serviceName, id, name string) *Album {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return &Album{
		Name: name,
		Sources: []AlbumSource{{
			Service:     service,
			ServiceName: serviceName,
			ID:          id,
			Name:        name,
		}},
	}
}

// mergeArtists folds service-local artist names into unified identities by
// normalized display name. Order follows first appearance.
func mergeArtists(items ...Artist) []Artist {
	type slot struct {
		key string
		a   Artist
	}
	var order []string
	byKey := map[string]*slot{}
	for _, in := range items {
		if in.Name == "" {
			continue
		}
		key := NormalizeIdentityKey(in.Name)
		if key == "" {
			continue
		}
		if cur, ok := byKey[key]; ok {
			cur.a.Sources = append(cur.a.Sources, in.Sources...)
			continue
		}
		byKey[key] = &slot{key: key, a: in}
		order = append(order, key)
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]Artist, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k].a)
	}
	return out
}

// PrimaryService returns the first source service ID, or "".
func (p Playlist) PrimaryService() string {
	if len(p.Sources) == 0 {
		return ""
	}
	return p.Sources[0].Service
}

// primaryPlaylistSource is a convenience when a single-service transform has
// exactly one location.
func primaryPlaylistSource(service, serviceName, id, url string, extra map[string]any) PlaylistSource {
	if len(extra) == 0 {
		extra = nil
	}
	return PlaylistSource{
		Service:     service,
		ServiceName: serviceName,
		ID:          id,
		URL:         url,
		Extra:       extra,
	}
}

// MergePlaylistsSync groups playlists that share a normalized name into a
// single user-facing playlist with multiple Sources. This is the v1 "sync"
// model: Guardian treats same-named playlists on different services as one
// thing the user cares about, without copying tracks between services yet.
func MergePlaylistsSync(in []Playlist) []Playlist {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		key string
		p   Playlist
	}
	var order []string
	byKey := map[string]*entry{}

	for _, p := range in {
		key := NormalizeIdentityKey(p.Name)
		if key == "" {
			key = "\x00" + p.PrimaryService() + ":" + firstSourceID(p)
		}
		if cur, ok := byKey[key]; ok {
			cur.p.Sources = append(cur.p.Sources, p.Sources...)
			// Prefer a non-empty description/owner; track_count = max (copies).
			if cur.p.Description == "" {
				cur.p.Description = p.Description
			}
			if cur.p.Owner == "" {
				cur.p.Owner = p.Owner
			}
			if p.TrackCount > cur.p.TrackCount {
				cur.p.TrackCount = p.TrackCount
			}
			if len(cur.p.Extra) == 0 && len(p.Extra) > 0 {
				cur.p.Extra = p.Extra
			}
			continue
		}
		// Clone sources so later appends don't alias.
		cp := p
		cp.Sources = append([]PlaylistSource(nil), p.Sources...)
		byKey[key] = &entry{key: key, p: cp}
		order = append(order, key)
	}

	out := make([]Playlist, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		// Stable source order: by service name.
		sort.SliceStable(e.p.Sources, func(i, j int) bool {
			if e.p.Sources[i].ServiceName != e.p.Sources[j].ServiceName {
				return e.p.Sources[i].ServiceName < e.p.Sources[j].ServiceName
			}
			return e.p.Sources[i].Service < e.p.Sources[j].Service
		})
		out = append(out, e.p)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func firstSourceID(p Playlist) string {
	if len(p.Sources) == 0 {
		return ""
	}
	return p.Sources[0].ID
}

// trackIdentityKey produces the merge key for a liked track: normalized title
// plus primary artist. Same v1 quality as playlist name matching.
func trackIdentityKey(t Track) string {
	title := NormalizeIdentityKey(t.Name)
	artist := ""
	if len(t.Artists) > 0 {
		artist = NormalizeIdentityKey(t.Artists[0].Name)
	}
	if title == "" {
		if len(t.Sources) > 0 {
			return "\x00" + t.Sources[0].Service + ":" + t.Sources[0].ID
		}
		return "\x00"
	}
	if artist == "" {
		return title
	}
	return title + "|" + artist
}

// MergeTracksSync groups liked tracks that share a normalized title and
// primary artist into a single user-facing track with multiple Sources.
// This mirrors MergePlaylistsSync for the liked-songs library.
func MergeTracksSync(in []Track) []Track {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		key string
		t   Track
	}
	var order []string
	byKey := map[string]*entry{}

	for _, t := range in {
		key := trackIdentityKey(t)
		if cur, ok := byKey[key]; ok {
			cur.t.Sources = append(cur.t.Sources, t.Sources...)
			// Merge artist sources; keep first non-empty album.
			cur.t.Artists = mergeArtists(append(cur.t.Artists, t.Artists...)...)
			if cur.t.Album == nil && t.Album != nil {
				album := *t.Album
				cur.t.Album = &album
			}
			if cur.t.DurationMs == 0 && t.DurationMs != 0 {
				cur.t.DurationMs = t.DurationMs
			}
			if cur.t.Duration == "" && t.Duration != "" {
				cur.t.Duration = t.Duration
			}
			if len(cur.t.Extra) == 0 && len(t.Extra) > 0 {
				cur.t.Extra = t.Extra
			}
			continue
		}
		cp := t
		cp.Sources = append([]TrackSource(nil), t.Sources...)
		if t.Album != nil {
			album := *t.Album
			cp.Album = &album
		}
		byKey[key] = &entry{key: key, t: cp}
		order = append(order, key)
	}

	out := make([]Track, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		sort.SliceStable(e.t.Sources, func(i, j int) bool {
			if e.t.Sources[i].ServiceName != e.t.Sources[j].ServiceName {
				return e.t.Sources[i].ServiceName < e.t.Sources[j].ServiceName
			}
			return e.t.Sources[i].Service < e.t.Sources[j].Service
		})
		if e.t.Artists != nil {
			sort.SliceStable(e.t.Artists, func(i, j int) bool {
				return e.t.Artists[i].Name < e.t.Artists[j].Name
			})
		}
		out = append(out, e.t)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return trackIdentityKey(out[i]) < trackIdentityKey(out[j])
	})
	return out
}

// albumIdentityKey produces the merge key for a saved album: normalized title
// plus primary artist.
func albumIdentityKey(a Album) string {
	title := NormalizeIdentityKey(a.Name)
	artist := ""
	if len(a.Artists) > 0 {
		artist = NormalizeIdentityKey(a.Artists[0].Name)
	}
	if title == "" {
		if len(a.Sources) > 0 {
			return "\x00" + a.Sources[0].Service + ":" + a.Sources[0].ID
		}
		return "\x00"
	}
	if artist == "" {
		return title
	}
	return title + "|" + artist
}

// MergeAlbumsSync groups saved albums that share a normalized title and
// primary artist into one user-facing album with multiple Sources.
func MergeAlbumsSync(in []Album) []Album {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		key string
		a   Album
	}
	var order []string
	byKey := map[string]*entry{}

	for _, a := range in {
		key := albumIdentityKey(a)
		if cur, ok := byKey[key]; ok {
			cur.a.Sources = append(cur.a.Sources, a.Sources...)
			cur.a.Artists = mergeArtists(append(cur.a.Artists, a.Artists...)...)
			continue
		}
		cp := a
		cp.Sources = append([]AlbumSource(nil), a.Sources...)
		byKey[key] = &entry{key: key, a: cp}
		order = append(order, key)
	}

	out := make([]Album, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		sort.SliceStable(e.a.Sources, func(i, j int) bool {
			if e.a.Sources[i].ServiceName != e.a.Sources[j].ServiceName {
				return e.a.Sources[i].ServiceName < e.a.Sources[j].ServiceName
			}
			return e.a.Sources[i].Service < e.a.Sources[j].Service
		})
		out = append(out, e.a)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return albumIdentityKey(out[i]) < albumIdentityKey(out[j])
	})
	return out
}

// mergeArtistLibrary folds followed artists across services by normalized name.
func mergeArtistLibrary(in []Artist) []Artist {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		key string
		a   Artist
	}
	var order []string
	byKey := map[string]*entry{}

	for _, a := range in {
		key := NormalizeIdentityKey(a.Name)
		if key == "" {
			if len(a.Sources) > 0 {
				key = "\x00" + a.Sources[0].Service + ":" + a.Sources[0].ID
			} else {
				continue
			}
		}
		if cur, ok := byKey[key]; ok {
			cur.a.Sources = append(cur.a.Sources, a.Sources...)
			continue
		}
		cp := a
		cp.Sources = append([]ArtistSource(nil), a.Sources...)
		byKey[key] = &entry{key: key, a: cp}
		order = append(order, key)
	}

	out := make([]Artist, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		sort.SliceStable(e.a.Sources, func(i, j int) bool {
			if e.a.Sources[i].ServiceName != e.a.Sources[j].ServiceName {
				return e.a.Sources[i].ServiceName < e.a.Sources[j].ServiceName
			}
			return e.a.Sources[i].Service < e.a.Sources[j].Service
		})
		out = append(out, e.a)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return NormalizeIdentityKey(out[i].Name) < NormalizeIdentityKey(out[j].Name)
	})
	return out
}
