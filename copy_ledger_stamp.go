package main

// Library stamping for the copy ledger (issue #76). Each helper attaches
// CopyInfo to sources[] when a ledger row matches (service, id). Missing
// rows leave sources unchanged — fail-open.

func (s *Server) stampPlaylistCopies(items []Playlist) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("playlists", src.Service, src.ID)
		}
	}
}

func (s *Server) stampTrackCopies(items []Track) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("tracks", src.Service, src.ID)
		}
	}
}

func (s *Server) stampPhotoCopies(items []Photo) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("photos", src.Service, src.ID)
		}
	}
}

func (s *Server) stampMessageCopies(items []Message) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("messages", src.Service, src.ID)
		}
	}
}

func (s *Server) stampContactCopies(items []Contact) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("contacts", src.Service, src.ID)
		}
	}
}

func (s *Server) stampEventCopies(items []CalendarEvent) {
	for i := range items {
		for j := range items[i].Sources {
			src := &items[i].Sources[j]
			src.Copy = s.copyInfoFor("events", src.Service, src.ID)
		}
	}
}
