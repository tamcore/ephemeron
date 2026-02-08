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
	removed []string
}

func newMockStore() *mockStore {
	return &mockStore{images: make(map[string]int64)}
}

func (m *mockStore) Ping(context.Context) error { return nil }
func (m *mockStore) Close() error               { return nil }

func (m *mockStore) TrackImage(_ context.Context, imageWithTag string, expiresAt time.Time) error {
	m.images[imageWithTag] = expiresAt.UnixMilli()
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

func (m *mockStore) RemoveImage(_ context.Context, imageWithTag string) error {
	delete(m.images, imageWithTag)
	m.removed = append(m.removed, imageWithTag)
	return nil
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
