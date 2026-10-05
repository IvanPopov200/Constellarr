package subtitles

import (
	"strings"
	"testing"
	"time"
)

const sampleSRT = "1\r\n00:00:01,000 --> 00:00:03,500\r\nHello <i>world</i>\r\nsecond line\r\n\r\n2\r\n00:01:00,000 --> 00:01:02,000\r\nBye\r\n"

func TestParseSRTApplyShiftAndScale(t *testing.T) {
	doc, err := ParseDocument([]byte(sampleSRT))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Format != FormatSRT || len(doc.Cues()) != 2 {
		t.Fatalf("format %s cues %d", doc.Format, len(doc.Cues()))
	}
	cues := doc.Cues()
	if cues[0].ID != "1" || !strings.HasPrefix(cues[0].Text, "Hello <i>world</i>") {
		t.Fatalf("first cue: %+v", cues[0])
	}
	translated := map[string]string{"1": "Hallo <i>Welt</i>\nzweite Zeile", "2": "Tschüss"}
	out, err := doc.Apply(translated)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), "00:00:01,000 --> 00:00:03,500") || !strings.Contains(string(out), "00:01:00,000 --> 00:01:02,000") {
		t.Fatalf("timestamps changed: %q", out)
	}
	if !strings.Contains(string(out), "Hallo <i>Welt</i>") {
		t.Fatalf("translated text missing: %q", out)
	}

	shifted, err := doc.Shift(1500 * time.Millisecond)
	if err != nil {
		t.Fatalf("shift: %v", err)
	}
	if !strings.Contains(string(shifted), "00:00:02,500 --> 00:00:05,000") {
		t.Fatalf("shifted timestamp wrong: %q", shifted)
	}
	scaled, err := doc.Scale(25, 23.976)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if !strings.Contains(string(scaled), "00:00:01,043") {
		t.Fatalf("scaled timestamp wrong: %q", scaled)
	}
}

func TestParseSRTRejectsBrokenCues(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"no timestamp":     "1\nHello\n",
		"no text":          "1\n00:00:01,000 --> 00:00:02,000\n",
		"end before start": "1\n00:00:05,000 --> 00:00:01,000\nText\n",
		"nul byte":         "1\n00:00:01,000 --> 00:00:02,000\nHe\x00llo\n",
	}
	for name, payload := range cases {
		if _, err := ParseDocument([]byte(payload)); err == nil {
			t.Fatalf("%s: expected a parse error", name)
		}
	}
	if _, err := ParseDocument([]byte{0xff, 0xfe, 0x00}); err == nil {
		t.Fatal("invalid UTF-8 must be rejected")
	}
}

func TestApplyRejectsCueIntegrityViolations(t *testing.T) {
	doc, err := ParseDocument([]byte(sampleSRT))
	if err != nil {
		t.Fatal(err)
	}
	cases := []map[string]string{
		{"1": "one"},
		{"1": "one", "2": "two", "3": "three"},
		{"1": "one", "9": "two"},
		{"1": "no markup here", "2": "two"},
		{"1": "", "2": "two"},
	}
	for _, texts := range cases {
		if _, err := doc.Apply(texts); err == nil {
			t.Fatalf("expected integrity failure for %v", texts)
		}
	}
}

func TestParseSRTDuplicateIndicesStayUnique(t *testing.T) {
	doc, err := ParseDocument([]byte("7\n00:00:01,000 --> 00:00:02,000\nA\n\n7\n00:00:03,000 --> 00:00:04,000\nB\n"))
	if err != nil {
		t.Fatal(err)
	}
	cues := doc.Cues()
	if cues[0].ID == cues[1].ID {
		t.Fatalf("duplicate cue ids: %v %v", cues[0].ID, cues[1].ID)
	}
	if _, err := doc.Apply(map[string]string{cues[0].ID: "A2", cues[1].ID: "B2"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func TestParseVTTWithIdentifierAndSettings(t *testing.T) {
	payload := "WEBVTT\n\nintro\n00:00:01.000 --> 00:00:03.000 align:start position:10%\nHi <c.yellow>there</c>\n\nNOTE a comment\n\n00:00:04.000 --> 00:00:05.000\nBye\n"
	doc, err := ParseDocument([]byte(payload))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Format != FormatVTT || len(doc.Cues()) != 2 {
		t.Fatalf("format %s cues %d", doc.Format, len(doc.Cues()))
	}
	out, err := doc.Apply(map[string]string{doc.Cues()[0].ID: "Hallo <c.yellow>du</c>", doc.Cues()[1].ID: "Tschüss"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), "align:start position:10%") || !strings.Contains(string(out), "00:00:04.000 --> 00:00:05.000") {
		t.Fatalf("vtt rewrite damaged timing or settings: %q", out)
	}
	shifted, err := doc.Shift(2 * time.Second)
	if err != nil || !strings.Contains(string(shifted), "00:00:03.000 --> 00:00:05.000") {
		t.Fatalf("vtt shift: %v %q", err, shifted)
	}
}

const sampleASS = `[Script Info]
ScriptType: v4.00+
Title: Sample

[V4+ Styles]
Format: Name, Fontname, Fontsize
Style: Default,Arial,20

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\i1}Hello, world{\i0}
Dialogue: 0,0:01:00.00,0:01:02.00,Default,,0,0,0,,Bye
`

func TestParseASSKeepsColumnsAndOverrides(t *testing.T) {
	doc, err := ParseDocument([]byte(sampleASS))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Format != FormatASS || len(doc.Cues()) != 2 {
		t.Fatalf("format %s cues %d", doc.Format, len(doc.Cues()))
	}
	if doc.Cues()[0].Text != `{\i1}Hello, world{\i0}` {
		t.Fatalf("text column: %q", doc.Cues()[0].Text)
	}
	out, err := doc.Apply(map[string]string{doc.Cues()[0].ID: `{\i1}Hallo, Welt{\i0}`, doc.Cues()[1].ID: "Tschüss"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(string(out), "Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\\i1}Hallo, Welt{\\i0}") {
		t.Fatalf("ASS rewrite damaged structure: %q", out)
	}
	shifted, err := doc.Shift(time.Second)
	if err != nil || !strings.Contains(string(shifted), "0:00:02.00,0:00:04.50") {
		t.Fatalf("ASS shift: %v %q", err, shifted)
	}
}

func TestShiftNeverProducesNegativeTimes(t *testing.T) {
	doc, err := ParseDocument([]byte("1\n00:00:01,000 --> 00:00:02,000\nText\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.Shift(-10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "00:00:00,000 --> 00:00:00,000") {
		t.Fatalf("negative shift not clamped: %q", out)
	}
}
