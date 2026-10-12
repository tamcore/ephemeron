// Package proxy fronts the registry and rejects manifest pushes that would
// overwrite an existing immutable tag with different content.
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/tamcore/ephemeron/internal/metrics"
)

// maxManifestBytes matches the manifest size limit of distribution.
const maxManifestBytes = 4 << 20

const headTimeout = 10 * time.Second

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ",")

var forwardedHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

// Handler is a reverse proxy in front of the registry.
type Handler struct {
	upstream *url.URL
	patterns []string
	client   *http.Client
	proxy    *httputil.ReverseProxy
	logger   *slog.Logger
}

// New creates a proxy to upstream that enforces immutability for tags matching patterns.
func New(upstream *url.URL, patterns []string, logger *slog.Logger) *Handler {
	return &Handler{
		upstream: upstream,
		patterns: patterns,
		client:   &http.Client{Timeout: headTimeout},
		logger:   logger,
		proxy: &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(upstream)
				// The registry builds upload Location URLs from Host and X-Forwarded-*.
				pr.Out.Host = pr.In.Host
				for _, k := range forwardedHeaders {
					if v := pr.In.Header.Values(k); len(v) > 0 {
						pr.Out.Header[k] = v
					}
				}
			},
			FlushInterval: -1,
		},
	}
}

// ValidatePatterns checks that at least one pattern is set and all are valid globs.
func ValidatePatterns(patterns []string) error {
	if len(patterns) == 0 {
		return errors.New("IMMUTABLE_TAG_PATTERNS is required")
	}
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid immutable tag pattern %q: %w", p, err)
		}
	}
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if path.Clean(r.URL.Path) == "/v2/_catalog" {
		writeError(w, http.StatusForbidden, "DENIED", "catalog listing is disabled")
		return
	}
	if r.Method == http.MethodPut {
		if repo, tag, ok := parseManifestTag(r.URL.Path); ok && h.isImmutable(tag) {
			if !h.allowManifestPut(w, r, repo, tag) {
				return
			}
		}
	}
	h.proxy.ServeHTTP(w, r)
}

// allowManifestPut buffers the manifest and reports whether the push may proceed.
// It writes the error response itself when it returns false.
func (h *Handler) allowManifestPut(w http.ResponseWriter, r *http.Request, repo, tag string) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifestBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "manifest exceeds 4 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "MANIFEST_INVALID", "cannot read manifest")
		}
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	existing, err := h.existingDigest(r.Context(), repo, tag, r.Header.Get("Authorization"))
	if err != nil {
		h.logger.Error("cannot check existing tag", "repository", repo, "tag", tag, "error", err)
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cannot verify tag immutability, retry later")
		return false
	}

	sum := sha256.Sum256(body)
	newDigest := "sha256:" + hex.EncodeToString(sum[:])
	if existing == "" || existing == newDigest {
		return true
	}

	h.logger.Warn("immutable tag overwrite rejected",
		"repository", repo, "tag", tag, "old_digest", existing, "new_digest", newDigest)
	metrics.ImmutableTagViolations.WithLabelValues(repo, tag).Inc()
	writeError(w, http.StatusForbidden, "DENIED",
		fmt.Sprintf("tag %s is immutable and already exists, push a new tag", tag))
	return false
}

// existingDigest returns the digest the tag points to, or "" if the tag does not exist.
func (h *Handler) existingDigest(ctx context.Context, repo, tag, auth string) (string, error) {
	u := h.upstream.JoinPath("v2", repo, "manifests", tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", nil
	case http.StatusOK:
		d := resp.Header.Get("Docker-Content-Digest")
		if d == "" {
			return "", errors.New("registry returned no Docker-Content-Digest")
		}
		return d, nil
	default:
		return "", fmt.Errorf("registry returned status %d", resp.StatusCode)
	}
}

func (h *Handler) isImmutable(tag string) bool {
	for _, p := range h.patterns {
		if ok, _ := path.Match(p, tag); ok {
			return true
		}
	}
	return false
}

// parseManifestTag extracts repository and tag from /v2/<repo>/manifests/<tag>.
// Digest references return ok=false.
func parseManifestTag(p string) (repo, tag string, ok bool) {
	rest, found := strings.CutPrefix(path.Clean(p), "/v2/")
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(rest, "/manifests/")
	if i <= 0 {
		return "", "", false
	}
	repo, tag = rest[:i], rest[i+len("/manifests/"):]
	if tag == "" || strings.ContainsAny(tag, ":/") {
		return "", "", false
	}
	return repo, tag, true
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": code, "message": msg}},
	})
}
