package migration

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Constellarr template tokens; anything else in a source format is reported instead of dropped.
var supportedTokens = map[string]bool{
	"title": true, "year": true, "imdbId": true, "quality": true, "original": true,
	"part": true, "season": true, "episode": true, "episodeCode": true, "episodeTitle": true,
}

var movieFormatTokens = map[string]string{
	"{movie title}":    "{title}",
	"{release year}":   "{year}",
	"{imdbid}":         "{imdbId}",
	"{quality full}":   "{quality}",
	"{quality title}":  "{quality}",
	"{original title}": "{original}",
}

var seriesFormatTokens = map[string]string{
	"{series title}":   "{title}",
	"{series year}":    "{year}",
	"{imdbid}":         "{imdbId}",
	"{season:00}":      "{season}",
	"{season}":         "{season}",
	"{episode:00}":     "{episode}",
	"{episode}":        "{episode}",
	"{episode title}":  "{episodeTitle}",
	"{quality full}":   "{quality}",
	"{quality title}":  "{quality}",
	"{original title}": "{original}",
}

func movieNaming(app App, folderFormat, fileFormat string) NamingPlan {
	folder, folderUnsupported := mapNamingTemplate(folderFormat, movieFormatTokens)
	file, fileUnsupported := mapNamingTemplate(fileFormat, movieFormatTokens)
	plan := NamingPlan{Source: app, Media: MediaMovies, Folder: folder, File: file}
	plan.Unsupported = append(plan.Unsupported, folderUnsupported...)
	plan.Unsupported = append(plan.Unsupported, fileUnsupported...)
	plan.Applicable = len(plan.Unsupported) == 0 && validTemplate(folder, file, false) == nil
	return plan
}

func seriesNaming(app App, seriesFormat, seasonFormat, fileFormat string) NamingPlan {
	folder, folderUnsupported := mapNamingTemplate(seriesFormat, seriesFormatTokens)
	season, seasonUnsupported := mapNamingTemplate(seasonFormat, seriesFormatTokens)
	if season != "" {
		folder = strings.TrimRight(folder, "/ ") + "/" + strings.TrimSpace(season)
	}
	file, fileUnsupported := mapNamingTemplate(fileFormat, seriesFormatTokens)
	plan := NamingPlan{Source: app, Media: MediaTV, Folder: folder, File: file}
	plan.Unsupported = append(plan.Unsupported, folderUnsupported...)
	plan.Unsupported = append(plan.Unsupported, seasonUnsupported...)
	plan.Unsupported = append(plan.Unsupported, fileUnsupported...)
	plan.Applicable = len(plan.Unsupported) == 0 && validTemplate(folder, file, true) == nil
	return plan
}

// mapNamingTemplate rewrites source tokens, keeping unknown tokens so the plan can report them.
func mapNamingTemplate(source string, tokens map[string]string) (string, []string) {
	var out strings.Builder
	var unsupported []string
	seen := map[string]bool{}
	for i := 0; i < len(source); {
		open := strings.IndexByte(source[i:], '{')
		if open < 0 {
			out.WriteString(source[i:])
			break
		}
		out.WriteString(source[i : i+open])
		closeIdx := strings.IndexByte(source[i+open:], '}')
		if closeIdx < 0 {
			out.WriteString(source[i+open:])
			break
		}
		token := source[i+open : i+open+closeIdx+1]
		if mapped, ok := tokens[strings.ToLower(token)]; ok {
			out.WriteString(mapped)
		} else {
			if !seen[token] {
				unsupported = append(unsupported, token)
				seen[token] = true
			}
			out.WriteString(token)
		}
		i += open + closeIdx + 1
	}
	return strings.TrimSpace(out.String()), unsupported
}

// validTemplate mirrors the library naming rules so previews fail before an apply would.
func validTemplate(folder, file string, episodic bool) error {
	if err := validTokens(folder); err != nil {
		return err
	}
	episodeUnique := false
	for _, match := range tokenNames(file) {
		if !supportedTokens[match] {
			return fmt.Errorf("unsupported token {%s}", match)
		}
		if match == "episodeCode" || match == "episode" || match == "original" {
			episodeUnique = true
		}
	}
	for label, value := range map[string]string{"folder": folder, "file": file} {
		if value == "" {
			return fmt.Errorf("%s template is empty", label)
		}
		if len(value) > 512 || !filepath.IsLocal(value) || strings.Contains(value, "://") || strings.Contains(value, `\`) {
			return fmt.Errorf("%s template is not a usable relative path", label)
		}
	}
	if episodic && !episodeUnique {
		return fmt.Errorf("the file template needs {episodeCode}, {episode}, or {original}")
	}
	if !episodic && strings.Contains(folder, "{season}") {
		return fmt.Errorf("movie templates cannot use {season}")
	}
	return nil
}

func validTokens(template string) error {
	for _, name := range tokenNames(template) {
		if !supportedTokens[name] {
			return fmt.Errorf("unsupported token {%s}", name)
		}
	}
	return nil
}

func tokenNames(template string) []string {
	var names []string
	for i := 0; i < len(template); {
		open := strings.IndexByte(template[i:], '{')
		if open < 0 {
			break
		}
		closeIdx := strings.IndexByte(template[i+open:], '}')
		if closeIdx < 0 {
			break
		}
		names = append(names, template[i+open+1:i+open+closeIdx])
		i += open + closeIdx + 1
	}
	return names
}
