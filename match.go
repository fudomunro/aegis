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
