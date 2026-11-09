package kinds

import (
	"context"
	"net/http"
)

type tracksKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []Track)
	mark    func([]Track, string)
	copies  func([]Track)
}

func newTracksKind(h Host, e *Engine, d Deps) Kind {
	k := &tracksKind{h: h, e: e, resolve: d.ResolveTracks}
	k.mark, k.copies = Stamps(h, "tracks", forEachTrackSource)
	def := *BuiltinKindByID("tracks")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[LikedTrackProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *tracksKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[Track]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Track], []string) {
			return Collect[Track, LikedTrackProvider](k.h, r, kind.PermType,
				func(p LikedTrackProvider) func(context.Context) ([]Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		ServiceOf: TrackServiceName,
		SortKey:   TrackIdentityKey,
		Merge:     MergeTracksSync,
		Enrich: func(ctx context.Context, tracks []Track) {
			if k.resolve != nil {
				k.resolve(ctx, tracks)
			}
		},
		Encode: func(items []Track, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return TrackList{Tracks: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachTrackSource(t *Track, f func(SourceMut)) {
	for i := range t.Sources {
		src := &t.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}
