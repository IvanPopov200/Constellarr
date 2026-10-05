package media

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessZIPExtraction(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("media-payload"), 100)
	writeZIP(t, filepath.Join(in, "release.zip"),
		zipEntry{name: "Movie/movie.mkv", data: payload},
		zipEntry{name: "Movie/release.nfo", data: []byte("notes")},
		zipEntry{name: "cover.jpg", data: []byte("cover")},
	)
	var stages []string
	files, err := Process(context.Background(), in, out, 0, func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(files) != 1 || files[0].Name != "Movie/movie.mkv" || files[0].Size != int64(len(payload)) {
		t.Fatalf("files = %+v", files)
	}
	got, err := os.ReadFile(filepath.Join(out, "Movie", "movie.mkv"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("output content differs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "cover.jpg")); err == nil {
		t.Fatal("non-media entry was extracted")
	}
	if len(stages) != 2 || stages[0] != stageVerifying || stages[1] != stageExtracting {
		t.Fatalf("stages = %v", stages)
	}
}

func TestProcessRejectsUnsafeZIPEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
	}{
		{name: "parent", entries: []zipEntry{{name: "../escape.mkv", data: []byte("x")}}},
		{name: "absolute", entries: []zipEntry{{name: "/escape.mkv", data: []byte("x")}}},
		{name: "backslash", entries: []zipEntry{{name: `..\escape.mkv`, data: []byte("x")}}},
		{name: "subtitle", entries: []zipEntry{{name: "../escape.srt", data: []byte("subtitle")}}},
		{name: "symlink", entries: []zipEntry{{name: "movie.mkv", data: []byte("target"), mode: os.ModeSymlink | 0o777}}},
		{name: "afterMedia", entries: []zipEntry{
			{name: "movie.mkv", data: []byte("partial")},
			{name: "../escape.mkv", data: []byte("x")},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			in := filepath.Join(base, "in")
			out := filepath.Join(base, "out")
			if err := os.MkdirAll(in, 0o700); err != nil {
				t.Fatal(err)
			}
			writeZIP(t, filepath.Join(in, "release.zip"), tc.entries...)
			if _, err := Process(context.Background(), in, out, 0, nil); err == nil {
				t.Fatal("unsafe entry was accepted")
			}
			if _, err := os.Stat(filepath.Join(base, "escape.mkv")); err == nil {
				t.Fatal("wrote outside the output directory")
			}
			if _, err := os.Stat(filepath.Join(out, "escape.mkv")); err == nil {
				t.Fatal("wrote unsafe entry")
			}
			if info, err := os.Stat(filepath.Join(in, "release.zip")); err != nil || info.Size() == 0 {
				t.Fatalf("input was not preserved: %v", err)
			}
		})
	}
}

func TestProcessRequiresMedia(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	writeZIP(t, filepath.Join(in, "release.zip"),
		zipEntry{name: "movie.mkv", data: nil},
		zipEntry{name: "release.nfo", data: []byte("notes")},
		zipEntry{name: "movie.en.srt", data: []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")},
	)
	_, err := Process(context.Background(), in, out, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "no playable media") {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessCopiesLooseMedia(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("loose-media"), 10)
	if err := os.WriteFile(filepath.Join(in, "Movie.2024.1080p.mkv"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "Movie.2024.nfo"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := Process(context.Background(), in, out, 0, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(files) != 1 || files[0].Name != "Movie.2024.1080p.mkv" || files[0].Size != int64(len(payload)) {
		t.Fatalf("files = %+v", files)
	}
	got, err := os.ReadFile(filepath.Join(out, "Movie.2024.1080p.mkv"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("output content differs: %v", err)
	}
}

func TestProcessMissingSegmentsWithoutPAR2(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(in, "Movie.2024.mkv"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Process(context.Background(), in, out, 3, nil)
	if err == nil || !strings.Contains(err.Error(), "3 missing segment") {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessRejectsNestedOutput(t *testing.T) {
	in := t.TempDir()
	if err := os.WriteFile(filepath.Join(in, "Movie.mkv"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Process(context.Background(), in, filepath.Join(in, "out"), 0, nil)
	if err == nil || !strings.Contains(err.Error(), "output directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := t.TempDir()
	if _, err := Process(ctx, in, t.TempDir(), 0, nil); err == nil {
		t.Fatal("cancelled context was ignored")
	}
}

func TestProcessPreservesMusicAndSubtitlePayloads(t *testing.T) {
	for _, archive := range []bool{false, true} {
		name := "loose"
		if archive {
			name = "zip"
		}
		t.Run(name, func(t *testing.T) {
			in, out := t.TempDir(), t.TempDir()
			entries := []zipEntry{
				{name: "01 - Track.flac", data: []byte("audio")},
				{name: "01 - Track.lrc", data: []byte("[00:01.00]Lyrics")},
				{name: "Episode.mkv", data: []byte("video")},
				{name: "Episode.bg.forced.srt", data: []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")},
			}
			if archive {
				writeZIP(t, filepath.Join(in, "release.zip"), entries...)
			} else {
				for _, entry := range entries {
					if err := os.WriteFile(filepath.Join(in, entry.name), entry.data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			files, err := Process(context.Background(), in, out, 0, nil)
			if err != nil || len(files) != len(entries) {
				t.Fatalf("Process = %v, %v", files, err)
			}
			for _, entry := range entries {
				got, err := os.ReadFile(filepath.Join(out, entry.name))
				if err != nil || !bytes.Equal(got, entry.data) {
					t.Fatalf("payload %s changed: %v", entry.name, err)
				}
			}
		})
	}
}
