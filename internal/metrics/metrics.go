package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// WebhookEventsTotal counts registry webhook events received.
	WebhookEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "regmehwf",
		Subsystem: "hooks",
		Name:      "webhook_events_total",
		Help:      "Total number of registry webhook events received.",
	}, []string{"action"})

	// ImagesTracked counts images added to TTL tracking.
	ImagesTracked = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "regmehwf",
		Subsystem: "hooks",
		Name:      "images_tracked_total",
		Help:      "Total number of images added to TTL tracking.",
	})

	// ImagesReaped counts images deleted by the reaper.
	ImagesReaped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "regmehwf",
		Subsystem: "reaper",
		Name:      "images_reaped_total",
		Help:      "Total number of expired images deleted.",
	})

	// ReaperCycleDuration observes the duration of each reap cycle.
	ReaperCycleDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "regmehwf",
		Subsystem: "reaper",
		Name:      "cycle_duration_seconds",
		Help:      "Duration of each reaper cycle in seconds.",
		Buckets:   prometheus.DefBuckets,
	})

	// ReaperCycleErrors counts failed reap cycles.
	ReaperCycleErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "regmehwf",
		Subsystem: "reaper",
		Name:      "cycle_errors_total",
		Help:      "Total number of failed reaper cycles.",
	})

	// TrackedImagesGauge shows the current number of tracked images.
	TrackedImagesGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "regmehwf",
		Subsystem: "reaper",
		Name:      "tracked_images",
		Help:      "Current number of images being tracked for expiry.",
	})
)
