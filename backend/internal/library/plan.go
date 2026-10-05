package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultFolderTemplate = "{title} ({year})"
	defaultFileTemplate   = "{title} ({year})"
	maxComponentBytes     = 200
)

var templateTokens = []string{"title", "year", "imdbId", "quality", "original", "part", "season", "episode", "episodeCode", "episodeTitle"}

// episodeTemplateTokens require Options.Episode so a movie template cannot render empty episode names.
var episodeTemplateTokens = map[string]bool{"season": true, "episode": true, "episodeCode": true, "episodeTitle": true}

type target struct {
	srcRel    string
	size      int64
	destRel   string
	part      string
	tempRel   string
	existed   bool
	owned     bool
	destSize  int64
	destHash  digest
	replace   bool
	publish   bool
	inPlace   bool
	auxiliary bool
}

type plan struct {
	root           string
	sourceRoot     string
	mode           string
	folderRel      string
	targets        []*target
	sidecars       []nfoSidecar
	existing       map[string]bool
	subtitleOwners map[string]string
}

// Preview returns the deterministic destinations an import would publish.
func Preview(opts Options, sources []Source) ([]File, error) {
	p, err := buildPlan(opts, sources)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(p.targets))
	for _, t := range p.targets {
		if t.auxiliary {
			continue
		}
		files = append(files, File{Path: t.destRel, Size: t.size})
	}
	return files, nil
}

func buildPlan(opts Options, sources []Source) (*plan, error) {
	if opts.Root == "" || opts.SourceRoot == "" {
		return nil, fmt.Errorf("%w: source and library roots must be configured", ErrUnsafe)
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: library root", ErrUnsafe)
	}
	sourceRoot, err := filepath.Abs(opts.SourceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: source root", ErrUnsafe)
	}
	mode, err := normalizeMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateEpisode(opts.Episode); err != nil {
		return nil, err
	}
	folderTemplate := opts.FolderTemplate
	if folderTemplate == "" {
		folderTemplate = defaultFolderTemplate
	}
	fileTemplate := opts.FileTemplate
	if fileTemplate == "" {
		fileTemplate = defaultFileTemplate
	}
	hasEpisode := opts.Episode != nil
	if err := validateTemplate(folderTemplate, hasEpisode); err != nil {
		return nil, err
	}
	if err := validateTemplate(fileTemplate, hasEpisode); err != nil {
		return nil, err
	}
	existing, err := existingPaths(root, opts.Existing)
	if err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("%w: library root: %v", ErrUnsafe, err)
	}
	defer rootHandle.Close()
	sourceHandle, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: source root: %v", ErrSource, err)
	}
	defer sourceHandle.Close()

	targets, err := selectSources(sourceHandle, sourceRoot, sources)
	if err != nil {
		return nil, err
	}
	p := &plan{root: root, sourceRoot: sourceRoot, mode: mode, existing: existing}
	if err := nameTargets(p, targets, folderTemplate, fileTemplate, opts); err != nil {
		return nil, err
	}
	sidecars, err := buildSidecars(opts, folderTemplate, p.folderRel, targets)
	if err != nil {
		return nil, err
	}
	p.sidecars = sidecars
	if err := planSubtitles(p, sourceHandle, rootHandle); err != nil {
		return nil, err
	}
	for _, t := range p.targets {
		if sourceRoot == root && t.srcRel == t.destRel {
			t.inPlace = true
			continue
		}
		if err := checkDest(rootHandle, t); err != nil {
			return nil, err
		}
		t.owned = existing[t.destRel]
	}
	return p, nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeCopy:
		return ModeCopy, nil
	case ModeMove:
		return ModeMove, nil
	case ModeLink, "link":
		return ModeLink, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrMode, mode)
	}
}

func selectSources(root *os.Root, sourceRoot string, sources []Source) ([]*target, error) {
	var targets []*target
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		rel, err := relWithinRoot(sourceRoot, source.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: source %q", ErrUnsafe, source.Name)
		}
		if seen[rel] || !isVideo(rel) || isSampleOrTrailer(rel) {
			continue
		}
		seen[rel] = true
		if err := verifyNoSymlinks(root, rel); err != nil {
			return nil, err
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrSource, rel, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s is not a regular file", ErrSource, rel)
		}
		size := info.Size()
		if source.Size > 0 && source.Size != size {
			return nil, fmt.Errorf("%w: %s changed size", ErrSource, rel)
		}
		if size <= 0 {
			return nil, fmt.Errorf("%w: %s is empty", ErrSource, rel)
		}
		targets = append(targets, &target{srcRel: rel, size: size})
	}
	if len(targets) == 0 {
		return nil, ErrNoMedia
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].srcRel < targets[j].srcRel })
	return targets, nil
}

func nameTargets(p *plan, targets []*target, folderTemplate, fileTemplate string, opts Options) error {
	assignParts(targets)
	folder, err := renderFolder(folderTemplate, opts, targets[0].srcRel)
	if err != nil {
		return err
	}
	p.folderRel = folder
	p.targets = targets
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		name, err := renderFile(fileTemplate, opts, t)
		if err != nil {
			return err
		}
		dest := name
		if p.folderRel != "" {
			dest = path.Join(p.folderRel, name)
		}
		key := strings.ToLower(dest)
		if seen[key] {
			dest = uniqueDest(dest, seen)
			key = strings.ToLower(dest)
		}
		seen[key] = true
		t.destRel = dest
	}
	return nil
}

func uniqueDest(dest string, seen map[string]bool) string {
	ext := path.Ext(dest)
	stem := strings.TrimSuffix(dest, ext)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !seen[strings.ToLower(candidate)] {
			return candidate
		}
	}
	return dest
}

func assignParts(targets []*target) {
	if len(targets) == 1 {
		return
	}
	used := make(map[int]bool, len(targets))
	for i, t := range targets {
		n := partNumber(baseNoExt(t.srcRel))
		if n <= 0 || used[n] {
			n = i + 1
			for used[n] {
				n++
			}
		}
		used[n] = true
		t.part = fmt.Sprintf("part%d", n)
	}
}

func renderFolder(tmpl string, opts Options, firstSrc string) (string, error) {
	rendered := renderTokens(tmpl, tokenValues(opts, firstSrc, ""))
	parts := strings.Split(rendered, "/")
	for i, part := range parts {
		part = tidyName(part)
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: folder name is empty", ErrTemplate)
		}
		parts[i] = part
	}
	clean, ok := cleanRel(strings.Join(parts, "/"))
	if !ok {
		return "", fmt.Errorf("%w: folder template escapes the library root", ErrTemplate)
	}
	return clean, nil
}

func renderFile(tmpl string, opts Options, t *target) (string, error) {
	name := tidyName(renderTokens(tmpl, tokenValues(opts, t.srcRel, t.part)))
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("%w: file name is empty", ErrTemplate)
	}
	if t.part != "" && !strings.Contains(tmpl, "{part}") {
		name += " - " + t.part
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: file name %q contains a path separator", ErrTemplate, name)
	}
	ext := strings.ToLower(path.Ext(t.srcRel))
	if limit := 255 - len(ext); len(name) > limit {
		name = truncateBytes(name, limit)
	}
	return name + ext, nil
}

func tokenValues(opts Options, srcRel, part string) map[string]string {
	original := sanitizeValue(baseNoExt(srcRel))
	title := sanitizeValue(opts.Metadata.Title)
	if title == "" {
		title = original
	}
	year := ""
	if opts.Metadata.Year > 0 {
		year = strconv.Itoa(opts.Metadata.Year)
	}
	values := map[string]string{
		"title":    title,
		"year":     year,
		"imdbId":   sanitizeValue(opts.Metadata.IMDbID),
		"quality":  sanitizeValue(opts.Quality),
		"original": original,
		"part":     part,
	}
	if episode := opts.Episode; episode != nil {
		values["season"] = fmt.Sprintf("%02d", episode.Season)
		values["episode"] = renderEpisodeToken(episode.Numbers)
		values["episodeCode"] = renderEpisodeCode(episode.Season, episode.Numbers)
		values["episodeTitle"] = sanitizeValue(episode.Title)
	}
	return values
}

func renderTokens(tmpl string, values map[string]string) string {
	pairs := make([]string, 0, len(templateTokens)*2)
	for _, token := range templateTokens {
		pairs = append(pairs, "{"+token+"}", values[token])
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

func validateTemplate(tmpl string, hasEpisode bool) error {
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			return nil
		}
		close := strings.IndexByte(tmpl[i+open:], '}')
		if close < 0 {
			return fmt.Errorf("%w: unmatched '{'", ErrTemplate)
		}
		name := tmpl[i+open+1 : i+open+close]
		known := false
		for _, token := range templateTokens {
			if name == token {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("%w: unsupported token {%s}", ErrTemplate, name)
		}
		if !hasEpisode && episodeTemplateTokens[name] {
			return fmt.Errorf("%w: template token {%s} requires episode data", ErrEpisode, name)
		}
		i += open + close + 1
	}
	return nil
}

func sanitizeValue(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, " .-_")
	return truncateBytes(value, maxComponentBytes)
}

func tidyName(name string) string {
	for i := 0; i < 4; i++ {
		next := strings.Join(strings.Fields(name), " ")
		next = strings.NewReplacer("()", "", "( )", "", "[]", "", "[ ]", "", "{}", "", "{ }", "").Replace(next)
		next = strings.Trim(strings.Join(strings.Fields(next), " "), " .-_")
		if next == name {
			return next
		}
		name = next
	}
	return name
}

func truncateBytes(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	cut := value[:limit]
	for !utf8.ValidString(cut) && len(cut) > 0 {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " .-_")
}

func existingPaths(root string, entries []string) (map[string]bool, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		rel, err := relWithinRoot(root, entry)
		if err != nil {
			return nil, fmt.Errorf("%w: existing path %q", ErrUnsafe, entry)
		}
		out[rel] = true
	}
	return out, nil
}

func checkDest(root *os.Root, t *target) error {
	if dir := path.Dir(t.destRel); dir != "." {
		if err := checkDir(root, dir); err != nil {
			return err
		}
	}
	info, err := root.Lstat(t.destRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafe, t.destRel)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a file", ErrConflict, t.destRel)
	}
	t.existed, t.destSize = true, info.Size()
	return nil
}

func checkDir(root *os.Root, dir string) error {
	parts := strings.Split(dir, "/")
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
		if !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", ErrConflict, current)
		}
	}
	return nil
}

// staleExisting lists owned files the import replaces.
func (p *plan) staleExisting() []string {
	var stale []string
	for rel := range p.existing {
		if owner := p.subtitleOwners[rel]; owner != "" && p.unchangedVideo(owner) {
			continue
		}
		if rel == recycleDir || strings.HasPrefix(rel, recycleDir+"/") {
			continue
		}
		if rel == p.folderRel || strings.HasPrefix(p.folderRel, rel+"/") {
			continue
		}
		if p.hasSidecar(rel) {
			continue
		}
		coveredByDest := false
		for _, t := range p.targets {
			if rel == t.destRel || strings.HasPrefix(t.destRel, rel+"/") {
				coveredByDest = true
				break
			}
		}
		if coveredByDest {
			continue
		}
		stale = append(stale, rel)
	}
	sort.Strings(stale)
	return stale
}

// hasSidecar keeps plan-written sidecars out of stale-owned archiving.
func (p *plan) hasSidecar(rel string) bool {
	for _, sidecar := range p.sidecars {
		if sidecar.rel == rel {
			return true
		}
	}
	return false
}
