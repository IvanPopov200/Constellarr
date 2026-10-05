package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apiFixture(t *testing.T) (*serviceFixture, http.Handler) {
	t.Helper()
	fixture := newServiceFixture(t, nil)
	mux := http.NewServeMux()
	fixture.service.Register(mux)
	return fixture, mux
}

func apiRequest(t *testing.T, handler http.Handler, method, path, body string) (int, []byte) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func TestSubtitleAPIRoutesAndConfigRedaction(t *testing.T) {
	fixture, handler := apiFixture(t)
	ctx := context.Background()
	if _, err := fixture.service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	status, raw := apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"kind":"movie"`) {
		t.Fatalf("library: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/wanted", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"language":"en"`) {
		t.Fatalf("wanted: %d %s", status, raw)
	}
	// Config writes accept secrets but never return them.
	configBody := `{"enabled":true,"autoSearch":true,"autoDownload":true,"scanMinutes":30,"searchIntervalHours":6,` +
		`"retryMinutes":60,"cutoffScore":55,"providerTimeoutSeconds":20,"defaultProfileId":"default",` +
		`"providers":[{"id":"opensubtitles","name":"OpenSubtitles","type":"opensubtitles",` +
		`"endpoint":"https://api.opensubtitles.com/api/v1","username":"user","password":"hunter2",` +
		`"apiKey":"top-secret","enabled":true}],` +
		`"sync":{"helperPath":"","ffmpegPath":"","timeoutSeconds":600,"maxOffsetSeconds":60,"minScore":0,` +
		`"qualityMaxOffsetSeconds":30,"maxFramerateDeviation":0.1,"vad":"","audioReferenceSeconds":3600,` +
		`"maxEmbeddedStreamIndex":64},` +
		`"ai":{"enabled":false,"baseURL":"","model":"","timeoutSeconds":120,"maxTokens":4096,` +
		`"maxRequests":200,"maxTotalTokens":300000,"maxCharacters":6000,"temperature":0.2,"overrideExisting":false}}`
	status, raw = apiRequest(t, handler, http.MethodPut, "/api/v1/subtitle-config", configBody)
	if status != http.StatusOK {
		t.Fatalf("save config: %d %s", status, raw)
	}
	if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "hunter2") {
		t.Fatalf("config response leaked secrets: %s", raw)
	}
	var view Config
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Providers[0].APIKeySet || !view.Providers[0].PasswordSet {
		t.Fatalf("configured flags missing: %s", raw)
	}
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/providers", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"configured":true`) || strings.Contains(string(raw), "top-secret") {
		t.Fatalf("providers: %d %s", status, raw)
	}
	// Unknown fields and oversized bodies are rejected.
	status, _ = apiRequest(t, handler, http.MethodPut, "/api/v1/subtitle-config", `{"unknown":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", status)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/jobs/missing/cancel", "{}")
	if status != http.StatusNotFound {
		t.Fatalf("cancel missing job: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/music/movie-1/search", "{}")
	if status != http.StatusBadRequest {
		t.Fatalf("bad kind: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/unknown/search", "{}")
	if status != http.StatusNotFound {
		t.Fatalf("unknown video: %d %s", status, raw)
	}
}

func TestSubtitleAPIValidationAndErrorShape(t *testing.T) {
	fixture, handler := apiFixture(t)
	ctx := context.Background()
	rel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	sidecar := filepath.Join(fixture.root, filepath.FromSlash(rel))
	if err := os.WriteFile(sidecar, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	status, raw := apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/sync",
		`{"mode":"offset","path":"../../outside.en.srt","offsetSeconds":1}`)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("traversal sync: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/sync",
		`{"mode":"offset","path":"`+rel+`","offsetSeconds":0}`)
	if status != http.StatusBadRequest {
		t.Fatalf("zero offset: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/download",
		`{"providerId":"opensubtitles","fileId":"42","language":"en","unexpected":1}`)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid download request") {
		t.Fatalf("unknown field download: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/translate",
		`{"path":"`+rel+`","language":"de"}`)
	if status != http.StatusConflict || !strings.Contains(string(raw), "Connections") {
		t.Fatalf("translation without AI config: %d %s", status, raw)
	}
	// An omitted audioStream must mean automatic, not embedded stream zero.
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/sync",
		`{"mode":"audio","path":"`+rel+`"}`)
	if status != http.StatusAccepted {
		t.Fatalf("audio sync without a stream index: %d %s", status, raw)
	}
	// Sync queues a job and the sidecar download endpoint serves the file.
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/sync",
		`{"mode":"offset","path":"`+rel+`","offsetSeconds":1.5}`)
	if status != http.StatusAccepted {
		t.Fatalf("sync queue: %d %s", status, raw)
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil || job.Kind != "sync" {
		t.Fatalf("job = %+v (%s)", job, raw)
	}
	fixture.service.execute(ctx, job)
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/jobs/"+job.ID, "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"status":"done"`) {
		t.Fatalf("job status: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/movie/movie-1/file?path="+urlQueryEscape(rel), "")
	if status != http.StatusOK || !strings.Contains(string(raw), "00:00:02,500") {
		t.Fatalf("file download: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/movie/movie-1/file?path="+urlQueryEscape("../../etc/passwd"), "")
	if status != http.StatusBadRequest {
		t.Fatalf("traversal file: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodGet, "/api/v1/subtitles/movie/movie-1", "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"sidecars"`) {
		t.Fatalf("detail: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodPost, "/api/v1/subtitles/movie/movie-1/assignment",
		`{"profileId":"default","monitored":false}`)
	if status != http.StatusOK || !strings.Contains(string(raw), `"monitored":false`) {
		t.Fatalf("assignment: %d %s", status, raw)
	}
	status, raw = apiRequest(t, handler, http.MethodDelete, "/api/v1/subtitles/movie/movie-1/assignment", "")
	if status != http.StatusOK {
		t.Fatalf("reset assignment: %d %s", status, raw)
	}
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

func TestSubtitleErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{ErrNotFound, http.StatusNotFound},
		{ErrInvalid, http.StatusBadRequest},
		{ErrUnsafe, http.StatusBadRequest},
		{ErrConflict, http.StatusConflict},
		{ErrNotConfigured, http.StatusConflict},
		{ErrQuota, http.StatusTooManyRequests},
		{ErrRateLimited, http.StatusTooManyRequests},
		{ErrUnavailable, http.StatusServiceUnavailable},
		{ErrTimeout, http.StatusGatewayTimeout},
		{ErrCueIntegrity, http.StatusUnprocessableEntity},
		{ErrLanguageMismatch, http.StatusUnprocessableEntity},
		{ErrLowQualitySync, http.StatusUnprocessableEntity},
		{context.Canceled, http.StatusConflict},
		{errors.New("boom"), http.StatusInternalServerError},
		{dbError("query", errors.New("pq: relation does not exist")), http.StatusInternalServerError},
	}
	for _, item := range cases {
		status, message := subtitleError(item.err)
		if status != item.status {
			t.Fatalf("%v: status %d, want %d", item.err, status, item.status)
		}
		if message == "" || strings.Contains(message, "relation does not exist") {
			t.Fatalf("%v: unsafe message %q", item.err, message)
		}
	}
}
