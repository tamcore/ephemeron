package recover

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tamcore/ephemeron/internal/registry"
)

type mockStore struct {
	images      map[string]time.Time
	sizes       map[string]int64
	initialized bool
}

func newMockStore() *mockStore {
	return &mockStore{
		images: make(map[string]time.Time),
		sizes:  make(map[string]int64),
	}
}

func (m *mockStore) Ping(_ context.Context) error { return nil }
func (m *mockStore) Close() error                 { return nil }

func (m *mockStore) TrackImage(_ context.Context, imageWithTag string, expiresAt time.Time, sizeBytes int64) error {
	m.images[imageWithTag] = expiresAt
	m.sizes[imageWithTag] = sizeBytes
	return nil
}

func (m *mockStore) ListImages(_ context.Context) ([]string, error) {
	keys := make([]string, 0, len(m.images))
	for k := range m.images {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *mockStore) GetExpiry(_ context.Context, imageWithTag string) (int64, error) {
	return m.images[imageWithTag].UnixMilli(), nil
}

func (m *mockStore) GetImageSize(_ context.Context, imageWithTag string) (int64, error) {
	return m.sizes[imageWithTag], nil
}

func (m *mockStore) RemoveImage(_ context.Context, imageWithTag string) error {
	delete(m.images, imageWithTag)
	return nil
}

func (m *mockStore) AcquireReaperLock(_ context.Context, _ time.Duration) (bool, error) {
	return true, nil
}

func (m *mockStore) ReleaseReaperLock(_ context.Context) error { return nil }

func (m *mockStore) IsInitialized(_ context.Context) (bool, error) {
	return m.initialized, nil
}

func (m *mockStore) SetInitialized(_ context.Context) error {
	m.initialized = true
	return nil
}

func (m *mockStore) ImageCount(_ context.Context) (int64, error) {
	return int64(len(m.images)), nil
}

func TestRunIfNeeded_AlreadyInitialized(t *testing.T) {
	store := newMockStore()
	store.initialized = true

	r := New(store, registry.New("http://unused"), time.Hour, 24*time.Hour, slog.Default())

	if err := r.RunIfNeeded(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.images) != 0 {
		t.Fatalf("expected no images tracked, got %d", len(store.images))
	}
}
