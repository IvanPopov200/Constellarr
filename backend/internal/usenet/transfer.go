package usenet

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/javi11/nntppool/v5"
)

const (
	segmentTimeout = 60 * time.Second
	maxAttempts    = 2
)

type segmentTask struct {
	file      int
	messageID string
}

type segmentResult struct {
	task  segmentTask
	seg   segment
	err   error
	hard  bool
	fatal bool
}

type partRange struct {
	begin int64
	end   int64
}

type fileState struct {
	index   int
	nzb     *nzbFile
	name    string
	size    int64
	ranges  []partRange
	parts   []segment
	missing int
}

// assemblyName prefers the NZB subject name; obfuscated posts vary the yEnc name per part.
func (st *fileState) assemblyName(s segment) string {
	if st.nzb.name != "" {
		return st.nzb.name
	}
	if s.FileName != "" {
		return s.FileName
	}
	return fmt.Sprintf("file-%d.bin", st.index+1)
}

func (st *fileState) accept(s segment) error {
	name := st.assemblyName(s)
	if st.size == 0 {
		st.name = name
		st.size = s.FileSize
	} else {
		if s.FileSize != st.size {
			return fmt.Errorf("file %q part %d announces %d bytes, want %d", st.name, s.Part, s.FileSize, st.size)
		}
		// Without a subject name the per-part yEnc name is the only identity, so it must stay consistent.
		if st.nzb.name == "" && !strings.EqualFold(name, st.name) {
			return fmt.Errorf("file %q part %d names %q", st.name, s.Part, name)
		}
	}
	i := sort.Search(len(st.ranges), func(i int) bool { return st.ranges[i].begin >= s.PartBegin })
	if i > 0 && st.ranges[i-1].end > s.PartBegin {
		return fmt.Errorf("file %q part %d overlaps part ending at %d", st.name, s.Part, st.ranges[i-1].end)
	}
	if i < len(st.ranges) && s.PartBegin+s.PartSize > st.ranges[i].begin {
		return fmt.Errorf("file %q part %d overlaps part starting at %d", st.name, s.Part, st.ranges[i].begin)
	}
	st.ranges = append(st.ranges, partRange{})
	copy(st.ranges[i+1:], st.ranges[i:])
	st.ranges[i] = partRange{begin: s.PartBegin, end: s.PartBegin + s.PartSize}
	st.parts = append(st.parts, s.meta())
	return nil
}

// coverage only applies when every segment was retrieved.
func (st *fileState) coverage() error {
	if st.missing > 0 {
		return nil
	}
	pos := int64(0)
	for _, r := range st.ranges {
		if r.begin != pos {
			return fmt.Errorf("file %q part coverage breaks at byte %d", st.name, pos)
		}
		pos = r.end
	}
	if pos != st.size {
		return fmt.Errorf("file %q parts cover %d of %d bytes", st.name, pos, st.size)
	}
	return nil
}

func assembleFile(dir, partsDir string, st *fileState) (string, error) {
	out := filepath.Join(dir, st.name)
	if filepath.Dir(out) != filepath.Clean(dir) {
		return "", fmt.Errorf("unsafe output name %q", st.name)
	}
	if err := st.coverage(); err != nil {
		return "", err
	}
	tmp := filepath.Join(partsDir, "out-"+hashed(st.name)+".tmp")
	// Read-write so a complete file can be re-read for CRC verification.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	for _, part := range st.parts {
		cs, ok := readSegment(partsDir, part.MessageID)
		if !ok {
			return "", fmt.Errorf("cached part %s for %q is unreadable", part.MessageID, st.name)
		}
		if _, err := f.WriteAt(cs.Data, cs.PartBegin); err != nil {
			return "", err
		}
	}
	if err := f.Truncate(st.size); err != nil {
		return "", err
	}
	if st.missing == 0 {
		if err := verifyParts(f, st); err != nil {
			return "", err
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	done = true
	return out, nil
}

func verifyParts(f *os.File, st *fileState) error {
	buf := make([]byte, 64<<10)
	for _, part := range st.parts {
		if !part.HasCRC {
			continue
		}
		h := crc32.NewIEEE()
		if _, err := io.CopyBuffer(h, io.NewSectionReader(f, part.PartBegin, part.PartSize), buf); err != nil {
			return err
		}
		if h.Sum32() != part.CRC {
			return fmt.Errorf("assembled part %d of %q failed its CRC", part.Part, st.name)
		}
	}
	return nil
}

func worker(ctx context.Context, clients []*nntppool.Client, partsDir string, cached map[string]segment, tasks <-chan segmentTask, results chan<- segmentResult) {
	for task := range tasks {
		if ctx.Err() != nil {
			return
		}
		if s, ok := cached[task.messageID]; ok {
			results <- segmentResult{task: task, seg: s}
			continue
		}
		s, err, hard := fetchSegment(ctx, clients, task.messageID)
		if err != nil {
			results <- segmentResult{task: task, err: err, hard: hard}
			continue
		}
		if err := writeSegment(partsDir, s); err != nil {
			results <- segmentResult{task: task, err: fmt.Errorf("cache write failed: %w", err), hard: true, fatal: true}
			continue
		}
		results <- segmentResult{task: task, seg: s.meta()}
	}
}

// fetchSegment tries each configured host with a real BODY before concluding the article is missing.
func fetchSegment(ctx context.Context, clients []*nntppool.Client, messageID string) (segment, error, bool) {
	actx, cancel := context.WithTimeout(ctx, segmentTimeout)
	defer cancel()
	var hardErr, damagedErr, lastErr error
	for _, client := range clients {
		if actx.Err() != nil {
			return segment{}, actx.Err(), true
		}
		s, err := fetchFromHost(actx, client, messageID)
		if err == nil {
			return s, nil, false
		}
		lastErr = err
		if hardError(err) {
			hardErr = err
		} else if damagedErr == nil {
			damagedErr = err
		}
	}
	if hardErr != nil {
		return segment{}, hardErr, true
	}
	if damagedErr != nil {
		return segment{}, damagedErr, false
	}
	if lastErr == nil {
		lastErr = errors.New("usenet: no provider hosts are configured")
	}
	return segment{}, lastErr, false
}

func fetchFromHost(ctx context.Context, client *nntppool.Client, messageID string) (segment, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		s, err := fetchOnce(ctx, client, messageID)
		if err == nil {
			return s, nil
		}
		lastErr = err
		if !retryable(err) {
			break
		}
	}
	return segment{}, lastErr
}

// errBadArticle counts as a missing segment, not a job failure.
var errBadArticle = errors.New("unusable yEnc article")

func fetchOnce(ctx context.Context, client *nntppool.Client, messageID string) (segment, error) {
	body, err := client.Fetch(ctx, nntppool.Req{MessageID: messageID})
	if err != nil {
		return segment{}, err
	}
	if body.Encoding != nntppool.EncodingYEnc {
		return segment{}, fmt.Errorf("%w: body is not yEnc", errBadArticle)
	}
	meta := body.YEnc
	s := segment{
		MessageID: messageID,
		FileName:  baseName(meta.FileName),
		FileSize:  meta.FileSize,
		Part:      meta.Part,
		Total:     meta.Total,
		PartBegin: meta.PartBegin,
		PartSize:  meta.PartSize,
		CRC:       body.CRC,
		HasCRC:    body.ExpectedCRC != 0,
		Data:      body.Bytes,
	}
	if !s.valid() {
		return segment{}, fmt.Errorf("%w: metadata or length out of bounds", errBadArticle)
	}
	return s, nil
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !errors.Is(err, nntppool.ErrArticleNotFound) &&
		!errors.Is(err, nntppool.ErrNoMessageID) &&
		!errors.Is(err, nntppool.ErrAuthRejected)
}

// hardError reports failures worth surfacing as a job error.
func hardError(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, nntppool.ErrArticleNotFound) &&
		!errors.Is(err, nntppool.ErrCRCMismatch) &&
		!errors.Is(err, errBadArticle) &&
		!errors.Is(err, context.Canceled)
}
