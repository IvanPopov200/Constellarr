package media

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

const (
	kindRAR = "rar"
	kindZIP = "zip"
)

var (
	errRedirectedEntry  = errors.New("archive contains a redirected or non-regular file; refusing to extract")
	errEncryptedArchive = errors.New("archive is encrypted; encrypted downloads are not supported")
	partRARVolume       = regexp.MustCompile(`(?i)^(.*)\.part([0-9]+)\.rar$`)
)

type archiveSet struct {
	kind  string
	start string
}

func extract(ctx context.Context, in *os.Root, out string) ([]File, error) {
	entries, err := fs.ReadDir(in.FS(), ".")
	if err != nil {
		return nil, errors.New("download input directory could not be read")
	}
	w, err := newWriter(out)
	if err != nil {
		return nil, errors.New("download output directory could not be opened")
	}
	defer w.close()
	for _, set := range findArchives(entries) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch set.kind {
		case kindRAR:
			err = extractRAR(ctx, in, set.start, w)
		case kindZIP:
			err = extractZIP(ctx, in, set.start, w)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := copyLoose(ctx, in, entries, w); err != nil {
		return nil, err
	}
	return w.files, nil
}

// findArchives represents every RAR set by its first volume.
func findArchives(entries []fs.DirEntry) []archiveSet {
	type volume struct {
		base string
		name string
		num  int
	}
	var parts []volume
	partBase := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := partRARVolume.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		num, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		base := strings.ToLower(match[1])
		parts = append(parts, volume{base: base, name: entry.Name(), num: num})
		partBase[base] = true
	}
	first := make(map[string]volume)
	for _, part := range parts {
		current, ok := first[part.base]
		if !ok || part.num < current.num || (part.num == current.num && part.name < current.name) {
			first[part.base] = part
		}
	}
	var sets []archiveSet
	for _, part := range first {
		sets = append(sets, archiveSet{kind: kindRAR, start: part.name})
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".rar"):
			if partRARVolume.MatchString(name) || partBase[strings.TrimSuffix(lower, ".rar")] {
				continue
			}
			sets = append(sets, archiveSet{kind: kindRAR, start: name})
		case strings.HasSuffix(lower, ".zip"):
			sets = append(sets, archiveSet{kind: kindZIP, start: name})
		}
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].start < sets[j].start })
	return sets
}

func extractRAR(ctx context.Context, in *os.Root, name string, w *writer) error {
	rc, err := rardecode.OpenReader(name, rardecode.FileSystem(in.FS()))
	if err != nil {
		return rarError(ctx, err)
	}
	defer rc.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := rc.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return rarError(ctx, err)
		}
		if header.HeaderEncrypted || header.Encrypted {
			return errEncryptedArchive
		}
		if header.IsDir {
			continue
		}
		if header.Mode()&fs.ModeType != 0 || header.LinkType != rardecode.LinkTypeNone {
			return errRedirectedEntry
		}
		if !isMedia(header.Name) {
			continue
		}
		if err := w.copyFrom(ctx, header.Name, rc, header.UnPackedSize); err != nil {
			if passthrough(err) {
				return err
			}
			return rarError(ctx, err)
		}
	}
}

func extractZIP(ctx context.Context, in *os.Root, name string, w *writer) error {
	f, err := in.Open(name)
	if err != nil {
		return errors.New("ZIP archive could not be opened")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return errors.New("ZIP archive could not be opened")
	}
	reader, err := zip.NewReader(f, info.Size())
	if err != nil {
		return errors.New("ZIP archive could not be opened")
	}
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Flags&0x1 != 0 {
			return errEncryptedArchive
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&fs.ModeType != 0 || entry.ExternalAttrs>>16&0x400 != 0 {
			return errRedirectedEntry
		}
		if !isMedia(entry.Name) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return errors.New("ZIP archive is damaged or uses an unsupported compression method")
		}
		err = w.copyFrom(ctx, entry.Name, rc, int64(entry.UncompressedSize64))
		rc.Close()
		if err != nil {
			if passthrough(err) {
				return err
			}
			return errors.New("ZIP archive is damaged and could not be extracted")
		}
	}
	return nil
}

func copyLoose(ctx context.Context, in *os.Root, entries []fs.DirEntry, w *writer) error {
	for _, entry := range entries {
		if entry.IsDir() || !isMedia(entry.Name()) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := in.Open(entry.Name())
		if err != nil {
			return errors.New("download media file could not be read")
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return errors.New("download media file could not be read")
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return errRedirectedEntry
		}
		err = w.copyFrom(ctx, entry.Name(), f, info.Size())
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func rarError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted), errors.Is(err, rardecode.ErrBadPassword):
		return errEncryptedArchive
	case errors.Is(err, rardecode.ErrUnknownVersion), errors.Is(err, rardecode.ErrUnsupportedDecoder):
		return errors.New("archive uses an unsupported RAR version")
	default:
		return errors.New("archive is incomplete or damaged and could not be extracted")
	}
}
