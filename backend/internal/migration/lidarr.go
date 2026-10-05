package migration

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// lidarrArtist mirrors the fields of Lidarr's ArtistResource that the music catalog needs.
type lidarrArtist struct {
	ID               int    `json:"id"`
	ArtistName       string `json:"artistName"`
	SortName         string `json:"sortName"`
	Disambiguation   string `json:"disambiguation"`
	ForeignArtistID  string `json:"foreignArtistId"`
	Monitored        bool   `json:"monitored"`
	MonitorNewItems  string `json:"monitorNewItems"`
	QualityProfileID int    `json:"qualityProfileId"`
	RootFolderPath   string `json:"rootFolderPath"`
	Path             string `json:"path"`
	ArtistType       string `json:"artistType"`
	Tags             []int  `json:"tags"`
}

type lidarrAlbum struct {
	ID             int    `json:"id"`
	ArtistID       int    `json:"artistId"`
	Title          string `json:"title"`
	ForeignAlbumID string `json:"foreignAlbumId"`
	AlbumType      string `json:"albumType"`
	ReleaseDate    string `json:"releaseDate"`
	Monitored      bool   `json:"monitored"`
	ProfileID      int    `json:"profileId"`
	MediumCount    int    `json:"mediumCount"`
}

type lidarrNaming struct {
	ArtistFolderFormat   string `json:"artistFolderFormat"`
	StandardTrackFormat  string `json:"standardTrackFormat"`
	MultiDiscTrackFormat string `json:"multiDiscTrackFormat"`
}

func (s *Service) discoverLidarr(ctx context.Context, connection Connection) (snapshot, error) {
	headers := map[string]string{"X-Api-Key": connection.APIKey}
	var status arrStatus
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/system/status", headers, nil, &status); err != nil {
		return snapshot{}, err
	}
	var roots []arrRoot
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/rootfolder", headers, nil, &roots); err != nil {
		return snapshot{}, err
	}
	var profiles []arrProfile
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/qualityprofile", headers, nil, &profiles); err != nil {
		return snapshot{}, err
	}
	var tags []arrTag
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/tag", headers, nil, &tags); err != nil {
		return snapshot{}, err
	}
	var artists []lidarrArtist
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/artist", headers, nil, &artists); err != nil {
		return snapshot{}, err
	}
	var naming lidarrNaming
	out := snapshot{Versions: map[App]string{AppLidarr: status.Version}, Music: &musicSnapshot{}}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/config/naming", headers, nil, &naming); err != nil {
		out.Warnings = append(out.Warnings, sanitize("Lidarr naming settings could not be read: "+err.Error(), connection.secrets()))
	} else {
		out.Music.Naming = append(out.Music.Naming, musicNaming(AppLidarr, naming))
		if strings.TrimSpace(naming.MultiDiscTrackFormat) != "" {
			out.Unsupported = append(out.Unsupported, Unsupported{Area: "music naming", Detail: "Lidarr's multi-disc track format is not imported; Constellarr uses its own multi-disc template"})
		}
	}
	for _, root := range roots {
		out.Music.Roots = append(out.Music.Roots, RootPlan{Source: AppLidarr, Media: MediaMusic, Path: root.Path, Accessible: root.Accessible})
	}
	out.Music.Profiles = append(out.Music.Profiles, s.lidarrProfiles(profiles)...)
	labels := tagLabels(tags)
	for _, artist := range artists {
		plan := MusicArtistPlan{
			ID: artist.ID, Name: strings.TrimSpace(artist.ArtistName), SortName: strings.TrimSpace(artist.SortName),
			MusicBrainzID: strings.ToLower(strings.TrimSpace(artist.ForeignArtistID)),
			Monitored:     artist.Monitored, MonitorOption: lidarrMonitorOption(artist.MonitorNewItems),
			Type: strings.TrimSpace(artist.ArtistType),
			Tags: labelsFor(labels, artist.Tags), ProfileKey: profileKey(AppLidarr, artist.QualityProfileID),
			RootPath: artist.RootFolderPath,
		}
		if plan.Name == "" {
			plan.Name = plan.MusicBrainzID
		}
		if !validMusicBrainzID(plan.MusicBrainzID) {
			plan.Unsupported = "the artist has no MusicBrainz identifier; add it manually after migration"
		} else if plan.RootPath == "" {
			plan.Unsupported = "the artist has no root folder in Lidarr"
		}
		if plan.Unsupported == "" {
			albums, err := s.lidarrAlbums(ctx, connection, headers, artist)
			if err != nil {
				out.Warnings = append(out.Warnings, sanitize(fmt.Sprintf("Lidarr albums for %s could not be read: %s", plan.Name, err.Error()), connection.secrets()))
			}
			for _, album := range albums {
				entry := MusicAlbumPlan{
					ID: album.ID, Title: strings.TrimSpace(album.Title), MusicBrainzID: strings.ToLower(strings.TrimSpace(album.ForeignAlbumID)),
					Year: yearFromDate(album.ReleaseDate), Type: strings.TrimSpace(album.AlbumType), Monitored: album.Monitored,
				}
				if !validMusicBrainzID(entry.MusicBrainzID) {
					entry.Unsupported = "the album has no MusicBrainz identifier; add it manually after migration"
				} else if entry.Title == "" {
					entry.Unsupported = "the album has no title"
				}
				if album.ProfileID != 0 && album.ProfileID != artist.QualityProfileID {
					out.Unsupported = append(out.Unsupported, Unsupported{
						Area:   "music profile",
						Detail: fmt.Sprintf("album %q uses its own Lidarr profile; the artist profile is applied instead", entry.Title),
					})
				}
				plan.Albums = append(plan.Albums, entry)
			}
		}
		out.Music.Artists = append(out.Music.Artists, plan)
	}
	return out, nil
}

func (s *Service) lidarrAlbums(ctx context.Context, connection Connection, headers map[string]string, artist lidarrArtist) ([]lidarrAlbum, error) {
	var albums []lidarrAlbum
	if err := s.requestJSON(ctx, connection, http.MethodGet,
		fmt.Sprintf("/api/v1/album?artistId=%d", artist.ID), headers, nil, &albums); err != nil {
		return nil, err
	}
	return albums, nil
}

func lidarrMonitorOption(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "none":
		return "none"
	case "new":
		return "future"
	default:
		return "all"
	}
}

var musicBrainzPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validMusicBrainzID(id string) bool {
	return musicBrainzPattern.MatchString(strings.ToLower(strings.TrimSpace(id)))
}

var (
	lidarrFormats = map[string]string{
		"flac": "flac", "flac 24bit": "flac",
		"alac": "alac", "alac 24bit": "alac",
		"wav": "wav", "wavpack": "wavpack", "ape": "ape", "opus": "opus",
		"ogg vorbis q10": "vorbis", "ogg vorbis q9": "vorbis", "ogg vorbis q8": "vorbis",
		"ogg vorbis q7": "vorbis", "ogg vorbis q6": "vorbis", "ogg vorbis q5": "vorbis",
		"aac-192": "aac", "aac-256": "aac", "aac-320": "aac", "aac-vbr": "aac",
		"wma": "wma",
	}
	lidarrLossless = map[string]bool{"flac": true, "alac": true, "wav": true, "wavpack": true, "ape": true, "aiff": true}
	bitratePattern = regexp.MustCompile(`-(\d{2,3})$`)
)

func (s *Service) lidarrProfiles(profiles []arrProfile) []MusicProfilePlan {
	out := make([]MusicProfilePlan, 0, len(profiles))
	for _, profile := range profiles {
		plan := MusicProfilePlan{Source: AppLidarr, Key: profileKey(AppLidarr, profile.ID), Name: profile.Name, Upgrade: profile.UpgradeAllowed}
		seen := map[string]bool{}
		for _, quality := range arrQualities(profile.Items) {
			name := strings.ToLower(strings.TrimSpace(quality))
			format, ok := lidarrFormats[name]
			if !ok {
				// Lidarr keeps low-bitrate MP3 tiers for existing files only; they are not selectable.
				if strings.HasPrefix(name, "mp3-") && lidarrBitrate(name) >= 128 {
					format = "mp3"
				} else {
					if !seen["u:"+quality] {
						plan.Unsupported = append(plan.Unsupported, quality)
						seen["u:"+quality] = true
					}
					continue
				}
			}
			if seen["f:"+format] {
				continue
			}
			seen["f:"+format] = true
			plan.Formats = append(plan.Formats, format)
			if !lidarrLossless[format] {
				if bitrate := lidarrBitrate(name); bitrate > 0 && (plan.MinBitrateKbps == 0 || bitrate < plan.MinBitrateKbps) {
					plan.MinBitrateKbps = bitrate
				}
			}
		}
		plan.LosslessOnly = len(plan.Formats) > 0
		for _, format := range plan.Formats {
			if !lidarrLossless[format] {
				plan.LosslessOnly = false
				break
			}
		}
		plan.Cutoff = lidarrCutoff(profile, plan.Formats)
		out = append(out, plan)
	}
	return out
}

// lidarrCutoff maps the numeric Lidarr cutoff back onto the mapped formats.
func lidarrCutoff(profile arrProfile, formats []string) string {
	name := strings.ToLower(arrCutoffName(profile))
	if name != "" {
		if format, ok := lidarrFormats[name]; ok {
			for _, mapped := range formats {
				if mapped == format {
					return mapped
				}
			}
		} else if strings.HasPrefix(name, "mp3-") {
			for _, mapped := range formats {
				if mapped == "mp3" {
					return "mp3"
				}
			}
		}
	}
	if len(formats) == 0 {
		return ""
	}
	return formats[0]
}

func lidarrBitrate(quality string) int {
	if quality == "mp3-vbr-v0" {
		return 245
	}
	if quality == "mp3-vbr-v2" {
		return 190
	}
	if match := bitratePattern.FindStringSubmatch(quality); len(match) == 2 {
		value, err := strconv.Atoi(match[1])
		if err == nil && value > 0 && value <= 1536 {
			return value
		}
	}
	return 0
}

// musicTemplateTokens mirrors the music module's supported naming tokens.
var musicTemplateTokens = map[string]bool{
	"artist": true, "trackArtist": true, "album": true, "year": true, "date": true, "type": true,
	"format": true, "quality": true, "track": true, "disc": true, "title": true,
	"mbAlbumId": true, "mbArtistId": true, "original": true,
}

var musicFormatTokens = map[string]string{
	"{artist name}":           "{artist}",
	"{album title}":           "{album}",
	"{release year}":          "{year}",
	"{release date}":          "{date}",
	"{track number}":          "{track:02}",
	"{track:02}":              "{track:02}",
	"{track:00}":              "{track:02}",
	"{disc}":                  "{disc}",
	"{track title}":           "{title}",
	"{medium number}":         "{disc}",
	"{quality full}":          "{quality}",
	"{album type}":            "{type}",
	"{musicbrainz album id}":  "{mbAlbumId}",
	"{musicbrainz artist id}": "{mbArtistId}",
}

var (
	lidarrTrackToken  = regexp.MustCompile(`(?i)\{track:\d*\}`)
	lidarrMediumToken = regexp.MustCompile(`(?i)\{(medium|disc)(:\d*)?\}`)
	lidarrMediumWord  = regexp.MustCompile(`(?i)\{(medium|disc) number\}`)
)

// normalizeLidarrTokens folds Lidarr's width modifiers into the music module's tokens.
func normalizeLidarrTokens(format string) string {
	format = lidarrTrackToken.ReplaceAllString(format, "{track:02}")
	format = lidarrMediumWord.ReplaceAllString(format, "{disc}")
	return lidarrMediumToken.ReplaceAllString(format, "{disc}")
}

// musicNaming maps Lidarr's artist folder and track formats onto the music module templates.
func musicNaming(app App, naming lidarrNaming) NamingPlan {
	folder, folderUnsupported := mapNamingTemplate(normalizeLidarrTokens(naming.ArtistFolderFormat), musicFormatTokens)
	file, fileUnsupported := mapNamingTemplate(normalizeLidarrTokens(naming.StandardTrackFormat), musicFormatTokens)
	if !strings.Contains(folder, "{album}") {
		folder = strings.TrimRight(folder, "/ ") + "/{album} ({year})"
	}
	plan := NamingPlan{Source: app, Media: MediaMusic, Folder: folder, File: file}
	plan.Unsupported = append(plan.Unsupported, folderUnsupported...)
	plan.Unsupported = append(plan.Unsupported, fileUnsupported...)
	plan.Applicable = len(plan.Unsupported) == 0 && validMusicTemplatePair(folder, file) == nil
	return plan
}

func validMusicTemplatePair(folder, file string) error {
	if err := validMusicTokens(folder, false); err != nil {
		return err
	}
	return validMusicTokens(file, true)
}

// validMusicTokens mirrors the music module token rules so previews fail before an apply would.
func validMusicTokens(template string, fileLevel bool) error {
	if strings.TrimSpace(template) == "" {
		return fmt.Errorf("empty template")
	}
	if len(template) > 512 || strings.Contains(template, `\`) || strings.Contains(template, "://") {
		return fmt.Errorf("unusable template")
	}
	for _, name := range tokenNames(template) {
		base := name
		if colon := strings.IndexByte(base, ':'); colon >= 0 {
			base = base[:colon]
		}
		if !musicTemplateTokens[base] {
			return fmt.Errorf("unsupported token {%s}", name)
		}
		if !fileLevel && (base == "track" || base == "disc" || base == "title" || base == "trackArtist") {
			return fmt.Errorf("the folder template cannot use {%s}", base)
		}
	}
	return nil
}

func yearFromDate(value string) int {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 1000 || year > 3000 {
		return 0
	}
	return year
}
