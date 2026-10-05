package subtitles

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Format is a supported subtitle container.
type Format string

const (
	FormatSRT Format = "srt"
	FormatVTT Format = "vtt"
	FormatASS Format = "ass"
	FormatSSA Format = "ssa"
)

// Cue is one subtitle cue with a stable identifier for translation round-trips.
type Cue struct {
	ID    string        `json:"id"`
	Start time.Duration `json:"-"`
	End   time.Duration `json:"-"`
	Text  string        `json:"text"`
}

type span struct{ start, end int }

type cueSpan struct {
	id       string
	start    time.Duration
	end      time.Duration
	text     span
	startTS  span
	endTS    span
	centsecs bool
}

// Document keeps the original bytes and cue field offsets so rewrites stay byte-exact elsewhere.
type Document struct {
	Format Format
	data   []byte
	cues   []cueSpan
}

const maxCueBytes = 8 << 10

// ParseDocument detects the subtitle format, validates it, and records cue offsets.
func ParseDocument(data []byte) (*Document, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: subtitle payload is empty", ErrInvalid)
	}
	if len(data) > maxSubtitleBytes {
		return nil, fmt.Errorf("%w: subtitle payload exceeds %d bytes", ErrInvalid, maxSubtitleBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: subtitle payload must be NUL-free UTF-8 text", ErrInvalid)
	}
	doc := &Document{Format: detectFormat(data), data: data}
	var err error
	switch doc.Format {
	case FormatVTT:
		err = doc.parseVTT()
	case FormatASS, FormatSSA:
		err = doc.parseASS()
	default:
		err = doc.parseSRT()
	}
	if err != nil {
		return nil, err
	}
	if len(doc.cues) == 0 {
		return nil, fmt.Errorf("%w: no subtitle cues were found", ErrInvalid)
	}
	for _, cue := range doc.cues {
		text := string(data[cue.text.start:cue.text.end])
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%w: cue %s has no text", ErrInvalid, cue.id)
		}
		if len(text) > maxCueBytes {
			return nil, fmt.Errorf("%w: cue %s exceeds %d bytes", ErrInvalid, cue.id, maxCueBytes)
		}
		if cue.end < cue.start {
			return nil, fmt.Errorf("%w: cue %s ends before it starts", ErrInvalid, cue.id)
		}
	}
	return doc, nil
}

// Cues returns the parsed cues with stable identifiers and original ordering.
func (d *Document) Cues() []Cue {
	cues := make([]Cue, 0, len(d.cues))
	for _, cue := range d.cues {
		cues = append(cues, Cue{
			ID:    cue.id,
			Start: cue.start,
			End:   cue.end,
			Text:  string(d.data[cue.text.start:cue.text.end]),
		})
	}
	return cues
}

// Apply replaces cue text only after every identifier matches exactly once and markup survives.
func (d *Document) Apply(texts map[string]string) ([]byte, error) {
	if len(texts) != len(d.cues) {
		return nil, fmt.Errorf("%w: expected %d cues, received %d", ErrCueIntegrity, len(d.cues), len(texts))
	}
	replacements := make([]struct {
		span span
		text []byte
	}, 0, len(d.cues))
	for _, cue := range d.cues {
		text, ok := texts[cue.id]
		if !ok {
			return nil, fmt.Errorf("%w: cue %s is missing", ErrCueIntegrity, cue.id)
		}
		if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return nil, fmt.Errorf("%w: cue %s has unusable text", ErrCueIntegrity, cue.id)
		}
		if len(text) > maxCueBytes {
			return nil, fmt.Errorf("%w: cue %s exceeds %d bytes", ErrCueIntegrity, cue.id, maxCueBytes)
		}
		source := string(d.data[cue.text.start:cue.text.end])
		if !equalMarkers(markers(source), markers(text)) {
			return nil, fmt.Errorf("%w: cue %s does not preserve inline formatting", ErrCueIntegrity, cue.id)
		}
		replacements = append(replacements, struct {
			span span
			text []byte
		}{cue.text, []byte(text)})
	}
	out := splice(d.data, replacements)
	check, err := ParseDocument(out)
	if err != nil {
		return nil, fmt.Errorf("%w: translated subtitle is invalid: %v", ErrCueIntegrity, err)
	}
	if len(check.cues) != len(d.cues) {
		return nil, fmt.Errorf("%w: translated subtitle changed the cue count", ErrCueIntegrity)
	}
	for i := range check.cues {
		if check.cues[i].start != d.cues[i].start || check.cues[i].end != d.cues[i].end {
			return nil, fmt.Errorf("%w: translated subtitle changed cue timing", ErrCueIntegrity)
		}
	}
	return out, nil
}

// Shift moves every cue by offset while leaving all other bytes untouched.
func (d *Document) Shift(offset time.Duration) ([]byte, error) {
	return d.rewrite(func(start, end time.Duration) (time.Duration, time.Duration) {
		return start + offset, end + offset
	})
}

// Scale converts cue timing between frame rates while leaving all other bytes untouched.
func (d *Document) Scale(from, to float64) ([]byte, error) {
	if from <= 0 || to <= 0 || from > 240 || to > 240 {
		return nil, fmt.Errorf("%w: frame rates must be between 0 and 240", ErrInvalid)
	}
	ratio := from / to
	if ratio < 0.5 || ratio > 2 {
		return nil, fmt.Errorf("%w: frame rate ratio %.3f is out of the supported range", ErrInvalid, ratio)
	}
	return d.rewrite(func(start, end time.Duration) (time.Duration, time.Duration) {
		return scaleDuration(start, ratio), scaleDuration(end, ratio)
	})
}

func scaleDuration(value time.Duration, ratio float64) time.Duration {
	return time.Duration(float64(value) * ratio)
}

func (d *Document) rewrite(times func(start, end time.Duration) (time.Duration, time.Duration)) ([]byte, error) {
	type edit struct {
		span span
		text string
	}
	edits := make([]edit, 0, len(d.cues)*2)
	for _, cue := range d.cues {
		start, end := times(cue.start, cue.end)
		if start < 0 {
			start = 0
		}
		if end < start {
			end = start
		}
		startText, err := d.formatTime(start, cue.centsecs)
		if err != nil {
			return nil, err
		}
		endText, err := d.formatTime(end, cue.centsecs)
		if err != nil {
			return nil, err
		}
		edits = append(edits, edit{cue.startTS, startText}, edit{cue.endTS, endText})
	}
	replacements := make([]struct {
		span span
		text []byte
	}, 0, len(edits))
	for _, edit := range edits {
		replacements = append(replacements, struct {
			span span
			text []byte
		}{edit.span, []byte(edit.text)})
	}
	out := splice(d.data, replacements)
	if _, err := ParseDocument(out); err != nil {
		return nil, fmt.Errorf("%w: rewritten subtitle is invalid: %v", ErrInvalid, err)
	}
	return out, nil
}

func (d *Document) formatTime(value time.Duration, centsecs bool) (string, error) {
	ms := int64(math.Round(float64(value) / float64(time.Millisecond)))
	hours := ms / 3_600_000
	if hours > 99 && !centsecs {
		return "", fmt.Errorf("%w: cue time %s exceeds 99 hours", ErrInvalid, value)
	}
	minutes := (ms / 60_000) % 60
	seconds := (ms / 1000) % 60
	millis := ms % 1000
	if centsecs {
		return fmt.Sprintf("%d:%02d:%02d.%02d", hours, minutes, seconds, millis/10), nil
	}
	separator := ","
	if d.Format == FormatVTT {
		separator = "."
	}
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", hours, minutes, seconds, separator, millis), nil
}

// splice applies non-overlapping replacements back to front so offsets stay valid.
func splice(data []byte, replacements []struct {
	span span
	text []byte
}) []byte {
	out := append([]byte(nil), data...)
	for i := len(replacements) - 1; i >= 0; i-- {
		replacement := replacements[i]
		next := make([]byte, 0, len(out)-replacement.span.end+replacement.span.start+len(replacement.text))
		next = append(next, out[:replacement.span.start]...)
		next = append(next, replacement.text...)
		next = append(next, out[replacement.span.end:]...)
		out = next
	}
	return out
}

func detectFormat(data []byte) Format {
	head := strings.TrimLeft(strings.TrimPrefix(string(data), "\ufeff"), " \t\r\n")
	if strings.HasPrefix(head, "WEBVTT") {
		return FormatVTT
	}
	window := data
	if len(window) > 4096 {
		window = window[:4096]
	}
	text := string(window)
	if strings.Contains(text, "[Script Info]") || strings.Contains(text, "[Events]") {
		if strings.Contains(text, "ScriptType: v4.00+") || strings.Contains(text, "[V4+ Styles]") {
			return FormatASS
		}
		return FormatSSA
	}
	return FormatSRT
}

func (d *Document) parseSRT() error {
	used := map[string]bool{}
	for _, block := range blocks(d.data) {
		lines := block.lines()
		if len(lines) == 1 && strings.HasPrefix(lines[0].text, "X-TIMESTAMP-MAP") {
			continue
		}
		if len(lines) < 2 {
			if len(lines) == 1 {
				return fmt.Errorf("%w: SRT cue %q is incomplete", ErrInvalid, lines[0].text)
			}
			continue
		}
		index := 0
		if isDigits(lines[0].text) {
			index = len(d.cues) + 1
			if parsed, err := strconv.Atoi(lines[0].text); err == nil {
				index = parsed
			}
			lines = lines[1:]
		}
		if len(lines) < 2 {
			return fmt.Errorf("%w: SRT cue %d has no text", ErrInvalid, index)
		}
		start, end, err := parseTimestampLine(lines[0].text, lines[0].start, lines[0].end)
		if err != nil {
			return err
		}
		text := lines[1:]
		span := span{text[0].start, text[len(text)-1].end}
		d.cues = append(d.cues, cueSpan{
			id:      uniqueID(strconv.Itoa(index), used),
			start:   start.value,
			end:     end.value,
			text:    span,
			startTS: start.span,
			endTS:   end.span,
		})
	}
	return nil
}

type parsedTime struct {
	value time.Duration
	span  span
}

// parseTimestampLine extracts both timestamps and their byte spans from an SRT or VTT timing line.
func parseTimestampLine(line string, offset int, lineEnd int) (parsedTime, parsedTime, error) {
	arrow := strings.Index(line, "-->")
	if arrow < 0 {
		return parsedTime{}, parsedTime{}, fmt.Errorf("%w: timing line %q has no --> separator", ErrInvalid, line)
	}
	left := strings.TrimSpace(line[:arrow])
	right := strings.TrimSpace(line[arrow+3:])
	if cut := strings.IndexAny(right, " \t"); cut >= 0 {
		right = right[:cut]
	}
	leftAt := offset + strings.Index(line, left)
	rightAt := offset + arrow + 3 + strings.Index(line[arrow+3:], right)
	start, ok := parseTime(left)
	if !ok {
		return parsedTime{}, parsedTime{}, fmt.Errorf("%w: unsupported cue start time %q", ErrInvalid, left)
	}
	end, ok := parseTime(right)
	if !ok {
		return parsedTime{}, parsedTime{}, fmt.Errorf("%w: unsupported cue end time %q", ErrInvalid, right)
	}
	return parsedTime{start, span{leftAt, leftAt + len(left)}},
		parsedTime{end, span{rightAt, rightAt + len(right)}}, nil
}

// parseTime accepts hh:mm:ss[.,]mmm, mm:ss.mmm, and ASS h:mm:ss.cc forms.
func parseTime(text string) (time.Duration, bool) {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ':' })
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	var hours, minutes int
	seconds := parts[len(parts)-1]
	frac := ""
	if cut := strings.IndexAny(seconds, ".,"); cut >= 0 {
		frac = seconds[cut+1:]
		seconds = seconds[:cut]
	}
	var ok bool
	if minutes, ok = parseDigits(parts[len(parts)-2]); !ok {
		return 0, false
	}
	if len(parts) == 3 {
		if hours, ok = parseDigits(parts[0]); !ok {
			return 0, false
		}
	}
	var secs int
	if secs, ok = parseDigits(seconds); !ok {
		return 0, false
	}
	if len(frac) > 3 {
		return 0, false
	}
	millis := 0
	switch len(frac) {
	case 0:
	case 1:
		millis, _ = strconv.Atoi(frac)
		millis *= 100
	case 2:
		millis, _ = strconv.Atoi(frac)
		millis *= 10
	default:
		millis, _ = strconv.Atoi(frac)
	}
	if minutes > 59 || secs > 59 || hours > 999 {
		return 0, false
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute +
		time.Duration(secs)*time.Second + time.Duration(millis)*time.Millisecond, true
}

func parseDigits(text string) (int, bool) {
	if text == "" || !isDigits(text) {
		return 0, false
	}
	value, err := strconv.Atoi(text)
	return value, err == nil
}

func isDigits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return text != ""
}

func (d *Document) parseVTT() error {
	text := strings.TrimPrefix(string(d.data), "\ufeff")
	if !strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "WEBVTT") {
		return fmt.Errorf("%w: VTT payload must start with WEBVTT", ErrInvalid)
	}
	headerEnd := strings.IndexAny(text, "\r\n")
	if headerEnd < 0 {
		return fmt.Errorf("%w: VTT payload has no cues", ErrInvalid)
	}
	offset := len(d.data) - len(text) + headerEnd
	used := map[string]bool{}
	for _, block := range blocks(d.data[offset:]) {
		block = block.shift(offset)
		lines := block.lines()
		if len(lines) == 0 {
			continue
		}
		switch strings.ToUpper(strings.SplitN(lines[0].text, " ", 2)[0]) {
		case "NOTE", "STYLE", "REGION":
			continue
		}
		id := ""
		if !strings.Contains(lines[0].text, "-->") {
			id = lines[0].text
			lines = lines[1:]
		}
		if len(lines) < 2 {
			return fmt.Errorf("%w: VTT cue %q has no text", ErrInvalid, id)
		}
		start, end, err := parseTimestampLine(lines[0].text, lines[0].start, lines[0].end)
		if err != nil {
			return err
		}
		body := lines[1:]
		d.cues = append(d.cues, cueSpan{
			id:      uniqueID(fmt.Sprintf("c%d", len(d.cues)+1), used),
			start:   start.value,
			end:     end.value,
			text:    span{body[0].start, body[len(body)-1].end},
			startTS: start.span,
			endTS:   end.span,
		})
	}
	return nil
}

func (d *Document) parseASS() error {
	eventsAt := strings.Index(string(d.data), "[Events]")
	if eventsAt < 0 {
		return fmt.Errorf("%w: subtitle payload has no [Events] section", ErrInvalid)
	}
	columns := []string{}
	used := map[string]bool{}
	for _, block := range blocks(d.data[eventsAt:]) {
		block = block.shift(eventsAt)
		for _, entry := range block.lines() {
			lower := strings.ToLower(entry.text)
			switch {
			case strings.HasPrefix(lower, "format:"):
				fields := strings.Split(strings.TrimSpace(entry.text[len("format:"):]), ",")
				columns = columns[:0]
				for _, field := range fields {
					columns = append(columns, strings.TrimSpace(field))
				}
			case strings.HasPrefix(lower, "dialogue:"):
				if len(columns) == 0 {
					columns = defaultASSColumns
				}
				dialogue := entry.text[len("dialogue:"):]
				fields, spans := splitFields(dialogue, entry.start+len("dialogue:"), len(columns))
				textIndex, startIndex, endIndex := assColumnIndexes(columns)
				if textIndex < 0 || startIndex < 0 || endIndex < 0 {
					return fmt.Errorf("%w: ASS event format has no Start, End, and Text columns", ErrInvalid)
				}
				start, ok := parseTime(fields[startIndex])
				if !ok {
					return fmt.Errorf("%w: unsupported ASS start time %q", ErrInvalid, fields[startIndex])
				}
				end, ok := parseTime(fields[endIndex])
				if !ok {
					return fmt.Errorf("%w: unsupported ASS end time %q", ErrInvalid, fields[endIndex])
				}
				d.cues = append(d.cues, cueSpan{
					id:       uniqueID(fmt.Sprintf("d%d", len(d.cues)+1), used),
					start:    start,
					end:      end,
					text:     spans[textIndex],
					startTS:  spans[startIndex],
					endTS:    spans[endIndex],
					centsecs: true,
				})
			}
		}
	}
	return nil
}

var defaultASSColumns = []string{"Layer", "Start", "End", "Style", "Name", "MarginL", "MarginR", "MarginV", "Effect", "Text"}

func assColumnIndexes(columns []string) (text, start, end int) {
	text, start, end = -1, -1, -1
	for i, column := range columns {
		switch strings.ToLower(column) {
		case "text":
			text = i
		case "start":
			start = i
		case "end":
			end = i
		}
	}
	return text, start, end
}

// splitFields splits an ASS event row into its fixed columns, keeping commas inside the last column.
func splitFields(value string, offset int, count int) ([]string, []span) {
	fields := make([]string, 0, count)
	spans := make([]span, 0, count)
	cursor := 0
	for i := 0; i < count; i++ {
		next := strings.IndexByte(value[cursor:], ',')
		if next < 0 || i == count-1 {
			field := value[cursor:]
			trimmed := strings.TrimSpace(field)
			at := cursor + strings.Index(field, trimmed)
			fields = append(fields, trimmed)
			spans = append(spans, span{offset + at, offset + at + len(trimmed)})
			break
		}
		field := value[cursor : cursor+next]
		trimmed := strings.TrimSpace(field)
		at := cursor + strings.Index(field, trimmed)
		fields = append(fields, trimmed)
		spans = append(spans, span{offset + at, offset + at + len(trimmed)})
		cursor += next + 1
	}
	for len(fields) < count {
		fields = append(fields, "")
		spans = append(spans, span{offset + len(value), offset + len(value)})
	}
	return fields, spans
}

func uniqueID(candidate string, used map[string]bool) string {
	if !used[candidate] {
		used[candidate] = true
		return candidate
	}
	for i := 2; ; i++ {
		next := candidate + "-" + strconv.Itoa(i)
		if !used[next] {
			used[next] = true
			return next
		}
	}
}

// markers returns inline markup tokens in source order.
func markers(text string) []string {
	found := []string{}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '<':
			end := strings.IndexByte(text[i:], '>')
			if end < 0 {
				continue
			}
			found = append(found, text[i:i+end+1])
			i += end
		case '{':
			end := strings.IndexByte(text[i:], '}')
			if end < 0 {
				continue
			}
			found = append(found, text[i:i+end+1])
			i += end
		}
	}
	return found
}

func equalMarkers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type block struct {
	data []byte
	base int
}

func (b block) shift(offset int) block {
	return block{data: b.data, base: b.base + offset}
}

func (b block) lines() []line {
	out := []line{}
	cursor := b.base
	for _, raw := range strings.Split(string(b.data), "\n") {
		text := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			start := cursor + strings.Index(text, trimmed)
			out = append(out, line{text: trimmed, start: start, end: start + len(trimmed)})
		}
		cursor += len(raw) + 1
	}
	return out
}

type line struct {
	text  string
	start int
	end   int
}

// blocks splits a payload on blank lines and keeps byte offsets relative to base.
func blocks(data []byte) []block {
	out := []block{}
	start := 0
	cursor := 0
	for _, raw := range strings.Split(string(data), "\n") {
		lineLen := len(raw)
		if strings.TrimSpace(strings.TrimSuffix(raw, "\r")) == "" {
			if cursor > start {
				out = append(out, block{data: data[start:cursor], base: start})
			}
			start = cursor + lineLen + 1
		}
		cursor += lineLen + 1
	}
	if cursor > start {
		out = append(out, block{data: data[start:cursor], base: start})
	}
	return out
}
