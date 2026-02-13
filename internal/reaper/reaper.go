package reaper

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tamcore/ephemeron/internal/metrics"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
)

// Reaper periodically checks for and deletes expired images.
type Reaper struct {
	redis       redisclient.Store
	registryURL string
	logger      *slog.Logger
	httpClient  *http.Client
}

// New creates a new Reaper.
func New(redis redisclient.Store, registryURL string, logger *slog.Logger) *Reaper {
	return &Reaper{
		redis:       redis,
		registryURL: strings.TrimRight(registryURL, "/"),
		logger:      logger,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
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

	r.logger.Info("reap cycle starting", "total_images", len(images))
	metrics.TrackedImagesGauge.Set(float64(len(images)))

	now := time.Now().UnixMilli()

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

		// Get image size before deletion for metrics
		sizeBytes, err := r.redis.GetImageSize(ctx, image)
		if err != nil {
			r.logger.Warn("failed to get image size for metrics", "image", image, "error", err)
			sizeBytes = 0
		}

		if err := r.deleteImage(ctx, image); err != nil {
			r.logger.Error("failed to delete image", "image", image, "error", err)
			continue
		}

		// Update storage metrics
		metrics.ImagesReaped.Inc()
		metrics.BytesReclaimed.Add(float64(sizeBytes))
		metrics.TrackedBytesTotal.Sub(float64(sizeBytes))

		sizeMB := float64(sizeBytes) / (1024 * 1024)
		r.logger.Info("reaped expired image",
			"image", image,
			"size_bytes", sizeBytes,
			"size_mb", fmt.Sprintf("%.2f", sizeMB),
		)
	}

	return nil
}

func (r *Reaper) deleteImage(ctx context.Context, imageWithTag string) error {
	parts := strings.SplitN(imageWithTag, ":", 2)
	if len(parts) != 2 {
		_ = r.redis.RemoveImage(ctx, imageWithTag)
		return fmt.Errorf("invalid image format: %s", imageWithTag)
	}
	repo, tag := parts[0], parts[1]

	// Get the manifest digest via HEAD request.
	headURL := fmt.Sprintf("%s/v2/%s/manifests/%s", r.registryURL, repo, tag)
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return fmt.Errorf("creating HEAD request: %w", err)
	}
	headReq.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	headResp, err := r.httpClient.Do(headReq)
	if err != nil {
		return fmt.Errorf("HEAD manifest: %w", err)
	}
	defer func() { _ = headResp.Body.Close() }()

	if headResp.StatusCode == http.StatusNotFound {
		// Image already gone from registry, just clean up Redis.
		return r.redis.RemoveImage(ctx, imageWithTag)
	}
	if headResp.StatusCode != http.StatusOK {
		return fmt.Errorf("HEAD manifest returned %d", headResp.StatusCode)
	}

	digest := headResp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		// Fall back to ETag like the upstream implementation.
		digest = strings.Trim(headResp.Header.Get("ETag"), `"`)
	}
	if digest == "" {
		return fmt.Errorf("no digest found for %s", imageWithTag)
	}

	// Delete the manifest by digest.
	deleteURL := fmt.Sprintf("%s/v2/%s/manifests/%s", r.registryURL, repo, digest)
	delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("creating DELETE request: %w", err)
	}
	delReq.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	delResp, err := r.httpClient.Do(delReq)
	if err != nil {
		return fmt.Errorf("DELETE manifest: %w", err)
	}
	defer func() { _ = delResp.Body.Close() }()

	validStatus := delResp.StatusCode == http.StatusAccepted ||
		delResp.StatusCode == http.StatusOK ||
		delResp.StatusCode == http.StatusNotFound
	if !validStatus {
		return fmt.Errorf("DELETE manifest returned %d", delResp.StatusCode)
	}

	return r.redis.RemoveImage(ctx, imageWithTag)
}
