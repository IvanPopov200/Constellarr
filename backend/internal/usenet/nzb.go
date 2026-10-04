package usenet

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	maxNZBSize         = 64 << 20
	maxNZBFiles        = 1024
	maxSegmentsPerFile = 200000
	maxTotalSegments   = 500000
	maxArticleBytes    = 16 << 20
	maxFileBytes       = 100 << 30
	maxTotalBytes      = 1 << 40
	maxMessageIDLen    = 250
	maxFileNameLen     = 200
)

type nzbSegment struct {
	messageID string
	bytes     int64
	number    int
}

type nzbFile struct {
	subject  string
	name     string
	segments []nzbSegment
}

func parseNZB(data []byte) ([]nzbFile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("nzb is empty")
	}
	if len(data) > maxNZBSize {
		return nil, fmt.Errorf("nzb exceeds %d bytes", maxNZBSize)
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	var (
		files      []nzbFile
		fileIndex  = -1
		segIndex   = -1
		totalSegs  int
		totalBytes int64
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse nzb: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "file":
				if len(files) >= maxNZBFiles {
					return nil, fmt.Errorf("nzb has more than %d files", maxNZBFiles)
				}
				var subject string
				for _, attr := range t.Attr {
					if attr.Name.Local == "subject" {
						subject = attr.Value
					}
				}
				files = append(files, nzbFile{subject: subject, name: subjectFileName(subject)})
				fileIndex = len(files) - 1
				segIndex = -1
			case "segment":
				if fileIndex < 0 {
					continue
				}
				if len(files[fileIndex].segments) >= maxSegmentsPerFile {
					return nil, fmt.Errorf("nzb file %d has more than %d segments", fileIndex+1, maxSegmentsPerFile)
				}
				if totalSegs >= maxTotalSegments {
					return nil, fmt.Errorf("nzb has more than %d segments", maxTotalSegments)
				}
				seg := nzbSegment{}
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "bytes":
						seg.bytes, _ = strconv.ParseInt(attr.Value, 10, 64)
					case "number":
						seg.number, _ = strconv.Atoi(attr.Value)
					}
				}
				seg.bytes = min(max(seg.bytes, 0), maxArticleBytes)
				files[fileIndex].segments = append(files[fileIndex].segments, seg)
				segIndex = len(files[fileIndex].segments) - 1
				totalSegs++
				totalBytes += seg.bytes
			}
		case xml.CharData:
			if fileIndex >= 0 && segIndex >= 0 && files[fileIndex].segments[segIndex].messageID == "" {
				files[fileIndex].segments[segIndex].messageID = strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			if t.Name.Local == "segment" {
				segIndex = -1
			}
		}
	}

	if totalBytes > maxTotalBytes {
		return nil, fmt.Errorf("nzb advertises more than %d bytes", int64(maxTotalBytes))
	}
	if len(files) == 0 {
		return nil, errors.New("nzb has no files")
	}
	for i := range files {
		f := &files[i]
		if len(f.segments) == 0 {
			return nil, fmt.Errorf("nzb file %s has no segments", fileLabel(f))
		}
		for j := range f.segments {
			if !validMessageID(f.segments[j].messageID) {
				return nil, fmt.Errorf("nzb file %s has an invalid message id", fileLabel(f))
			}
		}
		sort.SliceStable(f.segments, func(a, b int) bool { return f.segments[a].number < f.segments[b].number })
	}

	seen := make(map[string]int, len(files))
	for i := range files {
		name := files[i].name
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("nzb files %d and %d both name %q", prev+1, i+1, name)
		}
		seen[key] = i
	}
	return files, nil
}

func fileLabel(f *nzbFile) string {
	if f.name != "" {
		return fmt.Sprintf("%q", f.name)
	}
	if f.subject != "" {
		return fmt.Sprintf("%q", truncate(f.subject, 60))
	}
	return "without a subject"
}

// validMessageID keeps CRLF out of the BODY command.
func validMessageID(id string) bool {
	if len(id) < 3 || len(id) > maxMessageIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c < 0x21 || c > 0x7e || c == '<' || c == '>' {
			return false
		}
	}
	return true
}

// subjectFileName handles subjects like `[1/3] - "movie.mkv" yEnc (1/2)`.
func subjectFileName(subject string) string {
	if i := strings.Index(subject, `"`); i >= 0 {
		if j := strings.Index(subject[i+1:], `"`); j >= 0 {
			return baseName(subject[i+1 : i+1+j])
		}
	}
	s := subject
	if i := strings.Index(strings.ToLower(s), "yenc"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		if k := strings.Index(s, "]"); k >= 0 {
			s = strings.TrimSpace(s[k+1:])
		}
	}
	for _, field := range strings.Fields(s) {
		field = strings.Trim(field, "-_")
		if strings.Contains(field, ".") && !strings.ContainsAny(field, "()[]") {
			return baseName(field)
		}
	}
	return ""
}

// baseName reduces a name to a safe path element so traversal cannot survive.
func baseName(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	runes := []rune(name)
	if len(runes) > maxFileNameLen {
		name = string(runes[:maxFileNameLen])
	}
	return name
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
