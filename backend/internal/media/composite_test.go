package media

import (
	"bytes"
	"context"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestProcessRepairsAndExtractsCompositeJob(t *testing.T) {
	requirePAR2(t)
	in := t.TempDir()
	out := t.TempDir()
	rarPayload := bytes.Repeat([]byte("rar-media"), 20000)
	split := len(rarPayload) / 2
	first := rarEntry{name: "Movie.mkv", data: rarPayload[:split], split: 0x0002, size: uint32(len(rarPayload)), crc: crc32.ChecksumIEEE(rarPayload)}
	second := rarEntry{name: "Movie.mkv", data: rarPayload[split:], split: 0x0001, size: uint32(len(rarPayload)), crc: crc32.ChecksumIEEE(rarPayload)}
	mustWrite(t, filepath.Join(in, "Release.part01.rar"), rarVolume([]rarEntry{first}, false, false))
	mustWrite(t, filepath.Join(in, "Release.part02.rar"), rarVolume([]rarEntry{second}, false, true))
	zipPayload := bytes.Repeat([]byte("zip-media"), 30000)
	writeZIP(t, filepath.Join(in, "Extras.zip"), zipEntry{name: "Extras/clip.mp4", data: zipPayload})
	mustWrite(t, filepath.Join(in, "Bonus.webm"), bytes.Repeat([]byte("bonus"), 1000))
	mustWrite(t, filepath.Join(in, "release.nfo"), []byte("notes"))
	runPAR2Test(t, in, "create", "-q", "-b128", "-r100", "--", "release.par2", "Release.part01.rar", "Release.part02.rar", "Extras.zip", "Bonus.webm")
	f, err := os.OpenFile(filepath.Join(in, "Release.part02.rar"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0x5a}, 300), 500); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Remove(filepath.Join(in, "Extras.zip")); err != nil {
		t.Fatal(err)
	}
	var stages []string
	files, err := Process(context.Background(), in, out, 2, func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("Process: %v (stages %v)", err, stages)
	}
	var names []string
	for _, file := range files {
		names = append(names, file.Name)
	}
	sort.Strings(names)
	want := []string{"Bonus.webm", "Extras/clip.mp4", "Movie.mkv"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v (stages %v)", names, stages)
	}
	got, err := os.ReadFile(filepath.Join(out, "Movie.mkv"))
	if err != nil || !bytes.Equal(got, rarPayload) {
		t.Fatalf("rar output differs: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(out, "Extras", "clip.mp4"))
	if err != nil || !bytes.Equal(got, zipPayload) {
		t.Fatalf("zip output differs: %v", err)
	}
}
