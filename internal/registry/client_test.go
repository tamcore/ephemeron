package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testRepo1 = "app1"
	testRepo2 = "app2"
)

func TestListRepositories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/_catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(catalogResponse{
			Repositories: []string{testRepo1, testRepo2},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	repos, err := c.ListRepositories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0] != testRepo1 || repos[1] != testRepo2 {
		t.Fatalf("unexpected repos: %v", repos)
	}
}

func TestListTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/myapp/tags/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(tagsResponse{
			Tags: []string{"1h", "30m", "latest"},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	tags, err := c.ListTags(context.Background(), "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(tags))
	}
}

func TestListRepositories_Pagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Link", `</v2/_catalog?n=1000&last=app1>; rel="next"`)
			_ = json.NewEncoder(w).Encode(catalogResponse{
				Repositories: []string{testRepo1},
			})
		} else {
			_ = json.NewEncoder(w).Encode(catalogResponse{
				Repositories: []string{testRepo2},
			})
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	repos, err := c.ListRepositories(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos across pages, got %d", len(repos))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 API calls, got %d", callCount)
	}
}

func TestListRepositories_HTTPErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "internal server error", status: http.StatusInternalServerError},
		{name: "service unavailable", status: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"errors": "something broke"})
			}))
			defer srv.Close()

			c := New(srv.URL)
			_, err := c.ListRepositories(context.Background())
			if err == nil {
				t.Fatalf("expected error for status %d, got nil", tt.status)
			}
		})
	}
}

func TestListTags_HTTPErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "internal server error", status: http.StatusInternalServerError},
		{name: "service unavailable", status: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"errors": "something broke"})
			}))
			defer srv.Close()

			c := New(srv.URL)
			_, err := c.ListTags(context.Background(), "myapp")
			if err == nil {
				t.Fatalf("expected error for status %d, got nil", tt.status)
			}
		})
	}
}

func TestListRepositories_PaginationRunaway(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Always return a next link — simulates a broken registry.
		w.Header().Set("Link", `</v2/_catalog?n=1000&last=app1>; rel="next"`)
		_ = json.NewEncoder(w).Encode(catalogResponse{
			Repositories: []string{testRepo1},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.ListRepositories(context.Background())
	if err == nil {
		t.Fatal("expected error for runaway pagination, got nil")
	}
	if callCount != maxPages {
		t.Fatalf("expected %d API calls, got %d", maxPages, callCount)
	}
}

func TestGetImageManifestInfo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/myapp/manifests/1h" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}

		// Set digest header
		w.Header().Set("Docker-Content-Digest", "sha256:abc123def456")

		manifest := ManifestV2{
			SchemaVersion: 2,
			Config:        ManifestConfig{Size: 2000},
			Layers: []ManifestLayer{
				{Size: 8000},
				{Size: 15000},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer srv.Close()

	c := New(srv.URL)
	info, err := c.GetImageManifestInfo(context.Background(), "myapp", "1h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedSize := int64(2000 + 8000 + 15000)
	if info.SizeBytes != expectedSize {
		t.Fatalf("expected size %d, got %d", expectedSize, info.SizeBytes)
	}

	expectedDigest := "sha256:abc123def456"
	if info.Digest != expectedDigest {
		t.Fatalf("expected digest %s, got %s", expectedDigest, info.Digest)
	}
}

func TestGetImageManifestInfo_ETagFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Docker-Content-Digest, use ETag
		w.Header().Set("ETag", `"sha256:fallback123"`)

		manifest := ManifestV2{
			SchemaVersion: 2,
			Config:        ManifestConfig{Size: 500},
			Layers:        []ManifestLayer{{Size: 1500}},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer srv.Close()

	c := New(srv.URL)
	info, err := c.GetImageManifestInfo(context.Background(), "myapp", "etag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ETag should be trimmed of quotes
	expectedDigest := "sha256:fallback123"
	if info.Digest != expectedDigest {
		t.Fatalf("expected digest %s, got %s", expectedDigest, info.Digest)
	}

	if info.SizeBytes != 2000 {
		t.Fatalf("expected size 2000, got %d", info.SizeBytes)
	}
}

func TestGetImageManifestInfo_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.GetImageManifestInfo(context.Background(), "myapp", "missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestGetImageManifestInfo_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:test")
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.GetImageManifestInfo(context.Background(), "myapp", "bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestGetImageManifestInfo_Index(t *testing.T) {
	children := map[string]ManifestV2{
		"sha256:amd64": {Config: ManifestConfig{Size: 100}, Layers: []ManifestLayer{{Size: 1000}}},
		"sha256:arm64": {Config: ManifestConfig{Size: 200}, Layers: []ManifestLayer{{Size: 2000}, {Size: 3000}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/v2/myapp/manifests/")
		if ref == "multi-1h" {
			w.Header().Set("Docker-Content-Digest", "sha256:index")
			_ = json.NewEncoder(w).Encode(ManifestV2{
				SchemaVersion: 2,
				Manifests:     []ManifestDescriptor{{Digest: "sha256:amd64"}, {Digest: "sha256:arm64"}},
			})
			return
		}
		child, ok := children[ref]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(child)
	}))
	defer srv.Close()

	info, err := New(srv.URL).GetImageManifestInfo(context.Background(), "myapp", "multi-1h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Digest != "sha256:index" {
		t.Fatalf("digest = %s, want sha256:index", info.Digest)
	}
	if want := int64(100 + 1000 + 200 + 2000 + 3000); info.SizeBytes != want {
		t.Fatalf("size = %d, want %d", info.SizeBytes, want)
	}
}

func TestGetImageManifestInfo_IndexChildMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/multi-1h") {
			_ = json.NewEncoder(w).Encode(ManifestV2{Manifests: []ManifestDescriptor{{Digest: "sha256:gone"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := New(srv.URL).GetImageManifestInfo(context.Background(), "myapp", "multi-1h"); err == nil {
		t.Fatal("expected error for missing child manifest")
	}
}
