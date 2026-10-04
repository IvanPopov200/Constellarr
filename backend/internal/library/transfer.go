package library

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

const copyBufferBytes = 1 << 20

// publishAt is a test seam over the atomic no-replace publish move.
var publishAt = func(root *os.Root, tempRel, destRel string) error {
	return renameNoReplace(root, tempRel, destRel)
}

type digest [sha256.Size]byte

type archiveRecord struct {
	original string
	archived string
}

type publishedFile struct {
	rel  string
	info fs.FileInfo
}

type rollback struct {
	published  []publishedFile
	archives   []archiveRecord
	nfoWritten []publishedFile
}

func stage(ctx context.Context, p *plan, t *target, root, srcRoot *os.Root, created *[]string) error {
	dir := path.Dir(t.destRel)
	if err := mkdirAll(root, dir, created); err != nil {
		return err
	}
	tempRel := tempName(dir)
	if p.mode != ModeCopy && linkStaged(p, t, root, srcRoot, tempRel) == nil {
		t.tempRel = tempRel
		return nil
	}
	source, err := srcRoot.Open(t.srcRel)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := stageCopy(ctx, root, tempRel, source, t.size); err != nil {
		return err
	}
	t.tempRel = tempRel
	return nil
}

// linkStaged links through pinned directories and verifies the linked inode.
func linkStaged(p *plan, t *target, root, srcRoot *os.Root, tempRel string) error {
	source, err := srcRoot.Open(t.srcRel)
	if err != nil {
		return err
	}
	info, err := source.Stat()
	source.Close()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != t.size {
		return fmt.Errorf("%w: %s changed after planning", ErrSource, t.srcRel)
	}
	if p.sourceRoot == p.root {
		if err := root.Link(t.srcRel, tempRel); err != nil {
			return err
		}
		return verifyLinked(root, nil, t.srcRel, info, tempRel)
	}
	srcDir, err := srcRoot.Open(path.Dir(t.srcRel))
	if err != nil {
		return err
	}
	defer srcDir.Close()
	dstDir, err := root.Open(path.Dir(tempRel))
	if err != nil {
		return err
	}
	defer dstDir.Close()
	if err := linkatCall(srcDir, path.Base(t.srcRel), dstDir, path.Base(tempRel)); err != nil {
		return err
	}
	return verifyLinked(root, dstDir, t.srcRel, info, tempRel)
}

// verifyLinked checks the linked temp by inode and discards it through the pinned directory.
func verifyLinked(root *os.Root, dir *os.File, srcRel string, want fs.FileInfo, tempRel string) error {
	linked, err := root.Open(tempRel)
	var linkedInfo fs.FileInfo
	if err == nil {
		linkedInfo, err = linked.Stat()
		linked.Close()
	}
	if err == nil && linkedInfo.Mode().IsRegular() && os.SameFile(want, linkedInfo) {
		_, err = verifyFile(root, tempRel, want.Size())
		return err
	}
	if err == nil {
		err = fmt.Errorf("%w: hardlink for %s is not the planned file", ErrSource, srcRel)
	}
	if dir != nil {
		unlinkatCall(dir, path.Base(tempRel))
	} else {
		root.Remove(tempRel)
	}
	return err
}

func stageCopy(ctx context.Context, root *os.Root, tempRel string, src io.Reader, size int64) error {
	dst, err := root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, err := copyContext(ctx, dst, src, size)
	if err == nil {
		err = dst.Sync()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	if err == nil && (written <= 0 || written != size) {
		err = fmt.Errorf("%w: copied %d of %d bytes", ErrSource, written, size)
	}
	if err == nil {
		err = root.Chmod(tempRel, 0o644)
	}
	if err != nil {
		root.Remove(tempRel)
		return err
	}
	return nil
}

func adoptExisting(ctx context.Context, root *os.Root, t *target) (bool, error) {
	info, err := root.Lstat(t.destRel)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	return sameContent(ctx, root, t.tempRel, t.size, root, t.destRel, info.Size())
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	buffer := make([]byte, copyBufferBytes)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if written+int64(n) > size {
				return written, fmt.Errorf("%w: source grew while copying", ErrSource)
			}
			wrote, writeErr := dst.Write(buffer[:n])
			written += int64(wrote)
			if writeErr != nil || wrote != n {
				if writeErr != nil {
					return written, writeErr
				}
				return written, fmt.Errorf("%w: short write", ErrSource)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func sameContent(ctx context.Context, aRoot *os.Root, aRel string, aSize int64, bRoot *os.Root, bRel string, bSize int64) (bool, error) {
	if aSize != bSize || aSize <= 0 {
		return false, nil
	}
	aSum, err := hashFile(ctx, aRoot, aRel)
	if err != nil {
		return false, err
	}
	bSum, err := hashFile(ctx, bRoot, bRel)
	if err != nil {
		return false, err
	}
	return aSum == bSum, nil
}

func hashFile(ctx context.Context, root *os.Root, rel string) (digest, error) {
	var sum digest
	file, err := root.Open(rel)
	if err != nil {
		return sum, err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, copyBufferBytes)
	for {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return sum, readErr
		}
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func removeIfSame(root *os.Root, file publishedFile) {
	info, err := root.Lstat(file.rel)
	if err != nil || file.info == nil || !os.SameFile(file.info, info) {
		return
	}
	root.Remove(file.rel)
}

// restoreArchive puts an archived path back without replacing a concurrent arrival.
func restoreArchive(root *os.Root, a archiveRecord) {
	renameNoReplace(root, a.archived, a.original)
}

func deleteSources(srcRoot *os.Root, p *plan) error {
	var errs []error
	for _, t := range p.targets {
		if t.inPlace {
			continue
		}
		if err := srcRoot.Remove(t.srcRel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("library: removing source %s: %w", t.srcRel, err))
		}
	}
	return errors.Join(errs...)
}
