package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tamcore/ephemeron/internal/registry"
)

const (
	testApp        = "myapp"
	testAppTTL     = "myapp:1h"
	testAppProdTTL = "myapp:prod-1h"
	testPush       = "push"
)

func TestHandler_Auth(t *testing.T) {
	handler := NewHandler(nil, nil, "test-token", 0, 0, nil, slog.Default())

	tests := []struct {
		name     string
		auth     string
		wantCode int
	}{
		{name: "accepts valid token", auth: "Token test-token", wantCode: http.StatusOK},
		{name: "rejects missing auth", auth: "", wantCode: http.StatusUnauthorized},
		{name: "rejects wrong token", auth: "Token wrong-token", wantCode: http.StatusUnauthorized},
		{name: "rejects wrong token of same length", auth: "Token test-tokem", wantCode: http.StatusUnauthorized},
		{name: "rejects wrong scheme", auth: "Bearer test-token", wantCode: http.StatusUnauthorized},
		{name: "rejects token with trailing whitespace", auth: "Token test-token ", wantCode: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader([]byte("{}")))
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tt.wantCode {
				t.Errorf("expected %d, got %d", tt.wantCode, rr.Code)
			}
		})
	}

	t.Run("rejects wrong method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/hook/registry-event", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", rr.Code)
		}
	})
}

func TestHandler_EventParsing(t *testing.T) {
	// We can't test Redis interaction without a real Redis,
	// but we can test that the handler parses events correctly
	// by checking that it doesn't error on valid input (with nil redis it will fail,
	// so we just test the auth + decode path).

	t.Run("rejects invalid json", func(t *testing.T) {
		handler := NewHandler(nil, nil, "tok", 0, 0, nil, slog.Default())
		req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader([]byte("not json")))
		req.Header.Set("Authorization", "Token tok")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rr.Code)
		}
	})

	t.Run("accepts empty events", func(t *testing.T) {
		handler := NewHandler(nil, nil, "tok", 0, 0, nil, slog.Default())
		body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{}})
		req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
		req.Header.Set("Authorization", "Token tok")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("skips non-push events", func(t *testing.T) {
		handler := NewHandler(nil, nil, "tok", 0, 0, nil, slog.Default())
		body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
			{Action: "pull", Target: EventTarget{Repository: "foo", Tag: "1h"}},
		}})
		req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
		req.Header.Set("Authorization", "Token tok")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})

	t.Run("skips events with empty repo or tag", func(t *testing.T) {
		handler := NewHandler(nil, nil, "tok", 0, 0, nil, slog.Default())
		body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
			{Action: testPush, Target: EventTarget{Repository: "", Tag: "1h"}},
			{Action: testPush, Target: EventTarget{Repository: "foo", Tag: ""}},
		}})
		req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
		req.Header.Set("Authorization", "Token tok")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
	})
}

// mockStore is a minimal mock for testing size tracking
type mockStore struct {
	images  map[string]time.Time
	sizes   map[string]int64
	digests map[string]string
	created map[string]int64
}

func newMockStore() *mockStore {
	return &mockStore{
		images:  make(map[string]time.Time),
		sizes:   make(map[string]int64),
		digests: make(map[string]string),
		created: make(map[string]int64),
	}
}

func (m *mockStore) TrackImage(
	_ context.Context,
	imageWithTag string,
	expiresAt time.Time,
	sizeBytes int64,
	digest string,
) error {
	m.images[imageWithTag] = expiresAt
	m.sizes[imageWithTag] = sizeBytes
	m.digests[imageWithTag] = digest
	m.created[imageWithTag] = time.Now().UnixMilli()
	return nil
}

func (m *mockStore) GetImageDigest(_ context.Context, imageWithTag string) (string, error) {
	return m.digests[imageWithTag], nil
}

func (m *mockStore) GetCreatedTimestamp(_ context.Context, imageWithTag string) (int64, error) {
	return m.created[imageWithTag], nil
}

func (m *mockStore) Ping(context.Context) error                                     { return nil }
func (m *mockStore) Close() error                                                   { return nil }
func (m *mockStore) ListImages(context.Context) ([]string, error)                   { return nil, nil }
func (m *mockStore) GetExpiry(context.Context, string) (int64, error)               { return 0, nil }
func (m *mockStore) GetImageSize(context.Context, string) (int64, error)            { return 0, nil }
func (m *mockStore) RemoveImage(context.Context, string) error                      { return nil }
func (m *mockStore) AcquireReaperLock(context.Context, time.Duration) (bool, error) { return true, nil }
func (m *mockStore) ReleaseReaperLock(context.Context) error                        { return nil }
func (m *mockStore) IsInitialized(context.Context) (bool, error)                    { return false, nil }
func (m *mockStore) SetInitialized(context.Context) error                           { return nil }
func (m *mockStore) ImageCount(context.Context) (int64, error)                      { return 0, nil }

// mockRegistry is a minimal mock for testing size fetching
type mockRegistry struct {
	sizes   map[string]int64
	digests map[string]string
	err     error
}

func (m *mockRegistry) GetImageSize(_ context.Context, repo, tag string) (int64, error) {
	if m.err != nil {
		return 0, m.err
	}
	key := repo + ":" + tag
	return m.sizes[key], nil
}

func (m *mockRegistry) GetImageManifestInfo(_ context.Context, repo, tag string) (*registry.ManifestInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	key := repo + ":" + tag
	return &registry.ManifestInfo{
		Digest:    m.digests[key],
		SizeBytes: m.sizes[key],
	}, nil
}

func TestHandler_SizeTracking_Success(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		sizes: map[string]int64{
			testAppTTL: 12345678,
		},
		digests: map[string]string{
			testAppTTL: "sha256:abc123",
		},
	}

	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	if _, exists := store.images[testAppTTL]; !exists {
		t.Fatal("expected image to be tracked")
	}

	if store.sizes[testAppTTL] != 12345678 {
		t.Fatalf("expected size 12345678, got %d", store.sizes[testAppTTL])
	}
}

func TestHandler_SizeTracking_FetchError(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		err: http.ErrHandlerTimeout,
	}

	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should succeed despite size fetch error (best effort)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 despite fetch error, got %d", rr.Code)
	}

	if _, exists := store.images[testAppTTL]; !exists {
		t.Fatal("expected image to be tracked even with size fetch error")
	}

	// Should track with size=0 on error
	if store.sizes[testAppTTL] != 0 {
		t.Fatalf("expected size 0 on fetch error, got %d", store.sizes[testAppTTL])
	}
}

func TestDetectOverwrite_FirstPush(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		sizes:   map[string]int64{testAppTTL: 100000},
		digests: map[string]string{testAppTTL: "sha256:new123"},
	}

	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should succeed - first push, no existing digest
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for first push, got %d", rr.Code)
	}

	if store.digests[testAppTTL] != "sha256:new123" {
		t.Fatalf("expected digest to be stored, got %s", store.digests[testAppTTL])
	}
}

func TestDetectOverwrite_SameDigest(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		sizes:   map[string]int64{testAppTTL: 100000},
		digests: map[string]string{testAppTTL: "sha256:same123"},
	}

	// Pre-populate with existing digest
	store.digests[testAppTTL] = "sha256:same123"
	store.created[testAppTTL] = time.Now().UnixMilli()

	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should succeed - same digest, no overwrite
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for same digest re-push, got %d", rr.Code)
	}
}

func TestDetectOverwrite_DifferentDigest_Observability(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		sizes:   map[string]int64{testAppTTL: 100000},
		digests: map[string]string{testAppTTL: "sha256:new456"},
	}

	// Pre-populate with different existing digest
	store.digests[testAppTTL] = "sha256:old123"
	store.created[testAppTTL] = time.Now().Add(-10 * time.Minute).UnixMilli()

	// No immutable patterns = observability mode only
	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should succeed despite overwrite (observability mode)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 in observability mode, got %d", rr.Code)
	}

	// New digest should be stored
	if store.digests[testAppTTL] != "sha256:new456" {
		t.Fatalf("expected new digest to be stored, got %s", store.digests[testAppTTL])
	}
}

func TestDetectOverwrite_DifferentDigest_Enforcement(t *testing.T) {
	store := newMockStore()
	registry := &mockRegistry{
		sizes:   map[string]int64{testAppProdTTL: 100000},
		digests: map[string]string{testAppProdTTL: "sha256:new789"},
	}

	// Pre-populate with different existing digest
	store.digests[testAppProdTTL] = "sha256:old456"
	store.created[testAppProdTTL] = time.Now().Add(-5 * time.Minute).UnixMilli()

	// Set immutable pattern that matches "prod-*"
	handler := NewHandler(store, registry, "tok", time.Hour, 24*time.Hour, []string{"prod-*"}, slog.Default())

	body, _ := json.Marshal(EventEnvelope{Events: []RegistryEvent{
		{Action: testPush, Target: EventTarget{Repository: testApp, Tag: "prod-1h"}},
	}})

	req := httptest.NewRequest(http.MethodPost, "/v1/hook/registry-event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Token tok")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Should fail - enforcement mode blocks overwrite
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for immutable tag overwrite, got %d", rr.Code)
	}

	// Old digest should still be in store (webhook failed before tracking)
	if store.digests[testAppProdTTL] != "sha256:old456" {
		t.Fatalf("expected old digest to remain, got %s", store.digests[testAppProdTTL])
	}
}

func TestIsImmutableTag_Matches(t *testing.T) {
	handler := NewHandler(
		nil, nil, "tok", time.Hour, 24*time.Hour,
		[]string{"prod-*", "release-*", "v[0-9]*"},
		slog.Default(),
	)

	tests := []struct {
		tag      string
		expected bool
	}{
		{"prod-1h", true},
		{"prod-latest", true},
		{"release-1.0", true},
		{"v1", true},
		{"v123", true},
		{"dev-1h", false},
		{"test", false},
		{"latest", false},
		{"1h", false},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := handler.isImmutableTag(tt.tag)
			if result != tt.expected {
				t.Errorf("tag %s: expected %v, got %v", tt.tag, tt.expected, result)
			}
		})
	}
}

func TestIsImmutableTag_NoPatterns(t *testing.T) {
	handler := NewHandler(nil, nil, "tok", time.Hour, 24*time.Hour, nil, slog.Default())

	// No patterns = nothing is immutable
	if handler.isImmutableTag("prod-1h") {
		t.Error("expected false when no patterns configured")
	}
}

func TestIsImmutableTag_InvalidPattern(t *testing.T) {
	// Invalid glob pattern should be skipped
	handler := NewHandler(nil, nil, "tok", time.Hour, 24*time.Hour, []string{"[invalid"}, slog.Default())

	// Should return false (pattern error is logged and skipped)
	if handler.isImmutableTag("test") {
		t.Error("expected false for invalid pattern")
	}
}
