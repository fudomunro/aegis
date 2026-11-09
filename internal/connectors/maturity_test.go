package connectors

import "testing"

func TestFieldCoverageFor(t *testing.T) {
	got := FieldCoverageFor("artists", "name")
	if len(got) != 2 {
		t.Fatalf("artists content fields = %d, want 2", len(got))
	}
	if !got[0].Supported || got[0].Name != "name" {
		t.Fatalf("artists[0] = %+v, want name supported", got[0])
	}
	if got[1].Supported || got[1].Name != "entity_id" {
		t.Fatalf("artists[1] = %+v, want entity_id unsupported", got[1])
	}
}

func TestFieldCoverageForIgnoresUnknownPopulated(t *testing.T) {
	got := FieldCoverageFor("tracks", "name", "sources", "popularity")
	for _, f := range got {
		if f.Name != "name" && f.Supported {
			t.Fatalf("field %q marked supported; only name is populated", f.Name)
		}
	}
	if len(got) != len(MaturityContentFields["tracks"]) {
		t.Fatalf("tracks graded %d fields, want %d", len(got), len(MaturityContentFields["tracks"]))
	}
}

func TestFieldCoverageForNoModel(t *testing.T) {
	if got := FieldCoverageFor("search", "q"); got != nil {
		t.Fatalf("search field coverage = %+v, want nil", got)
	}
}

func TestGradeConnectionMaturityComplete(t *testing.T) {
	m := ConnectionMaturity{
		Service:   "spotify",
		Transport: TransportAPI,
		Kinds: []KindMaturity{
			{Name: "tracks", Supported: true, Fields: FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "duration", "entity_id")},
			{Name: "artists", Supported: true, Fields: FieldCoverageFor("artists", "name", "entity_id")},
		},
	}
	g := GradeConnectionMaturity(m)
	if g.Level != "complete" {
		t.Fatalf("level = %q, want complete (pct %v)", g.Level, g.CoveragePct)
	}
	if g.CoveragePct != 100 || g.FieldPct == nil || *g.FieldPct != 100 {
		t.Fatalf("grade = %+v, want 100/100", g)
	}
	if g.Offered != 2 || g.Supported != 2 {
		t.Fatalf("offered/supported = %d/%d, want 2/2", g.Offered, g.Supported)
	}
}

func TestGradeConnectionMaturityBandsOnLowerAxis(t *testing.T) {
	// All types supported, but half the fields missing: the level must
	// follow fields (50% → partial), not the 100% type coverage.
	m := ConnectionMaturity{
		Kinds: []KindMaturity{
			{Name: "tracks", Supported: true, Fields: FieldCoverageFor("tracks", "name", "artists", "album")},
			{Name: "search", Supported: true},
		},
	}
	g := GradeConnectionMaturity(m)
	if g.Level != "partial" {
		t.Fatalf("level = %q, want partial", g.Level)
	}
	if g.CoveragePct != 100 {
		t.Fatalf("coverage = %v, want 100", g.CoveragePct)
	}
	if g.FieldPct == nil || *g.FieldPct < 49 || *g.FieldPct > 51 {
		t.Fatalf("field pct = %v, want ~50", g.FieldPct)
	}
}

func TestGradeConnectionMaturityUnsupportedTypesCount(t *testing.T) {
	m := ConnectionMaturity{
		Kinds: []KindMaturity{
			{Name: "tracks", Supported: true, Fields: FieldCoverageFor("tracks", "name", "artists", "album", "duration_ms", "duration", "entity_id")},
			{Name: "shows", Supported: false},
			{Name: "audiobooks", Supported: false},
		},
	}
	g := GradeConnectionMaturity(m)
	if g.Level != "basic" {
		t.Fatalf("level = %q, want basic (1 of 3 types)", g.Level)
	}
	if g.CoveragePct < 33 || g.CoveragePct > 34 {
		t.Fatalf("coverage = %v, want ~33", g.CoveragePct)
	}
	if g.FieldPct == nil || *g.FieldPct != 100 {
		t.Fatalf("field pct = %v, want 100 (fields look fine; coverage does not)", g.FieldPct)
	}
}

func TestGradeConnectionMaturityNoModelTypes(t *testing.T) {
	// Operational types only: no field axis, level follows coverage.
	m := ConnectionMaturity{
		Kinds: []KindMaturity{
			{Name: "user", Supported: true},
			{Name: "search", Supported: true},
			{Name: "player", Supported: false},
		},
	}
	g := GradeConnectionMaturity(m)
	if g.FieldPct != nil {
		t.Fatalf("field pct = %v, want nil", g.FieldPct)
	}
	if g.Level != "partial" {
		t.Fatalf("level = %q, want partial (~67%%)", g.Level)
	}

	threeOfFour := m
	threeOfFour.Kinds = append(append([]KindMaturity{}, m.Kinds...), KindMaturity{Name: "library", Supported: true})
	if got := GradeConnectionMaturity(threeOfFour).Level; got != "substantial" {
		t.Fatalf("3 of 4 level = %q, want substantial (75%%)", got)
	}
}

func TestGradeConnectionMaturityEmpty(t *testing.T) {
	g := GradeConnectionMaturity(ConnectionMaturity{Service: "x"})
	if g.Level != "basic" || g.Offered != 0 {
		t.Fatalf("empty grade = %+v, want basic/0", g)
	}
}

func TestMaturityLevelThresholds(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{100, "complete"},
		{99.998, "substantial"},
		{75, "substantial"},
		{74.9, "partial"},
		{40, "partial"},
		{39.9, "basic"},
		{0, "basic"},
	}
	for _, c := range cases {
		if got := maturityLevel(c.pct); got != c.want {
			t.Errorf("maturityLevel(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

func TestUnsupportedKindsAreTheGap(t *testing.T) {
	m := ConnectionMaturity{Kinds: []KindMaturity{
		{Name: "tracks", Supported: true},
		{Name: "shows", Supported: false},
		{Name: "audiobooks", Supported: false},
	}}
	var unsupported, names []string
	for _, dt := range m.Kinds {
		names = append(names, dt.Name)
		if !dt.Supported {
			unsupported = append(unsupported, dt.Name)
		}
	}
	if len(unsupported) != 2 || unsupported[0] != "shows" || unsupported[1] != "audiobooks" {
		t.Fatalf("unsupported = %v, want [shows audiobooks]", unsupported)
	}
	if len(names) != 3 || names[0] != "tracks" {
		t.Fatalf("type names = %v, want catalog order", names)
	}
}
