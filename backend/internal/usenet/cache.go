package usenet

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
)

// Cached parts use a length-prefixed binary record so a corrupt file cannot demand a huge allocation.
const (
	cacheDirName = ".parts"
	cacheMagic   = "USGC1"
)

// segment is one decoded yEnc part; Data is transient.
type segment struct {
	MessageID string
	FileName  string
	FileSize  int64
	Part      int64
	Total     int64
	PartBegin int64
	PartSize  int64
	CRC       uint32
	HasCRC    bool
	Data      []byte
}

func (s segment) valid() bool {
	if !validMessageID(s.MessageID) || len(s.FileName) > maxFileNameLen {
		return false
	}
	if s.FileSize <= 0 || s.FileSize > maxFileBytes || s.PartSize <= 0 || s.PartSize > maxArticleBytes {
		return false
	}
	if s.PartBegin < 0 || s.PartBegin+s.PartSize > s.FileSize {
		return false
	}
	if s.Total > 0 {
		if s.Part < 1 || s.Part > s.Total || s.Total > maxSegmentsPerFile {
			return false
		}
	} else if s.Part != 0 || s.PartBegin != 0 || s.PartSize != s.FileSize {
		return false
	}
	if int64(len(s.Data)) != s.PartSize {
		return false
	}
	if s.HasCRC && crc32.ChecksumIEEE(s.Data) != s.CRC {
		return false
	}
	return true
}

func (s segment) meta() segment {
	s.Data = nil
	return s
}

func cachePath(partsDir, messageID string) string {
	sum := sha256.Sum256([]byte(messageID))
	return filepath.Join(partsDir, "seg-"+hex.EncodeToString(sum[:16])+".part")
}

func encodeSegment(s segment) []byte {
	var buf bytes.Buffer
	buf.WriteString(cacheMagic)
	writeString(&buf, s.MessageID)
	writeString(&buf, s.FileName)
	for _, v := range []int64{s.FileSize, s.Part, s.Total, s.PartBegin, s.PartSize} {
		_ = binary.Write(&buf, binary.BigEndian, v)
	}
	_ = binary.Write(&buf, binary.BigEndian, s.CRC)
	if s.HasCRC {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(s.Data)))
	buf.Write(s.Data)
	return buf.Bytes()
}

func writeString(buf *bytes.Buffer, s string) {
	_ = binary.Write(buf, binary.BigEndian, uint16(len(s)))
	buf.WriteString(s)
}

func decodeSegment(raw []byte) (segment, error) {
	var s segment
	r := bytes.NewReader(raw)
	magic := make([]byte, len(cacheMagic))
	if _, err := r.Read(magic); err != nil || string(magic) != cacheMagic {
		return s, errors.New("bad cache header")
	}
	var err error
	if s.MessageID, err = readString(r); err != nil {
		return s, err
	}
	if s.FileName, err = readString(r); err != nil {
		return s, err
	}
	nums := make([]int64, 5)
	for i := range nums {
		if err = binary.Read(r, binary.BigEndian, &nums[i]); err != nil {
			return s, err
		}
	}
	s.FileSize, s.Part, s.Total, s.PartBegin, s.PartSize = nums[0], nums[1], nums[2], nums[3], nums[4]
	if err = binary.Read(r, binary.BigEndian, &s.CRC); err != nil {
		return s, err
	}
	flag, err := r.ReadByte()
	if err != nil {
		return s, err
	}
	s.HasCRC = flag == 1
	var dataLen uint32
	if err = binary.Read(r, binary.BigEndian, &dataLen); err != nil {
		return s, err
	}
	if int64(dataLen) > maxArticleBytes || r.Len() != int(dataLen) {
		return segment{}, errors.New("bad cache payload length")
	}
	s.Data = raw[len(raw)-r.Len():]
	if !s.valid() {
		return segment{}, errors.New("invalid cached segment")
	}
	return s, nil
}

func readString(r *bytes.Reader) (string, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "", err
	}
	if int(n) > r.Len() {
		return "", errors.New("bad cache string length")
	}
	buf := make([]byte, n)
	if _, err := r.Read(buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// writeSegment fsyncs before rename so resume cannot trust a torn write.
func writeSegment(partsDir string, s segment) error {
	path := cachePath(partsDir, s.MessageID)
	f, err := os.CreateTemp(partsDir, ".tmp-seg-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(encodeSegment(s)); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readSegment returns a cached part only when its bounds and CRC still verify.
func readSegment(partsDir, messageID string) (segment, bool) {
	path := cachePath(partsDir, messageID)
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 || info.Size() > maxArticleBytes+4096 {
		return segment{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return segment{}, false
	}
	s, err := decodeSegment(raw)
	if err != nil || s.MessageID != messageID {
		return segment{}, false
	}
	return s, true
}

func loadCachedSegments(partsDir string, wanted map[string]struct{}) map[string]segment {
	cached := make(map[string]segment, len(wanted))
	for id := range wanted {
		if s, ok := readSegment(partsDir, id); ok {
			cached[id] = s.meta()
		}
	}
	return cached
}

func hashed(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:8])
}
