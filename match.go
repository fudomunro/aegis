package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Match methods for Track.Match / Album.Match.
const (
	TrackMatchISRC = "isrc"
	TrackMatchName = "name"
	AlbumMatchUPC  = "upc"
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
		// Canonical sources first so the authoritative home is obvious.
		sortPlaylistSourcesCanonicalFirst(e.p.Sources)
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

// trackIdentityKey produces the fallback merge key for a liked track:
// normalized title plus primary artist. Used when no durable EntityID (ISRC)
// is available. Same v1 quality as playlist name matching.
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

// trackEntityID returns a normalized durable catalog ID (ISRC) when present.
func trackEntityID(t Track) string {
	return strings.ToUpper(strings.TrimSpace(t.EntityID))
}

// durationMsTolerance is how far two name-matched tracks' lengths may differ
// and still be treated as the same recording.
const durationMsTolerance = 5000

// durationCompatible reports whether two tracks may be the same recording
// based on duration. Zero means unknown and does not block a merge.
func durationCompatible(a, b Track) bool {
	if a.DurationMs <= 0 || b.DurationMs <= 0 {
		return true
	}
	diff := a.DurationMs - b.DurationMs
	if diff < 0 {
		diff = -diff
	}
	return diff <= durationMsTolerance
}

// youtubeTitleNoise matches common YouTube decoration at the end of a title
// so music videos can name-match catalog tracks (Spotify, etc.).
var youtubeTitleNoise = regexp.MustCompile(
	`(?i)\s*[\(\[]\s*(?:official\s+)?(?:music\s+)?(?:video|audio|lyric\s*video?|lyrics?|visuali[sz]er|hd|hq|4k|live|explicit|clean|remaster(?:ed)?)(?:\s+[^)\]]*)?[\)\]]\s*$`)

// cleanYouTubeTitle strips stacked trailing noise tags from a video title.
// The cleaned string is used as the neutral Track.Name; the original is
// preserved in source extra as original_title when they differ.
func cleanYouTubeTitle(name string) string {
	s := strings.TrimSpace(name)
	for i := 0; i < 5; i++ {
		next := strings.TrimSpace(youtubeTitleNoise.ReplaceAllString(s, ""))
		if next == "" || next == s {
			return next
		}
		s = next
	}
	return s
}

// mergeTrackInto folds b into a (a is the survivor). match is the identity
// method used for this join.
func mergeTrackInto(a *Track, b Track, match string) {
	a.Sources = append(a.Sources, b.Sources...)
	a.Artists = mergeArtists(append(a.Artists, b.Artists...)...)
	if a.Album == nil && b.Album != nil {
		album := *b.Album
		a.Album = &album
	}
	if a.DurationMs == 0 && b.DurationMs != 0 {
		a.DurationMs = b.DurationMs
	}
	if a.Duration == "" && b.Duration != "" {
		a.Duration = b.Duration
	}
	if a.EntityID == "" {
		a.EntityID = b.EntityID
	}
	if len(a.Extra) == 0 && len(b.Extra) > 0 {
		a.Extra = b.Extra
	}
	if a.Match == "" {
		a.Match = match
	} else if a.Match == TrackMatchName && match == TrackMatchISRC {
		a.Match = TrackMatchISRC
	}
}

func cloneTrackForMerge(t Track) Track {
	cp := t
	cp.Sources = append([]TrackSource(nil), t.Sources...)
	if t.Album != nil {
		album := *t.Album
		cp.Album = &album
	}
	if t.Artists != nil {
		cp.Artists = append([]Artist(nil), t.Artists...)
	}
	return cp
}

// MergeTracksSync groups liked tracks into unified entities.
//
// Identity order (issue #4):
//  1. Shared EntityID (ISRC) — durable, high confidence (Match=isrc)
//  2. Normalized title + primary artist — fallback (Match=name)
//
// Name-only tracks are absorbed into an ISRC group when their name key
// matches, so a YouTube music video can join a Spotify catalog track.
// Distinct ISRCs with the same display name stay separate (different
// recordings / remixes).
func MergeTracksSync(in []Track) []Track {
	if len(in) == 0 {
		return nil
	}

	type entry struct {
		t Track
	}
	var order []string
	byEntity := map[string]*entry{}
	var nameOnly []Track

	for _, t := range in {
		if id := trackEntityID(t); id != "" {
			if cur, ok := byEntity[id]; ok {
				mergeTrackInto(&cur.t, t, TrackMatchISRC)
				continue
			}
			cp := cloneTrackForMerge(t)
			cp.EntityID = id
			byEntity[id] = &entry{t: cp}
			order = append(order, "e:"+id)
			continue
		}
		nameOnly = append(nameOnly, t)
	}

	// Name key → ISRC groups with that key (absorption targets).
	entityByName := map[string][]*entry{}
	ids := make([]string, 0, len(byEntity))
	for id := range byEntity {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := byEntity[id]
		k := trackIdentityKey(e.t)
		entityByName[k] = append(entityByName[k], e)
	}

	byName := map[string]*entry{}
	for _, t := range nameOnly {
		k := trackIdentityKey(t)
		absorbed := false
		for _, e := range entityByName[k] {
			if durationCompatible(e.t, t) {
				mergeTrackInto(&e.t, t, TrackMatchName)
				absorbed = true
				break
			}
		}
		if absorbed {
			continue
		}
		if e, ok := byName[k]; ok {
			if durationCompatible(e.t, t) {
				mergeTrackInto(&e.t, t, TrackMatchName)
				continue
			}
			// Same title/artist but incompatible length → separate entry.
			if t.DurationMs > 0 {
				k = k + "|" + strconv.Itoa(t.DurationMs/1000)
			}
		}
		cp := cloneTrackForMerge(t)
		byName[k] = &entry{t: cp}
		order = append(order, "n:"+k)
	}

	// Multi-source ISRC groups are high-confidence matches.
	for _, e := range byEntity {
		if len(e.t.Sources) > 1 {
			e.t.Match = TrackMatchISRC
		}
	}

	out := make([]Track, 0, len(order))
	for _, k := range order {
		var e *entry
		if strings.HasPrefix(k, "e:") {
			e = byEntity[k[2:]]
		} else {
			e = byName[k[2:]]
		}
		if e == nil {
			continue
		}
		sortTrackSourcesCanonicalFirst(e.t.Sources)
		if e.t.Artists != nil {
			sort.SliceStable(e.t.Artists, func(i, j int) bool {
				return e.t.Artists[i].Name < e.t.Artists[j].Name
			})
		}
		// Drop Match on still-single name-only tracks (not a join).
		if e.t.EntityID == "" && e.t.Match == TrackMatchName && len(e.t.Sources) <= 1 {
			e.t.Match = ""
		}
		out = append(out, e.t)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := trackEntityID(out[i]), trackEntityID(out[j])
		if ei != "" && ej != "" && ei != ej {
			return ei < ej
		}
		return trackIdentityKey(out[i]) < trackIdentityKey(out[j])
	})
	return out
}

// albumIdentityKey produces the fallback merge key for a saved album:
// normalized title plus primary artist.
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

// albumEntityID returns a normalized durable catalog ID (UPC) when present.
func albumEntityID(a Album) string {
	return strings.ToUpper(strings.TrimSpace(a.EntityID))
}

func mergeAlbumInto(dst *Album, src Album, match string) {
	dst.Sources = append(dst.Sources, src.Sources...)
	dst.Artists = mergeArtists(append(dst.Artists, src.Artists...)...)
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	if dst.Match == "" {
		dst.Match = match
	} else if dst.Match == TrackMatchName && match == AlbumMatchUPC {
		dst.Match = AlbumMatchUPC
	}
}

func cloneAlbumForMerge(a Album) Album {
	cp := a
	cp.Sources = append([]AlbumSource(nil), a.Sources...)
	if a.Artists != nil {
		cp.Artists = append([]Artist(nil), a.Artists...)
	}
	return cp
}

// MergeAlbumsSync groups saved albums into unified entities.
//
// Identity order (issue #4):
//  1. Shared EntityID (UPC/barcode) — durable (Match=upc)
//  2. Normalized title + primary artist — fallback (Match=name)
func MergeAlbumsSync(in []Album) []Album {
	if len(in) == 0 {
		return nil
	}

	type entry struct{ a Album }
	var order []string
	byEntity := map[string]*entry{}
	var nameOnly []Album

	for _, a := range in {
		if id := albumEntityID(a); id != "" {
			if cur, ok := byEntity[id]; ok {
				mergeAlbumInto(&cur.a, a, AlbumMatchUPC)
				continue
			}
			cp := cloneAlbumForMerge(a)
			cp.EntityID = id
			byEntity[id] = &entry{a: cp}
			order = append(order, "e:"+id)
			continue
		}
		nameOnly = append(nameOnly, a)
	}

	entityByName := map[string][]*entry{}
	ids := make([]string, 0, len(byEntity))
	for id := range byEntity {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := byEntity[id]
		k := albumIdentityKey(e.a)
		entityByName[k] = append(entityByName[k], e)
	}

	byName := map[string]*entry{}
	for _, a := range nameOnly {
		k := albumIdentityKey(a)
		absorbed := false
		for _, e := range entityByName[k] {
			mergeAlbumInto(&e.a, a, TrackMatchName)
			absorbed = true
			break
		}
		if absorbed {
			continue
		}
		if e, ok := byName[k]; ok {
			mergeAlbumInto(&e.a, a, TrackMatchName)
			continue
		}
		cp := cloneAlbumForMerge(a)
		byName[k] = &entry{a: cp}
		order = append(order, "n:"+k)
	}

	for _, e := range byEntity {
		if len(e.a.Sources) > 1 {
			e.a.Match = AlbumMatchUPC
		}
	}

	out := make([]Album, 0, len(order))
	for _, k := range order {
		var e *entry
		if strings.HasPrefix(k, "e:") {
			e = byEntity[k[2:]]
		} else {
			e = byName[k[2:]]
		}
		if e == nil {
			continue
		}
		sort.SliceStable(e.a.Sources, func(i, j int) bool {
			if e.a.Sources[i].ServiceName != e.a.Sources[j].ServiceName {
				return e.a.Sources[i].ServiceName < e.a.Sources[j].ServiceName
			}
			return e.a.Sources[i].Service < e.a.Sources[j].Service
		})
		if e.a.EntityID == "" && e.a.Match == TrackMatchName && len(e.a.Sources) <= 1 {
			e.a.Match = ""
		}
		out = append(out, e.a)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := albumEntityID(out[i]), albumEntityID(out[j])
		if ei != "" && ej != "" && ei != ej {
			return ei < ej
		}
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
