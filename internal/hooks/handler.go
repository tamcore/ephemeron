package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	redisclient "github.com/tamcore/ephemeron/internal/redis"

	"github.com/tamcore/ephemeron/internal/metrics"
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

	// Fetch image size from registry (best effort)
	sizeBytes, err := h.registry.GetImageSize(ctx, repo, tag)
	if err != nil {
		h.logger.Warn("failed to fetch image size, tracking with size=0",
			"image", imageWithTag,
			"error", err,
		)
		sizeBytes = 0
		metrics.ImageSizeFetchErrors.Inc()
	}

	sizeMB := float64(sizeBytes) / (1024 * 1024)

	h.logger.Info("tracking image",
		"image", imageWithTag,
		"ttl", ttl.String(),
		"expires_at", expiresAt.Format(time.RFC3339),
		"size_bytes", sizeBytes,
		"size_mb", fmt.Sprintf("%.2f", sizeMB),
	)

	if err := h.redis.TrackImage(ctx, imageWithTag, expiresAt, sizeBytes); err != nil {
		return err
	}

	metrics.ImagesTracked.Inc()
	metrics.TrackedBytesTotal.Add(float64(sizeBytes))
	metrics.ImageSizeBytes.Observe(float64(sizeBytes))

	return nil
}
