package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInferName(t *testing.T) {
	cases := []struct {
		stem  string
		title string
		year  int
		imdb  string
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-GRP", "The Matrix", 1999, ""},
		{"Blade Runner 2049 (2017)", "Blade Runner 2049", 2017, ""},
		{"2012.2009.1080p", "2012", 2009, ""},
		{"Some.Movie.2019.2160p.WEB-DL.DDP5.1.HDR.x265-GRP", "Some Movie", 2019, ""},
		{"Heat.1995.tt0113277", "Heat", 1995, "tt0113277"},
		{"No.Year.Here", "No Year Here", 0, ""},
		{"Movie (2020) [tt1234567]", "Movie", 2020, "tt1234567"},
	}
	for _, tc := range cases {
		title, year, imdb := inferName(tc.stem)
		if title != tc.title || year != tc.year || imdb != tc.imdb {
			t.Errorf("inferName(%q) = (%q, %d, %q), want (%q, %d, %q)",
				tc.stem, title, year, imdb, tc.title, tc.year, tc.imdb)
		}
	}
}

func TestScanFindsVideosAndIgnoresUnusablePaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Movies", "The Matrix (1999)", "The Matrix (1999).mkv"), "matrix")
	writeFile(t, filepath.Join(root, "Movies", "The Matrix (1999)", "sample.mkv"), "sample")
	writeFile(t, filepath.Join(root, "Movies", "The Matrix (1999)", ".recycle", "old.mkv"), "old")
	writeFile(t, filepath.Join(root, "Movies", "Temp", "draft.mkv"), "draft")
	writeFile(t, filepath.Join(root, "Movies", "Blade Runner 2049 (2017)", "Blade Runner 2049 - part1.mkv"), "part-one")
	writeFile(t, filepath.Join(root, "Movies", "Blade Runner 2049 (2017)", "Blade Runner 2049 - part2.mkv"), "part-two")
	writeFile(t, filepath.Join(root, "Movies", "Some.Movie.2010.1080p.BluRay.x264-GRP.mkv"), "some")
	writeFile(t, filepath.Join(root, "Movies", "Heat (1995)", "Heat.mkv"), "heat")

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.mkv"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(root, "Movies", "link.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Movies", "Linked")); err != nil {
		t.Fatal(err)
	}

	candidates, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byPath := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		byPath[candidate.Path] = candidate
	}
	if len(candidates) != 4 {
		t.Fatalf("candidates = %+v", candidates)
	}

	matrix, ok := byPath["Movies/The Matrix (1999)/The Matrix (1999).mkv"]
	if !ok || matrix.Title != "The Matrix" || matrix.Year != 1999 {
		t.Fatalf("matrix candidate = %+v", matrix)
	}

	blade, ok := byPath["Movies/Blade Runner 2049 (2017)/Blade Runner 2049 - part1.mkv"]
	if !ok || blade.Title != "Blade Runner 2049" || blade.Year != 2017 || blade.Size != int64(len("part-one")+len("part-two")) {
		t.Fatalf("multipart candidate = %+v", blade)
	}

	some, ok := byPath["Movies/Some.Movie.2010.1080p.BluRay.x264-GRP.mkv"]
	if !ok || some.Title != "Some Movie" || some.Year != 2010 || some.Quality == "" {
		t.Fatalf("release candidate = %+v", some)
	}

	heat, ok := byPath["Movies/Heat (1995)/Heat.mkv"]
	if !ok || heat.Title != "Heat" || heat.Year != 1995 {
		t.Fatalf("folder-named candidate = %+v", heat)
	}
}

func TestScanCancellationAndBadRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, t.TempDir()); err == nil {
		t.Fatal("canceled scan should fail")
	}
	if _, err := Scan(context.Background(), ""); err == nil {
		t.Fatal("empty root should fail")
	}
	if _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root should fail")
	}
}
