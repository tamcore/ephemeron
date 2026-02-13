package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	imagesKey      = "current.images"
	reaperLockKey  = "reaper.lock"
	initializedKey = "ephemeron:initialized"
)

// Client wraps the Redis client with ephemeron-specific operations.
type Client struct {
	rdb *redis.Client
}

// New creates a new Redis client from the given URL.
func New(redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parsing redis URL: %w", err)
	}
	return &Client{rdb: redis.NewClient(opts)}, nil
}

// Ping checks the connection to Redis.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// TrackImage adds an image to the tracking set and stores its expiry metadata.
func (c *Client) TrackImage(ctx context.Context, imageWithTag string, expiresAt time.Time, sizeBytes int64) error {
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, imagesKey, imageWithTag)
	pipe.HSet(ctx, imageWithTag,
		"created", strconv.FormatInt(time.Now().UnixMilli(), 10),
		"expires", strconv.FormatInt(expiresAt.UnixMilli(), 10),
		"size_bytes", strconv.FormatInt(sizeBytes, 10),
	)
	_, err := pipe.Exec(ctx)
	return err
}

// ListImages returns all tracked images.
func (c *Client) ListImages(ctx context.Context) ([]string, error) {
	return c.rdb.SMembers(ctx, imagesKey).Result()
}

// GetExpiry returns the expiry timestamp (in epoch milliseconds) for an image.
func (c *Client) GetExpiry(ctx context.Context, imageWithTag string) (int64, error) {
	val, err := c.rdb.HGet(ctx, imageWithTag, "expires").Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(val, 10, 64)
}

// GetImageSize returns the size in bytes for an image.
// Returns 0 for missing field (backward compatibility with old records).
func (c *Client) GetImageSize(ctx context.Context, imageWithTag string) (int64, error) {
	val, err := c.rdb.HGet(ctx, imageWithTag, "size_bytes").Result()
	if err == redis.Nil {
		// Field doesn't exist (old record without size tracking)
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(val, 10, 64)
}

// RemoveImage removes an image from the tracking set and deletes its metadata.
func (c *Client) RemoveImage(ctx context.Context, imageWithTag string) error {
	pipe := c.rdb.Pipeline()
	pipe.SRem(ctx, imagesKey, imageWithTag)
	pipe.Del(ctx, imageWithTag)
	_, err := pipe.Exec(ctx)
	return err
}

// AcquireReaperLock attempts to acquire a distributed lock for the reaper.
// Returns true if the lock was acquired. The lock auto-expires after the given TTL.
func (c *Client) AcquireReaperLock(ctx context.Context, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, reaperLockKey, "locked", ttl).Result()
}

// ReleaseReaperLock releases the distributed reaper lock.
func (c *Client) ReleaseReaperLock(ctx context.Context) error {
	return c.rdb.Del(ctx, reaperLockKey).Err()
}

// IsInitialized checks if ephemeron has been initialized (i.e. Redis has been populated).
func (c *Client) IsInitialized(ctx context.Context) (bool, error) {
	val, err := c.rdb.Exists(ctx, initializedKey).Result()
	if err != nil {
		return false, err
	}
	return val > 0, nil
}

// SetInitialized marks Redis as initialized.
func (c *Client) SetInitialized(ctx context.Context) error {
	return c.rdb.Set(ctx, initializedKey, "true", 0).Err()
}

// ImageCount returns the number of tracked images.
func (c *Client) ImageCount(ctx context.Context) (int64, error) {
	return c.rdb.SCard(ctx, imagesKey).Result()
}
