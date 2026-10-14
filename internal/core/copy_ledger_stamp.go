package core

import "github.com/fudomunro/aegis/internal/types"

// Library stamping for the copy ledger (issue #76). Each helper attaches
// CopyInfo to sources[] when a ledger row matches (service, id). Missing
// rows leave sources unchanged — fail-open.

func (s *Server) stampPlaylistCopies(items []types.Playlist) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("playlists", src.Service, src.ID)
		}
	}
}

func (s *Server) stampTrackCopies(items []types.Track) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("tracks", src.Service, src.ID)
		}
	}
}

func (s *Server) stampPhotoCopies(items []types.Photo) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("photos", src.Service, src.ID)
		}
	}
}

func (s *Server) stampMessageCopies(items []types.Message) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("messages", src.Service, src.ID)
		}
	}
}

func (s *Server) stampContactCopies(items []types.Contact) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("contacts", src.Service, src.ID)
		}
	}
}

func (s *Server) stampEventCopies(items []types.CalendarEvent) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("events", src.Service, src.ID)
		}
	}
}
