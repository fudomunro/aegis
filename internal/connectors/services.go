package connectors

// ServiceName returns the canonical display name for a connector-owned
// service ID. aegis.json "name" is the per-instance label; this registry is
// the canonical one services resolve to (issue #254). Services without a
// connector — custom proxies — return false so the caller can fall back to
// config or the raw ID.
func ServiceName(id string) (string, bool) {
	name, ok := serviceNames[id]
	return name, ok
}

var serviceNames = map[string]string{
	SpotifyServiceID:        SpotifyServiceName,
	YouTubeMusicServiceID:   YouTubeMusicServiceName,
	AmazonMusicServiceID:    AmazonMusicServiceName,
	GoogleGmailServiceID:    GoogleGmailServiceName,
	TakeoutServiceID:        TakeoutServiceName,
	GoogleCalendarServiceID: GoogleCalendarServiceName,
	GoogleContactsServiceID: GoogleContactsServiceName,
	GooglePhotosServiceID:   GooglePhotosServiceName,
	GoogleDriveServiceID:    GoogleDriveServiceName,
	ImmichServiceID:         ImmichServiceName,
	BlueskyServiceID:        BlueskyServiceName,
	YNABServiceID:           YNABServiceName,
	OwnTracksServiceID:      OwnTracksServiceName,
	CalDAVServiceID:         CalDAVServiceName,
	CardDAVServiceID:        CardDAVServiceName,
	PaperlessServiceID:      PaperlessServiceName,
	SampleCalendarServiceID: SampleCalendarServiceName,
	SignalServiceID:         SignalServiceName,
	QuickenServiceID:        QuickenServiceName,
	TimelineServiceID:       TimelineServiceName,
	LinkedInServiceID:       LinkedInServiceName,
	GitHubServiceID:         GitHubServiceName,
}

// ServiceTitleAliases maps unambiguous title phrases onto canonical service
// IDs (issue #254). Maintenance attribution uses them to name a service from
// an issue title; a match is kept only when it resolves to a configured
// service. Keys are lowercase.
func ServiceTitleAliases() map[string]string { return serviceTitleAliases }

var serviceTitleAliases = map[string]string{
	"spotify":          SpotifyServiceID,
	"youtube music":    YouTubeMusicServiceID,
	"youtube":          YouTubeMusicServiceID,
	"amazon music":     AmazonMusicServiceID,
	"gmail":            GoogleGmailServiceID,
	"google photos":    GooglePhotosServiceID,
	"google contacts":  GoogleContactsServiceID,
	"google calendar":  GoogleCalendarServiceID,
	"google drive":     GoogleDriveServiceID,
	"google timeline":  TimelineServiceID,
	"immich":           ImmichServiceID,
	"bluesky":          BlueskyServiceID,
	"ynab":             YNABServiceID,
	"owntracks":        OwnTracksServiceID,
	"paperless":        PaperlessServiceID,
	"quicken":          QuickenServiceID,
	"signal messenger": SignalServiceID,
	"linkedin":         LinkedInServiceID,
	"github":           GitHubServiceID,
}

// CommonWordIDs are service IDs that are also common English words. A bare
// substring match would swallow unrelated titles ("maintenance signal
// bundle"), so only their alias phrases may attribute them (issue #254).
func CommonWordIDs() map[string]bool { return commonWordIDs }

var commonWordIDs = map[string]bool{
	GoogleContactsServiceID: true, // "google"
	SignalServiceID:         true, // "signal"
}

// LocalImportStoreIDs are the service IDs of the local import stores: file
// based, no OAuth or static credentials. They show on the Advanced import
// panels, not the configured-services list (issues #141, #142, #168).
// Derived from LocalImportStoreSpecs() — the registry is the only list.
func LocalImportStoreIDs() map[string]bool {
	specs := LocalImportStoreSpecs()
	out := make(map[string]bool, len(specs))
	for _, s := range specs {
		out[s.Service.ID] = true
	}
	return out
}

// PhotoShareDefaultTarget is the default destination for photo shares
// (issue #20). Declared here so core never names a share provider.
const PhotoShareDefaultTarget = BlueskyServiceID
