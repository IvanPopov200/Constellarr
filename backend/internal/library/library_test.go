package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

func writeFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustNotExist(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist: %v", name, err)
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, entry := range list {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestPreviewRendersTokensAndSanitizes(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "release", "movie.mkv"), "payload")
	sources := []Source{{Name: "release/movie.mkv"}}
	opts := Options{
		SourceRoot:     src,
		Root:           lib,
		FolderTemplate: "{title} ({year})",
		FileTemplate:   "{title} ({year}) [{imdbId}] - {quality}",
		Metadata:       metadata.Title{Title: "Some: Movie", Year: 2020, IMDbID: "tt1234567"},
		Quality:        "1080p BluRay",
	}
	files, err := Preview(opts, sources)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := "Some Movie (2020)/Some Movie (2020) [tt1234567] - 1080p BluRay.mkv"
	if len(files) != 1 || files[0].Path != want || files[0].Size != int64(len("payload")) {
		t.Fatalf("preview = %+v, want %s", files, want)
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("preview wrote to the library: %v", got)
	}
}

func TestPreviewRejectsUnsafeTemplates(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "movie.mkv"), "payload")
	sources := []Source{{Name: "movie.mkv"}}
	cases := []struct {
		name   string
		folder string
		file   string
	}{
		{"unknown token", "{unknown} ({year})", "{title}"},
		{"traversal folder", "{title}/../{year}", "{title}"},
		{"absolute folder", "/{title}", "{title}"},
		{"file separator", "{title}", "{title}/x"},
		{"parent folder", "..", "{title}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{
				SourceRoot: src, Root: lib, FolderTemplate: tc.folder, FileTemplate: tc.file,
				Metadata: metadata.Title{Title: "Movie", Year: 2020},
			}
			if _, err := Preview(opts, sources); !errors.Is(err, ErrTemplate) {
				t.Fatalf("err = %v, want ErrTemplate", err)
			}
		})
	}
}

func TestImportRejectsTraversalSourcesAndExisting(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	for _, name := range []string{"../outside.mkv", "/etc/passwd", "dir/../../outside.mkv"} {
		if _, err := Import(context.Background(), opts, []Source{{Name: name}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("source %q: err = %v, want ErrUnsafe", name, err)
		}
	}
	opts.Existing = []string{"../owned.mkv"}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("existing traversal: err = %v, want ErrUnsafe", err)
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("unsafe inputs wrote %v", got)
	}
}

func TestImportAcceptsAbsolutePathsAndOriginalToken(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	source := filepath.Join(src, "release", "Some.Movie.2020.mkv")
	writeFile(t, source, "payload")
	opts := Options{
		SourceRoot: src, Root: lib, FileTemplate: "{original}",
		Metadata: metadata.Title{Title: "Some Movie"},
		Existing: []string{filepath.Join(lib, "Some Movie", "Some.Movie.2020.mkv")},
	}
	files, err := Import(context.Background(), opts, []Source{{Name: source, Size: int64(len("payload"))}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := "Some Movie/Some.Movie.2020.mkv"
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("files = %+v, want %s", files, want)
	}
	if readFile(t, filepath.Join(lib, filepath.FromSlash(want))) != "payload" {
		t.Fatal("content differs")
	}
}

func TestImportSingleFileIgnoresPartToken(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	opts := Options{
		SourceRoot: src, Root: lib, FileTemplate: "{title} - {part}",
		Metadata: metadata.Title{Title: "Movie", Year: 2020},
	}
	files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(files) != 1 || files[0].Path != "Movie (2020)/Movie.mkv" {
		t.Fatalf("files = %+v", files)
	}
}

func TestImportCopyIsDeterministicAndExcludesSamples(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	for name, data := range map[string]string{
		"release/Movie.2020.1080p.mkv":         "video-payload",
		"release/Movie.2020.1080p.sample.mkv":  "sample",
		"release/Movie.2020.1080p-trailer.mkv": "trailer",
		"release/Movie.2020.1080p.nfo":         "sidecar",
		"release/Movie.2020.1080p.rar":         "archive",
		"release/cache.tmp":                    "cache",
	} {
		writeFile(t, filepath.Join(src, filepath.FromSlash(name)), data)
	}
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	all := []Source{
		{Name: "release/Movie.2020.1080p.mkv"},
		{Name: "release/Movie.2020.1080p.sample.mkv"},
		{Name: "release/Movie.2020.1080p-trailer.mkv"},
		{Name: "release/Movie.2020.1080p.nfo"},
		{Name: "release/Movie.2020.1080p.rar"},
		{Name: "release/cache.tmp"},
	}
	preview, err := Preview(opts, all)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	files, err := Import(context.Background(), opts, all)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := "Movie (2020)/Movie (2020).mkv"
	if len(files) != 1 || files[0].Path != want || len(preview) != 1 || preview[0].Path != want {
		t.Fatalf("files = %+v, preview = %+v", files, preview)
	}
	if got := readFile(t, filepath.Join(lib, filepath.FromSlash(want))); got != "video-payload" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "release", "Movie.2020.1080p.mkv")); err != nil {
		t.Fatalf("copy mode removed the source: %v", err)
	}
	if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 || got[0] != "Movie (2020).mkv" {
		t.Fatalf("destination entries = %v", got)
	}
}

func TestImportMultiPartsWithoutCollisions(t *testing.T) {
	t.Run("auto suffix", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.CD1.mkv"), "part-one")
		writeFile(t, filepath.Join(src, "Movie.CD2.mkv"), "part-two")
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		files, err := Import(context.Background(), opts, []Source{{Name: "Movie.CD1.mkv"}, {Name: "Movie.CD2.mkv"}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		want := []string{"Movie (2020)/Movie (2020) - part1.mkv", "Movie (2020)/Movie (2020) - part2.mkv"}
		if len(files) != 2 || files[0].Path != want[0] || files[1].Path != want[1] {
			t.Fatalf("files = %+v", files)
		}
		if readFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020) - part1.mkv")) != "part-one" {
			t.Fatal("part one content differs")
		}
	})
	t.Run("part token", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.part1.mkv"), "one")
		writeFile(t, filepath.Join(src, "Movie.part2.mkv"), "two")
		opts := Options{
			SourceRoot: src, Root: lib, FileTemplate: "{title} - {part}",
			Metadata: metadata.Title{Title: "Movie", Year: 2020},
		}
		files, err := Import(context.Background(), opts, []Source{{Name: "Movie.part2.mkv"}, {Name: "Movie.part1.mkv"}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if files[0].Path != "Movie (2020)/Movie - part1.mkv" || files[1].Path != "Movie (2020)/Movie - part2.mkv" {
			t.Fatalf("files = %+v", files)
		}
	})
	t.Run("same base different extensions", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "one")
		writeFile(t, filepath.Join(src, "Movie.mp4"), "two")
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}, {Name: "Movie.mp4"}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if files[0].Path != "Movie (2020)/Movie (2020) - part1.mkv" || files[1].Path != "Movie (2020)/Movie (2020) - part2.mp4" {
			t.Fatalf("files = %+v", files)
		}
	})
}

func TestImportRefusesUnrelatedExistingMedia(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	writeFile(t, dest, "another-version")
	writeFile(t, filepath.Join(src, "Movie.mkv"), "new-download")
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if readFile(t, dest) != "another-version" {
		t.Fatal("unrelated media was replaced")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
	readFile(t, filepath.Join(src, "Movie.mkv"))
}

func TestImportUpgradeArchivesOwnedVersion(t *testing.T) {
	t.Run("replace archived with collision", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
		writeFile(t, dest, "old-version")
		writeFile(t, filepath.Join(lib, ".recycle", "Movie (2020)", "Movie (2020).mkv"), "even-older")
		writeFile(t, filepath.Join(src, "Movie.mkv"), "brand-new-content")
		opts := Options{
			SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020},
			Existing: []string{"Movie (2020)/Movie (2020).mkv"},
		}
		files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if len(files) != 1 || readFile(t, dest) != "brand-new-content" {
			t.Fatalf("files = %+v, dest = %q", files, readFile(t, dest))
		}
		recycle := filepath.Join(lib, ".recycle", "Movie (2020)")
		if readFile(t, filepath.Join(recycle, "Movie (2020).mkv")) != "even-older" {
			t.Fatal("older recycle entry was overwritten")
		}
		if readFile(t, filepath.Join(recycle, "Movie (2020) (1).mkv")) != "old-version" {
			t.Fatal("replaced version was not archived")
		}
	})
	t.Run("stale path under another name", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(lib, "Old Name (2019)", "Old Name (2019).mkv"), "old-library-file")
		writeFile(t, filepath.Join(src, "New.Name.2020.mkv"), "fresh")
		opts := Options{
			SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "New Name", Year: 2020},
			Existing: []string{"Old Name (2019)/Old Name (2019).mkv"},
		}
		if _, err := Import(context.Background(), opts, []Source{{Name: "New.Name.2020.mkv"}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		mustNotExist(t, filepath.Join(lib, "Old Name (2019)", "Old Name (2019).mkv"))
		if readFile(t, filepath.Join(lib, ".recycle", "Old Name (2019)", "Old Name (2019).mkv")) != "old-library-file" {
			t.Fatal("stale movie-owned path was not archived")
		}
		if readFile(t, filepath.Join(lib, "New Name (2020)", "New Name (2020).mkv")) != "fresh" {
			t.Fatal("new file missing")
		}
	})
}

func TestImportIdempotentRetryByContent(t *testing.T) {
	t.Run("copy retry", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		first, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
		if err != nil {
			t.Fatalf("first import: %v", err)
		}
		opts.Existing = []string{first[0].Path}
		second, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if len(second) != 1 || second[0] != first[0] {
			t.Fatalf("retry = %+v, first = %+v", second, first)
		}
		if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 {
			t.Fatalf("retry added files: %v", got)
		}
		mustNotExist(t, filepath.Join(lib, ".recycle"))
	})
	t.Run("same size different content upgrades", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
		writeFile(t, dest, "AAAA")
		writeFile(t, filepath.Join(src, "Movie.mkv"), "BBBB")
		opts := Options{
			SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020},
			Existing: []string{"Movie (2020)/Movie (2020).mkv"},
		}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		if readFile(t, dest) != "BBBB" {
			t.Fatal("same-size different content was treated as identical")
		}
		if readFile(t, filepath.Join(lib, ".recycle", "Movie (2020)", "Movie (2020).mkv")) != "AAAA" {
			t.Fatal("old content missing from recycle")
		}
	})
	t.Run("same size different content unowned conflicts", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"), "AAAA")
		writeFile(t, filepath.Join(src, "Movie.mkv"), "BBBB")
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
	})
	t.Run("move retry keeps identical destination", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"), "payload")
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("files = %+v", files)
		}
		mustNotExist(t, filepath.Join(src, "Movie.mkv"))
		mustNotExist(t, filepath.Join(lib, ".recycle"))
	})
}

func TestImportMoveDeletesSourceOnlyAfterSuccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		mustNotExist(t, filepath.Join(src, "Movie.mkv"))
		if readFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")) != "payload" {
			t.Fatal("destination missing")
		}
	})
	t.Run("conflict keeps source", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"), "another-version")
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
		readFile(t, filepath.Join(src, "Movie.mkv"))
	})
}

func TestImportPublishFailureRestoresArchiveAndKeepsSources(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	writeFile(t, dest, "old-version")
	writeFile(t, filepath.Join(src, "Movie.mkv"), "brand-new-content")
	opts := Options{
		SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020},
		Existing: []string{"Movie (2020)/Movie (2020).mkv"},
	}
	original := publishAt
	defer func() { publishAt = original }()
	publishAt = func(root *os.Root, tempRel, destRel string) error {
		if destRel == "Movie (2020)/Movie (2020).mkv" {
			return errors.New("injected publish failure")
		}
		return original(root, tempRel, destRel)
	}
	_, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
	publishAt = original
	if err == nil {
		t.Fatal("expected the injected publish failure")
	}
	if readFile(t, dest) != "old-version" {
		t.Fatal("archived original was not restored")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle", "Movie (2020)", "Movie (2020).mkv"))
	readFile(t, filepath.Join(src, "Movie.mkv"))
	if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 || got[0] != "Movie (2020).mkv" {
		t.Fatalf("rollback left files: %v", got)
	}

	files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(files) != 1 || readFile(t, dest) != "brand-new-content" {
		t.Fatalf("retry result = %+v", files)
	}
	if readFile(t, filepath.Join(lib, ".recycle", "Movie (2020)", "Movie (2020).mkv")) != "old-version" {
		t.Fatal("retry did not archive the old version")
	}
	mustNotExist(t, filepath.Join(src, "Movie.mkv"))
}

func TestImportCancellationLeavesNoPartialState(t *testing.T) {
	t.Run("pre-canceled", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(ctx, opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if got := entries(t, lib); len(got) != 0 {
			t.Fatalf("cancellation left %v", got)
		}
		readFile(t, filepath.Join(src, "Movie.mkv"))
	})
	t.Run("mid copy", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancelAfterFirstRead{cancel: cancel}
		err = stageCopy(ctx, root, "temp-file", reader, 1<<20)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Fatalf("canceled copy left %v", got)
		}
	})
	t.Run("truncated source", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		err = stageCopy(context.Background(), root, "temp-file", bytes.NewReader([]byte("short")), 100)
		if !errors.Is(err, ErrSource) {
			t.Fatalf("err = %v, want ErrSource", err)
		}
		if got := entries(t, dir); len(got) != 0 {
			t.Fatalf("failed copy left %v", got)
		}
	})
}

type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterFirstRead) Read(p []byte) (int, error) {
	if r.reads == 0 {
		r.reads++
		r.cancel()
		return copy(p, "data"), nil
	}
	return copy(p, "data"), nil
}

func TestImportHardlinkModeAndCrossDeviceFallback(t *testing.T) {
	t.Run("hardlink shares the inode", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		sourceInfo, err := os.Stat(filepath.Join(src, "Movie.mkv"))
		if err != nil {
			t.Fatal(err)
		}
		destInfo, err := os.Stat(filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(sourceInfo, destInfo) {
			t.Fatal("hardlink mode did not link the source")
		}
	})
	t.Run("cross-device falls back to copy", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
		original := linkatCall
		linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error { return syscall.EXDEV }
		defer func() { linkatCall = original }()
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
		if readFile(t, dest) != "payload" {
			t.Fatal("copy fallback produced wrong content")
		}
		sourceInfo, err := os.Stat(filepath.Join(src, "Movie.mkv"))
		if errors.Is(err, os.ErrNotExist) {
			sourceInfo = nil
		}
		destInfo, err := os.Stat(dest)
		if err != nil {
			t.Fatal(err)
		}
		if sourceInfo != nil && os.SameFile(sourceInfo, destInfo) {
			t.Fatal("copy fallback used a link")
		}
		mustNotExist(t, filepath.Join(src, "Movie.mkv"))
	})
}

func TestImportRejectsSourceSymlinkEscapes(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "real.mkv"), "payload")
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "real.mkv"), filepath.Join(src, "evil.mkv")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, "inside.mkv"), "payload")
	if err := os.Symlink(outside, filepath.Join(src, "linked")); err != nil {
		t.Fatal(err)
	}
	lib := t.TempDir()
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	for _, name := range []string{"evil.mkv", "linked/real.mkv"} {
		if _, err := Import(context.Background(), opts, []Source{{Name: name}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("%s: err = %v, want ErrUnsafe", name, err)
		}
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("unsafe sources imported %v", got)
	}
}

func TestImportRejectsDestinationSymlinkEscapes(t *testing.T) {
	outside := t.TempDir()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	t.Run("folder symlink", func(t *testing.T) {
		lib := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(lib, "Movie (2020)")); err != nil {
			t.Fatal(err)
		}
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v, want ErrUnsafe", err)
		}
		if got := entries(t, outside); len(got) != 0 {
			t.Fatalf("import escaped the root: %v", got)
		}
	})
	t.Run("file symlink", func(t *testing.T) {
		lib := t.TempDir()
		writeFile(t, filepath.Join(outside, "target.mkv"), "outside")
		writeFile(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"), "placeholder")
		if err := os.Remove(filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "target.mkv"), filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")); err != nil {
			t.Fatal(err)
		}
		opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v, want ErrUnsafe", err)
		}
		if readFile(t, filepath.Join(outside, "target.mkv")) != "outside" {
			t.Fatal("destination symlink target was modified")
		}
	})
}

func TestImportReorganizationInPlace(t *testing.T) {
	t.Run("move", func(t *testing.T) {
		root := t.TempDir()
		oldRel := "Old Name (2020)/Old Name (2020).mkv"
		writeFile(t, filepath.Join(root, filepath.FromSlash(oldRel)), "library-content")
		opts := Options{
			SourceRoot: root, Root: root, Mode: ModeMove,
			Metadata: metadata.Title{Title: "New Name", Year: 2020},
			Existing: []string{oldRel},
		}
		files, err := Import(context.Background(), opts, []Source{{Name: oldRel}})
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		want := "New Name (2020)/New Name (2020).mkv"
		if len(files) != 1 || files[0].Path != want {
			t.Fatalf("files = %+v", files)
		}
		if readFile(t, filepath.Join(root, filepath.FromSlash(want))) != "library-content" {
			t.Fatal("renamed file content differs")
		}
		mustNotExist(t, filepath.Join(root, filepath.FromSlash(oldRel)))
		if readFile(t, filepath.Join(root, ".recycle", filepath.FromSlash(oldRel))) != "library-content" {
			t.Fatal("old name was not archived")
		}
	})
	t.Run("copy keeps the old version in recycle", func(t *testing.T) {
		root := t.TempDir()
		oldRel := "Old Name (2020)/Old Name (2020).mkv"
		writeFile(t, filepath.Join(root, filepath.FromSlash(oldRel)), "library-content")
		opts := Options{
			SourceRoot: root, Root: root, Mode: ModeCopy,
			Metadata: metadata.Title{Title: "New Name", Year: 2020},
			Existing: []string{oldRel},
		}
		if _, err := Import(context.Background(), opts, []Source{{Name: oldRel}}); err != nil {
			t.Fatalf("Import: %v", err)
		}
		if readFile(t, filepath.Join(root, ".recycle", filepath.FromSlash(oldRel))) != "library-content" {
			t.Fatal("copy mode did not archive the old version")
		}
	})
}

func TestImportKeepsUnknownFilesIntact(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	writeFile(t, filepath.Join(lib, "Movie (2020)", "poster.jpg"), "poster")
	writeFile(t, filepath.Join(lib, "Movie (2020)", "other.mkv"), "other")
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if readFile(t, filepath.Join(lib, "Movie (2020)", "poster.jpg")) != "poster" {
		t.Fatal("unknown poster was touched")
	}
	if readFile(t, filepath.Join(lib, "Movie (2020)", "other.mkv")) != "other" {
		t.Fatal("unknown media was touched")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
}

func TestOpenContainment(t *testing.T) {
	root := t.TempDir()
	rel := "Movie (2020)/Movie (2020).mkv"
	writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), "payload")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.mkv"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(root, "escape.mkv")); err != nil {
		t.Fatal(err)
	}
	file, err := Open(root, rel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, err := os.ReadFile(file.Name())
	file.Close()
	if err != nil || string(data) != "payload" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if _, err := Open(root, filepath.Join(root, rel)); err != nil {
		t.Fatalf("absolute inside root: %v", err)
	}
	for _, name := range []string{"../secret.mkv", filepath.Join(outside, "secret.mkv"), "escape.mkv", "Movie (2020)"} {
		if _, err := Open(root, name); err == nil {
			t.Fatalf("Open(%q) should fail", name)
		}
	}
}

func TestArchiveCollisionsAndEscapeChecks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Movie (2020)", "Movie (2020).mkv"), "one")
	if err := Archive(root, "Movie (2020)/Movie (2020).mkv"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	writeFile(t, filepath.Join(root, "Movie (2020)", "Movie (2020).mkv"), "two")
	if err := Archive(root, "Movie (2020)/Movie (2020).mkv"); err != nil {
		t.Fatalf("second Archive: %v", err)
	}
	if readFile(t, filepath.Join(root, ".recycle", "Movie (2020)", "Movie (2020).mkv")) != "one" {
		t.Fatal("first archive differs")
	}
	if readFile(t, filepath.Join(root, ".recycle", "Movie (2020)", "Movie (2020) (1).mkv")) != "two" {
		t.Fatal("collision archive differs")
	}
	writeFile(t, filepath.Join(root, "Old Movie", "extra.mkv"), "extra")
	if err := Archive(root, "Old Movie"); err != nil {
		t.Fatalf("directory Archive: %v", err)
	}
	if readFile(t, filepath.Join(root, ".recycle", "Old Movie", "extra.mkv")) != "extra" {
		t.Fatal("archived directory differs")
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.mkv"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(root, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret.mkv", filepath.Join(outside, "secret.mkv"), "link.mkv", "linked/secret.mkv"} {
		if err := Archive(root, name); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("Archive(%q) err = %v, want ErrUnsafe", name, err)
		}
	}
}

func TestImportExistingFolderDoesNotAuthorizeChildren(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	writeFile(t, dest, "another-version")
	writeFile(t, filepath.Join(src, "Movie.mkv"), "new-download")
	opts := Options{
		SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020},
		Existing: []string{"Movie (2020)"},
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if readFile(t, dest) != "another-version" {
		t.Fatal("folder ownership authorized overwriting a child file")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
}

func TestImportRejectsSubstitutedHardlink(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "real-source")
	writeFile(t, filepath.Join(src, "decoy.mkv"), "decoy-content!")
	original := linkatCall
	defer func() { linkatCall = original }()
	linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
		return original(oldDir, "decoy.mkv", newDir, newName)
	}
	opts := Options{SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	if readFile(t, dest) != "real-source" {
		t.Fatal("a substituted hardlink target was published")
	}
	if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 {
		t.Fatalf("destination entries = %v", got)
	}
}

func TestImportDestSwapCannotWriteOutsideRoot(t *testing.T) {
	src, lib, outside := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	original := linkatCall
	defer func() { linkatCall = original }()
	linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
		folder := filepath.Join(lib, "Movie (2020)")
		if err := os.Rename(folder, folder+".swapped"); err != nil {
			return err
		}
		if err := os.Symlink(outside, folder); err != nil {
			return err
		}
		return original(oldDir, oldName, newDir, newName)
	}
	opts := Options{SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err == nil {
		t.Fatal("escaping destination swap must fail the import")
	}
	if got := entries(t, outside); len(got) != 0 {
		t.Fatalf("import wrote outside the root: %v", got)
	}
	if got := entries(t, filepath.Join(lib, "Movie (2020).swapped")); len(got) != 0 {
		t.Fatalf("pinned directory cleanup left files: %v", got)
	}
	readFile(t, filepath.Join(src, "Movie.mkv"))
}

func TestImportPublishRacePreservesConcurrentArrival(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "download-content")
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	original := linkatCall
	defer func() { linkatCall = original }()
	linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
		if err := original(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, []byte("concurrent-arrival"), 0o644)
	}
	opts := Options{SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if readFile(t, dest) != "concurrent-arrival" {
		t.Fatal("concurrent arrival was clobbered")
	}
	readFile(t, filepath.Join(src, "Movie.mkv"))
	if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 {
		t.Fatalf("publish race left files: %v", got)
	}
}

func TestImportAdoptsIdenticalConcurrentArrival(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "download-content")
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	original := linkatCall
	defer func() { linkatCall = original }()
	linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
		if err := original(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, []byte("download-content"), 0o644)
	}
	opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	files, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(files) != 1 || readFile(t, dest) != "download-content" {
		t.Fatalf("files = %+v", files)
	}
	mustNotExist(t, filepath.Join(src, "Movie.mkv"))
	if got := entries(t, filepath.Join(lib, "Movie (2020)")); len(got) != 1 {
		t.Fatalf("adopted arrival left files: %v", got)
	}
}

func TestImportUpgradeDetectsDestinationChange(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	writeFile(t, dest, "old-version")
	writeFile(t, filepath.Join(src, "Movie.mkv"), "brand-new-content")
	original := linkatCall
	defer func() { linkatCall = original }()
	linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
		if err := original(oldDir, oldName, newDir, newName); err != nil {
			return err
		}
		return os.WriteFile(dest, []byte("intruder-version"), 0o644)
	}
	opts := Options{
		SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020},
		Existing: []string{"Movie (2020)/Movie (2020).mkv"},
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if readFile(t, dest) != "intruder-version" {
		t.Fatal("destination changed after planning was overwritten")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
	readFile(t, filepath.Join(src, "Movie.mkv"))
}

func TestImportRollbackKeepsConcurrentArrivalInRecycle(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
	writeFile(t, dest, "old-version")
	writeFile(t, filepath.Join(src, "Movie.mkv"), "brand-new-content")
	originalPublish := publishAt
	defer func() { publishAt = originalPublish }()
	publishAt = func(root *os.Root, tempRel, destRel string) error {
		if destRel == "Movie (2020)/Movie (2020).mkv" {
			file, err := root.OpenFile(destRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			file.Write([]byte("concurrent-arrival"))
			file.Close()
			return errors.New("injected publish failure")
		}
		return originalPublish(root, tempRel, destRel)
	}
	opts := Options{
		SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020},
		Existing: []string{"Movie (2020)/Movie (2020).mkv"},
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err == nil {
		t.Fatal("expected the injected publish failure")
	}
	if readFile(t, dest) != "concurrent-arrival" {
		t.Fatal("rollback clobbered a concurrent arrival")
	}
	if readFile(t, filepath.Join(lib, ".recycle", "Movie (2020)", "Movie (2020).mkv")) != "old-version" {
		t.Fatal("rollback lost the archived original")
	}
	readFile(t, filepath.Join(src, "Movie.mkv"))
}

func TestImportNoReplaceUnsupportedFallsBackWithoutClobber(t *testing.T) {
	t.Run("publish falls back to link", func(t *testing.T) {
		src, lib := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(src, "Movie.mkv"), "download-content")
		dest := filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv")
		originalRename, originalLink := renameatNoReplace, linkatCall
		defer func() { renameatNoReplace, linkatCall = originalRename, originalLink }()
		renameatNoReplace = func(root *os.Root, oldRel, newRel string) error { return errNoReplaceUnsupported }
		linkatCall = func(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
			if err := originalLink(oldDir, oldName, newDir, newName); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dest, []byte("concurrent-arrival"), 0o644)
		}
		opts := Options{SourceRoot: src, Root: lib, Mode: ModeLink, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
		if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
		if readFile(t, dest) != "concurrent-arrival" {
			t.Fatal("no-replace fallback clobbered a concurrent arrival")
		}
		readFile(t, filepath.Join(src, "Movie.mkv"))
	})
	t.Run("directory archive fails clearly", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "Old (2020)", "extra.mkv"), "extra")
		writeFile(t, filepath.Join(root, ".recycle", "Old (2020)", "extra.mkv"), "older")
		original := renameatNoReplace
		defer func() { renameatNoReplace = original }()
		renameatNoReplace = func(root *os.Root, oldRel, newRel string) error { return errNoReplaceUnsupported }
		if err := Archive(root, "Old (2020)"); !errors.Is(err, errNoReplaceUnsupported) {
			t.Fatalf("err = %v, want errNoReplaceUnsupported", err)
		}
		if readFile(t, filepath.Join(root, "Old (2020)", "extra.mkv")) != "extra" {
			t.Fatal("unsupported archive moved the original")
		}
		if readFile(t, filepath.Join(root, ".recycle", "Old (2020)", "extra.mkv")) != "older" {
			t.Fatal("unsupported archive clobbered a recycle entry")
		}
	})
}
