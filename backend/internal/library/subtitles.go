package library

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
)

var subtitleSuffix = regexp.MustCompile(`(?i)^(?:\.[a-z0-9_-]{1,32}){0,4}\.(?:srt|vtt|ass|ssa|sub|idx|sup)$`)

func SubtitleSidecars(rootPath, video string) ([]File, error) {
	rel, err := relWithinRoot(rootPath, video)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return subtitleFiles(root, rel)
}

func subtitleFiles(root *os.Root, video string) ([]File, error) {
	dir := path.Dir(video)
	if err := verifyNoSymlinks(root, dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	handle, err := root.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	entries, err := handle.ReadDir(100001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 100000 {
		return nil, fmt.Errorf("%w: subtitle directory is too large", ErrSource)
	}
	stem := strings.TrimSuffix(path.Base(video), path.Ext(video))
	files := make([]File, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, stem) || !subtitleSuffix.MatchString(strings.TrimPrefix(name, stem)) {
			continue
		}
		rel := path.Join(dir, name)
		if err := verifyNoSymlinks(root, rel); err != nil {
			return nil, err
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 256<<20 {
			return nil, fmt.Errorf("%w: unusable subtitle sidecar %s", ErrSource, rel)
		}
		files = append(files, File{Path: rel, Size: info.Size()})
	}
	return files, nil
}

func planSubtitles(p *plan, source, destination *os.Root) error {
	if p.existing == nil {
		p.existing = map[string]bool{}
	}
	p.subtitleOwners = map[string]string{}
	for video := range p.existing {
		if !isVideo(video) {
			continue
		}
		files, err := subtitleFiles(destination, video)
		if err != nil {
			return err
		}
		for _, file := range files {
			p.subtitleOwners[file.Path] = video
		}
	}
	for rel := range p.subtitleOwners {
		p.existing[rel] = true
	}
	seen := map[string]bool{}
	sources := map[string]bool{}
	for _, video := range p.targets {
		files, err := subtitleFiles(source, video.srcRel)
		if err != nil {
			return err
		}
		srcStem := strings.TrimSuffix(video.srcRel, path.Ext(video.srcRel))
		destStem := strings.TrimSuffix(video.destRel, path.Ext(video.destRel))
		for _, file := range files {
			dest := destStem + strings.TrimPrefix(file.Path, srcStem)
			if len(path.Base(dest)) > 255 {
				return fmt.Errorf("%w: subtitle filename is too long", ErrTemplate)
			}
			if seen[strings.ToLower(dest)] || sources[file.Path] {
				return fmt.Errorf("%w: ambiguous subtitle destination", ErrConflict)
			}
			seen[strings.ToLower(dest)] = true
			sources[file.Path] = true
			p.targets = append(p.targets, &target{srcRel: file.Path, destRel: dest, size: file.Size, auxiliary: true})
		}
	}
	return nil
}

func (p *plan) unchangedVideo(rel string) bool {
	for _, t := range p.targets {
		if !t.auxiliary && t.destRel == rel && !t.publish {
			return true
		}
	}
	return false
}

func MoveWithSubtitles(rootPath, from, to string) error {
	from, err := relWithinRoot(rootPath, from)
	if err != nil {
		return err
	}
	to, err = relWithinRoot(rootPath, to)
	if err != nil || from == to {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	subtitles, err := subtitleFiles(root, from)
	if err != nil {
		return err
	}
	type move struct{ from, to string }
	moves := []move{{from, to}}
	oldStem, newStem := strings.TrimSuffix(from, path.Ext(from)), strings.TrimSuffix(to, path.Ext(to))
	for _, subtitle := range subtitles {
		moves = append(moves, move{subtitle.Path, newStem + strings.TrimPrefix(subtitle.Path, oldStem)})
	}
	var created []string
	for _, item := range moves {
		if err := verifyNoSymlinks(root, item.from); err != nil {
			return err
		}
		if err := mkdirAll(root, path.Dir(item.to), &created); err != nil {
			return err
		}
		if _, err := root.Lstat(item.to); err == nil {
			return fmt.Errorf("%w: %s", ErrConflict, item.to)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for i, item := range moves {
		if err := renameNoReplace(root, item.from, item.to); err != nil {
			problems := []error{err}
			for j := i - 1; j >= 0; j-- {
				problems = append(problems, renameNoReplace(root, moves[j].to, moves[j].from))
			}
			return errors.Join(problems...)
		}
	}
	return nil
}
