package media

import (
	"bytes"
	"context"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestFindArchivesSelectsStarts(t *testing.T) {
	in := t.TempDir()
	for _, name := range []string{
		"Release.part02.rar",
		"Release.part01.rar",
		"Release.rar",
		"Single.rar",
		"Old.r00",
		"Old.rar",
		"Movie.zip",
	} {
		if err := os.WriteFile(filepath.Join(in, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(in)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, set := range findArchives(entries) {
		got[set.kind+":"+set.start] = true
	}
	for _, want := range []string{"rar:Release.part01.rar", "rar:Single.rar", "rar:Old.rar", "zip:Movie.zip"} {
		if !got[want] {
			t.Errorf("missing archive %s in %v", want, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("archives = %v", got)
	}
}

func TestProcessRARExtraction(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("rar-payload"), 500)
	archive := rarArchive(
		rarEntry{name: "Movie/movie.mkv", data: payload},
		rarEntry{name: "notes.txt", data: []byte("notes")},
	)
	if err := os.WriteFile(filepath.Join(in, "release.rar"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := Process(context.Background(), in, out, 0, nil)
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
	if _, err := os.Stat(filepath.Join(out, "notes.txt")); err == nil {
		t.Fatal("non-media entry was extracted")
	}
}

func TestProcessRARMultipartExtraction(t *testing.T) {
	payload := bytes.Repeat([]byte("multipart-payload"), 300)
	split := len(payload) / 2
	cases := map[string][2]string{
		"zeros":  {"Release.part01.rar", "Release.part02.rar"},
		"single": {"Release.part1.rar", "Release.part2.rar"},
	}
	for name, volumes := range cases {
		t.Run(name, func(t *testing.T) {
			in := t.TempDir()
			out := t.TempDir()
			first := rarEntry{name: "Movie.mkv", data: payload[:split], split: 0x0002, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
			second := rarEntry{name: "Movie.mkv", data: payload[split:], split: 0x0001, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
			if err := os.WriteFile(filepath.Join(in, volumes[0]), rarVolume([]rarEntry{first}, false, false), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(in, volumes[1]), rarVolume([]rarEntry{second}, false, true), 0o600); err != nil {
				t.Fatal(err)
			}
			files, err := Process(context.Background(), in, out, 0, nil)
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(files) != 1 || files[0].Name != "Movie.mkv" || files[0].Size != int64(len(payload)) {
				t.Fatalf("files = %+v", files)
			}
			got, err := os.ReadFile(filepath.Join(out, "Movie.mkv"))
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("multipart output content differs: %v", err)
			}
		})
	}
}

func TestProcessRAROldVolumesExtraction(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("old-style"), 200)
	split := len(payload) / 2
	first := rarEntry{name: "Movie.mkv", data: payload[:split], split: 0x0002, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
	second := rarEntry{name: "Movie.mkv", data: payload[split:], split: 0x0001, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
	if err := os.WriteFile(filepath.Join(in, "Release.rar"), rarVolumeNamed([]rarEntry{first}, false, false, false), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "Release.r00"), rarVolumeNamed([]rarEntry{second}, false, true, false), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := Process(context.Background(), in, out, 0, nil)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(files) != 1 || files[0].Name != "Movie.mkv" || files[0].Size != int64(len(payload)) {
		t.Fatalf("files = %+v", files)
	}
	got, err := os.ReadFile(filepath.Join(out, "Movie.mkv"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("old volume output content differs: %v", err)
	}
}

func TestProcessRejectsEncryptedZIP(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	writeZIP(t, filepath.Join(in, "release.zip"), zipEntry{name: "movie.mkv", data: []byte("data"), flags: 0x1})
	if _, err := Process(context.Background(), in, out, 0, nil); err == nil {
		t.Fatal("encrypted archive was accepted")
	}
}

func TestProcessRARRejectsSymlink(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	archive := rarArchive(rarEntry{name: "movie.mkv", data: []byte("target"), attrs: 0xa1ff})
	if err := os.WriteFile(filepath.Join(in, "release.rar"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Process(context.Background(), in, out, 0, nil); err == nil {
		t.Fatal("symlink entry was accepted")
	}
	if _, err := os.Stat(filepath.Join(out, "movie.mkv")); err == nil {
		t.Fatal("symlink entry was written")
	}
}
