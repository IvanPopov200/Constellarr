package library

import (
	"reflect"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		name     string
		identity EpisodeIdentity
		ok       bool
	}{
		{name: "The.Show.US.S01E02.1080p.WEB-DL.x264-GRP", identity: EpisodeIdentity{Title: "The Show US", Season: 1, Numbers: []int{2}}, ok: true},
		{name: "Show.2019.S01E01E02.1080p", identity: EpisodeIdentity{Title: "Show", Year: 2019, Season: 1, Numbers: []int{1, 2}}, ok: true},
		{name: "Show.S01E01E02E03", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1, 2, 3}}, ok: true},
		{name: "Show.S01E01-E03.1080p", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1, 2, 3}}, ok: true},
		{name: "Show.S01E01-03.1080p", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1, 2, 3}}, ok: true},
		{name: "Show.S01E02E03-E05", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{2, 3, 4, 5}}, ok: true},
		{name: "Show.S00E01.Specials.mkv", identity: EpisodeIdentity{Title: "Show", Season: 0, Numbers: []int{1}}, ok: true},
		{name: "Show.S2E1", identity: EpisodeIdentity{Title: "Show", Season: 2, Numbers: []int{1}}, ok: true},
		{name: "Show.1x02.1080p.WEB", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{2}}, ok: true},
		{name: "Show (2019) S01E01 1080p", identity: EpisodeIdentity{Title: "Show", Year: 2019, Season: 1, Numbers: []int{1}}, ok: true},
		{name: "The.Daily.Show.2020.10.05.1080p", identity: EpisodeIdentity{Title: "The Daily Show", Season: -1, AirDate: "2020-10-05"}, ok: true},
		{name: "Show.2020.01.31", identity: EpisodeIdentity{Title: "Show", Season: -1, AirDate: "2020-01-31"}, ok: true},
		{name: "Show.Season.1.COMPLETE.1080p", identity: EpisodeIdentity{Title: "Show", Season: 1, Pack: true}, ok: true},
		{name: "Show.S01.1080p.WEB-DL", identity: EpisodeIdentity{Title: "Show", Season: 1, Pack: true}, ok: true},
		{name: "Show.S01E01.mkv", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1}}, ok: true},
		{name: "Show.S01E01.E02.1080p", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1, 2}}, ok: true},
		{name: "TV/Show/Season 01/Show.S01E01-E02.mkv", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1, 2}}, ok: true},
		{name: "S01E01", identity: EpisodeIdentity{Season: 1, Numbers: []int{1}}, ok: true},
		{name: "Show.S01E01.720p.WEB", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1}}, ok: true},
		{name: "Show.S01E01-1080p.WEB", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1}}, ok: true},
		{name: "Show.S01E01-5.1.1080p", identity: EpisodeIdentity{Title: "Show", Season: 1, Numbers: []int{1}}, ok: true},

		{name: "Some.Movie.2019.1080p.BluRay.x264-GRP", ok: false},
		{name: "The.Matrix.1999.1080p.BluRay.x264-GRP", ok: false},
		{name: "Blade Runner 2049 (2017)", ok: false},
		{name: "SomeS01E01.1080p", ok: false},
		{name: "[Group] Show - 05 [1080p].mkv", ok: false},
		{name: "Show.2020.1080p", ok: false},
		{name: "Show.2020.02.30", ok: false},
		{name: "Show.S01E1500", ok: false},
		{name: "Show.S101E01", ok: false},
		{name: "Show.S01E00", ok: false},
		{name: "Show.S01E01-E03-E01", ok: false},
		{name: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity, ok := ParseEpisode(tc.name)
			if ok != tc.ok {
				t.Fatalf("ParseEpisode(%q) ok = %v, want %v", tc.name, ok, tc.ok)
			}
			if !ok {
				return
			}
			if identity.Title != tc.identity.Title || identity.Year != tc.identity.Year ||
				identity.Season != tc.identity.Season || identity.AirDate != tc.identity.AirDate ||
				identity.Pack != tc.identity.Pack || !reflect.DeepEqual(identity.Numbers, tc.identity.Numbers) {
				t.Fatalf("ParseEpisode(%q) = %+v, want %+v", tc.name, identity, tc.identity)
			}
		})
	}
}

func TestParseEpisodeRangeLimit(t *testing.T) {
	if _, ok := ParseEpisode("Show.S01E01-E100"); !ok {
		t.Fatal("a 100 episode range should parse")
	}
	if _, ok := ParseEpisode("Show.S01E01-E101"); ok {
		t.Fatal("a range above 100 episodes should not parse")
	}
	identity, ok := ParseEpisode("Show.S01E01-E100")
	if !ok || len(identity.Numbers) != 100 || identity.Numbers[99] != 100 {
		t.Fatalf("range expansion = %+v", identity)
	}
}

func TestValidateEpisode(t *testing.T) {
	cases := []struct {
		name    string
		episode *Episode
		wantErr bool
	}{
		{name: "movie", episode: nil},
		{name: "single", episode: &Episode{Season: 1, Numbers: []int{2}}},
		{name: "specials", episode: &Episode{Season: 0, Numbers: []int{1}}},
		{name: "multi", episode: &Episode{Season: 10, Numbers: []int{1, 2, 3}}},
		{name: "negative season", episode: &Episode{Season: -1, Numbers: []int{1}}, wantErr: true},
		{name: "season bound", episode: &Episode{Season: 101, Numbers: []int{1}}, wantErr: true},
		{name: "no numbers", episode: &Episode{Season: 1}, wantErr: true},
		{name: "zero number", episode: &Episode{Season: 1, Numbers: []int{0}}, wantErr: true},
		{name: "number bound", episode: &Episode{Season: 1, Numbers: []int{1001}}, wantErr: true},
		{name: "duplicates", episode: &Episode{Season: 1, Numbers: []int{1, 1}}, wantErr: true},
		{name: "details match", episode: &Episode{Season: 1, Numbers: []int{1, 2}, Details: []metadata.Episode{
			{Season: 1, Number: 1, Title: "One", IMDbID: "tt1111111"},
			{Season: 1, Number: 2, Title: "Two", IMDbID: "tt2222222"},
		}}},
		{name: "detail season mismatch", episode: &Episode{Season: 1, Numbers: []int{1}, Details: []metadata.Episode{
			{Season: 2, Number: 1},
		}}, wantErr: true},
		{name: "detail number missing", episode: &Episode{Season: 1, Numbers: []int{1}, Details: []metadata.Episode{
			{Season: 1, Number: 2},
		}}, wantErr: true},
		{name: "detail duplicate", episode: &Episode{Season: 1, Numbers: []int{1, 2}, Details: []metadata.Episode{
			{Season: 1, Number: 1}, {Season: 1, Number: 1},
		}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEpisode(tc.episode)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateEpisode(%+v) err = %v, wantErr %v", tc.episode, err, tc.wantErr)
			}
		})
	}
	numbers := make([]int, 101)
	for i := range numbers {
		numbers[i] = i + 1
	}
	if err := validateEpisode(&Episode{Season: 1, Numbers: numbers}); err == nil {
		t.Fatal("more than 100 numbers should fail")
	}
}

func TestSeasonFolderNumber(t *testing.T) {
	cases := []struct {
		name   string
		season int
		ok     bool
	}{
		{"Season 1", 1, true},
		{"Season 01", 1, true},
		{"Season.12", 12, true},
		{"S01", 1, true},
		{"specials", 0, true},
		{"Breaking Bad", 0, false},
		{"Season 101", 0, false},
	}
	for _, tc := range cases {
		season, ok := seasonFolderNumber(tc.name)
		if ok != tc.ok || (ok && season != tc.season) {
			t.Errorf("seasonFolderNumber(%q) = (%d, %v), want (%d, %v)", tc.name, season, ok, tc.season, tc.ok)
		}
	}
}
