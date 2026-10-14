package types

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Match methods for Track / Album / Artist / Playlist.
const (
	TrackMatchISRC    = "isrc"
	TrackMatchMBID    = "mbid"
	TrackMatchName    = "name"
	AlbumMatchUPC     = "upc"
	AlbumMatchMBID    = "mbid"
	ArtistMatchMBID   = "mbid"
	PlaylistMatchLink = "link"
	PlaylistMatchName = "name"
)

// NormalizeIdentityKey produces a comparable key for cross-service identity
// matching. It is intentionally simple (case/whitespace, YouTube " - Topic"):
// good enough for v1, not a durable music-graph ID. Tracked in
// https://github.com/fudomunro/aegis/issues/4
func NormalizeIdentityKey(name string) string {
	// Compare on whitespace-collapsed tokens and drop the YouTube "- topic"
	// unit wherever it appears ("Name - Topic" / "Name\n- Topic"). Token
	// pairing keeps this idempotent; a raw " - topic" replace does not once
	// Fields has already eaten the leading space (issue #107 fuzz).
	words := strings.Fields(strings.ToLower(strings.TrimSpace(name)))
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); {
		if i+1 < len(words) && words[i] == "-" && words[i+1] == "topic" {
			i += 2
			continue
		}
		out = append(out, words[i])
		i++
	}
	return strings.Join(out, " ")
}

// NewArtist builds a unified artist from one service spelling.
func NewArtist(service, serviceName, id, name string) Artist {
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

// NewAlbum builds a unified album from one service spelling.
func NewAlbum(service, serviceName, id, name string) *Album {
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

// MergeArtists folds service-local artist names into unified identities by
// normalized display name. Order follows first appearance.
func MergeArtists(items ...Artist) []Artist {
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

// PrimaryPlaylistSource is a convenience when a single-service transform has
// exactly one location.
func PrimaryPlaylistSource(service, serviceName, id, url string, extra map[string]any) PlaylistSource {
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
// single user-facing playlist with multiple Sources. Prefer
// MergePlaylistsSyncLinked when explicit entity links are available.
func MergePlaylistsSync(in []Playlist) []Playlist {
	return MergePlaylistsSyncLinked(in, nil)
}

// MergePlaylistsSyncLinked merges playlists using explicit user-confirmed
// links first (issue #4), then falls back to normalized name equality.
// linkKey may be nil. Multi-source name merges set Match=name; linked
// merges set Match=link.
func MergePlaylistsSyncLinked(in []Playlist, linkKey func(Playlist) string) []Playlist {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		key string
		p   Playlist
	}
	var order []string
	byKey := map[string]*entry{}

	playlistKey := func(p Playlist) string {
		if linkKey != nil {
			if lk := linkKey(p); lk != "" {
				return lk
			}
		}
		key := NormalizeIdentityKey(p.Name)
		if key == "" {
			key = "\x00" + p.PrimaryService() + ":" + FirstSourceID(p)
		}
		return "name:" + key
	}

	for _, p := range in {
		key := playlistKey(p)
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
			if strings.HasPrefix(key, "link:") {
				cur.p.Match = PlaylistMatchLink
			} else {
				cur.p.Match = PlaylistMatchName
			}
			continue
		}
		// Clone sources so later appends don't alias.
		cp := p
		cp.Sources = append([]PlaylistSource(nil), p.Sources...)
		if strings.HasPrefix(key, "link:") {
			cp.Match = PlaylistMatchLink
		}
		byKey[key] = &entry{key: key, p: cp}
		order = append(order, key)
	}

	out := make([]Playlist, 0, len(order))
	for _, k := range order {
		e := byKey[k]
		// Canonical sources first so the authoritative home is obvious.
		SortPlaylistSourcesCanonicalFirst(e.p.Sources)
		// Drop Match on still-single name-only playlists (not a join).
		if e.p.Match == PlaylistMatchName && len(e.p.Sources) <= 1 {
			e.p.Match = ""
		}
		out = append(out, e.p)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func FirstSourceID(p Playlist) string {
	if len(p.Sources) == 0 {
		return ""
	}
	return p.Sources[0].ID
}

// TrackIdentityKey produces the fallback merge key for a liked track:
// normalized title plus primary artist. Used when no durable EntityID (ISRC)
// is available. Same v1 quality as playlist name matching.
func TrackIdentityKey(t Track) string {
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

// TrackEntityID returns a normalized durable catalog ID (ISRC) when present.
func TrackEntityID(t Track) string {
	return strings.ToUpper(strings.TrimSpace(t.EntityID))
}

// durationMsTolerance is how far two name-matched tracks' lengths may differ
// and still be treated as the same recording.
const durationMsTolerance = 5000

// DurationCompatible reports whether two tracks may be the same recording
// based on duration. Zero means unknown and does not block a merge.
func DurationCompatible(a, b Track) bool {
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

// CleanYouTubeTitle strips stacked trailing noise tags from a video title.
// The cleaned string is used as the neutral Track.Name; the original is
// preserved in source extra as original_title when they differ.
func CleanYouTubeTitle(name string) string {
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
	a.Artists = MergeArtists(append(a.Artists, b.Artists...)...)
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
		if id := TrackEntityID(t); id != "" {
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
		k := TrackIdentityKey(e.t)
		entityByName[k] = append(entityByName[k], e)
	}

	byName := map[string]*entry{}
	for _, t := range nameOnly {
		k := TrackIdentityKey(t)
		absorbed := false
		for _, e := range entityByName[k] {
			if DurationCompatible(e.t, t) {
				mergeTrackInto(&e.t, t, TrackMatchName)
				absorbed = true
				break
			}
		}
		if absorbed {
			continue
		}
		if e, ok := byName[k]; ok {
			if DurationCompatible(e.t, t) {
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
		SortTrackSourcesCanonicalFirst(e.t.Sources)
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
		ei, ej := TrackEntityID(out[i]), TrackEntityID(out[j])
		if ei != "" && ej != "" && ei != ej {
			return ei < ej
		}
		return TrackIdentityKey(out[i]) < TrackIdentityKey(out[j])
	})
	return out
}

// AlbumIdentityKey produces the fallback merge key for a saved album:
// normalized title plus primary artist.
func AlbumIdentityKey(a Album) string {
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

// AlbumEntityID returns a normalized durable catalog ID (UPC) when present.
func AlbumEntityID(a Album) string {
	return strings.ToUpper(strings.TrimSpace(a.EntityID))
}

func mergeAlbumInto(dst *Album, src Album, match string) {
	dst.Sources = append(dst.Sources, src.Sources...)
	dst.Artists = MergeArtists(append(dst.Artists, src.Artists...)...)
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
		if id := AlbumEntityID(a); id != "" {
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
		k := AlbumIdentityKey(e.a)
		entityByName[k] = append(entityByName[k], e)
	}

	byName := map[string]*entry{}
	for _, a := range nameOnly {
		k := AlbumIdentityKey(a)
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
		ei, ej := AlbumEntityID(out[i]), AlbumEntityID(out[j])
		if ei != "" && ej != "" && ei != ej {
			return ei < ej
		}
		return AlbumIdentityKey(out[i]) < AlbumIdentityKey(out[j])
	})
	return out
}

// ArtistEntityID returns a normalized MusicBrainz MBID when present.
func ArtistEntityID(a Artist) string {
	return strings.ToLower(strings.TrimSpace(a.EntityID))
}

func mergeArtistInto(dst *Artist, src Artist, match string) {
	dst.Sources = append(dst.Sources, src.Sources...)
	if dst.EntityID == "" {
		dst.EntityID = src.EntityID
	}
	if dst.Match == "" {
		dst.Match = match
	} else if dst.Match == TrackMatchName && match == ArtistMatchMBID {
		dst.Match = ArtistMatchMBID
	}
}

// MergeArtistLibrary folds followed artists across services.
//
// Identity order (issue #4):
//  1. Shared EntityID (MusicBrainz MBID) — durable (Match=mbid)
//  2. Normalized display name — fallback (Match=name)
func MergeArtistLibrary(in []Artist) []Artist {
	if len(in) == 0 {
		return nil
	}
	type entry struct {
		a Artist
	}
	var order []string
	byEntity := map[string]*entry{}
	var nameOnly []Artist

	for _, a := range in {
		if id := ArtistEntityID(a); id != "" {
			if cur, ok := byEntity[id]; ok {
				mergeArtistInto(&cur.a, a, ArtistMatchMBID)
				continue
			}
			cp := a
			cp.EntityID = id
			cp.Sources = append([]ArtistSource(nil), a.Sources...)
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
		k := NormalizeIdentityKey(e.a.Name)
		if k == "" && len(e.a.Sources) > 0 {
			k = "\x00" + e.a.Sources[0].Service + ":" + e.a.Sources[0].ID
		}
		entityByName[k] = append(entityByName[k], e)
	}

	byName := map[string]*entry{}
	for _, a := range nameOnly {
		key := NormalizeIdentityKey(a.Name)
		if key == "" {
			if len(a.Sources) > 0 {
				key = "\x00" + a.Sources[0].Service + ":" + a.Sources[0].ID
			} else {
				continue
			}
		}
		absorbed := false
		for _, e := range entityByName[key] {
			mergeArtistInto(&e.a, a, TrackMatchName)
			absorbed = true
			break
		}
		if absorbed {
			continue
		}
		if e, ok := byName[key]; ok {
			mergeArtistInto(&e.a, a, TrackMatchName)
			continue
		}
		cp := a
		cp.Sources = append([]ArtistSource(nil), a.Sources...)
		byName[key] = &entry{a: cp}
		order = append(order, "n:"+key)
	}

	for _, e := range byEntity {
		if len(e.a.Sources) > 1 {
			e.a.Match = ArtistMatchMBID
		}
	}

	out := make([]Artist, 0, len(order))
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
		ei, ej := ArtistEntityID(out[i]), ArtistEntityID(out[j])
		if ei != "" && ej != "" && ei != ej {
			return ei < ej
		}
		return NormalizeIdentityKey(out[i].Name) < NormalizeIdentityKey(out[j].Name)
	})
	return out
}

// SortPlaylistSourcesCanonicalFirst orders canonical sources before others.
func SortPlaylistSourcesCanonicalFirst(src []PlaylistSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}

// SortTrackSourcesCanonicalFirst orders canonical sources before others.
func SortTrackSourcesCanonicalFirst(src []TrackSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}

// SortContactSourcesCanonicalFirst orders canonical sources before others.
func SortContactSourcesCanonicalFirst(src []ContactSource) {
	sort.SliceStable(src, func(i, j int) bool {
		if src[i].Canonical != src[j].Canonical {
			return src[i].Canonical
		}
		if src[i].ServiceName != src[j].ServiceName {
			return src[i].ServiceName < src[j].ServiceName
		}
		return src[i].Service < src[j].Service
	})
}
