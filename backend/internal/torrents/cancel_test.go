package torrents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/media"
)

// TestCancelIntentPersistsWhenItRacesTheAttach covers a cancel recorded before its status write.
func TestCancelIntentPersistsWhenItRacesTheAttach(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		MaxActiveJobs: 2, DHTEnabled: true, PEXEnabled: true, SeedRatioLimit: 0,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := service.Add(ctx, AddInput{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("2c", 20), Source: "magnet"})
	if err != nil {
		t.Fatal(err)
	}
	// Cancel records the intent before its own status write, so the engine can attach first.
	service.markCancelIntent(job.ID, true)
	service.Start(ctx)
	waitUntil(t, 30*time.Second, "the raced cancel to persist", func() bool {
		return loadJob(t, service, job.ID).Status == statusCancelled
	})
	time.Sleep(2 * time.Second)
	if current := loadJob(t, service, job.ID); current.Status != statusCancelled {
		t.Fatalf("job revived after a raced cancel: %s %q", current.Status, current.Error)
	}
}

func TestCancelStopsRunningExtraction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		MaxActiveJobs: 2, DHTEnabled: true, PEXEnabled: true, SeedRatioLimit: 0,
	}); err != nil {
		t.Fatal(err)
	}
	// Register the service close after the stub so cleanup stops the engine before restoring processMedia.
	stubMediaProcess(t, func(ctx context.Context, inputDir, outDir string) ([]media.File, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	t.Cleanup(service.Close)
	torrentBytes, pieces, hash := processingTorrent(t, map[string]string{"release.rar": "archive-bytes"})
	job, err := service.Add(ctx, AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	verifiedPayload(t, service, job.ID, hash, pieces, map[string]string{"release.rar": "archive-bytes"})
	service.Start(ctx)

	waitUntil(t, 30*time.Second, "the extraction to start", func() bool {
		current := loadJob(t, service, job.ID)
		return current.Processing != nil && current.Processing.State == processRunning
	})
	if _, err := service.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("cannot cancel: %v", err)
	}
	waitUntil(t, 30*time.Second, "the extraction to stop", func() bool {
		current := loadJob(t, service, job.ID)
		return current.Status == statusCancelled && current.Processing != nil && current.Processing.State != processRunning
	})
	// The cancelled extraction must not be rescheduled by the engine.
	time.Sleep(2 * time.Second)
	current := loadJob(t, service, job.ID)
	if current.Status != statusCancelled || current.Processing.State == processRunning {
		t.Fatalf("cancelled extraction was rescheduled: %s %+v", current.Status, current.Processing)
	}

	// An explicit resume requeues the job and its pending extraction.
	resumed, err := service.Resume(ctx, job.ID)
	if err != nil || resumed.Status != statusQueued {
		t.Fatalf("resume after cancel = %+v (%v)", resumed, err)
	}
	waitUntil(t, 30*time.Second, "the extraction to restart after resume", func() bool {
		extraction := loadJob(t, service, job.ID)
		return extraction.Processing != nil && extraction.Processing.State == processRunning
	})
}
