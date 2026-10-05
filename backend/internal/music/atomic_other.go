//go:build !darwin && !linux

package music

import "os"

func renameatNoReplace(root *os.Root, oldRel, newRel string) error {
	return errNoReplaceUnsupported
}
