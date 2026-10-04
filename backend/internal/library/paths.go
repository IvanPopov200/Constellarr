package library

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// cleanRel validates a slash-separated path that must stay below a root.
func cleanRel(name string) (string, bool) {
	if name == "" || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	if path.IsAbs(name) {
		return "", false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// relWithinRoot normalizes a relative or root-absolute path and rejects escapes.
func relWithinRoot(root, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
	}
	if filepath.IsAbs(name) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("%w: root", ErrUnsafe)
		}
		rel, err := filepath.Rel(absRoot, filepath.Clean(name))
		if err != nil {
			return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
		}
		name = filepath.ToSlash(rel)
	}
	clean, ok := cleanRel(name)
	if !ok {
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

// mkdirAll refuses symbolic links and files while creating directories.
func mkdirAll(root *os.Root, dir string, created *[]string) error {
	if dir == "" || dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		current := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(current)
		if err == nil {
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafe, current)
			}
			if !info.IsDir() {
				return fmt.Errorf("%w: %s is not a directory", ErrConflict, current)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := root.Mkdir(current, 0o755); err != nil {
			return err
		}
		*created = append(*created, current)
	}
	return nil
}

func tempName(dir string) string {
	name := ".library-" + rand.Text() + ".tmp"
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

func verifyFile(root *os.Root, rel string, size int64) (fs.FileInfo, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafe, rel)
	}
	if info.Size() != size || size <= 0 {
		return nil, fmt.Errorf("%w: %s has size %d, expected %d", ErrSource, rel, info.Size(), size)
	}
	return info, nil
}
