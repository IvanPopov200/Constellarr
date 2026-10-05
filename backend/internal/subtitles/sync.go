package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultHelperTimeout = 600
	maxHelperTimeout     = 3600
	defaultAudioSeconds  = 3600
	maxAudioSeconds      = 7200
	helperLogLimit       = 64 << 10
	probeBodyLimit       = 2 << 20
	defaultProbeTimeout  = 30
	maxStreamIndex       = 4096
)

var (
	helperMarkerRe = regexp.MustCompile(`[A-Za-z0-9_./\\-]+\.py:\d+`)
	scorePattern   = regexp.MustCompile(`score:\s*(-?[0-9.]+)`)
	offsetPattern  = regexp.MustCompile(`offset seconds:\s*(-?[0-9.]+)`)
	scalePattern   = regexp.MustCompile(`framerate scale factor:\s*([0-9.]+)`)
	lowQualityRe   = regexp.MustCompile(`low-quality alignment \(([^)]*)\)`)
)

// allowedVAD mirrors the detector names accepted by ffsubsync 0.5.x, including the subs_then_* defaults.
var allowedVAD = map[string]bool{
	"": true, "webrtc": true, "auditok": true, "silero": true,
	"subs_then_webrtc": true, "subs_then_auditok": true, "subs_then_silero": true,
	"fused": true, "fused:intersection": true, "fused:union": true, "fused:weighted": true,
}

// normalizeHelperLog strips rich "file.py:line" markers and hard wrapping so ffsubsync 0.5.x warnings stay matchable.
func normalizeHelperLog(text string) string {
	return strings.Join(strings.Fields(helperMarkerRe.ReplaceAllString(text, " ")), " ")
}

// helperMetrics is the alignment summary parsed from ffsubsync output.
type helperMetrics struct {
	Offset     float64
	Scale      float64
	Score      float64
	HasOffset  bool
	HasScale   bool
	HasScore   bool
	LowQuality bool
	Reasons    string
}

// SyncOptions carries the resolved sync settings for one run.
type SyncOptions struct {
	Reference string
	Input     string
	Output    string
	Mode      string
	// Offset, FPSFrom, and FPSTo drive the pure-Go offset and frame-rate modes.
	Offset          float64
	FPSFrom         float64
	FPSTo           float64
	MaxOffset       float64
	MinScore        float64
	QualityMaxOff   float64
	MaxFramerateDev float64
	NoFixFramerate  bool
	GoldenSection   bool
	VAD             string
	AudioStream     int
	AudioSeconds    int
	TimeoutSeconds  int
	HelperPath      string
	FFmpegPath      string
	RootPath        string
}

// helperArgs builds the fixed ffsubsync command line; no shell is ever involved.
func helperArgs(opts SyncOptions) []string {
	args := []string{
		opts.Reference,
		"-i", opts.Input,
		"-o", opts.Output,
		"--max-offset-seconds", strconv.FormatFloat(opts.MaxOffset, 'f', 3, 64),
		"--output-encoding", "utf-8",
	}
	if opts.MinScore != 0 || opts.QualityMaxOff > 0 {
		args = append(args,
			"--skip-sync-on-low-quality",
			"--min-score", strconv.FormatFloat(opts.MinScore, 'f', 3, 64),
			"--quality-max-offset-seconds", strconv.FormatFloat(opts.QualityMaxOff, 'f', 3, 64))
	}
	if opts.MaxFramerateDev > 0 {
		args = append(args, "--max-framerate-deviation", strconv.FormatFloat(opts.MaxFramerateDev, 'f', 3, 64))
	}
	if opts.NoFixFramerate {
		args = append(args, "--no-fix-framerate")
	}
	if opts.GoldenSection {
		args = append(args, "--gss")
	}
	if opts.VAD != "" {
		args = append(args, "--vad", opts.VAD)
	}
	if dir := helperFFmpegDir(opts.FFmpegPath); dir != "" {
		args = append(args, "--ffmpeg-path", dir)
	}
	return args
}

// helperFFmpegDir converts a configured ffmpeg binary path into the directory ffsubsync expects.
func helperFFmpegDir(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}
	if info, err := os.Stat(configured); err == nil && !info.IsDir() {
		return filepath.Dir(configured)
	}
	return configured
}

func parseHelperLog(text string) helperMetrics {
	metrics := helperMetrics{}
	normalized := normalizeHelperLog(text)
	if match := scorePattern.FindStringSubmatch(normalized); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			metrics.Score, metrics.HasScore = value, true
		}
	}
	if match := offsetPattern.FindStringSubmatch(normalized); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			metrics.Offset, metrics.HasOffset = value, true
		}
	}
	if match := scalePattern.FindStringSubmatch(normalized); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			metrics.Scale, metrics.HasScale = value, true
		}
	}
	if match := lowQualityRe.FindStringSubmatch(normalized); match != nil {
		metrics.LowQuality, metrics.Reasons = true, strings.Join(strings.Fields(match[1]), " ")
	}
	if !metrics.HasScale {
		metrics.Scale = 1
	}
	return metrics
}

// runCommand executes one bounded child process with its output captured to a temporary log.
func runCommand(ctx context.Context, name string, args []string, timeout time.Duration, roots ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	setProcessGroup(cmd)
	// Kill the whole helper process group so ffmpeg children cannot outlive a timeout.
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	log := &limitedBuffer{limit: helperLogLimit}
	cmd.Stdout = log
	cmd.Stderr = log
	err := cmd.Run()
	logText := redactPaths(log.String(), roots...)
	if runCtx.Err() != nil && ctx.Err() == nil {
		return logText, fmt.Errorf("%w: helper timed out after %s", ErrTimeout, timeout)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return logText, ctxErr
		}
		message := helperFailureLine(logText)
		if message == "" {
			message = err.Error()
		}
		return logText, fmt.Errorf("%s: %s", filepath.Base(name), message)
	}
	return logText, nil
}

// helperFailureLine prefers the logger's error text and tolerates rich hard-wrapping.
func helperFailureLine(text string) string {
	normalized := normalizeHelperLog(text)
	for _, marker := range []string{"ERROR", "Traceback (most recent call last)", "Exception"} {
		if index := strings.Index(normalized, marker); index >= 0 {
			return truncate(normalized[index:], 300)
		}
	}
	if normalized == "" {
		return ""
	}
	if len(normalized) > 300 {
		normalized = normalized[len(normalized)-300:]
	}
	return truncate(normalized, 300)
}

// RunSync executes one synchronization and returns the validated payload.
func RunSync(ctx context.Context, opts SyncOptions) (SyncResult, []byte, error) {
	if err := validateSyncOptions(opts); err != nil {
		return SyncResult{}, nil, err
	}
	switch opts.Mode {
	case "offset":
		return applyOffset(ctx, opts)
	case "fps":
		return applyFPS(ctx, opts)
	case "audio", "reference":
		return runHelperSync(ctx, opts)
	default:
		return SyncResult{}, nil, fmt.Errorf("%w: sync mode must be offset, fps, audio, or reference", ErrInvalid)
	}
}

func validateSyncOptions(opts SyncOptions) error {
	if !allowedVAD[opts.VAD] {
		return fmt.Errorf("%w: unsupported VAD %q", ErrInvalid, opts.VAD)
	}
	if opts.MaxOffset < 0 || opts.MaxOffset > 600 {
		return fmt.Errorf("%w: max offset must be between 0 and 600 seconds", ErrInvalid)
	}
	if opts.AudioStream < -1 || opts.AudioStream > maxStreamIndex {
		return fmt.Errorf("%w: audio stream index is out of range", ErrInvalid)
	}
	if opts.TimeoutSeconds < 0 || opts.TimeoutSeconds > maxHelperTimeout {
		return fmt.Errorf("%w: helper timeout must be between 0 and %d seconds", ErrInvalid, maxHelperTimeout)
	}
	if opts.Input == "" {
		return fmt.Errorf("%w: a subtitle file is required", ErrInvalid)
	}
	switch opts.Mode {
	case "offset":
		if absFloat(opts.Offset) > 600 {
			return fmt.Errorf("%w: offset must be between -600 and 600 seconds", ErrInvalid)
		}
	case "fps":
		if opts.FPSFrom < 1 || opts.FPSFrom > 240 || opts.FPSTo < 1 || opts.FPSTo > 240 {
			return fmt.Errorf("%w: frame rates must be between 1 and 240", ErrInvalid)
		}
	case "audio", "reference":
		if opts.Reference == "" {
			return fmt.Errorf("%w: a reference is required for %s synchronization", ErrInvalid, opts.Mode)
		}
	}
	return nil
}

func (opts SyncOptions) timeout() time.Duration {
	seconds := opts.TimeoutSeconds
	if seconds <= 0 {
		seconds = defaultHelperTimeout
	}
	if seconds > maxHelperTimeout {
		seconds = maxHelperTimeout
	}
	return time.Duration(seconds) * time.Second
}

func applyOffset(ctx context.Context, opts SyncOptions) (SyncResult, []byte, error) {
	data, err := readBoundedFile(ctx, opts.Input)
	if err != nil {
		return SyncResult{}, nil, err
	}
	document, err := ParseDocument(data)
	if err != nil {
		return SyncResult{}, nil, err
	}
	if opts.MaxOffset > 0 && absFloat(opts.Offset) > opts.MaxOffset {
		return SyncResult{}, nil, fmt.Errorf("%w: offset %.3fs exceeds the %.3fs limit", ErrInvalid, opts.Offset, opts.MaxOffset)
	}
	out, err := document.Shift(time.Duration(opts.Offset * float64(time.Second)))
	if err != nil {
		return SyncResult{}, nil, err
	}
	return SyncResult{
		Mode:    "offset",
		Offset:  opts.Offset,
		Scale:   1,
		Changed: !bytes.Equal(data, out),
		Detail:  fmt.Sprintf("shifted every cue by %.3fs", opts.Offset),
	}, out, nil
}

func applyFPS(ctx context.Context, opts SyncOptions) (SyncResult, []byte, error) {
	data, err := readBoundedFile(ctx, opts.Input)
	if err != nil {
		return SyncResult{}, nil, err
	}
	document, err := ParseDocument(data)
	if err != nil {
		return SyncResult{}, nil, err
	}
	out, err := document.Scale(opts.FPSFrom, opts.FPSTo)
	if err != nil {
		return SyncResult{}, nil, err
	}
	return SyncResult{
		Mode:    "fps",
		Scale:   opts.FPSFrom / opts.FPSTo,
		Changed: !bytes.Equal(data, out),
		Detail:  fmt.Sprintf("rescaled cues from %g fps to %g fps", opts.FPSFrom, opts.FPSTo),
	}, out, nil
}

func runHelperSync(ctx context.Context, opts SyncOptions) (SyncResult, []byte, error) {
	helper, err := resolveHelper(opts.HelperPath)
	if err != nil {
		return SyncResult{}, nil, err
	}
	reference := opts.Reference
	cleanup := func() {}
	if opts.Mode == "audio" && opts.AudioStream >= 0 {
		work, err := os.MkdirTemp("", "constellarr-subs-")
		if err != nil {
			return SyncResult{}, nil, errors.New("subtitles: a working directory could not be created")
		}
		cleanup = func() { os.RemoveAll(work) }
		audio := filepath.Join(work, "reference.wav")
		seconds := opts.AudioSeconds
		if seconds <= 0 || seconds > maxAudioSeconds {
			seconds = defaultAudioSeconds
		}
		if err := ExtractAudio(ctx, opts.FFmpegPath, reference, opts.AudioStream, seconds, audio); err != nil {
			cleanup()
			return SyncResult{}, nil, err
		}
		reference = audio
	}
	defer cleanup()

	output, err := os.CreateTemp("", "constellarr-sync-*.srt")
	if err != nil {
		return SyncResult{}, nil, errors.New("subtitles: a temporary output file could not be created")
	}
	outputPath := output.Name()
	output.Close()
	defer os.Remove(outputPath)

	runOpts := opts
	runOpts.Reference, runOpts.Output = reference, outputPath
	logText, err := runCommand(ctx, helper, helperArgs(runOpts), opts.timeout(), opts.RootPath)
	metrics := parseHelperLog(logText)
	if err != nil {
		return SyncResult{}, nil, fmt.Errorf("subtitles: sync failed: %w", err)
	}
	if metrics.LowQuality {
		return SyncResult{}, nil, fmt.Errorf("%w: %s", ErrLowQualitySync, metrics.Reasons)
	}
	data, err := readBoundedFile(ctx, outputPath)
	if err != nil {
		return SyncResult{}, nil, fmt.Errorf("subtitles: sync produced no usable output: %w", err)
	}
	input, err := readBoundedFile(ctx, opts.Input)
	if err != nil {
		return SyncResult{}, nil, err
	}
	if _, err := ParseDocument(data); err != nil {
		return SyncResult{}, nil, fmt.Errorf("%w: synced subtitle is invalid: %v", ErrInvalid, err)
	}
	detail := fmt.Sprintf("aligned against %s", filepath.Base(reference))
	if metrics.HasScore {
		detail += fmt.Sprintf(" (score %.1f)", metrics.Score)
	}
	return SyncResult{
		Mode:    opts.Mode,
		Offset:  metrics.Offset,
		Scale:   metrics.Scale,
		Score:   metrics.Score,
		Changed: !bytes.Equal(input, data),
		Detail:  detail,
	}, data, nil
}

// resolveHelper finds ffsubsync and reports an actionable error when it is missing.
func resolveHelper(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		info, err := os.Stat(configured)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("%w: configured ffsubsync path %q does not exist", ErrUnavailable, configured)
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%w: %q is not executable", ErrUnavailable, configured)
		}
		return configured, nil
	}
	path, err := exec.LookPath("ffsubsync")
	if err != nil {
		return "", fmt.Errorf("%w: ffsubsync is not installed; set the helper path in subtitle settings or install it with pip install ffsubsync", ErrUnavailable)
	}
	return path, nil
}

func resolveFFmpeg(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		candidate := filepath.Join(configured, "ffmpeg")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("%w: no ffmpeg binary was found in %q", ErrUnavailable, configured)
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("%w: ffmpeg is not installed or not on PATH", ErrUnavailable)
	}
	return path, nil
}

func resolveFFprobe(ffmpegPath string) (string, error) {
	if ffmpegPath != "" {
		candidate := filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", fmt.Errorf("%w: ffprobe is not installed or not on PATH", ErrUnavailable)
	}
	return path, nil
}

// ProbeStreams lists embedded audio and subtitle streams for the UI.
func ProbeStreams(ctx context.Context, ffmpegPath, videoPath string) ([]Stream, error) {
	ffmpeg, err := resolveFFmpeg(ffmpegPath)
	if err != nil {
		return nil, err
	}
	probe, err := resolveFFprobe(ffmpeg)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, probe,
		"-v", "error", "-print_format", "json", "-show_streams", "-show_format", videoPath)
	var out bytes.Buffer
	var errOut limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if probeCtx.Err() != nil {
			return nil, fmt.Errorf("%w: ffprobe timed out", ErrUnavailable)
		}
		return nil, fmt.Errorf("subtitles: ffprobe failed: %s", lastMeaningfulLine(redactPaths(errOut.String())))
	}
	var parsed probeResponse
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, errors.New("subtitles: ffprobe returned an unreadable response")
	}
	streams := make([]Stream, 0, len(parsed.Streams))
	for _, item := range parsed.Streams {
		if item.CodecType != "audio" && item.CodecType != "subtitle" {
			continue
		}
		forcedByTitle, hiByTitle := streamTitleFlags(item.Tags["title"])
		streams = append(streams, Stream{
			Index:    item.Index,
			Type:     item.CodecType,
			Codec:    item.CodecName,
			Language: truncate(item.Tags["language"], 32),
			Title:    truncate(item.Tags["title"], 200),
			Forced:   item.Disposition["forced"] == 1 || forcedByTitle,
			HI:       hiByTitle,
			Default:  item.Disposition["default"] == 1,
			Channels: item.Channels,
		})
	}
	return streams, nil
}

// ExtractEmbeddedSubtitle extracts one subtitle stream to SRT via ffmpeg.
func ExtractEmbeddedSubtitle(ctx context.Context, ffmpegPath, videoPath string, streamIndex int, outputPath string) error {
	ordinal, err := streamOrdinal(ctx, ffmpegPath, videoPath, streamIndex, "subtitle")
	if err != nil {
		return err
	}
	ffmpeg, err := resolveFFmpeg(ffmpegPath)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout*10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, ffmpeg, "-nostdin", "-y", "-loglevel", "error",
		"-i", videoPath, "-map", "0:s:"+strconv.Itoa(ordinal), "-f", "srt", outputPath)
	log := &limitedBuffer{limit: helperLogLimit}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("%w: subtitle extraction timed out", ErrUnavailable)
		}
		return fmt.Errorf("subtitles: ffmpeg could not extract the subtitle stream: %s", lastMeaningfulLine(redactPaths(log.String())))
	}
	if _, err := readBoundedFile(ctx, outputPath); err != nil {
		return fmt.Errorf("%w: extracted subtitle is unusable", ErrInvalid)
	}
	return nil
}

// ExtractAudio writes one audio stream to a mono 16 kHz WAV reference for voice activity sync.
func ExtractAudio(ctx context.Context, ffmpegPath, videoPath string, streamIndex int, seconds int, outputPath string) error {
	ffmpeg, err := resolveFFmpeg(ffmpegPath)
	if err != nil {
		return err
	}
	args := []string{"-nostdin", "-y", "-loglevel", "error", "-i", videoPath, "-vn", "-ac", "1", "-ar", "16000"}
	if streamIndex >= 0 {
		ordinal, err := streamOrdinal(ctx, ffmpegPath, videoPath, streamIndex, "audio")
		if err != nil {
			return err
		}
		args = append(args, "-map", "0:a:"+strconv.Itoa(ordinal))
	}
	if seconds > 0 {
		args = append(args, "-t", strconv.Itoa(seconds))
	}
	args = append(args, "-f", "wav", outputPath)
	runCtx, cancel := context.WithTimeout(ctx, defaultProbeTimeout*20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, ffmpeg, args...)
	log := &limitedBuffer{limit: helperLogLimit}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("%w: audio extraction timed out", ErrUnavailable)
		}
		return fmt.Errorf("subtitles: ffmpeg could not extract the audio stream: %s", lastMeaningfulLine(redactPaths(log.String())))
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("%w: audio extraction produced no output", ErrInvalid)
	}
	return nil
}

// streamTitleFlags derives variant hints from whole title tokens such as "SDH" or "Forced".
func streamTitleFlags(title string) (forced, hi bool) {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return false, false
	}
	if strings.Contains(title, "hearing impaired") {
		hi = true
	}
	for _, token := range strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		switch token {
		case "sdh", "hi":
			hi = true
		case "forced", "foreign":
			forced = true
		}
	}
	return forced, hi
}

func streamOrdinal(ctx context.Context, ffmpegPath, videoPath string, streamIndex int, kind string) (int, error) {
	streams, err := ProbeStreams(ctx, ffmpegPath, videoPath)
	if err != nil {
		return 0, err
	}
	ordinal := 0
	for _, stream := range streams {
		if stream.Type != kind {
			continue
		}
		if stream.Index == streamIndex {
			return ordinal, nil
		}
		ordinal++
	}
	return 0, fmt.Errorf("%w: %s stream %d does not exist", ErrNotFound, kind, streamIndex)
}

type probeResponse struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		Channels    int               `json:"channels"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
}

// limitedBuffer keeps at most limit bytes while draining the rest of the stream.
type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buffer.Write(p[:remaining])
		} else {
			b.buffer.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func readBoundedFile(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle file is not readable", ErrNotFound)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSubtitleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle file could not be read", ErrInvalid)
	}
	if len(data) > maxSubtitleBytes {
		return nil, fmt.Errorf("%w: subtitle file exceeds %d bytes", ErrInvalid, maxSubtitleBytes)
	}
	return data, nil
}

func redactPaths(text string, roots ...string) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		text = strings.ReplaceAll(text, root, "<library>")
	}
	return text
}

func lastMeaningfulLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.Join(strings.Fields(lines[i]), " ")
		if line != "" {
			return truncate(line, 300)
		}
	}
	return ""
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
