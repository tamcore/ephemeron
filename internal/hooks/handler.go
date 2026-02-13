package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/tamcore/ephemeron/internal/metrics"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
	"github.com/tamcore/ephemeron/internal/registry"
)

// RegistryEvent represents a single event from the Docker Registry webhook.
type RegistryEvent struct {
	Action string      `json:"action"`
	Target EventTarget `json:"target"`
}

// EventTarget contains the repository and tag from a registry event.
type EventTarget struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// EventEnvelope is the top-level structure sent by the Docker Registry.
type EventEnvelope struct {
	Events []RegistryEvent `json:"events"`
}

// registryClient is the subset of registry operations needed by the handler.
type registryClient interface {
	GetImageSize(ctx context.Context, repo, tag string) (int64, error)
	GetImageManifestInfo(ctx context.Context, repo, tag string) (*registry.ManifestInfo, error)
}

// Handler handles incoming registry webhook events.
type Handler struct {
	redis                redisclient.Store
	registry             registryClient
	hookToken            string
	defaultTTL           time.Duration
	maxTTL               time.Duration
	logger               *slog.Logger
	immutableTagPatterns []string
}

// NewHandler creates a new webhook handler.
func NewHandler(
	redis redisclient.Store,
	registry registryClient,
	hookToken string,
	defaultTTL, maxTTL time.Duration,
	immutableTagPatterns []string,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		redis:                redis,
		registry:             registry,
		hookToken:            hookToken,
		defaultTTL:           defaultTTL,
		maxTTL:               maxTTL,
		immutableTagPatterns: immutableTagPatterns,
		logger:               logger,
	}
}

// ServeHTTP handles POST /v1/hook/registry-event.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	auth := r.Header.Get("Authorization")
	if auth != fmt.Sprintf("Token %s", h.hookToken) {
		h.logger.Warn("unauthorized webhook request")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("{}"))
		return
	}

	var envelope EventEnvelope
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		h.logger.Error("failed to decode webhook body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	for _, event := range envelope.Events {
		metrics.WebhookEventsTotal.WithLabelValues(event.Action).Inc()

		if event.Action != "push" {
			continue
		}
		if event.Target.Repository == "" || event.Target.Tag == "" {
			continue
		}
		if err := h.handlePush(ctx, event.Target.Repository, event.Target.Tag); err != nil {
			h.logger.Error("failed to handle push event",
				"image", event.Target.Repository,
				"tag", event.Target.Tag,
				"error", err,
			)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (h *Handler) handlePush(ctx context.Context, repo, tag string) error {
	imageWithTag := fmt.Sprintf("%s:%s", repo, tag)

	ttl := ClampTTL(ParseTTL(tag), h.defaultTTL, h.maxTTL)
	expiresAt := time.Now().Add(ttl)

	// Fetch manifest info (digest + size) - best effort
	var manifestInfo *registry.ManifestInfo
	var sizeBytes int64
	var digest string

	manifestInfo, err := h.registry.GetImageManifestInfo(ctx, repo, tag)
	if err != nil {
		h.logger.Warn("failed to fetch manifest info, tracking without digest",
			"image", imageWithTag,
			"error", err,
		)
		sizeBytes = 0
		digest = ""
		metrics.DigestFetchErrors.Inc()
	} else {
		sizeBytes = manifestInfo.SizeBytes
		digest = manifestInfo.Digest
	}

	// Detect tag overwrite (may block webhook in enforcement mode)
	if digest != "" {
		if err := h.detectOverwrite(ctx, imageWithTag, repo, tag, digest); err != nil {
			// Error means overwrite blocked (enforcement mode)
			return err
		}
	}

	sizeMB := float64(sizeBytes) / (1024 * 1024)

	h.logger.Info("tracking image",
		"image", imageWithTag,
		"ttl", ttl.String(),
		"expires_at", expiresAt.Format(time.RFC3339),
		"size_bytes", sizeBytes,
		"size_mb", fmt.Sprintf("%.2f", sizeMB),
		"digest", digest,
	)

	if err := h.redis.TrackImage(ctx, imageWithTag, expiresAt, sizeBytes, digest); err != nil {
		return err
	}

	metrics.ImagesTracked.Inc()
	metrics.TrackedBytesTotal.Add(float64(sizeBytes))
	metrics.ImageSizeBytes.Observe(float64(sizeBytes))

	return nil
}

// detectOverwrite checks if tag push overwrites existing content with different digest.
// Returns error if overwrite should be blocked (enforcement mode), nil otherwise.
func (h *Handler) detectOverwrite(ctx context.Context, imageWithTag, repo, tag, newDigest string) error {
	existingDigest, err := h.redis.GetImageDigest(ctx, imageWithTag)
	if err != nil {
		h.logger.Warn("failed to check existing digest (non-critical)",
			"image", imageWithTag,
			"error", err,
		)
		return nil // Best effort: continue on error
	}

	// No existing digest = first push or old record (backward compatible)
	if existingDigest == "" {
		return nil
	}

	// Same digest = re-push of same content (no-op)
	if existingDigest == newDigest {
		return nil
	}

	// Different digest = overwrite detected!
	h.logger.Warn("tag overwrite detected",
		"image", imageWithTag,
		"old_digest", existingDigest,
		"new_digest", newDigest,
	)

	metrics.TagOverwritesTotal.WithLabelValues(repo).Inc()

	// Calculate age of overwritten image
	if createdMillis, err := h.redis.GetCreatedTimestamp(ctx, imageWithTag); err == nil && createdMillis > 0 {
		ageSeconds := time.Since(time.UnixMilli(createdMillis)).Seconds()
		metrics.OverwrittenImageAge.Observe(ageSeconds)
	}

	// Check if tag matches immutable patterns (enforcement mode)
	if h.isImmutableTag(tag) {
		h.logger.Error("immutable tag overwrite rejected",
			"image", imageWithTag,
			"tag", tag,
			"old_digest", existingDigest,
			"new_digest", newDigest,
		)
		metrics.ImmutableTagViolations.WithLabelValues(repo, tag).Inc()
		return fmt.Errorf("tag %s is immutable, overwrite rejected", tag)
	}

	return nil // Observability mode: log but allow
}

// isImmutableTag checks if tag matches any immutable patterns.
func (h *Handler) isImmutableTag(tag string) bool {
	for _, pattern := range h.immutableTagPatterns {
		matched, err := filepath.Match(pattern, tag)
		if err != nil {
			h.logger.Warn("invalid immutable tag pattern",
				"pattern", pattern,
				"error", err,
			)
			continue
		}
		if matched {
			return true
		}
	}
	return false
}
