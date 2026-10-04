package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	par2Magic        = "PAR2\x00PKT"
	par2FileDescType = "PAR 2.0\x00FileDesc"
	par2UniNameType  = "PAR 2.0\x00UniFileN"
	par2HeaderBytes  = 64
	par2MaxPackets   = 1 << 20
	par2MaxBodyBytes = 64 << 10
	par2Timeout      = time.Hour
)

var (
	errPAR2Unreadable = errors.New("PAR2 recovery data is unreadable; retry the download")
	errPAR2Names      = errors.New("PAR2 recovery data contains unusable file names; refusing to repair")
	par2VolumeName    = regexp.MustCompile(`(?i)\.vol[0-9]+[+\-][0-9]+\.par2$`)
)

func verifyAndRepair(ctx context.Context, dir string, missingSegments int, setStage func(string)) error {
	set, err := par2Set(dir)
	if err != nil {
		return err
	}
	if set == "" {
		if missingSegments > 0 {
			return fmt.Errorf("download has %d missing segment(s) and no PAR2 recovery data is available; retry the download", missingSegments)
		}
		return nil
	}
	repair, err := runPAR2(ctx, dir, "verify", set)
	if err != nil {
		return unrepaired(missingSegments, err)
	}
	if !repair {
		return nil
	}
	stage(setStage, stageRepairing)
	if _, err := runPAR2(ctx, dir, "repair", set); err != nil {
		return unrepaired(missingSegments, err)
	}
	return nil
}

func unrepaired(missingSegments int, err error) error {
	if missingSegments > 0 {
		return fmt.Errorf("download has %d missing segment(s) and PAR2 recovery could not repair the payload; retry the download: %w", missingSegments, err)
	}
	return err
}

// par2Set validates the file names embedded in every PAR2 file par2cmdline may load before picking one.
func par2Set(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", errors.New("download input directory could not be read")
	}
	var mains, volumes []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".par2") {
			continue
		}
		if !entry.Type().IsRegular() {
			return "", errPAR2Unreadable
		}
		names, err := par2Names(filepath.Join(dir, entry.Name()))
		if err != nil {
			return "", err
		}
		for _, name := range names {
			if !safePAR2Name(name) {
				return "", errPAR2Names
			}
		}
		if par2VolumeName.MatchString(entry.Name()) {
			volumes = append(volumes, entry.Name())
		} else {
			mains = append(mains, entry.Name())
		}
	}
	candidates := mains
	if len(candidates) == 0 {
		candidates = volumes
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Strings(candidates)
	return candidates[0], nil
}

// safePAR2Name normalizes Windows separators before confining the name to the input directory.
func safePAR2Name(name string) bool {
	if strings.ContainsRune(name, ':') {
		return false
	}
	if strings.ContainsRune(name, '\\') {
		name = strings.ReplaceAll(name, `\`, "/")
	}
	_, ok := cleanRel(name)
	return ok
}

func par2Names(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, errPAR2Unreadable
	}
	defer f.Close()
	header := make([]byte, par2HeaderBytes)
	var names []string
	for i := 0; i < par2MaxPackets; i++ {
		if _, err := io.ReadFull(f, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, errPAR2Unreadable
		}
		if string(header[:8]) != par2Magic {
			return nil, errPAR2Unreadable
		}
		size := int64(binary.LittleEndian.Uint64(header[8:16]))
		if size < par2HeaderBytes || size%4 != 0 {
			return nil, errPAR2Unreadable
		}
		body := size - par2HeaderBytes
		switch packetType := string(header[48:64]); packetType {
		case par2FileDescType, par2UniNameType:
			if body > par2MaxBodyBytes {
				return nil, errPAR2Unreadable
			}
			buf := make([]byte, body)
			if _, err := io.ReadFull(f, buf); err != nil {
				return nil, errPAR2Unreadable
			}
			if packetType == par2FileDescType {
				if len(buf) < 56 {
					return nil, errPAR2Unreadable
				}
				names = append(names, strings.TrimRight(string(buf[56:]), "\x00"))
			} else {
				if len(buf) < 32 {
					return nil, errPAR2Unreadable
				}
				names = append(names, par2UTF16Name(buf[32:]))
			}
		default:
			if _, err := f.Seek(body, io.SeekCurrent); err != nil {
				return nil, errPAR2Unreadable
			}
		}
	}
	if len(names) == 0 {
		return nil, errPAR2Unreadable
	}
	return names, nil
}

func par2UTF16Name(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:]))
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00")
}

// par2Candidates lists regular payload files so par2cmdline can match renamed data by content.
func par2Candidates(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || strings.HasSuffix(strings.ToLower(entry.Name()), ".par2") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// runPAR2 reports whether verification found repairable damage.
func runPAR2(ctx context.Context, dir, mode, file string) (bool, error) {
	bin, err := exec.LookPath("par2")
	if err != nil {
		return false, errors.New("par2 command line tool is not available; install par2cmdline")
	}
	candidates, err := par2Candidates(dir)
	if err != nil {
		return false, errors.New("download input directory could not be read")
	}
	runCtx, cancel := context.WithTimeout(ctx, par2Timeout)
	defer cancel()
	args := append([]string{mode, "-q", "-t2", "-m256", "--", file}, candidates...)
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(), "LC_ALL=C"}
	var out par2Output
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	if runErr == nil {
		return false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if runCtx.Err() != nil {
		return false, fmt.Errorf("PAR2 %s timed out", mode)
	}
	code := -1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = exitErr.ExitCode()
	}
	if code < 0 {
		return false, fmt.Errorf("PAR2 %s failed to run", mode)
	}
	if mode == "verify" && code == 1 {
		return true, nil
	}
	if mode == "verify" && code == 2 {
		if detail := out.detail(); detail != "" {
			return false, fmt.Errorf("PAR2 recovery data cannot repair the damaged payload: %s", detail)
		}
		return false, errors.New("PAR2 recovery data cannot repair the damaged payload")
	}
	if detail := out.detail(); detail != "" {
		return false, fmt.Errorf("PAR2 %s failed (exit code %d): %s", mode, code, detail)
	}
	return false, fmt.Errorf("PAR2 %s failed (exit code %d)", mode, code)
}

type par2Output struct {
	tail []byte
}

const par2OutputTail = 8 << 10

func (o *par2Output) Write(p []byte) (int, error) {
	o.tail = append(o.tail, p...)
	if len(o.tail) > par2OutputTail {
		o.tail = append(o.tail[:0], o.tail[len(o.tail)-par2OutputTail:]...)
	}
	return len(p), nil
}

func (o *par2Output) detail() string {
	text := strings.TrimSpace(string(o.tail))
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	var b strings.Builder
	for _, r := range text {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	detail := strings.TrimSpace(b.String())
	if len(detail) > 160 {
		detail = detail[:160]
	}
	return detail
}
