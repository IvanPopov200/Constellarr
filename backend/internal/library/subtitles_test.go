package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

func TestSubtitleSidecarsFollowVideoImportsAndRename(t *testing.T) {
	for _, mode := range []string{ModeCopy, ModeLink, ModeMove} {
		t.Run(mode, func(t *testing.T) {
			src, lib := t.TempDir(), t.TempDir()
			writeFile(t, filepath.Join(src, "release.mkv"), "video")
			for _, suffix := range []string{".en.srt", ".bg.forced.ass", ".idx", ".sub"} {
				writeFile(t, filepath.Join(src, "release"+suffix), "subtitle"+suffix)
			}
			writeFile(t, filepath.Join(src, "release-other.en.srt"), "unrelated")
			opts := Options{SourceRoot: src, Root: lib, Mode: mode, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
			files, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}})
			if err != nil || len(files) != 1 {
				t.Fatalf("import = %+v, %v", files, err)
			}
			stem := strings.TrimSuffix(files[0].Path, ".mkv")
			for _, suffix := range []string{".en.srt", ".bg.forced.ass", ".idx", ".sub"} {
				if got := readFile(t, filepath.Join(lib, stem+suffix)); got != "subtitle"+suffix {
					t.Fatalf("sidecar = %q", got)
				}
				if mode == ModeMove {
					mustNotExist(t, filepath.Join(src, "release"+suffix))
				} else {
					readFile(t, filepath.Join(src, "release"+suffix))
				}
			}
			readFile(t, filepath.Join(src, "release-other.en.srt"))
			opts.SourceRoot, opts.Mode = lib, ModeMove
			opts.Metadata.Title = "New Name"
			opts.Existing = []string{files[0].Path}
			renamed, err := Import(context.Background(), opts, []Source{{Name: files[0].Path}})
			if err != nil || len(renamed) != 1 {
				t.Fatalf("rename = %+v, %v", renamed, err)
			}
			for _, suffix := range []string{".en.srt", ".bg.forced.ass", ".idx", ".sub"} {
				readFile(t, filepath.Join(lib, strings.TrimSuffix(renamed[0].Path, ".mkv")+suffix))
				mustNotExist(t, filepath.Join(lib, stem+suffix))
			}
		})
	}
}

func TestSubtitleSidecarsPreserveUnchangedVideoAndArchiveOnUpgrade(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "release.mkv"), "original")
	opts := Options{SourceRoot: src, Root: lib, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	files, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}})
	if err != nil {
		t.Fatal(err)
	}
	opts.Existing = []string{files[0].Path}
	subtitle := strings.TrimSuffix(files[0].Path, ".mkv") + ".en.srt"
	writeFile(t, filepath.Join(lib, subtitle), "timed to original")
	if _, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}}); err != nil {
		t.Fatal(err)
	}
	readFile(t, filepath.Join(lib, subtitle))
	writeFile(t, filepath.Join(src, "release.mkv"), "upgraded cut")
	if _, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}}); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(lib, subtitle))
	if got := readFile(t, filepath.Join(lib, ".recycle", subtitle)); got != "timed to original" {
		t.Fatalf("archived subtitle = %q", got)
	}
}

func TestSubtitleImportRollbackAndUnsafeSidecar(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "release.mkv"), "video")
	writeFile(t, filepath.Join(src, "release.en.srt"), "subtitle")
	opts := Options{SourceRoot: src, Root: lib, Mode: ModeMove, Metadata: metadata.Title{Title: "Movie", Year: 2020}}
	originalPublish := publishAt
	t.Cleanup(func() { publishAt = originalPublish })
	publishAt = func(root *os.Root, temporary, destination string) error {
		if strings.HasSuffix(destination, ".srt") {
			return errors.New("injected subtitle failure")
		}
		return originalPublish(root, temporary, destination)
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}}); err == nil {
		t.Fatal("expected subtitle publication failure")
	}
	readFile(t, filepath.Join(src, "release.mkv"))
	readFile(t, filepath.Join(src, "release.en.srt"))
	mustNotExist(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"))
	publishAt = originalPublish
	if err := os.Remove(filepath.Join(src, "release.en.srt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "release.mkv"), filepath.Join(src, "release.en.srt")); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "release.mkv"}}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unsafe subtitle accepted: %v", err)
	}
}
