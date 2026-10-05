package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery/ai"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
)

type testCore struct {
	pool    *pgxpool.Pool
	manager *downloads.Manager
}

// newTestCore builds an isolated schema with the shared migrations applied.
func newTestCore(t *testing.T) *testCore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a reachable PostgreSQL server to run this test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("cannot create a pool from TEST_DATABASE_URL: %v", err)
	}
	schema := "bridge_test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("cannot create an isolated test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("TEST_DATABASE_URL is invalid: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("cannot create a pool for the test schema: %v", err)
	}
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	t.Cleanup(func() {
		manager.Close()
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	return &testCore{pool: pool, manager: manager}
}

type chatFixture struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	reply    string
	requests []capturedChat
}

type capturedChat struct {
	path          string
	authorization string
	model         string
	maxTokens     int
	temperature   float64
	system        string
	user          string
}

func newChatFixture(t *testing.T, reply string) *chatFixture {
	t.Helper()
	fixture := &chatFixture{reply: reply}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload struct {
			Model       string    `json:"model"`
			MaxTokens   int       `json:"max_tokens"`
			Temperature float64   `json:"temperature"`
			Messages    []message `json:"messages"`
		}
		_ = json.Unmarshal(raw, &payload)
		captured := capturedChat{
			path: r.URL.Path, authorization: r.Header.Get("Authorization"), model: payload.Model,
			maxTokens: payload.MaxTokens, temperature: payload.Temperature,
		}
		for _, entry := range payload.Messages {
			if entry.Role == "system" {
				captured.system = entry.Content
			} else {
				captured.user = entry.Content
			}
		}
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, captured)
		status, reply := fixture.status, fixture.reply
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, reply)
			return
		}
		encoded, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
			"usage":   map[string]int{"total_tokens": 42},
		})
		w.Write(encoded)
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (f *chatFixture) set(status int, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.reply = status, reply
}

func (f *chatFixture) captured() []capturedChat {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedChat{}, f.requests...)
}

const translationReply = `{"language":"es","cues":[{"id":"1","text":"Hola <i>mundo</i>"},{"id":"2","text":"[MUSIC] suena"}]}`

func TestSharedTranslatorUsesStoredProviderConfig(t *testing.T) {
	core := newTestCore(t)
	ctx := context.Background()
	service, err := ai.New(ctx, core.pool)
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	translator := sharedTranslator{service: service}
	cues := []subtitles.Cue{
		{ID: "1", Start: 0, End: time.Second, Text: "Hello <i>world</i>"},
		{ID: "2", Start: time.Second, End: 2 * time.Second, Text: "[MUSIC] playing"},
	}
	request := subtitles.TranslationRequest{
		Language: "es", SourceLanguage: "en", Cues: cues, MaxTokens: 777, Temperature: 0.3,
	}

	// Without a stored provider the subtitle worker reports its own sentinel plus actionable guidance.
	_, err = translator.Translate(ctx, request)
	if !errors.Is(err, subtitles.ErrNotConfigured) {
		t.Fatalf("unconfigured translator error = %v; want subtitles.ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "AI provider") {
		t.Fatalf("unconfigured translator error is not actionable: %v", err)
	}

	fixture := newChatFixture(t, translationReply)
	if _, err := service.SetConfig(ctx, ai.Config{
		BaseURL: fixture.URL + "/v1", APIKey: "synthetic-subtitle-secret", Model: "subtitle-model",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	reply, err := translator.Translate(ctx, request)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if reply.Language != "es" || reply.Tokens != 42 || len(reply.Cues) != 2 {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Cues[0].ID != "1" || reply.Cues[0].Text != "Hola <i>mundo</i>" || reply.Cues[1].Start != time.Second {
		t.Fatalf("reply cues = %+v", reply.Cues)
	}
	captured := fixture.captured()
	if len(captured) != 1 {
		t.Fatalf("captured requests = %d; want one", len(captured))
	}
	request0 := captured[0]
	if request0.path != "/v1/chat/completions" || request0.authorization != "Bearer synthetic-subtitle-secret" ||
		request0.model != "subtitle-model" {
		t.Fatalf("request = %+v", request0)
	}
	// Subtitle chunk budgets come from the subtitle request, not from the recommendation settings.
	if request0.maxTokens != 777 || request0.temperature != 0.3 {
		t.Fatalf("chunk budgets = %d tokens at %v", request0.maxTokens, request0.temperature)
	}
	if !strings.Contains(strings.ToLower(request0.system), "translate subtitles") || !strings.Contains(request0.user, `"id":"1"`) {
		t.Fatalf("prompt = %q / %q", request0.system, request0.user)
	}

	// Provider failures are sanitized and never echo the key.
	fixture.set(http.StatusUnauthorized, `{"error":{"message":"bad key synthetic-subtitle-secret"}}`)
	_, err = translator.Translate(ctx, request)
	if err == nil || !strings.Contains(err.Error(), "redacted") {
		t.Fatalf("sanitized error = %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-subtitle-secret") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

func TestSharedTranslatorPicksUpSavedConfiguration(t *testing.T) {
	core := newTestCore(t)
	ctx := context.Background()
	service, err := ai.New(ctx, core.pool)
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	const singleCue = `{"language":"es","cues":[{"id":"1","text":"Hola"}]}`
	first := newChatFixture(t, singleCue)
	second := newChatFixture(t, singleCue)
	for _, step := range []struct {
		fixture *chatFixture
		key     string
		model   string
	}{
		{first, "first-secret", "first-model"},
		{second, "second-secret", "second-model"},
	} {
		if _, err := service.SetConfig(ctx, ai.Config{
			BaseURL: step.fixture.URL + "/v1", APIKey: step.key, Model: step.model,
		}); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if _, err := (sharedTranslator{service: service}).Translate(ctx, subtitles.TranslationRequest{
			Language: "es", Cues: []subtitles.Cue{{ID: "1", Text: "Hello"}},
		}); err != nil {
			t.Fatalf("Translate: %v", err)
		}
	}
	captured := second.captured()
	if len(captured) != 1 || captured[0].authorization != "Bearer second-secret" || captured[0].model != "second-model" {
		t.Fatalf("second provider request = %+v", captured)
	}
	if len(first.captured()) != 1 {
		t.Fatalf("first provider requests = %d", len(first.captured()))
	}
}

// TestSharedTranslatorPreservesDocumentCues runs the subtitle document pipeline over the bridge.
func TestSharedTranslatorPreservesDocumentCues(t *testing.T) {
	core := newTestCore(t)
	ctx := context.Background()
	service, err := ai.New(ctx, core.pool)
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	fixture := newChatFixture(t, translationReply)
	if _, err := service.SetConfig(ctx, ai.Config{
		BaseURL: fixture.URL + "/v1", APIKey: "synthetic-subtitle-secret", Model: "subtitle-model",
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	source := "1\n00:00:01,000 --> 00:00:02,500\nHello <i>world</i>\n\n2\n00:00:03,000 --> 00:00:04,000\n[MUSIC] playing\n\n"
	document, err := subtitles.ParseDocument([]byte(source))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	output, result, err := subtitles.TranslateDocument(ctx, sharedTranslator{service: service}, document,
		subtitles.TranslateRequest{Language: "es", SourceLanguage: "en"},
		subtitles.AIConfig{Enabled: true, MaxTokens: 321, Temperature: 0.1}, nil)
	if err != nil {
		t.Fatalf("TranslateDocument: %v", err)
	}
	if result.Cues != 2 {
		t.Fatalf("translated cues = %+v", result)
	}
	text := string(output)
	for _, want := range []string{"00:00:01,000 --> 00:00:02,500", "Hola <i>mundo</i>", "[MUSIC] suena", "00:00:03,000 --> 00:00:04,000"} {
		if !strings.Contains(text, want) {
			t.Fatalf("translated document is missing %q:\n%s", want, text)
		}
	}
	captured := fixture.captured()
	if len(captured) != 1 {
		t.Fatalf("chunk requests = %d; want one", len(captured))
	}
	if captured[0].maxTokens != 321 || captured[0].temperature != 0.1 {
		t.Fatalf("chunk budgets = %d tokens at %v", captured[0].maxTokens, captured[0].temperature)
	}
	if strings.Contains(text, "synthetic-subtitle-secret") {
		t.Fatalf("document leaked the API key:\n%s", text)
	}
}
