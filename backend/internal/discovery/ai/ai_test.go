package ai_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery/ai"
)

const testKey = "synthetic-ai-secret"

func TestNewClientRejectsUnsafeConfiguration(t *testing.T) {
	base := ai.Config{BaseURL: "https://ai.example/v1", APIKey: testKey, Model: "test-model"}
	cases := map[string]ai.Config{
		"credentials in URL": {BaseURL: "https://user:pass@ai.example/v1", APIKey: testKey, Model: "test-model"},
		"query in URL":       {BaseURL: "https://ai.example/v1?key=1", APIKey: testKey, Model: "test-model"},
		"fragment in URL":    {BaseURL: "https://ai.example/v1#frag", APIKey: testKey, Model: "test-model"},
		"other scheme":       {BaseURL: "ftp://ai.example/v1", APIKey: testKey, Model: "test-model"},
		"relative URL":       {BaseURL: "/v1", APIKey: testKey, Model: "test-model"},
		"empty base URL":     {APIKey: testKey, Model: "test-model"},
		"missing model":      {BaseURL: "https://ai.example/v1", APIKey: testKey},
	}
	for name, cfg := range cases {
		if _, err := ai.NewClient(cfg); err == nil {
			t.Errorf("%s: NewClient accepted %+v", name, cfg)
		} else if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: error leaked the API key: %v", name, err)
		}
	}
	if _, err := ai.NewClient(base); err != nil {
		t.Fatalf("NewClient rejected a valid configuration: %v", err)
	}
}

// TestKeylessLocalEndpointOmitsAuthorization keeps local OpenAI-compatible services usable.
func TestKeylessLocalEndpointOmitsAuthorization(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()
	keyless, err := ai.NewClient(ai.Config{BaseURL: server.URL + "/v1", Model: "local-model"})
	if err != nil {
		t.Fatalf("NewClient rejected a keyless local endpoint: %v", err)
	}
	if _, err := keyless.Complete(context.Background(), "hello"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if authorization != "" {
		t.Fatalf("keyless client sent authorization %q", authorization)
	}
}

func TestClientCompleteModelsAndAuthorization(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if r.Method == http.MethodPost {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			gotBody = string(body)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"  {\"candidates\":[]}  "}}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer server.Close()

	client, err := ai.NewClient(ai.Config{BaseURL: server.URL + "/v1", APIKey: testKey, Model: "model-a", MaxTokens: 12, Temperature: 0.2})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	content, err := client.Complete(context.Background(), "Suggest titles")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(content) != `{"candidates":[]}` {
		t.Fatalf("Complete content = %q", content)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer "+testKey {
		t.Fatalf("request path %q, authorization %q", gotPath, gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"model-a"`) || !strings.Contains(gotBody, `"max_tokens":12`) {
		t.Fatalf("chat body = %s", gotBody)
	}

	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if gotPath != "/v1/models" || len(models) != 2 || models[1] != "model-b" {
		t.Fatalf("models = %v from %q", models, gotPath)
	}
	if err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestClientRedirectStaysOnConfiguredOrigin(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect leaked a request to another origin")
	}))
	defer foreign.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer local.Close()

	client, err := ai.NewClient(ai.Config{BaseURL: local.URL, APIKey: testKey, Model: "model-a"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Complete(context.Background(), "hello")
	var providerErr *ai.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "redirect" {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
}

func TestClientLimitsAndSanitizesFailures(t *testing.T) {
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"`+strings.Repeat("x", 1<<20)+`"}}]}`)
	}))
	defer large.Close()
	client, err := ai.NewClient(ai.Config{BaseURL: large.URL, APIKey: testKey, Model: "model-a"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Complete(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response error = %v", err)
	}

	cases := map[string]struct {
		status     int
		body, want string
	}{
		"unauthorized": {http.StatusUnauthorized, `{"error":{"message":"bad key ` + testKey + `"}}`, "rejected the API key"},
		"malformed":    {http.StatusOK, `not json`, "unexpected response"},
		"empty":        {http.StatusOK, `{"choices":[]}`, "unexpected response"},
	}
	for name, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		client, err := ai.NewClient(ai.Config{BaseURL: server.URL, APIKey: testKey, Model: "model-a"})
		if err != nil {
			server.Close()
			t.Fatalf("%s: NewClient: %v", name, err)
		}
		_, err = client.Complete(context.Background(), "hello")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: error leaked the API key: %v", name, err)
		}
		server.Close()
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer slow.Close()
	client, err = ai.NewClient(ai.Config{BaseURL: slow.URL, APIKey: testKey, Model: "model-a"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := client.Complete(ctx, "hello"); !errors.Is(err, context.DeadlineExceeded) && err == nil {
		t.Fatalf("timed out request error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timed out request took %s", elapsed)
	}
}
