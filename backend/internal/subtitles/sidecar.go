package subtitles

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/language"

	"github.com/IvanPopov200/Constellarr/backend/internal/library"
)

var (
	languagePattern = regexp.MustCompile(`^[a-z]{2,3}([-_][a-z0-9]{2,8}){0,2}$`)
	sidecarFormats  = map[string]Format{".srt": FormatSRT, ".vtt": FormatVTT, ".ass": FormatASS, ".ssa": FormatSSA}
	flagAliases     = map[string]string{"forced": "forced", "hi": "hi", "sdh": "hi"}
	recycleDir      = ".recycle"
	maxDirEntries   = 4096
)

// SidecarName is a parsed subtitle sidecar file name.
type SidecarName struct {
	Language string
	Forced   bool
	HI       bool
	Format   Format
	// Plain marks a sidecar without a language suffix such as video.srt.
	Plain bool
}

// SidecarFile is one sidecar found next to a video.
type SidecarFile struct {
	Name string
	Size int64
	SidecarName
}

// canonicalLanguagePattern accepts the BCP-47 shapes this service stores in file names.
var canonicalLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,2}$`)

// NormalizeLanguage maps ISO 639-2/3 and bibliographic aliases (eng, gre, rum, chi) to BCP-47, keeping region and script.
func NormalizeLanguage(code string) (string, bool) {
	code = strings.TrimSpace(strings.ReplaceAll(code, "_", "-"))
	if code == "" || len(code) > maxLanguageBytes || !languagePattern.MatchString(strings.ToLower(code)) {
		return "", false
	}
	tag, err := language.Parse(code)
	if err != nil {
		return "", false
	}
	canonical := tag.String()
	base, _ := tag.Base()
	// "und" is the parser's fallback for unknown codes and must never name a sidecar.
	if canonical == "und" || strings.HasPrefix(canonical, "und-") || base.String() == "" || base.String() == "und" {
		return "", false
	}
	if !canonicalLanguagePattern.MatchString(canonical) || len(canonical) > maxLanguageBytes {
		return "", false
	}
	return canonical, true
}

// languageMatches reports whether a sidecar language satisfies a wanted language.
func languageMatches(have, want string) bool {
	haveBase, haveRegion, _ := strings.Cut(strings.ToLower(have), "-")
	wantBase, wantRegion, _ := strings.Cut(strings.ToLower(want), "-")
	if haveBase != wantBase {
		return false
	}
	// A region-tagged file satisfies the base request; a base file only satisfies a base request.
	return wantRegion == "" || haveRegion == wantRegion
}

// ParseSidecarName parses video.language[.forced][.hi].ext names; plain video.ext yields an empty language.
func ParseSidecarName(videoRel, name string) (SidecarName, bool) {
	videoBase := path.Base(videoRel)
	stem := strings.TrimSuffix(videoBase, path.Ext(videoBase))
	if stem == "" || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return SidecarName{}, false
	}
	ext := strings.ToLower(path.Ext(name))
	format, ok := sidecarFormats[ext]
	if !ok {
		return SidecarName{}, false
	}
	if name == stem+ext {
		return SidecarName{Format: format, Plain: true}, true
	}
	if !strings.HasPrefix(name, stem+".") {
		return SidecarName{}, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, stem+"."), path.Ext(name)), ".")
	parsed := SidecarName{Format: format}
	for _, part := range parts {
		if parsed.Language == "" {
			if language, valid := NormalizeLanguage(part); valid {
				parsed.Language = language
				continue
			}
			// The first component must be a language once flags are excluded.
			if _, isFlag := flagAliases[strings.ToLower(part)]; !isFlag {
				return SidecarName{}, false
			}
		}
		flag, isFlag := flagAliases[strings.ToLower(part)]
		if !isFlag {
			return SidecarName{}, false
		}
		switch flag {
		case "forced":
			parsed.Forced = true
		case "hi":
			parsed.HI = true
		}
	}
	if parsed.Language == "" && !parsed.Forced && !parsed.HI {
		return SidecarName{}, false
	}
	return parsed, true
}

// SidecarFileName renders video.language[.forced][.hi].ext for a catalog video.
func SidecarFileName(videoRel, language string, forced, hi bool, format Format) (string, error) {
	videoBase := path.Base(videoRel)
	stem := strings.TrimSuffix(videoBase, path.Ext(videoBase))
	if stem == "" {
		return "", fmt.Errorf("%w: video path has no usable file name", ErrUnsafe)
	}
	ext := extensionFor(format)
	suffix := ""
	normalized, ok := NormalizeLanguage(language)
	if ok {
		suffix = "." + normalized
	} else if strings.TrimSpace(language) != "" {
		return "", fmt.Errorf("%w: unsupported language code %q", ErrInvalid, language)
	}
	if forced {
		suffix += ".forced"
	}
	if hi {
		suffix += ".hi"
	}
	return stem + suffix + ext, nil
}

func extensionFor(format Format) string {
	switch format {
	case FormatVTT:
		return ".vtt"
	case FormatASS:
		return ".ass"
	case FormatSSA:
		return ".ssa"
	default:
		return ".srt"
	}
}

// ListSidecars reads the video directory and returns recognizable sidecar files.
func ListSidecars(rootPath, videoRel string) ([]SidecarFile, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("%w: library root is not readable", ErrUnsafe)
	}
	defer root.Close()
	return listSidecars(root, videoRel)
}

func listSidecars(root *os.Root, videoRel string) ([]SidecarFile, error) {
	dir, err := root.Open(path.Dir(videoRel))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxDirEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	files := make([]SidecarFile, 0, 4)
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		parsed, ok := ParseSidecarName(videoRel, name)
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, SidecarFile{Name: name, Size: info.Size(), SidecarName: parsed})
	}
	return files, nil
}

// ReadSubtitle reads a bounded UTF-8 subtitle file below a library root.
func ReadSubtitle(rootPath, rel string) ([]byte, error) {
	file, err := library.Open(rootPath, rel)
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle file is not readable", ErrNotFound)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSubtitleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle file could not be read", ErrInvalid)
	}
	if len(data) > maxSubtitleBytes {
		return nil, fmt.Errorf("%w: subtitle file exceeds %d bytes", ErrInvalid, maxSubtitleBytes)
	}
	return data, nil
}

// PublishTarget describes one sidecar write.
type PublishTarget struct {
	RootPath  string
	VideoPath string
	Language  string
	Forced    bool
	HI        bool
	Format    Format
	Source    string
	Data      []byte
	// Path replaces an existing sidecar at this exact video-relative path.
	Path string
}

// PublishSidecar validates and atomically installs a sidecar, recycling the previous file first.
func PublishSidecar(ctx context.Context, target PublishTarget) (Sidecar, error) {
	if err := ctx.Err(); err != nil {
		return Sidecar{}, err
	}
	format := target.Format
	if format == "" {
		format = FormatSRT
	}
	parsed, err := ParseDocument(target.Data)
	if err != nil {
		return Sidecar{}, err
	}
	if !formatCompatible(parsed.Format, format) {
		return Sidecar{}, fmt.Errorf("%w: subtitle payload is not %s", ErrInvalid, format)
	}
	if target.Language != "" {
		normalized, ok := NormalizeLanguage(target.Language)
		if !ok {
			return Sidecar{}, fmt.Errorf("%w: unsupported language code %q", ErrInvalid, target.Language)
		}
		target.Language = normalized
	}
	target.Format = format
	videoRel, err := cleanRel(target.VideoPath)
	if err != nil {
		return Sidecar{}, err
	}
	targetRel, err := publishRel(videoRel, target)
	if err != nil {
		return Sidecar{}, err
	}

	root, err := os.OpenRoot(target.RootPath)
	if err != nil {
		return Sidecar{}, fmt.Errorf("%w: library root is not readable", ErrUnsafe)
	}
	defer root.Close()
	if err := verifyNoSymlinks(root, videoRel); err != nil {
		return Sidecar{}, err
	}
	info, err := root.Lstat(videoRel)
	if err != nil || !info.Mode().IsRegular() {
		return Sidecar{}, fmt.Errorf("%w: video file is not available", ErrNotFound)
	}

	if existing, err := root.ReadFile(targetRel); err == nil {
		if bytes.Equal(existing, target.Data) {
			return sidecarRecord(target, targetRel, int64(len(existing))), nil
		}
	}
	tempRel := path.Join(path.Dir(targetRel), ".subtitles-"+rand.Text()+".tmp")
	file, err := root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Sidecar{}, fmt.Errorf("subtitles: staging %s: %w", path.Base(targetRel), err)
	}
	if _, err := file.Write(target.Data); err != nil {
		file.Close()
		root.Remove(tempRel)
		return Sidecar{}, fmt.Errorf("subtitles: staging %s: %w", path.Base(targetRel), err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		root.Remove(tempRel)
		return Sidecar{}, fmt.Errorf("subtitles: staging %s: %w", path.Base(targetRel), err)
	}
	if err := file.Close(); err != nil {
		root.Remove(tempRel)
		return Sidecar{}, fmt.Errorf("subtitles: staging %s: %w", path.Base(targetRel), err)
	}

	baseName := path.Base(targetRel)
	archived := ""
	if _, err := root.Lstat(targetRel); err == nil {
		archived, err = archivePrior(root, targetRel)
		if err != nil {
			root.Remove(tempRel)
			return Sidecar{}, fmt.Errorf("subtitles: recycling the previous %s: %w", baseName, err)
		}
	}
	if err := root.Rename(tempRel, targetRel); err != nil {
		// Restore the recycled file so a failed publish never loses the previous subtitle.
		if archived != "" {
			if restoreErr := root.Rename(archived, targetRel); restoreErr != nil {
				return Sidecar{}, fmt.Errorf("subtitles: publishing %s: %w (previous file kept in %s)", baseName, err, archived)
			}
		}
		root.Remove(tempRel)
		return Sidecar{}, fmt.Errorf("subtitles: publishing %s: %w", baseName, err)
	}
	written, err := root.Lstat(targetRel)
	if err != nil || !written.Mode().IsRegular() || written.Size() != int64(len(target.Data)) {
		return Sidecar{}, fmt.Errorf("subtitles: published %s could not be verified", baseName)
	}
	return sidecarRecord(target, targetRel, written.Size()), nil
}

// publishRel resolves the destination: an explicit same-video path or video.language[.forced][.hi].ext.
func publishRel(videoRel string, target PublishTarget) (string, error) {
	if target.Path != "" {
		clean, err := cleanRel(target.Path)
		if err != nil {
			return "", err
		}
		if path.Dir(clean) != path.Dir(videoRel) {
			return "", fmt.Errorf("%w: subtitle must sit next to its video", ErrUnsafe)
		}
		base := path.Base(clean)
		videoStem := strings.TrimSuffix(path.Base(videoRel), path.Ext(videoRel))
		if base != videoStem+path.Ext(base) && !strings.HasPrefix(base, videoStem+".") {
			return "", fmt.Errorf("%w: subtitle name does not belong to this video", ErrUnsafe)
		}
		if _, ok := sidecarFormats[strings.ToLower(path.Ext(base))]; !ok {
			return "", fmt.Errorf("%w: unsupported subtitle extension", ErrInvalid)
		}
		return clean, nil
	}
	baseName, err := SidecarFileName(videoRel, target.Language, target.Forced, target.HI, target.Format)
	if err != nil {
		return "", err
	}
	if dir := path.Dir(videoRel); dir != "." {
		return path.Join(dir, baseName), nil
	}
	return baseName, nil
}

func formatCompatible(have, want Format) bool {
	if want == FormatASS || want == FormatSSA {
		return have == FormatASS || have == FormatSSA
	}
	return have == want
}

func sidecarRecord(target PublishTarget, rel string, size int64) Sidecar {
	source := target.Source
	if source == "" {
		source = "manual"
	}
	return Sidecar{
		Path:     rel,
		Language: target.Language,
		Format:   string(target.Format),
		Forced:   target.Forced,
		HI:       target.HI,
		Source:   source,
		Size:     size,
	}
}

// archivePrior recycles a path below root/.recycle and returns the recycled location.
func archivePrior(root *os.Root, rel string) (string, error) {
	if rel == recycleDir || strings.HasPrefix(rel, recycleDir+"/") {
		return "", fmt.Errorf("%w: %s is already recycled", ErrUnsafe, rel)
	}
	for attempt := 0; attempt < 16; attempt++ {
		dest := recycledName(rel, attempt)
		if dir := path.Dir(dest); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
		}
		if _, err := root.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := root.Rename(rel, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	return "", fmt.Errorf("%w: no free recycle name for %s", ErrConflict, rel)
}

func recycledName(rel string, attempt int) string {
	base := path.Join(recycleDir, rel)
	if attempt == 0 {
		return base
	}
	ext := path.Ext(base)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), attempt, ext)
}

// MovedSidecar records one sidecar file renamed with its video.
type MovedSidecar struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// MoveSidecars renames every sidecar of a video after the video itself moved, without replacing files.
func MoveSidecars(rootPath, fromVideo, toVideo string) ([]MovedSidecar, error) {
	fromRel, err := cleanRel(fromVideo)
	if err != nil {
		return nil, err
	}
	toRel, err := cleanRel(toVideo)
	if err != nil {
		return nil, err
	}
	if path.Dir(fromRel) == path.Dir(toRel) && fromRel == toRel {
		return nil, nil
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("%w: library root is not readable", ErrUnsafe)
	}
	defer root.Close()
	if err := verifyNoSymlinks(root, fromRel); err != nil {
		return nil, err
	}
	entries, err := listSidecars(root, fromRel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	toStem := strings.TrimSuffix(path.Base(toRel), path.Ext(toRel))
	fromStem := strings.TrimSuffix(path.Base(fromRel), path.Ext(fromRel))
	moved := make([]MovedSidecar, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name, fromStem) {
			continue
		}
		destName := toStem + strings.TrimPrefix(entry.Name, fromStem)
		from := path.Join(path.Dir(fromRel), entry.Name)
		to := path.Join(path.Dir(toRel), destName)
		if dir := path.Dir(to); dir != "." && dir != path.Dir(from) {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return moved, err
			}
		}
		if _, err := root.Lstat(to); err == nil {
			return moved, fmt.Errorf("%w: %s already exists", ErrConflict, to)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return moved, err
		}
		if err := root.Rename(from, to); err != nil {
			return moved, err
		}
		moved = append(moved, MovedSidecar{From: from, To: to})
	}
	return moved, nil
}

func cleanRel(name string) (string, error) {
	if name == "" || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
		}
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
	}
	return clean, nil
}

func verifyNoSymlinks(root *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		current := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafe, current)
		}
	}
	return nil
}

// ValidateSidecarPath ensures a subtitle path belongs to the video: same directory, same stem, subtitle extension.
func ValidateSidecarPath(videoRel, rel string) error {
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	videoClean, err := cleanRel(videoRel)
	if err != nil {
		return err
	}
	if path.Dir(clean) != path.Dir(videoClean) {
		return fmt.Errorf("%w: subtitle must sit next to its video", ErrUnsafe)
	}
	videoStem := strings.TrimSuffix(path.Base(videoClean), path.Ext(videoClean))
	base := path.Base(clean)
	if base != videoStem+path.Ext(base) && !strings.HasPrefix(base, videoStem+".") {
		return fmt.Errorf("%w: subtitle name does not belong to this video", ErrUnsafe)
	}
	if _, ok := sidecarFormats[strings.ToLower(path.Ext(base))]; !ok {
		return fmt.Errorf("%w: unsupported subtitle extension", ErrInvalid)
	}
	return nil
}

// ResolveRelativePath accepts a root-relative or root-absolute path and returns the relative form.
func ResolveRelativePath(rootPath, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
	}
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(rootPath, filepath.Clean(name))
		if err != nil {
			return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
		}
		name = filepath.ToSlash(rel)
	}
	return cleanRel(name)
}
