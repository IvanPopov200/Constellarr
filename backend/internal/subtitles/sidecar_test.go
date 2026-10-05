package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseSidecarName(t *testing.T) {
	valid := []struct {
		name     string
		language string
		forced   bool
		hi       bool
		format   Format
		plain    bool
	}{
		{"Movie.2020.en.srt", "en", false, false, FormatSRT, false},
		{"Movie.2020.EN.SRT", "en", false, false, FormatSRT, false},
		{"Movie.2020.pt-BR.forced.srt", "pt-BR", true, false, FormatSRT, false},
		{"Movie.2020.en.hi.vtt", "en", false, true, FormatVTT, false},
		{"Movie.2020.en.sdh.ass", "en", false, true, FormatASS, false},
		{"Movie.2020.eng.forced.hi.ssa", "en", true, true, FormatSSA, false},
		{"Movie.2020.srt", "", false, false, FormatSRT, true},
		{"Movie.2020.forced.srt", "", true, false, FormatSRT, false},
	}
	for _, want := range valid {
		got, ok := ParseSidecarName("Movie.2020.mkv", want.name)
		if !ok {
			t.Fatalf("%s: not recognized", want.name)
		}
		if got.Language != want.language || got.Forced != want.forced || got.HI != want.hi ||
			got.Format != want.format || got.Plain != want.plain {
			t.Fatalf("%s: got %+v", want.name, got)
		}
	}
	invalid := []string{
		"Movie.2020.mkv", "Movie.2020.en.txt", "Movie.2020.notes.srt", "../Movie.2020.en.srt",
		"Movie.2020.en/../x.srt", "Other.Movie.en.srt", "Movie.2020.en.unknown.srt", "",
		"Movie.2020.\x01en.srt",
	}
	for _, name := range invalid {
		if _, ok := ParseSidecarName("Movie.2020.mkv", name); ok {
			t.Fatalf("%q must not parse as a sidecar", name)
		}
	}
}

func TestSidecarFileNameAndLanguageNormalization(t *testing.T) {
	name, err := SidecarFileName("Movies/Movie.2020.mkv", "pt_br", true, true, FormatVTT)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Movie.2020.pt-BR.forced.hi.vtt" {
		t.Fatalf("name = %q", name)
	}
	if _, err := SidecarFileName("Movie.2020.mkv", "english", false, false, FormatSRT); err == nil {
		t.Fatal("invalid language must be rejected")
	}
	for input, want := range map[string]string{
		"EN": "en", "Pt-br": "pt-BR", "pt_br": "pt-BR", "eng": "en",
		"gre": "el", "rum": "ro", "chi": "zh", "deu": "de", "ger": "de", "fre": "fr",
		"kor": "ko", "por": "pt", "spa": "es", "zh-Hans": "zh-Hans", "es-419": "es-419",
	} {
		got, ok := NormalizeLanguage(input)
		if !ok || got != want {
			t.Fatalf("NormalizeLanguage(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"", "english", "und", "zz", "-", "en--US", "en.US", "toolonglanguagecode"} {
		if got, ok := NormalizeLanguage(input); ok {
			t.Fatalf("NormalizeLanguage(%q) = %q, want rejection", input, got)
		}
	}
}

func TestExtractionLanguageSatisfiesWantedVariant(t *testing.T) {
	// An extracted stream tagged "eng" is stored as en, and an SDH file still covers plain en.
	if language, ok := NormalizeLanguage("eng"); !ok || language != "en" {
		t.Fatalf("eng = %q, %v", language, ok)
	}
	if !sidecarSatisfies([]Sidecar{{Language: "en", HI: true}}, LanguagePreference{Code: "en"}) {
		t.Fatal("a hearing-impaired sidecar must satisfy a plain language request")
	}
	if !sidecarSatisfies([]Sidecar{{Language: "en", HI: true}}, LanguagePreference{Code: "en", HI: true}) {
		t.Fatal("a hearing-impaired sidecar must satisfy a HI request")
	}
	if sidecarSatisfies([]Sidecar{{Language: "en"}}, LanguagePreference{Code: "en", HI: true}) {
		t.Fatal("a plain sidecar must not satisfy a HI request")
	}
	if sidecarSatisfies([]Sidecar{{Language: "en", HI: true, Forced: true}}, LanguagePreference{Code: "en"}) {
		t.Fatal("a forced sidecar must not satisfy a full-dialogue request")
	}
}

const (
	nszSRT = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	// A second payload of the same shape but different text.
	nszSRT2 = "1\n00:00:01,000 --> 00:00:02,000\nServus\n"
)

func TestPublishSidecarBacksUpAndVerifies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Film.2020.mkv"), "video")
	ctx := context.Background()
	published, err := PublishSidecar(ctx, PublishTarget{
		RootPath: root, VideoPath: "Film.2020.mkv", Language: "en",
		Format: FormatSRT, Source: "provider", Data: []byte(nszSRT),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Path != "Film.2020.en.srt" {
		t.Fatalf("path = %q", published.Path)
	}
	if data, err := os.ReadFile(filepath.Join(root, "Film.2020.en.srt")); err != nil || string(data) != nszSRT {
		t.Fatalf("published content: %v %q", err, data)
	}
	// Replacing recycles the previous file instead of overwriting it.
	if _, err := PublishSidecar(ctx, PublishTarget{
		RootPath: root, VideoPath: "Film.2020.mkv", Language: "en",
		Format: FormatSRT, Source: "ai", Data: []byte(nszSRT2),
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	recycled, err := os.ReadFile(filepath.Join(root, ".recycle", "Film.2020.en.srt"))
	if err != nil || string(recycled) != nszSRT {
		t.Fatalf("recycled backup: %v %q", err, recycled)
	}
	// Publishing identical bytes must not create a second recycle entry.
	if _, err := PublishSidecar(ctx, PublishTarget{
		RootPath: root, VideoPath: "Film.2020.mkv", Language: "en",
		Format: FormatSRT, Source: "ai", Data: []byte(nszSRT2),
	}); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".recycle", "Film.2020.en (1).srt")); !os.IsNotExist(err) {
		t.Fatal("identical publish must not recycle again")
	}
}

func TestPublishSidecarRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Film.mkv"), "video")
	cases := []PublishTarget{
		{RootPath: root, VideoPath: "../Film.mkv", Language: "en", Data: []byte(nszSRT)},
		{RootPath: root, VideoPath: "Film.mkv", Language: "../../etc/passwd", Data: []byte(nszSRT)},
		{RootPath: root, VideoPath: "Film.mkv", Language: "en", Format: FormatVTT, Data: []byte(nszSRT)},
		{RootPath: root, VideoPath: "Film.mkv", Language: "en", Data: []byte{0xff, 0xfe}},
		{RootPath: root, VideoPath: "Missing.mkv", Language: "en", Data: []byte(nszSRT)},
	}
	for i, target := range cases {
		if _, err := PublishSidecar(context.Background(), target); err == nil {
			t.Fatalf("case %d: expected rejection", i)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".subtitles-") {
			t.Fatalf("temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestPublishSidecarKeepsOriginalWhenRecycleIsFull(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Film.mkv"), "video")
	writeFile(t, filepath.Join(root, "Film.en.srt"), nszSRT)
	if err := os.MkdirAll(filepath.Join(root, ".recycle"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range recycleAttempts("Film.en.srt") {
		writeFile(t, filepath.Join(root, ".recycle", name), "old")
	}
	_, err := PublishSidecar(context.Background(), PublishTarget{
		RootPath: root, VideoPath: "Film.mkv", Language: "en", Data: []byte(nszSRT2),
	})
	if err == nil {
		t.Fatal("expected a conflict when every recycle name is taken")
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "Film.en.srt")); readErr != nil || string(data) != nszSRT {
		t.Fatalf("original subtitle was lost: %v %q", readErr, data)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 3 {
		t.Fatalf("unexpected files left behind: %v", entries)
	}
}

func recycleAttempts(rel string) []string {
	names := []string{rel}
	for i := 1; i < 16; i++ {
		names = append(names, strings.TrimSuffix(rel, ".srt")+" ("+strconv.Itoa(i)+").srt")
	}
	return names
}

func TestListSidecarsFiltersForeignFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Show.S01E01.mkv"), "video")
	for _, name := range []string{"Show.S01E01.en.srt", "Show.S01E01.de.forced.vtt", "Show.S01E01.srt", "Show.S01E02.en.srt", "notes.txt"} {
		writeFile(t, filepath.Join(root, name), nszSRT)
	}
	files, err := ListSidecars(root, "Show.S01E01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("sidecars = %+v", files)
	}
}

func TestMoveSidecarsFollowsVideoRename(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Old.Name.mkv"), "video")
	writeFile(t, filepath.Join(root, "Old.Name.en.srt"), nszSRT)
	writeFile(t, filepath.Join(root, "Old.Name.de.forced.srt"), nszSRT)
	moved, err := MoveSidecars(root, "Old.Name.mkv", "New.Name.mkv")
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(moved) != 2 {
		t.Fatalf("moved = %+v", moved)
	}
	for _, name := range []string{"New.Name.en.srt", "New.Name.de.forced.srt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A destination sidecar blocks the move instead of being replaced.
	writeFile(t, filepath.Join(root, "Third.Name.en.srt"), nszSRT)
	if _, err := MoveSidecars(root, "New.Name.mkv", "Third.Name.mkv"); err == nil {
		t.Fatal("expected a conflict on an existing destination sidecar")
	}
}

func TestValidateSidecarPath(t *testing.T) {
	if err := ValidateSidecarPath("Show/S01/Show.S01E01.mkv", "Show/S01/Show.S01E01.en.srt"); err != nil {
		t.Fatalf("valid sidecar rejected: %v", err)
	}
	invalid := [][2]string{
		{"Show/S01/Show.S01E01.mkv", "Show/S01/Other.S01E01.en.srt"},
		{"Show/S01/Show.S01E01.mkv", "Show/S01/Show.S01E01.en.txt"},
		{"Show/S01/Show.S01E01.mkv", "Show/S02/Show.S01E01.en.srt"},
		{"Show/S01/Show.S01E01.mkv", "../Show.S01E01.en.srt"},
	}
	for _, pair := range invalid {
		if err := ValidateSidecarPath(pair[0], pair[1]); err == nil {
			t.Fatalf("%v must be rejected", pair)
		}
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
