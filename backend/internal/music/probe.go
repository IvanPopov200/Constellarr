package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	probeTimeout    = 30 * time.Second
	maxProbeBytes   = 1 << 20
	defaultFFprobe  = "ffprobe"
	attachedPicture = 1
)

// audioInfo is the subset of ffprobe output the importer trusts for identity and quality.
type audioInfo struct {
	Audio       bool
	Codec       string
	Format      string
	Lossless    bool
	BitrateKbps int
	SampleRate  int
	Channels    int
	DurationMS  int
	Tags        map[string]string
}

type probeOutput struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		BitRate     string `json:"bit_rate"`
		SampleRate  string `json:"sample_rate"`
		Channels    int    `json:"channels"`
		Duration    string `json:"duration"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

// ffprobeAvailable reports whether the configured helper can be executed.
func ffprobeAvailable(helper string) bool {
	helper = strings.TrimSpace(helper)
	if helper == "" {
		helper = defaultFFprobe
	}
	if strings.ContainsRune(helper, os.PathSeparator) {
		info, err := os.Stat(helper)
		return err == nil && info.Mode().IsRegular()
	}
	_, err := exec.LookPath(helper)
	return err == nil
}

// probeAudio runs the configured ffprobe helper and validates that the file is audio, not video.
func probeAudio(ctx context.Context, helper, filePath string) (audioInfo, error) {
	helper = strings.TrimSpace(helper)
	if helper == "" {
		helper = defaultFFprobe
	}
	runCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, helper, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-i", filePath)
	command.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitedWriter{buffer: &stdout, limit: maxProbeBytes}
	command.Stderr = &limitedWriter{buffer: &stderr, limit: 4096}
	if err := command.Run(); err != nil {
		if runCtx.Err() != nil {
			return audioInfo{}, errors.New("music: probing a media file timed out")
		}
		return audioInfo{}, errors.New("music: a media file could not be probed")
	}
	var output probeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return audioInfo{}, errors.New("music: the probe output could not be read")
	}
	return audioFromProbe(output)
}

func audioFromProbe(output probeOutput) (audioInfo, error) {
	info := audioInfo{}
	videoStreams := 0
	for _, stream := range output.Streams {
		switch stream.CodecType {
		case "video":
			if stream.Disposition.AttachedPic != attachedPicture {
				videoStreams++
			}
		case "audio":
			if info.Audio {
				continue
			}
			info.Audio = true
			info.Codec = strings.ToLower(strings.TrimSpace(stream.CodecName))
			info.BitrateKbps = parseKbps(stream.BitRate)
			info.SampleRate, _ = strconv.Atoi(strings.TrimSpace(stream.SampleRate))
			info.Channels = stream.Channels
			info.DurationMS = parseDurationMS(stream.Duration)
		}
	}
	if videoStreams > 0 {
		return audioInfo{}, fmt.Errorf("%w: the file contains video", errNotAudio)
	}
	if !info.Audio {
		return audioInfo{}, fmt.Errorf("%w: the file has no audio stream", errNotAudio)
	}
	if info.BitrateKbps == 0 {
		info.BitrateKbps = parseKbps(output.Format.BitRate)
	}
	if info.DurationMS == 0 {
		info.DurationMS = parseDurationMS(output.Format.Duration)
	}
	info.Format = normalizeCodec(info.Codec, output.Format.FormatName)
	info.Lossless = losslessFormats[info.Format] || strings.HasPrefix(info.Codec, "pcm_")
	if info.Lossless {
		info.BitrateKbps = 0
	}
	info.Tags = normalizeTags(output.Format.Tags)
	return info, nil
}

func normalizeCodec(codec, formatName string) string {
	switch {
	case strings.HasPrefix(codec, "pcm_"), codec == "pcm":
		return "wav"
	case codec == "":
	default:
		if strings.HasPrefix(codec, "flac") {
			return "flac"
		}
		if strings.HasPrefix(codec, "alac") {
			return "alac"
		}
		if codec == "mp3" || codec == "mp2" {
			return "mp3"
		}
		if codec == "aac" || codec == "aac_latm" {
			return "aac"
		}
		if strings.HasPrefix(codec, "vorbis") {
			return "vorbis"
		}
		if codec == "opus" {
			return "opus"
		}
		if codec == "wmav2" || codec == "wmapro" {
			return "wma"
		}
		if codec == "ape" {
			return "ape"
		}
		return codec
	}
	// Fall back to the container when no stream codec was reported.
	container := strings.ToLower(strings.TrimSpace(strings.Split(formatName, ",")[0]))
	switch container {
	case "flac":
		return "flac"
	case "mp3":
		return "mp3"
	case "wav":
		return "wav"
	case "ogg":
		return "vorbis"
	case "opus":
		return "opus"
	case "m4a", "aac", "mp4":
		return "aac"
	default:
		return container
	}
}

func normalizeTags(raw map[string]string) map[string]string {
	tags := make(map[string]string, len(raw))
	for key, value := range raw {
		normalized := strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch normalized {
		case "albumartist", "album artist", "album_artist":
			normalized = "albumartist"
		case "disc", "discnumber", "disc_number":
			normalized = "disc"
		case "track", "tracknumber", "track_number":
			normalized = "track"
		case "date", "year", "originaldate":
			normalized = "date"
		default:
			if strings.HasPrefix(normalized, "musicbrainz_") || strings.HasPrefix(normalized, "musicbrainz ") {
				normalized = "musicbrainz_" + strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(normalized, "musicbrainz_"), "musicbrainz "))
			}
		}
		if _, exists := tags[normalized]; !exists {
			tags[normalized] = value
		}
	}
	return tags
}

func parseKbps(raw string) int {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int(math.Round(float64(value) / 1000))
}

func parseDurationMS(raw string) int {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int(math.Round(value * 1000))
}

// limitedWriter caps helper output so a broken probe cannot exhaust memory.
type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	total := len(p)
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return total, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	w.buffer.Write(p)
	return total, nil
}

func audioExtension(name string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(name))]
}

var audioExtensions = map[string]bool{
	".flac": true, ".m4a": true, ".mp3": true, ".aac": true, ".ogg": true, ".oga": true,
	".opus": true, ".wav": true, ".aiff": true, ".aif": true, ".ape": true, ".wv": true,
	".wma": true, ".dsf": true, ".dff": true, ".mpc": true, ".tta": true, ".alac": true,
}
