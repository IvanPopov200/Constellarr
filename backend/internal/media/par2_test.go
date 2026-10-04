package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestPAR2NamesAndValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "release.par2")
	unicodeName := par2Packet(par2UniNameType, unicodeNameBody("Movie/movie.mkv"))
	content := append(par2FileDescPacket("../escape.mkv"), par2FileDescPacket("movie.mkv")...)
	content = append(content, unicodeName...)
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := par2Names(file)
	if err != nil {
		t.Fatalf("par2Names: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("names = %q", names)
	}
	want := map[string]bool{"../escape.mkv": false, "movie.mkv": true, "Movie/movie.mkv": true}
	for _, name := range names {
		valid, ok := want[name]
		if !ok {
			t.Fatalf("unexpected name %q", name)
		}
		if safePAR2Name(name) != valid {
			t.Errorf("safePAR2Name(%q) = %v", name, !valid)
		}
	}
	for _, name := range []string{"", ".", "..", "/etc/passwd", `..\escape.mkv`, "C:movie.mkv", "a\x00b", "a\nb"} {
		if safePAR2Name(name) {
			t.Errorf("safePAR2Name(%q) accepted", name)
		}
	}
}

func TestPAR2SetFallsBackToVolume(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.vol00+1.par2"), par2FileDescPacket("movie.mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := par2Set(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set != "release.vol00+1.par2" {
		t.Fatalf("set = %q", set)
	}
}

func TestProcessRejectsUnsafeNameInRecoveryVolume(t *testing.T) {
	base := t.TempDir()
	in := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "release.par2"), par2FileDescPacket("movie.mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "release.vol00+1.par2"), par2FileDescPacket("../escape.mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Process(context.Background(), in, out, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "unusable file names") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "escape") {
		t.Fatalf("error leaks the entry name: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "escape.mkv")); statErr == nil {
		t.Fatal("wrote outside the output directory")
	}
	if _, statErr := os.Stat(filepath.Join(out, "movie.mkv")); statErr == nil {
		t.Fatal("processing continued past the unsafe recovery volume")
	}
}

func TestProcessRejectsUnsafePAR2Names(t *testing.T) {
	base := t.TempDir()
	in := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "release.par2"), par2FileDescPacket("../../escape.mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Process(context.Background(), in, out, 0, nil)
	if err == nil {
		t.Fatal("unsafe PAR2 file name was accepted")
	}
	if strings.Contains(err.Error(), "escape") {
		t.Fatalf("error leaks the entry name: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "escape.mkv")); statErr == nil {
		t.Fatal("wrote outside the output directory")
	}
}

func TestProcessRepairsRenamedPayloads(t *testing.T) {
	requirePAR2(t)
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("renamed-media"), 20000)
	split := len(payload) / 2
	first := rarEntry{name: "Movie.mkv", data: payload[:split], split: 0x0002, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
	second := rarEntry{name: "Movie.mkv", data: payload[split:], split: 0x0001, size: uint32(len(payload)), crc: crc32.ChecksumIEEE(payload)}
	obfuscated := []string{"randomtoken.part1.rar", "randomtoken.part2.rar"}
	mustWrite(t, filepath.Join(in, obfuscated[0]), rarVolume([]rarEntry{first}, false, false))
	mustWrite(t, filepath.Join(in, obfuscated[1]), rarVolume([]rarEntry{second}, false, true))
	runPAR2Test(t, in, "create", "-q", "-b64", "-r20", "--", "release.par2", obfuscated[0], obfuscated[1])
	subject := []string{"ReadableMovie.part01.rar", "ReadableMovie.part02.rar"}
	for i, name := range obfuscated {
		if err := os.Rename(filepath.Join(in, name), filepath.Join(in, subject[i])); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := par2Candidates(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(candidates, ",") != strings.Join(subject, ",") {
		t.Fatalf("candidates = %v", candidates)
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
		t.Fatalf("output content differs: %v", err)
	}
	for _, name := range obfuscated {
		if _, err := os.Stat(filepath.Join(in, name)); err != nil {
			t.Fatalf("PAR2 did not restore %s: %v", name, err)
		}
	}
}

func TestProcessWithoutPAR2Tool(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(in, "release.par2"), par2FileDescPacket("movie.mkv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	_, err := Process(context.Background(), in, out, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "par2") {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessPAR2Repair(t *testing.T) {
	requirePAR2(t)
	in := t.TempDir()
	out := t.TempDir()
	payload := make([]byte, 256<<10)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	runPAR2Test(t, in, "create", "-q", "-b64", "-r20", "--", "movie.par2", "movie.mkv")
	f, err := os.OpenFile(filepath.Join(in, "movie.mkv"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xff}, 4096), 8192); err != nil {
		t.Fatal(err)
	}
	f.Close()
	corrupted, err := os.ReadFile(filepath.Join(in, "movie.mkv"))
	if err != nil || bytes.Equal(corrupted, payload) {
		t.Fatalf("payload was not damaged: %v", err)
	}

	var stages []string
	files, err := Process(context.Background(), in, out, 1, func(s string) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(files) != 1 || files[0].Name != "movie.mkv" || files[0].Size != int64(len(payload)) {
		t.Fatalf("files = %+v", files)
	}
	if !strings.Contains(strings.Join(stages, ","), stageRepairing) {
		t.Fatalf("stages = %v", stages)
	}
	repaired, err := os.ReadFile(filepath.Join(in, "movie.mkv"))
	if err != nil || !bytes.Equal(repaired, payload) {
		t.Fatalf("input payload was not repaired: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(out, "movie.mkv"))
	if err != nil || !bytes.Equal(copied, payload) {
		t.Fatalf("output payload differs: %v", err)
	}
}

func TestProcessPAR2VerifiedWithoutRepair(t *testing.T) {
	requirePAR2(t)
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("verified"), 1000)
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	runPAR2Test(t, in, "create", "-q", "-b64", "-r20", "--", "movie.par2", "movie.mkv")
	var stages []string
	if _, err := Process(context.Background(), in, out, 0, func(s string) { stages = append(stages, s) }); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if strings.Contains(strings.Join(stages, ","), stageRepairing) {
		t.Fatalf("unexpected repair stage: %v", stages)
	}
}

func TestProcessPAR2RepairNotPossible(t *testing.T) {
	requirePAR2(t)
	in := t.TempDir()
	out := t.TempDir()
	payload := bytes.Repeat([]byte("lost"), 10<<10)
	if err := os.WriteFile(filepath.Join(in, "movie.mkv"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	runPAR2Test(t, in, "create", "-q", "-b16", "-r5", "--", "movie.par2", "movie.mkv")
	for _, entry := range mustReadDir(t, in) {
		if strings.HasSuffix(entry, ".par2") && entry != "movie.par2" {
			if err := os.Remove(filepath.Join(in, entry)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Remove(filepath.Join(in, "movie.mkv")); err != nil {
		t.Fatal(err)
	}
	_, err := Process(context.Background(), in, out, 4, nil)
	if err == nil || !strings.Contains(err.Error(), "could not repair") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "movie.mkv")); statErr == nil {
		t.Fatal("missing media was reported as extracted")
	}
}

func unicodeNameBody(name string) []byte {
	body := make([]byte, 32, 32+len(name)*2)
	copy(body[0:16], "file-id-00000001")
	for _, unit := range utf16.Encode([]rune(name)) {
		body = binary.LittleEndian.AppendUint16(body, unit)
	}
	return body
}

func requirePAR2(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("par2"); err != nil {
		t.Skip("par2cmdline is not installed")
	}
}

func runPAR2Test(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("par2", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("par2 %v: %v: %s", args, err, out)
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
