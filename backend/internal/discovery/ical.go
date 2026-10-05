package discovery

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// CalendarICS renders the merged calendar as iCalendar with CRLF folding and stable date-only UIDs.
func CalendarICS(view CalendarView, now time.Time) []byte {
	var out bytes.Buffer
	for _, line := range []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Constellarr//Discovery//EN",
		"CALSCALE:GREGORIAN", "METHOD:PUBLISH", "X-WR-CALNAME:Constellarr",
	} {
		appendICalLine(&out, line)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	for _, entry := range view.Entries {
		date, err := time.Parse(dateLayout, entry.Date)
		if err != nil {
			continue
		}
		summary := entry.Title
		if entry.Subtitle != "" {
			summary = summary + " — " + entry.Subtitle
		}
		for _, line := range []string{
			"BEGIN:VEVENT",
			"UID:" + escapeICal("discovery-"+entry.ID+"@constellarr"),
			"DTSTAMP:" + stamp,
			"DTSTART;VALUE=DATE:" + date.Format("20060102"),
			"DTEND;VALUE=DATE:" + date.AddDate(0, 0, 1).Format("20060102"),
			"SUMMARY:" + escapeICal(summary),
			"CATEGORIES:" + escapeICal(entry.MediaType),
			"END:VEVENT",
		} {
			appendICalLine(&out, line)
		}
	}
	appendICalLine(&out, "END:VCALENDAR")
	return out.Bytes()
}

// escapeICal removes CR and LF so a title can never inject extra calendar properties.
func escapeICal(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return strings.NewReplacer("\r\n", `\n`, "\r", `\n`, "\n", `\n`).Replace(value)
}

// appendICalLine writes one CRLF-terminated line folded to the 75-octet limit.
func appendICalLine(out *bytes.Buffer, line string) {
	limit := 75
	for {
		cut := len(line)
		if cut > limit {
			cut = limit
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
		}
		out.WriteString(line[:cut])
		line = line[cut:]
		if line == "" {
			break
		}
		out.WriteString("\r\n ")
		limit = 74
	}
	out.WriteString("\r\n")
}
