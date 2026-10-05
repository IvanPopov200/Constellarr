package music

import (
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

func TestTorrentReleaseConversion(t *testing.T) {
	item := indexer.Release{
		ID: "torrent_source-a_0123456789abcdef", Protocol: "torrent", Source: "source-a", Seeders: 14,
		Title: "Muse - Absolution (2003) [FLAC] [24bit]", Size: 400 << 20,
	}
	release, ok := torrentRelease(item)
	if !ok {
		t.Fatal("a music torrent was dropped")
	}
	if release.Protocol != "torrent" || release.Source != "source-a" || release.Seeders != 14 || release.Size != 400<<20 {
		t.Fatalf("torrent release fields = %+v", release)
	}
	if release.ID != item.ID || release.Title != item.Title {
		t.Fatalf("torrent identity changed: %+v", release)
	}

	for name, candidate := range map[string]indexer.Release{
		"missing prefix": {ID: "0123456789abcdef", Title: "Muse - Absolution [FLAC]"},
		"video":          {ID: "torrent_a_1", Title: "Some.Movie.2020.1080p.BluRay.x264-GROUP"},
		"series":         {ID: "torrent_a_2", Title: "Some Show S02E05 1080p WEB-DL"},
		"ebook":          {ID: "torrent_a_3", Title: "Some Author - A Book.epub"},
		"empty title":    {ID: "torrent_a_4", Title: "   "},
	} {
		if _, ok := torrentRelease(candidate); ok {
			t.Fatalf("%s was accepted as a music release", name)
		}
	}
}

func TestMusicReleaseTitleKeepsAudioRips(t *testing.T) {
	for _, title := range []string{
		"Muse - Absolution (2003) [FLAC]",
		"Muse - Absolution [MP3 320] [WEB]",
		"Muse - Hullabaloo [Blu-Ray Audio] [FLAC 24bit]",
		"The Beatles - Abbey Road (1969) [Vinyl 24-96 FLAC]",
		"Various Artists - Now 100 (2018) [AAC 256]",
	} {
		if !musicReleaseTitle(title) {
			t.Fatalf("%q was rejected as non-music", title)
		}
	}
	for _, title := range []string{
		"Some.Movie.2020.1080p.BluRay.x264-GROUP",
		"Some Show S02 COMPLETE 1080p",
		"Audiobook - Some Novel.mp3",
	} {
		if musicReleaseTitle(title) {
			t.Fatalf("%q was accepted as music", title)
		}
	}
}

func TestSortReleasesPrefersSeedersForEqualQuality(t *testing.T) {
	profile := QualityProfile{ID: "standard", Name: "Standard", Formats: []string{"flac", "mp3"}, Cutoff: "flac"}
	releases := []Release{
		{ID: "torrent-low", Title: "Muse - Absolution [FLAC]", Protocol: "torrent", Seeders: 2, Decision: evaluate(profile, "Muse - Absolution [FLAC]", 1<<30, nil)},
		{ID: "torrent-high", Title: "Muse - Absolution [FLAC]", Protocol: "torrent", Seeders: 40, Decision: evaluate(profile, "Muse - Absolution [FLAC]", 1<<30, nil)},
		{ID: "usenet", Title: "Muse - Absolution [FLAC]", Decision: evaluate(profile, "Muse - Absolution [FLAC]", 1<<30, nil)},
	}
	sortReleases(releases)
	if releases[0].ID != "torrent-high" || releases[1].ID != "torrent-low" || releases[2].ID != "usenet" {
		t.Fatalf("sorted order = %s, %s, %s", releases[0].ID, releases[1].ID, releases[2].ID)
	}
}

func TestDedupeReleases(t *testing.T) {
	releases := dedupeReleases([]Release{
		{ID: "a", Title: "first"}, {ID: "b", Title: "second"}, {ID: "a", Title: "duplicate"}, {ID: "", Title: "empty"},
	})
	if len(releases) != 2 || releases[0].ID != "a" || releases[1].ID != "b" {
		t.Fatalf("dedupe = %+v", releases)
	}
}

func TestPathWithin(t *testing.T) {
	if !pathWithin("/data/torrents/job-1", "/data/torrents/job-1") || !pathWithin("/data/torrents/job-1", "/data/torrents/job-1/album") {
		t.Fatal("a torrent output folder was not recognized")
	}
	if pathWithin("/data/torrents/job-1", "/data/torrents/job-2") || pathWithin("/data/torrents/job-1", "/data/library") {
		t.Fatal("an unrelated folder was treated as a torrent payload")
	}
}
