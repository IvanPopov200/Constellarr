package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chatServer(t *testing.T, content string) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var lastRequest []byte
	var lastAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		lastAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		lastRequest = body
		w.Write([]byte(`{"choices":[{"message":{"content":` + strconvQuote(content) + `}}],"usage":{"total_tokens":123}}`))
	}))
	t.Cleanup(server.Close)
	return server, &lastRequest, &lastAuth
}

func strconvQuote(text string) string {
	encoded, _ := json.Marshal(text)
	return string(encoded)
}

func TestOpenAITranslatorBuildsStrictRequest(t *testing.T) {
	server, lastRequest, lastAuth := chatServer(t, `{"language":"de","cues":[{"id":"1","text":"Hallo <i>Welt</i>"}]}`)
	translator, err := NewOpenAITranslator(AIConfig{
		BaseURL: server.URL + "/v1", APIKey: "secret-key", Model: "test-model",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := translator.Translate(context.Background(), TranslationRequest{
		Language: "de",
		Cues:     []Cue{{ID: "1", Text: "Hello <i>world</i>", Start: 1, End: 2}},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if len(reply.Cues) != 1 || reply.Cues[0].Text != "Hallo <i>Welt</i>" || reply.Tokens != 123 {
		t.Fatalf("reply = %+v", reply)
	}
	if *lastAuth != "Bearer secret-key" {
		t.Fatalf("authorization = %q", *lastAuth)
	}
	var sent map[string]any
	if err := json.Unmarshal(*lastRequest, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["model"] != "test-model" || sent["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("request = %v", sent)
	}
	messages := sent["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "keep inline markup") || !strings.Contains(system, "exactly once") {
		t.Fatalf("system prompt = %q", system)
	}
}

func TestOpenAITranslatorRejectsAdversarialReplies(t *testing.T) {
	cases := map[string]string{
		"missing cue":      `{"language":"de","cues":[]}`,
		"extra cue":        `{"language":"de","cues":[{"id":"1","text":"Hallo"},{"id":"9","text":"Neu"}]}`,
		"duplicate cue":    `{"language":"de","cues":[{"id":"1","text":"Hallo"},{"id":"1","text":"Hallo"}]}`,
		"changed language": `{"language":"fr","cues":[{"id":"1","text":"Hallo"},{"id":"2","text":"Tschüss"}]}`,
		"dropped markup":   `{"language":"de","cues":[{"id":"1","text":"Hallo Welt"},{"id":"2","text":"Tschüss"}]}`,
		"changed label":    `{"language":"de","cues":[{"id":"1","text":"Hallo <i>Welt</i>"},{"id":"2","text":"Seufzer"}]}`,
		"not json":         `I cannot do that`,
		"empty text":       `{"language":"de","cues":[{"id":"1","text":""},{"id":"2","text":"Tschüss"}]}`,
	}
	for name, content := range cases {
		server, _, _ := chatServer(t, content)
		translator, err := NewOpenAITranslator(AIConfig{BaseURL: server.URL + "/v1", Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		cues := []Cue{{ID: "1", Text: "Hello <i>world</i>"}, {ID: "2", Text: "[SIGHS]"}}
		if name == "changed label" {
			cues = []Cue{{ID: "1", Text: "Hello <i>world</i>"}, {ID: "2", Text: "[SIGHS]"}}
		}
		if _, err := translator.Translate(context.Background(), TranslationRequest{Language: "de", Cues: cues}); err == nil {
			t.Fatalf("%s: expected a validation failure", name)
		}
	}
}

func TestOpenAITranslatorErrorsDoNotLeakKeys(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key secret-key"}}`))
	}))
	defer server.Close()
	translator, err := NewOpenAITranslator(AIConfig{BaseURL: server.URL, APIKey: "secret-key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = translator.Translate(context.Background(), TranslationRequest{Language: "de", Cues: []Cue{{ID: "1", Text: "Hi"}}})
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error leaked credentials: %v", err)
	}
	if _, err := NewOpenAITranslator(AIConfig{BaseURL: "ftp://x", Model: "m"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("base url validation = %v", err)
	}
	if _, err := NewOpenAITranslator(AIConfig{BaseURL: "http://x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("model validation = %v", err)
	}
}

// flakyTranslator returns scripted replies to exercise bounded retries.
type flakyTranslator struct {
	replies []TranslationReply
	errs    []error
	calls   int
}

func (f *flakyTranslator) Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error) {
	index := f.calls
	f.calls++
	if index < len(f.errs) && f.errs[index] != nil {
		return TranslationReply{}, f.errs[index]
	}
	if index < len(f.replies) {
		return f.replies[index], nil
	}
	return TranslationReply{}, errors.New("no scripted reply")
}

func TestTranslateDocumentRetriesThenSucceeds(t *testing.T) {
	document, err := ParseDocument([]byte(sampleSRT))
	if err != nil {
		t.Fatal(err)
	}
	translator := &flakyTranslator{
		replies: []TranslationReply{
			{Cues: []Cue{{ID: "1", Text: "Hallo <i>Welt</i>"}}, Tokens: 10},
			{Cues: []Cue{{ID: "1", Text: "Hallo <i>Welt</i>"}, {ID: "2", Text: "Tschüss"}}, Tokens: 20},
		},
		errs: []error{ErrCueIntegrity},
	}
	out, result, err := TranslateDocument(context.Background(), translator, document, TranslateRequest{Language: "de"}, AIConfig{}, nil)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if result.Cues != 2 || result.Tokens != 20 || result.Requests != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(string(out), "Hallo <i>Welt</i>") || !strings.Contains(string(out), "00:00:01,000 --> 00:00:03,500") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(string(out), "00:01:00,000 --> 00:01:02,000") {
		t.Fatalf("cue order or timing changed: %q", out)
	}
}

func TestTranslateDocumentFailsClosedOnPersistentRejection(t *testing.T) {
	document, err := ParseDocument([]byte(sampleSRT))
	if err != nil {
		t.Fatal(err)
	}
	translator := &flakyTranslator{replies: []TranslationReply{
		{Cues: []Cue{{ID: "1", Text: "Hallo <i>Welt</i>"}}},
		{Cues: []Cue{{ID: "1", Text: "Hallo <i>Welt</i>"}}},
		{Cues: []Cue{{ID: "1", Text: "Hallo <i>Welt</i>"}}},
	}}
	out, _, err := TranslateDocument(context.Background(), translator, document, TranslateRequest{Language: "de"}, AIConfig{}, nil)
	if !errors.Is(err, ErrCueIntegrity) {
		t.Fatalf("error = %v", err)
	}
	if out != nil {
		t.Fatal("a failed translation must not produce a payload")
	}
	if translator.calls != translationRetries+1 {
		t.Fatalf("retries = %d", translator.calls)
	}
}

func TestTranslateDocumentEnforcesTokenBudget(t *testing.T) {
	payload := "1\n00:00:01,000 --> 00:00:02,000\nA\n\n2\n00:00:03,000 --> 00:00:04,000\nB\n"
	document, err := ParseDocument([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	script := func() *flakyTranslator {
		return &flakyTranslator{replies: []TranslationReply{
			{Cues: []Cue{{ID: "1", Text: "A"}}, Tokens: 500},
			{Cues: []Cue{{ID: "2", Text: "B"}}, Tokens: 500},
		}}
	}
	_, _, err = TranslateDocument(context.Background(), script(), document, TranslateRequest{Language: "de"},
		AIConfig{MaxCharacters: 1, MaxRequests: 1}, nil)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("request budget error = %v", err)
	}
	_, _, err = TranslateDocument(context.Background(), script(), document, TranslateRequest{Language: "de"},
		AIConfig{MaxCharacters: 1, MaxTotalTokens: 600}, nil)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("token budget error = %v", err)
	}
}

func TestChunkCuesSplitsOnBoundsWithoutSplittingCues(t *testing.T) {
	cues := []Cue{{ID: "1", Text: strings.Repeat("a", 10)}, {ID: "2", Text: strings.Repeat("b", 10)}, {ID: "3", Text: strings.Repeat("c", 10)}}
	chunks, err := chunkCues(cues, 15, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	chunks, err = chunkCues(cues, 100, 2)
	if err != nil || len(chunks) != 2 || len(chunks[0]) != 2 {
		t.Fatalf("cue-count chunking = %v, %v", chunks, err)
	}
	if _, err := chunkCues([]Cue{{ID: "1", Text: strings.Repeat("a", 100)}}, 10, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized cue error = %v", err)
	}
}

func TestTranslateDocumentPreservesBytesOutsideCueText(t *testing.T) {
	payload := "1\r\n00:00:01,000 --> 00:00:02,000\r\n<i>Hello</i> [MUSIC]\r\n"
	document, err := ParseDocument([]byte(payload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	translator := &flakyTranslator{replies: []TranslationReply{
		{Cues: []Cue{{ID: document.Cues()[0].ID, Text: "<i>Hallo</i> [MUSIC]"}}, Tokens: 7},
	}}
	out, _, err := TranslateDocument(context.Background(), translator, document, TranslateRequest{Language: "de"}, AIConfig{}, nil)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if string(out) != "1\r\n00:00:01,000 --> 00:00:02,000\r\n<i>Hallo</i> [MUSIC]\r\n" {
		t.Fatalf("output = %q", out)
	}
}
