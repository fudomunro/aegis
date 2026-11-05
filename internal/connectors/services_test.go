package connectors

import "testing"

func TestServiceName(t *testing.T) {
	cases := []struct {
		id   string
		name string
	}{
		{SpotifyServiceID, SpotifyServiceName},
		{YouTubeMusicServiceID, YouTubeMusicServiceName},
		{AmazonMusicServiceID, AmazonMusicServiceName},
		{GoogleGmailServiceID, GoogleGmailServiceName},
		{GoogleCalendarServiceID, GoogleCalendarServiceName},
		{GoogleContactsServiceID, GoogleContactsServiceName},
		{GooglePhotosServiceID, GooglePhotosServiceName},
		{GoogleDriveServiceID, GoogleDriveServiceName},
		{ImmichServiceID, ImmichServiceName},
		{BlueskyServiceID, BlueskyServiceName},
		{YNABServiceID, YNABServiceName},
		{OwnTracksServiceID, OwnTracksServiceName},
		{CalDAVServiceID, CalDAVServiceName},
		{CardDAVServiceID, CardDAVServiceName},
		{PaperlessServiceID, PaperlessServiceName},
		{SampleCalendarServiceID, SampleCalendarServiceName},
	}
	for _, tc := range cases {
		name, ok := ServiceName(tc.id)
		if !ok || name != tc.name {
			t.Errorf("ServiceName(%q) = %q, %v; want %q, true", tc.id, name, ok, tc.name)
		}
	}
	for _, id := range []string{"", "tidal", "quicken", "signal", "google-takeout", "mail-archive"} {
		if name, ok := ServiceName(id); ok {
			t.Errorf("ServiceName(%q) = %q, true; want false for non-connector services", id, name)
		}
	}
}
