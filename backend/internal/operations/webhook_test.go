package operations

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService() *Service {
	service := &Service{
		logger: slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
		options: Options{
			HTTPClient: &http.Client{Timeout: 2 * time.Second},
		},
		notifiedAt: map[string]time.Time{},
	}
	return service
}

func TestPostWebhookDeliversPayloadAndSecretHeader(t *testing.T) {
	var (
		mu      sync.Mutex
		payload alertNotification
		header  string
		agent   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&payload)
		header = r.Header.Get("X-Constellarr-Secret")
		agent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := testService()
	config := defaultWebhook()
	config.Enabled = true
	config.URL = server.URL
	config.Secret = "shhh"
	err := service.postWebhook(context.Background(), config, alertNotification{
		Source: "constellarr", Type: "alert", Alert: "job_stuck", Status: "firing",
		Severity: severityWarning, Message: "2 jobs have not progressed.", Value: 2, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if header != "shhh" || agent != "constellarr" {
		t.Fatalf("unexpected headers: secret=%q agent=%q", header, agent)
	}
	if payload.Alert != "job_stuck" || payload.Status != "firing" || payload.Value != 2 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestPostWebhookRefusesCrossOriginRedirect(t *testing.T) {
	var (
		attackerHits int32
		leakedSecret atomic.Bool
	)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attackerHits, 1)
		if r.Header.Get("X-Constellarr-Secret") != "" {
			leakedSecret.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/collect", http.StatusFound)
	}))
	defer origin.Close()

	service := testService()
	config := defaultWebhook()
	config.Enabled = true
	config.URL = origin.URL
	config.Secret = "shhh"
	err := service.postWebhook(context.Background(), config, alertNotification{Alert: "db_health"})
	if err == nil || !strings.Contains(err.Error(), "redirect was refused") {
		t.Fatalf("expected a refused redirect, got %v", err)
	}
	if hits := atomic.LoadInt32(&attackerHits); hits != 0 {
		t.Fatalf("the redirected origin received %d request(s)", hits)
	}
	if leakedSecret.Load() {
		t.Fatal("the webhook secret reached another origin")
	}
}

func TestPostWebhookFollowsSameOriginRedirect(t *testing.T) {
	var (
		mu     sync.Mutex
		secret string
		hits   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hook" {
			http.Redirect(w, r, "/moved", http.StatusTemporaryRedirect)
			return
		}
		mu.Lock()
		hits++
		secret = r.Header.Get("X-Constellarr-Secret")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	service := testService()
	config := defaultWebhook()
	config.Enabled = true
	config.URL = server.URL + "/hook"
	config.Secret = "shhh"
	if err := service.postWebhook(context.Background(), config, alertNotification{Alert: "db_health"}); err != nil {
		t.Fatalf("a same-origin redirect should be followed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || secret != "shhh" {
		t.Fatalf("unexpected delivery after the redirect: hits=%d secret=%q", hits, secret)
	}
}

func TestPostWebhookErrorsHideEndpoint(t *testing.T) {
	service := testService()
	config := defaultWebhook()
	config.Enabled = true
	config.URL = "http://127.0.0.1:1/hook"
	config.Secret = "topsecret"
	err := service.postWebhook(context.Background(), config, alertNotification{Alert: "db_health"})
	if err == nil {
		t.Fatal("expected the unreachable endpoint to fail")
	}
	if strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("webhook error leaks endpoint details: %q", err.Error())
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	config.URL = server.URL
	err = service.postWebhook(context.Background(), config, alertNotification{Alert: "db_health"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("expected the status code in the error, got %v", err)
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("webhook error leaks the endpoint: %q", err.Error())
	}
}

func TestValidateWebhookRules(t *testing.T) {
	current := defaultWebhook()
	current.Secret = "saved"
	cases := []struct {
		name   string
		update webhookConfig
	}{
		{"non HTTP scheme", webhookConfig{Enabled: true, URL: "ftp://example.com/hook"}},
		{"credentials in URL", webhookConfig{Enabled: true, URL: "https://user:pass@example.com/hook"}},
		{"enabled without URL", webhookConfig{Enabled: true}},
		{"bad header name", webhookConfig{Enabled: true, URL: "https://example.com/hook", HeaderName: "bad header"}},
		{"long timeout", webhookConfig{Enabled: true, URL: "https://example.com/hook", TimeoutSeconds: 600}},
		{"negative interval", webhookConfig{Enabled: true, URL: "https://example.com/hook", MinimumIntervalSeconds: -1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateWebhook(current, test.update); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected an invalid configuration, got %v", err)
			}
		})
	}
	validated, err := validateWebhook(current, webhookConfig{Enabled: true, URL: "https://example.com/hook", TimeoutSeconds: 5})
	if err != nil {
		t.Fatalf("valid configuration was rejected: %v", err)
	}
	if validated.Secret != "saved" {
		t.Fatalf("a blank secret must keep the saved one, got %q", validated.Secret)
	}
	if validated.MinimumIntervalSeconds != 0 {
		t.Fatalf("an explicit zero interval disables rate limiting: %+v", validated)
	}
	if defaultWebhook().MinimumIntervalSeconds != 300 {
		t.Fatalf("unexpected default interval: %+v", defaultWebhook())
	}
}

func TestDeliverAppliesRateLimitAndRecoveryPreference(t *testing.T) {
	var (
		mu       sync.Mutex
		received []alertNotification
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload alertNotification
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		received = append(received, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	service := testService()
	config := defaultWebhook()
	config.Enabled = true
	config.URL = server.URL
	config.MinimumIntervalSeconds = 3600
	config.NotifyRecovery = false
	service.configMu.Lock()
	service.webhook = config
	service.configMu.Unlock()

	service.deliver(context.Background(), alertEvent{name: "job_stuck", firing: true, message: "stuck", at: time.Now()})
	service.deliver(context.Background(), alertEvent{name: "job_stuck", firing: true, message: "stuck again", at: time.Now()})
	service.deliver(context.Background(), alertEvent{name: "job_stuck", firing: false, message: "resolved", at: time.Now()})
	mu.Lock()
	if len(received) != 1 || received[0].Status != "firing" {
		mu.Unlock()
		t.Fatalf("expected one firing notification, got %+v", received)
	}
	mu.Unlock()

	config.NotifyRecovery = true
	config.MinimumIntervalSeconds = 0
	service.configMu.Lock()
	service.webhook = config
	service.configMu.Unlock()
	service.deliver(context.Background(), alertEvent{name: "job_stuck", firing: false, message: "resolved", at: time.Now()})
	mu.Lock()
	if len(received) != 2 || received[1].Status != "resolved" {
		mu.Unlock()
		t.Fatalf("expected a recovery notification, got %+v", received)
	}
	mu.Unlock()

	disabled := config
	disabled.Enabled = false
	service.configMu.Lock()
	service.webhook = disabled
	service.configMu.Unlock()
	service.deliver(context.Background(), alertEvent{name: "db_health", firing: true, at: time.Now()})
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("disabled notifications must not be delivered: %+v", received)
	}
}
