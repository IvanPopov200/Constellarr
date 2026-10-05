package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ReleaseGroup is one resolved MusicBrainz release group for a discovery request.
type ReleaseGroup struct {
	MusicBrainzID       string  `json:"musicBrainzId"`
	ArtistMusicBrainzID string  `json:"artistMusicBrainzId"`
	ArtistName          string  `json:"artistName"`
	Title               string  `json:"title"`
	Year                int     `json:"year"`
	ReleaseDate         string  `json:"releaseDate"`
	Type                string  `json:"type"`
	Tracks              []Track `json:"tracks,omitempty"`
}

// LookupReleaseGroup resolves a release group, refusing records without an identifiable artist or title.
func (s *Service) LookupReleaseGroup(ctx context.Context, releaseGroupID string) (ReleaseGroup, error) {
	mbid := strings.TrimSpace(releaseGroupID)
	if !validMBID(mbid) {
		return ReleaseGroup{}, fmt.Errorf("%w: the MusicBrainz release group ID is invalid", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return ReleaseGroup{}, err
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return ReleaseGroup{}, err
	}
	looked, err := client.lookupAlbum(ctx, mbid)
	if err != nil {
		return ReleaseGroup{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(looked.MusicBrainzID), mbid) || strings.TrimSpace(looked.Title) == "" {
		return ReleaseGroup{}, fmt.Errorf("%w: the release group could not be resolved", ErrNotFound)
	}
	if !validMBID(strings.TrimSpace(looked.ArtistID)) {
		return ReleaseGroup{}, fmt.Errorf("%w: the release group has no resolvable artist", ErrNotFound)
	}
	return ReleaseGroup{
		MusicBrainzID:       looked.MusicBrainzID,
		ArtistMusicBrainzID: strings.TrimSpace(looked.ArtistID),
		ArtistName:          looked.ArtistName,
		Title:               looked.Title,
		Year:                looked.Year,
		ReleaseDate:         looked.ReleaseDate,
		Type:                looked.Type,
		Tracks:              looked.Tracks,
	}, nil
}

// AlbumByReleaseGroup loads one decorated library album by its release-group ID.
func (s *Service) AlbumByReleaseGroup(ctx context.Context, releaseGroupID string) (Album, error) {
	mbid := strings.TrimSpace(releaseGroupID)
	if !validMBID(mbid) {
		return Album{}, ErrNotFound
	}
	album, err := s.Store.AlbumByMusicBrainz(ctx, mbid)
	if err != nil {
		return Album{}, err
	}
	return s.Album(ctx, album.ID)
}

// RequestAlbum resolves one album for a request by library ID or release-group ID.
func (s *Service) RequestAlbum(ctx context.Context, id string) (Album, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Album{}, ErrNotFound
	}
	if validMBID(id) {
		album, err := s.Store.AlbumByMusicBrainz(ctx, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Album{}, err
		}
		if err == nil {
			return s.Album(ctx, album.ID)
		}
	}
	return s.Album(ctx, id)
}

type AddReleaseGroupInput struct {
	MusicBrainzID string `json:"musicBrainzId"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
}

// AddReleaseGroup adds only the selected album (its artist stays unmonitored) and is idempotent across retries.
func (s *Service) AddReleaseGroup(ctx context.Context, input AddReleaseGroupInput) (Album, error) {
	mbid := strings.TrimSpace(input.MusicBrainzID)
	if !validMBID(mbid) {
		return Album{}, fmt.Errorf("%w: the MusicBrainz release group ID is invalid", ErrInvalid)
	}
	if existing, err := s.AlbumByReleaseGroup(ctx, mbid); err == nil {
		return s.monitorRequestAlbum(ctx, existing)
	} else if !errors.Is(err, ErrNotFound) {
		return Album{}, err
	}
	group, err := s.LookupReleaseGroup(ctx, mbid)
	if err != nil {
		return Album{}, err
	}
	if title := strings.TrimSpace(input.Title); title != "" && !strings.EqualFold(title, group.Title) {
		return Album{}, fmt.Errorf("%w: the requested title does not match the MusicBrainz release group", ErrConflict)
	}
	artist, err := s.requestArtist(ctx, group)
	if err != nil {
		return Album{}, err
	}
	year := group.Year
	if year == 0 {
		year = input.Year
	}
	album := Album{
		ID: newID(), ArtistID: artist.ID, ArtistName: artist.Name,
		MusicBrainzID: group.MusicBrainzID, Title: group.Title, Year: year,
		ReleaseDate: group.ReleaseDate, Type: group.Type, Monitored: true,
		ProfileID: artist.ProfileID, RootID: artist.RootID, Tracks: group.Tracks,
	}
	saved, err := s.Store.SaveAlbum(ctx, album)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			if existing, lookupErr := s.AlbumByReleaseGroup(ctx, mbid); lookupErr == nil {
				return s.monitorRequestAlbum(ctx, existing)
			}
		}
		return Album{}, err
	}
	_ = s.Store.Event(ctx, saved.ID, artist.ID, "added", "Added album "+saved.Title)
	return s.Album(ctx, saved.ID)
}

// requestArtist reuses the library artist or creates it unmonitored; the lookup avoids AddArtist's nil query values.
func (s *Service) requestArtist(ctx context.Context, group ReleaseGroup) (Artist, error) {
	existing, err := s.Store.ArtistByMusicBrainz(ctx, group.ArtistMusicBrainzID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Artist{}, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Artist{}, err
	}
	root, err := s.chooseRoot(cfg, "")
	if err != nil {
		return Artist{}, err
	}
	profile, ok := profileByID(cfg, "")
	if !ok {
		return Artist{}, fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return Artist{}, err
	}
	body, err := client.get(ctx, "artist/"+group.ArtistMusicBrainzID, url.Values{}, maxBrainzBytes)
	if err != nil {
		return Artist{}, err
	}
	var looked mbArtist
	if err := json.Unmarshal(body, &looked); err != nil || looked.ID == "" {
		return Artist{}, errors.New("music: the metadata provider returned an unusable response")
	}
	if !strings.EqualFold(strings.TrimSpace(looked.ID), group.ArtistMusicBrainzID) {
		return Artist{}, fmt.Errorf("%w: the release group artist could not be resolved", ErrNotFound)
	}
	artist := Artist{
		ID: newID(), MusicBrainzID: looked.ID, Name: firstNonEmpty(group.ArtistName, looked.Name),
		SortName: looked.SortName, Disambiguation: looked.Disambiguation, Country: looked.Country, Type: looked.Type,
		Monitored: false, MonitorOption: "none", ProfileID: profile.ID, RootID: root.ID,
	}
	saved, err := s.Store.SaveArtistWithAlbums(ctx, artist, nil)
	if uniqueViolation(err) {
		return s.Store.ArtistByMusicBrainz(ctx, group.ArtistMusicBrainzID)
	}
	if err != nil {
		return Artist{}, err
	}
	_ = s.Store.Event(ctx, "", saved.ID, "added", "Added artist "+saved.Name)
	return s.Artist(ctx, saved.ID)
}

func (s *Service) monitorRequestAlbum(ctx context.Context, album Album) (Album, error) {
	if album.Monitored {
		return album, nil
	}
	return s.MonitorAlbum(ctx, album.ID, AlbumMonitorInput{Monitored: true})
}
