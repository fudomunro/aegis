package core

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
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
func IsSampleLibraryService(svc *Service) bool {
	if svc == nil {
		return false
	}
	return svc.Endpoint == SampleLibraryEndpoint ||
		strings.HasPrefix(svc.ID, SampleLibraryServiceIDPrefix)
}

func (c *SampleLibraryConnector) Capabilities() types.ServiceCapabilities {
	kinds := []string{
		"playlists", "tracks", "albums", "artists", "contacts",
		"photos", "events", "messages", "transactions", "places", "documents",
	}
	dts := make([]types.DataTypeCapability, 0, len(kinds))
	for _, k := range kinds {
		dts = append(dts, types.DataTypeCapability{
			Name:        k,
			Description: "Synthetic fixture data (no upstream provider).",
			Operations: []types.Operation{
				{
					Name:        "list",
					Description: "List synthetic " + k + " as neutral library values.",
					Method:      "GET",
					Path:        "/me/" + k,
					Response: types.ResponseSpec{
						Description: "Neutral " + k + " for the bound dataset partition.",
						Fields:      []string{k, "total"},
					},
				},
			},
		})
	}
	return types.ServiceCapabilities{DataTypes: dts}
}

func (c *SampleLibraryConnector) SystemInstructions(baseURL string) string {
	return fmt.Sprintf("Sample Library (%s) is synthetic fixture data. Use Aegis library endpoints under %s/v1/library/; there is no upstream provider.",
		c.dataset, strings.TrimRight(baseURL, "/"))
}

func (c *SampleLibraryConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSONError(w, http.StatusNotFound, "sample library data is only available through /v1/library")
}

func (c *SampleLibraryConnector) ListUserPlaylists(context.Context) ([]types.Playlist, bool, error) {
	return c.set.Playlists, false, nil
}

func (c *SampleLibraryConnector) ListUserLikedTracks(context.Context) ([]types.Track, bool, error) {
	return c.set.Tracks, false, nil
}

func (c *SampleLibraryConnector) ListUserSavedAlbums(context.Context) ([]types.Album, bool, error) {
	return c.set.Albums, false, nil
}

func (c *SampleLibraryConnector) ListUserFollowedArtists(context.Context) ([]types.Artist, bool, error) {
	return c.set.Artists, false, nil
}

func (c *SampleLibraryConnector) ListUserContacts(context.Context) ([]types.Contact, bool, error) {
	return c.set.Contacts, false, nil
}

func (c *SampleLibraryConnector) ListUserPhotos(context.Context) ([]types.Photo, bool, error) {
	return c.set.Photos, false, nil
}

func (c *SampleLibraryConnector) ListCalendarEvents(context.Context) ([]types.CalendarEvent, bool, error) {
	return c.set.Events, false, nil
}

func (c *SampleLibraryConnector) ListMessages(context.Context) ([]types.Message, bool, error) {
	return c.set.Messages, false, nil
}

func (c *SampleLibraryConnector) ListUserTransactions(context.Context) ([]types.Transaction, bool, error) {
	return c.set.Transactions, false, nil
}

func (c *SampleLibraryConnector) ListUserPlaceVisits(context.Context) ([]types.PlaceVisit, bool, error) {
	return c.set.Places, false, nil
}

func (c *SampleLibraryConnector) ListUserDocuments(context.Context) ([]types.Document, bool, error) {
	return c.set.Documents, false, nil
}
