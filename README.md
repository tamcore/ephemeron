# Ephemeron

[![CI](https://github.com/tamcore/ephemeron/actions/workflows/ci.yaml/badge.svg)](https://github.com/tamcore/ephemeron/actions/workflows/ci.yaml)
[![Release](https://github.com/tamcore/ephemeron/actions/workflows/release.yaml/badge.svg)](https://github.com/tamcore/ephemeron/actions/workflows/release.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tamcore/ephemeron)](https://goreportcard.com/report/github.com/tamcore/ephemeron)

**Ephemeron** (from Greek *ephḗmeron* — ἐφήμερον, "lasting only a day") is a self-hosted ephemeral container registry manager. It automatically deletes expired container images based on TTL (time-to-live) values encoded in image tags.

Tag your images with durations like `myimage:5m`, `myimage:1h`, or `myimage:1d` — Ephemeron tracks them and reaps them when they expire.

## How It Works

1. A Docker/OCI registry sends push webhooks to Ephemeron
2. Ephemeron parses the image tag for a TTL duration and stores the expiry in Redis
3. A reaper loop periodically checks for expired images and deletes them from the registry

Tags like `5m`, `1h`, `24h`, `1d`, `1w`, or combinations (`1h30m`) are automatically parsed. Tags that can't be parsed fall back to `DEFAULT_TTL`.

## Getting Started

### Prerequisites

- Go 1.25+
- Redis
- An OCI-compatible container registry (e.g., [distribution/distribution](https://github.com/distribution/distribution))

### Build

```sh
make build
```

This produces the `bin/ephemeron` binary.

### Run

```sh
bin/ephemeron serve
```

### Commands

| Command   | Description                                                  |
|-----------|--------------------------------------------------------------|
| `serve`   | Start the webhook server, reaper loop, and landing page      |
| `proxy`   | Start the registry proxy that enforces tag immutability      |
| `reap`    | Run a single reap cycle (useful for CronJobs)                |
| `recover` | Re-populate Redis by scanning the registry catalog           |
| `version` | Print version and commit info                                |

## Configuration

All configuration is done via environment variables.

| Variable                   | Default                  | Description                                        |
|----------------------------|--------------------------|---------------------------------------------------|
| `PORT`                     | `8000`                   | Public HTTP port (webhooks, landing page)         |
| `INTERNAL_PORT`            | `9090`                   | Internal port (healthz, readyz, metrics)          |
| `REDIS_URL`                | `redis://localhost:6379` | Redis connection URL                              |
| `HOOK_TOKEN`               | *(required)*             | Shared secret for registry webhook auth           |
| `REGISTRY_URL`             | `http://localhost:5000`  | OCI registry base URL                             |
| `HOSTNAME_OVERRIDE`        | `localhost`              | Public hostname shown on landing page             |
| `DEFAULT_TTL`              | `1h`                     | TTL for images with unparseable tags              |
| `MAX_TTL`                  | `24h`                    | Maximum allowed TTL                               |
| `REAP_INTERVAL`            | `1m`                     | How often the reaper checks for expiries          |
| `LOG_FORMAT`               | `json`                   | Log format (`json` or `text`)                     |

`REDISCLOUD_URL` is also supported as an alias for `REDIS_URL`.

### Tag Immutability

**Detection (always on):** The manager logs every tag overwrite (same tag, different digest) and counts it in Prometheus metrics. Registry notifications arrive after the push is committed, so the manager cannot block an overwrite.

**Enforcement (`proxy` command):** The proxy runs in front of the registry and rejects a manifest push with HTTP 403 `DENIED` when the tag matches an immutable pattern and already points to a different digest. A re-push of the same digest passes. The proxy also denies `/v2/_catalog`. All other requests, including blob uploads, stream through unchanged.

| Variable                 | Default                 | Description                                         |
|--------------------------|-------------------------|-----------------------------------------------------|
| `IMMUTABLE_TAG_PATTERNS` | *(required)*            | Comma-separated glob patterns (`path.Match` syntax) |
| `REGISTRY_URL`           | `http://localhost:5000` | Upstream registry                                   |
| `PORT`                   | `5080`                  | Proxy port                                          |
| `INTERNAL_PORT`          | `9090`                  | Internal port (healthz, metrics)                    |

```bash
export IMMUTABLE_TAG_PATTERNS="*"                  # Every tag is immutable
export IMMUTABLE_TAG_PATTERNS="prod-*,release-*"   # Only prod-* and release-* tags
```

The Helm chart runs the proxy as a sidecar in the registry pod and points Ingress/HTTPRoute at it when `manager.env.immutableTagPatterns` is set.

Limits:
- Two concurrent first pushes of the same tag can both pass the check. The last write wins.
- If the existing-tag check fails (registry error), the push is rejected with HTTP 503.

**Metrics available:**
- `ephemeron_immutability_tag_overwrites_total` — Count of detected overwrites (manager)
- `ephemeron_immutability_overwritten_image_age_seconds` — Age distribution of overwritten images (manager)
- `ephemeron_immutability_digest_fetch_errors_total` — Digest fetch failures (manager)
- `ephemeron_immutability_immutable_tag_violations_total` — Overwrites rejected by the proxy (proxy)

## Recovery

Ephemeron tracks image expiry data in Redis. If Redis data is lost, images in the registry become untracked orphans that will never be reaped.

**Automatic recovery:** On `serve` startup, if Redis has not been initialized (no `ephemeron:initialized` key), Ephemeron automatically scans the registry catalog, parses TTLs from image tags, and re-populates tracking data.

**Manual recovery:** Run `ephemeron recover` to force a full re-scan at any time. This is idempotent and safe to run repeatedly.

## Deployment

### Docker Compose

A ready-to-use Docker Compose setup is available in [`deploy/docker-compose/`](deploy/docker-compose/).

### Helm

A Helm chart is provided in [`deploy/helm/`](deploy/helm/), including Redis HA as a dependency and optional Prometheus ServiceMonitor support.

## Development

```sh
# Format and vet
make fmt
make vet

# Lint
make lint

# Run tests
make test

# Build
make build
```

### Pre-Commit Checks

The following must pass before every commit:

```sh
go vet ./...
go test ./...
golangci-lint run
```

### Commit Convention

This project uses [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`, `ci:`, `build:`.
