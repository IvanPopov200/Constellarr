//go:build darwin

package music

import (
	"errors"
	"os"
	"path"
	"syscall"

	"golang.org/x/sys/unix"
)

func renameatNoReplace(root *os.Root, oldRel, newRel string) error {
	oldDir, err := root.Open(path.Dir(oldRel))
	if err != nil {
		return err
	}
	defer oldDir.Close()
	newDir, err := root.Open(path.Dir(newRel))
	if err != nil {
		return err
	}
	defer newDir.Close()
	err = unix.RenameatxNp(int(oldDir.Fd()), path.Base(oldRel), int(newDir.Fd()), path.Base(newRel), unix.RENAME_EXCL)
	return noReplaceError(err)
}

func noReplaceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOSYS), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
		return errNoReplaceUnsupported
	}
	return err
}
