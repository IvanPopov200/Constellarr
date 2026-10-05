package torrents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	maxTorrentFiles = 20000
	maxNameRunes    = 300
	maxPathRunes    = 1800
	maxComponent    = 200
)

var errUnsafePath error = invalid("the torrent contains an unsafe file name")

// validComponent rejects names that could escape the job directory or confuse the filesystem.
func validComponent(part string) bool {
	if part == "" || part == "." || part == ".." || len(part) > maxComponent {
		return false
	}
	if strings.ContainsAny(part, `\/`) || strings.ContainsRune(part, 0) {
		return false
	}
	for _, r := range part {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func componentsOf(name string) ([]string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") || len([]rune(name)) > maxPathRunes {
		return nil, errUnsafePath
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if !validComponent(part) {
			return nil, errUnsafePath
		}
	}
	return parts, nil
}

func splitRel(name string) (string, error) {
	parts, err := componentsOf(name)
	if err != nil {
		return "", err
	}
	return path.Join(parts...), nil
}

// torrentFiles validates every announced path and rejects duplicate or case-colliding names.
func torrentFiles(info *metainfo.Info) ([]File, error) {
	if info == nil {
		return nil, errUnsafePath
	}
	name := info.BestName()
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return nil, errUnsafePath
	}
	infos := info.UpvertedFiles()
	if len(infos) == 0 || len(infos) > maxTorrentFiles {
		return nil, errUnsafePath
	}
	files := make([]File, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, file := range infos {
		parts := append([]string{}, file.BestPath()...)
		if len(parts) == 0 {
			parts = []string{name}
		}
		for _, part := range parts {
			if !validComponent(part) {
				return nil, errUnsafePath
			}
		}
		rel := path.Join(parts...)
		if len([]rune(rel)) > maxPathRunes {
			return nil, errUnsafePath
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate file name %s", errUnsafePath, rel)
		}
		seen[key] = true
		files = append(files, File{Name: rel, Size: file.Length})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func (s *Service) dataDir(infoHash string) (string, error) {
	if !validInfoHash(infoHash) {
		return "", errUnsafePath
	}
	return filepath.Join(s.root, infoHash), nil
}

func validInfoHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// checkNoSymlinks refuses directories and entries that would redirect writes outside the job directory.
func checkNoSymlinks(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errUnsafePath
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return errUnsafePath
	}
	return filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errUnsafePath
		}
		return nil
	})
}

// safeFilePath reproduces the validated name of a torrent file and drops unsafe components.
func safeFilePath(info *metainfo.Info, file metainfo.FileInfo) string {
	parts := file.BestPath()
	safe := make([]string, 0, len(parts))
	for _, part := range parts {
		if validComponent(part) {
			safe = append(safe, part)
		}
	}
	if len(safe) == 0 {
		if name := info.BestName(); validComponent(name) {
			return name
		}
		return "file"
	}
	return filepath.Join(safe...)
}

// removeDataDir deletes only a per-job directory created by this service.
func (s *Service) removeDataDir(dir string) error {
	cleaned, err := filepath.Abs(dir)
	if err != nil || filepath.Dir(cleaned) != s.root || !validInfoHash(filepath.Base(cleaned)) {
		return errUnsafePath
	}
	if err := os.RemoveAll(cleaned); err != nil {
		return errors.New("torrents: the download directory could not be removed")
	}
	return nil
}
