package library

import (
	"errors"
	"os"
)

// errNoReplaceUnsupported reports a filesystem that cannot move paths without replacing.
var errNoReplaceUnsupported = errors.New("library: filesystem lacks atomic no-replace support")

// renameatNoReplace and linkatCall are test seams over the platform primitives.
var (
	renameatNoReplace = renameatNoReplacePinned
	linkatCall        = linkatPinned
)

// renameNoReplace moves a path through pinned parent directories and never replaces newRel.
func renameNoReplace(root *os.Root, oldRel, newRel string) error {
	err := renameatNoReplace(root, oldRel, newRel)
	if !errors.Is(err, errNoReplaceUnsupported) {
		return err
	}
	info, statErr := root.Lstat(oldRel)
	if statErr != nil {
		return statErr
	}
	if info.IsDir() {
		return errNoReplaceUnsupported
	}
	if err := root.Link(oldRel, newRel); err != nil {
		return err
	}
	if err := root.Remove(oldRel); err != nil {
		root.Remove(newRel)
		return err
	}
	return nil
}
