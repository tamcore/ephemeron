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
| `reap`    | Run a single reap cycle (useful for CronJobs)                |
| `version` | Print version and commit info                                |

## Configuration

All configuration is done via environment variables.

| Variable             | Default                  | Description                              |
|----------------------|--------------------------|------------------------------------------|
| `PORT`               | `8000`                   | Public HTTP port (webhooks, landing page)|
| `INTERNAL_PORT`      | `9090`                   | Internal port (healthz, readyz, metrics) |
| `REDIS_URL`          | `redis://localhost:6379` | Redis connection URL                     |
| `HOOK_TOKEN`         | *(required)*             | Shared secret for registry webhook auth  |
| `REGISTRY_URL`       | `http://localhost:5000`  | OCI registry base URL                    |
| `HOSTNAME_OVERRIDE`  | `localhost`              | Public hostname shown on landing page    |
| `DEFAULT_TTL`        | `1h`                     | TTL for images with unparseable tags     |
| `MAX_TTL`            | `24h`                    | Maximum allowed TTL                      |
| `REAP_INTERVAL`      | `1m`                     | How often the reaper checks for expiries |
| `LOG_FORMAT`         | `json`                   | Log format (`json` or `text`)            |

`REDISCLOUD_URL` is also supported as an alias for `REDIS_URL`.

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
