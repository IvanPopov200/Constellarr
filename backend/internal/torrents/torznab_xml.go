package torrents

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

type torznabFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string        `xml:"title"`
		Items []torznabItem `xml:"item"`
	} `xml:"channel"`
}

type torznabItem struct {
	Title     string           `xml:"title"`
	GUID      string           `xml:"guid"`
	Link      string           `xml:"link"`
	PubDate   string           `xml:"pubDate"`
	SizeBytes int64            `xml:"size"`
	Category  []string         `xml:"category"`
	Enclosure torznabEnclosure `xml:"enclosure"`
	Attrs     []torznabAttr    `xml:"attr"`
}

type torznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (item torznabItem) attr(name string) string {
	for _, attr := range item.Attrs {
		if strings.EqualFold(attr.Name, name) {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

// magnetURL returns the feed's complete magnet link, including tracker passkeys.
func (item torznabItem) magnetURL() string {
	candidates := []string{item.attr("magneturl"), item.Link, item.Enclosure.URL}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if !strings.HasPrefix(strings.ToLower(candidate), "magnet:") {
			continue
		}
		if _, err := parseMagnet(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// privateHint reports a feed marked as tracker-private, which pins the job away from DHT and PEX.
func (item torznabItem) privateHint() bool {
	switch strings.ToLower(item.attr("private")) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// hash reports the v1 info hash of a result, from its attribute or its magnet link.
func (item torznabItem) hash() string {
	if value := strings.ToLower(item.attr("infohash")); validInfoHash(value) {
		return value
	}
	if magnet := item.magnetURL(); magnet != "" {
		if hash, err := parseMagnet(magnet); err == nil {
			return hash
		}
	}
	return ""
}

func (item torznabItem) size() int64 {
	if item.SizeBytes > 0 {
		return item.SizeBytes
	}
	if item.Enclosure.Length > 0 {
		return item.Enclosure.Length
	}
	if value, err := strconv.ParseInt(item.attr("size"), 10, 64); err == nil && value > 0 {
		return value
	}
	return 0
}

func (item torznabItem) counts() (int, int) {
	seeders, leechers := -1, -1
	if value, err := strconv.Atoi(item.attr("seeders")); err == nil {
		seeders = value
	}
	if value, err := strconv.Atoi(item.attr("leechers")); err == nil {
		leechers = value
	}
	if seeders < 0 {
		if value, err := strconv.Atoi(item.attr("peers")); err == nil {
			seeders = value
		}
	}
	return seeders, leechers
}

func (item torznabItem) published() time.Time {
	value := strings.TrimSpace(item.PubDate)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func (item torznabItem) category() string {
	if name := item.attr("category"); name != "" {
		if _, err := strconv.Atoi(name); err != nil {
			return name
		}
	}
	if len(item.Category) > 0 {
		return item.Category[0]
	}
	return item.attr("category")
}

type torznabCaps struct {
	XMLName   xml.Name `xml:"caps"`
	Searching struct {
		Search      torznabCapability `xml:"search"`
		TVSearch    torznabCapability `xml:"tv-search"`
		MovieSearch torznabCapability `xml:"movie-search"`
		MusicSearch torznabCapability `xml:"music-search"`
		BookSearch  torznabCapability `xml:"book-search"`
	} `xml:"searching"`
}

type torznabCapability struct {
	Available string `xml:"available,attr"`
}

func (caps torznabCaps) available() []string {
	names := make([]string, 0, 5)
	for _, candidate := range []struct {
		name string
		cap  torznabCapability
	}{
		{"search", caps.Searching.Search},
		{"tv-search", caps.Searching.TVSearch},
		{"movie-search", caps.Searching.MovieSearch},
		{"music-search", caps.Searching.MusicSearch},
		{"book-search", caps.Searching.BookSearch},
	} {
		if strings.EqualFold(candidate.cap.Available, "yes") {
			names = append(names, candidate.name)
		}
	}
	return names
}
