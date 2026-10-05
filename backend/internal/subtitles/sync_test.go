package subtitles

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHelperArgsAreFixedAndOrdered(t *testing.T) {
	args := helperArgs(SyncOptions{
		Reference: "video.mkv", Input: "in.srt", Output: "out.srt",
		MaxOffset: 45.5, MinScore: 0.5, QualityMaxOff: 30, MaxFramerateDev: 0.1,
		GoldenSection: true, VAD: "webrtc", FFmpegPath: "/opt/ffmpeg",
	})
	want := []string{
		"video.mkv", "-i", "in.srt", "-o", "out.srt",
		"--max-offset-seconds", "45.500", "--output-encoding", "utf-8",
		"--skip-sync-on-low-quality", "--min-score", "0.500", "--quality-max-offset-seconds", "30.000",
		"--max-framerate-deviation", "0.100", "--gss", "--vad", "webrtc", "--ffmpeg-path", "/opt/ffmpeg",
	}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v", args)
	}
	plain := helperArgs(SyncOptions{Reference: "v", Input: "i", Output: "o", MaxOffset: 60})
	if strings.Join(plain, " ") != "v -i i -o o --max-offset-seconds 60.000 --output-encoding utf-8" {
		t.Fatalf("plain args = %v", plain)
	}
	// A configured ffmpeg binary becomes the directory ffsubsync expects.
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withBinary := helperArgs(SyncOptions{Reference: "v", Input: "i", Output: "o", MaxOffset: 60, FFmpegPath: ffmpeg})
	if !strings.Contains(strings.Join(withBinary, " "), "--ffmpeg-path "+filepath.Dir(ffmpeg)) {
		t.Fatalf("ffmpeg directory args = %v", withBinary)
	}
}

func TestParseHelperLog(t *testing.T) {
	log := "INFO extracting speech segments\nINFO score: -12.5\nINFO offset seconds: 2.250\nINFO framerate scale factor: 1.001\n"
	metrics := parseHelperLog(log)
	if !metrics.HasScore || metrics.Score != -12.5 || metrics.Offset != 2.25 || metrics.Scale != 1.001 {
		t.Fatalf("metrics = %+v", metrics)
	}
	low := parseHelperLog("WARNING low-quality alignment (score -1.0 < 0.0; |offset| 44.0s > 30.0s); leaving subtitles unmodified")
	if !low.LowQuality || !strings.Contains(low.Reasons, "offset") {
		t.Fatalf("low quality metrics = %+v", low)
	}
	// ffsubsync 0.5.x logs through rich, which hard-wraps long warnings mid-message.
	wrapped := "           INFO     score: 42685.000                            ffsubsync.py:255\n" +
		"           INFO     offset seconds: -7.000                      ffsubsync.py:256\n" +
		"           WARNING  low-quality alignment (score 42685.0 <      ffsubsync.py:269\n" +
		"                    100000.0); leaving subtitles unmodified\n"
	wrappedMetrics := parseHelperLog(wrapped)
	if !wrappedMetrics.LowQuality || !strings.Contains(wrappedMetrics.Reasons, "100000.0") {
		t.Fatalf("wrapped low quality metrics = %+v", wrappedMetrics)
	}
	if wrappedMetrics.Score != 42685 || wrappedMetrics.Offset != -7 {
		t.Fatalf("wrapped metrics = %+v", wrappedMetrics)
	}
}

func TestHelperFailureLineToleratesWrapping(t *testing.T) {
	wrapped := "INFO extracting speech segments from reference\n" +
		"           ERROR    unable to read reference; try ensuring     ffsubsync.py:734\n" +
		"                    the file exists and has correct permissions\n"
	message := helperFailureLine(wrapped)
	if !strings.HasPrefix(message, "ERROR") || !strings.Contains(message, "permissions") || strings.Contains(message, "ffsubsync.py") {
		t.Fatalf("failure line = %q", message)
	}
	if got := helperFailureLine("plain failure"); got != "plain failure" {
		t.Fatalf("plain failure = %q", got)
	}
}

func TestAudioStreamIndexDefaultsToAutomatic(t *testing.T) {
	if got := audioStreamIndex(nil); got != -1 {
		t.Fatalf("nil audio stream = %d", got)
	}
	negative := -1
	if got := audioStreamIndex(&negative); got != -1 {
		t.Fatalf("negative audio stream = %d", got)
	}
	zero, two := 0, 2
	if got := audioStreamIndex(&zero); got != 0 {
		t.Fatalf("stream zero = %d", got)
	}
	if got := audioStreamIndex(&two); got != 2 {
		t.Fatalf("stream two = %d", got)
	}
}

func TestRunSyncOffsetAndFPSModes(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.srt")
	if err := os.WriteFile(input, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	result, data, err := RunSync(context.Background(), SyncOptions{Mode: "offset", Input: input, Offset: -0.5, MaxOffset: 60})
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	if !result.Changed || !strings.Contains(string(data), "00:00:00,500 --> 00:00:03,000") {
		t.Fatalf("offset result: %+v %q", result, data)
	}
	if _, _, err := RunSync(context.Background(), SyncOptions{Mode: "offset", Input: input, Offset: 90, MaxOffset: 60}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized offset error = %v", err)
	}
	result, data, err = RunSync(context.Background(), SyncOptions{Mode: "fps", Input: input, FPSFrom: 25, FPSTo: 24})
	if err != nil {
		t.Fatalf("fps: %v", err)
	}
	if result.Scale != 25.0/24.0 || !strings.Contains(string(data), "00:00:01,042") {
		t.Fatalf("fps result: %+v %q", result, data)
	}
	if _, _, err := RunSync(context.Background(), SyncOptions{Mode: "fps", Input: input, FPSFrom: 25, FPSTo: 100}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported ratio error = %v", err)
	}
	if _, _, err := RunSync(context.Background(), SyncOptions{Mode: "audio", Input: input}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing reference error = %v", err)
	}
	if _, _, err := RunSync(context.Background(), SyncOptions{Mode: "audio", Input: input, Reference: "v", VAD: "--evil"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported vad error = %v", err)
	}
	for _, vad := range []string{"subs_then_webrtc", "subs_then_auditok", "subs_then_silero", "fused:weighted"} {
		if err := validateSyncOptions(SyncOptions{Mode: "audio", Input: input, Reference: "v", VAD: vad}); err != nil {
			t.Fatalf("vad %q rejected: %v", vad, err)
		}
	}
}

// writeFakeHelper installs a stand-in ffsubsync that records its arguments and copies a fixture output.
func writeFakeHelper(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	path := filepath.Join(dir, "ffsubsync")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

func TestRunHelperSyncUsesFixedArgumentsAndBoundedOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.srt")
	if err := os.WriteFile(input, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(dir, "reference.mkv")
	if err := os.WriteFile(reference, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	helper, argsFile := writeFakeHelper(t, `
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  prev="$arg"
done
printf '1\n00:00:03,000 --> 00:00:05,000\nHello\n' > "$out"
echo "INFO score: 42.5" 1>&2
echo "INFO offset seconds: 2.000" 1>&2
echo "INFO framerate scale factor: 1.000" 1>&2
`)
	ctx := context.Background()
	result, data, err := RunSync(ctx, SyncOptions{
		Mode: "reference", Input: input, Reference: reference,
		MaxOffset: 60, MinScore: 0.5, QualityMaxOff: 30, HelperPath: helper, TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("helper sync: %v", err)
	}
	if result.Score != 42.5 || result.Offset != 2 || result.Scale != 1 || !result.Changed {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(string(data), "00:00:03,000 --> 00:00:05,000") {
		t.Fatalf("payload = %q", data)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if args[0] != reference || args[1] != "-i" || args[2] != input || args[3] != "-o" {
		t.Fatalf("recorded args = %v", args)
	}
	if !strings.Contains(strings.Join(args, " "), "--min-score 0.500") {
		t.Fatalf("quality flags missing: %v", args)
	}
}

func TestRunHelperSyncRejectsLowQualityAlignment(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.srt")
	if err := os.WriteFile(input, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(dir, "reference.srt")
	if err := os.WriteFile(reference, []byte(sampleSRT), 0o644); err != nil {
		t.Fatal(err)
	}
	helper, _ := writeFakeHelper(t, `
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  prev="$arg"
done
cp "$2" "$out"
echo "WARNING low-quality alignment (score -1.0 < 0.0); leaving subtitles unmodified" 1>&2
`)
	_, _, err := RunSync(context.Background(), SyncOptions{
		Mode: "reference", Input: input, Reference: reference,
		MaxOffset: 60, MinScore: 0.5, QualityMaxOff: 30, HelperPath: helper,
	})
	if !errors.Is(err, ErrLowQualitySync) {
		t.Fatalf("low quality error = %v", err)
	}
}

func TestRunHelperSyncFailsWhenHelperIsMissingOrTimesOut(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.srt")
	os.WriteFile(input, []byte(sampleSRT), 0o644)
	_, _, err := RunSync(context.Background(), SyncOptions{
		Mode: "reference", Input: input, Reference: input, HelperPath: filepath.Join(dir, "absent-ffsubsync"),
	})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "ffsubsync") {
		t.Fatalf("missing helper error = %v", err)
	}
	helper, _ := writeFakeHelper(t, "sleep 5")
	start := time.Now()
	_, _, err = RunSync(context.Background(), SyncOptions{
		Mode: "reference", Input: input, Reference: input, HelperPath: helper, TimeoutSeconds: 1,
	})
	if !errors.Is(err, ErrTimeout) || time.Since(start) > 4*time.Second {
		t.Fatalf("timeout error = %v after %s", err, time.Since(start))
	}
}

func TestRunHelperSyncRejectsUnparseableHelperOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.srt")
	os.WriteFile(input, []byte(sampleSRT), 0o644)
	helper, _ := writeFakeHelper(t, `
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  prev="$arg"
done
printf 'this is not a subtitle' > "$out"
`)
	if _, _, err := RunSync(context.Background(), SyncOptions{
		Mode: "reference", Input: input, Reference: input, HelperPath: helper,
	}); err == nil {
		t.Fatal("unparseable helper output must fail")
	}
}

func TestProbeAndExtractEmbeddedStreams(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "fixture.mkv")
	subtitle := filepath.Join(dir, "fixture.srt")
	if err := os.WriteFile(subtitle, []byte("1\n00:00:00,500 --> 00:00:01,500\nEmbedded line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=128x72:d=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-i", subtitle,
		"-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "mpeg4", "-c:a", "aac", "-c:s", "srt", "-shortest", video)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build failed: %v %s", err, output)
	}
	ctx := context.Background()
	streams, err := ProbeStreams(ctx, "", video)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var audioIndex, subtitleIndex = -1, -1
	for _, stream := range streams {
		switch stream.Type {
		case "audio":
			audioIndex = stream.Index
		case "subtitle":
			subtitleIndex = stream.Index
		}
	}
	if audioIndex < 0 || subtitleIndex < 0 {
		t.Fatalf("streams = %+v", streams)
	}
	extracted := filepath.Join(dir, "extracted.srt")
	if err := ExtractEmbeddedSubtitle(ctx, "", video, subtitleIndex, extracted); err != nil {
		t.Fatalf("extract subtitle: %v", err)
	}
	data, err := os.ReadFile(extracted)
	if err != nil || !strings.Contains(string(data), "Embedded line") {
		t.Fatalf("extracted subtitle = %q, %v", data, err)
	}
	audio := filepath.Join(dir, "reference.wav")
	if err := ExtractAudio(ctx, "", video, audioIndex, 1, audio); err != nil {
		t.Fatalf("extract audio: %v", err)
	}
	if info, err := os.Stat(audio); err != nil || info.Size() == 0 {
		t.Fatalf("extracted audio missing: %v", err)
	}
	if err := ExtractEmbeddedSubtitle(ctx, "", video, 999, filepath.Join(dir, "missing.srt")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing stream error = %v", err)
	}
	if _, err := ProbeStreams(ctx, "", filepath.Join(dir, "absent.mkv")); err == nil {
		t.Fatal("probe must fail for a missing file")
	}
}

func TestRedactPathsInHelperOutput(t *testing.T) {
	log := "ERROR could not read /media/library/Movie/Movie.en.srt after " + strconv.Itoa(3) + " tries"
	if got := redactPaths(log, "/media/library"); strings.Contains(got, "/media/library") {
		t.Fatalf("path leaked: %q", got)
	}
}
