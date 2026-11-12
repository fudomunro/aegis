package kinds

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Identity, normalization, and query-filter helpers shared by the core merge
// pipeline and the connector-family import stores. Identity keys live beside
// TrackIdentityKey in this package so every transport computes them the same
// way (issue #266).

// ExtraString reads a string Extra field.
func ExtraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// NormalizeHandleList normalizes contact handles and drops duplicates.
func NormalizeHandleList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, h := range in {
		n := NormalizeContactHandle(h)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TransactionDay returns the calendar day (UTC) of a posted/authorized time.
func TransactionDay(raw string) string {
	n := NormalizeTransactionDate(raw)
	if n == "" {
		return ""
	}
	if len(n) >= 10 {
		return n[:10]
	}
	return n
}

// DateAtOrAfter reports whether day is on/after the bound's calendar day.
func DateAtOrAfter(day, bound string) bool {
	b := TransactionDay(bound)
	if b == "" {
		return true
	}
	return day >= b
}

// DateBefore reports whether day is strictly before the bound's calendar day.
func DateBefore(day, bound string) bool {
	b := TransactionDay(bound)
	if b == "" {
		return true
	}
	return day < b
}

// NormalizeTransactionAmount parses a signed decimal string into a canonical
// form ("+0.00" / "-12.34") so the same money value matches across providers.
// Accepts optional leading +/-. Thousand separators are stripped.
func NormalizeTransactionAmount(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Normalize to at least two decimal places without floating money.
	if !strings.Contains(s, ".") {
		s += ".00"
	} else {
		parts := strings.SplitN(s, ".", 2)
		frac := parts[1]
		for len(frac) < 2 {
			frac += "0"
		}
		s = parts[0] + "." + frac
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return strings.TrimSpace(raw)
	}
	if neg {
		return "-" + s
	}
	return s
}

// TransactionMerchantOrDescription returns the best merchant-ish string.
func TransactionMerchantOrDescription(t Transaction) string {
	if m := strings.TrimSpace(t.Merchant); m != "" {
		return m
	}
	return strings.TrimSpace(t.Description)
}

// TransactionServiceName returns the first source service name for stable sort.
func TransactionServiceName(t Transaction) string {
	if len(t.Sources) > 0 {
		return t.Sources[0].ServiceName
	}
	return ""
}

// TransactionIdentityKey is the sort/merge key. Prefer durable provider IDs
// when present; otherwise amount + posted day + normalized merchant.
// Transactions without any identity fall back to service:id so they stay
// distinct.
func TransactionIdentityKey(t Transaction) string {
	if id := strings.ToLower(strings.TrimSpace(t.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range t.Sources {
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "import_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "fitid"))); id != "" {
			return "pid:" + id
		}
	}
	amt := NormalizeTransactionAmount(t.Amount)
	merchant := NormalizeIdentityKey(TransactionMerchantOrDescription(t))
	day := TransactionDay(t.PostedAt)
	if day == "" {
		day = TransactionDay(t.AuthorizedAt)
	}
	if amt == "" || merchant == "" || day == "" {
		if len(t.Sources) > 0 && t.Sources[0].Service != "" && t.Sources[0].ID != "" {
			return "src:" + t.Sources[0].Service + ":" + t.Sources[0].ID
		}
		return "anon:" + TransactionServiceName(t) + ":" + t.Amount + ":" + t.PostedAt
	}
	return "amd:" + amt + "|d:" + day + "|m:" + merchant
}

// PlaceVisitServiceName returns the first source service name for stable sort.
func PlaceVisitServiceName(v PlaceVisit) string {
	if len(v.Sources) > 0 {
		return v.Sources[0].ServiceName
	}
	return ""
}

// PlaceMergeKey is the durable-ish place identity: locality+name when named,
// otherwise coordinates rounded to ~100m.
func PlaceMergeKey(p Place) string {
	name := NormalizeIdentityKey(p.Name)
	if name == "" {
		name = NormalizeIdentityKey(p.Address)
	}
	loc := NormalizeIdentityKey(p.Locality)
	country := NormalizeIdentityKey(p.Country)
	if name != "" || loc != "" {
		return "n:" + name + "|l:" + loc + "|c:" + country
	}
	if p.Lat != 0 || p.Lng != 0 {
		return "g:" + strconv.FormatFloat(RoundCoord(p.Lat), 'f', 3, 64) +
			"," + strconv.FormatFloat(RoundCoord(p.Lng), 'f', 3, 64)
	}
	return ""
}

// RoundCoord rounds to ~0.001° (~100m) so provider jitter does not split
// the same place across merge keys.
func RoundCoord(v float64) float64 {
	return float64(int(v*1000+math.Copysign(0.5, v))) / 1000
}

// PlaceIdentityKey is the sort/merge key. Prefer durable provider IDs when
// present; otherwise start day + end day + normalized place key. Visits
// without any identity fall back to service:id so they stay distinct.
func PlaceIdentityKey(v PlaceVisit) string {
	if id := strings.ToLower(strings.TrimSpace(v.EntityID)); id != "" {
		return "pid:" + id
	}
	for _, src := range v.Sources {
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "provider_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(ExtraString(src.Extra, "place_id"))); id != "" {
			return "pid:" + id
		}
		if id := strings.ToLower(strings.TrimSpace(src.ID)); id != "" {
			return "pid:" + id
		}
	}
	start := NormalizePlaceVisitTime(v.StartAt)
	end := NormalizePlaceVisitTime(v.EndAt)
	placeKey := PlaceMergeKey(v.Place)
	if start == "" || placeKey == "" {
		if len(v.Sources) > 0 && v.Sources[0].Service != "" && v.Sources[0].ID != "" {
			return "src:" + v.Sources[0].Service + ":" + v.Sources[0].ID
		}
		return "anon:" + PlaceVisitServiceName(v) + ":" + v.StartAt
	}
	return "wp:" + start + "|" + end + "|" + placeKey
}

// --- placement identity (issue #366) ---
//
// Placement and existence filters match the way the merge layer does: a
// durable catalog id first, then a duration-guarded name. A missed match
// duplicates an item on the next converge run; a false match hides one, so
// the ladder mirrors MergeTracksSync exactly — distinct durable ids stay
// separate even when the names collide, and name matches require
// compatible lengths.

// isrcFormat matches the ISRC shape (CC-XXX-YY-NNNNN, dashes optional).
var isrcFormat = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

// upcFormat matches a UPC/barcode shape (11–14 digits) — what distinguishes
// an album's UPC EntityID from a MusicBrainz MBID in match reporting.
var upcFormat = regexp.MustCompile(`^[0-9]{11,14}$`)

// TrackMatchKey reports the strongest identity key a track carries and
// which match method it came from (TrackMatchISRC, TrackMatchMBID, or
// TrackMatchName) — recorded per placed item so matches are explainable.
func TrackMatchKey(t Track) (key, how string) {
	if id := TrackEntityID(t); id != "" {
		if isrcFormat.MatchString(id) {
			return "id:" + id, TrackMatchISRC
		}
		// MusicBrainz recording enrichment lands in EntityID too (issue
		// #4); any other durable id rides the same equality path.
		return "id:" + id, TrackMatchMBID
	}
	return "name:" + TrackIdentityKey(t), TrackMatchName
}

// TracksMatch reports whether two tracks are the same recording (issue
// #366): shared durable id first, else duration-guarded name. Mirrors the
// MergeTracksSync absorption rules — a name-only track matches an id'd one
// when the names agree and the lengths fit, but two different durable ids
// never match even when the names collide.
func TracksMatch(a, b Track) bool {
	ia, ib := TrackEntityID(a), TrackEntityID(b)
	if ia != "" && ib != "" {
		return ia == ib
	}
	if TrackIdentityKey(a) != TrackIdentityKey(b) {
		return false
	}
	return DurationCompatible(a, b)
}

// TrackMatcher indexes a track list for "is this item already here?" checks
// with the TracksMatch ladder — the existence filter placement and repair
// need. Zero value is not ready; use NewTrackMatcher.
type TrackMatcher struct {
	byID   map[string]bool
	byName map[string][]Track
}

// NewTrackMatcher indexes the tracks a location already holds.
func NewTrackMatcher(tracks []Track) *TrackMatcher {
	m := &TrackMatcher{
		byID:   make(map[string]bool),
		byName: make(map[string][]Track),
	}
	for _, t := range tracks {
		m.Add(t)
	}
	return m
}

// Add indexes one more held track.
func (m *TrackMatcher) Add(t Track) {
	if m == nil {
		return
	}
	if id := TrackEntityID(t); id != "" {
		m.byID[id] = true
	}
	k := TrackIdentityKey(t)
	m.byName[k] = append(m.byName[k], t)
}

// Has reports whether the indexed list already holds this track.
func (m *TrackMatcher) Has(t Track) bool {
	if m == nil {
		return false
	}
	if id := TrackEntityID(t); id != "" && m.byID[id] {
		return true
	}
	for _, c := range m.byName[TrackIdentityKey(t)] {
		if TracksMatch(t, c) {
			return true
		}
	}
	return false
}

// AlbumMatchKey reports the strongest identity key an album carries and
// which match method it came from (AlbumMatchUPC, AlbumMatchMBID, or
// TrackMatchName) — recorded per placed item so matches are explainable.
func AlbumMatchKey(a Album) (key, how string) {
	if id := AlbumEntityID(a); id != "" {
		if upcFormat.MatchString(id) {
			return "id:" + id, AlbumMatchUPC
		}
		return "id:" + id, AlbumMatchMBID
	}
	return "name:" + AlbumIdentityKey(a), TrackMatchName
}

// AlbumsMatch reports whether two albums are the same release (issue #375):
// shared durable id first, else title + primary artist. Mirrors
// MergeAlbumsSync — distinct durable ids stay separate even when the
// titles collide.
func AlbumsMatch(a, b Album) bool {
	ia, ib := AlbumEntityID(a), AlbumEntityID(b)
	if ia != "" && ib != "" {
		return ia == ib
	}
	return AlbumIdentityKey(a) == AlbumIdentityKey(b)
}

// AlbumMatcher indexes an album list for "is this item already here?"
// checks with the AlbumsMatch ladder — the existence filter placement and
// convergence need. Zero value is not ready; use NewAlbumMatcher.
type AlbumMatcher struct {
	byID   map[string]bool
	byName map[string][]Album
}

// NewAlbumMatcher indexes the albums a location already holds.
func NewAlbumMatcher(albums []Album) *AlbumMatcher {
	m := &AlbumMatcher{
		byID:   make(map[string]bool),
		byName: make(map[string][]Album),
	}
	for _, a := range albums {
		m.Add(a)
	}
	return m
}

// Add indexes one more held album.
func (m *AlbumMatcher) Add(a Album) {
	if m == nil {
		return
	}
	if id := AlbumEntityID(a); id != "" {
		m.byID[id] = true
	}
	k := AlbumIdentityKey(a)
	m.byName[k] = append(m.byName[k], a)
}

// Has reports whether the indexed list already holds this album.
func (m *AlbumMatcher) Has(a Album) bool {
	if m == nil {
		return false
	}
	if id := AlbumEntityID(a); id != "" && m.byID[id] {
		return true
	}
	for _, c := range m.byName[AlbumIdentityKey(a)] {
		if AlbumsMatch(a, c) {
			return true
		}
	}
	return false
}

// ArtistMatchKey reports the strongest identity key an artist carries and
// which match method it came from (ArtistMatchMBID or TrackMatchName).
func ArtistMatchKey(a Artist) (key, how string) {
	if id := ArtistEntityID(a); id != "" {
		return "id:" + id, ArtistMatchMBID
	}
	return "name:" + NormalizeIdentityKey(a.Name), TrackMatchName
}

// ArtistsMatch reports whether two artists are the same act (issue #375):
// shared MBID first, else normalized name. Mirrors MergeArtistLibrary —
// nameless artists with no source never match.
func ArtistsMatch(a, b Artist) bool {
	ia, ib := ArtistEntityID(a), ArtistEntityID(b)
	if ia != "" && ib != "" {
		return ia == ib
	}
	ka, kb := artistNameKey(a), artistNameKey(b)
	return ka != "" && ka == kb
}

// artistNameKey is the artist name-fallback identity: normalized name, else
// the first service-local id when the artist has no name (the merge layer's
// name-group key).
func artistNameKey(a Artist) string {
	if k := NormalizeIdentityKey(a.Name); k != "" {
		return k
	}
	if len(a.Sources) > 0 {
		return "\x00" + a.Sources[0].Service + ":" + a.Sources[0].ID
	}
	return ""
}

// ArtistMatcher indexes an artist list for "is this item already here?"
// checks with the ArtistsMatch ladder. Zero value is not ready; use
// NewArtistMatcher.
type ArtistMatcher struct {
	byID   map[string]bool
	byName map[string][]Artist
}

// NewArtistMatcher indexes the artists a location already holds.
func NewArtistMatcher(artists []Artist) *ArtistMatcher {
	m := &ArtistMatcher{
		byID:   make(map[string]bool),
		byName: make(map[string][]Artist),
	}
	for _, a := range artists {
		m.Add(a)
	}
	return m
}

// Add indexes one more held artist.
func (m *ArtistMatcher) Add(a Artist) {
	if m == nil {
		return
	}
	if id := ArtistEntityID(a); id != "" {
		m.byID[id] = true
	}
	k := artistNameKey(a)
	m.byName[k] = append(m.byName[k], a)
}

// Has reports whether the indexed list already holds this artist.
func (m *ArtistMatcher) Has(a Artist) bool {
	if m == nil {
		return false
	}
	if id := ArtistEntityID(a); id != "" && m.byID[id] {
		return true
	}
	for _, c := range m.byName[artistNameKey(a)] {
		if ArtistsMatch(a, c) {
			return true
		}
	}
	return false
}

// ContactsMatch reports whether two contacts are the same person: any
// shared identity key — vCard UID, email, phone, handle — or the weak
// display-name fallback both sides are eligible for. This is the
// MergeContacts edge relation, applied pairwise: a shared vCard UID folds
// cards even when their emails drifted apart, and a shared email folds
// cards that carry different UIDs (the UID names the card, the email names
// the person — two cards of one person may carry different UIDs).
func ContactsMatch(a, b Contact) bool {
	kb := map[string]bool{}
	for _, k := range ContactIdentityKeys(b) {
		kb[k] = true
	}
	for _, k := range ContactIdentityKeys(a) {
		if kb[k] {
			return true
		}
	}
	return false
}

// ContactMatcher indexes a contact list for "is this person already here?"
// checks with the ContactsMatch relation — the existence filter convergence
// and conflict resolution need. Zero value is not ready; use
// NewContactMatcher.
type ContactMatcher struct {
	byKey map[string]bool
}

// NewContactMatcher indexes the contacts a location already holds.
func NewContactMatcher(contacts []Contact) *ContactMatcher {
	m := &ContactMatcher{byKey: make(map[string]bool)}
	for _, c := range contacts {
		m.Add(c)
	}
	return m
}

// Add indexes one more held contact.
func (m *ContactMatcher) Add(c Contact) {
	if m == nil {
		return
	}
	for _, k := range ContactIdentityKeys(c) {
		m.byKey[k] = true
	}
}

// Has reports whether the indexed list already holds this person.
func (m *ContactMatcher) Has(c Contact) bool {
	if m == nil {
		return false
	}
	for _, k := range ContactIdentityKeys(c) {
		if m.byKey[k] {
			return true
		}
	}
	return false
}
