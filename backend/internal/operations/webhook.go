package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type webhookConfig struct {
	Enabled                bool   `json:"enabled"`
	URL                    string `json:"url"`
	HeaderName             string `json:"headerName"`
	Secret                 string `json:"secret"`
	TimeoutSeconds         int    `json:"timeoutSeconds"`
	MinimumIntervalSeconds int    `json:"minimumIntervalSeconds"`
	NotifyRecovery         bool   `json:"notifyRecovery"`
}

func defaultWebhook() webhookConfig {
	return webhookConfig{
		HeaderName: "X-Constellarr-Secret", TimeoutSeconds: 5, MinimumIntervalSeconds: 300, NotifyRecovery: true,
	}
}

func (c webhookConfig) normalized() webhookConfig {
	defaults := defaultWebhook()
	if c.HeaderName == "" {
		c.HeaderName = defaults.HeaderName
	}
	if c.TimeoutSeconds <= 0 || c.TimeoutSeconds > 30 {
		c.TimeoutSeconds = defaults.TimeoutSeconds
	}
	if c.MinimumIntervalSeconds < 0 || c.MinimumIntervalSeconds > 86400 {
		c.MinimumIntervalSeconds = defaults.MinimumIntervalSeconds
	}
	return c
}

var headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// validateWebhook keeps the secret out of requests and confines deliveries to absolute HTTP(S) URLs.
func validateWebhook(current, update webhookConfig) (webhookConfig, error) {
	update.URL = strings.TrimSpace(update.URL)
	update.HeaderName = strings.TrimSpace(update.HeaderName)
	update.Secret = strings.TrimSpace(update.Secret)
	if len(update.URL) > 2048 {
		return webhookConfig{}, InvalidError("webhook URL is too long")
	}
	if update.URL != "" {
		parsed, err := url.Parse(update.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return webhookConfig{}, InvalidError("webhook URL must be an absolute HTTP or HTTPS URL")
		}
		if parsed.User != nil {
			return webhookConfig{}, InvalidError("webhook URL must not contain credentials")
		}
	}
	if update.Enabled && update.URL == "" {
		return webhookConfig{}, InvalidError("a webhook URL is required before notifications are enabled")
	}
	if update.HeaderName == "" {
		update.HeaderName = defaultWebhook().HeaderName
	}
	if !headerNamePattern.MatchString(update.HeaderName) {
		return webhookConfig{}, InvalidError("webhook header name is not valid")
	}
	if update.Secret == "" {
		update.Secret = current.Secret
	}
	if len(update.Secret) > 512 {
		return webhookConfig{}, InvalidError("webhook secret is too long")
	}
	if update.TimeoutSeconds < 1 || update.TimeoutSeconds > 30 {
		return webhookConfig{}, InvalidError("webhook timeout must be between 1 and 30 seconds")
	}
	if update.MinimumIntervalSeconds < 0 || update.MinimumIntervalSeconds > 86400 {
		return webhookConfig{}, InvalidError("webhook minimum interval must be between 0 and 86400 seconds")
	}
	return update, nil
}

type webhookView struct {
	Enabled                bool   `json:"enabled"`
	URL                    string `json:"url"`
	HeaderName             string `json:"headerName"`
	SecretConfigured       bool   `json:"secretConfigured"`
	TimeoutSeconds         int    `json:"timeoutSeconds"`
	MinimumIntervalSeconds int    `json:"minimumIntervalSeconds"`
	NotifyRecovery         bool   `json:"notifyRecovery"`
}

func (c webhookConfig) view() webhookView {
	return webhookView{
		Enabled: c.Enabled, URL: c.URL, HeaderName: c.HeaderName, SecretConfigured: c.Secret != "",
		TimeoutSeconds: c.TimeoutSeconds, MinimumIntervalSeconds: c.MinimumIntervalSeconds,
		NotifyRecovery: c.NotifyRecovery,
	}
}

type alertEvent struct {
	id       int64
	name     string
	firing   bool
	severity string
	message  string
	value    float64
	at       time.Time
}

type alertNotification struct {
	Source   string    `json:"source"`
	Type     string    `json:"type"`
	Alert    string    `json:"alert"`
	Status   string    `json:"status"`
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
	Value    float64   `json:"value"`
	At       time.Time `json:"at"`
}

func (s *Service) webhookConfig() webhookConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.webhook
}

func (s *Service) runNotifier(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.notify:
			s.deliver(ctx, event)
		}
	}
}

func (s *Service) deliver(ctx context.Context, event alertEvent) {
	config := s.webhookConfig()
	if !config.Enabled || config.URL == "" {
		return
	}
	if !event.firing && !config.NotifyRecovery {
		return
	}
	status := "firing"
	if !event.firing {
		status = "resolved"
	}
	key := event.name + "|" + status
	window := time.Duration(config.MinimumIntervalSeconds) * time.Second
	now := time.Now()
	s.notifyMu.Lock()
	if last, ok := s.notifiedAt[key]; ok && window > 0 && now.Sub(last) < window {
		s.notifyMu.Unlock()
		return
	}
	s.notifiedAt[key] = now
	s.notifyMu.Unlock()

	payload := alertNotification{
		Source: "constellarr", Type: "alert", Alert: event.name, Status: status,
		Severity: event.severity, Message: event.message, Value: event.value, At: event.at,
	}
	if err := s.postWebhook(ctx, config, payload); err != nil {
		s.logger.Warn("operations: alert webhook failed", "alert", event.name, "status", status, "error", err.Error())
		if event.id != 0 {
			s.recordNotificationFailure(ctx, event.name, err)
		}
		return
	}
	if event.id != 0 {
		if _, err := s.pool.Exec(ctx, `UPDATE operations_alert_events SET notified_at = now() WHERE id = $1`, event.id); err != nil {
			s.logger.Debug("operations: alert notification could not be recorded")
		}
	}
}

func (s *Service) recordNotificationFailure(ctx context.Context, name string, cause error) {
	event := Event{
		Kind: KindNotificationFailed, Severity: severityWarning, Source: "webhook",
		Message: "Alert notification for " + name + " failed: " + cause.Error(), Ref: name,
	}
	if err := s.insertEvent(ctx, event.sanitized()); err != nil {
		s.logger.Debug("operations: notification failure could not be recorded")
	}
}

// postWebhook errors never include the endpoint URL, the secret or response bodies.
func (s *Service) postWebhook(ctx context.Context, config webhookConfig, payload alertNotification) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("notification payload could not be encoded")
	}
	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, config.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("webhook request could not be created")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "constellarr")
	if config.Secret != "" {
		request.Header.Set(config.HeaderName, config.Secret)
	}
	origin, err := url.Parse(config.URL)
	if err != nil {
		return errors.New("webhook endpoint is unreachable")
	}
	response, err := s.webhookClient(origin).Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return errors.New("webhook request timed out")
		}
		if errors.Is(err, errWebhookRedirect) {
			return errors.New("webhook redirect was refused")
		}
		return errors.New("webhook endpoint is unreachable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("webhook endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

var errWebhookRedirect = errors.New("webhook redirect was refused")

// webhookClient follows redirects only within the configured origin so the secret header cannot leak.
func (s *Service) webhookClient(origin *url.URL) *http.Client {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > 0 && sameOrigin(origin, next.URL) {
				return nil
			}
			return errWebhookRedirect
		},
	}
	if base := s.options.HTTPClient; base != nil {
		client.Timeout, client.Transport, client.Jar = base.Timeout, base.Transport, base.Jar
	}
	return client
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
