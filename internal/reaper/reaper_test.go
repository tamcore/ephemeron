package reaper

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockStore is an in-memory implementation of redis.Store for testing.
type mockStore struct {
	images  map[string]int64 // imageWithTag -> expiresAt (epoch millis)
	sizes   map[string]int64 // imageWithTag -> sizeBytes
	digests map[string]string
	created map[string]int64
	removed []string
}

func newMockStore() *mockStore {
	return &mockStore{
		images:  make(map[string]int64),
		sizes:   make(map[string]int64),
		digests: make(map[string]string),
		created: make(map[string]int64),
	}
}

func (m *mockStore) Ping(context.Context) error { return nil }
func (m *mockStore) Close() error               { return nil }

func (m *mockStore) TrackImage(
	_ context.Context,
	imageWithTag string,
	expiresAt time.Time,
	sizeBytes int64,
	digest string,
) error {
	m.images[imageWithTag] = expiresAt.UnixMilli()
	m.sizes[imageWithTag] = sizeBytes
	m.digests[imageWithTag] = digest
	m.created[imageWithTag] = time.Now().UnixMilli()
	return nil
}

func (m *mockStore) ListImages(context.Context) ([]string, error) {
	out := make([]string, 0, len(m.images))
	for k := range m.images {
		out = append(out, k)
	}
	return out, nil
}

func (m *mockStore) GetExpiry(_ context.Context, imageWithTag string) (int64, error) {
	return m.images[imageWithTag], nil
}

func (m *mockStore) GetImageSize(_ context.Context, imageWithTag string) (int64, error) {
	return m.sizes[imageWithTag], nil
}

func (m *mockStore) GetImageDigest(_ context.Context, imageWithTag string) (string, error) {
	return m.digests[imageWithTag], nil
}

func (m *mockStore) GetCreatedTimestamp(_ context.Context, imageWithTag string) (int64, error) {
	return m.created[imageWithTag], nil
}

func (m *mockStore) RemoveImage(_ context.Context, imageWithTag string) error {
	delete(m.images, imageWithTag)
	m.removed = append(m.removed, imageWithTag)
	return nil
}

func (m *mockStore) AcquireReaperLock(context.Context, time.Duration) (bool, error) {
	return true, nil
}

func (m *mockStore) ReleaseReaperLock(context.Context) error { return nil }

func (m *mockStore) IsInitialized(context.Context) (bool, error) { return false, nil }
func (m *mockStore) SetInitialized(context.Context) error        { return nil }

func (m *mockStore) ImageCount(context.Context) (int64, error) {
	return int64(len(m.images)), nil
}

func TestDeleteImage_404FromRegistry(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer registry.Close()

	store := newMockStore()
	store.images["myimage:1h"] = time.Now().Add(-time.Hour).UnixMilli()

	r := New(store, registry.URL, slog.Default())
	err := r.deleteImage(t.Context(), "myimage:1h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := store.images["myimage:1h"]; exists {
		t.Error("expected image to be removed from store")
	}
}

func TestDeleteImage_SuccessfulDelete(t *testing.T) {
	var deleteCalled bool

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Docker-Content-Digest", "sha256:abc123")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deleteCalled = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer registry.Close()

	store := newMockStore()
	store.images["myimage:1h"] = time.Now().Add(-time.Hour).UnixMilli()

	r := New(store, registry.URL, slog.Default())
	err := r.deleteImage(t.Context(), "myimage:1h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Error("expected DELETE to be called on registry")
	}
	if _, exists := store.images["myimage:1h"]; exists {
		t.Error("expected image to be removed from store")
	}
}

func TestDeleteImage_InvalidFormat(t *testing.T) {
	store := newMockStore()
	r := New(store, "http://localhost", slog.Default())
	err := r.deleteImage(t.Context(), "no-colon-here")
	if err == nil {
		t.Error("expected error for invalid image format")
	}
}

func TestReapOnce_ExpiredImage(t *testing.T) {
	var deleteCalled bool

	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Docker-Content-Digest", "sha256:abc123")
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			deleteCalled = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer registry.Close()

	store := newMockStore()
	// Image expired 1 minute ago.
	store.images["myapp:5m"] = time.Now().Add(-time.Minute).UnixMilli()

	r := New(store, registry.URL, slog.Default())
	if err := r.ReapOnce(t.Context()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deleteCalled {
		t.Error("expected DELETE to be called")
	}
	if len(store.images) != 0 {
		t.Errorf("expected store to be empty, got %d images", len(store.images))
	}
}

func TestReapOnce_NotExpired(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("registry should not be called for non-expired images")
	}))
	defer registry.Close()

	store := newMockStore()
	// Image expires in 1 hour.
	store.images["myapp:1h"] = time.Now().Add(time.Hour).UnixMilli()

	r := New(store, registry.URL, slog.Default())
	if err := r.ReapOnce(t.Context()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.images) != 1 {
		t.Error("non-expired image should not be removed")
	}
}
