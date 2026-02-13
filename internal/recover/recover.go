package recover

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tamcore/ephemeron/internal/hooks"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
	"github.com/tamcore/ephemeron/internal/registry"
)

// Runner recovers image tracking state by scanning the registry catalog.
type Runner struct {
	redis      redisclient.Store
	registry   *registry.Client
	defaultTTL time.Duration
	maxTTL     time.Duration
	logger     *slog.Logger
}

// New creates a new recovery runner.
func New(
	redis redisclient.Store,
	registry *registry.Client,
	defaultTTL, maxTTL time.Duration,
	logger *slog.Logger,
) *Runner {
	return &Runner{
		redis:      redis,
		registry:   registry,
		defaultTTL: defaultTTL,
		maxTTL:     maxTTL,
		logger:     logger,
	}
}

// Run scans the registry catalog, parses TTLs from tags, and re-populates
// Redis with tracking data. It is idempotent — re-tracking an already-tracked
// image simply overwrites its metadata.
func (r *Runner) Run(ctx context.Context) error {
	repos, err := r.registry.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("listing repositories: %w", err)
	}

	r.logger.Info("starting recovery", "repositories", len(repos))

	var recovered int
	var totalBytes int64
	for _, repo := range repos {
		tags, err := r.registry.ListTags(ctx, repo)
		if err != nil {
			r.logger.Warn("failed to list tags, skipping repo", "repo", repo, "error", err)
			continue
		}

		for _, tag := range tags {
			ttl := hooks.ClampTTL(hooks.ParseTTL(tag), r.defaultTTL, r.maxTTL)
			expiresAt := time.Now().Add(ttl)
			imageWithTag := fmt.Sprintf("%s:%s", repo, tag)

			// Fetch manifest info - best effort
			var sizeBytes int64
			var digest string

			manifestInfo, err := r.registry.GetImageManifestInfo(ctx, repo, tag)
			if err != nil {
				r.logger.Warn("failed to fetch manifest info during recovery",
					"image", imageWithTag,
					"error", err,
				)
				sizeBytes = 0
				digest = ""
			} else {
				sizeBytes = manifestInfo.SizeBytes
				digest = manifestInfo.Digest
			}

			if err := r.redis.TrackImage(ctx, imageWithTag, expiresAt, sizeBytes, digest); err != nil {
				r.logger.Error("failed to track image", "image", imageWithTag, "error", err)
				continue
			}

			r.logger.Debug("recovered image",
				"image", imageWithTag,
				"ttl", ttl.String(),
				"size_bytes", sizeBytes,
				"digest", digest,
			)
			recovered++
			totalBytes += sizeBytes
		}
	}

	totalMB := float64(totalBytes) / (1024 * 1024)
	r.logger.Info("recovery complete",
		"images_recovered", recovered,
		"total_bytes", totalBytes,
		"total_mb", fmt.Sprintf("%.2f", totalMB),
	)
	return nil
}

// RunIfNeeded checks whether Redis has been initialized. If not, it runs
// recovery and marks Redis as initialized.
func (r *Runner) RunIfNeeded(ctx context.Context) error {
	initialized, err := r.redis.IsInitialized(ctx)
	if err != nil {
		return fmt.Errorf("checking initialization state: %w", err)
	}

	if initialized {
		r.logger.Debug("redis already initialized, skipping recovery")
		return nil
	}

	r.logger.Info("redis not initialized, starting recovery")

	if err := r.Run(ctx); err != nil {
		return err
	}

	if err := r.redis.SetInitialized(ctx); err != nil {
		return fmt.Errorf("setting initialized flag: %w", err)
	}

	return nil
}
