package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
)

// fakeCatalog serves videos from memory so service tests do not need movie or TV rows.
type fakeCatalog struct {
	videos []Video
}

func (f *fakeCatalog) Videos(ctx context.Context) ([]Video, error) { return f.videos, nil }

func (f *fakeCatalog) Video(ctx context.Context, kind, id string) (Video, error) {
	for _, video := range f.videos {
		if video.Kind == kind && video.ID == id {
			return video, nil
		}
	}
	return Video{}, ErrNotFound
}

func testPool(t *testing.T) *pgxpool.Pool {
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
	schema := "subtitle_test_" + strings.ToLower(fmt.Sprintf("%d_%d", time.Now().UnixNano(), os.Getpid()))
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
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	return pool
}

// migrateTestSchema applies the shared migrations through the downloads manager.
func migrateTestSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := downloads.New(context.Background(), pool, downloads.Config{Directory: t.TempDir()}); err != nil {
		t.Fatalf("downloads.New (migrations): %v", err)
	}
}

type serviceFixture struct {
	service   *Service
	pool      *pgxpool.Pool
	catalog   *fakeCatalog
	root      string
	video     Video
	provider  *httptest.Server
	searchHit bool
}

func videoFixture(t *testing.T, root string) Video {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "Movies", "Film (2020)"), 0o755); err != nil {
		t.Fatal(err)
	}
	videoPath := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].mkv"
	if err := os.WriteFile(filepath.Join(root, videoPath), []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Video{
		Kind: KindMovie, ID: "movie-1", Title: "Film", Year: 2020, IMDbID: "tt0133093",
		RootID: "movies", Path: videoPath, Size: 17, rootPath: root,
	}
}

func newServiceFixture(t *testing.T, handler http.HandlerFunc) *serviceFixture {
	t.Helper()
	pool := testPool(t)
	migrateTestSchema(t, pool)
	root := t.TempDir()
	video := videoFixture(t, root)
	catalog := &fakeCatalog{videos: []Video{video}}
	service, err := New(context.Background(), pool, Options{Catalog: catalog})
	if err != nil {
		t.Fatalf("subtitles.New: %v", err)
	}
	t.Cleanup(service.Close)
	fixture := &serviceFixture{service: service, pool: pool, catalog: catalog, root: root, video: video}
	if handler != nil {
		fixture.provider = httptest.NewServer(handler)
		t.Cleanup(fixture.provider.Close)
	}
	return fixture
}

// configureProviders points the service at a local OpenSubtitles-shaped test server.
func (f *serviceFixture) configureProviders(t *testing.T, endpoint string) {
	t.Helper()
	ctx := context.Background()
	cfg, err := f.service.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = []Provider{{
		ID: "opensubtitles", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
		Endpoint: endpoint, APIKey: "test-key", Enabled: true,
	}}
	cfg.ScanMinutes = 5
	cfg.RetryMinutes = 5
	cfg.ProviderTimeoutSeconds = 10
	if _, err := f.service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
}

func openSubtitlesFixture(t *testing.T, payload string, hits *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles":
			*hits++
			if r.Header.Get("Api-Key") != "test-key" {
				t.Errorf("search Api-Key = %q", r.Header.Get("Api-Key"))
			}
			w.Write([]byte(`{"data":[{"attributes":{"subtitle_id":"7","language":"en","download_count":900,"hearing_impaired":false,"foreign_parts_only":false,"format":"srt","fps":23.976,"ratings":8.4,"release":"Film.2020.WEBDL","files":[{"file_id":42,"file_name":"film.2020.en.srt"}]}}]}`))
		case "/download":
			w.Write([]byte(`{"link":"http://` + r.Host + `/files/42.srt","file_name":"film.2020.en.srt","requests":1,"remaining":17,"reset_time_utc":"2026-10-06 00:00:00"}`))
		case "/files/42.srt":
			w.Write([]byte(payload))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestServiceScanWantedAndAutoDownload(t *testing.T) {
	hits := 0
	fixture := newServiceFixture(t, nil)
	payload := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	fixture.provider = httptest.NewServer(openSubtitlesFixture(t, payload, &hits))
	defer fixture.provider.Close()
	fixture.configureProviders(t, fixture.provider.URL)
	ctx := context.Background()

	summary, err := fixture.service.Scan(ctx)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Videos != 1 || summary.Sidecars != 0 || summary.Wanted != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	items, err := fixture.service.Library(ctx, LibraryFilter{})
	if err != nil || len(items) != 1 || items[0].Missing != 1 {
		t.Fatalf("library = %+v, %v", items, err)
	}
	cfg, err := fixture.service.store.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := fixture.service.store.DueWanted(ctx, 10)
	if err != nil || len(wanted) != 1 {
		t.Fatalf("due wanted = %+v, %v", wanted, err)
	}
	fixture.service.autoSearchItem(ctx, cfg, wanted[0])
	jobs, err := fixture.service.Jobs(ctx, true, 10)
	if err != nil || len(jobs) != 1 || jobs[0].Kind != "download" {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	fixture.service.execute(ctx, jobs[0])
	job, err := fixture.service.Job(ctx, jobs[0].ID)
	if err != nil || job.Status != "done" {
		t.Fatalf("job = %+v, %v", job, err)
	}
	if hits != 1 {
		t.Fatalf("provider hits = %d", hits)
	}
	sidecar := filepath.Join(fixture.root, "Movies", "Film (2020)", "Film (2020) [WEBDL-1080p].en.srt")
	if data, err := os.ReadFile(sidecar); err != nil || string(data) != payload {
		t.Fatalf("installed sidecar = %q, %v", data, err)
	}
	// A rescan confirms the satisfied state and clears the wanted queue.
	if _, err := fixture.service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = fixture.service.Library(ctx, LibraryFilter{})
	if err != nil || items[0].Missing != 0 {
		t.Fatalf("library after download = %+v, %v", items, err)
	}
	due, err := fixture.service.store.DueWanted(ctx, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("due wanted after download = %+v, %v", due, err)
	}
	history, err := fixture.service.History(ctx, KindMovie, "movie-1", 20)
	if err != nil || len(history) == 0 || history[0].Action != "downloaded" {
		t.Fatalf("history = %+v, %v", history, err)
	}
}

func TestServiceAutoSearchSkipsUnconfiguredProviders(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	if _, err := fixture.service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, err := fixture.service.store.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.service.configuredProviders(cfg)) != 0 {
		t.Fatal("the seeded provider must not count as configured without an API key")
	}
	fixture.service.schedule(ctx)
	jobs, err := fixture.service.Jobs(ctx, true, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
}

func TestServiceDownloadFailureRecordsQuotaWithoutSidecar(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/subtitles":
			w.Write([]byte(`{"data":[{"attributes":{"subtitle_id":"7","language":"en","format":"srt","files":[{"file_id":42,"file_name":"film.srt"}]}}]}`))
		case "/download":
			w.WriteHeader(http.StatusNotAcceptable)
			w.Write([]byte(`{"message":"You have downloaded your allowed 20 subtitles today"}`))
		}
	}))
	defer provider.Close()
	fixture.configureProviders(t, provider.URL)
	ctx := context.Background()
	if _, err := fixture.service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.service.Download(ctx, KindMovie, "movie-1", DownloadRequest{
		ProviderID: "opensubtitles", FileID: "42", Language: "en", FileName: "film.srt",
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "quota") {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "Movies", "Film (2020)", "Film (2020) [WEBDL-1080p].en.srt")); !os.IsNotExist(err) {
		t.Fatal("a failed download must not leave a sidecar")
	}
	wanted, err := fixture.service.store.DueWanted(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// The failed attempt is deferred instead of retried immediately.
	rows, err := fixture.service.store.Wanted(ctx)
	if err != nil || len(rows[videoKey(KindMovie, "movie-1")]) != 1 {
		t.Fatalf("wanted = %+v, %v", rows, err)
	}
	if rows[videoKey(KindMovie, "movie-1")][0].Attempts != 1 || !strings.Contains(rows[videoKey(KindMovie, "movie-1")][0].Error, "quota") {
		t.Fatalf("wanted row = %+v", rows[videoKey(KindMovie, "movie-1")][0])
	}
	_ = wanted
	statuses, err := fixture.service.Providers(ctx)
	if err != nil || len(statuses) != 1 || !strings.Contains(statuses[0].LastError, "quota") {
		t.Fatalf("provider status = %+v, %v", statuses, err)
	}
}

func TestServiceSyncOffsetPublishesWithBackup(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecar := filepath.Join(fixture.root, "Movies", "Film (2020)", "Film (2020) [WEBDL-1080p].en.srt")
	if err := os.WriteFile(sidecar, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.service.Sync(ctx, KindMovie, "movie-1", SyncRequest{
		Mode: "offset", Path: "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt", OffsetSeconds: -1,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "done" {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	data, err := os.ReadFile(sidecar)
	if err != nil || !strings.Contains(string(data), "00:00:00,000 --> 00:00:02,500") {
		t.Fatalf("synced sidecar = %q, %v", data, err)
	}
	backup, err := os.ReadFile(filepath.Join(fixture.root, ".recycle", "Movies", "Film (2020)", "Film (2020) [WEBDL-1080p].en.srt"))
	if err != nil || string(backup) != sampleSRT {
		t.Fatalf("recycled backup = %q, %v", backup, err)
	}
}

func TestServiceSyncAdversarialPaths(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	outside := filepath.Join(fixture.root, "..", "outside.en.srt")
	if err := os.WriteFile(outside, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside) })
	cases := []SyncRequest{
		{Mode: "offset", Path: "../../outside.en.srt", OffsetSeconds: 1},
		{Mode: "offset", Path: "/etc/passwd", OffsetSeconds: 1},
		{Mode: "offset", Path: "Movies/Film (2020)/../other.en.srt", OffsetSeconds: 1},
		{Mode: "reference", Path: "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt", ReferencePath: "../../outside.en.srt"},
	}
	for _, request := range cases {
		if _, err := fixture.service.Sync(ctx, KindMovie, "movie-1", request); err == nil {
			t.Fatalf("adversarial sync %+v must be rejected", request)
		}
	}
	if _, err := fixture.service.Download(ctx, KindMovie, "movie-1", DownloadRequest{
		ProviderID: "opensubtitles", FileID: "42", Language: "../../etc/en",
	}); err == nil {
		t.Fatal("adversarial language must be rejected")
	}
}

func TestServiceTranslateReviewThenApply(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecarRel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	sidecar := filepath.Join(fixture.root, filepath.FromSlash(sidecarRel))
	if err := os.WriteFile(sidecar, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	translator := &prefixTranslator{language: "de"}
	fixture.service.SetTranslator(translator)
	job, err := fixture.service.Translate(ctx, KindMovie, "movie-1", TranslateRequest{Path: sidecarRel, Language: "de"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "done" {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	outputs, err := fixture.service.store.Outputs(ctx, KindMovie, "movie-1")
	if err != nil || len(outputs) != 1 {
		t.Fatalf("outputs = %+v, %v", outputs, err)
	}
	if outputs[0].Language != "de" || !strings.Contains(outputs[0].Payload, "DE:") {
		t.Fatalf("output = %+v", outputs[0])
	}
	// The source sidecar stays byte-identical until the review is applied.
	if data, err := os.ReadFile(sidecar); err != nil || string(data) != sampleSRT {
		t.Fatalf("source sidecar changed before review: %q, %v", data, err)
	}
	saved, err := fixture.service.ApplyOutput(ctx, outputs[0].ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if saved.Path != "Movies/Film (2020)/Film (2020) [WEBDL-1080p].de.srt" {
		t.Fatalf("applied path = %q", saved.Path)
	}
	applied, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(saved.Path)))
	if err != nil || !strings.Contains(string(applied), "DE:") || strings.Contains(string(applied), "00:00:01,000 --> 00:00:03,500") == false {
		t.Fatalf("applied output = %q, %v", applied, err)
	}
	if _, err := fixture.service.ApplyOutput(ctx, outputs[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("double apply error = %v", err)
	}
	// Discarding removes the staged payload without touching the library.
	discardJob, err := fixture.service.Translate(ctx, KindMovie, "movie-1", TranslateRequest{Path: sidecarRel, Language: "de"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.execute(ctx, discardJob)
	outputs, err = fixture.service.store.Outputs(ctx, KindMovie, "movie-1")
	if err != nil || len(outputs) != 1 {
		t.Fatalf("outputs = %+v, %v", outputs, err)
	}
	if err := fixture.service.DiscardOutput(ctx, outputs[0].ID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := fixture.service.Output(ctx, outputs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discarded output lookup error = %v", err)
	}
}

func TestServiceTranslateFailsClosedWhenTranslatorRejects(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecarRel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	sidecar := filepath.Join(fixture.root, filepath.FromSlash(sidecarRel))
	if err := os.WriteFile(sidecar, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.service.SetTranslator(brokenTranslator{})
	job, err := fixture.service.Translate(ctx, KindMovie, "movie-1", TranslateRequest{Path: sidecarRel, Language: "de"})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "failed" {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	outputs, err := fixture.service.store.Outputs(ctx, KindMovie, "movie-1")
	if err != nil || len(outputs) != 0 {
		t.Fatalf("failed translation left outputs: %+v, %v", outputs, err)
	}
	if data, err := os.ReadFile(sidecar); err != nil || string(data) != sampleSRT {
		t.Fatalf("sidecar changed after failure: %q, %v", data, err)
	}
}

func TestServiceJobCancelPreventsExecution(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecarRel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	if err := os.WriteFile(filepath.Join(fixture.root, filepath.FromSlash(sidecarRel)), []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.service.Sync(ctx, KindMovie, "movie-1", SyncRequest{Mode: "offset", Path: sidecarRel, OffsetSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CancelJob(ctx, job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "cancelled" {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	if data, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(sidecarRel))); err != nil || string(data) != sampleSRT {
		t.Fatalf("cancelled job changed the sidecar: %q, %v", data, err)
	}
	// Cancelling a finished job is a conflict, not a silent success.
	if err := fixture.service.CancelJob(ctx, job.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel finished job error = %v", err)
	}
}

func TestServiceStartRecoversJobsAndRunsThem(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecarRel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	if err := os.WriteFile(filepath.Join(fixture.root, filepath.FromSlash(sidecarRel)), []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.service.Sync(ctx, KindMovie, "movie-1", SyncRequest{Mode: "offset", Path: sidecarRel, OffsetSeconds: 2})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := fixture.service.Job(ctx, job.ID)
		if err == nil && stored.Status == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "done" {
		t.Fatalf("job = %+v, %v", stored, err)
	}
}

// prefixTranslator prefixes every cue with its language so tests can assert on payloads.
type prefixTranslator struct{ language string }

func (p *prefixTranslator) Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error) {
	if err := ctx.Err(); err != nil {
		return TranslationReply{}, err
	}
	reply := TranslationReply{Language: p.language, Tokens: 5}
	for _, cue := range req.Cues {
		reply.Cues = append(reply.Cues, Cue{ID: cue.ID, Text: "DE:" + cue.Text})
	}
	return reply, nil
}

type brokenTranslator struct{}

func (brokenTranslator) Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error) {
	return TranslationReply{Language: "de", Cues: []Cue{{ID: "missing", Text: "x"}}}, nil
}

type unconfiguredTranslator struct{}

func (unconfiguredTranslator) Translate(ctx context.Context, req TranslationRequest) (TranslationReply, error) {
	return TranslationReply{}, ErrNotConfigured
}

func TestServiceTranslateReportsInjectedTranslatorUnconfigured(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	sidecarRel := "Movies/Film (2020)/Film (2020) [WEBDL-1080p].en.srt"
	if err := os.WriteFile(filepath.Join(fixture.root, filepath.FromSlash(sidecarRel)), []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	// An injected shared translator is used even though the legacy subtitle AI config is disabled.
	fixture.service.SetTranslator(unconfiguredTranslator{})
	job, err := fixture.service.Translate(ctx, KindMovie, "movie-1", TranslateRequest{Path: sidecarRel, Language: "de"})
	if err != nil {
		t.Fatalf("translate must queue with an injected translator: %v", err)
	}
	fixture.service.execute(ctx, job)
	stored, err := fixture.service.Job(ctx, job.ID)
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "Connections") {
		t.Fatalf("job = %+v, %v", stored, err)
	}
	if outputs, err := fixture.service.store.Outputs(ctx, KindMovie, "movie-1"); err != nil || len(outputs) != 0 {
		t.Fatalf("outputs = %+v, %v", outputs, err)
	}
}

func TestServiceConfigRedactsSecrets(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	cfg, err := fixture.service.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = []Provider{{
		ID: "opensubtitles", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
		Endpoint: "https://api.opensubtitles.com/api/v1", Username: "user", Password: "hunter2",
		APIKey: "top-secret", Enabled: true,
	}}
	cfg.AI = AIConfig{Enabled: true, BaseURL: "https://ai.example/v1", APIKey: "ai-secret", Model: "m"}
	if _, err := fixture.service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	view, err := fixture.service.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "top-secret") || strings.Contains(string(raw), "ai-secret") || strings.Contains(string(raw), "hunter2") {
		t.Fatalf("secrets leaked in the config view: %s", raw)
	}
	if !view.Providers[0].APIKeySet || !view.Providers[0].PasswordSet || !view.AI.APIKeySet {
		t.Fatalf("configured flags were not reported: %s", raw)
	}
	// A blank secret on save keeps the stored credential.
	next, err := fixture.service.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next.AI.Model = "m2"
	if _, err := fixture.service.SetConfig(ctx, next); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.service.store.Config(ctx)
	if err != nil || stored.Providers[0].APIKey != "top-secret" || stored.AI.APIKey != "ai-secret" {
		t.Fatalf("stored secrets = %+v, %v", stored, err)
	}
	if _, err := NewOpenAITranslator(AIConfig{Enabled: true, BaseURL: "https://ai.example/v1", Model: "m"}); err != nil {
		t.Fatalf("translator without a key must build: %v", err)
	}
}

func TestServiceProfilesAndAssignments(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	profile, err := fixture.service.SaveProfile(ctx, Profile{
		ID: "polish", Name: "Polish", Languages: []LanguagePreference{{Code: "pl"}, {Code: "en", Forced: true, HI: true}},
	})
	if err != nil {
		t.Fatalf("save profile: %v", err)
	}
	if profile.Cutoff != 2 {
		t.Fatalf("profile = %+v", profile)
	}
	assignment, err := fixture.service.SetAssignment(ctx, KindMovie, "movie-1", "polish", true)
	if err != nil || assignment.ProfileID != "polish" {
		t.Fatalf("assignment = %+v, %v", assignment, err)
	}
	if _, err := fixture.service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.Library(ctx, LibraryFilter{})
	// Base, forced, and hearing-impaired variants plus the plain Polish one are all wanted.
	if err != nil || len(items) != 1 || len(items[0].Wanted) != 4 || items[0].Missing != 4 {
		t.Fatalf("library = %+v, %v", items, err)
	}
	if err := fixture.service.DeleteProfile(ctx, "polish"); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting an assigned profile error = %v", err)
	}
	if err := fixture.service.ResetAssignment(ctx, KindMovie, "movie-1"); err != nil {
		t.Fatalf("reset assignment: %v", err)
	}
	if err := fixture.service.DeleteProfile(ctx, "polish"); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
}

// gatedCatalog lets tests observe and hold a scan inside the catalog read.
type gatedCatalog struct {
	release   chan struct{}
	entered   chan struct{}
	ignoreCtx bool
	sawCancel atomic.Bool
	once      sync.Once
}

func newGatedCatalog(ignoreCtx bool) *gatedCatalog {
	return &gatedCatalog{release: make(chan struct{}), entered: make(chan struct{}), ignoreCtx: ignoreCtx}
}

func (g *gatedCatalog) Videos(ctx context.Context) ([]Video, error) {
	g.once.Do(func() { close(g.entered) })
	if g.ignoreCtx {
		<-g.release
		return nil, nil
	}
	select {
	case <-g.release:
		return nil, nil
	case <-ctx.Done():
		g.sawCancel.Store(true)
		return nil, ctx.Err()
	}
}

func (g *gatedCatalog) Video(ctx context.Context, kind, id string) (Video, error) {
	return Video{}, ErrNotFound
}

func TestLegacyZeroStreamIndexConfigStillExtracts(t *testing.T) {
	fixture := newServiceFixture(t, nil)
	ctx := context.Background()
	// Reproduces a config row seeded before the default existed.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE subtitle_config SET data = jsonb_set(data, '{sync,maxEmbeddedStreamIndex}', '0'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	cfg, err := fixture.service.store.Config(ctx)
	if err != nil || cfg.Sync.MaxEmbeddedStreamIndex != 64 {
		t.Fatalf("legacy config bound = %d, %v", cfg.Sync.MaxEmbeddedStreamIndex, err)
	}
	job, err := fixture.service.Extract(ctx, KindMovie, "movie-1", ExtractRequest{StreamIndex: 4})
	if err != nil || job.Kind != "extract" {
		t.Fatalf("extract with a legacy zero bound: %+v, %v", job, err)
	}
	if _, err := fixture.service.Extract(ctx, KindMovie, "movie-1", ExtractRequest{StreamIndex: 65}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-range stream error = %v", err)
	}
}

func TestCloseDrainsTrackedScan(t *testing.T) {
	pool := testPool(t)
	migrateTestSchema(t, pool)
	catalog := newGatedCatalog(true)
	service, err := New(context.Background(), pool, Options{Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.launchScan(); err != nil {
		t.Fatalf("launch scan: %v", err)
	}
	select {
	case <-catalog.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan never reached the catalog")
	}
	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a tracked scan was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(catalog.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not drain the tracked scan")
	}
	if err := service.launchScan(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("post-close scan admission = %v", err)
	}
}

func TestCloseCancelsInFlightScan(t *testing.T) {
	pool := testPool(t)
	migrateTestSchema(t, pool)
	catalog := newGatedCatalog(false)
	service, err := New(context.Background(), pool, Options{Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(context.Background())
	if err := service.launchScan(); err != nil {
		t.Fatalf("launch scan: %v", err)
	}
	select {
	case <-catalog.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan never reached the catalog")
	}
	done := make(chan struct{})
	go func() {
		service.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel the in-flight scan")
	}
	if !catalog.sawCancel.Load() {
		t.Fatal("the scan context was not cancelled by Close")
	}
}

// buildSubtitleFixtureVideo writes a small MKV with plain eng, eng SDH, and forced kor subtitle tracks.
func buildSubtitleFixtureVideo(t *testing.T, target string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	cue := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("1\n00:00:00,500 --> 00:00:02,000\nLine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	command := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=128x72:d=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-i", cue("plain.srt"), "-i", cue("sdh.srt"), "-i", cue("forced.srt"),
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map", "3:s", "-map", "4:s",
		"-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng",
		"-metadata:s:s:1", "language=eng", "-metadata:s:s:1", "title=SDH",
		"-metadata:s:s:2", "language=kor", "-disposition:s:2", "+forced",
		"-shortest", target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot build the fixture: %v %s", err, output)
	}
}

// catalogFixture creates a real movie catalog entry whose file carries embedded subtitle variants.
func catalogFixture(t *testing.T, pool *pgxpool.Pool) (*movies.Service, movies.Movie, string) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("OMDB_API_KEY", "")
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieService, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	cfg, err := movieService.ConfigView(ctx)
	if err != nil || len(cfg.RootFolders) == 0 {
		t.Fatalf("movie roots = %+v, %v", cfg.RootFolders, err)
	}
	root := cfg.RootFolders[0]
	rel := "Fixture Movie (2020)/Fixture Movie (2020) [WEBDL-1080p].mkv"
	absolute := filepath.Join(root.Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	buildSubtitleFixtureVideo(t, absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		t.Fatal(err)
	}
	movie, err := movieService.Add(ctx, movies.AddInput{
		Metadata: metadata.Title{Title: "Fixture Movie", Year: 2020}, Monitored: true,
	})
	if err != nil {
		t.Fatalf("add movie: %v", err)
	}
	if _, err := movieService.Store.Patch(ctx, movie.ID, map[string]any{"files": []movies.File{{
		RootID: root.ID, Path: rel, Size: info.Size(), Quality: "WEBDL-1080p", ImportedAt: time.Now().UTC(),
	}}}); err != nil {
		t.Fatalf("attach movie file: %v", err)
	}
	return movieService, movie, root.Path
}

func TestCatalogExtractionSatisfiesWanted(t *testing.T) {
	pool := testPool(t)
	movieService, movie, root := catalogFixture(t, pool)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Movies: movieService})
	if err != nil {
		t.Fatalf("subtitles.New: %v", err)
	}
	t.Cleanup(service.Close)
	summary, err := service.Scan(ctx)
	if err != nil || summary.Videos != 1 || summary.Wanted != 1 {
		t.Fatalf("scan = %+v, %v", summary, err)
	}
	streams, err := service.Streams(ctx, KindMovie, movie.ID)
	if err != nil {
		t.Fatalf("streams: %v", err)
	}
	sdhIndex, forcedIndex := -1, -1
	for _, stream := range streams {
		if stream.Type != "subtitle" {
			continue
		}
		if stream.Language == "eng" && stream.HI && !stream.Forced {
			sdhIndex = stream.Index
		}
		if stream.Language == "kor" && stream.Forced {
			forcedIndex = stream.Index
		}
	}
	if sdhIndex < 0 || forcedIndex < 0 {
		t.Fatalf("fixture streams = %+v", streams)
	}

	// Preview keeps the stream variant through review and apply.
	job, err := service.Extract(ctx, KindMovie, movie.ID, ExtractRequest{StreamIndex: sdhIndex, Preview: true})
	if err != nil {
		t.Fatalf("extract queue: %v", err)
	}
	service.execute(ctx, job)
	stored, err := service.Job(ctx, job.ID)
	if err != nil || stored.Status != "done" {
		t.Fatalf("extract job = %+v, %v", stored, err)
	}
	outputs, err := service.store.Outputs(ctx, KindMovie, movie.ID)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("outputs = %+v, %v", outputs, err)
	}
	if outputs[0].Language != "en" || !outputs[0].HI || outputs[0].Forced {
		t.Fatalf("staged output lost the stream variant: %+v", outputs[0])
	}
	sidecar, err := service.ApplyOutput(ctx, outputs[0].ID)
	if err != nil || !strings.HasSuffix(sidecar.Path, ".en.hi.srt") {
		t.Fatalf("applied sidecar = %+v, %v", sidecar, err)
	}

	// The extracted English subtitle satisfies the wanted language.
	if _, err := service.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := service.Library(ctx, LibraryFilter{})
	if err != nil || len(items) != 1 || items[0].Missing != 0 {
		t.Fatalf("library after extraction = %+v, %v", items, err)
	}
	if items[0].Video.Title != "Fixture Movie" {
		t.Fatalf("movie title = %q", items[0].Video.Title)
	}

	// A forced stream publishes directly with its canonical language and flag.
	direct, err := service.Extract(ctx, KindMovie, movie.ID, ExtractRequest{StreamIndex: forcedIndex})
	if err != nil {
		t.Fatalf("forced extract queue: %v", err)
	}
	service.execute(ctx, direct)
	if job, err := service.Job(ctx, direct.ID); err != nil || job.Status != "done" {
		t.Fatalf("forced extract job = %+v, %v", job, err)
	}
	forcedPath := filepath.Join(root, "Fixture Movie (2020)", "Fixture Movie (2020) [WEBDL-1080p].ko.forced.srt")
	if _, err := os.Stat(forcedPath); err != nil {
		t.Fatalf("forced sidecar missing: %v", err)
	}
}
