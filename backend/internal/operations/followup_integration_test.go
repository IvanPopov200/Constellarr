package operations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadlineWriter records the deadlines long operations request through http.ResponseController.
type deadlineWriter struct {
	httptest.ResponseRecorder
	writeDeadline time.Time
	readDeadline  time.Time
}

func (d *deadlineWriter) SetWriteDeadline(deadline time.Time) error {
	d.writeDeadline = deadline
	return nil
}

func (d *deadlineWriter) SetReadDeadline(deadline time.Time) error {
	d.readDeadline = deadline
	return nil
}

func TestConcurrentRestoresAreRejected(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	service := newTestService(t, pool, Options{
		DataDir: t.TempDir(), DatabaseURL: databaseURL, AllowLiveRestore: true,
		Quiesce: func(quiesceCtx context.Context) error {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return nil
			case <-quiesceCtx.Done():
				return quiesceCtx.Err()
			}
		},
		Resume: func(context.Context) error { return nil },
	})
	requireBackupTools(t, service)
	backup, err := service.CreateBackup(ctx, "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	first := make(chan error, 1)
	go func() {
		_, err := service.Restore(ctx, backup.ID, backup.ID)
		first <- err
	}()
	waitFor(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})

	begin := time.Now()
	if _, err := service.Restore(ctx, backup.ID, backup.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second restore must be refused while the first runs, got %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Fatalf("the second restore waited behind the first one for %v", elapsed)
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first restore: %v", err)
	}
	if service.maintenance.Load() {
		t.Fatal("the maintenance flag was not released after the restore finished")
	}
	if _, err := service.Restore(ctx, backup.ID, backup.ID); err != nil {
		t.Fatalf("a restore after the first finished must be admitted, got %v", err)
	}
}

func TestCanceledRestoreRequestReleasesAdmission(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	service := newTestService(t, pool, Options{
		DataDir: t.TempDir(), DatabaseURL: databaseURL, AllowLiveRestore: true,
		Quiesce: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Resume: func(context.Context) error { return nil },
	})
	requireBackupTools(t, service)
	backup, err := service.CreateBackup(context.Background(), "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Restore(canceled, backup.ID, backup.ID); err == nil {
		t.Fatal("a canceled restore request must not run")
	}
	waitFor(t, func() bool { return !service.maintenance.Load() })
	// With Quiesce gone the next attempt must reach the validation path again instead of reporting a running restore.
	service.options.Quiesce = nil
	if _, err := service.Restore(context.Background(), backup.ID, backup.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected the live-restore guard, got %v", err)
	}
}

func TestLongOperationHandlersExtendDeadlines(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	service := newTestService(t, pool, Options{
		DataDir: t.TempDir(), DatabaseURL: databaseURL,
		BackupTimeout: 20 * time.Minute, RestoreTimeout: 25 * time.Minute,
	})
	requireBackupTools(t, service)
	backup, err := service.CreateBackup(context.Background(), "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	mux := http.NewServeMux()
	service.Register(mux)

	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		minimum time.Duration
	}{
		{"create", http.MethodPost, "/api/v1/operations/backups", `{}`, 15 * time.Minute},
		{"import", http.MethodPost, "/api/v1/operations/backups/import", `{"data":""}`, 15 * time.Minute},
		{"restore preview", http.MethodPost, "/api/v1/operations/backups/" + backup.ID + "/restore", `{"preview":true}`, 15 * time.Minute},
		{"restore", http.MethodPost, "/api/v1/operations/backups/" + backup.ID + "/restore", `{"confirm":"` + backup.ID + `"}`, 20 * time.Minute},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			writer := &deadlineWriter{}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			mux.ServeHTTP(writer, request)
			if writer.writeDeadline.Before(time.Now().Add(test.minimum)) {
				t.Fatalf("write deadline %v does not cover the operation", writer.writeDeadline)
			}
			if writer.readDeadline.Before(time.Now().Add(test.minimum)) {
				t.Fatalf("read deadline %v does not cover the operation", writer.readDeadline)
			}
		})
	}
}

func TestExtendedDeadlineSurvivesServerWriteTimeout(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		service.extendDeadlines(w, r, time.Minute)
		time.Sleep(700 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
	})
	server := httptest.NewUnstartedServer(service.WrapHTTP(mux))
	server.Config.WriteTimeout = 300 * time.Millisecond
	server.Start()
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/slow")
	if err != nil {
		t.Fatalf("the extended write deadline was cut off: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "completed") {
		t.Fatalf("unexpected response after the extended deadline: %d %q (%v)", response.StatusCode, body, err)
	}
}

func TestTorrentAndMusicStateAvoidDoubleCounting(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO downloads (id, release_id, title, nzb, protocol, status, bytes_done, bytes_total)
		VALUES ('usenet-1', 'release-1', 'Usenet job', '\x00'::bytea, 'usenet', 'downloading', 1024, 4096),
		       ('torrent-shadow', 'release-2', 'Torrent job', '\x00'::bytea, 'torrent', 'downloading', 500, 1000);
		INSERT INTO torrent_jobs (id, info_hash, title, status, bytes_done, bytes_total)
		VALUES ('torrent-1', 'hash-1', 'Torrent job', 'downloading', 500, 1000),
		       ('torrent-2', 'hash-2', 'Seeding job', 'seeding', 2000, 2000);
		INSERT INTO music_artists (id, name, data) VALUES ('artist-1', 'Artist', '{}');
		INSERT INTO music_albums (id, artist_id, title, data) VALUES ('album-1', 'artist-1', 'Album', '{}');
		INSERT INTO music_tracks (id, album_id, disc, number, title, data) VALUES ('track-1', 'album-1', 1, 1, 'Track', '{}');`); err != nil {
		t.Fatalf("seed domain tables: %v", err)
	}
	service.collect(ctx)

	mux := http.NewServeMux()
	service.Register(mux)
	scrape := httptest.NewRecorder()
	mux.ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()
	for _, expected := range []string{
		`constellarr_downloads{state="downloading"} 1`,
		`constellarr_download_bytes_done{state="downloading"} 1024`,
		`constellarr_torrents{state="downloading"} 1`,
		`constellarr_torrents{state="seeding"} 1`,
		"constellarr_torrents_seeding 1",
		"constellarr_torrents_active 1",
		`constellarr_library_items{kind="artists"} 1`,
		`constellarr_library_items{kind="albums"} 1`,
		`constellarr_library_items{kind="tracks"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics output is missing %q", expected)
		}
	}

	status := httptest.NewRecorder()
	mux.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil))
	var view statusView
	if err := json.Unmarshal(status.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if view.Downloads.Active != 1 || view.Downloads.BytesDone != 1024 {
		t.Fatalf("torrent shadow rows leaked into the download view: %+v", view.Downloads)
	}
	if view.Torrents.Active != 1 || view.Torrents.Seeding != 1 || view.Torrents.BytesDone != 2500 {
		t.Fatalf("unexpected torrent view: %+v", view.Torrents)
	}
	if view.Library.Artists != 1 || view.Library.Albums != 1 || view.Library.Tracks != 1 {
		t.Fatalf("music libraries are missing from the status view: %+v", view.Library)
	}
}

func TestImportFailuresBecomeEventsOnce(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO movies (id, data) VALUES ('movie-1', '{}');
		INSERT INTO tv_series (id, data) VALUES ('series-1', '{}');
		INSERT INTO movie_history (movie_id, type, message) VALUES ('movie-1', 'import-failed', 'Import failed: bad container');
		INSERT INTO movie_history (movie_id, type, message) VALUES ('movie-1', 'grabbed', 'Grabbed a release');
		INSERT INTO tv_history (series_id, type, message) VALUES ('series-1', 'import-failed', 'Import failed: missing episode');
		INSERT INTO music_history (album_id, artist_id, type, message) VALUES ('album-1', 'artist-1', 'error', 'Import failed: bad tags');
		INSERT INTO music_history (album_id, artist_id, type, message) VALUES ('album-1', 'artist-1', 'error', 'Album refresh failed: timeout');`); err != nil {
		t.Fatalf("seed history tables: %v", err)
	}
	service.collect(ctx)
	service.collect(ctx)
	events, err := service.listEvents(ctx, eventFilter{kinds: []string{KindImportError}})
	if err != nil || len(events) != 3 {
		t.Fatalf("expected three import failure events, got %+v (%v)", events, err)
	}
	sources := map[string]string{}
	for _, event := range events {
		sources[event.Source] = event.Ref
		if !strings.Contains(event.Ref, ":") {
			t.Fatalf("import failure references must be namespaced: %+v", event)
		}
	}
	for _, source := range []string{"movies", "tv", "music"} {
		if !strings.HasPrefix(sources[source], source+":") {
			t.Fatalf("missing %s import failure event: %+v", source, events)
		}
	}
	counts, err := service.countEventsByKind(ctx)
	if err != nil || counts[KindImportError] != 3 {
		t.Fatalf("unexpected import error counter: %+v (%v)", counts, err)
	}
}
