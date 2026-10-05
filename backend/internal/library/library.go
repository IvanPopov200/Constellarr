// Package library provides safe shared filesystem imports, scans, renames, and Jellyfin sidecars.
package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const (
	ModeCopy = "copy"
	ModeMove = "move"
	ModeLink = "hardlink"

	nfoName    = "movie.nfo"
	tvShowNFO  = "tvshow.nfo"
	recycleDir = ".recycle"
)

var (
	ErrTemplate = errors.New("library: unusable naming template")
	ErrUnsafe   = errors.New("library: unsafe path")
	ErrConflict = errors.New("library: destination already exists")
	ErrSource   = errors.New("library: unusable source file")
	ErrNoMedia  = errors.New("library: no video files to import")
	ErrMode     = errors.New("library: unsupported import mode")
	ErrEpisode  = errors.New("library: invalid episode data")
)

type Source struct {
	Name string
	Size int64
}

type Options struct {
	SourceRoot     string
	Root           string
	FolderTemplate string
	FileTemplate   string
	// Mode is "copy", "move", or "hardlink"; empty means copy.
	Mode     string
	Metadata metadata.Title
	Quality  string
	WriteNFO bool
	// Episode carries TV numbering and sidecars; nil imports a movie.
	Episode *Episode
	// Existing lists owned file paths below Root, relative or absolute.
	Existing []string
}

type File struct {
	// Path is relative to the import root.
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type Candidate struct {
	// Path is relative to the scan root.
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Title   string `json:"title"`
	Year    int    `json:"year"`
	IMDbID  string `json:"imdbId"`
	Quality string `json:"quality"`
}

// Import links or copies every usable video to its rendered destination, archiving replaced owned files.
func Import(ctx context.Context, opts Options, sources []Source) ([]File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := buildPlan(opts, sources)
	if err != nil {
		return nil, err
	}
	srcRoot, err := os.OpenRoot(p.sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: source root: %v", ErrSource, err)
	}
	defer srcRoot.Close()
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, fmt.Errorf("%w: library root: %v", ErrUnsafe, err)
	}
	defer root.Close()

	rec := &rollback{}
	var created []string
	fail := func(err error) ([]File, error) {
		rollbackRun(root, p, rec, created)
		return nil, err
	}

	for _, t := range p.targets {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if t.inPlace {
			continue
		}
		if t.existed {
			sum, err := hashFile(ctx, root, t.destRel)
			if err != nil {
				return fail(err)
			}
			t.destHash = sum
			if t.size == t.destSize {
				sourceSum, err := hashFile(ctx, srcRoot, t.srcRel)
				if err != nil {
					return fail(err)
				}
				if sourceSum == sum {
					continue
				}
			}
			if !t.owned {
				return fail(fmt.Errorf("%w: %s", ErrConflict, t.destRel))
			}
			t.replace = true
		}
		t.publish = true
	}

	for _, t := range p.targets {
		if !t.publish {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := stage(ctx, p, t, root, srcRoot, &created); err != nil {
			return fail(err)
		}
	}

	for _, t := range p.targets {
		if !t.publish {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if t.replace {
			sum, err := hashFile(ctx, root, t.destRel)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				t.replace = false
			case err != nil:
				return fail(err)
			case sum != t.destHash:
				return fail(fmt.Errorf("%w: %s changed during import", ErrConflict, t.destRel))
			default:
				archived, err := archiveInto(root, t.destRel)
				if err != nil {
					return fail(err)
				}
				rec.archives = append(rec.archives, archiveRecord{t.destRel, archived})
			}
		}
		if err := publishAt(root, t.tempRel, t.destRel); err != nil {
			if errors.Is(err, fs.ErrExist) {
				same, adoptErr := adoptExisting(ctx, root, t)
				if adoptErr != nil {
					return fail(adoptErr)
				}
				if same {
					root.Remove(t.tempRel)
					t.tempRel = ""
					continue
				}
				return fail(fmt.Errorf("%w: %s appeared during import", ErrConflict, t.destRel))
			}
			return fail(fmt.Errorf("library: publishing %s: %w", t.destRel, err))
		}
		t.tempRel = ""
		info, err := verifyFile(root, t.destRel, t.size)
		if err != nil {
			return fail(err)
		}
		rec.published = append(rec.published, publishedFile{t.destRel, info})
	}

	for _, sidecar := range p.sidecars {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := publishNFO(root, sidecar.rel, sidecar.data, rec); err != nil {
			return fail(err)
		}
	}

	for _, rel := range p.staleExisting() {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if _, err := root.Lstat(rel); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return fail(err)
		}
		archived, err := archiveInto(root, rel)
		if err != nil {
			return fail(err)
		}
		rec.archives = append(rec.archives, archiveRecord{rel, archived})
	}

	files := make([]File, 0, len(p.targets))
	for _, t := range p.targets {
		if t.auxiliary {
			continue
		}
		files = append(files, File{Path: t.destRel, Size: t.size})
	}
	if p.mode == ModeMove {
		if err := deleteSources(srcRoot, p); err != nil {
			return files, err
		}
	}
	return files, nil
}

// Open returns a file below root, refusing paths that leave the root.
func Open(rootPath, name string) (*os.File, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("%w: root must be configured", ErrUnsafe)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := relWithinRoot(rootPath, name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafe, name)
	}
	return f, nil
}

// Archive moves an owned path below root/.recycle without following symbolic links.
func Archive(rootPath, name string) error {
	if rootPath == "" {
		return fmt.Errorf("%w: root must be configured", ErrUnsafe)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := relWithinRoot(rootPath, name)
	if err != nil {
		return err
	}
	if err := verifyNoSymlinks(root, rel); err != nil {
		return err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return fmt.Errorf("%w: %s cannot be archived", ErrUnsafe, name)
	}
	_, err = archiveInto(root, rel)
	return err
}

// archiveInto moves a file or folder below .recycle without replacing an existing entry.
func archiveInto(root *os.Root, rel string) (string, error) {
	if err := verifyNoSymlinks(root, rel); err != nil {
		return "", err
	}
	if rel == recycleDir || strings.HasPrefix(rel, recycleDir+"/") {
		return "", fmt.Errorf("%w: %s is already archived", ErrUnsafe, rel)
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 16; attempt++ {
		dest := recycleName(rel, info.IsDir(), attempt)
		if dir := path.Dir(dest); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
		}
		err := renameNoReplace(root, rel, dest)
		if err == nil {
			return dest, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: no free recycle name for %s", ErrConflict, rel)
}

func recycleName(rel string, isDir bool, attempt int) string {
	base := path.Join(recycleDir, rel)
	if attempt == 0 {
		return base
	}
	ext := ""
	if !isDir {
		ext = path.Ext(base)
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), attempt, ext)
}

func rollbackRun(root *os.Root, p *plan, rec *rollback, created []string) {
	for i := len(rec.nfoWritten) - 1; i >= 0; i-- {
		removeIfSame(root, rec.nfoWritten[i])
	}
	for i := len(rec.published) - 1; i >= 0; i-- {
		removeIfSame(root, rec.published[i])
	}
	for i := len(rec.archives) - 1; i >= 0; i-- {
		a := rec.archives[i]
		if dir := path.Dir(a.original); dir != "." {
			root.MkdirAll(dir, 0o755)
		}
		restoreArchive(root, a)
	}
	for _, t := range p.targets {
		if t.tempRel != "" {
			root.Remove(t.tempRel)
		}
	}
	for i := len(created) - 1; i >= 0; i-- {
		root.Remove(created[i])
	}
}
