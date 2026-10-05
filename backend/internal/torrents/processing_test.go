package torrents

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/IvanPopov200/Constellarr/backend/internal/media"
)

func TestProcessingDecision(t *testing.T) {
	cases := []struct {
		name  string
		files []File
		state string
		input string
	}{
		{"loose video", []File{{Name: "Movie/movie.mkv", Size: 10}}, processSkipped, ""},
		{"loose audio", []File{{Name: "album/track.flac", Size: 10}}, processSkipped, ""},
		{"rar at root", []File{{Name: "release.rar", Size: 10}, {Name: "release.r00", Size: 10}}, processPending, ""},
		{"rar in folder", []File{{Name: "Release/movie.part01.rar", Size: 10}, {Name: "Release/movie.part02.rar", Size: 10}}, processPending, "Release"},
		{"zip beside subtitles", []File{{Name: "Release/pack.zip", Size: 10}, {Name: "Release/notes.srt", Size: 4}}, processPending, "Release"},
		{"unsupported archive", []File{{Name: "release.7z", Size: 10}}, processFailed, ""},
		{"flat files only", []File{{Name: "readme.nfo", Size: 10}}, processSkipped, ""},
	}
	for _, test := range cases {
		row := processingDecision(test.files)
		if row.state != test.state || row.input != test.input {
			t.Errorf("%s: got %s %q, want %s %q", test.name, row.state, row.input, test.state, test.input)
		}
	}
	if row := processingDecision([]File{{Name: "Release/movie.mkv", Size: 10}, {Name: "Release/pack.zip", Size: 10}}); row.state != processSkipped {
		t.Errorf("loose media must win over archives, got %s", row.state)
	}
}

func TestSanitizeProcessError(t *testing.T) {
	message := sanitizeProcessError(
		errors.New("open /data/torrents/abc/extract/movie.mkv: permission denied"), "/data/torrents/abc", "/data/torrents/abc/extract")
	if message != "open .../movie.mkv: permission denied" {
		t.Fatalf("absolute paths must be scrubbed, got %q", message)
	}
	if sanitizeProcessError(errors.New("")) == "" {
		t.Fatal("an empty extraction error must still be reported")
	}
}

// stubMediaProcess replaces the extractor for deterministic state-machine tests.
func stubMediaProcess(t *testing.T, run func(ctx context.Context, inputDir, outDir string) ([]media.File, error)) {
	t.Helper()
	previous := processMedia
	processMedia = func(ctx context.Context, inputDir, outDir string, _ int, _ func(string)) ([]media.File, error) {
		return run(ctx, inputDir, outDir)
	}
	t.Cleanup(func() { processMedia = previous })
}

// processingTorrent builds a multi-file torrent whose payload is the given files.
func processingTorrent(t *testing.T, files map[string]string) ([]byte, int, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	meta := metainfo.MetaInfo{InfoBytes: raw}
	if err := meta.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), info.NumPieces(), meta.HashInfoBytes().HexString()
}

func TestProcessingStatesAndRetry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{MaxActiveJobs: 2, DHTEnabled: true, PEXEnabled: true, SeedRatioLimit: 0}); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var mu sync.Mutex
	attempts := map[string]int{}
	stubMediaProcess(t, func(ctx context.Context, inputDir, outDir string) ([]media.File, error) {
		mu.Lock()
		attempts[inputDir]++
		attempt := attempts[inputDir]
		mu.Unlock()
		_, broken := os.Stat(filepath.Join(inputDir, "broken.rar"))
		if broken == nil && attempt == 1 {
			return nil, errors.New("archive is encrypted; encrypted downloads are not supported")
		}
		if broken != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := os.WriteFile(filepath.Join(outDir, "extracted.mkv"), []byte("extracted-bytes"), 0o600); err != nil {
			return nil, err
		}
		return []media.File{{Name: "extracted.mkv", Size: 15}}, nil
	})

	first, pieces, hash := processingTorrent(t, map[string]string{"release.rar": "archive-bytes"})
	job, err := service.Add(ctx, AddInput{Torrent: first, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	verifiedPayload(t, service, job.ID, hash, pieces, map[string]string{"release.rar": "archive-bytes"})

	second, pieces, hash := processingTorrent(t, map[string]string{"broken.rar": "archive-bytes"})
	broken, err := service.Add(ctx, AddInput{Torrent: second, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	verifiedPayload(t, service, broken.ID, hash, pieces, map[string]string{"broken.rar": "archive-bytes"})
	if _, err := service.UpdateLimits(ctx, broken.ID, 1, 0); err != nil {
		t.Fatal(err)
	}

	service.Start(ctx)
	t.Cleanup(service.Close)

	waitUntil(t, 30*time.Second, "the extraction to start", func() bool {
		current := loadJob(t, service, job.ID)
		return current.Processing != nil && current.Processing.State == processRunning
	})
	bridge, err := service.DownloadJob(ctx, job.ID)
	if err != nil || bridge.Status != "extracting" {
		t.Fatalf("extraction must not report completed: %+v (%v)", bridge, err)
	}
	output, err := service.OutputDirectory(job.ID)
	if err != nil || filepath.Base(output) != job.InfoHash {
		t.Fatalf("unprocessed output directory = %q, %v", output, err)
	}
	close(release)

	waitUntil(t, 30*time.Second, "the extraction to finish", func() bool {
		current := loadJob(t, service, job.ID)
		return current.Processing != nil && current.Processing.State == processCompleted
	})
	bridge, err = service.DownloadJob(ctx, job.ID)
	if err != nil || bridge.Status != "completed" || len(bridge.Files) != 1 || bridge.Files[0].Name != "extracted.mkv" {
		t.Fatalf("bridge after extraction = %+v (%v)", bridge, err)
	}
	if output, err = service.OutputDirectory(job.ID); err != nil || filepath.Base(filepath.Dir(output)) != processedDirName {
		t.Fatalf("processed output directory = %q, %v", output, err)
	}
	handle, err := service.OpenFile(ctx, job.ID, "extracted.mkv")
	if err != nil {
		t.Fatalf("extracted file is not downloadable: %v", err)
	}
	content, readErr := io.ReadAll(handle)
	handle.Close()
	if readErr != nil || string(content) != "extracted-bytes" {
		t.Fatalf("extracted file content = %q (%v)", content, readErr)
	}
	if payload, err := service.OpenFile(ctx, job.ID, "release.rar"); err != nil {
		t.Fatalf("payload files stay downloadable while seeding: %v", err)
	} else {
		payload.Close()
	}
	if _, err := service.OpenFile(ctx, job.ID, "undeclared.mkv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undeclared files must be refused: %v", err)
	}

	// A failed extraction is retried through Resume, which the parent adapter maps to Retry.
	waitUntil(t, 30*time.Second, "the failed extraction", func() bool {
		current := loadJob(t, service, broken.ID)
		return current.Processing != nil && current.Processing.State == processFailed
	})
	failed, err := service.DownloadJob(ctx, broken.ID)
	if err != nil || failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("failed extraction must surface the reason: %+v (%v)", failed, err)
	}
	// The job is still seeding, so resume must accept the extraction retry.
	resumed, err := service.Resume(ctx, broken.ID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if resumed.Processing == nil || resumed.Processing.State != processPending {
		t.Fatalf("resume did not queue the retry: %+v", resumed.Processing)
	}
	waitUntil(t, 30*time.Second, "the retried extraction", func() bool {
		current := loadJob(t, service, broken.ID)
		return current.Processing != nil && current.Processing.State == processCompleted
	})
}

func TestProcessingResumesAfterRestart(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	directory := t.TempDir()
	service, err := New(ctx, pool, Options{Directory: directory, Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{MaxActiveJobs: 2, DHTEnabled: true, PEXEnabled: true, SeedRatioLimit: 0}); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	stubMediaProcess(t, func(ctx context.Context, inputDir, outDir string) ([]media.File, error) {
		select {
		case <-release:
			return []media.File{{Name: "movie.mkv", Size: 3}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	torrentBytes, pieces, hash := processingTorrent(t, map[string]string{"release.zip": "archive-bytes"})
	job, err := service.Add(ctx, AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	verifiedPayload(t, service, job.ID, hash, pieces, map[string]string{"release.zip": "archive-bytes"})
	service.Start(ctx)
	waitUntil(t, 30*time.Second, "extraction to start", func() bool {
		current := loadJob(t, service, job.ID)
		return current.Processing != nil && current.Processing.State == processRunning
	})
	service.Close()

	restarted, err := New(ctx, pool, Options{Directory: directory, Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	restarted.Start(ctx)
	t.Cleanup(restarted.Close)
	close(release)
	waitUntil(t, 30*time.Second, "the restarted service to finish extraction", func() bool {
		current := loadJob(t, restarted, job.ID)
		return current.Processing != nil && current.Processing.State == processCompleted
	})
	if health := restarted.Health(ctx); !health.OK {
		t.Fatalf("health after restart: %+v", health)
	}
}
