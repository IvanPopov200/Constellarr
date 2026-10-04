package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	imdbPattern = regexp.MustCompile(`(?i)tt(\d{7,10})`)
	yearPattern = regexp.MustCompile(`^(19|20)\d{2}$`)
	parenYear   = regexp.MustCompile(`\((\d{4})\)`)
	inferSkip   = map[string]bool{"imdb": true, "imdbid": true}
)

// Scan returns video candidates while ignoring recycle, temp, sample, and symlink paths.
func Scan(ctx context.Context, rootPath string) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if rootPath == "" {
		return nil, fmt.Errorf("%w: scan root must be configured", ErrUnsafe)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	type entry struct {
		rel  string
		size int64
	}
	var entries []entry
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if p != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !isVideo(p) || isSampleOrTrailer(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{rel: filepath.ToSlash(p), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	type group struct {
		rep  string
		size int64
		dir  string
	}
	groups := make(map[string]*group)
	for _, e := range entries {
		dir := path.Dir(e.rel)
		key := strings.ToLower(dir + "/" + cleanStem(baseNoExt(e.rel)))
		g, ok := groups[key]
		if !ok {
			g = &group{rep: e.rel, dir: dir}
			groups[key] = g
		}
		g.size += e.size
		if e.rel < g.rep {
			g.rep = e.rel
		}
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidates := make([]Candidate, 0, len(keys))
	for _, key := range keys {
		g := groups[key]
		title, year, imdb := inferName(cleanStem(baseNoExt(g.rep)))
		folder := path.Base(g.dir)
		if g.dir != "." && folder != "" {
			folderTitle, folderYear, folderIMDb := inferName(folder)
			if year == 0 && folderYear > 0 {
				title, year = folderTitle, folderYear
			}
			if title == "" || (year == 0 && folderYear > 0) {
				title = folderTitle
			}
			if imdb == "" {
				imdb = folderIMDb
			}
		}
		if title == "" {
			title = tidyName(strings.Join(tokenize(baseNoExt(g.rep)), " "))
		}
		candidates = append(candidates, Candidate{
			Path:    g.rep,
			Size:    g.size,
			Title:   title,
			Year:    year,
			IMDbID:  imdb,
			Quality: parseQuality(path.Base(g.rep)),
		})
	}
	return candidates, nil
}

// inferName guesses title, year, and IMDb identity from a file or folder stem.
func inferName(stem string) (string, int, string) {
	imdb := ""
	if match := imdbPattern.FindStringSubmatch(stem); match != nil {
		imdb = "tt" + match[1]
		stem = imdbPattern.ReplaceAllString(stem, " ")
	}
	year := 0
	if match := parenYear.FindStringSubmatchIndex(stem); match != nil {
		if y, err := strconv.Atoi(stem[match[2]:match[3]]); err == nil && validYear(y) {
			year = y
			stem = stem[:match[0]] + " " + stem[match[1]:]
		}
	}
	tokens := tokenize(stem)
	kept := tokens[:0]
	for _, token := range tokens {
		if !inferSkip[strings.ToLower(token)] {
			kept = append(kept, token)
		}
	}
	tokens = kept
	junkAt := len(tokens)
	for i, token := range tokens {
		if isJunkToken(token) {
			junkAt = i
			break
		}
	}
	end := junkAt
	if year == 0 {
		for i := 0; i < junkAt; i++ {
			if y, ok := yearValue(tokens[i]); ok {
				year = y
				end = i
			}
		}
	}
	title := tidyName(strings.Join(tokens[:end], " "))
	if title == "" {
		title = tidyName(strings.Join(tokens, " "))
	}
	if title == "" {
		title = tidyName(stem)
	}
	return title, year, imdb
}

func yearValue(token string) (int, bool) {
	if !yearPattern.MatchString(token) {
		return 0, false
	}
	year, err := strconv.Atoi(token)
	if err != nil || !validYear(year) {
		return 0, false
	}
	return year, true
}

func validYear(year int) bool {
	return year >= 1900 && year <= time.Now().Year()+2
}
