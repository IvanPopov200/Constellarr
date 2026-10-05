package music

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

// releaseQuery names the music release search; Text drives free-text searches.
type releaseQuery struct {
	Artist string
	Album  string
	Year   int
	Text   string
}

// providerOutcome keeps one provider result so a partial success stays usable.
type providerOutcome struct {
	items []Release
	err   error
}

// searchProviders runs the Newznab and torrent searches together; one working provider is enough.
func (s *Service) searchProviders(ctx context.Context, query releaseQuery) ([]Release, error) {
	outcomes := make(chan providerOutcome, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		outcomes <- s.usenetSearch(ctx, query)
	}()
	go func() {
		defer workers.Done()
		outcomes <- s.torrentSearch(ctx, query)
	}()
	workers.Wait()
	close(outcomes)

	items := make([]Release, 0, musicSearchLimit)
	var failures []error
	unavailable := 0
	for outcome := range outcomes {
		items = append(items, outcome.items...)
		switch {
		case outcome.err == nil:
		case errors.Is(outcome.err, ErrNotConfigured), errors.Is(outcome.err, downloads.ErrNotConfigured):
			unavailable++
		default:
			failures = append(failures, outcome.err)
		}
	}
	items = dedupeReleases(items)
	if len(items) > 0 {
		return items, nil
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	if unavailable == 2 {
		return nil, fmt.Errorf("%w: no music release provider is configured", ErrNotConfigured)
	}
	return items, nil
}

func (s *Service) usenetSearch(ctx context.Context, query releaseQuery) providerOutcome {
	client, err := s.indexer()
	if err != nil {
		return providerOutcome{err: err}
	}
	if query.Album == "" && query.Artist == "" {
		items, err := client.SearchQuery(ctx, query.Text)
		if err != nil {
			return providerOutcome{err: err}
		}
		return providerOutcome{items: items}
	}
	items, err := client.SearchAlbum(ctx, query.Artist, query.Album, query.Year)
	if err != nil {
		return providerOutcome{err: err}
	}
	return providerOutcome{items: items}
}

func (s *Service) torrentSearch(ctx context.Context, query releaseQuery) providerOutcome {
	text := strings.TrimSpace(firstNonEmpty(query.Text, strings.TrimSpace(query.Artist+" "+query.Album)))
	if text == "" {
		return providerOutcome{err: fmt.Errorf("%w: a music search query is required", ErrInvalid)}
	}
	items, err := s.Downloads.SearchTorrents(ctx, text)
	if err != nil {
		return providerOutcome{err: err}
	}
	releases := make([]Release, 0, len(items))
	for _, item := range items {
		if release, ok := torrentRelease(item); ok {
			releases = append(releases, release)
		}
	}
	return providerOutcome{items: releases}
}

// torrentRelease converts a torrent indexer release, dropping names that are clearly not music.
func torrentRelease(item indexer.Release) (Release, bool) {
	title := strings.TrimSpace(item.Title)
	if !validTorrentReleaseID(item.ID) || title == "" || !musicReleaseTitle(title) {
		return Release{}, false
	}
	protocol := strings.TrimSpace(item.Protocol)
	if protocol == "" {
		protocol = "torrent"
	}
	return Release{
		ID: item.ID, Title: title, Size: item.Size, Published: item.Published,
		Protocol: protocol, Source: item.Source, Seeders: item.Seeders,
	}, true
}

func validTorrentReleaseID(id string) bool {
	if !strings.HasPrefix(id, downloads.TorrentPrefix) || len(id) > 300 {
		return false
	}
	remainder := strings.TrimPrefix(id, downloads.TorrentPrefix)
	if remainder == "" {
		return false
	}
	for i := 0; i < len(remainder); i++ {
		if c := remainder[i]; c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

var (
	videoReleasePattern = regexp.MustCompile(`\b(2160p|1440p|1080p|1080i|720p|576p|480p|4k|uhd|remux|bluray|blu-ray|brrip|bdrip|web-?dl|webrip|hdtv|dvdrip|x264|x265|h\.?264|h\.?265|hevc|avc|xvid|divx|s\d{1,2}e\d{1,3}|season[ ._-]?\d{1,2}|complete series)\b`)
	ebookReleasePattern = regexp.MustCompile(`\.(epub|mobi|azw3?|pdf|cbr|cbz)\b|\b(audiobook|ebook)\b`)
	audioFormatPattern  = regexp.MustCompile(`\b(flac|alac|ape|wav|aiff|dsd|dsf|dff|mp3|m4a|aac|ogg|opus|wma|24bit|16bit|hi-?res|lossless|dts-?hd|dolby)\b`)
)

// musicReleaseTitle rejects video and ebook names while keeping Blu-Ray audio rips.
func musicReleaseTitle(title string) bool {
	lower := strings.ToLower(title)
	if ebookReleasePattern.MatchString(lower) {
		return false
	}
	return !videoReleasePattern.MatchString(lower) || audioFormatPattern.MatchString(lower)
}

func dedupeReleases(releases []Release) []Release {
	seen := make(map[string]bool, len(releases))
	kept := make([]Release, 0, len(releases))
	for _, release := range releases {
		if release.ID == "" || seen[release.ID] {
			continue
		}
		seen[release.ID] = true
		kept = append(kept, release)
	}
	return kept
}
