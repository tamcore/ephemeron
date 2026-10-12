package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testManifest     = `{"schemaVersion":2}`
	testManifestPath = "/v2/team/app/manifests/v1"
	otherDigest      = "sha256:other"
)

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type fakeRegistry struct {
	existing   map[string]string
	headStatus int
	puts       atomic.Int32
	lastHead   *http.Request
	lastPut    *http.Request
	lastBody   []byte
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodHead:
		f.lastHead = r
		if f.headStatus != 0 {
			w.WriteHeader(f.headStatus)
			return
		}
		d, ok := f.existing[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		f.puts.Add(1)
		f.lastPut = r
		f.lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func newTestProxy(t *testing.T, reg *fakeRegistry, patterns ...string) *Handler {
	t.Helper()
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return New(u, patterns, slog.Default())
}

func putManifest(h http.Handler, p string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, p, bytes.NewReader(body))
	req.Host = "reg.example.com"
	req.Header.Set("Authorization", "Bearer abc")
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestManifestPut(t *testing.T) {
	body := []byte(testManifest)
	tests := []struct {
		name     string
		path     string
		patterns []string
		existing string
		wantCode int
		wantPuts int32
	}{
		{"new tag", testManifestPath, []string{"*"}, "", http.StatusCreated, 1},
		{"same digest re-push", testManifestPath, []string{"*"}, sha256Digest(body), http.StatusCreated, 1},
		{"different digest", testManifestPath, []string{"*"}, otherDigest, http.StatusForbidden, 0},
		{"pattern mismatch", testManifestPath, []string{"prod-*"}, otherDigest, http.StatusCreated, 1},
		{"digest reference", "/v2/team/app/manifests/sha256:abc", []string{"*"}, otherDigest, http.StatusCreated, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeRegistry{existing: map[string]string{}}
			if tt.existing != "" {
				reg.existing[tt.path] = tt.existing
			}
			h := newTestProxy(t, reg, tt.patterns...)

			rr := putManifest(h, tt.path, body)

			if rr.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tt.wantCode, rr.Body)
			}
			if got := reg.puts.Load(); got != tt.wantPuts {
				t.Fatalf("puts = %d, want %d", got, tt.wantPuts)
			}
		})
	}
}

func TestManifestPut_ForwardsExactBody(t *testing.T) {
	reg := &fakeRegistry{existing: map[string]string{}}
	h := newTestProxy(t, reg, "*")

	putManifest(h, testManifestPath, []byte(testManifest))

	if string(reg.lastBody) != testManifest {
		t.Fatalf("forwarded body = %q", reg.lastBody)
	}
	if reg.lastPut.Host != "reg.example.com" {
		t.Fatalf("forwarded Host = %q, want reg.example.com", reg.lastPut.Host)
	}
	if got := reg.lastPut.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Fatalf("X-Forwarded-Proto = %q, want https", got)
	}
}

func TestManifestPut_HeadRequest(t *testing.T) {
	reg := &fakeRegistry{existing: map[string]string{}}
	h := newTestProxy(t, reg, "*")

	putManifest(h, testManifestPath, []byte(testManifest))

	if reg.lastHead == nil {
		t.Fatal("expected HEAD request")
	}
	if got := reg.lastHead.Header.Get("Authorization"); got != "Bearer abc" {
		t.Fatalf("HEAD Authorization = %q", got)
	}
	accept := reg.lastHead.Header.Get("Accept")
	for _, mt := range []string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	} {
		if !strings.Contains(accept, mt) {
			t.Errorf("Accept missing %s", mt)
		}
	}
}

func TestManifestPut_RegistryError(t *testing.T) {
	reg := &fakeRegistry{headStatus: http.StatusInternalServerError}
	h := newTestProxy(t, reg, "*")

	rr := putManifest(h, testManifestPath, []byte(testManifest))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
	if reg.puts.Load() != 0 {
		t.Fatal("manifest must not be forwarded")
	}
}

func TestManifestPut_TooLarge(t *testing.T) {
	reg := &fakeRegistry{existing: map[string]string{}}
	h := newTestProxy(t, reg, "*")

	rr := putManifest(h, testManifestPath, bytes.Repeat([]byte("a"), maxManifestBytes+1))

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rr.Code)
	}
	if reg.puts.Load() != 0 {
		t.Fatal("manifest must not be forwarded")
	}
}

func TestCatalogDenied(t *testing.T) {
	h := newTestProxy(t, &fakeRegistry{}, "*")

	for _, p := range []string{"/v2/_catalog", "/v2/_catalog/", "/v2//_catalog"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: code = %d, want 403", p, rr.Code)
		}
	}
}

func TestParseManifestTag(t *testing.T) {
	tests := []struct {
		path     string
		wantRepo string
		wantTag  string
		wantOK   bool
	}{
		{"/v2/app/manifests/v1", "app", "v1", true},
		{"/v2/team/app/manifests/v1", "team/app", "v1", true},
		{"/v2/a/b/../c/manifests/v1", "a/c", "v1", true},
		{"/v2/app/manifests/sha256:abc", "", "", false},
		{"/v2/app/blobs/uploads/", "", "", false},
		{"/v2/manifests/v1", "", "", false},
		{"/v2/app/manifests/", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			repo, tag, ok := parseManifestTag(tt.path)
			if repo != tt.wantRepo || tag != tt.wantTag || ok != tt.wantOK {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", repo, tag, ok, tt.wantRepo, tt.wantTag, tt.wantOK)
			}
		})
	}
}

func TestValidatePatterns(t *testing.T) {
	if err := ValidatePatterns([]string{"prod-*", "v[0-9]*"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidatePatterns([]string{"[invalid"}); err == nil {
		t.Fatal("expected error for invalid pattern")
	}
	if err := ValidatePatterns(nil); err == nil {
		t.Fatal("expected error for empty patterns")
	}
}
