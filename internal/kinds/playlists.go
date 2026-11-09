package kinds

import (
	"context"
	"net/http"

	"github.com/fudomunro/aegis/internal/types"
)

type playlistsKind struct {
	providerKind
	h       Host
	e       *Engine
	linkKey func(types.Playlist) string
	mark    func([]types.Playlist, string)
	copies  func([]types.Playlist)
}

func newPlaylistsKind(h Host, e *Engine, d Deps) Kind {
	k := &playlistsKind{h: h, e: e, linkKey: d.PlaylistLinkKey}
	k.mark, k.copies = Stamps(h, "playlists", forEachPlaylistSource)
	def := *BuiltinKindByID("playlists")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[types.PlaylistProvider],
		routes: []Route{{
			Pattern:     def.Path,
			AppReadable: def.AppReadable,
			Handler:     k.serve,
		}},
	}
	return k
}

func (k *playlistsKind) serve(w http.ResponseWriter, r *http.Request) {
	kind := k.def
	Aggregate(k.h, k.e, w, r, Spec[types.Playlist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[types.Playlist], []string) {
			return Collect[types.Playlist, types.PlaylistProvider](k.h, r, kind.PermType,
				func(p types.PlaylistProvider) func(context.Context) ([]types.Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		ServiceOf: types.PlaylistServiceName,
		SortKey:   func(p types.Playlist) string { return p.Name },
		Merge: func(in []types.Playlist) []types.Playlist {
			return types.MergePlaylistsSyncLinked(in, k.linkKey)
		},
		Encode: func(items []types.Playlist, page types.PageRequest, bySvc map[string]types.LibraryServiceStatus) any {
			pageItems, pr := types.SlicePage(items, page)
			return types.PlaylistList{Playlists: pageItems, LibraryListMeta: types.MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachPlaylistSource(p *types.Playlist, f func(SourceMut)) {
	for i := range p.Sources {
		src := &p.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *types.CopyInfo) { src.Copy = info },
		})
	}
}
