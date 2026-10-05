package media

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

const (
	copyBufferBytes = 1 << 20
	maxNameBytes    = 1024
)

var (
	errUnsafeEntry   = errors.New("archive contains an unusable entry path; refusing to extract")
	errOutputFile    = errors.New("download output file could not be written")
	errMediaTooLarge = errors.New("media exceeds the download size limit")
)

type writer struct {
	root  *os.Root
	files []File
	index map[string]int
	total int64
}

func newWriter(dir string) (*writer, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &writer{root: root, index: make(map[string]int)}, nil
}

func (w *writer) close() error { return w.root.Close() }

// Atomic replacement keeps interrupted extraction safe to retry.
func (w *writer) copyFrom(ctx context.Context, name string, src io.Reader, size int64) error {
	rel, ok := cleanRel(name)
	if !ok {
		return errUnsafeEntry
	}
	if !isPayload(rel) {
		return nil
	}
	limit := int64(maxFileBytes)
	if !isMedia(rel) {
		limit = 256 << 20
	}
	if room := int64(maxTotalBytes - w.total); room < limit {
		limit = room
	}
	if size > limit {
		return errMediaTooLarge
	}
	dir := path.Dir(rel)
	if dir != "." {
		if err := w.root.MkdirAll(dir, 0o755); err != nil {
			return errOutputFile
		}
	}
	tmp, tmpName, err := w.createTemp(dir)
	if err != nil {
		return errOutputFile
	}
	written, copyErr := copyLimited(ctx, tmp, src, limit)
	if copyErr == nil && written == 0 {
		tmp.Close()
		w.root.Remove(tmpName)
		return nil
	}
	if copyErr != nil {
		tmp.Close()
		w.root.Remove(tmpName)
		return copyErr
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		w.root.Remove(tmpName)
		return errOutputFile
	}
	if err := tmp.Close(); err != nil {
		w.root.Remove(tmpName)
		return errOutputFile
	}
	if err := w.root.Chmod(tmpName, 0o644); err != nil {
		w.root.Remove(tmpName)
		return errOutputFile
	}
	if err := w.root.Rename(tmpName, rel); err != nil {
		w.root.Remove(tmpName)
		return errOutputFile
	}
	w.total += written - w.record(rel, written)
	return nil
}

func (w *writer) createTemp(dir string) (*os.File, string, error) {
	for i := 0; i < 8; i++ {
		name := ".tmp-" + rand.Text()
		if dir != "." {
			name = dir + "/" + name
		}
		f, err := w.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate a temporary output name")
}

func (w *writer) record(name string, size int64) int64 {
	if i, ok := w.index[name]; ok {
		previous := w.files[i].Size
		w.files[i].Size = size
		return previous
	}
	w.index[name] = len(w.files)
	w.files = append(w.files, File{Name: name, Size: size})
	return 0
}

func cleanRel(name string) (string, bool) {
	if name == "" || len(name) > maxNameBytes || strings.ContainsRune(name, '\\') {
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

func copyLimited(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	buf := make([]byte, copyBufferBytes)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if written+int64(n) > limit {
				return written, errMediaTooLarge
			}
			wrote, writeErr := dst.Write(buf[:n])
			written += int64(wrote)
			if writeErr != nil || wrote != n {
				return written, errOutputFile
			}
		}
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

func passthrough(err error) bool {
	return errors.Is(err, errUnsafeEntry) || errors.Is(err, errMediaTooLarge) ||
		errors.Is(err, errOutputFile) || errors.Is(err, errRedirectedEntry) ||
		errors.Is(err, errEncryptedArchive) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
