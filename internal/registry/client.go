package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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

// ManifestV2 represents an OCI/Docker image manifest v2.
type ManifestV2 struct {
	SchemaVersion int             `json:"schemaVersion"`
	Config        ManifestConfig  `json:"config"`
	Layers        []ManifestLayer `json:"layers"`
}

// ManifestConfig contains the image configuration descriptor.
type ManifestConfig struct {
	Size int64 `json:"size"`
}

// ManifestLayer represents a single layer in the image.
type ManifestLayer struct {
	Size int64 `json:"size"`
}

// ListRepositories returns all repository names from the registry catalog.
func (c *Client) ListRepositories(ctx context.Context) ([]string, error) {
	var all []string
	url := fmt.Sprintf("%s/v2/_catalog?n=1000", c.baseURL)

	for url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating catalog request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing catalog: %w", err)
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

	for url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating tags request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing tags for %s: %w", repo, err)
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

// GetImageSize fetches the total size of an image by fetching its manifest
// and summing the config size and all layer sizes.
func (c *Client) GetImageSize(ctx context.Context, repo, tag string) (int64, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, repo, tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("creating manifest request: %w", err)
	}

	// Accept both OCI and Docker manifest formats
	req.Header.Set("Accept",
		"application/vnd.oci.image.manifest.v1+json,"+
			"application/vnd.docker.distribution.manifest.v2+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching manifest for %s:%s: %w", repo, tag, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("manifest request failed for %s:%s: status %d", repo, tag, resp.StatusCode)
	}

	var manifest ManifestV2
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return 0, fmt.Errorf("decoding manifest for %s:%s: %w", repo, tag, err)
	}

	// Sum config size + all layer sizes
	totalSize := manifest.Config.Size
	for _, layer := range manifest.Layers {
		totalSize += layer.Size
	}

	return totalSize, nil
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
