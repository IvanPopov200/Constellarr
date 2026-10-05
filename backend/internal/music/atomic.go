package music

import (
	"errors"
	"os"
)

var errNoReplaceUnsupported = errors.New("music: filesystem lacks atomic no-replace support")

// renameNoReplace moves a path without replacing an existing destination.
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

// publishNoReplace publishes a staged file without replacing a concurrent arrival.
func publishNoReplace(root *os.Root, tempRel, destRel string) error {
	return renameNoReplace(root, tempRel, destRel)
}
