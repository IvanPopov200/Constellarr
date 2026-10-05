package subtitles

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in coverage: set TEST_FFSUBSYNC_PATH and optionally TEST_MEDIA_VIDEO (read-only) to catch CLI and logger drift.

func realHelperPath(t *testing.T) string {
	t.Helper()
	helper := strings.TrimSpace(os.Getenv("TEST_FFSUBSYNC_PATH"))
	if helper == "" {
		t.Skip("set TEST_FFSUBSYNC_PATH to a real ffsubsync install to run this test")
	}
	info, err := os.Stat(helper)
	if err != nil || info.IsDir() {
		t.Fatalf("TEST_FFSUBSYNC_PATH %q is not an executable file", helper)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required for real-helper tests")
	}
	return helper
}

func realHelperOptions(helper, input, reference string) SyncOptions {
	return SyncOptions{
		Mode: "reference", Input: input, Reference: reference,
		MaxOffset: 60, MinScore: 0, QualityMaxOff: 30, MaxFramerateDev: 0.1,
		AudioStream: -1, HelperPath: helper, TimeoutSeconds: 120,
	}
}

// writeDriftingSRT builds a reference subtitle and the same cues shifted by offsetMs.
func writeDriftingSRT(t *testing.T, dir string, offsetMs int) (string, string) {
	t.Helper()
	var source strings.Builder
	for index := 0; index < 40; index++ {
		start := time.Duration(index)*5*time.Second + 1200*time.Millisecond
		end := start + 1800*time.Millisecond
		fmt.Fprintf(&source, "%d\n%s --> %s\nLine %d\n\n", index+1,
			formatSRTTime(start), formatSRTTime(end), index+1)
	}
	reference := filepath.Join(dir, "reference.srt")
	if err := os.WriteFile(reference, []byte(source.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	document, err := ParseDocument([]byte(source.String()))
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := document.Shift(time.Duration(offsetMs) * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "shifted.srt")
	if err := os.WriteFile(input, shifted, 0o644); err != nil {
		t.Fatal(err)
	}
	return reference, input
}

func formatSRTTime(value time.Duration) string {
	ms := value.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

func TestRealHelperReferenceSyncAndLowQuality(t *testing.T) {
	helper := realHelperPath(t)
	dir := t.TempDir()
	reference, input := writeDriftingSRT(t, dir, 5000)
	result, data, err := RunSync(context.Background(), realHelperOptions(helper, input, reference))
	if err != nil {
		t.Fatalf("real reference sync: %v", err)
	}
	if math.Abs(result.Offset+5) > 0.2 {
		t.Fatalf("offset = %.3f, want about -5.000", result.Offset)
	}
	if !result.Changed {
		t.Fatalf("real helper did not change the subtitle: %+v", result)
	}
	document, err := ParseDocument(data)
	if err != nil {
		t.Fatalf("real helper output is not a valid subtitle: %v", err)
	}
	if cues := document.Cues(); len(cues) != 40 || cues[0].Start != 1200*time.Millisecond {
		t.Fatalf("synced cues = %d first %s", len(cues), cues[0].Start)
	}

	// An impossible score floor must surface the wrapped low-quality warning and publish nothing.
	rejected := realHelperOptions(helper, input, reference)
	rejected.MinScore = 1 << 30
	if _, payload, err := RunSync(context.Background(), rejected); !errors.Is(err, ErrLowQualitySync) || payload != nil {
		t.Fatalf("low quality result = %v %q", err, payload)
	}
}

func TestRealHelperEmbeddedStreamAndAudioSync(t *testing.T) {
	helper := realHelperPath(t)
	video := strings.TrimSpace(os.Getenv("TEST_MEDIA_VIDEO"))
	if video == "" {
		t.Skip("set TEST_MEDIA_VIDEO to a video with an embedded subtitle stream")
	}
	if info, err := os.Stat(video); err != nil || info.IsDir() {
		t.Fatalf("TEST_MEDIA_VIDEO %q is not a file", video)
	}
	ctx := context.Background()
	streams, err := ProbeStreams(ctx, "", video)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	subtitleIndex, audioIndex, audioLanguage := -1, -1, ""
	for _, stream := range streams {
		if stream.Type == "subtitle" && (stream.Language == "en" || subtitleIndex < 0) {
			subtitleIndex = stream.Index
		}
		if stream.Type == "audio" && (stream.Language == "eng" || audioIndex < 0) {
			audioIndex, audioLanguage = stream.Index, stream.Language
		}
	}
	if subtitleIndex < 0 || audioIndex < 0 {
		t.Fatalf("video has no usable streams: %+v", streams)
	}

	dir := t.TempDir()
	reference := filepath.Join(dir, "embedded.srt")
	if err := ExtractEmbeddedSubtitle(ctx, "", video, subtitleIndex, reference); err != nil {
		t.Fatalf("extract embedded subtitle: %v", err)
	}
	document, err := ParseDocument(mustRead(t, reference))
	if err != nil {
		t.Fatalf("extracted subtitle: %v", err)
	}
	if len(document.Cues()) < 2 {
		t.Fatalf("extracted %d cues", len(document.Cues()))
	}
	shifted, err := document.Shift(7 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "shifted.srt")
	if err := os.WriteFile(input, shifted, 0o644); err != nil {
		t.Fatal(err)
	}

	referenceSync := realHelperOptions(helper, input, reference)
	referenceResult, referenceData, err := RunSync(ctx, referenceSync)
	if err != nil {
		t.Fatalf("real video reference sync: %v", err)
	}
	if math.Abs(referenceResult.Offset+7) > 0.3 {
		t.Fatalf("reference offset = %.3f, want about -7.000", referenceResult.Offset)
	}
	synced, err := ParseDocument(referenceData)
	if err != nil || len(synced.Cues()) != len(document.Cues()) {
		t.Fatalf("reference sync output = %v cues %d", err, len(synced.Cues()))
	}

	// Default ffsubsync 0.5.x VAD prefers embedded subtitles, so this stays fast and deterministic.
	audioSync := realHelperOptions(helper, input, video)
	audioSync.Mode = "audio"
	audioResult, _, err := RunSync(ctx, audioSync)
	if err != nil {
		t.Fatalf("real audio sync: %v", err)
	}
	if math.Abs(audioResult.Offset+7) > 0.6 {
		t.Fatalf("audio offset = %.3f, want about -7.000", audioResult.Offset)
	}

	// Force the actual WebRTC voice activity detector over the video's audio track.
	vadSync := realHelperOptions(helper, input, video)
	vadSync.Mode = "audio"
	vadSync.VAD = "webrtc"
	if ffmpeg, err := exec.LookPath("ffmpeg"); err == nil {
		// A real helper must accept the directory form of --ffmpeg-path.
		vadSync.FFmpegPath = filepath.Dir(ffmpeg)
	}
	if _, _, err := RunSync(ctx, vadSync); err != nil {
		t.Fatalf("real webrtc sync: %v", err)
	}

	audio := filepath.Join(dir, "reference.wav")
	if err := ExtractAudio(ctx, "", video, audioIndex, 2, audio); err != nil {
		t.Fatalf("extract audio (%s): %v", audioLanguage, err)
	}
	if info, err := os.Stat(audio); err != nil || info.Size() == 0 {
		t.Fatalf("audio output missing: %v", err)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
