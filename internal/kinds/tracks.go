package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type tracksKind struct {
	providerKind
	h       Host
	e       *Engine
	resolve func(context.Context, []types.Track)
	mark    func([]types.Track, string)
	copies  func([]types.Track)
}

func newTracksKind(h Host, e *Engine, d Deps) Kind {
	k := &tracksKind{h: h, e: e, resolve: d.ResolveTracks}
	k.mark, k.copies = Stamps(h, "tracks", forEachTrackSource)
	def := *BuiltinKindByID("tracks")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.LikedTrackProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[types.Track]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Track], []string) {
			return Collect[types.Track, types.LikedTrackProvider](k.h, r, kind.PermType,
				func(p types.LikedTrackProvider) func(context.Context) ([]types.Track, bool, error) {
					return p.ListUserLikedTracks
				})
		},
		ServiceOf: types.TrackServiceName,
		SortKey:   types.TrackIdentityKey,
		Merge:     types.MergeTracksSync,
		Enrich: func(ctx context.Context, tracks []types.Track) {
			if k.resolve != nil {
				k.resolve(ctx, tracks)
			}
		},
		Encode: func(items []types.Track, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.TrackList{Tracks: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachTrackSource(t *types.Track, f func(SourceMut)) {
	for i := range t.Sources {
		src := &t.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
