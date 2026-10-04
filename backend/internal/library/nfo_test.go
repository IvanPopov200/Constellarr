package library

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

type parsedNFO struct {
	XMLName   xml.Name
	Title     string   `xml:"title"`
	Year      int      `xml:"year"`
	Plot      string   `xml:"plot"`
	Rating    float64  `xml:"rating"`
	IMDbID    string   `xml:"imdbid"`
	Directors []string `xml:"director"`
	Genres    []string `xml:"genre"`
	Actors    []struct {
		Name  string `xml:"name"`
		Order int    `xml:"order"`
	} `xml:"actor"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
}

func TestImportWritesEscapedJellyfinNFO(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	rating := 8.5
	opts := Options{
		SourceRoot: src, Root: lib, WriteNFO: true,
		Metadata: metadata.Title{
			Title:     `A & B <C> "D"`,
			Year:      2020,
			Plot:      "Tom & Jerry <script>alert</script>",
			Rating:    &rating,
			Directors: []string{"Jane & John"},
			Cast:      []string{"X <Y>", "Z"},
			Genres:    []string{"Action & Adventure"},
			IMDbID:    "tt1234567",
		},
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	nfoPath := filepath.Join(lib, "A & B C D (2020)", "movie.nfo")
	raw := readFile(t, nfoPath)
	if strings.Contains(raw, "<script>") {
		t.Fatalf("nfo is not XML escaped: %s", raw)
	}
	if !strings.HasPrefix(raw, xml.Header) || !strings.Contains(raw, "&amp;") {
		t.Fatalf("nfo header or escaping missing: %s", raw)
	}
	var doc parsedNFO
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("nfo is not valid XML: %v", err)
	}
	if doc.Title != `A & B <C> "D"` || doc.Year != 2020 || doc.Plot != "Tom & Jerry <script>alert</script>" {
		t.Fatalf("nfo basics = %+v", doc)
	}
	if doc.Rating != 8.5 || doc.IMDbID != "tt1234567" {
		t.Fatalf("nfo rating/imdb = %+v", doc)
	}
	if len(doc.Directors) != 1 || doc.Directors[0] != "Jane & John" {
		t.Fatalf("nfo directors = %v", doc.Directors)
	}
	if len(doc.Genres) != 1 || doc.Genres[0] != "Action & Adventure" {
		t.Fatalf("nfo genres = %v", doc.Genres)
	}
	if len(doc.Actors) != 2 || doc.Actors[0].Name != "X <Y>" || doc.Actors[1].Order != 1 {
		t.Fatalf("nfo actors = %v", doc.Actors)
	}
	if len(doc.UniqueIDs) != 1 || doc.UniqueIDs[0].Type != "imdb" || doc.UniqueIDs[0].Value != "tt1234567" {
		t.Fatalf("nfo unique ids = %v", doc.UniqueIDs)
	}
}

func TestImportNFOIsIdempotentAndArchivesOldSidecar(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	opts := Options{
		SourceRoot: src, Root: lib, WriteNFO: true,
		Metadata: metadata.Title{Title: "Movie", Year: 2020, IMDbID: "tt1234567"},
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	nfoPath := filepath.Join(lib, "Movie (2020)", "movie.nfo")
	first := readFile(t, nfoPath)
	opts.Existing = []string{"Movie (2020)/Movie (2020).mkv", "Movie (2020)/movie.nfo"}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if readFile(t, nfoPath) != first {
		t.Fatal("retry rewrote the sidecar")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle"))

	writeFile(t, nfoPath, "foreign-sidecar")
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err != nil {
		t.Fatalf("third import: %v", err)
	}
	if readFile(t, nfoPath) != first {
		t.Fatal("foreign sidecar was not replaced")
	}
	if readFile(t, filepath.Join(lib, ".recycle", "Movie (2020)", "movie.nfo")) != "foreign-sidecar" {
		t.Fatal("foreign sidecar was not archived")
	}
}

func TestImportNFOPublishFailureRollsBackMedia(t *testing.T) {
	src, lib := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "Movie.mkv"), "payload")
	writeFile(t, filepath.Join(lib, "Movie (2020)", "movie.nfo"), "old-sidecar")
	opts := Options{
		SourceRoot: src, Root: lib, Mode: ModeMove, WriteNFO: true,
		Metadata: metadata.Title{Title: "Movie", Year: 2020},
	}
	original := publishAt
	defer func() { publishAt = original }()
	publishAt = func(root *os.Root, tempRel, destRel string) error {
		if destRel == "Movie (2020)/movie.nfo" {
			return errors.New("injected nfo failure")
		}
		return original(root, tempRel, destRel)
	}
	if _, err := Import(context.Background(), opts, []Source{{Name: "Movie.mkv"}}); err == nil {
		t.Fatal("expected the injected nfo failure")
	}
	mustNotExist(t, filepath.Join(lib, "Movie (2020)", "Movie (2020).mkv"))
	if readFile(t, filepath.Join(lib, "Movie (2020)", "movie.nfo")) != "old-sidecar" {
		t.Fatal("old sidecar was not restored")
	}
	mustNotExist(t, filepath.Join(lib, ".recycle", "Movie (2020)", "movie.nfo"))
	readFile(t, filepath.Join(src, "Movie.mkv"))
}
