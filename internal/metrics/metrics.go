package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// WebhookEventsTotal counts registry webhook events received.
	WebhookEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "hooks",
		Name:      "webhook_events_total",
		Help:      "Total number of registry webhook events received.",
	}, []string{"action"})

	// ImagesTracked counts images added to TTL tracking.
	ImagesTracked = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "hooks",
		Name:      "images_tracked_total",
		Help:      "Total number of images added to TTL tracking.",
	})

	// ImagesReaped counts images deleted by the reaper.
	ImagesReaped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "reaper",
		Name:      "images_reaped_total",
		Help:      "Total number of expired images deleted.",
	})

	// ReaperCycleDuration observes the duration of each reap cycle.
	ReaperCycleDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "ephemeron",
		Subsystem: "reaper",
		Name:      "cycle_duration_seconds",
		Help:      "Duration of each reaper cycle in seconds.",
		Buckets:   prometheus.DefBuckets,
	})

	// ReaperCycleErrors counts failed reap cycles.
	ReaperCycleErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "reaper",
		Name:      "cycle_errors_total",
		Help:      "Total number of failed reaper cycles.",
	})

	// TrackedImagesGauge shows the current number of tracked images.
	TrackedImagesGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "ephemeron",
		Subsystem: "reaper",
		Name:      "tracked_images",
		Help:      "Current number of images being tracked for expiry.",
	})

	// TrackedBytesTotal shows the total storage currently tracked.
	TrackedBytesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "ephemeron",
		Subsystem: "storage",
		Name:      "tracked_bytes_total",
		Help:      "Total storage in bytes currently tracked for expiry.",
	})

	// BytesReclaimed counts total storage reclaimed by deletion.
	BytesReclaimed = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "storage",
		Name:      "bytes_reclaimed_total",
		Help:      "Total storage in bytes reclaimed by deleting expired images.",
	})

	// ImageSizeBytes observes the size distribution of tracked images.
	ImageSizeBytes = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "ephemeron",
		Subsystem: "storage",
		Name:      "image_size_bytes",
		Help:      "Size distribution of tracked images in bytes.",
		Buckets: []float64{
			1048576, 10485760, 52428800, 104857600, 262144000,
			524288000, 1073741824, 2147483648, 5368709120, 10737418240,
		}, // 1MB to 10GB
	})

	// ImageSizeFetchErrors counts failures to fetch image size from registry.
	ImageSizeFetchErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "hooks",
		Name:      "image_size_fetch_errors_total",
		Help:      "Total number of failures fetching image size from registry.",
	})

	// TagOverwritesTotal counts detected tag overwrites.
	TagOverwritesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "immutability",
		Name:      "tag_overwrites_total",
		Help:      "Total tag overwrites detected (same tag, different digest).",
	}, []string{"repository"})

	// OverwrittenImageAge observes age of images when overwritten.
	OverwrittenImageAge = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "ephemeron",
		Subsystem: "immutability",
		Name:      "overwritten_image_age_seconds",
		Help:      "Age in seconds of previous image when tag was overwritten.",
		Buckets: []float64{
			60, 300, 900, 1800, 3600, // 1m, 5m, 15m, 30m, 1h
			7200, 21600, 43200, 86400, // 2h, 6h, 12h, 24h
			172800, 604800, 2592000, // 2d, 7d, 30d
		},
	})

	// DigestFetchErrors counts digest fetch failures.
	DigestFetchErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "immutability",
		Name:      "digest_fetch_errors_total",
		Help:      "Total failures fetching digest from registry.",
	})

	// ImmutableTagViolations counts blocked overwrites in enforcement mode.
	ImmutableTagViolations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ephemeron",
		Subsystem: "immutability",
		Name:      "immutable_tag_violations_total",
		Help:      "Total overwrite attempts blocked by immutability enforcement.",
	}, []string{"repository", "tag"})
)
