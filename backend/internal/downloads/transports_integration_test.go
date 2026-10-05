package downloads_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

type torrentFixture struct {
	job       downloads.Job
	directory string
}

func (f *torrentFixture) Search(context.Context, string) ([]indexer.Release, error) {
	return []indexer.Release{{ID: f.job.ReleaseID, Title: f.job.Title, Protocol: "torrent"}}, nil
}
func (f *torrentFixture) Add(context.Context, string, string) (downloads.Job, error) {
	return f.job, nil
}
func (f *torrentFixture) Get(context.Context, string) (downloads.Job, error) { return f.job, nil }
func (f *torrentFixture) Retry(context.Context, string) (downloads.Job, error) {
	f.job.Status = "downloading"
	return f.job, nil
}
func (f *torrentFixture) OutputDirectory(string) (string, error) { return f.directory, nil }

func TestTorrentAcquisitionPersistsAndKeepsSeedingFiles(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	cfg := downloads.Config{Directory: t.TempDir()}
	manager, err := downloads.New(ctx, pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &torrentFixture{
		job:       downloads.Job{ID: "torrent-job", ReleaseID: "torrent_source_release", Title: "Sintel.2010.1080p", Status: "downloading", Files: []downloads.OutputFile{}},
		directory: t.TempDir(),
	}
	manager.SetTorrents(fixture)
	results, err := manager.SearchMovie(ctx, "tt1727587", "Sintel", 2010)
	if err != nil || len(results) != 1 || results[0].Protocol != "torrent" {
		t.Fatalf("torrent-only search = %v, %v", results, err)
	}
	job, err := manager.Add(ctx, fixture.job.ReleaseID, fixture.job.Title)
	if err != nil || job.Protocol != "torrent" {
		t.Fatalf("Add = %v, %v", job, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO downloads(id,release_id,title,nzb) VALUES ('usenet-job','usenet-release','Broken NZB',''::bytea)`); err != nil {
		t.Fatal(err)
	}
	manager.Start(ctx)
	defer manager.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE id='usenet-job'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Usenet queue did not process its own job")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE id=$1`, job.ID).Scan(&status); err != nil || status != "downloading" {
		t.Fatalf("Usenet worker touched torrent: %s, %v", status, err)
	}
	list, err := manager.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "usenet-job" {
		t.Fatalf("Usenet queue includes torrent: %v, %v", list, err)
	}
	fixture.job.Status = "completed"
	fixture.job.Files = []downloads.OutputFile{{Name: "Sintel.mkv", Size: 5}}
	if err := os.WriteFile(filepath.Join(fixture.directory, "Sintel.mkv"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err = manager.Get(ctx, job.ID)
	if err != nil || job.Status != "completed" || len(job.Files) != 1 {
		t.Fatalf("Get completed = %v, %v", job, err)
	}
	mode, err := manager.ImportMode(ctx, job.ID, "move")
	if err != nil || mode != "hardlink" {
		t.Fatalf("unsafe torrent import mode: %s, %v", mode, err)
	}
	file, err := manager.OpenFile(ctx, job.ID, "Sintel.mkv")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	manager.Close()
	restarted, err := downloads.New(ctx, pool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Get(ctx, job.ID); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("missing transport accepted: %v", err)
	}
	restarted.SetTorrents(fixture)
	if got, err := restarted.Get(ctx, job.ID); err != nil || got.Status != "completed" || got.Protocol != "torrent" {
		t.Fatalf("restart lost acquisition: %v, %v", got, err)
	}
}
