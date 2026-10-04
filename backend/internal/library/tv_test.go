package library

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const (
	tvFolderTemplate = "{title} ({year}) [imdb-{imdbId}]/Season {season}"
	tvFileTemplate   = "{title} - {episodeCode} - {episodeTitle} [{quality}]"
)

type parsedEpisodeNFO struct {
	XMLName   xml.Name
	Title     string `xml:"title"`
	Season    int    `xml:"season"`
	Episode   int    `xml:"episode"`
	Aired     string `xml:"aired"`
	IMDbID    string `xml:"imdbid"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
}

func tvOptions(src, lib string, episode *Episode, writeNFO bool) Options {
	return Options{
		SourceRoot:     src,
		Root:           lib,
		FolderTemplate: tvFolderTemplate,
		FileTemplate:   tvFileTemplate,
		Metadata:       metadata.Title{Title: "Some Show", Year: 2019, IMDbID: "tt1234567"},
		Quality:        "1080p WEB",
		WriteNFO:       writeNFO,
		Episode:        episode,
	}
}

func TestPreviewTVRendersEpisodeTokens(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01E02.1080p.WEB.mkv"), "payload")
	sources := []Source{{Name: "Show.S01E01E02.1080p.WEB.mkv"}}

	opts := tvOptions(src, lib, &Episode{
		Season: 1, Numbers: []int{1, 2}, Title: `Pilot: "Part/One"`, AirDate: "2019-01-02",
	}, false)
	files, err := Preview(opts, sources)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := "Some Show (2019) [imdb-tt1234567]/Season 01/Some Show - S01E01E02 - Pilot Part One [1080p WEB].mkv"
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("preview = %+v, want %s", files, want)
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("preview wrote to the library: %v", got)
	}
}

func TestPreviewTVSpecialsAndEpisodeToken(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S00E03.mkv"), "payload")
	sources := []Source{{Name: "Show.S00E03.mkv"}}

	specials := tvOptions(src, lib, &Episode{Season: 0, Numbers: []int{3}, Title: "Special"}, false)
	specials.FileTemplate = "{title} - {episode} - {episodeCode}"
	files, err := Preview(specials, sources)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want := "Some Show (2019) [imdb-tt1234567]/Season 00/Some Show - 03 - S00E03.mkv"
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("preview = %+v, want %s", files, want)
	}

	multi := tvOptions(src, lib, &Episode{Season: 2, Numbers: []int{1, 2}, Title: "Two Parter"}, false)
	multi.FileTemplate = "{title} - {episode}"
	files, err = Preview(multi, sources)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	want = "Some Show (2019) [imdb-tt1234567]/Season 02/Some Show - 01-E02.mkv"
	if len(files) != 1 || files[0].Path != want {
		t.Fatalf("multi preview = %+v, want %s", files, want)
	}
}

func TestPreviewRejectsMissingOrInvalidEpisodeData(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.mkv"), "payload")
	sources := []Source{{Name: "Show.mkv"}}
	cases := []struct {
		name    string
		episode *Episode
		tmpl    string
	}{
		{"movie with episode token", nil, "{title} - {episodeCode}"},
		{"empty episode", &Episode{Season: 1}, tvFileTemplate},
		{"season out of range", &Episode{Season: 101, Numbers: []int{1}}, tvFileTemplate},
		{"number out of range", &Episode{Season: 1, Numbers: []int{1001}}, tvFileTemplate},
		{"duplicate numbers", &Episode{Season: 1, Numbers: []int{1, 1}}, tvFileTemplate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tvOptions(src, lib, tc.episode, false)
			opts.FileTemplate = tc.tmpl
			if _, err := Preview(opts, sources); !errors.Is(err, ErrEpisode) {
				t.Fatalf("err = %v, want ErrEpisode", err)
			}
		})
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("invalid episode data wrote %v", got)
	}
}

func TestImportTVWritesEpisodeAndSeriesNFO(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01.1080p.WEB.mkv"), "episode-one")
	opts := tvOptions(src, lib, &Episode{
		Season: 1, Numbers: []int{1}, Title: "Pilot", AirDate: "2019-01-02", IMDbID: "tt7654321",
	}, true)
	opts.Metadata.Plot = "Some plot"
	opts.Metadata.Cast = []string{"Actor One"}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S01E01.1080p.WEB.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	seriesDir := filepath.Join(lib, "Some Show (2019) [imdb-tt1234567]")
	episodeNFO := filepath.Join(seriesDir, "Season 01", "Some Show - S01E01 - Pilot [1080p WEB].nfo")
	mustNotExist(t, filepath.Join(seriesDir, "Season 01", "movie.nfo"))
	mustNotExist(t, filepath.Join(seriesDir, "movie.nfo"))

	rawSeries := readFile(t, filepath.Join(seriesDir, "tvshow.nfo"))
	if !strings.Contains(rawSeries, "<plot>Some plot</plot>") {
		t.Fatalf("tvshow.nfo lost series metadata: %s", rawSeries)
	}
	var series parsedNFO
	if err := xml.Unmarshal([]byte(rawSeries), &series); err != nil {
		t.Fatalf("tvshow.nfo is not valid XML: %v", err)
	}
	if series.XMLName.Local != "tvshow" || series.Title != "Some Show" || series.Year != 2019 || series.IMDbID != "tt1234567" {
		t.Fatalf("tvshow.nfo = %+v", series)
	}
	if len(series.Actors) != 1 || series.Actors[0].Name != "Actor One" {
		t.Fatalf("tvshow.nfo actors = %+v", series.Actors)
	}

	rawEpisode := readFile(t, episodeNFO)
	var episode parsedEpisodeNFO
	if err := xml.Unmarshal([]byte(rawEpisode), &episode); err != nil {
		t.Fatalf("episode nfo is not valid XML: %v", err)
	}
	if episode.XMLName.Local != "episodedetails" || episode.Title != "Pilot" || episode.Season != 1 ||
		episode.Episode != 1 || episode.Aired != "2019-01-02" || episode.IMDbID != "tt7654321" {
		t.Fatalf("episode nfo = %+v", episode)
	}
	if len(episode.UniqueIDs) != 1 || episode.UniqueIDs[0].Type != "imdb" || episode.UniqueIDs[0].Value != "tt7654321" {
		t.Fatalf("episode unique ids = %+v", episode.UniqueIDs)
	}
}

func TestImportTVMultiEpisodeNFOConcatenatesBlocks(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01E02.mkv"), "double-episode")
	opts := tvOptions(src, lib, &Episode{
		Season: 1, Numbers: []int{1, 2}, Title: "Two Parter", AirDate: "2019-01-02", IMDbID: "tt7654321",
	}, true)
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S01E01E02.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	raw := readFile(t, filepath.Join(lib,
		"Some Show (2019) [imdb-tt1234567]", "Season 01", "Some Show - S01E01E02 - Two Parter [1080p WEB].nfo"))
	decoder := xml.NewDecoder(strings.NewReader(raw))
	count := 0
	for {
		var block parsedEpisodeNFO
		err := decoder.Decode(&block)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("episode nfo is not decodable: %v", err)
		}
		count++
		if block.XMLName.Local != "episodedetails" || block.Title != "Two Parter" || block.Season != 1 ||
			block.Episode != count || block.Aired != "" || block.IMDbID != "" || len(block.UniqueIDs) != 0 {
			t.Fatalf("episode block %d = %+v", count, block)
		}
	}
	if count != 2 {
		t.Fatalf("episode blocks = %d, want 2", count)
	}
}

func TestImportTVMultiEpisodeNFOusesPerEpisodeDetails(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01E02.mkv"), "double-episode")
	opts := tvOptions(src, lib, &Episode{
		Season: 1, Numbers: []int{1, 2}, Title: "Two Parter", AirDate: "2000-01-01", IMDbID: "tt9999999",
		Details: []metadata.Episode{
			{Season: 1, Number: 1, Title: "First", AirDate: "2019-01-02", IMDbID: "tt1111111"},
			{Season: 1, Number: 2, Title: "Second", AirDate: "2019-01-09", IMDbID: "tt2222222"},
		},
	}, true)
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S01E01E02.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	raw := readFile(t, filepath.Join(lib,
		"Some Show (2019) [imdb-tt1234567]", "Season 01", "Some Show - S01E01E02 - Two Parter [1080p WEB].nfo"))
	decoder := xml.NewDecoder(strings.NewReader(raw))
	want := []parsedEpisodeNFO{
		{Title: "First", Season: 1, Episode: 1, Aired: "2019-01-02", IMDbID: "tt1111111"},
		{Title: "Second", Season: 1, Episode: 2, Aired: "2019-01-09", IMDbID: "tt2222222"},
	}
	for i, expected := range want {
		var block parsedEpisodeNFO
		if err := decoder.Decode(&block); err != nil {
			t.Fatalf("decode episode block %d: %v", i+1, err)
		}
		if block.XMLName.Local != "episodedetails" || block.Title != expected.Title || block.Season != expected.Season ||
			block.Episode != expected.Episode || block.Aired != expected.Aired || block.IMDbID != expected.IMDbID {
			t.Fatalf("episode block %d = %+v, want %+v", i+1, block, expected)
		}
		if len(block.UniqueIDs) != 1 || block.UniqueIDs[0].Type != "imdb" || block.UniqueIDs[0].Value != expected.IMDbID {
			t.Fatalf("episode block %d unique ids = %+v", i+1, block.UniqueIDs)
		}
	}
	var extra parsedEpisodeNFO
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected extra episode block: %+v (%v)", extra, err)
	}
}

func TestImportTVSeriesNFOUsesSeasonTemplatePrefix(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01.mkv"), "episode")
	base := tvOptions(src, lib, &Episode{Season: 1, Numbers: []int{1}, Title: "Pilot"}, true)
	base.FileTemplate = "{title} - {episodeCode}"

	extras := base
	extras.FolderTemplate = "{title} ({year})/Season {season}/Extras"
	if _, err := Import(context.Background(), extras, []Source{{Name: "Show.S01E01.mkv"}}); err != nil {
		t.Fatalf("extras import: %v", err)
	}
	readFile(t, filepath.Join(lib, "Some Show (2019)", "tvshow.nfo"))
	mustNotExist(t, filepath.Join(lib, "Some Show (2019)", "Season 01", "tvshow.nfo"))
	readFile(t, filepath.Join(lib, "Some Show (2019)", "Season 01", "Extras", "Some Show - S01E01.mkv"))
	readFile(t, filepath.Join(lib, "Some Show (2019)", "Season 01", "Extras", "Some Show - S01E01.nfo"))

	nested := base
	nested.FolderTemplate = "Shows/{title} ({year})/Season {season}"
	if _, err := Import(context.Background(), nested, []Source{{Name: "Show.S01E01.mkv"}}); err != nil {
		t.Fatalf("nested import: %v", err)
	}
	readFile(t, filepath.Join(lib, "Shows", "Some Show (2019)", "tvshow.nfo"))
	mustNotExist(t, filepath.Join(lib, "Shows", "tvshow.nfo"))

	flat := base
	flat.FolderTemplate = "{title} ({year})"
	if _, err := Import(context.Background(), flat, []Source{{Name: "Show.S01E01.mkv"}}); err != nil {
		t.Fatalf("flat import: %v", err)
	}
	readFile(t, filepath.Join(lib, "Some Show (2019)", "tvshow.nfo"))
}

func TestImportTVUpgradePreservesOtherEpisodes(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "first.mkv"), "episode-one")
	writeFile(t, filepath.Join(src, "second.mkv"), "episode-two")
	base := tvOptions(src, lib, &Episode{Season: 1, Numbers: []int{1}, Title: "Pilot"}, true)
	base.FolderTemplate = "{title} ({year})/Season {season}"
	base.FileTemplate = "{title} - {episodeCode} - {episodeTitle}"

	episodeOne, err := Import(context.Background(), base, []Source{{Name: "first.mkv"}})
	if err != nil {
		t.Fatalf("import episode one: %v", err)
	}
	second := base
	second.Episode = &Episode{Season: 1, Numbers: []int{2}, Title: "Second"}
	episodeTwo, err := Import(context.Background(), second, []Source{{Name: "second.mkv"}})
	if err != nil {
		t.Fatalf("import episode two: %v", err)
	}
	oneMedia := filepath.Join(lib, filepath.FromSlash(episodeOne[0].Path))
	oneNFO := filepath.Join(lib, "Some Show (2019)", "Season 01", "Some Show - S01E01 - Pilot.nfo")
	twoMedia := filepath.Join(lib, filepath.FromSlash(episodeTwo[0].Path))
	twoNFO := filepath.Join(lib, "Some Show (2019)", "Season 01", "Some Show - S01E02 - Second.nfo")
	tvshowNFO := filepath.Join(lib, "Some Show (2019)", "tvshow.nfo")
	for _, path := range []string{oneMedia, oneNFO, twoMedia, twoNFO, tvshowNFO} {
		readFile(t, path)
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))

	// Retrying the same episode must not rewrite sidecars, clobber media, or archive anything.
	retry := base
	retry.Existing = []string{episodeOne[0].Path, "Some Show (2019)/Season 01/Some Show - S01E01 - Pilot.nfo", "Some Show (2019)/tvshow.nfo"}
	retryFiles, err := Import(context.Background(), retry, []Source{{Name: "first.mkv"}})
	if err != nil {
		t.Fatalf("retry episode one: %v", err)
	}
	if len(retryFiles) != 1 || retryFiles[0].Path != episodeOne[0].Path {
		t.Fatalf("retry files = %+v", retryFiles)
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
	if readFile(t, oneMedia) != "episode-one" || readFile(t, oneNFO) == "" {
		t.Fatal("retry changed episode one")
	}

	// A renamed upgrade of episode one archives only its own old paths.
	upgrade := base
	upgrade.Episode = &Episode{Season: 1, Numbers: []int{1}, Title: "Pilot Extended"}
	upgrade.Existing = append([]string(nil), retry.Existing...)
	if _, err := Import(context.Background(), upgrade, []Source{{Name: "first.mkv"}}); err != nil {
		t.Fatalf("upgrade episode one: %v", err)
	}
	mustNotExist(t, oneMedia)
	mustNotExist(t, oneNFO)
	if readFile(t, filepath.Join(lib, ".recycle", "Some Show (2019)", "Season 01", "Some Show - S01E01 - Pilot.mkv")) != "episode-one" {
		t.Fatal("upgraded media was not archived")
	}
	if readFile(t, filepath.Join(lib, ".recycle", "Some Show (2019)", "Season 01", "Some Show - S01E01 - Pilot.nfo")) == "" {
		t.Fatal("replaced sidecar was not archived")
	}
	if readFile(t, twoMedia) != "episode-two" || readFile(t, twoNFO) == "" {
		t.Fatal("upgrading episode one touched episode two")
	}
	readFile(t, tvshowNFO)
	readFile(t, filepath.Join(lib, "Some Show (2019)", "Season 01", "Some Show - S01E01 - Pilot Extended.nfo"))
}

func TestImportTVRefusesUnrelatedDestination(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	dest := filepath.Join(lib, "Some Show (2019) [imdb-tt1234567]", "Season 01", "Some Show - S01E01 - Pilot [1080p WEB].mkv")
	writeFile(t, dest, "another-copy")
	writeFile(t, filepath.Join(src, "Show.S01E01.1080p.WEB.mkv"), "new-download")
	opts := tvOptions(src, lib, &Episode{Season: 1, Numbers: []int{1}, Title: "Pilot"}, true)
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S01E01.1080p.WEB.mkv"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if readFile(t, dest) != "another-copy" {
		t.Fatal("an unowned episode file was clobbered")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))
	mustNotExist(t, filepath.Join(lib, "Some Show (2019) [imdb-tt1234567]", "tvshow.nfo"))
	readFile(t, filepath.Join(src, "Show.S01E01.1080p.WEB.mkv"))
}

func TestImportTVSpecialsNFOKeepsSeasonZero(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S00E01.mkv"), "special")
	opts := tvOptions(src, lib, &Episode{Season: 0, Numbers: []int{1}, Title: "Special"}, true)
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S00E01.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	raw := readFile(t, filepath.Join(lib, "Some Show (2019) [imdb-tt1234567]", "Season 00", "Some Show - S00E01 - Special [1080p WEB].nfo"))
	if !strings.Contains(raw, "<season>0</season>") || !strings.Contains(raw, "<episode>1</episode>") {
		t.Fatalf("special episode nfo = %s", raw)
	}
}

func TestImportTVNFOFailureRollsBackEverything(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Show.S01E01.mkv"), "payload")
	opts := tvOptions(src, lib, &Episode{Season: 1, Numbers: []int{1}, Title: "Pilot"}, true)
	opts.Mode = ModeMove
	original := publishAt
	defer func() { publishAt = original }()
	publishAt = func(root *os.Root, tempRel, destRel string) error {
		if strings.HasSuffix(destRel, ".nfo") && !strings.HasSuffix(destRel, "tvshow.nfo") {
			return errors.New("injected episode nfo failure")
		}
		return original(root, tempRel, destRel)
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Show.S01E01.mkv"}}); err == nil {
		t.Fatal("expected the injected nfo failure")
	}
	if got := entries(t, lib); len(got) != 0 {
		t.Fatalf("rollback left entries: %v", got)
	}
	readFile(t, filepath.Join(src, "Show.S01E01.mkv"))
}

func TestScanTV(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Shows/The Show (2019)/Season 01/The Show - S01E01 - Pilot.mkv": "one",
		"Shows/The Show (2019)/Season 01/The Show - S01E02E03.mkv":      "two-three",
		"Shows/The Show (2019)/Season 02/S02E01.mkv":                    "season-two",
		"Shows/The Show (2019)/Season 01/sample.mkv":                    "sample",
		"Shows/Another Show/Season 1/Another.Show.S01.1080p.WEB.mkv":    "pack",
		"Shows/Another Show/Season 1/S01E01.mkv":                        "no-title",
		"Shows/Another Show/Season 1/Another.Show.S01E02.CD1.mkv":       "part-one",
		"Shows/Another Show/Season 1/Another.Show.S01E02.CD2.mkv":       "part-two",
		"Daily Show/The.Daily.Show.2020.10.05.1080p.mkv":                "daily",
		"Movies/The Matrix (1999)/The Matrix.mkv":                       "movie",
		".recycle/old.mkv": "recycled",
	}
	for name, data := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), data)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.mkv"), "secret")
	if err := os.Symlink(filepath.Join(outside, "secret.mkv"), filepath.Join(root, "Shows", "link.mkv")); err != nil {
		t.Fatal(err)
	}

	candidates, err := ScanTV(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanTV: %v", err)
	}
	byPath := make(map[string]TVCandidate, len(candidates))
	for _, candidate := range candidates {
		byPath[candidate.Path] = candidate
	}
	if len(candidates) != 8 {
		t.Fatalf("candidates = %+v", candidates)
	}

	episode := byPath["Shows/The Show (2019)/Season 01/The Show - S01E01 - Pilot.mkv"]
	if episode.Title != "The Show" || episode.Year != 2019 || episode.Season != 1 ||
		len(episode.Numbers) != 1 || episode.Numbers[0] != 1 || episode.Pack || episode.AirDate != "" {
		t.Fatalf("episode candidate = %+v", episode)
	}
	multi := byPath["Shows/The Show (2019)/Season 01/The Show - S01E02E03.mkv"]
	if multi.Season != 1 || len(multi.Numbers) != 2 || multi.Numbers[0] != 2 || multi.Numbers[1] != 3 {
		t.Fatalf("multi candidate = %+v", multi)
	}
	folderNamed := byPath["Shows/The Show (2019)/Season 02/S02E01.mkv"]
	if folderNamed.Title != "The Show" || folderNamed.Year != 2019 || folderNamed.Season != 2 {
		t.Fatalf("season folder fallback = %+v", folderNamed)
	}
	pack := byPath["Shows/Another Show/Season 1/Another.Show.S01.1080p.WEB.mkv"]
	if pack.Title != "Another Show" || pack.Season != 1 || !pack.Pack || len(pack.Numbers) != 0 {
		t.Fatalf("pack candidate = %+v", pack)
	}
	noTitle := byPath["Shows/Another Show/Season 1/S01E01.mkv"]
	if noTitle.Title != "Another Show" || noTitle.Season != 1 {
		t.Fatalf("series folder fallback = %+v", noTitle)
	}
	if noTitle.Title == "Season 1" {
		t.Fatal("a season folder was misread as the series")
	}
	partOne := byPath["Shows/Another Show/Season 1/Another.Show.S01E02.CD1.mkv"]
	partTwo := byPath["Shows/Another Show/Season 1/Another.Show.S01E02.CD2.mkv"]
	if partOne.Season != 1 || len(partOne.Numbers) != 1 || partOne.Numbers[0] != 2 || partTwo.Numbers[0] != 2 ||
		partOne.Size != int64(len("part-one")) || partTwo.Size != int64(len("part-two")) {
		t.Fatalf("per-file candidates = %+v, %+v", partOne, partTwo)
	}
	daily := byPath["Daily Show/The.Daily.Show.2020.10.05.1080p.mkv"]
	if daily.Title != "The Daily Show" || daily.Season != -1 || daily.AirDate != "2020-10-05" ||
		len(daily.Numbers) != 0 || daily.Pack {
		t.Fatalf("daily candidate = %+v", daily)
	}
	if _, ok := byPath["Shows/link.mkv"]; ok {
		t.Fatal("a symlink was scanned")
	}
}

func TestScanTVCancellationAndBadRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanTV(ctx, t.TempDir()); err == nil {
		t.Fatal("canceled scan should fail")
	}
	if _, err := ScanTV(context.Background(), ""); err == nil {
		t.Fatal("empty root should fail")
	}
	if _, err := ScanTV(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root should fail")
	}
}

func TestTVCandidateJSONFields(t *testing.T) {
	raw, err := json.Marshal(TVCandidate{Candidate: Candidate{Path: "Show/S01E01.mkv"}, Season: 1, Numbers: []int{1}, Pack: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"path"`, `"season"`, `"episodes"`, `"airDate"`, `"pack"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("candidate JSON %s lacks %s", raw, field)
		}
	}
}
