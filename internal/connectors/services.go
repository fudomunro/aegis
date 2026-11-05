package connectors

// ServiceName returns the canonical display name for a connector-owned
// service ID. aegis.json "name" is the per-instance label; this registry is
// the canonical one services resolve to (issue #254). Services without a
// connector — custom proxies and core-local import stores — return false so
// the caller can fall back to config or the raw ID.
func ServiceName(id string) (string, bool) {
	name, ok := serviceNames[id]
	return name, ok
}

var serviceNames = map[string]string{
	SpotifyServiceID:        SpotifyServiceName,
	YouTubeMusicServiceID:   YouTubeMusicServiceName,
	AmazonMusicServiceID:    AmazonMusicServiceName,
	GoogleGmailServiceID:    GoogleGmailServiceName,
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
}
