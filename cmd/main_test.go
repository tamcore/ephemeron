package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	return ln
}

func TestRunServers_DrainsInFlightRequestsOnShutdown(t *testing.T) {
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var handlerFinished atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-releaseHandler
		w.WriteHeader(http.StatusOK)
		handlerFinished.Store(true)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	internalSrv := &http.Server{Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}

	ln := newTestListener(t)
	internalLn := newTestListener(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- runServers(ctx, slog.Default(), srv, internalSrv, ln, internalLn)
	}()

	requestDone := make(chan error, 1)
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/slow", ln.Addr().String()))
		if err != nil {
			requestDone <- err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			requestDone <- fmt.Errorf("unexpected status %d", resp.StatusCode)
			return
		}
		requestDone <- nil
	}()

	<-handlerStarted
	cancel()

	// runServers must not return while a request is still in flight.
	select {
	case <-runDone:
		t.Fatal("runServers returned before in-flight request completed")
	case <-time.After(200 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runServers returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServers did not return after shutdown")
	}

	if !handlerFinished.Load() {
		t.Error("runServers returned before the in-flight handler finished")
	}

	if err := <-requestDone; err != nil {
		t.Fatalf("in-flight request failed: %v", err)
	}
}

func TestEnvInt(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		set      bool
		fallback int
		want     int
	}{
		{name: "unset uses fallback", set: false, fallback: 8000, want: 8000},
		{name: "valid value", value: "9999", set: true, fallback: 8000, want: 9999},
		{name: "negative value", value: "-1", set: true, fallback: 8000, want: -1},
		{name: "malformed value uses fallback", value: "abc", set: true, fallback: 8000, want: 8000},
		{name: "trailing garbage uses fallback", value: "12x", set: true, fallback: 8000, want: 8000},
		{name: "empty value uses fallback", value: "", set: true, fallback: 8000, want: 8000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_INT"
			if tt.set {
				t.Setenv(key, tt.value)
			}
			got := envInt(slog.Default(), key, tt.fallback)
			if got != tt.want {
				t.Errorf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestEnvDuration(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		set      bool
		fallback time.Duration
		want     time.Duration
	}{
		{name: "unset uses fallback", set: false, fallback: time.Hour, want: time.Hour},
		{name: "valid value", value: "30m", set: true, fallback: time.Hour, want: 30 * time.Minute},
		{name: "malformed value uses fallback", value: "abc", set: true, fallback: time.Hour, want: time.Hour},
		{name: "bare number uses fallback", value: "30", set: true, fallback: time.Hour, want: time.Hour},
		{name: "empty value uses fallback", value: "", set: true, fallback: time.Hour, want: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_DURATION"
			if tt.set {
				t.Setenv(key, tt.value)
			}
			got := envDuration(slog.Default(), key, tt.fallback)
			if got != tt.want {
				t.Errorf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestRunServers_ReturnsPromptlyWhenIdle(t *testing.T) {
	srv := &http.Server{Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}
	internalSrv := &http.Server{Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- runServers(ctx, slog.Default(), srv, internalSrv, newTestListener(t), newTestListener(t))
	}()

	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runServers returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServers did not return after context cancellation")
	}
}
