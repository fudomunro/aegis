package types

import (
	"net/http"
	"testing"
)

func TestPermissions_AllowsAndClone(t *testing.T) {
	p := Permissions{"tracks": AccessRead, "playlists": AccessNone, "default": AccessWrite}
	if !p.AllowsRead("tracks") || p.AllowsWrite("tracks") {
		t.Fatalf("tracks read-only: %+v", p)
	}
	if p.AllowsRead("playlists") || p.AllowsWrite("playlists") {
		t.Fatalf("playlists none: %+v", p)
	}
	if !p.AllowsWrite("search") {
		t.Fatalf("default write should allow search: %+v", p)
	}
	if !p.Allows("tracks", http.MethodGet) || p.Allows("tracks", http.MethodPost) {
		t.Fatalf("Allows method: %+v", p)
	}
	c := p.Clone()
	if c["tracks"] != AccessRead || len(c) != 3 {
		t.Fatalf("clone = %+v", c)
	}
	c["tracks"] = AccessNone
	if p["tracks"] != AccessRead {
		t.Fatal("clone must be deep enough to not alias levels")
	}
	if got := p.DeniedDataTypes(); len(got) != 1 || got[0] != "playlists" {
		t.Fatalf("DeniedDataTypes = %v", got)
	}
}

func TestPermissions_RedactionLists(t *testing.T) {
	p := Permissions{
		"events":       AccessFreeBusy,
		"transactions": AccessSummary,
		"places":       AccessCity,
		"tracks":       AccessRead,
	}
	if got := p.FreeBusyDataTypes(); len(got) != 1 || got[0] != "events" {
		t.Fatalf("FreeBusyDataTypes = %v", got)
	}
	if got := p.SummaryDataTypes(); len(got) != 1 || got[0] != "transactions" {
		t.Fatalf("SummaryDataTypes = %v", got)
	}
	if got := p.PlacesAccessLevels(); got["places"] != AccessCity {
		t.Fatalf("PlacesAccessLevels = %v", got)
	}
	if !IsFreeBusyAccess(AccessFreeBusy) || IsFreeBusyAccess(AccessRead) {
		t.Fatal("IsFreeBusyAccess")
	}
	if !IsSummaryAccess(AccessSummary) || IsSummaryAccess(AccessRead) {
		t.Fatal("IsSummaryAccess")
	}
	if !IsCityAccess(AccessCity) || !IsPreciseAccess(AccessPrecise) {
		t.Fatal("IsCity/Precise")
	}
	if got := PlacesLevelForResponse(AccessCity); got != AccessCity {
		t.Fatalf("PlacesLevelForResponse city = %q", got)
	}
}

func TestPermissions_AccessForDataType(t *testing.T) {
	p := Permissions{"tracks": AccessNone, "default": AccessRead}
	if got := p.AccessForDataType("tracks"); got != AccessNone {
		t.Fatalf("classified tracks = %q", got)
	}
	// Unclassified uses Strictest (none), never default.
	if got := p.AccessForDataType(""); got != AccessNone {
		t.Fatalf("unclassified = %q, want strictest none", got)
	}
}

func TestValidatePermissions_RedactionScoping(t *testing.T) {
	if err := ValidatePermissions(Permissions{"events": AccessFreeBusy}); err != nil {
		t.Fatalf("events freebusy: %v", err)
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessFreeBusy}); err == nil {
		t.Fatal("freebusy must reject non-events")
	}
	if err := ValidatePermissions(Permissions{"places": AccessCity}); err != nil {
		t.Fatalf("places city: %v", err)
	}
	if err := ValidatePermissions(Permissions{"tracks": AccessCity}); err == nil {
		t.Fatal("city must reject non-places")
	}
	if err := ValidatePermissions(Permissions{"default": AccessFreeBusy}); err == nil {
		t.Fatal("default freebusy must reject")
	}
}

func TestAllowsMethod_Levels(t *testing.T) {
	if !AllowsMethod(AccessRead, http.MethodGet) || AllowsMethod(AccessRead, http.MethodPost) {
		t.Fatal("read")
	}
	if !AllowsMethod(AccessWrite, http.MethodPost) || AllowsMethod(AccessWrite, http.MethodGet) {
		t.Fatal("write")
	}
	if !AllowsMethod(AccessFull, http.MethodDelete) || !AllowsMethod(AccessFull, http.MethodGet) {
		t.Fatal("full")
	}
	if !AllowsMethod(AccessNone, "") == false {
		// AccessNone never allows
	}
	if AllowsMethod(AccessNone, http.MethodGet) {
		t.Fatal("none must deny")
	}
}

func TestFilterCapabilities_Known(t *testing.T) {
	caps := ServiceCapabilities{DataTypes: []DataTypeCapability{
		{Name: "tracks", Operations: []Operation{
			{Name: "list", Method: "GET"},
			{Name: "save", Method: "POST"},
		}},
		{Name: "playlists", Operations: []Operation{{Name: "list", Method: "GET"}}},
	}}
	filtered := FilterCapabilities(caps, Permissions{"tracks": AccessRead, "playlists": AccessNone})
	if len(filtered.DataTypes) != 1 || filtered.DataTypes[0].Name != "tracks" {
		t.Fatalf("filtered = %+v", filtered)
	}
	if len(filtered.DataTypes[0].Operations) != 1 {
		t.Fatalf("ops = %+v", filtered.DataTypes[0].Operations)
	}
	if !DataTypeKnown(caps, "tracks") || DataTypeKnown(caps, "nope") {
		t.Fatal("DataTypeKnown")
	}
	if DataTypeHasWriteOp(caps, "playlists") || !DataTypeHasWriteOp(caps, "tracks") {
		t.Fatal("DataTypeHasWriteOp")
	}
	if DataTypeHasAgentWriteOp(caps, "playlists") || !DataTypeHasAgentWriteOp(caps, "tracks") {
		t.Fatal("DataTypeHasAgentWriteOp")
	}
	if !IsMutatingMethod(http.MethodPost) || IsMutatingMethod(http.MethodGet) {
		t.Fatal("IsMutatingMethod")
	}
	writable := WritableDataTypesFromCapabilities(caps)
	if len(writable) != 1 || writable[0] != "tracks" {
		t.Fatalf("writable = %v", writable)
	}
}

func TestIsAgentDataWritePath(t *testing.T) {
	if IsAgentDataWritePath("/picker/sessions") || IsAgentDataWritePath("/search") {
		t.Fatal("picker/search are not agent data writes")
	}
	if !IsAgentDataWritePath("/me/tracks") {
		t.Fatal("library write path should count")
	}
}

func TestLibraryKind_MentionsInstructionsCacheKey(t *testing.T) {
	k := LibraryKind{
		ID: "widgets", Path: "/v1/library/widgets", Label: "widgets",
		Usage: "widgets", Summary: "widgets", SyncRule: "merge",
		ExampleQuestion: "what widgets?",
		Keywords:        []string{"my widgets"},
	}
	if !k.Mentions("show my widgets please") || k.Mentions("play some jazz") {
		t.Fatal("Mentions")
	}
	s := k.Instructions("https://aegis.example.com/")
	if s == "" {
		t.Fatal("Instructions empty")
	}
	if k.CacheKey() != "library:widgets" {
		t.Fatalf("CacheKey = %q", k.CacheKey())
	}
}

func TestMergeHelpers(t *testing.T) {
	a := NewArtist("spotify", "Spotify", "id1", "  A  ")
	if a.Name != "A" || a.Sources[0].ID != "id1" {
		t.Fatalf("NewArtist = %+v", a)
	}
	if NewAlbum("spotify", "Spotify", "1", "  ") != nil {
		t.Fatal("NewAlbum empty name")
	}
	src := PrimaryPlaylistSource("spotify", "Spotify", "p1", "https://x", map[string]any{"k": 1})
	if src.Service != "spotify" || src.ID != "p1" || src.URL != "https://x" {
		t.Fatalf("PrimaryPlaylistSource = %+v", src)
	}
	p := Playlist{Sources: []PlaylistSource{{Service: "a", ID: "1"}}}
	if FirstSourceID(p) != "1" {
		t.Fatalf("FirstSourceID = %q", FirstSourceID(p))
	}
	if got := ArtistEntityID(Artist{EntityID: "mb"}); got != "mb" {
		t.Fatalf("ArtistEntityID = %q", got)
	}
	if got := MergeArtistLibrary([]Artist{{Name: "A", Sources: []ArtistSource{{Service: "s", ID: "1"}}}}); len(got) != 1 {
		t.Fatalf("MergeArtistLibrary = %+v", got)
	}
	sources := []TrackSource{
		{Service: "b", ServiceName: "B"},
		{Service: "a", ServiceName: "A", Canonical: true},
	}
	SortTrackSourcesCanonicalFirst(sources)
	if !sources[0].Canonical || sources[0].Service != "a" {
		t.Fatalf("SortTrackSourcesCanonicalFirst = %+v", sources)
	}
	sources2 := []PlaylistSource{{Service: "b"}, {Service: "a", Canonical: true}}
	SortPlaylistSourcesCanonicalFirst(sources2)
	if !sources2[0].Canonical {
		t.Fatalf("SortPlaylistSourcesCanonicalFirst = %+v", sources2)
	}
	sources3 := []ContactSource{{Service: "b"}, {Service: "a", Canonical: true}}
	SortContactSourcesCanonicalFirst(sources3)
	if !sources3[0].Canonical {
		t.Fatalf("SortContactSourcesCanonicalFirst = %+v", sources3)
	}
}

func TestCanonicalService(t *testing.T) {
	p := Playlist{Sources: []PlaylistSource{{Service: "other"}, {Service: "home", Canonical: true}}}
	if p.CanonicalService() != "home" {
		t.Fatalf("playlist canonical = %q", p.CanonicalService())
	}
	tr := Track{Sources: []TrackSource{{Service: "only"}}}
	if tr.CanonicalService() != "only" {
		t.Fatalf("track primary = %q", tr.CanonicalService())
	}
	c := Contact{Sources: []ContactSource{{Service: "x"}, {Service: "y", Canonical: true}}}
	if c.CanonicalService() != "y" {
		t.Fatalf("contact = %q", c.CanonicalService())
	}
	ph := Photo{Sources: []PhotoSource{{Service: "p", Canonical: true}}}
	if ph.CanonicalService() != "p" {
		t.Fatalf("photo = %q", ph.CanonicalService())
	}
	m := Message{Sources: []MessageSource{{Service: "m"}}}
	if m.CanonicalService() != "m" {
		t.Fatalf("message = %q", m.CanonicalService())
	}
	e := CalendarEvent{Sources: []CalendarEventSource{{Service: "e", Canonical: true}}}
	if e.CanonicalService() != "e" {
		t.Fatalf("event = %q", e.CanonicalService())
	}
}

func TestNormalizeAccessAndTypePolicy(t *testing.T) {
	if NormalizeAccess("READ") != AccessRead {
		t.Fatal("NormalizeAccess READ")
	}
	if NormalizeAccess("") != "" {
		t.Fatal("empty stays empty")
	}
	tp := TypePolicy{Default: AccessRead, Providers: map[string]string{"spotify": AccessNone}}
	if tp.For("spotify") != AccessNone || tp.For("other") != AccessRead {
		t.Fatalf("TypePolicy.For = %+v", tp)
	}
	if (TypePolicies{"a": {Default: AccessWrite}}).For("a").Default != AccessWrite {
		t.Fatal("TypePolicies.For")
	}
}

func TestServiceNameHelpersAndPrimary(t *testing.T) {
	p := Playlist{Sources: []PlaylistSource{{ServiceName: "Spotify"}}}
	if PlaylistServiceName(p) != "Spotify" {
		t.Fatalf("PlaylistServiceName = %q", PlaylistServiceName(p))
	}
	if PlaylistServiceName(Playlist{}) != "" {
		t.Fatal("empty playlist")
	}
	tr := Track{Sources: []TrackSource{{ServiceName: "YT"}}}
	if TrackServiceName(tr) != "YT" || TrackServiceName(Track{}) != "" {
		t.Fatal("TrackServiceName")
	}
	al := Album{Sources: []AlbumSource{{ServiceName: "AM"}}}
	if AlbumServiceName(al) != "AM" || AlbumServiceName(Album{}) != "" {
		t.Fatal("AlbumServiceName")
	}
	ar := Artist{Sources: []ArtistSource{{ServiceName: "SP"}}}
	if ArtistServiceName(ar) != "SP" || ArtistServiceName(Artist{}) != "" {
		t.Fatal("ArtistServiceName")
	}
	pl := Playlist{Sources: []PlaylistSource{{Service: "b", ID: "2"}, {Service: "a", ID: "1", Canonical: true}}}
	if pl.PrimaryService() != "b" {
		t.Fatalf("PrimaryService = %q", pl.PrimaryService())
	}
}

func TestStrictestAndForDefaults(t *testing.T) {
	var empty Permissions
	if empty.Strictest() != DefaultAccessType {
		t.Fatalf("empty strictest = %q", empty.Strictest())
	}
	if empty.For("tracks") != DefaultAccessType {
		t.Fatalf("empty For = %q", empty.For("tracks"))
	}
	// Mixed read/write on unknown path must deny.
	mixed := Permissions{"tracks": AccessRead, "playlists": AccessWrite}
	if mixed.Strictest() != AccessNone {
		t.Fatalf("mixed strictest = %q", mixed.Strictest())
	}
	// Single tight level wins.
	if (Permissions{"tracks": AccessNone}).Strictest() != AccessNone {
		t.Fatal("none strictest")
	}
	if (Permissions{"tracks": AccessRead}).Strictest() != AccessRead {
		t.Fatal("read strictest")
	}
	// opt-in places: default key never enables.
	p := Permissions{PermissionDefaultKey: AccessFull}
	if p.For("places") != AccessNone {
		t.Fatalf("places default = %q", p.For("places"))
	}
	p2 := Permissions{"places": AccessCity}
	if p2.For("places") != AccessCity {
		t.Fatalf("places named = %q", p2.For("places"))
	}
}

func TestPlacesLevelForResponse_Levels(t *testing.T) {
	if PlacesLevelForResponse(AccessPrecise) != AccessPrecise {
		t.Fatal("precise keeps precise")
	}
	if PlacesLevelForResponse(AccessRead) != AccessRead {
		t.Fatal("read keeps names")
	}
	if PlacesLevelForResponse(AccessFull) != AccessPrecise {
		t.Fatal("full is precise for places")
	}
	if PlacesLevelForResponse(AccessCity) != AccessCity {
		t.Fatal("city")
	}
}

func TestValidatePermissions_More(t *testing.T) {
	if err := ValidatePermissions(Permissions{"places": AccessPrecise}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePermissions(Permissions{"places": AccessSummary}); err == nil {
		t.Fatal("summary on places")
	}
	if err := ValidatePermissions(Permissions{"transactions": AccessSummary}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePermissions(Permissions{"transactions": AccessCity}); err == nil {
		t.Fatal("city on transactions")
	}
	if err := ValidatePermissions(Permissions{"": AccessRead}); err == nil {
		t.Fatal("empty key")
	}
	if err := ValidatePermissions(Permissions{"tracks": "maybe"}); err == nil {
		t.Fatal("unknown level")
	}
}

func TestMergeArtistLibrary_NameMerge(t *testing.T) {
	merged := MergeArtistLibrary([]Artist{
		{Name: "A", Sources: []ArtistSource{{Service: "s1", ID: "1"}}},
		{Name: "a", Sources: []ArtistSource{{Service: "s2", ID: "2"}}},
		{Name: "B", Sources: []ArtistSource{{Service: "s1", ID: "3"}}},
	})
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestDataTypeForPath_QueryAndSlash(t *testing.T) {
	if DataTypeForPath("/me/tracks?limit=1") != "tracks" {
		t.Fatal("query strip")
	}
	if DataTypeForPath("   ") != "" {
		t.Fatal("blank")
	}
	if DataTypeForPath("/me/../secret") != "" {
		t.Fatal("dotdot must be non-canonical")
	}
}
