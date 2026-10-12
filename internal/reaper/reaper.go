package reaper

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tamcore/ephemeron/internal/metrics"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
	"github.com/tamcore/ephemeron/internal/registry"
)

// HealthReporter is called by the reaper to report registry interaction outcomes.
type HealthReporter interface {
	ReportSuccess()
	ReportFailure()
}

// Reaper periodically checks for and deletes expired images.
type Reaper struct {
	redis    redisclient.Store
	registry *registry.Client
	logger   *slog.Logger
	health   HealthReporter
}

// New creates a new Reaper. health may be nil.
func New(redis redisclient.Store, reg *registry.Client, logger *slog.Logger, health HealthReporter) *Reaper {
	return &Reaper{redis: redis, registry: reg, logger: logger, health: health}
}

// RunLoop starts the reaper loop, ticking at the given interval.
// It blocks until the context is cancelled.
func (r *Reaper) RunLoop(ctx context.Context, interval time.Duration) {
	r.logger.Info("starting reaper loop", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reaper loop stopped")
			return
		case <-ticker.C:
			if err := r.ReapOnce(ctx); err != nil {
				r.logger.Error("reap cycle failed", "error", err)
			}
		}
	}
}

// ReapOnce performs a single reap pass — checking all tracked images and
// deleting those that have expired. Uses a Redis lock to ensure only one
// replica runs the reaper at a time.
func (r *Reaper) ReapOnce(ctx context.Context) error {
	r.updateTrackedGauge(ctx)

	acquired, err := r.redis.AcquireReaperLock(ctx, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("acquiring reaper lock: %w", err)
	}
	if !acquired {
		r.logger.Debug("another replica holds the reaper lock, skipping")
		return nil
	}
	defer func() { _ = r.redis.ReleaseReaperLock(ctx) }()

	start := time.Now()
	defer func() {
		metrics.ReaperCycleDuration.Observe(time.Since(start).Seconds())
	}()

	images, err := r.redis.ListImages(ctx)
	if err != nil {
		metrics.ReaperCycleErrors.Inc()
		return fmt.Errorf("listing images: %w", err)
	}

	// An empty registry produces one of these per interval — keep that at
	// debug so steady-state logs stay quiet.
	cycleLog := r.logger.Info
	if len(images) == 0 {
		cycleLog = r.logger.Debug
	}
	cycleLog("reap cycle starting", "total_images", len(images))

	now := time.Now().UnixMilli()

	var attempted, failed int

	for _, image := range images {
		if err := ctx.Err(); err != nil {
			return err
		}

		expiresAt, err := r.redis.GetExpiry(ctx, image)
		if err != nil {
			r.logger.Warn("failed to get expiry, cleaning up", "image", image, "error", err)
			_ = r.redis.RemoveImage(ctx, image)
			continue
		}

		if expiresAt > now {
			remaining := time.Duration(expiresAt-now) * time.Millisecond
			r.logger.Debug("image not expired yet",
				"image", image,
				"remaining", remaining.Round(time.Second).String(),
			)
			continue
		}

		sizeBytes, err := r.redis.GetImageSize(ctx, image)
		if err != nil {
			r.logger.Warn("failed to get image size for metrics", "image", image, "error", err)
		}

		attempted++
		if err := r.deleteImage(ctx, image); err != nil {
			r.logger.Error("failed to delete image", "image", image, "error", err)
			failed++
			continue
		}

		metrics.ImagesReaped.Inc()
		metrics.BytesReclaimed.Add(float64(sizeBytes))
		metrics.TrackedBytesTotal.Sub(float64(sizeBytes))

		r.logger.Info("reaped expired image", "image", image, "size_bytes", sizeBytes)
	}

	// Report registry health based on deletion outcomes.
	// Only report when we actually attempted deletions — cycles with
	// no expired images are neutral and should not affect health state.
	if r.health != nil && attempted > 0 {
		if failed == attempted {
			r.health.ReportFailure()
		} else {
			r.health.ReportSuccess()
		}
	}

	return nil
}

// updateTrackedGauge runs on every replica, also on those that do not hold the reaper lock.
func (r *Reaper) updateTrackedGauge(ctx context.Context) {
	n, err := r.redis.ImageCount(ctx)
	if err != nil {
		r.logger.Warn("failed to count tracked images", "error", err)
		return
	}
	metrics.TrackedImagesGauge.Set(float64(n))
}

func (r *Reaper) deleteImage(ctx context.Context, imageWithTag string) error {
	repo, tag, ok := strings.Cut(imageWithTag, ":")
	if !ok {
		_ = r.redis.RemoveImage(ctx, imageWithTag)
		return fmt.Errorf("invalid image format: %s", imageWithTag)
	}
	if err := r.registry.DeleteManifest(ctx, repo, tag); err != nil {
		return err
	}
	return r.redis.RemoveImage(ctx, imageWithTag)
}
