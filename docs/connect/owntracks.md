# OwnTracks

Live, local-first location history that fits the Immich/self-hosted audience.
Configure a service named `owntracks` with `endpoint` pointing at an OwnTracks
Recorder:

```json
{
 "id": "owntracks",
 "name": "OwnTracks",
 "endpoint": "https://recorder.example:8083",
 "headers": {"X-API-Key": "…"},
 "grant_limits": {"places": "read"}
}
```

The connector prefers Recorder `/api/v0/visit` rows and otherwise clusters
`/api/v0/locations` samples into dwell visits (and short trips between them).

