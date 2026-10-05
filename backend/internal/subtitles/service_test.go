package subtitles

import (
	"context"
	"errors"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

func TestDefaultConfigBoundsEmbeddedStreams(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Sync.MaxEmbeddedStreamIndex != 64 {
		t.Fatalf("default max embedded stream index = %d", cfg.Sync.MaxEmbeddedStreamIndex)
	}
	if cfg.Sync.TimeoutSeconds == 0 || cfg.AI.MaxTokens == 0 || cfg.AI.MaxCharacters == 0 {
		t.Fatalf("default config has zero budgets: %+v", cfg)
	}
	legacy := applyConfigDefaults(Config{})
	if legacy.Sync.MaxEmbeddedStreamIndex != 64 || legacy.ScanMinutes == 0 || legacy.DefaultProfileID == "" || len(legacy.Providers) == 0 {
		t.Fatalf("legacy zero config was not filled: %+v", legacy)
	}
}

func TestEpisodeLabelOmitsSeriesTitle(t *testing.T) {
	label := episodeLabel(tv.Episode{Season: 1, Number: 4, Title: "Magic Xylophone"})
	if label != "S01E04 · Magic Xylophone" {
		t.Fatalf("episode label = %q", label)
	}
	if label := episodeLabel(tv.Episode{Season: 2, Number: 10}); label != "S02E10" {
		t.Fatalf("untitled episode label = %q", label)
	}
}

func TestScanAdmissionRefusesAfterClose(t *testing.T) {
	service := &Service{jobs: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1)}
	service.Close()
	if err := service.launchScan(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("scan admission after close = %v", err)
	}
	if service.scanRunning.Load() {
		t.Fatal("a refused scan must not keep the scan slot")
	}
}
