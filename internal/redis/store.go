package redis

import (
	"context"
	"time"
)

// Store defines the interface for image TTL tracking operations.
type Store interface {
	Ping(ctx context.Context) error
	Close() error
	TrackImage(ctx context.Context, imageWithTag string, expiresAt time.Time, sizeBytes int64) error
	ListImages(ctx context.Context) ([]string, error)
	GetExpiry(ctx context.Context, imageWithTag string) (int64, error)
	GetImageSize(ctx context.Context, imageWithTag string) (int64, error)
	RemoveImage(ctx context.Context, imageWithTag string) error
	AcquireReaperLock(ctx context.Context, ttl time.Duration) (bool, error)
	ReleaseReaperLock(ctx context.Context) error
	IsInitialized(ctx context.Context) (bool, error)
	SetInitialized(ctx context.Context) error
	ImageCount(ctx context.Context) (int64, error)
}
