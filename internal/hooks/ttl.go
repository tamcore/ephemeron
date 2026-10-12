package hooks

import (
	"regexp"
	"strconv"
	"time"
)

// durationPattern matches tags like "5m", "1h", "24h", "1d", "1w", "30m", "1h30m".
var durationPattern = regexp.MustCompile(
	`^(?:(\d+)w)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`,
)

var ttlUnits = []time.Duration{7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second}

// ParseTTL parses a duration string from an image tag.
// Returns -1 if the tag is not a valid duration.
func ParseTTL(tag string) time.Duration {
	matches := durationPattern.FindStringSubmatch(tag)
	if tag == "" || matches == nil {
		return -1
	}

	var d time.Duration
	for i, unit := range ttlUnits {
		n, _ := strconv.Atoi(matches[i+1])
		d += time.Duration(n) * unit
	}
	return d
}

// ClampTTL applies default and max TTL limits.
func ClampTTL(d, defaultTTL, maxTTL time.Duration) time.Duration {
	if d <= 0 {
		return defaultTTL
	}
	if d > maxTTL {
		return maxTTL
	}
	return d
}
