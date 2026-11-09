package sampledata

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// SampleLibraryServiceIDPrefix is the config service id prefix for fixture
// library services. Bind each with endpoint sample://library and a dataset.
const SampleLibraryServiceIDPrefix = "sample-library-"

// SampleLibraryEndpoint marks a service as fixture-backed library data.
const SampleLibraryEndpoint = "sample://library"

// SampleLibraryConnector serves one SampleLibrarySet as every library
// provider type. It is the demo/query fixture analog of a live connector
// (issue #77) and the data half of data-scope isolation (docs/data-scopes.md):
// each service instance owns exactly one dataset partition.
type SampleLibraryConnector struct {
	serviceID   string
	serviceName string
	dataset     string
	set         SampleLibrarySet
}

// NewSampleLibraryConnector builds a fixture connector for one dataset.
func NewSampleLibraryConnector(serviceID, dataset string, set SampleLibrarySet) *SampleLibraryConnector {
	name := "Sample Library"
	if dataset != "" {
		name = "Sample Library (" + dataset + ")"
	}
	return &SampleLibraryConnector{
		serviceID:   serviceID,
		serviceName: name,
		dataset:     dataset,
		set:         set,
	}
}

// IsSampleLibraryService reports whether a service should get fixture
// library data instead of a live provider.
func IsSampleLibraryService(id, endpoint string) bool {
	return endpoint == SampleLibraryEndpoint ||
		strings.HasPrefix(id, SampleLibraryServiceIDPrefix)
}

func (c *SampleLibraryConnector) Capabilities() shared.ServiceCapabilities {
	kinds := []string{
		"playlists", "tracks", "albums", "artists", "contacts",
		"photos", "events", "messages", "transactions", "places", "documents",
	}
	dts := make([]shared.KindCapability, 0, len(kinds))
	for _, k := range kinds {
		dts = append(dts, shared.KindCapability{
			Name:        k,
			Description: "Synthetic fixture data (no upstream provider).",
			Operations: []shared.Operation{
				{
					Name:        "list",
					Description: "List synthetic " + k + " as neutral library values.",
					Method:      "GET",
					Path:        "/me/" + k,
					Response: shared.ResponseSpec{
						Description: "Neutral " + k + " for the bound dataset partition.",
						Fields:      []string{k, "total"},
					},
				},
			},
		})
	}
	return shared.ServiceCapabilities{Kinds: dts}
}

func (c *SampleLibraryConnector) SystemInstructions(baseURL string) string {
	return fmt.Sprintf("Sample Library (%s) is synthetic fixture data. Use Aegis library endpoints under %s/v1/library/; there is no upstream provider.",
		c.dataset, strings.TrimRight(baseURL, "/"))
}

func (c *SampleLibraryConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	shared.WriteJSONError(w, http.StatusNotFound, "sample library data is only available through /v1/library")
}

func (c *SampleLibraryConnector) ListUserPlaylists(context.Context) ([]kinds.Playlist, bool, error) {
	return c.set.Playlists, false, nil
}

func (c *SampleLibraryConnector) ListUserLikedTracks(context.Context) ([]kinds.Track, bool, error) {
	return c.set.Tracks, false, nil
}

func (c *SampleLibraryConnector) ListUserSavedAlbums(context.Context) ([]kinds.Album, bool, error) {
	return c.set.Albums, false, nil
}

func (c *SampleLibraryConnector) ListUserFollowedArtists(context.Context) ([]kinds.Artist, bool, error) {
	return c.set.Artists, false, nil
}

func (c *SampleLibraryConnector) ListUserContacts(context.Context) ([]kinds.Contact, bool, error) {
	return c.set.Contacts, false, nil
}

func (c *SampleLibraryConnector) ListUserPhotos(context.Context) ([]kinds.Photo, bool, error) {
	return c.set.Photos, false, nil
}

func (c *SampleLibraryConnector) ListCalendarEvents(context.Context) ([]kinds.CalendarEvent, bool, error) {
	return c.set.Events, false, nil
}

func (c *SampleLibraryConnector) ListMessages(context.Context) ([]kinds.Message, bool, error) {
	return c.set.Messages, false, nil
}

func (c *SampleLibraryConnector) ListUserTransactions(context.Context) ([]kinds.Transaction, bool, error) {
	return c.set.Transactions, false, nil
}

func (c *SampleLibraryConnector) ListUserPlaceVisits(context.Context) ([]kinds.PlaceVisit, bool, error) {
	return c.set.Places, false, nil
}

func (c *SampleLibraryConnector) ListUserDocuments(context.Context) ([]kinds.Document, bool, error) {
	return c.set.Documents, false, nil
}

// FetchDocumentText implements DocumentProvider for the gated text endpoint.
// Sample documents are metadata-only; return a short synthetic excerpt.
func (c *SampleLibraryConnector) FetchDocumentText(_ context.Context, id string) (string, bool, error) {
	for _, d := range c.set.Documents {
		for _, src := range d.Sources {
			if src.ID == id {
				if d.Excerpt != "" {
					return d.Excerpt, false, nil
				}
				return "Synthetic sample document (" + c.dataset + "): " + d.Title, false, nil
			}
		}
	}
	return "", false, fmt.Errorf("document %q not found", id)
}

// Compile-time checks: missing provider methods silently drop the connector
// from library fan-out (type assert fails) — catch that at build time.
var (
	_ kinds.PlaylistProvider      = (*SampleLibraryConnector)(nil)
	_ kinds.LikedTrackProvider    = (*SampleLibraryConnector)(nil)
	_ kinds.AlbumProvider         = (*SampleLibraryConnector)(nil)
	_ kinds.ArtistProvider        = (*SampleLibraryConnector)(nil)
	_ kinds.ContactProvider       = (*SampleLibraryConnector)(nil)
	_ kinds.PhotoProvider         = (*SampleLibraryConnector)(nil)
	_ kinds.CalendarEventProvider = (*SampleLibraryConnector)(nil)
	_ kinds.MessageProvider       = (*SampleLibraryConnector)(nil)
	_ kinds.TransactionProvider   = (*SampleLibraryConnector)(nil)
	_ kinds.PlaceVisitProvider    = (*SampleLibraryConnector)(nil)
	_ kinds.DocumentProvider      = (*SampleLibraryConnector)(nil)
	_ connectors.ServiceConnector = (*SampleLibraryConnector)(nil)
)
