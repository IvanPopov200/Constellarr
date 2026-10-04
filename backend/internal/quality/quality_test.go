package quality_test

import (
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func TestParseReleaseTitles(t *testing.T) {
	cases := []struct {
		title string
		want  quality.Details
	}{
		{
			"Big Buck Bunny 2008 1080p BluRay x264-GROUP",
			quality.Details{Quality: "Bluray-1080p", Resolution: 1080, Source: "Bluray", Codec: "x264", Group: "GROUP"},
		},
		{
			"Movie.2019.2160p.BluRay.REMUX.HDR10.TrueHD.Atmos.7.1-GROUP",
			quality.Details{Quality: "Remux-2160p", Resolution: 2160, Source: "Remux", Audio: "TrueHD Atmos", HDR: "HDR10", Group: "GROUP"},
		},
		{
			"Movie.2019.1080p.WEB-DL.DDP5.1.H.264-NTb",
			quality.Details{Quality: "WEB-1080p", Resolution: 1080, Source: "WEB", Codec: "x264", Audio: "EAC3", Group: "NTb"},
		},
		{
			"Movie.2019.1080p.WEB.H264-GROUP",
			quality.Details{Quality: "WEB-1080p", Resolution: 1080, Source: "WEB", Codec: "x264", Group: "GROUP"},
		},
		{
			"Movie.2019.720p.HDTV.x265.AAC",
			quality.Details{Quality: "HDTV-720p", Resolution: 720, Source: "HDTV", Codec: "x265", Audio: "AAC"},
		},
		{
			"Movie.1999.DVDRip.XviD.AC3",
			quality.Details{Quality: "DVD", Resolution: 480, Source: "DVD", Codec: "XviD", Audio: "AC3"},
		},
		{
			"Movie.2005.SDTV.XviD",
			quality.Details{Quality: "SD", Resolution: 480, Source: "SD", Codec: "XviD"},
		},
		{
			"Movie.2019.1080p.BluRay.DTS-HD.MA.5.1.x264",
			quality.Details{Quality: "Bluray-1080p", Resolution: 1080, Source: "Bluray", Codec: "x264", Audio: "DTS-HD"},
		},
		{
			"Movie.2019.2160p.WEB-DL.DV.HDR.atmos.x265",
			quality.Details{Quality: "WEB-2160p", Resolution: 2160, Source: "WEB", Codec: "x265", Audio: "Atmos", HDR: "DV"},
		},
		{
			"Movie.2019.1080p.BluRay.FRENCH.EXTENDED.PROPER.x264-GRP",
			quality.Details{Quality: "Bluray-1080p", Resolution: 1080, Source: "Bluray", Codec: "x264", Language: "fr", Edition: "Extended", Proper: true, Group: "GRP"},
		},
		{
			"Movie.2019.720p.WEBRip.MULTI.Spanish.x264",
			quality.Details{Quality: "WEB-720p", Resolution: 720, Source: "WEB", Codec: "x264", Language: "multi"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := quality.Parse(tc.title)
			if got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.title, got, tc.want)
			}
		})
	}
}

func TestParseMalformedAndAmbiguousTitles(t *testing.T) {
	cases := []struct {
		title      string
		qualityID  string
		resolution int
	}{
		{"", "", 0},
		{"Movie 2019 1920x1080 x264", "", 0},
		{"Movie.2019.1080p.720p.BluRay.x264", "Bluray-1080p", 1080},
		{"Movie.2019.1080p.x264", "", 1080},
		{"Movie.2019.HDTV.XviD", "SD", 480},
		{"Movie.2019.HDCAM.x264", "CAM", 0},
		{"Movie.2019.2160p.CAM", "CAM", 2160},
		{"Movie.2019.TS.x264", "TS", 0},
		{"Movie.2019.WORKPRINT", "WORKPRINT", 0},
		{"Movie.2019.BluRay.1080p.REMUX", "Remux-1080p", 1080},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := quality.Parse(tc.title)
			if got.Quality != tc.qualityID || got.Resolution != tc.resolution {
				t.Fatalf("Parse(%q) = %+v, want quality %q at %d", tc.title, got, tc.qualityID, tc.resolution)
			}
		})
	}
}

func TestParseGroupAvoidsCompoundSuffixes(t *testing.T) {
	cases := map[string]string{
		"Movie.2019.1080p.WEB-DL":              "",
		"Movie.2019.1080p.BluRay.DTS-HD.MA":    "",
		"Movie.2019.1080p.BluRay.x264-RARBG":   "RARBG",
		"Movie.2019.2160p.WEB-DL.DDP5.1-NTb":   "NTb",
		"No group here 2019 1080p BluRay x264": "",
	}
	for title, want := range cases {
		if got := quality.Parse(title).Group; got != want {
			t.Fatalf("Parse(%q).Group = %q, want %q", title, got, want)
		}
	}
}

func TestQualitiesAreOrderedMostPreferredFirst(t *testing.T) {
	if quality.Qualities[0] != "Remux-2160p" {
		t.Fatalf("first quality = %q, want Remux-2160p", quality.Qualities[0])
	}
	if quality.Qualities[len(quality.Qualities)-1] != "SD" {
		t.Fatalf("last quality = %q, want SD", quality.Qualities[len(quality.Qualities)-1])
	}
	previous := 1 << 30
	for _, id := range quality.Qualities {
		if id == "CAM" || id == "TS" {
			t.Fatalf("unsafe quality %q is selectable", id)
		}
		resolution := 0
		switch {
		case strings.HasSuffix(id, "-2160p"):
			resolution = 2160
		case strings.HasSuffix(id, "-1080p"):
			resolution = 1080
		case strings.HasSuffix(id, "-720p"):
			resolution = 720
		case strings.HasSuffix(id, "-480p"):
			resolution = 480
		}
		if resolution > previous {
			t.Fatalf("qualities are not ordered by resolution: %v", quality.Qualities)
		}
		previous = resolution
	}
}

func TestDefaultsAreValidAndSafe(t *testing.T) {
	profiles := quality.Defaults()
	if len(profiles) != 3 {
		t.Fatalf("got %d default profiles, want 3", len(profiles))
	}
	ids := map[string]quality.Profile{}
	for _, profile := range profiles {
		if profile.ID == "" || profile.Name == "" {
			t.Fatalf("default profile lacks a clear ID and label: %+v", profile)
		}
		if err := quality.Validate(profile); err != nil {
			t.Fatalf("default profile %q is invalid: %v", profile.ID, err)
		}
		for _, id := range profile.Qualities {
			if id == "CAM" || id == "TS" || id == "TC" || id == "SCR" || id == "R5" {
				t.Fatalf("default profile %q selects %q", profile.ID, id)
			}
		}
		ids[profile.ID] = profile
	}
	for _, id := range []string{"hd", "uhd", "any"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("missing default profile %q", id)
		}
	}
	for _, wanted := range []string{"1080p", "720p"} {
		found := false
		for _, id := range ids["hd"].Qualities {
			if strings.Contains(id, wanted) {
				found = true
			}
		}
		if !found {
			t.Fatalf("HD profile does not allow %s: %v", wanted, ids["hd"].Qualities)
		}
	}
	for _, id := range ids["hd"].Qualities {
		if strings.Contains(id, "2160p") {
			t.Fatalf("HD profile allows %q", id)
		}
	}
	for _, id := range ids["uhd"].Qualities {
		if !strings.Contains(id, "2160p") {
			t.Fatalf("UHD profile allows non-UHD quality %q", id)
		}
	}
	if len(ids["any"].Qualities) != len(quality.Qualities) {
		t.Fatalf("Any profile allows %d qualities, want all %d", len(ids["any"].Qualities), len(quality.Qualities))
	}
	if ids["any"].Cutoff != "SD" {
		t.Fatalf("Any profile cutoff = %q, want SD so any allowed file stops upgrades", ids["any"].Cutoff)
	}
	if !quality.Satisfied(ids["any"], quality.Current{Quality: "SD"}) {
		t.Fatal("Any profile does not accept an allowed file")
	}
	if !quality.Satisfied(ids["hd"], quality.Current{Quality: "Bluray-1080p"}) {
		t.Fatal("HD profile does not accept its cutoff quality")
	}
	if quality.Satisfied(ids["hd"], quality.Current{Quality: "WEB-1080p"}) {
		t.Fatal("HD profile accepts a quality below its cutoff")
	}
}

func TestValidateRejectsInvalidProfiles(t *testing.T) {
	valid := quality.Profile{
		ID: "custom", Name: "Custom", Qualities: []string{"Bluray-1080p", "WEB-1080p"},
		Cutoff: "Bluray-1080p", MinMB: 10, MaxMB: 100, Language: "en", MinScore: 5,
		Rules: []quality.Rule{{Name: "prefer x265", Pattern: `x265`, Score: 10}},
	}
	if err := quality.Validate(valid); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	cases := map[string]quality.Profile{
		"no qualities":        {Qualities: nil},
		"bare resolution":     {Qualities: []string{"1080p"}},
		"camera quality":      {Qualities: []string{"CAM"}},
		"duplicate quality":   {Qualities: []string{"WEB-1080p", "WEB-1080p"}},
		"unknown cutoff":      {Qualities: []string{"WEB-1080p"}, Cutoff: "Remux-2160p"},
		"cutoff not selected": {Qualities: []string{"WEB-1080p"}, Cutoff: "Bluray-1080p"},
		"negative size":       {Qualities: []string{"WEB-1080p"}, MinMB: -1},
		"inverted sizes":      {Qualities: []string{"WEB-1080p"}, MinMB: 100, MaxMB: 10},
		"unknown language":    {Qualities: []string{"WEB-1080p"}, Language: "klingon"},
		"empty rule":          {Qualities: []string{"WEB-1080p"}, Rules: []quality.Rule{{Pattern: ""}}},
		"invalid regex":       {Qualities: []string{"WEB-1080p"}, Rules: []quality.Rule{{Pattern: `(`}}},
	}
	for name, profile := range cases {
		t.Run(name, func(t *testing.T) {
			if err := quality.Validate(profile); err == nil {
				t.Fatalf("Validate(%+v) succeeded, want an error", profile)
			}
		})
	}
}

func TestEvaluateScoresRules(t *testing.T) {
	profile := quality.Profile{
		Qualities: append([]string(nil), quality.Qualities...),
		Rules: []quality.Rule{
			{Name: "prefer x265", Pattern: `x265`, Score: 50},
			{Name: "avoid aac", Pattern: `aac`, Score: -20},
			{Name: "require hdr", Pattern: `hdr`, Required: true},
			{Name: "avoid xvid", Pattern: `xvid`, Negate: true, Required: true},
		},
	}
	title := "Movie.2019.2160p.WEB-DL.HDR.x265.DDP5.1-GROUP"
	decision := quality.Evaluate(profile, title, 0, nil)
	if !decision.Allowed {
		t.Fatalf("allowed release rejected: %v", decision.Reasons)
	}
	if decision.Score != 50 {
		t.Fatalf("score = %d, want 50", decision.Score)
	}
	if decision.Rank != 2 {
		t.Fatalf("rank = %d, want 2 for WEB-2160p", decision.Rank)
	}

	withoutHDR := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x265-GROUP", 0, nil)
	if withoutHDR.Allowed {
		t.Fatal("release without required HDR was allowed")
	}
	if !hasReason(withoutHDR, "required rule") {
		t.Fatalf("reasons lack the required rule: %v", withoutHDR.Reasons)
	}
	if withoutHDR.Score != 50 {
		t.Fatalf("score = %d, want 50 from the x265 rule", withoutHDR.Score)
	}

	rejectedRule := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.XviD.HDR-GROUP", 0, nil)
	if rejectedRule.Allowed || !hasReason(rejectedRule, "rejected by rule") {
		t.Fatalf("negated rule did not reject its match: %+v", rejectedRule)
	}

	withCAM := quality.Evaluate(profile, "Movie.2019.HDCAM.HDR.x265-GROUP", 0, nil)
	if withCAM.Allowed || !hasReason(withCAM, "never selected") {
		t.Fatalf("CAM release was not rejected: %+v", withCAM)
	}

	lowScore := profile
	lowScore.MinScore = 60
	if decision := quality.Evaluate(lowScore, title, 0, nil); decision.Allowed {
		t.Fatalf("release below the minimum score was allowed: %+v", decision)
	}

	rewarded := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.HDR.AAC.x265-GROUP", 0, nil)
	if rewarded.Score != 30 {
		t.Fatalf("score = %d, want 30 (50 - 20)", rewarded.Score)
	}
}

func TestEvaluateEnforcesSizeAndLanguage(t *testing.T) {
	profile := quality.Profile{
		Qualities: append([]string(nil), quality.Qualities...),
		MinMB:     100, MaxMB: 200, Language: "en",
	}
	title := "Movie.2019.1080p.BluRay.ENGLISH.x264-GROUP"
	if decision := quality.Evaluate(profile, title, 150*(1<<20), nil); !decision.Allowed {
		t.Fatalf("in-range release rejected: %v", decision.Reasons)
	}
	if decision := quality.Evaluate(profile, title, 50*(1<<20), nil); decision.Allowed || !hasReason(decision, "minimum") {
		t.Fatalf("small release allowed: %+v", decision)
	}
	if decision := quality.Evaluate(profile, title, 300*(1<<20), nil); decision.Allowed || !hasReason(decision, "maximum") {
		t.Fatalf("large release allowed: %+v", decision)
	}
	if decision := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 150*(1<<20), nil); decision.Allowed {
		t.Fatalf("release without the required language allowed: %+v", decision)
	}
	if decision := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.MULTI.x264-GROUP", 150*(1<<20), nil); !decision.Allowed {
		t.Fatalf("MULTI release rejected: %v", decision.Reasons)
	}
}

func TestEvaluateCurrentQualityUpgrades(t *testing.T) {
	profile := quality.Profile{
		ID: "hd", Name: "HD", Qualities: []string{"Bluray-1080p", "WEB-1080p", "WEB-720p"},
		Cutoff: "Bluray-1080p", Upgrade: true,
	}
	current := &quality.Current{Quality: "WEB-1080p", Score: 10}

	upgrade := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, current)
	if !upgrade.Allowed || !upgrade.Upgrade {
		t.Fatalf("better quality was not an upgrade: %+v", upgrade)
	}

	downgrade := quality.Evaluate(profile, "Movie.2019.720p.WEB-DL.x264-GROUP", 0, current)
	if downgrade.Allowed || !hasReason(downgrade, "downgrade") {
		t.Fatalf("downgrade accepted: %+v", downgrade)
	}

	same := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, current)
	if same.Allowed || !hasReason(same, "does not improve") {
		t.Fatalf("same quality and score accepted: %+v", same)
	}

	higherScore := &quality.Current{Quality: "WEB-1080p", Score: -5}
	sameWithBetter := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, higherScore)
	if !sameWithBetter.Allowed || !sameWithBetter.Upgrade {
		t.Fatalf("same quality with a higher score was not an upgrade: %+v", sameWithBetter)
	}

	atCutoff := &quality.Current{Quality: "Bluray-1080p", Score: 0}
	if decision := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.REMUX-GROUP", 0, atCutoff); decision.Allowed || !hasReason(decision, "cutoff") {
		t.Fatalf("release accepted after the cutoff was met: %+v", decision)
	}

	noUpgrades := profile
	noUpgrades.Upgrade = false
	if decision := quality.Evaluate(noUpgrades, "Movie.2019.1080p.BluRay.x264-GROUP", 0, current); decision.Allowed || !hasReason(decision, "upgrades are disabled") {
		t.Fatalf("upgrade accepted while upgrades are disabled: %+v", decision)
	}

	missing := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, nil)
	if !missing.Allowed || missing.Upgrade {
		t.Fatalf("initial download decision = %+v, want allowed without upgrade", missing)
	}
}

func TestEvaluateRejectsUnknownQualities(t *testing.T) {
	profile := quality.Profile{Qualities: append([]string(nil), quality.Qualities...)}
	cases := []struct {
		title  string
		reason string
	}{
		{"Movie.2019.1080p.x264-GROUP", "unknown"},
		{"Movie.2019.HDCAM-GROUP", "never selected"},
		{"Movie.2019.2160p.TS-GROUP", "never selected"},
	}
	for _, tc := range cases {
		decision := quality.Evaluate(profile, tc.title, 0, nil)
		if decision.Allowed {
			t.Fatalf("%q was allowed: %+v", tc.title, decision)
		}
		if !hasReason(decision, tc.reason) {
			t.Fatalf("%q lacks reason %q: %v", tc.title, tc.reason, decision.Reasons)
		}
	}
}

func TestSatisfied(t *testing.T) {
	profile := quality.Profile{
		Qualities: []string{"Bluray-1080p", "WEB-1080p", "WEB-720p"},
		Cutoff:    "Bluray-1080p", CutoffScore: 10,
	}
	cases := []struct {
		quality string
		score   int
		want    bool
	}{
		{"Bluray-1080p", 10, true},
		{"Remux-1080p", 0, false},
		{"WEB-1080p", 100, false},
		{"WEB-720p", 100, false},
		{"CAM", 100, false},
		{"Bluray-1080p", 5, false},
	}
	for _, tc := range cases {
		got := quality.Satisfied(profile, quality.Current{Quality: tc.quality, Score: tc.score})
		if got != tc.want {
			t.Fatalf("Satisfied(%q, score %d) = %v, want %v", tc.quality, tc.score, got, tc.want)
		}
	}
	noCutoff := quality.Profile{Qualities: []string{"WEB-1080p", "WEB-720p"}}
	if !quality.Satisfied(noCutoff, quality.Current{Quality: "WEB-1080p"}) {
		t.Fatal("the best allowed quality did not satisfy a profile without a cutoff")
	}
	if quality.Satisfied(noCutoff, quality.Current{Quality: "WEB-720p"}) {
		t.Fatal("a lower allowed quality satisfied a profile without a cutoff")
	}
	if quality.Satisfied(noCutoff, quality.Current{Quality: "Bluray-1080p"}) {
		t.Fatal("quality outside the profile satisfied it")
	}
	outsideCutoff := quality.Profile{Qualities: []string{"WEB-1080p"}, Cutoff: "Bluray-1080p"}
	if quality.Satisfied(outsideCutoff, quality.Current{Quality: "WEB-1080p"}) {
		t.Fatal("a cutoff outside the profile satisfied it")
	}
}

func hasReason(decision quality.Decision, substring string) bool {
	for _, reason := range decision.Reasons {
		if strings.Contains(reason, substring) {
			return true
		}
	}
	return false
}

func TestEvaluateRankFollowsProfileOrder(t *testing.T) {
	profile := quality.Profile{Qualities: []string{"WEB-1080p", "Bluray-1080p"}}
	web := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, nil)
	if !web.Allowed || web.Rank != 0 {
		t.Fatalf("WEB-1080p decision = %+v, want rank 0 and allowed", web)
	}
	bluray := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, nil)
	if !bluray.Allowed || bluray.Rank != 1 {
		t.Fatalf("Bluray-1080p decision = %+v, want rank 1 and allowed", bluray)
	}
	outside := quality.Evaluate(profile, "Movie.2019.2160p.WEB-DL.x265-GROUP", 0, nil)
	if outside.Allowed || outside.Rank != -1 {
		t.Fatalf("quality outside the profile = %+v, want rank -1 and rejected", outside)
	}
}

func TestEvaluateHonoursCustomProfileOrder(t *testing.T) {
	profile := quality.Profile{
		Qualities: []string{"WEB-1080p", "Bluray-1080p"},
		Cutoff:    "WEB-1080p", Upgrade: true,
	}
	blurayFile := &quality.Current{Quality: "Bluray-1080p", Score: 0}
	upgrade := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, blurayFile)
	if !upgrade.Allowed || !upgrade.Upgrade {
		t.Fatalf("profile-preferred WEB release was not an upgrade from Bluray: %+v", upgrade)
	}
	webFile := &quality.Current{Quality: "WEB-1080p", Score: 0}
	reverse := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, webFile)
	if reverse.Allowed || reverse.Upgrade {
		t.Fatalf("profile order was reversed: %+v", reverse)
	}
	// The same comparison rejects a lower profile rank whenever the cutoff is not met.
	three := quality.Profile{
		Qualities: []string{"WEB-1080p", "Bluray-1080p", "WEB-720p"},
		Cutoff:    "WEB-1080p", Upgrade: true,
	}
	downgrade := quality.Evaluate(three, "Movie.2019.720p.WEB-DL.x264-GROUP", 0, blurayFile)
	if downgrade.Allowed || !hasReason(downgrade, "downgrade") {
		t.Fatalf("lower profile rank accepted: %+v", downgrade)
	}
}

func TestEvaluateKeepsResolutionOutsideProfile(t *testing.T) {
	profile := quality.Profile{
		Qualities: []string{"Bluray-1080p", "WEB-1080p"},
		Cutoff:    "Bluray-1080p", Upgrade: true,
	}
	uhdFile := &quality.Current{Quality: "Remux-2160p", Score: 0}
	decision := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, uhdFile)
	if decision.Allowed || decision.Upgrade || !hasReason(decision, "resolution") {
		t.Fatalf("1080p candidate replaced a 2160p file: %+v", decision)
	}
	notAllowed := quality.Evaluate(profile, "Movie.2019.2160p.BluRay.REMUX-GROUP", 0, uhdFile)
	if notAllowed.Allowed {
		t.Fatalf("quality outside the profile was allowed: %+v", notAllowed)
	}
	lowerFile := &quality.Current{Quality: "WEB-480p", Score: 0}
	better := quality.Evaluate(profile, "Movie.2019.1080p.BluRay.x264-GROUP", 0, lowerFile)
	if !better.Allowed || !better.Upgrade {
		t.Fatalf("higher-resolution allowed release did not upgrade a file outside the profile: %+v", better)
	}
}

func TestEvaluateEmptyCutoffTargetsBestAllowed(t *testing.T) {
	profile := quality.Profile{Qualities: []string{"WEB-1080p", "WEB-720p"}, Upgrade: true}
	current := &quality.Current{Quality: "WEB-720p", Score: 0}
	if quality.Satisfied(profile, *current) {
		t.Fatal("a non-best allowed release satisfied an empty cutoff")
	}
	decision := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, current)
	if !decision.Allowed || !decision.Upgrade {
		t.Fatalf("best allowed release did not upgrade a lower allowed file: %+v", decision)
	}
	best := &quality.Current{Quality: "WEB-1080p", Score: 0}
	if !quality.Satisfied(profile, *best) {
		t.Fatal("the best allowed quality did not satisfy an empty cutoff")
	}
	blocked := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x265-GROUP", 0, best)
	if blocked.Allowed || !hasReason(blocked, "cutoff") {
		t.Fatalf("release accepted after reaching the best allowed quality: %+v", blocked)
	}
}

func TestEvaluateCutoffScoreStillGates(t *testing.T) {
	profile := quality.Profile{
		Qualities: []string{"WEB-1080p", "WEB-720p"},
		Cutoff:    "WEB-1080p", CutoffScore: 50, Upgrade: true,
		Rules: []quality.Rule{{Name: "prefer hdr", Pattern: `hdr`, Score: 60}},
	}
	current := &quality.Current{Quality: "WEB-1080p", Score: 10}
	if quality.Satisfied(profile, *current) {
		t.Fatal("score below the cutoff score satisfied the profile")
	}
	upgrade := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.HDR.x264-GROUP", 0, current)
	if !upgrade.Allowed || !upgrade.Upgrade || upgrade.Score != 60 {
		t.Fatalf("score upgrade was not selected: %+v", upgrade)
	}
	met := &quality.Current{Quality: "WEB-1080p", Score: 60}
	if !quality.Satisfied(profile, *met) {
		t.Fatal("cutoff score was not honoured")
	}
	blocked := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.HDR.x265-GROUP", 0, met)
	if blocked.Allowed || !hasReason(blocked, "cutoff") {
		t.Fatalf("release accepted after the cutoff score was met: %+v", blocked)
	}
}

func TestEvaluateHonoursDisabledUpgradesWithProfileOrder(t *testing.T) {
	profile := quality.Profile{
		Qualities: []string{"WEB-1080p", "Bluray-1080p"},
		Cutoff:    "WEB-1080p", Upgrade: false,
	}
	current := &quality.Current{Quality: "Bluray-1080p", Score: 0}
	decision := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, current)
	if decision.Allowed || decision.Upgrade || !hasReason(decision, "upgrades are disabled") {
		t.Fatalf("upgrade accepted while upgrades are disabled: %+v", decision)
	}
	missing := quality.Evaluate(profile, "Movie.2019.1080p.WEB-DL.x264-GROUP", 0, nil)
	if !missing.Allowed || missing.Upgrade {
		t.Fatalf("initial download rejected: %+v", missing)
	}
}
