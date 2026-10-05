package subtitles

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

const (
	translationBodyLimit = 2 << 20
	defaultChunkChars    = 6000
	maxChunkChars        = 20000
	defaultChunkCues     = 100
	maxChunkCues         = 500
	defaultAITimeout     = 120
	defaultMaxTokens     = 4096
	defaultMaxRequests   = 200
	defaultTotalTokens   = 300000
	translationRetries   = 2
)

var labelPattern = regexp.MustCompile(`[\[(]([A-Z0-9][A-Z0-9 '.,!?&/+-]{0,38})[\])]`)

// TranslationRequest is one bounded chunk of cues sent to a translator.
type TranslationRequest struct {
	Language       string
	SourceLanguage string
	Cues           []Cue
	MaxTokens      int
	Temperature    float64
	// Correction carries the previous validation failure on a bounded retry.
	Correction string
}

// TranslationReply is a validated translation of one chunk.
type TranslationReply struct {
	Language string
	Cues     []Cue
	Tokens   int
}

// Translator translates subtitle cue text through a configured AI service.
type Translator interface {
	Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error)
}

// OpenAITranslator calls any OpenAI-compatible chat completions endpoint.
type OpenAITranslator struct {
	baseURL string
	apiKey  string
	model   string
	timeout time.Duration
	client  *http.Client
}

func NewOpenAITranslator(cfg AIConfig) (*OpenAITranslator, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: the AI base URL must be an http(s) URL", ErrInvalid)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("%w: an AI model name is required", ErrInvalid)
	}
	timeout := cfg.TimeoutSeconds
	if timeout <= 0 || timeout > 900 {
		timeout = defaultAITimeout
	}
	return &OpenAITranslator{
		baseURL: endpoint,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		model:   strings.TrimSpace(cfg.Model),
		timeout: time.Duration(timeout) * time.Second,
		client:  &http.Client{},
	}, nil
}

func (t *OpenAITranslator) Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error) {
	if len(req.Cues) == 0 {
		return TranslationReply{}, fmt.Errorf("%w: no cues to translate", ErrInvalid)
	}
	target, ok := NormalizeLanguage(req.Language)
	if !ok {
		return TranslationReply{}, fmt.Errorf("%w: unsupported target language %q", ErrInvalid, req.Language)
	}
	requestCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	body, err := json.Marshal(t.chatRequest(target, req))
	if err != nil {
		return TranslationReply{}, errors.New("subtitles: translation request could not be built")
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, t.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return TranslationReply{}, errors.New("subtitles: translation request could not be built")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if t.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return TranslationReply{}, t.sanitize("translation service request failed", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, translationBodyLimit+1))
	if err != nil {
		return TranslationReply{}, t.sanitize("translation service response failed", err)
	}
	if len(raw) > translationBodyLimit {
		return TranslationReply{}, errors.New("subtitles: translation service response exceeded the size limit")
	}
	if response.StatusCode != http.StatusOK {
		return TranslationReply{}, t.sanitize(fmt.Sprintf("translation service returned HTTP %d", response.StatusCode),
			errors.New(upstreamMessage(raw)))
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return TranslationReply{}, errors.New("subtitles: translation service returned an unreadable response")
	}
	reply, err := validateReply(parsed.Choices[0].Message.Content, req.Cues, target)
	if err != nil {
		return TranslationReply{}, err
	}
	reply.Tokens = parsed.Usage.TotalTokens
	return reply, nil
}

func (t *OpenAITranslator) sanitize(prefix string, err error) error {
	message := err.Error()
	if t.apiKey != "" {
		message = strings.ReplaceAll(message, t.apiKey, "[redacted]")
	}
	message = strings.Join(strings.Fields(message), " ")
	return fmt.Errorf("%s: %s", prefix, truncate(message, 300))
}

func (t *OpenAITranslator) chatRequest(language string, req TranslationRequest) map[string]any {
	system := "You translate subtitles. Reply with one JSON object: {\"language\":\"" + language +
		"\",\"cues\":[{\"id\":\"...\",\"text\":\"...\"}]}. Translate only the text of every given cue exactly once, " +
		"keep the same cue ids, keep inline markup such as <i>...</i> and {\\an8} byte-for-byte, and keep non-dialogue " +
		"labels like [MUSIC] or (SIGHS) unchanged. Never add, merge, split, drop, or reorder cues. Do not include timestamps."
	if req.SourceLanguage != "" {
		system += " The source language is " + req.SourceLanguage + "."
	}
	if req.Correction != "" {
		system += " The previous reply was rejected: " + req.Correction + " Reply with corrected JSON only."
	}
	payload := make([]map[string]string, 0, len(req.Cues))
	for _, cue := range req.Cues {
		payload = append(payload, map[string]string{"id": cue.ID, "text": cue.Text})
	}
	cuesJSON, _ := json.Marshal(payload)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	return map[string]any{
		"model": t.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": string(cuesJSON)},
		},
		"temperature":     req.Temperature,
		"max_tokens":      maxTokens,
		"response_format": map[string]string{"type": "json_object"},
		"stream":          false,
	}
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func upstreamMessage(raw []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "unreadable error response"
	}
	if parsed.Error.Message != "" {
		return truncate(parsed.Error.Message, 200)
	}
	if parsed.Message != "" {
		return truncate(parsed.Message, 200)
	}
	return "no error detail"
}

// validateReply enforces exact cue identity, language, markup, and label preservation.
func validateReply(content string, requested []Cue, language string) (TranslationReply, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	var parsed struct {
		Language string `json:"language"`
		Cues     []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"cues"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return TranslationReply{}, fmt.Errorf("%w: translation reply is not the requested JSON object", ErrCueIntegrity)
	}
	replyLanguage, ok := NormalizeLanguage(parsed.Language)
	if !ok || !sameBaseLanguage(replyLanguage, language) {
		return TranslationReply{}, fmt.Errorf("%w: expected %s, received %q", ErrLanguageMismatch, language, truncate(parsed.Language, 32))
	}
	if len(parsed.Cues) != len(requested) {
		return TranslationReply{}, fmt.Errorf("%w: expected %d cues, received %d", ErrCueIntegrity, len(requested), len(parsed.Cues))
	}
	wanted := make(map[string]Cue, len(requested))
	for _, cue := range requested {
		wanted[cue.ID] = cue
	}
	seen := make(map[string]bool, len(parsed.Cues))
	out := make([]Cue, 0, len(parsed.Cues))
	for _, cue := range parsed.Cues {
		source, ok := wanted[cue.ID]
		if !ok {
			return TranslationReply{}, fmt.Errorf("%w: unexpected cue %q", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		if seen[cue.ID] {
			return TranslationReply{}, fmt.Errorf("%w: duplicate cue %q", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		seen[cue.ID] = true
		text := strings.TrimSpace(cue.Text)
		if text == "" {
			return TranslationReply{}, fmt.Errorf("%w: cue %q has no translated text", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		if !equalMarkers(markers(source.Text), markers(text)) {
			return TranslationReply{}, fmt.Errorf("%w: cue %q changed inline formatting", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		if !preservesLabels(source.Text, text) {
			return TranslationReply{}, fmt.Errorf("%w: cue %q changed a non-dialogue label", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		out = append(out, Cue{ID: cue.ID, Start: source.Start, End: source.End, Text: text})
	}
	if len(seen) != len(wanted) {
		return TranslationReply{}, fmt.Errorf("%w: not every cue was returned exactly once", ErrCueIntegrity)
	}
	return TranslationReply{Language: replyLanguage, Cues: out}, nil
}

func sameBaseLanguage(a, b string) bool {
	aBase, _, _ := strings.Cut(strings.ToLower(a), "-")
	bBase, _, _ := strings.Cut(strings.ToLower(b), "-")
	return aBase == bBase
}

// preservesLabels requires uppercase bracketed labels to survive translation unchanged.
func preservesLabels(source, translated string) bool {
	for _, label := range labelPattern.FindAllString(source, -1) {
		if !strings.Contains(translated, label) {
			return false
		}
	}
	return true
}

// chunkCues splits cues into bounded chunks without ever splitting one cue.
func chunkCues(cues []Cue, maxChars, maxCues int) ([][]Cue, error) {
	if maxChars <= 0 {
		maxChars = defaultChunkChars
	}
	if maxChars > maxChunkChars {
		maxChars = maxChunkChars
	}
	if maxCues <= 0 {
		maxCues = defaultChunkCues
	}
	if maxCues > maxChunkCues {
		maxCues = maxChunkCues
	}
	chunks := [][]Cue{}
	current := []Cue{}
	chars := 0
	for _, cue := range cues {
		if len(cue.Text) > maxChars {
			return nil, fmt.Errorf("%w: cue %s is too long to translate safely", ErrInvalid, truncate(cue.ID, 64))
		}
		if len(current) >= maxCues || (chars+len(cue.Text) > maxChars && len(current) > 0) {
			chunks = append(chunks, current)
			current, chars = []Cue{}, 0
		}
		current = append(current, cue)
		chars += len(cue.Text)
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks, nil
}

// TranslateDocument translates every cue in the document and rewrites only cue text.
func TranslateDocument(ctx context.Context, translator Translator, document *Document, req TranslateRequest, cfg AIConfig, progress func(done, total int)) ([]byte, TranslateResult, error) {
	target, ok := NormalizeLanguage(req.Language)
	if !ok {
		return nil, TranslateResult{}, fmt.Errorf("%w: unsupported target language %q", ErrInvalid, req.Language)
	}
	chunks, err := chunkCues(document.Cues(), cfg.MaxCharacters, defaultChunkCues)
	if err != nil {
		return nil, TranslateResult{}, err
	}
	maxRequests := cfg.MaxRequests
	if maxRequests <= 0 {
		maxRequests = defaultMaxRequests
	}
	budget := cfg.MaxTotalTokens
	if budget <= 0 {
		budget = defaultTotalTokens
	}
	translations := map[string]string{}
	result := TranslateResult{Language: target}
	for index, chunk := range chunks {
		reply, err := translateChunk(ctx, translator, chunk, target, req.SourceLanguage, cfg)
		if err != nil {
			return nil, TranslateResult{}, err
		}
		result.Requests += reply.requests
		result.Tokens += reply.tokens
		if result.Requests > maxRequests {
			return nil, TranslateResult{}, fmt.Errorf("%w: translation exceeded %d requests", ErrBudget, maxRequests)
		}
		if result.Tokens > budget {
			return nil, TranslateResult{}, fmt.Errorf("%w: translation exceeded %d tokens", ErrBudget, budget)
		}
		for _, cue := range reply.cues {
			translations[cue.ID] = cue.Text
		}
		if progress != nil {
			progress(index+1, len(chunks))
		}
	}
	out, err := document.Apply(translations)
	if err != nil {
		return nil, TranslateResult{}, err
	}
	result.Cues = len(translations)
	return out, result, nil
}

type chunkReply struct {
	cues     []Cue
	tokens   int
	requests int
}

func translateChunk(ctx context.Context, translator Translator, chunk []Cue, language, sourceLanguage string, cfg AIConfig) (chunkReply, error) {
	correction := ""
	var lastErr error
	for attempt := 0; attempt <= translationRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return chunkReply{}, err
		}
		reply, err := translator.Translate(ctx, TranslationRequest{
			Language:       language,
			SourceLanguage: sourceLanguage,
			Cues:           chunk,
			MaxTokens:      cfg.MaxTokens,
			Temperature:    cfg.Temperature,
			Correction:     correction,
		})
		if err == nil {
			err = verifyChunkReply(chunk, reply)
		}
		if err == nil {
			return chunkReply{cues: reply.Cues, tokens: reply.Tokens, requests: attempt + 1}, nil
		}
		lastErr = err
		if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrTimeout) || ctx.Err() != nil {
			return chunkReply{}, err
		}
		correction = truncate(err.Error(), 300)
	}
	return chunkReply{}, fmt.Errorf("subtitles: translation rejected after %d attempts: %w", translationRetries+1, lastErr)
}

func verifyChunkReply(chunk []Cue, reply TranslationReply) error {
	if len(reply.Cues) != len(chunk) {
		return fmt.Errorf("%w: expected %d cues, received %d", ErrCueIntegrity, len(chunk), len(reply.Cues))
	}
	byID := make(map[string]Cue, len(chunk))
	for _, cue := range chunk {
		byID[cue.ID] = cue
	}
	seen := map[string]bool{}
	for _, cue := range reply.Cues {
		source, ok := byID[cue.ID]
		if !ok || seen[cue.ID] {
			return fmt.Errorf("%w: cue identity mismatch at %s", ErrCueIntegrity, truncate(cue.ID, 64))
		}
		seen[cue.ID] = true
		if !equalMarkers(markers(source.Text), markers(cue.Text)) || !preservesLabels(source.Text, cue.Text) {
			return fmt.Errorf("%w: cue %s did not preserve formatting or labels", ErrCueIntegrity, truncate(cue.ID, 64))
		}
	}
	return nil
}
