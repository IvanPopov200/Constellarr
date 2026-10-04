package quality

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	resolutionPattern = regexp.MustCompile(`(?i)\b(2160p|1080[pi]|720[pi]|576[pi]|480[pi]|uhdremux|4k|uhd)\b`)
	remuxPattern      = regexp.MustCompile(`(?i)\b(remux|bdremux|uhdremux)\b`)
	blurayPattern     = regexp.MustCompile(`(?i)\b(blu-?ray|bdrip|brrip|bdremux)\b`)
	webPattern        = regexp.MustCompile(`(?i)\bweb(?:[ ._-]?(?:dl|rip))?\b`)
	hdtvPattern       = regexp.MustCompile(`(?i)\b(hdtv|hdtvrip)\b`)
	dvdPattern        = regexp.MustCompile(`(?i)\b(dvdrip|dvd-r|dvd5|dvd9|dvd)\b`)
	sdPattern         = regexp.MustCompile(`(?i)\b(sdtv|sd)\b`)

	badQualityPatterns = []struct {
		pattern *regexp.Regexp
		id      string
	}{
		{regexp.MustCompile(`(?i)\b(hdcam|cam)\b`), "CAM"},
		{regexp.MustCompile(`(?i)\b(hdts|telesync|ts)\b`), "TS"},
		{regexp.MustCompile(`(?i)\b(telecine|tc)\b`), "TC"},
		{regexp.MustCompile(`(?i)\b(dvdscr|screener|scr)\b`), "SCR"},
		{regexp.MustCompile(`(?i)\br5\b`), "R5"},
		{regexp.MustCompile(`(?i)\bworkprint\b`), "WORKPRINT"},
	}

	codecX265  = regexp.MustCompile(`(?i)\b(x265|h\.?265|hevc)\b`)
	codecX264  = regexp.MustCompile(`(?i)\b(x264|h\.?264|avc)\b`)
	codecAV1   = regexp.MustCompile(`(?i)\bav1\b`)
	codecXvid  = regexp.MustCompile(`(?i)\bxvid\b`)
	codecDivx  = regexp.MustCompile(`(?i)\bdivx\b`)
	codecMPEG2 = regexp.MustCompile(`(?i)\bmpeg-?2\b`)
	codecVC1   = regexp.MustCompile(`(?i)\bvc-?1\b`)

	audioDTSHD  = regexp.MustCompile(`(?i)\bdts-?hd(?:-?ma)?\b`)
	audioTrueHD = regexp.MustCompile(`(?i)\btrue-?hd\b`)
	audioDTS    = regexp.MustCompile(`(?i)\bdts\b`)
	audioEAC3   = regexp.MustCompile(`(?i)\b(eac3|dd\+|ddp)`)
	audioAC3    = regexp.MustCompile(`(?i)\b(ac-?3|dd)`)
	audioAtmos  = regexp.MustCompile(`(?i)\batmos\b`)
	audioFLAC   = regexp.MustCompile(`(?i)\bflac\b`)
	audioOpus   = regexp.MustCompile(`(?i)\bopus\b`)
	audioAAC    = regexp.MustCompile(`(?i)\baac\b`)
	audioMP3    = regexp.MustCompile(`(?i)\bmp3\b`)

	hdr10Plus  = regexp.MustCompile(`(?i)\bhdr10\+\b`)
	hdrDV      = regexp.MustCompile(`(?i)\b(dv|dovi|dolby[ ._-]?vision)\b`)
	hdr10      = regexp.MustCompile(`(?i)\bhdr10\b`)
	hdrHLG     = regexp.MustCompile(`(?i)\bhlg\b`)
	hdrGeneric = regexp.MustCompile(`(?i)\bhdr\b`)

	languagePattern = regexp.MustCompile(`(?i)\b(english|eng|french|truefrench|vff|vostfr|german|ger|deutsch|spanish|spa|esp|castellano|italian|ita|portuguese|por|dublado|russian|rus|dutch|nld|japanese|jpn|korean|kor|chinese|chi|mandarin|cantonese|arabic|ara|hindi|hin|polish|pol|swedish|swe|norwegian|nor|danish|dan|finnish|fin|turkish|tur|czech|cze|ces|hungarian|hun|greek|gre|ell|hebrew|heb|thai|tha|vietnamese|vie|ukrainian|ukr|romanian|ron|rum|indonesian|ind|multi)\b`)

	properPattern = regexp.MustCompile(`(?i)\b(proper|repack|rerip)\b`)

	editionPatterns = []struct {
		pattern *regexp.Regexp
		name    string
	}{
		{regexp.MustCompile(`(?i)\bdirectors?[ ._-]*cut\b`), "Director's Cut"},
		{regexp.MustCompile(`(?i)\bultimate[ ._-]*edition\b`), "Ultimate Edition"},
		{regexp.MustCompile(`(?i)\bspecial[ ._-]*edition\b`), "Special Edition"},
		{regexp.MustCompile(`(?i)\banniversary[ ._-]*edition\b`), "Anniversary Edition"},
		{regexp.MustCompile(`(?i)\bfinal[ ._-]*cut\b`), "Final Cut"},
		{regexp.MustCompile(`(?i)\bextended\b`), "Extended"},
		{regexp.MustCompile(`(?i)\bunrated\b`), "Unrated"},
		{regexp.MustCompile(`(?i)\buncut\b`), "Uncut"},
		{regexp.MustCompile(`(?i)\btheatrical([ ._-]*cut)?\b`), "Theatrical"},
		{regexp.MustCompile(`(?i)\bremaster(ed)?\b`), "Remastered"},
		{regexp.MustCompile(`(?i)\bimax\b`), "IMAX"},
	}

	groupStopWords = map[string]bool{
		"DL": true, "HD": true, "MA": true, "DTS": true, "DDP": true, "DD": true, "HDR": true,
		"DV": true, "TS": true, "TC": true, "CAM": true, "SCR": true, "AVC": true, "HEVC": true,
		"X264": true, "X265": true, "H264": true, "H265": true, "AAC": true, "AC3": true,
		"EAC3": true, "TRUEHD": true, "ATMOS": true, "FLAC": true, "MP3": true, "OPUS": true,
		"PROPER": true, "REPACK": true, "RERIP": true, "REMUX": true, "BLURAY": true,
		"WEBDL": true, "WEBRIP": true, "WEB": true, "HDTV": true, "DVDRIP": true, "BRRIP": true,
		"BDRIP": true, "XVID": true, "DIVX": true, "AV1": true, "UHD": true, "SD": true,
		"4K": true, "R5": true, "IMAX": true, "EXTENDED": true, "UNCUT": true, "UNRATED": true,
		"REMASTERED": true, "MULTI": true, "FRENCH": true, "ENGLISH": true, "GERMAN": true,
		"SPANISH": true, "ITALIAN": true, "RUSSIAN": true, "DUTCH": true, "KOREAN": true,
		"JAPANESE": true, "CHINESE": true, "PORTUGUESE": true, "DUBBED": true, "VOSTFR": true,
	}
)

func Parse(title string) Details {
	title = strings.TrimSpace(title)
	details := Details{
		Resolution: parseResolution(title),
		Source:     parseSource(title),
		Codec:      parseCodec(title),
		Audio:      parseAudio(title),
		HDR:        parseHDR(title),
		Language:   parseLanguage(title),
		Group:      parseGroup(title),
		Edition:    parseEdition(title),
		Proper:     properPattern.MatchString(title),
	}
	if bad := parseBadQuality(title); bad != "" {
		details.Quality = bad
		return details
	}
	details.Quality = qualityID(details.Resolution, details.Source)
	if details.Resolution == 0 && (details.Quality == "DVD" || details.Quality == "SD") {
		details.Resolution = 480
	}
	return details
}

func parseResolution(title string) int {
	switch strings.ToLower(resolutionPattern.FindString(title)) {
	case "2160p", "uhdremux", "4k", "uhd":
		return 2160
	case "1080p", "1080i":
		return 1080
	case "720p", "720i":
		return 720
	case "576p", "576i", "480p", "480i":
		return 480
	}
	return 0
}

func parseSource(title string) string {
	switch {
	case remuxPattern.MatchString(title):
		return "Remux"
	case blurayPattern.MatchString(title):
		return "Bluray"
	case webPattern.MatchString(title):
		return "WEB"
	case hdtvPattern.MatchString(title):
		return "HDTV"
	case dvdPattern.MatchString(title):
		return "DVD"
	case sdPattern.MatchString(title):
		return "SD"
	}
	return ""
}

func parseBadQuality(title string) string {
	for _, bad := range badQualityPatterns {
		if bad.pattern.MatchString(title) {
			return bad.id
		}
	}
	return ""
}

func qualityID(resolution int, source string) string {
	switch source {
	case "DVD":
		return "DVD"
	case "SD":
		return "SD"
	case "Remux":
		if resolution == 2160 || resolution == 1080 || resolution == 720 {
			return "Remux-" + strconv.Itoa(resolution) + "p"
		}
	case "Bluray", "WEB", "HDTV":
		if resolution != 0 {
			return source + "-" + strconv.Itoa(resolution) + "p"
		}
		if source == "HDTV" {
			return "SD"
		}
	}
	return ""
}

func parseCodec(title string) string {
	switch {
	case codecX265.MatchString(title):
		return "x265"
	case codecX264.MatchString(title):
		return "x264"
	case codecAV1.MatchString(title):
		return "AV1"
	case codecXvid.MatchString(title):
		return "XviD"
	case codecDivx.MatchString(title):
		return "DivX"
	case codecMPEG2.MatchString(title):
		return "MPEG2"
	case codecVC1.MatchString(title):
		return "VC-1"
	}
	return ""
}

func parseAudio(title string) string {
	audio := ""
	switch {
	case audioDTSHD.MatchString(title):
		audio = "DTS-HD"
	case audioTrueHD.MatchString(title):
		audio = "TrueHD"
	case audioDTS.MatchString(title):
		audio = "DTS"
	case audioEAC3.MatchString(title):
		audio = "EAC3"
	case audioAC3.MatchString(title):
		audio = "AC3"
	case audioFLAC.MatchString(title):
		audio = "FLAC"
	case audioOpus.MatchString(title):
		audio = "Opus"
	case audioAAC.MatchString(title):
		audio = "AAC"
	case audioMP3.MatchString(title):
		audio = "MP3"
	}
	if audioAtmos.MatchString(title) {
		if audio == "" {
			return "Atmos"
		}
		return audio + " Atmos"
	}
	return audio
}

func parseHDR(title string) string {
	switch {
	case hdr10Plus.MatchString(title):
		return "HDR10+"
	case hdrDV.MatchString(title):
		return "DV"
	case hdr10.MatchString(title):
		return "HDR10"
	case hdrHLG.MatchString(title):
		return "HLG"
	case hdrGeneric.MatchString(title):
		return "HDR"
	}
	return ""
}

func parseLanguage(title string) string {
	language := ""
	for _, token := range languagePattern.FindAllString(title, -1) {
		code := languageCodes[strings.ToLower(token)]
		if code == "multi" {
			return "multi"
		}
		if code == "" {
			continue
		}
		if language != "" && language != code {
			return "multi"
		}
		language = code
	}
	return language
}

func parseEdition(title string) string {
	for _, edition := range editionPatterns {
		if edition.pattern.MatchString(title) {
			return edition.name
		}
	}
	return ""
}

func parseGroup(title string) string {
	index := strings.LastIndex(title, "-")
	if index < 0 {
		return ""
	}
	group := title[index+1:]
	if group == "" || len(group) > 32 {
		return ""
	}
	for i := 0; i < len(group); i++ {
		c := group[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return ""
		}
	}
	if groupStopWords[strings.ToUpper(group)] {
		return ""
	}
	return group
}

func normalizeLanguage(raw string) string {
	return languageCodes[strings.ToLower(strings.TrimSpace(raw))]
}
