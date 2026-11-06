package shared

import (
	"math"
	"strings"
)

// HaversineMeters returns the great-circle distance between two points.
func HaversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	toRad := func(v float64) float64 { return v * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// Iso8601DurationMs parses an ISO-8601 duration (e.g. PT3M42S) into ms.
func Iso8601DurationMs(s string) int {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "PT") {
		return 0
	}
	s = s[2:]
	total := 0
	num := 0
	hasDigit := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= '0' && ch <= '9' {
			num = num*10 + int(ch-'0')
			hasDigit = true
			continue
		}
		if !hasDigit {
			continue
		}
		switch ch {
		case 'H':
			total += num * 3600 * 1000
		case 'M':
			total += num * 60 * 1000
		case 'S':
			total += num * 1000
		}
		num = 0
		hasDigit = false
	}
	return total
}
