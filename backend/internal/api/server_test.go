package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
)

func TestRequestHostEnforcement(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "library.home.arpa, UPPER.example")
	handler := api.New(nil)
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Fatalf("os.Hostname: %v", err)
	}

	cases := []struct {
		name string
		host string
		want int
	}{
		{"localhost", "localhost", http.StatusOK},
		{"localhost and port", "localhost:8080", http.StatusOK},
		{"localhost case", "LOCALHOST:8080", http.StatusOK},
		{"localhost root dot", "localhost.", http.StatusOK},
		{"IPv4", "127.0.0.1", http.StatusOK},
		{"IPv4 and port", "127.0.0.1:8080", http.StatusOK},
		{"LAN IPv4 and port", "192.168.1.42:8080", http.StatusOK},
		{"IPv6", "[::1]", http.StatusOK},
		{"IPv6 and port", "[::1]:8080", http.StatusOK},
		{"machine hostname", hostname, http.StatusOK},
		{"machine hostname and port", hostname + ":8080", http.StatusOK},
		{"configured name", "library.home.arpa", http.StatusOK},
		{"configured name and port", "library.home.arpa:8080", http.StatusOK},
		{"configured name case", "Library.Home.Arpa", http.StatusOK},
		{"configured name from uppercase entry", "upper.example", http.StatusOK},
		{"unknown name", "unknown.example", http.StatusForbidden},
		{"unconfigured httptest default host", "example.com", http.StatusForbidden},
		{"unknown name and port", "unknown.example:8080", http.StatusForbidden},
		{"subdomain of configured name", "sub.library.home.arpa", http.StatusForbidden},
		{"suffix of configured name", "evilibrary.home.arpa", http.StatusForbidden},
		{"configured name as a label suffix", "library.home.arpa.unknown.example", http.StatusForbidden},
		{"localhost as a label suffix", "localhost.unknown.example", http.StatusForbidden},
		{"missing host", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := serve(t, handler, apiRequest(t, http.MethodGet, "/healthz", tc.host, nil))
			if status != tc.want {
				t.Fatalf("host %q: status %d, body %s; want %d", tc.host, status, body, tc.want)
			}
		})
	}

	status, body := serve(t, handler, apiRequest(t, http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "unknown.example", nil))
	if status != http.StatusForbidden {
		t.Fatalf("poster read from an unknown host: status %d, body %s; want 403", status, body)
	}
	assertForbiddenJSON(t, body, "request host is not allowed", "library.home.arpa", "UPPER.example")
}

func TestAllowedHostsConfiguration(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "first.home.arpa")
	handler := api.New(nil)
	t.Setenv("ALLOWED_HOSTS", "second.home.arpa, list.home.arpa:8443")

	if status, _ := serve(t, handler, apiRequest(t, http.MethodGet, "/healthz", "first.home.arpa", nil)); status != http.StatusOK {
		t.Fatalf("host configured at construction: status %d; want 200", status)
	}
	if status, _ := serve(t, handler, apiRequest(t, http.MethodGet, "/healthz", "second.home.arpa", nil)); status != http.StatusForbidden {
		t.Fatalf("host added after construction: status %d; want 403", status)
	}

	rebuilt := api.New(nil)
	if status, _ := serve(t, rebuilt, apiRequest(t, http.MethodGet, "/healthz", "first.home.arpa", nil)); status != http.StatusForbidden {
		t.Fatalf("host removed before construction: status %d; want 403", status)
	}
	if status, _ := serve(t, rebuilt, apiRequest(t, http.MethodGet, "/healthz", "list.home.arpa", nil)); status != http.StatusOK {
		t.Fatalf("configured name without its port: status %d; want 200", status)
	}

	t.Setenv("ALLOWED_HOSTS", "")
	defaults := api.New(nil)
	if status, _ := serve(t, defaults, apiRequest(t, http.MethodGet, "/healthz", "localhost:8080", nil)); status != http.StatusOK {
		t.Fatalf("localhost without ALLOWED_HOSTS: status %d; want 200", status)
	}
	if status, _ := serve(t, defaults, apiRequest(t, http.MethodGet, "/healthz", "second.home.arpa", nil)); status != http.StatusForbidden {
		t.Fatalf("unconfigured name after clearing ALLOWED_HOSTS: status %d; want 403", status)
	}

	// TestMain configures example.com, the default host used by the older tests in this package.
	t.Setenv("ALLOWED_HOSTS", "example.com")
	if status, _ := serve(t, api.New(nil), apiRequest(t, http.MethodGet, "/healthz", "example.com", nil)); status != http.StatusOK {
		t.Fatalf("configured test host: status %d; want 200", status)
	}
}

func TestCrossSiteAPIRequestEnforcement(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "")
	handler := api.New(nil)

	cases := []struct {
		name   string
		method string
		target string
		site   string
		want   int
	}{
		{"poster GET from another site", http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "cross-site", http.StatusForbidden},
		{"movie list GET from another site", http.MethodGet, "/api/v1/movies", "cross-site", http.StatusForbidden},
		{"health GET from another site", http.MethodGet, "/api/v1/health", "cross-site", http.StatusForbidden},
		{"mutation from another site", http.MethodPost, "/api/v1/movies", "cross-site", http.StatusForbidden},
		{"same-origin poster GET", http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "same-origin", http.StatusNotFound},
		{"same-site poster GET", http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "same-site", http.StatusNotFound},
		{"top-level poster navigation", http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "none", http.StatusNotFound},
		{"non-browser poster GET", http.MethodGet, "/api/v1/movie-poster?imdbId=tt0133093", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.site != "" {
				headers["Sec-Fetch-Site"] = tc.site
			}
			if tc.method == http.MethodPost {
				headers["Content-Type"] = "application/json"
			}
			status, body := serve(t, handler, apiRequest(t, tc.method, tc.target, "localhost:8080", headers))
			if status != tc.want {
				t.Fatalf("%s %s with Sec-Fetch-Site %q: status %d, body %s; want %d", tc.method, tc.target, tc.site, status, body, tc.want)
			}
			if status == http.StatusForbidden {
				assertForbiddenJSON(t, body, "cross-site requests are not allowed")
			}
		})
	}

	// Compose healthchecks and uptime probes are plain HTTP requests and stay unaffected.
	status, body := serve(t, handler, apiRequest(t, http.MethodGet, "/healthz", "127.0.0.1:8080", map[string]string{"Sec-Fetch-Site": "cross-site"}))
	if status != http.StatusOK {
		t.Fatalf("healthcheck with a cross-site marker: status %d, body %s; want 200", status, body)
	}
}

func TestMutationRequestEnforcement(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "")
	handler := api.New(nil)
	jsonHeader := map[string]string{"Content-Type": "application/json"}

	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
		message string
	}{
		{"POST without Origin", http.MethodPost, "localhost:8080", jsonHeader, http.StatusNotFound, ""},
		{"POST through the Vite proxy", http.MethodPost, "localhost:5173", map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:5173"}, http.StatusNotFound, ""},
		{"POST from the served origin", http.MethodPost, "127.0.0.1:8080", map[string]string{"Content-Type": "application/json", "Origin": "http://127.0.0.1:8080"}, http.StatusNotFound, ""},
		{"POST behind a TLS proxy", http.MethodPut, "localhost:8080", map[string]string{"Content-Type": "application/json", "Origin": "https://localhost:8080"}, http.StatusNotFound, ""},
		{"DELETE without a content type", http.MethodDelete, "localhost:8080", nil, http.StatusNotFound, ""},
		{"POST from a foreign origin", http.MethodPost, "localhost:8080", map[string]string{"Content-Type": "application/json", "Origin": "http://evil.example"}, http.StatusForbidden, "request origin is not allowed"},
		{"POST from a non-HTTP origin", http.MethodPost, "localhost:8080", map[string]string{"Content-Type": "application/json", "Origin": "ftp://localhost:5173"}, http.StatusForbidden, "request origin is not allowed"},
		{"POST from an opaque origin", http.MethodPost, "localhost:8080", map[string]string{"Content-Type": "application/json", "Origin": "null"}, http.StatusForbidden, "request origin is not allowed"},
		{"DELETE from a foreign origin", http.MethodDelete, "localhost:8080", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden, "request origin is not allowed"},
		{"rebound POST with a matching Origin", http.MethodPost, "evil.example:8080", map[string]string{"Content-Type": "application/json", "Origin": "http://evil.example:8080", "Sec-Fetch-Site": "same-origin"}, http.StatusForbidden, "request host is not allowed"},
		{"GET with a foreign Origin", http.MethodGet, "localhost:8080", map[string]string{"Origin": "http://evil.example"}, http.StatusNotFound, ""},
		{"POST without a JSON content type", http.MethodPost, "localhost:8080", nil, http.StatusUnsupportedMediaType, ""},
		{"POST with a text content type", http.MethodPost, "localhost:8080", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := serve(t, handler, apiRequest(t, tc.method, "/api/v1/movies", tc.host, tc.headers))
			if status != tc.want {
				t.Fatalf("%s on host %q: status %d, body %s; want %d", tc.method, tc.host, status, body, tc.want)
			}
			if tc.message != "" {
				assertForbiddenJSON(t, body, tc.message, "evil.example")
			}
		})
	}
}

func TestLiveServerRequestGuards(t *testing.T) {
	t.Setenv("ALLOWED_HOSTS", "")
	server := httptest.NewServer(api.New(nil))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	send := func(req *http.Request) (int, []byte) {
		t.Helper()
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", req.Method, req.URL, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read %s %s: %v", req.Method, req.URL, err)
		}
		return response.StatusCode, body
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("build health request: %v", err)
	}
	if status, body := send(request); status != http.StatusOK {
		t.Fatalf("live healthcheck: status %d, body %s; want 200", status, body)
	}

	request, err = http.NewRequest(http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatalf("build page request: %v", err)
	}
	if status, body := send(request); status == http.StatusForbidden {
		t.Fatalf("page load from a loopback host: status %d, body %s", status, body)
	}

	request, err = http.NewRequest(http.MethodGet, server.URL+"/api/v1/movie-poster?imdbId=tt0133093", nil)
	if err != nil {
		t.Fatalf("build poster request: %v", err)
	}
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	status, body := send(request)
	if status != http.StatusForbidden {
		t.Fatalf("cross-site poster read on a live server: status %d, body %s; want 403", status, body)
	}
	assertForbiddenJSON(t, body, "cross-site requests are not allowed")

	request, err = http.NewRequest(http.MethodGet, server.URL+"/api/v1/movies", nil)
	if err != nil {
		t.Fatalf("build rebound request: %v", err)
	}
	request.Host = "evil.example"
	status, body = send(request)
	if status != http.StatusForbidden {
		t.Fatalf("rebound host on a live server: status %d, body %s; want 403", status, body)
	}
	assertForbiddenJSON(t, body, "request host is not allowed")

	request, err = http.NewRequest(http.MethodPost, server.URL+"/api/v1/movies", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build mutation request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", server.URL)
	if status, body := send(request); status != http.StatusNotFound {
		t.Fatalf("mutation from the listener origin: status %d, body %s; want 404", status, body)
	}
}

func apiRequest(t *testing.T, method, target, host string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

func serve(t *testing.T, handler http.Handler, req *http.Request) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func assertForbiddenJSON(t *testing.T, body []byte, message string, leaks ...string) {
	t.Helper()
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("rejection is not JSON: %s", body)
	}
	if payload["error"] != message {
		t.Fatalf("rejection message %q; want %q", payload["error"], message)
	}
	for _, leak := range append(leaks, "ALLOWED_HOSTS", "apikey") {
		if leak != "" && strings.Contains(string(body), leak) {
			t.Fatalf("rejection leaks %q: %s", leak, body)
		}
	}
}
