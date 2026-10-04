//go:build linux

package library

import (
	"errors"
	"os"
	"path"
	"syscall"

	"golang.org/x/sys/unix"
)

func renameatNoReplacePinned(root *os.Root, oldRel, newRel string) error {
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
	return noReplaceError(unix.Renameat2(int(oldDir.Fd()), path.Base(oldRel), int(newDir.Fd()), path.Base(newRel), unix.RENAME_NOREPLACE))
}

func linkatPinned(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
	return unix.Linkat(int(oldDir.Fd()), oldName, int(newDir.Fd()), newName, 0)
}

func unlinkatCall(dir *os.File, name string) {
	unix.Unlinkat(int(dir.Fd()), name, 0)
}

func noReplaceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOSYS), errors.Is(err, syscall.EOPNOTSUPP):
		return errNoReplaceUnsupported
	}
	return err
}
