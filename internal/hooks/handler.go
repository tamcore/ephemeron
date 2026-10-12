package hooks

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tamcore/ephemeron/internal/metrics"
	redisclient "github.com/tamcore/ephemeron/internal/redis"
	"github.com/tamcore/ephemeron/internal/registry"
)

const actionPush = "push"

// RegistryEvent represents a single event from the Docker Registry webhook.
type RegistryEvent struct {
	Action string      `json:"action"`
	Target EventTarget `json:"target"`
}

// EventTarget contains the repository, tag and manifest digest from a registry event.
type EventTarget struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
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
	redis      redisclient.Store
	registry   registryClient
	hookToken  string
	defaultTTL time.Duration
	maxTTL     time.Duration
	logger     *slog.Logger
}

// NewHandler creates a new webhook handler.
func NewHandler(
	redis redisclient.Store,
	registry registryClient,
	hookToken string,
	defaultTTL, maxTTL time.Duration,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		redis:      redis,
		registry:   registry,
		hookToken:  hookToken,
		defaultTTL: defaultTTL,
		maxTTL:     maxTTL,
		logger:     logger,
	}
}

// ServeHTTP handles POST /v1/hook/registry-event.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	auth := r.Header.Get("Authorization")
	expected := "Token " + h.hookToken
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
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

		if event.Action != actionPush {
			continue
		}
		if event.Target.Repository == "" || event.Target.Tag == "" {
			continue
		}
		if err := h.handlePush(ctx, event.Target); err != nil {
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

func (h *Handler) handlePush(ctx context.Context, target EventTarget) error {
	repo, tag := target.Repository, target.Tag
	imageWithTag := fmt.Sprintf("%s:%s", repo, tag)

	ttl := ClampTTL(ParseTTL(tag), h.defaultTTL, h.maxTTL)
	expiresAt := time.Now().Add(ttl)

	// Fetch manifest info (digest + size) - best effort
	var sizeBytes int64
	digest := target.Digest

	// The registry sends the event before it links the tag, so prefer the digest.
	manifestInfo, err := h.registry.GetImageManifestInfo(ctx, repo, cmp.Or(target.Digest, tag))
	if err != nil {
		h.logger.Warn("failed to fetch manifest info",
			"image", imageWithTag,
			"digest", digest,
			"error", err,
		)
		metrics.DigestFetchErrors.Inc()
	} else {
		sizeBytes = manifestInfo.SizeBytes
		digest = cmp.Or(digest, manifestInfo.Digest)
	}

	if digest != "" {
		h.detectOverwrite(ctx, imageWithTag, repo, digest)
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

// detectOverwrite logs and counts pushes that change the digest of a tracked tag.
func (h *Handler) detectOverwrite(ctx context.Context, imageWithTag, repo, newDigest string) {
	existingDigest, err := h.redis.GetImageDigest(ctx, imageWithTag)
	if err != nil {
		h.logger.Warn("failed to check existing digest (non-critical)",
			"image", imageWithTag,
			"error", err,
		)
		return
	}

	// No existing digest = first push or old record (backward compatible)
	if existingDigest == "" {
		return
	}

	// Same digest = re-push of same content (no-op)
	if existingDigest == newDigest {
		return
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
}
