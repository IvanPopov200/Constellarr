package music

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// canonicalFormats orders formats from best to worst when a profile lists none.
var canonicalFormats = []string{"flac", "alac", "wav", "aiff", "ape", "dsd", "opus", "vorbis", "aac", "mp3", "wma"}

var losslessFormats = map[string]bool{
	"flac": true, "alac": true, "wav": true, "aiff": true, "ape": true, "dsd": true, "wavpack": true,
}

var knownFormats = map[string]bool{
	"flac": true, "alac": true, "wav": true, "aiff": true, "ape": true, "dsd": true, "wavpack": true,
	"opus": true, "vorbis": true, "aac": true, "mp3": true, "wma": true,
}

type formatInfo struct {
	Format      string
	BitrateKbps int
	Lossless    bool
	Media       string
	Bits        int
}

type currentQuality struct {
	Format      string
	BitrateKbps int
	Lossless    bool
	Score       int
}

// parseFormat reads the format, bitrate, and media of a release from its name.
func parseFormat(title string) formatInfo {
	tokens := formatTokens(title)
	info := formatInfo{}
	explicitBitrate, numericBitrate := 0, 0
	for _, token := range tokens {
		switch token {
		case "flac":
			info.Format = "flac"
		case "alac":
			info.Format = "alac"
		case "wav":
			info.Format = "wav"
		case "aiff", "aif":
			info.Format = "aiff"
		case "ape":
			info.Format = "ape"
		case "dsd", "dsf", "dff":
			info.Format = "dsd"
		case "wavpack", "wv":
			info.Format = "wavpack"
		case "opus":
			info.Format = "opus"
		case "ogg", "vorbis":
			info.Format = "vorbis"
		case "aac", "m4a", "m4b":
			info.Format = "aac"
		case "mp3", "mpeg", "lame":
			info.Format = "mp3"
		case "wma":
			info.Format = "wma"
		case "vinyl", "lp":
			info.Media = "vinyl"
		case "web", "webrip", "webdl":
			if info.Media == "" {
				info.Media = "web"
			}
		case "cd", "cdrip", "cdda", "eac", "flacrip":
			info.Media = "cd"
		case "sacd", "bluray", "bd":
			if info.Media == "" {
				info.Media = "digital"
			}
		case "v0", "vbrv0":
			if info.Format == "" {
				info.Format = "mp3"
			}
			if explicitBitrate == 0 {
				explicitBitrate = 245
			}
		case "v2", "vbrv2":
			if info.Format == "" {
				info.Format = "mp3"
			}
			if explicitBitrate == 0 {
				explicitBitrate = 190
			}
		case "24bit":
			info.Bits = 24
		case "16bit":
			info.Bits = 16
		default:
			// Sample-rate markers such as 24-96 are ignored once the release is lossless.
			if number, err := strconv.Atoi(token); err == nil && number >= 32 && number <= 1536 && number > numericBitrate {
				numericBitrate = number
			}
		}
	}
	if info.Lossless = losslessFormats[info.Format]; info.Lossless {
		info.BitrateKbps = 0
	} else {
		info.BitrateKbps = max(explicitBitrate, numericBitrate)
	}
	return info
}

func formatTokens(title string) []string {
	return strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func formatRank(profile QualityProfile, format string) int {
	listed := profileFormats(profile)
	for index, candidate := range listed {
		if candidate == format {
			return index
		}
	}
	return len(listed)
}

func profileFormats(profile QualityProfile) []string {
	if len(profile.Formats) == 0 {
		return canonicalFormats
	}
	return profile.Formats
}

// evaluate decides whether a release fits the profile and improves on the imported files.
func evaluate(profile QualityProfile, title string, size int64, current *currentQuality) Decision {
	info := parseFormat(title)
	decision := Decision{
		Format: info.Format, BitrateKbps: info.BitrateKbps, Lossless: info.Lossless, Media: info.Media,
		Allowed: true, Reasons: []string{},
	}
	formats := profileFormats(profile)
	rank := formatRank(profile, info.Format)
	decision.Rank = rank
	known := knownFormats[info.Format]
	if len(profile.Formats) > 0 && !known {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, "the release format could not be identified")
	}
	if len(profile.Formats) > 0 && rank >= len(formats) {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, "the release format is not in the profile")
	}
	if profile.LosslessOnly && !info.Lossless {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, "the profile requires a lossless release")
	}
	if !info.Lossless && profile.MinBitrateKbps > 0 && info.BitrateKbps < profile.MinBitrateKbps {
		decision.Allowed = false
		if info.BitrateKbps == 0 {
			decision.Reasons = append(decision.Reasons, "the release bitrate is unknown")
		} else {
			decision.Reasons = append(decision.Reasons, "the release bitrate is below the profile minimum")
		}
	}
	if profile.MinMB > 0 && size > 0 && size < int64(profile.MinMB*float64(1<<20)) {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, "the release size is below the profile minimum")
	}
	if profile.MaxMB > 0 && size > int64(profile.MaxMB*float64(1<<20)) {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, "the release size is above the profile maximum")
	}
	decision.Score = scoreFormat(formats, rank, info)
	if current != nil {
		decision.Upgrade = betterThanCurrent(profile, decision, info, *current)
		// With files in place only a real improvement is worth another download.
		if !decision.Upgrade {
			decision.Allowed = false
			decision.Reasons = append(decision.Reasons, "the current files already match this release quality")
		}
		if decision.Upgrade && !profile.Upgrade {
			decision.Allowed = false
			decision.Reasons = append(decision.Reasons, "the profile does not allow automatic upgrades")
		}
	}
	return decision
}

func scoreFormat(formats []string, rank int, info formatInfo) int {
	score := max(0, len(formats)-rank) * 100
	if info.Lossless {
		score += 40
	}
	if info.Bits >= 24 {
		score += 10
	}
	score += min(info.BitrateKbps/10, 32)
	switch info.Media {
	case "vinyl":
		score += 3
	case "cd":
		score += 2
	case "web":
		score += 1
	}
	return score
}

func betterThanCurrent(profile QualityProfile, decision Decision, info formatInfo, current currentQuality) bool {
	if current.Format == "" {
		return false
	}
	if decision.Rank != formatRank(profile, current.Format) {
		return decision.Rank < formatRank(profile, current.Format)
	}
	if info.Format == current.Format {
		if info.Lossless {
			return false
		}
		return info.BitrateKbps > current.BitrateKbps
	}
	return decision.Score > current.Score
}

func currentAtCutoff(profile QualityProfile, current currentQuality) bool {
	cutoff := strings.TrimSpace(strings.ToLower(profile.Cutoff))
	if cutoff == "" || cutoff == "any" {
		return true
	}
	if cutoff == "lossless" {
		return current.Lossless
	}
	if current.Format == cutoff {
		return true
	}
	return formatRank(profile, current.Format) <= formatRank(profile, cutoff)
}

// fileQuality scores an imported file against the profile.
func fileQuality(profile QualityProfile, format string, bitrate int, lossless bool) (int, bool) {
	formats := profileFormats(profile)
	rank := formatRank(profile, format)
	info := formatInfo{Format: format, BitrateKbps: bitrate, Lossless: lossless || losslessFormats[format]}
	allowed := true
	if len(profile.Formats) > 0 && rank >= len(formats) {
		allowed = false
	}
	if profile.LosslessOnly && !info.Lossless {
		allowed = false
	}
	if !info.Lossless && profile.MinBitrateKbps > 0 && bitrate < profile.MinBitrateKbps {
		allowed = false
	}
	return scoreFormat(formats, rank, info), allowed
}

func bestCurrent(profile QualityProfile, files []File) *currentQuality {
	var best *currentQuality
	for _, file := range files {
		if file.Missing {
			continue
		}
		score, _ := fileQuality(profile, file.Format, file.BitrateKbps, file.Lossless)
		candidate := currentQuality{Format: file.Format, BitrateKbps: file.BitrateKbps, Lossless: file.Lossless, Score: score}
		if best == nil || candidate.Score > best.Score {
			value := candidate
			best = &value
		}
	}
	return best
}

func albumAtCutoff(profile QualityProfile, files []File) bool {
	current := bestCurrent(profile, files)
	if current == nil {
		return false
	}
	return currentAtCutoff(profile, *current)
}

// releaseMatchesAlbum compares normalized identities and never guesses across artists.
func releaseMatchesAlbum(album Album, release Release) bool {
	if album.MusicBrainzID != "" && strings.EqualFold(strings.TrimSpace(release.Album), strings.TrimSpace(album.MusicBrainzID)) {
		return true
	}
	title := normalizeText(album.Title)
	if title == "" {
		return false
	}
	haystack := normalizeText(release.Title)
	if !strings.Contains(haystack, title) {
		return false
	}
	if artist := normalizeText(album.ArtistName); artist != "" && !strings.Contains(haystack, artist) {
		return false
	}
	if album.Year > 0 && release.Year > 0 && abs(release.Year-album.Year) > 1 {
		return false
	}
	return true
}

func normalizeText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func sortReleases(releases []Release) {
	sort.SliceStable(releases, func(i, j int) bool {
		left, right := releases[i].Decision, releases[j].Decision
		if left.Allowed != right.Allowed {
			return left.Allowed
		}
		if left.Upgrade != right.Upgrade {
			return left.Upgrade
		}
		if left.Rank != right.Rank {
			return left.Rank < right.Rank
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		// Equal quality: torrents with more seeders download more reliably.
		if releases[i].Seeders != releases[j].Seeders {
			return releases[i].Seeders > releases[j].Seeders
		}
		return releases[i].Size > releases[j].Size
	})
}

func abs(value int) int {
	return int(math.Abs(float64(value)))
}
