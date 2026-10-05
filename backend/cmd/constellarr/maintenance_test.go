package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRestoreWaitsForActiveRequestsAndRefusesNewOnes(t *testing.T) {
	gate := &requestGate{}
	entered, release := make(chan struct{}), make(chan struct{})
	handler := gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/movies" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/operations/backups/test/restore", nil))
	if response.Code != http.StatusNoContent || gate.active != 0 {
		t.Fatal("restore must not wait for its own request")
	}
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/movies", nil))
		close(done)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if gate.pause(ctx) == nil {
		t.Fatal("restore ignored a running mutation")
	}
	gate.mu.Lock()
	paused := gate.paused
	gate.mu.Unlock()
	if paused {
		t.Fatal("failed pause left the API unavailable")
	}
	close(release)
	<-done
	if err := gate.pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/api/v1/tv", http.StatusServiceUnavailable},
		{http.MethodGet, "/api/v1/settings", http.StatusServiceUnavailable},
		{http.MethodGet, "/metrics", http.StatusServiceUnavailable},
		{http.MethodGet, "/healthz", http.StatusNoContent},
		{http.MethodGet, "/", http.StatusNoContent},
		{http.MethodPost, "/api/v1/operations/backups/test/restore", http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, response.Code, tc.status)
		}
	}
}
