package kinds

import (
	"context"
	"net/http"
)

type playlistsKind struct {
	providerKind
	h       Host
	e       *Engine
	linkKey func(Playlist) string
	mark    func([]Playlist, string)
	copies  func([]Playlist)
}

func newPlaylistsKind(h Host, e *Engine, d Deps) Kind {
	k := &playlistsKind{h: h, e: e, linkKey: d.PlaylistLinkKey}
	k.mark, k.copies = Stamps(h, "playlists", forEachPlaylistSource)
	def := *BuiltinKindByID("playlists")
	k.providerKind = providerKind{
		def:      def,
		provides: providerProvides[PlaylistProvider],
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
	Aggregate(k.h, k.e, w, r, Spec[Playlist]{
		Kind: kind,
		Targets: func(r *http.Request) ([]NamedLister[Playlist], []string) {
			return Collect[Playlist, PlaylistProvider](k.h, r, kind.PermType,
				func(p PlaylistProvider) func(context.Context) ([]Playlist, bool, error) {
					return p.ListUserPlaylists
				})
		},
		ServiceOf: PlaylistServiceName,
		SortKey:   func(p Playlist) string { return p.Name },
		Merge: func(in []Playlist) []Playlist {
			return MergePlaylistsSyncLinked(in, k.linkKey)
		},
		Encode: func(items []Playlist, page PageRequest, bySvc map[string]LibraryServiceStatus) any {
			pageItems, pr := SlicePage(items, page)
			return PlaylistList{Playlists: pageItems, LibraryListMeta: MetaForPage(pr, true, AnyServiceTruncated(bySvc), bySvc)}
		},
		MarkCanonical: k.mark,
		StampCopies:   k.copies,
	})
}

func forEachPlaylistSource(p *Playlist, f func(SourceMut)) {
	for i := range p.Sources {
		src := &p.Sources[i]
		f(SourceMut{
			Service:       src.Service,
			ID:            src.ID,
			MarkCanonical: func() { src.Canonical = true },
			SetCopy:       func(info *CopyInfo) { src.Copy = info },
		})
	}
}
