package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// maxPages bounds pagination loops so a registry returning broken
// Link headers cannot keep the client looping forever.
const maxPages = 100

// Client talks to the OCI distribution registry HTTP API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new registry client.
func New(registryURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(registryURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type catalogResponse struct {
	Repositories []string `json:"repositories"`
}

type tagsResponse struct {
	Tags []string `json:"tags"`
}

// ManifestAccept lists the manifest media types the registry stores.
var ManifestAccept = strings.Join([]string{
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ",")

// ManifestV2 represents an OCI/Docker image manifest, index or manifest list.
type ManifestV2 struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Config        ManifestConfig       `json:"config"`
	Layers        []ManifestLayer      `json:"layers"`
	Manifests     []ManifestDescriptor `json:"manifests"`
}

// ManifestDescriptor references a child manifest of an index.
type ManifestDescriptor struct {
	Digest string `json:"digest"`
}

func (m ManifestV2) imageSize() int64 {
	size := m.Config.Size
	for _, layer := range m.Layers {
		size += layer.Size
	}
	return size
}

// ManifestConfig contains the image configuration descriptor.
type ManifestConfig struct {
	Size int64 `json:"size"`
}

// ManifestLayer represents a single layer in the image.
type ManifestLayer struct {
	Size int64 `json:"size"`
}

// ManifestInfo contains metadata about an image manifest.
type ManifestInfo struct {
	Digest    string
	SizeBytes int64
}

// ListRepositories returns all repository names from the registry catalog.
func (c *Client) ListRepositories(ctx context.Context) ([]string, error) {
	var all []string
	url := fmt.Sprintf("%s/v2/_catalog?n=1000", c.baseURL)

	for page := 0; url != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("catalog pagination exceeded %d pages", maxPages)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating catalog request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing catalog: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("catalog request failed: status %d", resp.StatusCode)
		}

		var catalog catalogResponse
		err = json.NewDecoder(resp.Body).Decode(&catalog)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding catalog response: %w", err)
		}

		all = append(all, catalog.Repositories...)
		url = nextLink(resp, c.baseURL)
	}

	return all, nil
}

// ListTags returns all tags for a given repository.
func (c *Client) ListTags(ctx context.Context, repo string) ([]string, error) {
	var all []string
	url := fmt.Sprintf("%s/v2/%s/tags/list?n=1000", c.baseURL, repo)

	for page := 0; url != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("tags pagination for %s exceeded %d pages", repo, maxPages)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating tags request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing tags for %s: %w", repo, err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("tags request for %s failed: status %d", repo, resp.StatusCode)
		}

		var tags tagsResponse
		err = json.NewDecoder(resp.Body).Decode(&tags)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding tags response: %w", err)
		}

		all = append(all, tags.Tags...)
		url = nextLink(resp, c.baseURL)
	}

	return all, nil
}

// GetImageManifestInfo fetches the digest and total size of an image.
// For an index or manifest list, the size is the sum over all child manifests.
func (c *Client) GetImageManifestInfo(ctx context.Context, repo, ref string) (*ManifestInfo, error) {
	manifest, digest, err := c.getManifest(ctx, repo, ref)
	if err != nil {
		return nil, err
	}

	size := manifest.imageSize()
	for _, child := range manifest.Manifests {
		childManifest, _, err := c.getManifest(ctx, repo, child.Digest)
		if err != nil {
			return nil, err
		}
		size += childManifest.imageSize()
	}

	return &ManifestInfo{Digest: digest, SizeBytes: size}, nil
}

func (c *Client) getManifest(ctx context.Context, repo, ref string) (ManifestV2, string, error) {
	var manifest ManifestV2
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, repo, ref)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return manifest, "", fmt.Errorf("creating manifest request: %w", err)
	}
	req.Header.Set("Accept", ManifestAccept)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return manifest, "", fmt.Errorf("fetching manifest for %s:%s: %w", repo, ref, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return manifest, "", fmt.Errorf("manifest request failed for %s:%s: status %d", repo, ref, resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		digest = strings.Trim(resp.Header.Get("ETag"), `"`)
	}

	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return manifest, "", fmt.Errorf("decoding manifest for %s:%s: %w", repo, ref, err)
	}
	return manifest, digest, nil
}

// nextLink parses the Link header for pagination.
// The registry returns: Link: </v2/_catalog?n=1000&last=repo>; rel="next"
func nextLink(resp *http.Response, baseURL string) string {
	link := resp.Header.Get("Link")
	if link == "" {
		return ""
	}

	// Parse format: </path>; rel="next"
	start := strings.Index(link, "<")
	end := strings.Index(link, ">")
	if start < 0 || end < 0 || end <= start {
		return ""
	}

	path := link[start+1 : end]
	if strings.HasPrefix(path, "/") {
		return baseURL + path
	}
	return path
}
