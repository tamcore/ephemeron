package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRepositories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/_catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(catalogResponse{
			Repositories: []string{"app1", "app2"},
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
	if repos[0] != "app1" || repos[1] != "app2" {
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
				Repositories: []string{"app1"},
			})
		} else {
			_ = json.NewEncoder(w).Encode(catalogResponse{
				Repositories: []string{"app2"},
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

func TestGetImageSize_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/myapp/manifests/1h" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		manifest := ManifestV2{
			SchemaVersion: 2,
			Config:        ManifestConfig{Size: 1000},
			Layers: []ManifestLayer{
				{Size: 5000},
				{Size: 10000},
			},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer srv.Close()

	c := New(srv.URL)
	size, err := c.GetImageSize(context.Background(), "myapp", "1h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := int64(1000 + 5000 + 10000)
	if size != expected {
		t.Fatalf("expected size %d, got %d", expected, size)
	}
}

func TestGetImageSize_EmptyLayers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := ManifestV2{
			SchemaVersion: 2,
			Config:        ManifestConfig{Size: 500},
			Layers:        []ManifestLayer{},
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer srv.Close()

	c := New(srv.URL)
	size, err := c.GetImageSize(context.Background(), "myapp", "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if size != 500 {
		t.Fatalf("expected size 500, got %d", size)
	}
}

func TestGetImageSize_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.GetImageSize(context.Background(), "myapp", "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestGetImageSize_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.GetImageSize(context.Background(), "myapp", "bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
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
