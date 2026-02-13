# Ephemeron Architecture

## Overview

Ephemeron is a self-hosted ephemeral container registry manager that automatically deletes expired container images based on TTL (time-to-live) values encoded in image tags. The system follows a webhook-driven architecture where the registry notifies Ephemeron of image pushes, and a periodic reaper process deletes expired images.

### Key Design Principles

- **Event-driven**: React to registry push events via webhooks
- **Idempotent**: All operations can be safely retried
- **Distributed-ready**: Uses Redis locks for multi-replica coordination
- **Self-healing**: Automatic recovery from Redis data loss
- **Observable**: Prometheus metrics and structured logging

## System Architecture

```
┌─────────────────┐
│  OCI Registry   │
│  (Distribution) │
└────────┬────────┘
         │ Webhook: POST /v1/hook/registry-event
         │ (on image push)
         ▼
┌─────────────────────────────────────────────────┐
│             Ephemeron (serve)                   │
│                                                 │
│  ┌──────────────┐  ┌──────────────┐           │
│  │   Webhook    │  │    Reaper    │           │
│  │   Handler    │  │  (background │           │
│  │              │  │   goroutine) │           │
│  └──────┬───────┘  └──────┬───────┘           │
│         │                  │                    │
│         └──────┬───────────┘                    │
│                │                                │
│         ┌──────▼───────┐                       │
│         │  Redis Store │                       │
│         │   Interface  │                       │
│         └──────┬───────┘                       │
└────────────────┼────────────────────────────────┘
                 │
                 ▼
         ┌──────────────┐
         │    Redis     │
         │              │
         └──────────────┘
```

## Core Components

### 1. Main Application (`cmd/main.go`)

The application entry point provides four commands:

- **`serve`**: Primary mode - runs webhook server, reaper loop, and landing page
- **`reap`**: One-shot reaper execution (for CronJob deployments)
- **`recover`**: Manual recovery - scans registry catalog and rebuilds Redis state
- **`version`**: Display version information

#### Serve Command Flow

```
1. Load configuration from environment variables
2. Connect to Redis and verify connection
3. Run automatic recovery if Redis is uninitialized
4. Start reaper loop in background goroutine
5. Set up HTTP routes:
   - Public server (PORT): webhook endpoint + landing page
   - Internal server (INTERNAL_PORT): /healthz, /readyz, /metrics
6. Listen for SIGTERM/SIGINT for graceful shutdown
```

### 2. Webhook Handler (`internal/hooks/handler.go`)

Receives and processes registry push events.

#### Request Flow

```
POST /v1/hook/registry-event
Authorization: Token <HOOK_TOKEN>
Content-Type: application/json

{
  "events": [
    {
      "action": "push",
      "target": {
        "repository": "myapp",
        "tag": "1h30m"
      }
    }
  ]
}
```

#### Processing Logic

1. **Authentication**: Verify `Authorization: Token <HOOK_TOKEN>` header
2. **Parse events**: Decode JSON webhook payload
3. **Filter**: Only process `action: "push"` events with valid repository and tag
4. **Parse TTL**: Extract duration from tag using regex pattern
5. **Clamp TTL**: Apply `DEFAULT_TTL` (if unparseable) and `MAX_TTL` (if too large)
6. **Calculate expiry**: `expiresAt = time.Now() + ttl`
7. **Fetch image size**: GET manifest from registry to calculate total size (best effort)
8. **Track image**: Store in Redis with expiry timestamp and size
9. **Update metrics**: Increment tracked counters, observe size distribution

#### TTL Parsing (`internal/hooks/ttl.go`)

Regex pattern: `^(?:(\d+)w)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`

Examples:
- `5m` → 5 minutes
- `1h` → 1 hour
- `1h30m` → 90 minutes
- `2d` → 48 hours
- `1w` → 168 hours
- `1w3d12h` → 252 hours

If parsing fails or returns -1, `DEFAULT_TTL` is applied.

### 3. Reaper (`internal/reaper/reaper.go`)

Periodically scans tracked images and deletes expired ones from the registry.

#### Reaper Loop

```
┌──────────────────────────────────┐
│ Ticker fires (REAP_INTERVAL)    │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ Acquire distributed lock         │
│ (Redis: reaper.lock)             │
└─────────────┬────────────────────┘
              │ Lock acquired?
              ▼
         Yes  │  No → Skip cycle
              │
┌─────────────▼────────────────────┐
│ List all tracked images          │
│ (Redis: SMEMBERS current.images) │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ For each image:                  │
│   1. Get expiry timestamp        │
│   2. Compare with current time   │
│   3. If expired:                 │
│      - Get image size from Redis │
│      - deleteImage()             │
│      - Update storage metrics    │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ Release distributed lock         │
└──────────────────────────────────┘
```

#### Image Deletion Process

1. **Parse image**: Split `repo:tag` format
2. **Get manifest digest**:
   - `HEAD /v2/{repo}/manifests/{tag}`
   - Extract `Docker-Content-Digest` header (or fall back to `ETag`)
3. **Delete manifest by digest**:
   - `DELETE /v2/{repo}/manifests/{digest}`
   - Accept status: 200, 202, or 404
4. **Remove from Redis**: Clean up tracking data
5. **Handle errors**: If manifest not found (404), just clean up Redis

### 4. Recovery System (`internal/recover/recover.go`)

Rebuilds Redis state by scanning the registry catalog.

#### When Recovery Runs

- **Automatic**: On `serve` startup if `ephemeron:initialized` key is missing
- **Manual**: Via `ephemeron recover` command

#### Recovery Process

```
┌──────────────────────────────────┐
│ Check IsInitialized()            │
│ (Redis: EXISTS ephemeron:init)   │
└─────────────┬────────────────────┘
              │
              ▼ Not initialized
┌──────────────────────────────────┐
│ Scan registry catalog            │
│ GET /v2/_catalog?n=1000          │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ For each repository:             │
│   GET /v2/{repo}/tags/list       │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ For each tag:                    │
│   1. ParseTTL(tag)               │
│   2. ClampTTL()                  │
│   3. expiresAt = now + ttl       │
│   4. Fetch image size (manifest) │
│   5. TrackImage() in Redis       │
└─────────────┬────────────────────┘
              │
              ▼
┌──────────────────────────────────┐
│ SetInitialized()                 │
│ (Redis: SET ephemeron:init true) │
└──────────────────────────────────┘
```

**Idempotency**: Re-tracking an already-tracked image simply overwrites its metadata, so recovery can be run repeatedly without side effects.

**Pagination**: The registry client follows `Link` headers to handle large catalogs.

### 5. Redis Store (`internal/redis/`)

#### Interface (`store.go`)

Defines all Redis operations needed by the system:

```go
type Store interface {
    Ping(ctx) error
    Close() error

    // Image tracking
    TrackImage(ctx, imageWithTag, expiresAt, sizeBytes) error
    ListImages(ctx) ([]string, error)
    GetExpiry(ctx, imageWithTag) (int64, error)
    GetImageSize(ctx, imageWithTag) (int64, error)
    RemoveImage(ctx, imageWithTag) error
    ImageCount(ctx) (int64, error)

    // Distributed locking
    AcquireReaperLock(ctx, ttl) (bool, error)
    ReleaseReaperLock(ctx) error

    // Recovery state
    IsInitialized(ctx) (bool, error)
    SetInitialized(ctx) error
}
```

#### Data Model

##### Key: `current.images` (Set)
Contains all tracked image references in `repo:tag` format.

```
SMEMBERS current.images
→ ["myapp:1h", "backend:30m", "frontend:2h"]
```

##### Key: `<repo:tag>` (Hash)
Metadata for each tracked image.

```
HGETALL myapp:1h
→ {
    "created": "1707831234567",   // Unix milliseconds
    "expires": "1707834834567",   // Unix milliseconds
    "size_bytes": "12345678"      // Total image size in bytes
  }
```

Note: `size_bytes` may be "0" if size fetch failed or for old records (backward compatible).

##### Key: `reaper.lock` (String with TTL)
Distributed lock to ensure only one reaper instance runs at a time.

```
SET reaper.lock "locked" NX EX 300
→ Returns 1 if acquired, 0 if already held
```

TTL: 5 minutes (auto-expires if reaper crashes)

##### Key: `ephemeron:initialized` (String)
Flag indicating Redis has been populated (via recovery or normal operation).

```
EXISTS ephemeron:initialized
→ 1 if initialized, 0 if empty/new
```

### 6. Registry Client (`internal/registry/client.go`)

HTTP client for the OCI Distribution Registry API.

#### Operations

**List Repositories**
```
GET /v2/_catalog?n=1000
→ {"repositories": ["repo1", "repo2", ...]}
```

**List Tags**
```
GET /v2/{repo}/tags/list?n=1000
→ {"name": "repo", "tags": ["tag1", "tag2", ...]}
```

**Get Image Size**
```
GET /v2/{repo}/manifests/{tag}
Accept: application/vnd.oci.image.manifest.v1+json,
        application/vnd.docker.distribution.manifest.v2+json
→ Parses manifest JSON and sums config.size + all layers[].size
```

**Pagination**: Follows `Link: </v2/_catalog?n=1000&last=repo>; rel="next"` headers.

### 7. Web Handler (`internal/web/handler.go`)

Serves a landing page at `GET /` with:
- Hostname and push instructions
- Configured DEFAULT_TTL and MAX_TTL
- Version information
- Example usage

Template is embedded at build time from `internal/web/static/index.html`.

### 8. Configuration (`internal/config/config.go`)

All configuration via environment variables:

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `PORT` | 8000 | No | Public HTTP server port |
| `INTERNAL_PORT` | 9090 | No | Internal server port (metrics, probes) |
| `REDIS_URL` | `redis://localhost:6379` | Yes | Redis connection URL |
| `HOOK_TOKEN` | - | Yes | Webhook authentication token |
| `REGISTRY_URL` | `http://localhost:5000` | Yes | OCI registry base URL |
| `HOSTNAME_OVERRIDE` | `localhost` | No | Public hostname for landing page |
| `DEFAULT_TTL` | `1h` | No | TTL for unparseable tags |
| `MAX_TTL` | `24h` | No | Maximum allowed TTL |
| `REAP_INTERVAL` | `1m` | No | Reaper check frequency |
| `LOG_FORMAT` | `json` | No | Log format (`json` or `text`) |
| `IMMUTABLE_TAG_PATTERNS` | - | No | Comma-separated glob patterns for immutable tags |

Validation ensures:
- Required fields are present
- TTLs are positive
- `DEFAULT_TTL` ≤ `MAX_TTL`

### 9. Metrics (`internal/metrics/metrics.go`)

Prometheus metrics exposed at `GET /metrics` (internal port):

#### Counters
- `ephemeron_hooks_webhook_events_total{action}` - Total webhook events received
- `ephemeron_hooks_images_tracked_total` - Total images added to tracking
- `ephemeron_hooks_image_size_fetch_errors_total` - Total size fetch failures
- `ephemeron_reaper_images_reaped_total` - Total images deleted
- `ephemeron_reaper_cycle_errors_total` - Total failed reaper cycles
- `ephemeron_storage_bytes_reclaimed_total` - Total storage reclaimed by deletion
- `ephemeron_immutability_tag_overwrites_total{repository}` - Total tag overwrites detected
- `ephemeron_immutability_digest_fetch_errors_total` - Total digest fetch failures
- `ephemeron_immutability_immutable_tag_violations_total{repository,tag}` - Blocked overwrites (enforcement mode)

#### Gauges
- `ephemeron_reaper_tracked_images` - Current number of tracked images
- `ephemeron_storage_tracked_bytes_total` - Current total storage tracked

#### Histograms
- `ephemeron_reaper_cycle_duration_seconds` - Reaper cycle duration
- `ephemeron_storage_image_size_bytes` - Image size distribution (1MB-10GB buckets)
- `ephemeron_immutability_overwritten_image_age_seconds` - Age of images when overwritten (1m-30d buckets)

## HTTP API

### Public Endpoints (PORT=8000)

#### `POST /v1/hook/registry-event`
Webhook endpoint for registry push events.

**Authentication**: `Authorization: Token <HOOK_TOKEN>`

**Request Body**:
```json
{
  "events": [
    {
      "action": "push",
      "target": {
        "repository": "myapp",
        "tag": "1h"
      }
    }
  ]
}
```

**Response**: `200 OK` with `{}`

#### `GET /`
Landing page with usage instructions.

### Internal Endpoints (INTERNAL_PORT=9090)

#### `GET /healthz`
Liveness probe - always returns `200 OK`.

#### `GET /readyz`
Readiness probe - checks Redis connectivity.
- `200 OK` if Redis responds to PING
- `503 Service Unavailable` if Redis is down

#### `GET /metrics`
Prometheus metrics in text exposition format.

## Data Flow

### Image Push Flow

```
1. User pushes image
   $ docker push reg.example.com/myapp:1h

2. Registry sends webhook to Ephemeron
   POST /v1/hook/registry-event
   {
     "events": [{
       "action": "push",
       "target": {"repository": "myapp", "tag": "1h"}
     }]
   }

3. Ephemeron processes event
   - Authenticates request
   - Parses tag "1h" → 1 hour
   - Calculates expiry: now + 1h
   - Fetches manifest to calculate image size
   - Stores in Redis:
     * SADD current.images "myapp:1h"
     * HSET myapp:1h created <now> expires <now+1h> size_bytes <size>

4. User pulls and uses image
   $ docker pull reg.example.com/myapp:1h
   $ docker run reg.example.com/myapp:1h
```

### Image Expiry Flow

```
1. Reaper wakes up (every REAP_INTERVAL)

2. Acquire lock
   SETNX reaper.lock "locked" EX 300
   → Only one replica proceeds

3. List all images
   SMEMBERS current.images
   → ["myapp:1h", "backend:30m", ...]

4. For each image:
   - HGET myapp:1h expires → 1707834834567
   - Compare with current time
   - If expired:
     * HGET myapp:1h size_bytes → get size for metrics
     * HEAD /v2/myapp/manifests/1h → get digest
     * DELETE /v2/myapp/manifests/<digest>
     * SREM current.images "myapp:1h"
     * DEL myapp:1h
     * Update storage metrics (bytes reclaimed, tracked bytes)

5. Release lock
   DEL reaper.lock
```

## Deployment Architecture

### Single-Instance Deployment

Simple setup with one Ephemeron instance:

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Registry   │────▶│  Ephemeron   │────▶│    Redis     │
│              │     │    (serve)   │     │              │
└──────────────┘     └──────────────┘     └──────────────┘
```

**Use case**: Development, small registries

### Multi-Replica Deployment

High-availability setup with multiple replicas:

```
                      ┌──────────────┐
                      │   Registry   │
                      └──────┬───────┘
                             │ Webhooks
         ┌───────────────────┼───────────────────┐
         │                   │                   │
         ▼                   ▼                   ▼
┌──────────────┐    ┌──────────────┐    ┌──────────────┐
│ Ephemeron #1 │    │ Ephemeron #2 │    │ Ephemeron #3 │
└──────┬───────┘    └──────┬───────┘    └──────┬───────┘
       │                   │                   │
       └───────────────────┼───────────────────┘
                           │
                           ▼
                   ┌──────────────┐
                   │    Redis     │
                   │   (with HA)  │
                   └──────────────┘
```

**Features**:
- Load-balanced webhooks (all replicas can receive)
- Coordinated reaping (only one reaper runs via lock)
- Shared state in Redis

**Use case**: Production, large registries

### CronJob-Based Reaping

Alternative architecture with separated reaper:

```
┌──────────────┐     ┌──────────────────────┐     ┌──────────────┐
│   Registry   │────▶│  Ephemeron (serve)   │────▶│    Redis     │
│              │     │  - Webhook handler   │     │              │
└──────────────┘     │  - Landing page      │     └──────┬───────┘
                     │  - No reaper loop    │            │
                     └──────────────────────┘            │
                                                          │
                     ┌──────────────────────┐            │
                     │ CronJob              │            │
                     │ (ephemeron reap)     │◀───────────┘
                     │ Runs every N minutes │
                     └──────────────────────┘
```

**Benefits**:
- Decouple webhook handling from reaping
- Scale webhook handlers independently
- Use Kubernetes CronJob for reaper scheduling

## Recovery Mechanism

### Scenario: Redis Data Loss

If Redis loses all data (crash, eviction, cluster failover):

1. **Detection**: On next startup, `ephemeron:initialized` key is missing
2. **Automatic recovery**:
   - Scans registry catalog (`GET /v2/_catalog`)
   - Lists tags for each repository
   - Parses TTL from each tag
   - Repopulates Redis with current timestamps + TTL
3. **Resume normal operation**

### Recovery Semantics

**Expiry calculation during recovery**:
```go
expiresAt = time.Now() + ParseTTL(tag)
```

This means:
- Images get a "fresh" TTL based on their tag name
- An image pushed 2 hours ago with tag `1h` will now expire in 1 hour (not immediately)

**Trade-off**: Images may live longer than originally intended, but will not be prematurely deleted.

**Alternative**: Store original push timestamp in registry metadata (not currently implemented).

## Distributed Locking

### Why Locking?

In multi-replica deployments, only one reaper should run at a time to avoid:
- Duplicate DELETE requests to registry
- Wasted computation
- Race conditions

### Lock Implementation

```go
// Acquire
acquired, err := redis.SetNX("reaper.lock", "locked", 5*time.Minute)
if !acquired {
    // Another replica holds the lock
    return
}
defer redis.Del("reaper.lock")

// ... perform reaping ...
```

**Lock TTL**: 5 minutes (auto-expires if reaper crashes)

**Lock granularity**: Per reap cycle (not per image)

**Failure modes**:
- If reaper crashes while holding lock → lock expires after 5 minutes
- If lock expires during reaping → another replica may start reaping (safe due to idempotency)

## Error Handling

### Webhook Handler

- **Invalid JSON**: Returns `400 Bad Request`
- **Missing auth**: Returns `401 Unauthorized`
- **Redis failure**: Logs error, returns `503 Service Unavailable`

**Rationale**: Registry retries failed webhooks automatically (with `threshold` and `backoff` configuration), ensuring eventual consistency when Redis recovers.

### Reaper

- **Lock acquisition fails**: Skip cycle, try again on next interval
- **Image listing fails**: Increment `cycle_errors_total`, abort cycle
- **Individual image deletion fails**: Log error, continue with other images
- **Manifest not found (404)**: Clean up Redis, don't treat as error

**Rationale**: Partial reaping is better than no reaping. Errors are retried on next cycle.

### Recovery

- **Repository listing fails**: Return error, abort recovery
- **Tag listing fails for one repo**: Log warning, skip repo, continue with others
- **Individual image tracking fails**: Log error, continue with other images

**Rationale**: Recover as much as possible, even if some repositories fail.

## Security

### Authentication

- **Webhook endpoint**: Token-based authentication via `Authorization: Token <HOOK_TOKEN>` header
- **Registry deletion**: No authentication (assumes Ephemeron is on trusted network)

**Best practices**:
- Use strong random token for `HOOK_TOKEN`
- Run Ephemeron in same network as registry
- Use network policies to restrict registry API access

### Registry Configuration

Example Docker Registry config:

```yaml
notifications:
  endpoints:
    - name: ephemeron
      url: http://ephemeron:8000/v1/hook/registry-event
      headers:
        Authorization: [Token secret-token-here]
      timeout: 3s
      threshold: 5
      backoff: 1s
```

## Testing Strategy

### Unit Tests

- `internal/hooks/ttl_test.go`: TTL parsing logic
- `internal/config/config_test.go`: Configuration validation
- Component-specific tests for each package

### Integration Tests

- `internal/recover/recover_test.go`: Recovery with mocked registry
- `internal/reaper/reaper_test.go`: Reaper with mocked registry
- End-to-end flow not currently automated (uses docker-compose for manual testing)

### Manual Testing

Use `docker-compose.yaml` for local testing:
1. Start registry + redis + ephemeron
2. Push images with various TTL tags
3. Watch reaper logs
4. Verify images are deleted

## Observability

### Logging

**Format**: JSON (production) or text (development)

**Levels**:
- `DEBUG`: Image expiry checks, lock skips
- `INFO`: Image tracked, image reaped, recovery status
- `WARN`: Repository scan failures, Redis connection issues
- `ERROR`: Critical failures requiring investigation

**Structured fields**: `component`, `image`, `ttl`, `error`, `duration`

### Metrics

See [Metrics](#9-metrics-internalmetricsmetricsgo) section.

**Key metrics to monitor**:
- `ephemeron_reaper_tracked_images`: Should trend down as images expire
- `ephemeron_reaper_cycle_errors_total`: Should be zero or very low
- `ephemeron_hooks_images_tracked_total`: Correlates with push rate

### Health Checks

- **Liveness (`/healthz`)**: Always healthy (process is alive)
- **Readiness (`/readyz`)**: Redis connectivity check

**Kubernetes probes**:
```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 9090

readinessProbe:
  httpGet:
    path: /readyz
    port: 9090
```

## Future Enhancements

### Potential Improvements

1. **Granular Locking**: Per-image locks to allow parallel deletion
2. **Shared Layer Deduplication**: Account for shared base layers in size metrics
3. **Overwrite History Tracking**: Store digest change history in Redis for audit trail
4. **Per-Repository Policies**: Different immutability patterns per repository

### Architectural Constraints

- **No distributed transactions**: Redis operations are not atomic across multiple keys
- **No event sourcing**: Image history is not preserved
- **No rate limiting**: Webhook handler can be overwhelmed by high push rates
- **No authentication to registry**: Assumes trusted network

## Glossary

- **OCI**: Open Container Initiative - standard for container images and registries
- **Manifest**: JSON document describing an image's layers and configuration
- **Digest**: SHA256 hash used to uniquely identify a manifest
- **TTL**: Time-to-live - duration before automatic deletion
- **Reaper**: Background process that deletes expired images
- **Recovery**: Process of rebuilding Redis state from registry catalog
