// Package ai wraps the optional OpenAI-compatible provider shared by recommendations and subtitles.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotConfigured = errors.New("ai: the AI provider is not configured")

const (
	configLock       int64 = 0x4149436F6E666967
	defaultMaxTokens       = 700
	maxMaxTokens           = 4096
	maxModelRunes          = 128
	maxPromptRunes         = 8000
	requestTimeout         = 60 * time.Second
	maxResponseBytes int64 = 1 << 20
	maxModels              = 200
)

// Config holds the provider settings; APIKey is write-only in API responses.
type Config struct {
	BaseURL          string  `json:"baseURL"`
	APIKey           string  `json:"apiKey,omitempty"`
	Model            string  `json:"model"`
	MaxTokens        int     `json:"maxTokens"`
	Temperature      float64 `json:"temperature"`
	APIKeyConfigured bool    `json:"apiKeyConfigured"`
}

// Ready reports whether the endpoint and model are set; local services may run without a key.
func (c Config) Ready() bool {
	return strings.TrimSpace(c.BaseURL) != "" && strings.TrimSpace(c.Model) != ""
}

// Error is a sanitized provider failure; the model output and credentials never appear in it.
type Error struct {
	Op     string
	Kind   string
	Status int
}

func (e *Error) Error() string {
	detail := "ai: " + e.Op + ": "
	switch e.Kind {
	case "not configured":
		return detail + "the AI provider is not configured"
	case "unauthorized":
		return detail + "the AI provider rejected the API key"
	case "rate limited":
		return detail + "the AI provider rate limit was reached; retry later"
	case "timed out":
		return detail + "the AI provider timed out; retry later"
	case "unavailable":
		return detail + "the AI provider is unavailable; retry later"
	case "redirect":
		return detail + "the AI provider redirected to another origin"
	case "response too large":
		return detail + "the AI provider response is too large"
	case "invalid response":
		return detail + "the AI provider returned an unexpected response"
	default:
		if e.Status != 0 {
			return detail + "HTTP status " + strconv.Itoa(e.Status)
		}
		return detail + "request failed"
	}
}

type Client struct {
	base        *url.URL
	apiKey      string
	model       string
	maxTokens   int
	temperature float64
	http        *http.Client
}

// NewClient validates one provider configuration and returns a ready client; the key is optional.
func NewClient(cfg Config) (*Client, error) {
	base, err := parseBase(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" || utf8.RuneCountInString(model) > maxModelRunes {
		return nil, &Error{Op: "config", Kind: "invalid config"}
	}
	client := &Client{
		base:        base,
		apiKey:      strings.TrimSpace(cfg.APIKey),
		model:       model,
		maxTokens:   clampTokens(cfg.MaxTokens),
		temperature: clampTemperature(cfg.Temperature),
	}
	client.http = &http.Client{Timeout: requestTimeout, CheckRedirect: client.checkRedirect}
	return client, nil
}

func parseBase(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, &Error{Op: "config", Kind: "invalid config"}
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, &Error{Op: "config", Kind: "invalid config"}
	}
	// Credentials or parameters in the base URL would leak the key or break path joining.
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &Error{Op: "config", Kind: "invalid config"}
	}
	if base.Path == "" {
		base.Path = "/"
	}
	return base, nil
}

func clampTokens(value int) int {
	if value <= 0 {
		return defaultMaxTokens
	}
	if value > maxMaxTokens {
		return maxMaxTokens
	}
	return value
}

func clampTemperature(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 2:
		return 2
	default:
		return value
	}
}

// Model reports the configured model name for recording alongside results.
func (c *Client) Model() string { return c.model }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends one prompt and returns the model text; output stays within the response limit.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || utf8.RuneCountInString(prompt) > maxPromptRunes {
		return "", &Error{Op: "complete", Kind: "invalid config"}
	}
	return c.complete(ctx, prompt, c.maxTokens)
}

func (c *Client) complete(ctx context.Context, prompt string, maxTokens int) (string, error) {
	var out chatResponse
	status, err := c.post(ctx, "complete", "chat/completions", chatRequest{
		Model: c.model, Messages: []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens: maxTokens, Temperature: c.temperature, Stream: false,
	}, &out)
	if err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", &Error{Op: "complete", Kind: "invalid response", Status: status}
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", &Error{Op: "complete", Kind: "invalid response", Status: status}
	}
	return content, nil
}

// Test verifies the endpoint: /models when the server exposes it, otherwise a minimal completion.
func (c *Client) Test(ctx context.Context) error {
	if _, err := c.Models(ctx); err == nil {
		return nil
	}
	_, err := c.complete(ctx, "Reply with OK.", 1)
	return err
}

func (c *Client) Models(ctx context.Context) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	status, err := c.get(ctx, "models", "models", &payload)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || utf8.RuneCountInString(id) > maxModelRunes || len(models) >= maxModels {
			continue
		}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, &Error{Op: "models", Kind: "invalid response", Status: status}
	}
	return models, nil
}

func (c *Client) get(ctx context.Context, op, suffix string, out any) (int, error) {
	return c.do(ctx, op, http.MethodGet, suffix, nil, out)
}

func (c *Client) post(ctx context.Context, op, suffix string, body, out any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, &Error{Op: op, Kind: "invalid response"}
	}
	return c.do(ctx, op, http.MethodPost, suffix, encoded, out)
}

func (c *Client) do(ctx context.Context, op, method, suffix string, body []byte, out any) (int, error) {
	target := c.base.JoinPath(suffix)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, &Error{Op: op, Kind: "request failed"}
	}
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		if errors.Is(err, errForeignRedirect) {
			return 0, &Error{Op: op, Kind: "redirect"}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return 0, &Error{Op: op, Kind: "timed out"}
		}
		return 0, &Error{Op: op, Kind: "unavailable"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, &Error{Op: op, Kind: "unavailable"}
	}
	if int64(len(raw)) > maxResponseBytes {
		return 0, &Error{Op: op, Kind: "response too large"}
	}
	if err := c.statusError(op, resp.StatusCode); err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, &Error{Op: op, Kind: "invalid response", Status: resp.StatusCode}
	}
	return resp.StatusCode, nil
}

var errForeignRedirect = errors.New("ai provider redirect left the configured origin")

// checkRedirect keeps the API key on the configured origin only.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 4 {
		return &Error{Op: "request", Kind: "redirect"}
	}
	if req.URL.Scheme != c.base.Scheme || req.URL.Host != c.base.Host {
		return errForeignRedirect
	}
	return nil
}

func (c *Client) statusError(op string, status int) error {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Op: op, Kind: "unauthorized", Status: status}
	case status == http.StatusTooManyRequests:
		return &Error{Op: op, Kind: "rate limited", Status: status}
	case status >= http.StatusInternalServerError:
		return &Error{Op: op, Kind: "unavailable", Status: status}
	default:
		return &Error{Op: op, Kind: "request failed", Status: status}
	}
}

type TestResult struct {
	OK     bool     `json:"ok"`
	Error  string   `json:"error,omitempty"`
	Models []string `json:"models,omitempty"`
}

// Service stores one write-only provider configuration and hands out clients on demand.
type Service struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errors.New("ai: a PostgreSQL pool is required")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('discovery_ai_config') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, errors.New("ai: the configuration store is unavailable")
	}
	if !exists {
		return nil, errors.New("ai: database migrations have not been applied")
	}
	return &Service{pool: pool}, nil
}

// GetConfig returns the stored configuration with the API key omitted.
func (s *Service) GetConfig(ctx context.Context) (Config, error) {
	cfg, err := s.stored(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Config{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		cfg = Config{MaxTokens: defaultMaxTokens, Temperature: 0.7}
	}
	return redacted(cfg), nil
}

func (s *Service) SetConfig(ctx context.Context, input Config) (Config, error) {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Model = strings.TrimSpace(input.Model)
	input.MaxTokens = clampTokens(input.MaxTokens)
	input.Temperature = clampTemperature(input.Temperature)
	if _, err := parseBase(input.BaseURL); err != nil {
		return Config{}, err
	}
	if input.Model == "" || utf8.RuneCountInString(input.Model) > maxModelRunes {
		return Config{}, &Error{Op: "config", Kind: "invalid config"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, configLock); err != nil {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	current, err := readConfig(ctx, tx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	// A blank key keeps the stored secret, matching write-only credential updates elsewhere.
	if strings.TrimSpace(input.APIKey) == "" {
		input.APIKey = current.APIKey
	}
	input.APIKeyConfigured = false
	data, err := json.Marshal(input)
	if err != nil {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO discovery_ai_config (id, data, updated_at) VALUES (true, $1, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, data); err != nil {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, errors.New("ai: the configuration could not be saved")
	}
	return redacted(input), nil
}

func (s *Service) stored(ctx context.Context) (Config, error) {
	return readConfig(ctx, s.pool)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readConfig(ctx context.Context, q querier) (Config, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT data FROM discovery_ai_config WHERE id`).Scan(&raw); err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, errors.New("ai: the stored configuration is unreadable")
	}
	return cfg, nil
}

func redacted(cfg Config) Config {
	cfg.APIKeyConfigured = strings.TrimSpace(cfg.APIKey) != ""
	cfg.APIKey = ""
	return cfg
}

func (s *Service) client(ctx context.Context) (*Client, bool, error) {
	cfg, err := s.stored(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !cfg.Ready() {
		return nil, false, nil
	}
	client, err := NewClient(cfg)
	if err != nil {
		return nil, false, err
	}
	return client, true, nil
}

// Client returns a configured provider client or ErrNotConfigured.
func (s *Service) Client(ctx context.Context) (*Client, error) {
	client, ok, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotConfigured
	}
	return client, nil
}

// ConfiguredClient reports whether a usable client exists without treating an unconfigured provider as an error.
func (s *Service) ConfiguredClient(ctx context.Context) (*Client, bool, error) {
	return s.client(ctx)
}

// Secret returns the stored configuration including the API key for in-process clients; never serialize it to HTTP.
func (s *Service) Secret(ctx context.Context) (Config, bool, error) {
	cfg, err := s.stored(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	return cfg, cfg.Ready(), nil
}

// Complete runs one prompt through the configured provider for callers such as subtitle translation.
func (s *Service) Complete(ctx context.Context, prompt string) (string, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return "", err
	}
	return client.Complete(ctx, prompt)
}

func (s *Service) Test(ctx context.Context) (TestResult, error) {
	client, err := s.Client(ctx)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) || isConfigError(err) {
			return TestResult{OK: false, Error: err.Error()}, nil
		}
		return TestResult{}, err
	}
	models, modelErr := client.Models(ctx)
	if modelErr == nil {
		return TestResult{OK: true, Models: models}, nil
	}
	if err := client.Test(ctx); err != nil {
		return TestResult{OK: false, Error: err.Error()}, nil
	}
	return TestResult{OK: true}, nil
}

func (s *Service) Models(ctx context.Context) ([]string, error) {
	client, err := s.Client(ctx)
	if err != nil {
		return nil, err
	}
	return client.Models(ctx)
}

func isConfigError(err error) bool {
	var providerErr *Error
	return errors.As(err, &providerErr) && providerErr.Kind == "invalid config"
}
