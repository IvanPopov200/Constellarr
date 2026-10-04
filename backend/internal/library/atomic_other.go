//go:build !linux && !darwin

package library

import "os"

func renameatNoReplacePinned(root *os.Root, oldRel, newRel string) error {
	return errNoReplaceUnsupported
}

func linkatPinned(oldDir *os.File, oldName string, newDir *os.File, newName string) error {
	return errNoReplaceUnsupported
}

func unlinkatCall(dir *os.File, name string) {}
