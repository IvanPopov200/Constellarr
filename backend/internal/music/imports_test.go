package music

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		title    string
		format   string
		bitrate  int
		lossless bool
		media    string
	}{
		{"Muse - Absolution (2003) [FLAC] [24bit]", "flac", 0, true, ""},
		{"Artist.Album-[24BIT]-[WEBFLAC]-[2013]", "flac", 0, true, "web"},
		{"Muse - Absolution (2003) [FLAC] [24-96]", "flac", 0, true, ""},
		{"Muse - Absolution [MP3 320] [WEB]", "mp3", 320, false, "web"},
		{"Muse - Absolution [AAC 256]", "aac", 256, false, ""},
		{"Muse - Absolution Vinyl Rip [ALAC]", "alac", 0, true, "vinyl"},
		{"Muse - Absolution [V0]", "mp3", 245, false, ""},
		{"Muse - Absolution [OGG Vorbis 192]", "vorbis", 192, false, ""},
		{"Muse - Absolution", "", 0, false, ""},
		{"Muse - Absolution CD [APE]", "ape", 0, true, "cd"},
	}
	for _, test := range cases {
		info := parseFormat(test.title)
		if info.Format != test.format || info.BitrateKbps != test.bitrate || info.Lossless != test.lossless || info.Media != test.media {
			t.Fatalf("parseFormat(%q) = %+v, want format %q bitrate %d lossless %v media %q",
				test.title, info, test.format, test.bitrate, test.lossless, test.media)
		}
	}
}

func TestEvaluateQualityProfile(t *testing.T) {
	profile := QualityProfile{
		ID: "standard", Name: "Standard", Formats: []string{"flac", "alac", "mp3"},
		MinBitrateKbps: 256, MinMB: 50, Cutoff: "flac", Upgrade: true,
	}
	_ = context.Background()
	if decision := evaluate(profile, "Artist - Album [FLAC]", 500<<20, nil); !decision.Allowed || !decision.Lossless || decision.Rank != 0 {
		t.Fatalf("flac release = %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album [MP3 320]", 120<<20, nil); !decision.Allowed || decision.BitrateKbps != 320 {
		t.Fatalf("mp3 320 release = %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album [MP3 128]", 60<<20, nil); decision.Allowed || !containsReason(decision, "bitrate") {
		t.Fatalf("low bitrate release = %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album [AAC 320]", 100<<20, nil); decision.Allowed {
		t.Fatalf("unlisted format was allowed: %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album", 100<<20, nil); decision.Allowed {
		t.Fatalf("unidentified format was allowed by a listed profile: %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album [FLAC]", 10<<20, nil); decision.Allowed {
		t.Fatalf("tiny release passed a MinMB profile: %+v", decision)
	}
	losslessOnly := QualityProfile{ID: "lossless", Name: "Lossless", Formats: []string{"flac", "alac"}, LosslessOnly: true, Cutoff: "flac"}
	if decision := evaluate(losslessOnly, "Artist - Album [MP3 320]", 100<<20, nil); decision.Allowed || !containsReason(decision, "lossless") {
		t.Fatalf("lossy release passed a lossless-only profile: %+v", decision)
	}
	current := &currentQuality{Format: "mp3", BitrateKbps: 320, Score: 120}
	if decision := evaluate(profile, "Artist - Album [MP3 320]", 120<<20, current); decision.Allowed {
		t.Fatalf("same-quality release was allowed past the cutoff: %+v", decision)
	}
	if decision := evaluate(profile, "Artist - Album [FLAC]", 500<<20, current); !decision.Allowed || !decision.Upgrade {
		t.Fatalf("flac upgrade was rejected: %+v", decision)
	}
	noUpgrade := profile
	noUpgrade.Upgrade = false
	if decision := evaluate(noUpgrade, "Artist - Album [FLAC]", 500<<20, current); decision.Allowed || !containsReason(decision, "upgrades") {
		t.Fatalf("upgrade was allowed without profile support: %+v", decision)
	}
}

func containsReason(decision Decision, fragment string) bool {
	for _, reason := range decision.Reasons {
		if strings.Contains(reason, fragment) {
			return true
		}
	}
	return false
}

func TestReleaseMatchesAlbum(t *testing.T) {
	album := Album{ID: "a", Title: "Absolution", ArtistName: "Muse", Year: 2003}
	if !releaseMatchesAlbum(album, Release{Title: "Muse - Absolution (2003) [FLAC]"}) {
		t.Fatal("matching release was rejected")
	}
	if !releaseMatchesAlbum(Album{Title: "Absolution"}, Release{Title: "[FLAC] Absolution 2003"}) {
		t.Fatal("title-only match failed")
	}
	if releaseMatchesAlbum(album, Release{Title: "Other Band - Absolution (2003) [FLAC]"}) {
		t.Fatal("release by a different artist matched")
	}
	if releaseMatchesAlbum(album, Release{Title: "Muse - Absolution (2019) [FLAC]", Year: 2019}) {
		t.Fatal("release with a conflicting year matched")
	}
	if releaseMatchesAlbum(album, Release{Title: "Muse - Hullabaloo (2003) [FLAC]"}) {
		t.Fatal("release with another album title matched")
	}
}

func TestReleaseMatchesAlbumKeepsAlternateRecordingsSeparate(t *testing.T) {
	original := Album{Title: "Random Access Memories", ArtistName: "Daft Punk"}
	for _, variant := range []string{"Drumless", "Instrumental", "Karaoke"} {
		release := Release{Title: "Daft Punk-Random Access Memories-" + variant + " Edition-FLAC"}
		if releaseMatchesAlbum(original, release) {
			t.Fatalf("%s edition matched the original album", variant)
		}
		alternate := original
		alternate.Title += " (" + variant + " Edition)"
		if !releaseMatchesAlbum(alternate, release) {
			t.Fatalf("%s edition did not match its own album", variant)
		}
	}
}

func TestMusicTemplates(t *testing.T) {
	cfg := Config{FolderTemplate: "{artist}/{album} ({year})", FileTemplate: "{track:02} {title}"}
	album := Album{Title: "Absolution", ArtistName: "AC/DC", Year: 2003, MusicBrainzID: testGroupID}
	artist := Artist{Name: "AC/DC", MusicBrainzID: testMBID}
	values := albumTokenValues(album, artist)
	folder, err := musicFolderRel(cfg, values)
	if err != nil {
		t.Fatalf("musicFolderRel: %v", err)
	}
	if folder != "AC DC/Absolution (2003)" {
		t.Fatalf("folder = %q", folder)
	}
	assignment := trackAssignment{number: 3, title: "Fury: Live", disc: 1}
	assignment.track = Track{Title: "Fury"}
	assignment.source = &sourceFile{rel: "03 - Fury.flac"}
	name, err := musicFileName(cfg, 1, fileTokenValues(values, assignment), ".flac")
	if err != nil {
		t.Fatalf("musicFileName: %v", err)
	}
	if name != "03 Fury Live.flac" {
		t.Fatalf("file name = %q", name)
	}
	if _, err := musicFileName(Config{FileTemplate: "{unknown}"}, 1, fileTokenValues(values, assignment), ".flac"); !errors.Is(err, ErrTemplate) {
		t.Fatalf("unknown token error = %v", err)
	}
	if _, err := musicFolderRel(Config{FolderTemplate: "{title}"}, values); !errors.Is(err, ErrTemplate) {
		t.Fatalf("folder with a file token error = %v", err)
	}
	if _, err := musicFolderRel(Config{FolderTemplate: "../{album}"}, values); err == nil {
		t.Fatal("folder template escaped the root")
	}
	if got := effectiveFileTemplate(Config{FileTemplate: defaultFileTemplate}, 2); got != multidiscFileTemplate {
		t.Fatalf("multidisc template = %q", got)
	}
	if got := effectiveFileTemplate(Config{FileTemplate: "{title}"}, 2); got != "{title}" {
		t.Fatalf("custom template was replaced: %q", got)
	}
	if err := validateMusicTemplate("{track:02} {title}", true); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	if err := validateMusicTemplate("{track:2x}", true); err == nil {
		t.Fatal("invalid padding was accepted")
	}
}

func TestInferTrackFromNames(t *testing.T) {
	inferred := inferTrack("Muse - Absolution (2003) [FLAC]/CD2/03 - Fury.flac")
	if inferred.artist != "Muse" || inferred.album != "Absolution" || inferred.year != 2003 {
		t.Fatalf("folder identity = %+v", inferred)
	}
	if inferred.disc != 2 || inferred.number != 3 || inferred.title != "Fury" {
		t.Fatalf("track identity = %+v", inferred)
	}
	plain := inferTrack("Artist - Album - 07 - Song.mp3")
	if plain.number != 7 || plain.title != "Song" {
		t.Fatalf("plain track = %+v", plain)
	}
	numberFirst := inferTrack("01. Opening.flac")
	if numberFirst.number != 1 || numberFirst.title != "Opening" {
		t.Fatalf("number-first track = %+v", numberFirst)
	}
}

func TestSourceAlbumIdentityRejectsMixedAlbums(t *testing.T) {
	files := []sourceFile{
		{rel: "a/01.flac", info: audioInfo{Tags: map[string]string{"album": "Absolution", "albumartist": "Muse"}}},
		{rel: "a/02.flac", info: audioInfo{Tags: map[string]string{"album": "Absolution", "albumartist": "Muse"}}},
	}
	if _, err := sourceAlbumIdentity(files); err != nil {
		t.Fatalf("consistent files were rejected: %v", err)
	}
	mixed := append(files, sourceFile{rel: "a/03.flac", info: audioInfo{Tags: map[string]string{"album": "Origin of Symmetry", "albumartist": "Muse"}}})
	if _, err := sourceAlbumIdentity(mixed); !errors.Is(err, errAmbiguous) {
		t.Fatalf("mixed albums error = %v, want errAmbiguous", err)
	}
	target := albumIdentity{artist: "Muse", album: "Absolution", year: 2003}
	if !identityMatches(target, albumIdentity{artist: "Muse", album: "Absolution", year: 2004}) {
		t.Fatal("one-year pressing difference was rejected")
	}
	if identityMatches(target, albumIdentity{artist: "Muse", album: "Origin of Symmetry", year: 2001}) {
		t.Fatal("different album was accepted")
	}
}

func TestMatchTracksDetectsAmbiguity(t *testing.T) {
	album := Album{
		ID: "a", Title: "Album", ArtistName: "Artist",
		Tracks: []Track{
			{ID: "t1", Disc: 1, Number: 1, Title: "One"},
			{ID: "t2", Disc: 1, Number: 2, Title: "Two"},
		},
	}
	good := []sourceFile{
		{rel: "01.flac", size: 1, probed: true, info: audioInfo{Format: "flac", Lossless: true, Tags: map[string]string{"track": "1"}}},
		{rel: "02.flac", size: 1, probed: true, info: audioInfo{Format: "flac", Lossless: true, Tags: map[string]string{"track": "2"}}},
	}
	assignments, tracks, err := matchTracks(QualityProfile{}, album, good)
	if err != nil || len(assignments) != 2 || len(tracks) != 2 {
		t.Fatalf("matchTracks = %d assignments, %d tracks, %v", len(assignments), len(tracks), err)
	}
	if assignments[0].track.ID != "t1" || assignments[1].track.ID != "t2" {
		t.Fatalf("tracks were not matched by position: %+v", assignments)
	}
	if assignments[0].format != "flac" || !assignments[0].lossless {
		t.Fatalf("probed format was not used: %+v", assignments[0])
	}

	duplicate := []sourceFile{
		{rel: "01.flac", size: 1, probed: true, info: audioInfo{Tags: map[string]string{"track": "1"}}},
		{rel: "01-copy.flac", size: 1, probed: true, info: audioInfo{Tags: map[string]string{"track": "1"}}},
	}
	if _, _, err := matchTracks(QualityProfile{}, album, duplicate); !errors.Is(err, errAmbiguous) {
		t.Fatalf("duplicate number error = %v, want errAmbiguous", err)
	}

	bonus := append(good, sourceFile{rel: "99 bonus.flac", size: 1, probed: true, info: audioInfo{Format: "mp3", Tags: map[string]string{"title": "Bonus"}}})
	assignments, tracks, err = matchTracks(QualityProfile{}, album, bonus)
	if err != nil || len(assignments) != 3 || len(tracks) != 3 {
		t.Fatalf("bonus track = %d assignments, %d tracks, %v", len(assignments), len(tracks), err)
	}
	if assignments[2].number != 3 || assignments[2].title != "Bonus" {
		t.Fatalf("bonus track was not numbered: %+v", assignments[2])
	}
}

func TestScanAudioSourcesGuards(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "01 - One.flac"), strings.Repeat("a", 64))
	mustWrite(t, filepath.Join(source, "movie.mkv"), strings.Repeat("v", 64))
	mustWrite(t, filepath.Join(source, "cover.jpg"), "image")
	if err := os.MkdirAll(filepath.Join(source, ".recycle"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(source, ".recycle", "old.flac"), "old")
	files, err := scanAudioSources(ctx, source)
	if err != nil || len(files) != 1 || files[0].rel != "01 - One.flac" {
		t.Fatalf("scanAudioSources = %+v, %v", files, err)
	}

	empty := t.TempDir()
	if _, err := scanAudioSources(ctx, empty); !errors.Is(err, errNoAudio) {
		t.Fatalf("empty source error = %v, want errNoAudio", err)
	}

	linkSource := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.flac")
	mustWrite(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(linkSource, "escape.flac")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := scanAudioSources(ctx, linkSource); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("symlink source error = %v, want ErrUnsafe", err)
	}
}

func TestPublishFilesCopiesAndRecycles(t *testing.T) {
	ctx := context.Background()
	rootPath := t.TempDir()
	sourcePath := t.TempDir()
	mustWrite(t, filepath.Join(sourcePath, "01 - First.flac"), strings.Repeat("a", 128))
	mustWrite(t, filepath.Join(sourcePath, "02 - Second.flac"), strings.Repeat("b", 256))
	mustWrite(t, filepath.Join(sourcePath, "01 - First.lrc"), "[00:01.00] First lyric")
	mustWrite(t, filepath.Join(sourcePath, "album.cue"), "FILE \"01 First.flac\" WAVE")
	cfg := Config{ImportMode: importModeCopy, FolderTemplate: "{artist}/{album} ({year})", FileTemplate: "{track:02} {title}"}
	album := Album{ID: "a1", ArtistName: "Artist", Title: "Album", Year: 2001, ProfileID: "standard", RootID: "music"}
	artist := Artist{Name: "Artist"}
	root := RootFolder{ID: "music", Path: rootPath}

	planned := planTestImport(t, cfg, album, artist, sourcePath)
	files, err := publishFiles(ctx, importModeCopy, root, sourcePath, "Artist/Album (2001)", planned, map[string]bool{}, keepPaths(planned))
	if err != nil {
		t.Fatalf("publishFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	first := filepath.Join(rootPath, filepath.FromSlash(files[0].Path))
	if data, err := os.ReadFile(first); err != nil || len(data) != 128 {
		t.Fatalf("published file = %d bytes, %v", len(data), err)
	}
	lyrics, err := os.ReadFile(filepath.Join(rootPath, "Artist", "Album (2001)", "01 First.lrc"))
	if err != nil || !strings.Contains(string(lyrics), "First lyric") {
		t.Fatalf("lyrics sidecar = %q, %v", lyrics, err)
	}
	if cue, err := os.ReadFile(filepath.Join(rootPath, "Artist", "Album (2001)", "album.cue")); err != nil || !strings.Contains(string(cue), "01 First.flac") {
		t.Fatalf("cue sheet = %q, %v", cue, err)
	}

	// A second import of a better copy replaces the owned file and recycles the original.
	mustWrite(t, filepath.Join(sourcePath, "01 - First.flac"), strings.Repeat("c", 512))
	planned = planTestImport(t, cfg, album, artist, sourcePath)
	owned := map[string]bool{files[0].Path: true, files[1].Path: true}
	published, err := publishFiles(ctx, importModeCopy, root, sourcePath, "Artist/Album (2001)", planned, owned, keepPaths(planned))
	if err != nil {
		t.Fatalf("replacement publish: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(published[0].Path))); err != nil || len(data) != 512 {
		t.Fatalf("replacement file = %d bytes, %v", len(data), err)
	}
	recycled := filepath.Join(rootPath, ".recycle", "Artist", "Album (2001)", "01 First.flac")
	if _, err := os.Stat(recycled); err != nil {
		t.Fatalf("replaced original was not recycled: %v", err)
	}
}

func TestPublishFilesRejectsTamperedSource(t *testing.T) {
	ctx := context.Background()
	rootPath := t.TempDir()
	sourcePath := t.TempDir()
	target := filepath.Join(sourcePath, "01 - First.flac")
	mustWrite(t, target, strings.Repeat("a", 128))
	cfg := Config{ImportMode: importModeCopy, FolderTemplate: "{album}", FileTemplate: "{track:02} {title}"}
	album := Album{ID: "a1", ArtistName: "Artist", Title: "Album", Year: 2001, RootID: "music"}
	planned := planTestImport(t, cfg, album, Artist{Name: "Artist"}, sourcePath)
	if err := os.Truncate(target, 32); err != nil {
		t.Fatal(err)
	}
	root := RootFolder{ID: "music", Path: rootPath}
	if _, err := publishFiles(ctx, importModeCopy, root, sourcePath, "Album", planned, map[string]bool{}, keepPaths(planned)); err == nil {
		t.Fatal("a source that changed size was published")
	}
	if _, err := os.Stat(filepath.Join(rootPath, "Album")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback left files behind: %v", err)
	}
}

func TestImportCoverFromSource(t *testing.T) {
	sourcePath := t.TempDir()
	rootPath := t.TempDir()
	mustWrite(t, filepath.Join(sourcePath, "cover.jpg"), "image-data")
	album := Album{ID: "a1", Title: "Album", ArtistName: "Artist", RootID: "music"}
	cover, err := importCover(context.Background(), Config{}, album, RootFolder{ID: "music", Path: rootPath}, sourcePath, "Artist/Album (2001)")
	if err != nil || cover != "Artist/Album (2001)/cover.jpg" {
		t.Fatalf("importCover = %q, %v", cover, err)
	}
	if data, err := os.ReadFile(filepath.Join(rootPath, filepath.FromSlash(cover))); err != nil || string(data) != "image-data" {
		t.Fatalf("cover file = %q, %v", data, err)
	}
}

func TestProbeAudioWithRealFiles(t *testing.T) {
	if !ffprobeAvailable("ffprobe") {
		t.Skip("ffprobe is unavailable")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	writeTestWAV(t, wav)
	info, err := probeAudio(context.Background(), "ffprobe", wav)
	if err != nil {
		t.Fatalf("probeAudio: %v", err)
	}
	if !info.Audio || info.Format != "wav" || !info.Lossless || info.SampleRate != 8000 {
		t.Fatalf("wav probe = %+v", info)
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable; only the generated WAV fixture was checked")
	}
	mp3 := filepath.Join(dir, "tone.mp3")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-c:a", "libmp3lame", "-b:a", "128k", mp3).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot create the mp3 fixture: %v %s", err, out)
	}
	mp3Info, err := probeAudio(context.Background(), "ffprobe", mp3)
	if err != nil {
		t.Fatalf("mp3 probe: %v", err)
	}
	if mp3Info.Format != "mp3" || mp3Info.Lossless || mp3Info.BitrateKbps < 64 {
		t.Fatalf("mp3 probe = %+v", mp3Info)
	}

	video := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=duration=0.5:size=64x64:rate=5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-shortest", "-c:v", "libx264", "-c:a", "aac", video).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot create the video fixture: %v %s", err, out)
	}
	disguised := filepath.Join(dir, "video.mp3")
	if data, err := os.ReadFile(video); err == nil {
		mustWrite(t, disguised, string(data))
	}
	if _, err := probeAudio(context.Background(), "ffprobe", disguised); !errors.Is(err, errNotAudio) {
		t.Fatalf("video with an audio extension error = %v, want errNotAudio", err)
	}
}

func planTestImport(t *testing.T, cfg Config, album Album, artist Artist, sourcePath string) []plannedFile {
	t.Helper()
	sources, err := scanAudioSources(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("scanAudioSources: %v", err)
	}
	assignments, _, err := matchTracks(QualityProfile{}, album, sources)
	if err != nil {
		t.Fatalf("matchTracks: %v", err)
	}
	_, planned, err := planAlbumImport(cfg, album, artist, assignments)
	if err != nil {
		t.Fatalf("planAlbumImport: %v", err)
	}
	return planned
}

func keepPaths(planned []plannedFile) []string {
	paths := make([]string, 0, len(planned))
	for _, plan := range planned {
		paths = append(paths, plan.destRel)
	}
	return paths
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestWAV writes a short silent PCM WAV that ffprobe reports as lossless audio.
func writeTestWAV(t *testing.T, path string) {
	t.Helper()
	const sampleRate = 8000
	samples := sampleRate / 10
	dataSize := samples * 2
	var buffer bytes.Buffer
	buffer.WriteString("RIFF")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(36+dataSize))
	buffer.WriteString("WAVE")
	buffer.WriteString("fmt ")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(2))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(16))
	buffer.WriteString("data")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(dataSize))
	buffer.Write(make([]byte, dataSize))
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
