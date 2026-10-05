package music

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrTemplate = errors.New("music: unusable naming template")

const (
	defaultFolderTemplate = "{artist}/{album} ({year})"
	defaultFileTemplate   = "{track:02} {title}"
	// Multidisc albums keep the disc in the file name so players can order them.
	multidiscFileTemplate = "{disc}-{track:02} {title}"
	maxComponentBytes     = 200
)

var musicTokens = map[string]bool{
	"artist": true, "trackArtist": true, "album": true, "year": true, "date": true, "type": true,
	"format": true, "quality": true, "track": true, "disc": true, "title": true,
	"mbAlbumId": true, "mbArtistId": true, "original": true,
}

var fileOnlyTokens = map[string]bool{"track": true, "disc": true, "title": true, "trackArtist": true}

func effectiveFileTemplate(cfg Config, discs int) string {
	template := strings.TrimSpace(cfg.FileTemplate)
	if template == "" || (template == defaultFileTemplate && discs > 1) {
		if discs > 1 {
			return multidiscFileTemplate
		}
		return defaultFileTemplate
	}
	return template
}

func validateMusicTemplate(tmpl string, fileLevel bool) error {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return fmt.Errorf("%w: a template is required", ErrTemplate)
	}
	if len(tmpl) > 512 {
		return fmt.Errorf("%w: the template is too long", ErrTemplate)
	}
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			return nil
		}
		closeOffset := strings.IndexByte(tmpl[i+open:], '}')
		if closeOffset < 0 {
			return fmt.Errorf("%w: unmatched '{'", ErrTemplate)
		}
		spec := tmpl[i+open+1 : i+open+closeOffset]
		name, _, ok := parseMusicToken(spec)
		if !ok || !musicTokens[name] {
			return fmt.Errorf("%w: unsupported token {%s}", ErrTemplate, spec)
		}
		if !fileLevel && fileOnlyTokens[name] {
			return fmt.Errorf("%w: the folder template cannot use {%s}", ErrTemplate, name)
		}
		i += open + closeOffset + 1
	}
	return nil
}

func parseMusicToken(spec string) (string, int, bool) {
	name, width := spec, 0
	if colon := strings.IndexByte(spec, ':'); colon >= 0 {
		name = spec[:colon]
		format := spec[colon+1:]
		format = strings.TrimPrefix(format, "0")
		parsed, err := strconv.Atoi(format)
		if err != nil || parsed < 0 || parsed > 20 {
			return "", 0, false
		}
		width = parsed
	}
	if !validTokenName(name) {
		return "", 0, false
	}
	return name, width, true
}

func validTokenName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			return false
		}
	}
	return true
}

// renderMusicTemplate substitutes validated tokens and rejects values that escape their component.
func renderMusicTemplate(tmpl string, values map[string]string, fileLevel bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			out.WriteString(tmpl[i:])
			break
		}
		out.WriteString(tmpl[i : i+open])
		i += open
		closeOffset := strings.IndexByte(tmpl[i:], '}')
		if closeOffset < 0 {
			return "", fmt.Errorf("%w: unmatched '{'", ErrTemplate)
		}
		spec := tmpl[i+1 : i+closeOffset]
		i += closeOffset + 1
		name, width, ok := parseMusicToken(spec)
		if !ok || !musicTokens[name] {
			return "", fmt.Errorf("%w: unsupported token {%s}", ErrTemplate, spec)
		}
		if !fileLevel && fileOnlyTokens[name] {
			return "", fmt.Errorf("%w: the folder template cannot use {%s}", ErrTemplate, name)
		}
		value := values[name]
		if width > 0 {
			if number, err := strconv.Atoi(value); err == nil && number >= 0 {
				value = fmt.Sprintf("%0*d", width, number)
			}
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

func musicFolderRel(cfg Config, values map[string]string) (string, error) {
	template := strings.TrimSpace(cfg.FolderTemplate)
	if template == "" {
		template = defaultFolderTemplate
	}
	rendered, err := renderMusicTemplate(template, values, false)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rendered, "/")
	for i, part := range parts {
		part = sanitizeComponent(part)
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: the folder name is empty", ErrTemplate)
		}
		parts[i] = part
	}
	folder := strings.Join(parts, "/")
	if folder == "" || strings.HasPrefix(folder, "/") || strings.Contains(folder, "..") || !filepath.IsLocal(folder) {
		return "", fmt.Errorf("%w: the folder template escapes the library root", ErrTemplate)
	}
	return folder, nil
}

func musicFileName(cfg Config, discs int, values map[string]string, fallbackExt string) (string, error) {
	template := effectiveFileTemplate(cfg, discs)
	rendered, err := renderMusicTemplate(template, values, true)
	if err != nil {
		return "", err
	}
	name := sanitizeComponent(rendered)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("%w: the file name is empty", ErrTemplate)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%w: the file name contains a path separator", ErrTemplate)
	}
	ext := strings.ToLower(strings.TrimSpace(fallbackExt))
	if ext == "" {
		ext = ".flac"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if limit := 255 - len(ext); len(name) > limit {
		name = truncateBytes(name, limit)
	}
	return name + ext, nil
}

func sanitizeComponent(value string) string {
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
	return strings.Trim(truncateBytes(value, maxComponentBytes), " .-_")
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

func uniqueRel(dest string, seen map[string]bool) string {
	if !seen[strings.ToLower(dest)] {
		return dest
	}
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
