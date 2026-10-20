# Google Maps Timeline import

Timeline moved on-device in 2024, so this is a **local import** (like mail
Takeout), not a live API. Export Location History from Google Takeout, then:

```bash
./aegis -import-timeline ~/Downloads/Takeout/Location\ History
## or a monthly Semantic Location History JSON / Takeout zip
```

Or use **Advanced → Timeline import** (`/v1/admin/timeline/import`). Semantic
`placeVisit` rows become visits; `activitySegment` rows become trips with a
normalized `mode` (flight/drive/walk/…). Re-import skips stored rows.

