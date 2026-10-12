package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

type listResponse struct {
	Repositories []string `json:"repositories"`
	Tags         []string `json:"tags"`
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
	SchemaVersion int          `json:"schemaVersion"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
	Manifests     []Descriptor `json:"manifests"`
}

// Descriptor references content by digest and size.
type Descriptor struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (m ManifestV2) imageSize() int64 {
	size := m.Config.Size
	for _, layer := range m.Layers {
		size += layer.Size
	}
	return size
}

// ManifestInfo contains metadata about an image manifest.
type ManifestInfo struct {
	Digest    string
	SizeBytes int64
}

// ListRepositories returns all repository names from the registry catalog.
func (c *Client) ListRepositories(ctx context.Context) ([]string, error) {
	return c.listPaged(ctx, c.baseURL+"/v2/_catalog?n=1000", "repositories")
}

// ListTags returns all tags for a given repository.
func (c *Client) ListTags(ctx context.Context, repo string) ([]string, error) {
	return c.listPaged(ctx, fmt.Sprintf("%s/v2/%s/tags/list?n=1000", c.baseURL, repo), "tags")
}

func (c *Client) listPaged(ctx context.Context, next, field string) ([]string, error) {
	var all []string
	for page := 0; next != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("pagination of %s exceeded %d pages", field, maxPages)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, fmt.Errorf("creating %s request: %w", field, err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", field, err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s request %s failed: status %d", field, next, resp.StatusCode)
		}

		var body listResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding %s response: %w", field, err)
		}

		all = append(all, body.Repositories...)
		all = append(all, body.Tags...)
		next = c.nextLink(resp)
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
	resp, err := c.manifestRequest(ctx, http.MethodGet, repo, ref)
	if err != nil {
		return manifest, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return manifest, "", fmt.Errorf("manifest request failed for %s:%s: status %d", repo, ref, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return manifest, "", fmt.Errorf("decoding manifest for %s:%s: %w", repo, ref, err)
	}
	return manifest, digestOf(resp), nil
}

// DeleteManifest deletes the manifest that tag points to. A missing manifest is not an error.
func (c *Client) DeleteManifest(ctx context.Context, repo, tag string) error {
	head, err := c.manifestRequest(ctx, http.MethodHead, repo, tag)
	if err != nil {
		return err
	}
	_ = head.Body.Close()

	switch head.StatusCode {
	case http.StatusNotFound:
		return nil
	case http.StatusOK:
	default:
		return fmt.Errorf("HEAD manifest returned %d", head.StatusCode)
	}

	digest := digestOf(head)
	if digest == "" {
		return fmt.Errorf("no digest found for %s:%s", repo, tag)
	}

	del, err := c.manifestRequest(ctx, http.MethodDelete, repo, digest)
	if err != nil {
		return err
	}
	_ = del.Body.Close()

	switch del.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("DELETE manifest returned %d", del.StatusCode)
	}
}

func (c *Client) manifestRequest(ctx context.Context, method, repo, ref string) (*http.Response, error) {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, repo, ref)
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating %s manifest request: %w", method, err)
	}
	req.Header.Set("Accept", ManifestAccept)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s manifest %s:%s: %w", method, repo, ref, err)
	}
	return resp, nil
}

func digestOf(resp *http.Response) string {
	if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
		return d
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`)
}

// nextLink resolves the pagination Link header, e.g. </v2/_catalog?n=1000&last=repo>; rel="next".
func (c *Client) nextLink(resp *http.Response) string {
	link := resp.Header.Get("Link")
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start {
		return ""
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}
