package library

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

var (
	partPattern    = regexp.MustCompile(`(?i)[ ._-]?(?:part|cd|disc|disk|pt)[ ._-]?(\d{1,2})$`)
	samplePattern  = regexp.MustCompile(`(?i)(^|[^a-z0-9])(sample|samples|trailer|trailers)([^a-z0-9]|$)`)
	resolutionName = regexp.MustCompile(`^\d{3,4}p$`)
	recycleNames   = map[string]bool{".recycle": true, "temp": true, "tmp": true, ".tmp": true, "sample": true, "samples": true, "trailer": true, "trailers": true}
)

var videoExtensions = map[string]bool{
	".mkv":  true,
	".mp4":  true,
	".avi":  true,
	".mov":  true,
	".m4v":  true,
	".webm": true,
	".mpeg": true,
	".mpg":  true,
	".ts":   true,
	".wmv":  true,
}

func isVideo(name string) bool {
	return videoExtensions[strings.ToLower(path.Ext(name))]
}

func isSampleOrTrailer(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		stem := strings.TrimSuffix(part, path.Ext(part))
		if samplePattern.MatchString(stem) {
			return true
		}
	}
	return false
}

func skipDir(name string) bool {
	return recycleNames[strings.ToLower(name)]
}

func baseNoExt(rel string) string {
	base := path.Base(rel)
	ext := path.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func partNumber(stem string) int {
	match := partPattern.FindStringSubmatch(stem)
	if match == nil {
		return 0
	}
	number := 0
	for _, r := range match[1] {
		number = number*10 + int(r-'0')
	}
	return number
}

func cleanStem(stem string) string {
	return partPattern.ReplaceAllString(stem, "")
}

func tokenize(stem string) []string {
	return strings.FieldsFunc(stem, func(r rune) bool {
		return r == '.' || r == '_' || r == ' ' || r == '[' || r == ']' || r == '(' || r == ')' || r == '{' || r == '}' || unicode.IsControl(r)
	})
}

var releaseJunk = map[string]bool{
	"4k": true, "uhd": true, "hd": true, "sd": true, "sdr": true, "hdr": true, "hdr10": true, "dv": true, "hlg": true,
	"bluray": true, "blu": true, "brrip": true, "bdrip": true, "bdremux": true, "remux": true, "webrip": true, "webdl": true,
	"web": true, "dl": true, "hdtv": true, "dvdrip": true, "dvd": true, "hdrip": true, "cam": true, "tc": true, "ts": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true, "xvid": true, "divx": true, "av1": true,
	"8bit": true, "10bit": true, "proper": true, "repack": true, "extended": true, "unrated": true, "imax": true,
	"remastered": true, "complete": true, "internal": true, "multi": true, "dual": true, "subs": true, "subbed": true, "dubbed": true,
}

var audioJunk = regexp.MustCompile(`^(?:dd|ddp|ddp5|ac3|eac3|dts|dtshd|truehd|atmos|aac|flac|mp3)[0-9.]*$`)

func isJunkToken(token string) bool {
	lower := strings.ToLower(token)
	if resolutionName.MatchString(lower) || releaseJunk[lower] || audioJunk.MatchString(lower) {
		return true
	}
	for _, piece := range strings.Split(lower, "-") {
		if releaseJunk[piece] || audioJunk.MatchString(piece) {
			return true
		}
	}
	return false
}
