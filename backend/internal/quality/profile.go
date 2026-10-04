package quality

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Qualities lists selectable quality IDs from most to least preferred.
var Qualities = []string{
	"Remux-2160p",
	"Bluray-2160p",
	"WEB-2160p",
	"HDTV-2160p",
	"Remux-1080p",
	"Bluray-1080p",
	"WEB-1080p",
	"HDTV-1080p",
	"Remux-720p",
	"Bluray-720p",
	"WEB-720p",
	"HDTV-720p",
	"Bluray-480p",
	"WEB-480p",
	"HDTV-480p",
	"DVD",
	"SD",
}

var languageCodes = map[string]string{
	"english": "en", "eng": "en", "en": "en",
	"french": "fr", "truefrench": "fr", "vff": "fr", "vostfr": "fr", "fr": "fr",
	"german": "de", "ger": "de", "deutsch": "de", "de": "de",
	"spanish": "es", "spa": "es", "esp": "es", "castellano": "es", "es": "es",
	"italian": "it", "ita": "it", "it": "it",
	"portuguese": "pt", "por": "pt", "dublado": "pt", "pt": "pt",
	"russian": "ru", "rus": "ru", "ru": "ru",
	"dutch": "nl", "nld": "nl", "nl": "nl",
	"japanese": "ja", "jpn": "ja", "ja": "ja",
	"korean": "ko", "kor": "ko", "ko": "ko",
	"chinese": "zh", "chi": "zh", "mandarin": "zh", "cantonese": "zh", "zh": "zh",
	"arabic": "ar", "ara": "ar", "ar": "ar",
	"hindi": "hi", "hin": "hi", "hi": "hi",
	"polish": "pl", "pol": "pl", "pl": "pl",
	"swedish": "sv", "swe": "sv", "sv": "sv",
	"norwegian": "no", "nor": "no", "no": "no",
	"danish": "da", "dan": "da", "da": "da",
	"finnish": "fi", "fin": "fi", "fi": "fi",
	"turkish": "tr", "tur": "tr", "tr": "tr",
	"czech": "cs", "cze": "cs", "ces": "cs", "cs": "cs",
	"hungarian": "hu", "hun": "hu", "hu": "hu",
	"greek": "el", "gre": "el", "ell": "el", "el": "el",
	"hebrew": "he", "heb": "he", "he": "he",
	"thai": "th", "tha": "th", "th": "th",
	"vietnamese": "vi", "vie": "vi", "vi": "vi",
	"ukrainian": "uk", "ukr": "uk", "uk": "uk",
	"romanian": "ro", "ron": "ro", "rum": "ro", "ro": "ro",
	"indonesian": "id", "ind": "id", "id": "id",
	"multi": "multi",
}

func Defaults() []Profile {
	return []Profile{
		{ID: "hd", Name: "HD", Qualities: qualitiesFor(720, 1080), Cutoff: "Bluray-1080p", Upgrade: true},
		{ID: "uhd", Name: "UHD", Qualities: qualitiesFor(2160), Cutoff: "Bluray-2160p", Upgrade: true},
		// Any cuts off at its last allowed quality so an existing allowed file stops upgrades.
		{ID: "any", Name: "Any", Qualities: append([]string(nil), Qualities...), Cutoff: "SD", Upgrade: true},
	}
}

func qualitiesFor(resolutions ...int) []string {
	wanted := make(map[int]bool, len(resolutions))
	for _, resolution := range resolutions {
		wanted[resolution] = true
	}
	qualities := make([]string, 0, len(Qualities))
	for _, id := range Qualities {
		if wanted[resolutionOf(id)] {
			qualities = append(qualities, id)
		}
	}
	return qualities
}

func resolutionOf(id string) int {
	switch {
	case strings.HasSuffix(id, "-2160p"):
		return 2160
	case strings.HasSuffix(id, "-1080p"):
		return 1080
	case strings.HasSuffix(id, "-720p"):
		return 720
	case strings.HasSuffix(id, "-480p"), id == "DVD", id == "SD":
		return 480
	}
	return 0
}

func Validate(profile Profile) error {
	if len(profile.Qualities) == 0 {
		return errors.New("quality: a profile needs at least one quality")
	}
	allowed := make(map[string]bool, len(profile.Qualities))
	for _, id := range profile.Qualities {
		if _, ok := rankOf(id); !ok {
			return fmt.Errorf("quality: %q is not a selectable quality", id)
		}
		if allowed[id] {
			return fmt.Errorf("quality: %q is listed twice", id)
		}
		allowed[id] = true
	}
	if profile.Cutoff != "" {
		if _, ok := rankOf(profile.Cutoff); !ok {
			return fmt.Errorf("quality: cutoff %q is not a selectable quality", profile.Cutoff)
		}
		if !allowed[profile.Cutoff] {
			return fmt.Errorf("quality: cutoff %q is not one of the profile qualities", profile.Cutoff)
		}
	}
	if profile.MinMB < 0 || profile.MaxMB < 0 {
		return errors.New("quality: size limits cannot be negative")
	}
	if profile.MaxMB > 0 && profile.MinMB > profile.MaxMB {
		return errors.New("quality: the minimum size is above the maximum size")
	}
	if profile.Language != "" && normalizeLanguage(profile.Language) == "" {
		return fmt.Errorf("quality: %q is not a supported language", profile.Language)
	}
	for i, rule := range profile.Rules {
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("quality: rule %d needs a pattern", i+1)
		}
		if _, err := regexp.Compile("(?i)" + rule.Pattern); err != nil {
			return fmt.Errorf("quality: rule %d has an invalid pattern", i+1)
		}
	}
	return nil
}

// Satisfied reports whether the current release meets the profile cutoff; an empty cutoff targets the best allowed quality.
func Satisfied(profile Profile, current Current) bool {
	if profile.CutoffScore > 0 && current.Score < profile.CutoffScore {
		return false
	}
	target := profile.Cutoff
	if target == "" {
		if len(profile.Qualities) == 0 {
			return false
		}
		target = profile.Qualities[0]
	}
	targetRank, ok := profileRank(profile, target)
	if !ok {
		return false
	}
	currentRank, ok := profileRank(profile, current.Quality)
	if !ok {
		return false
	}
	return currentRank <= targetRank
}

func rankOf(id string) (int, bool) {
	for rank, quality := range Qualities {
		if quality == id {
			return rank, true
		}
	}
	return -1, false
}
