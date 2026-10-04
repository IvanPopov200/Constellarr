package media

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	stageVerifying  = "verifying"
	stageRepairing  = "repairing"
	stageExtracting = "extracting"

	maxFileBytes  = 100 << 30
	maxTotalBytes = 100 << 30
)

type File struct {
	Name string
	Size int64
}

func Process(ctx context.Context, inputDir, outputDir string, missingSegments int, setStage func(string)) ([]File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	in, err := filepath.Abs(inputDir)
	if err != nil {
		return nil, errors.New("download input directory is invalid")
	}
	out, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, errors.New("download output directory is invalid")
	}
	if info, statErr := os.Stat(in); statErr != nil || !info.IsDir() {
		return nil, errors.New("download input directory is not available")
	}
	if within(in, out) {
		return nil, errors.New("download output directory must not be inside the input directory")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, errors.New("download output directory could not be created")
	}
	inRoot, err := os.OpenRoot(in)
	if err != nil {
		return nil, errors.New("download input directory could not be opened")
	}
	defer inRoot.Close()

	stage(setStage, stageVerifying)
	if err := verifyAndRepair(ctx, in, missingSegments, setStage); err != nil {
		return nil, err
	}
	stage(setStage, stageExtracting)
	files, err := extract(ctx, inRoot, out)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no playable media found in the download")
	}
	return files, nil
}

func stage(set func(string), name string) {
	if set != nil {
		set(name)
	}
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

var mediaExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".mov":  true,
	".m4v":  true,
	".webm": true,
	".mpeg": true,
	".mpg":  true,
	".ts":   true,
	".wmv":  true,
}

func isMedia(name string) bool {
	return mediaExtensions[strings.ToLower(path.Ext(name))]
}
