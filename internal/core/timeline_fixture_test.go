package core

// sampleTimelineJSON is a minimal Semantic Location History month used by
// admin import tests (kept in core alongside the admin surface).
const sampleTimelineJSON = `{
  "timelineObjects": [
    {
      "placeVisit": {
        "location": {
          "latitudeE7": 374220000,
          "longitudeE7": -1220840000,
          "placeId": "ChIJ-googleplex",
          "address": "Googleplex, 1600 Amphitheatre Pkwy, Mountain View, CA, USA",
          "name": "Googleplex"
        },
        "duration": {
          "startTimestamp": "2023-01-15T10:00:00Z",
          "endTimestamp": "2023-01-15T18:00:00Z"
        },
        "placeConfidence": "HIGH",
        "visitConfidence": 90
      }
    },
    {
      "activitySegment": {
        "startLocation": {
          "latitudeE7": 374220000,
          "longitudeE7": -1220840000
        },
        "endLocation": {
          "latitudeE7": 377749000,
          "longitudeE7": -1224194000,
          "placeId": "ChIJ-sf",
          "address": "San Francisco, CA, USA",
          "name": "San Francisco"
        },
        "duration": {
          "startTimestamp": "2023-01-15T18:00:00Z",
          "endTimestamp": "2023-01-15T19:30:00Z"
        },
        "activityType": "IN_PASSENGER_VEHICLE"
      }
    },
    {
      "placeVisit": {
        "location": {
          "latitudeE7": 377749000,
          "longitudeE7": -1224194000,
          "placeId": "ChIJ-sf",
          "address": "San Francisco, CA, USA",
          "name": "San Francisco"
        },
        "duration": {
          "startTimestamp": "2023-01-15T19:30:00Z",
          "endTimestamp": "2023-01-16T09:00:00Z"
        },
        "placeConfidence": "MEDIUM"
      }
    }
  ]
}`
