package migration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

type Media string

const (
	MediaMovies Media = "movies"
	MediaTV     Media = "tv"
	MediaMusic  Media = "music"
)

// Item kinds describe one unit of work in an apply run.
const (
	itemRoot             = "root"
	itemProfile          = "profile"
	itemMusicProfile     = "musicProfile"
	itemNaming           = "naming"
	itemMovie            = "movie"
	itemSeries           = "series"
	itemSeason           = "season"
	itemMovieFile        = "movieFile"
	itemEpisodeFile      = "episodeFile"
	itemArtist           = "artist"
	itemAlbum            = "album"
	itemIndexer          = "indexer"
	itemUsenet           = "usenet"
	itemTorznab          = "torznab"
	itemJellyfin         = "jellyfin"
	itemSubtitleConfig   = "subtitleConfig"
	itemSubtitleProfile  = "subtitleProfile"
	itemSubtitleProvider = "subtitleProvider"
	itemTorrentSettings  = "torrentSettings"
	itemTorrentJob       = "torrentJob"
)

const (
	statusReady    = "ready"
	statusApplying = "applying"
	statusApplied  = "applied"
	statusPartial  = "partial"

	itemPending = "pending"
	itemDone    = "done"
	itemFailed  = "failed"
	itemSkipped = "skipped"

	maxPlanItems = 100000
)

// snapshot is the sanitized result of one preview pass over the source applications.
type snapshot struct {
	Versions      map[App]string
	Roots         []RootPlan
	Profiles      []ProfilePlan
	Movies        []MoviePlan
	Series        []SeriesPlan
	Naming        []NamingPlan
	Indexers      []IndexerPlan
	UsenetSources []UsenetPlan
	Torznab       []TorznabPlan
	Music         *musicSnapshot
	Jellyfin      *JellyfinPlan
	Subtitles     *SubtitlePlan
	Torrents      *TorrentPlan
	Warnings      []string
	Unsupported   []Unsupported
}

// musicSnapshot carries the Lidarr discovery for the music module.
type musicSnapshot struct {
	Roots    []RootPlan
	Profiles []MusicProfilePlan
	Naming   []NamingPlan
	Artists  []MusicArtistPlan
}

type Unsupported struct {
	Area   string `json:"area"`
	Detail string `json:"detail"`
}

type SourcePlan struct {
	App     App    `json:"app"`
	Version string `json:"version"`
}

type RootPlan struct {
	Source     App    `json:"source"`
	Media      Media  `json:"media"`
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
	Suggested  string `json:"suggestedLocalPath,omitempty"`
}

type ProfilePlan struct {
	Source      App      `json:"source"`
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Qualities   []string `json:"qualities"`
	Cutoff      string   `json:"cutoff"`
	Upgrade     bool     `json:"upgrade"`
	MinMB       float64  `json:"minMb"`
	MaxMB       float64  `json:"maxMb"`
	Language    string   `json:"language"`
	Suggested   string   `json:"suggestedProfileId,omitempty"`
	Unsupported []string `json:"unsupported,omitempty"`
}

type MoviePlan struct {
	Source     App      `json:"source"`
	ID         int      `json:"id"`
	Title      string   `json:"title"`
	Year       int      `json:"year"`
	IMDbID     string   `json:"imdbId"`
	Monitored  bool     `json:"monitored"`
	Tags       []string `json:"tags"`
	ProfileKey string   `json:"profileKey"`
	RootPath   string   `json:"rootPath"`
	FilePath   string   `json:"filePath,omitempty"`
	FileSize   int64    `json:"fileSize,omitempty"`
	Poster     string   `json:"poster,omitempty"`
	Plot       string   `json:"plot,omitempty"`
}

type EpisodeFilePlan struct {
	Key          string `json:"key"`
	RelativePath string `json:"relativePath"`
	Season       int    `json:"season"`
	Numbers      []int  `json:"numbers"`
	Size         int64  `json:"size"`
	Unsupported  string `json:"unsupported,omitempty"`
}

type SeriesPlan struct {
	Source             App               `json:"source"`
	ID                 int               `json:"id"`
	Title              string            `json:"title"`
	Year               int               `json:"year"`
	IMDbID             string            `json:"imdbId"`
	TVDBID             int               `json:"tvdbId"`
	Monitored          bool              `json:"monitored"`
	MonitorMode        string            `json:"monitorMode"`
	Tags               []string          `json:"tags"`
	ProfileKey         string            `json:"profileKey"`
	RootPath           string            `json:"rootPath"`
	UnmonitoredSeasons []int             `json:"unmonitoredSeasons,omitempty"`
	Files              []EpisodeFilePlan `json:"files,omitempty"`
	Poster             string            `json:"poster,omitempty"`
	Plot               string            `json:"plot,omitempty"`
}

type NamingPlan struct {
	Source      App      `json:"source"`
	Media       Media    `json:"media"`
	Folder      string   `json:"folder"`
	File        string   `json:"file"`
	Applicable  bool     `json:"applicable"`
	Unsupported []string `json:"unsupported,omitempty"`
}

// IndexerPlan is one NZB indexer candidate; the apply request selects exactly one.
type IndexerPlan struct {
	Key       string `json:"key"`
	Source    App    `json:"source"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	APIKeySet bool   `json:"apiKeySet"`
}

// UsenetPlan is one news server candidate; the apply request selects a primary and optional fallbacks.
type UsenetPlan struct {
	Key         string   `json:"key"`
	Source      App      `json:"source"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	UsernameSet bool     `json:"usernameSet"`
	Connections int      `json:"connections"`
	TLS         bool     `json:"tls"`
	Notes       []string `json:"notes,omitempty"`
}

// TorznabPlan is one Prowlarr torrent indexer that the torrent module can search directly.
type TorznabPlan struct {
	Key       string `json:"key"`
	Source    App    `json:"source"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Protocol  string `json:"protocol"`
	APIKeySet bool   `json:"apiKeySet"`
}

type MusicProfilePlan struct {
	Source         App      `json:"source"`
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	Formats        []string `json:"formats"`
	LosslessOnly   bool     `json:"losslessOnly"`
	MinBitrateKbps int      `json:"minBitrateKbps"`
	Cutoff         string   `json:"cutoff"`
	Upgrade        bool     `json:"upgrade"`
	Suggested      string   `json:"suggestedProfileId,omitempty"`
	Unsupported    []string `json:"unsupported,omitempty"`
}

type MusicAlbumPlan struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	MusicBrainzID string `json:"musicBrainzId"`
	Year          int    `json:"year"`
	Type          string `json:"type"`
	Monitored     bool   `json:"monitored"`
	Unsupported   string `json:"unsupported,omitempty"`
}

type MusicArtistPlan struct {
	ID            int              `json:"id"`
	Name          string           `json:"name"`
	SortName      string           `json:"sortName,omitempty"`
	MusicBrainzID string           `json:"musicBrainzId"`
	Monitored     bool             `json:"monitored"`
	MonitorOption string           `json:"monitorOption"`
	Type          string           `json:"type,omitempty"`
	Tags          []string         `json:"tags,omitempty"`
	ProfileKey    string           `json:"profileKey"`
	RootPath      string           `json:"rootPath"`
	Albums        []MusicAlbumPlan `json:"albums,omitempty"`
	Unsupported   string           `json:"unsupported,omitempty"`
}

// MusicPlan is stored with the plan so apply never re-reads Lidarr.
type MusicPlan struct {
	Roots    []RootPlan         `json:"roots"`
	Profiles []MusicProfilePlan `json:"profiles"`
	Naming   []NamingPlan       `json:"naming"`
	Artists  []MusicArtistPlan  `json:"artists"`
}

type JellyfinPlan struct {
	Version   string         `json:"version"`
	Product   string         `json:"product"`
	Libraries map[string]int `json:"libraries"`
	Locations int            `json:"locations"`
}

type SubtitlePlan struct {
	Source            App                    `json:"source"`
	Version           string                 `json:"version"`
	Languages         []string               `json:"languages"`
	LanguageEquals    []string               `json:"languageEquals,omitempty"`
	SingleLanguage    bool                   `json:"singleLanguage"`
	MinimumScore      int                    `json:"minimumScore"`
	MinimumScoreMovie int                    `json:"minimumScoreMovie"`
	UpgradeSubtitles  bool                   `json:"upgradeSubtitles"`
	AdaptiveSearching bool                   `json:"adaptiveSearching"`
	SearchHours       int                    `json:"searchHours"`
	DefaultProfile    string                 `json:"defaultProfile,omitempty"`
	MovieProfile      string                 `json:"movieProfile,omitempty"`
	Profiles          []SubtitleProfilePlan  `json:"profiles"`
	ProviderPlans     []SubtitleProviderPlan `json:"providerPlans"`
	Providers         []string               `json:"providers"`
	PathMappings      []string               `json:"pathMappings,omitempty"`
	Sync              SubtitleSyncPlan       `json:"sync"`
}

type SubtitleProfilePlan struct {
	Key         string                 `json:"key"`
	Name        string                 `json:"name"`
	Languages   []SubtitleLanguagePlan `json:"languages"`
	Cutoff      int                    `json:"cutoff"`
	Unsupported []string               `json:"unsupported,omitempty"`
}

type SubtitleLanguagePlan struct {
	Code   string `json:"code"`
	Forced bool   `json:"forced"`
	HI     bool   `json:"hi"`
}

// SubtitleProviderPlan describes one Bazarr provider account mapped onto a Constellarr provider.
type SubtitleProviderPlan struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Endpoint    string   `json:"endpoint,omitempty"`
	UsernameSet bool     `json:"usernameSet"`
	PasswordSet bool     `json:"passwordSet"`
	Unsupported []string `json:"unsupported,omitempty"`
}

type SubtitleSyncPlan struct {
	Enabled          bool `json:"enabled"`
	Movies           bool `json:"movies"`
	MaxOffsetSeconds int  `json:"maxOffsetSeconds"`
	Threshold        int  `json:"threshold"`
	MovieThreshold   int  `json:"movieThreshold"`
	NoFixFramerate   bool `json:"noFixFramerate"`
	GoldenSection    bool `json:"goldenSection"`
}

type TorrentPlan struct {
	Source             App               `json:"source"`
	Version            string            `json:"version"`
	DownloadDir        string            `json:"downloadDir"`
	IncompleteDir      string            `json:"incompleteDir,omitempty"`
	SpeedLimitDown     int64             `json:"speedLimitDown"`
	SpeedLimitUp       int64             `json:"speedLimitUp"`
	SpeedDownEnabled   bool              `json:"speedDownEnabled"`
	SpeedUpEnabled     bool              `json:"speedUpEnabled"`
	SeedRatioLimit     float64           `json:"seedRatioLimit"`
	SeedRatioLimited   bool              `json:"seedRatioLimited"`
	IdleSeedingMinutes int               `json:"idleSeedingMinutes"`
	IdleSeedingLimited bool              `json:"idleSeedingLimited"`
	DHTEnabled         bool              `json:"dhtEnabled"`
	PEXEnabled         bool              `json:"pexEnabled"`
	DownloadQueueSize  int               `json:"downloadQueueSize"`
	Torrents           int               `json:"torrents"`
	Labels             []string          `json:"labels,omitempty"`
	Transfers          []TorrentTransfer `json:"transfers,omitempty"`
}

type TorrentTransfer struct {
	Name         string            `json:"name"`
	Hash         string            `json:"hash"`
	MagnetLink   string            `json:"magnetLink,omitempty"`
	Status       int               `json:"status"`
	PercentDone  float64           `json:"percentDone"`
	DownloadDir  string            `json:"downloadDir"`
	Labels       []string          `json:"labels,omitempty"`
	BytesDone    int64             `json:"bytesDone"`
	TotalSize    int64             `json:"totalSize"`
	TrackerHosts []string          `json:"trackerHosts,omitempty"`
	Files        []TorrentFilePlan `json:"files,omitempty"`
	UploadRatio  float64           `json:"uploadRatio"`
	AddedAt      time.Time         `json:"addedAt"`
	Finished     bool              `json:"finished"`
}

type TorrentFilePlan struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Done int64  `json:"done"`
}

type Counts struct {
	Movies     int `json:"movies"`
	Series     int `json:"series"`
	Seasons    int `json:"seasons"`
	Files      int `json:"files"`
	Artists    int `json:"artists"`
	Albums     int `json:"albums"`
	Profiles   int `json:"profiles"`
	Roots      int `json:"roots"`
	Indexers   int `json:"indexers"`
	NewsServer int `json:"newsServers"`
	Torrents   int `json:"torrents"`
	Languages  int `json:"languages"`
}

// Plan pins the fetched source data; credentials are never part of it.
type Plan struct {
	ID            string        `json:"id"`
	Status        string        `json:"status"`
	CreatedAt     time.Time     `json:"createdAt"`
	ExpiresAt     time.Time     `json:"expiresAt"`
	Sources       []SourcePlan  `json:"sources"`
	Counts        Counts        `json:"counts"`
	Roots         []RootPlan    `json:"roots"`
	Profiles      []ProfilePlan `json:"profiles"`
	Naming        []NamingPlan  `json:"naming"`
	Indexers      []IndexerPlan `json:"indexers"`
	UsenetSources []UsenetPlan  `json:"usenetSources"`
	Torznab       []TorznabPlan `json:"torznab"`
	Music         *MusicPlan    `json:"music,omitempty"`
	Jellyfin      *JellyfinPlan `json:"jellyfin,omitempty"`
	Subtitles     *SubtitlePlan `json:"subtitles,omitempty"`
	Torrents      *TorrentPlan  `json:"torrents,omitempty"`
	Warnings      []string      `json:"warnings"`
	Unsupported   []Unsupported `json:"unsupported"`
	Movies        []MoviePlan   `json:"movies"`
	Series        []SeriesPlan  `json:"series"`
}

type PlanItem struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

type ItemTotals struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// PlanView is the client-facing plan with progress instead of the full catalog lists.
type PlanView struct {
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	CreatedAt     time.Time      `json:"createdAt"`
	ExpiresAt     time.Time      `json:"expiresAt"`
	Sources       []SourcePlan   `json:"sources"`
	Counts        Counts         `json:"counts"`
	Roots         []RootPlan     `json:"roots"`
	Profiles      []ProfilePlan  `json:"profiles"`
	Naming        []NamingPlan   `json:"naming"`
	Indexers      []IndexerPlan  `json:"indexers"`
	UsenetSources []UsenetPlan   `json:"usenetSources"`
	Torznab       []TorznabPlan  `json:"torznab"`
	Music         *MusicPlanView `json:"music,omitempty"`
	Jellyfin      *JellyfinPlan  `json:"jellyfin,omitempty"`
	Subtitles     *SubtitlePlan  `json:"subtitles,omitempty"`
	Torrents      *TorrentPlan   `json:"torrents,omitempty"`
	Warnings      []string       `json:"warnings"`
	Unsupported   []Unsupported  `json:"unsupported"`
	Totals        ItemTotals     `json:"totals"`
	Failures      []PlanItem     `json:"failures"`
}

// MusicPlanView is the client-facing music plan without the full artist list.
type MusicPlanView struct {
	Roots    []RootPlan         `json:"roots"`
	Profiles []MusicProfilePlan `json:"profiles"`
	Naming   []NamingPlan       `json:"naming"`
	Counts   Counts             `json:"counts"`
}

// Preview inspects the connections and stores a short-lived plan with per-item markers.
func (s *Service) Preview(ctx context.Context, connections []Connection) (PlanView, error) {
	normalized, err := validateConnections(connections)
	if err != nil {
		return PlanView{}, err
	}
	found, secrets, err := s.discoverAll(ctx, normalized)
	if err != nil {
		return PlanView{}, err
	}
	plan, err := s.buildPlan(ctx, found, normalized)
	if err != nil {
		return PlanView{}, err
	}
	if err := s.store(ctx, plan, normalized, secrets); err != nil {
		return PlanView{}, err
	}
	return s.view(ctx, plan)
}

func (s *Service) buildPlan(ctx context.Context, found snapshot, connections []Connection) (*Plan, error) {
	now := s.now().UTC()
	plan := &Plan{
		ID: rand.Text(), Status: statusReady, CreatedAt: now, ExpiresAt: now.Add(s.ttl),
		Roots: found.Roots, Profiles: found.Profiles, Naming: found.Naming,
		Indexers: found.Indexers, UsenetSources: found.UsenetSources, Torznab: found.Torznab,
		Jellyfin: found.Jellyfin, Subtitles: found.Subtitles, Torrents: found.Torrents,
		Warnings: found.Warnings, Unsupported: found.Unsupported,
	}
	for _, connection := range connections {
		if connection.App == AppJellyfin {
			continue
		}
		plan.Sources = append(plan.Sources, SourcePlan{App: connection.App, Version: found.Versions[connection.App]})
	}
	sort.Slice(plan.Sources, func(i, j int) bool { return plan.Sources[i].App < plan.Sources[j].App })
	plan.Movies = make([]MoviePlan, 0, len(found.Movies))
	for _, movie := range found.Movies {
		if movie.IMDbID == "" {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "movie", Detail: fmt.Sprintf("%s (%d) has no IMDb ID; add it manually after migration", movie.Title, movie.Year)})
			continue
		}
		if movie.RootPath == "" {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "movie", Detail: fmt.Sprintf("%s (%d) has no root folder in %s", movie.Title, movie.Year, movie.Source)})
			continue
		}
		plan.Movies = append(plan.Movies, movie)
	}
	plan.Series = make([]SeriesPlan, 0, len(found.Series))
	for _, series := range found.Series {
		if series.IMDbID == "" {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "series", Detail: fmt.Sprintf("%s (%d) has no IMDb ID; add it manually after migration", series.Title, series.Year)})
			continue
		}
		if series.RootPath == "" {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "series", Detail: fmt.Sprintf("%s (%d) has no root folder in %s", series.Title, series.Year, series.Source)})
			continue
		}
		plan.Series = append(plan.Series, series)
	}
	if found.Music != nil {
		plan.Music = s.buildMusicPlan(found.Music, plan)
	}
	for i := range plan.Roots {
		plan.Roots[i].Suggested = suggestedLocalPath(plan.Roots[i].Path)
	}
	if plan.Music != nil {
		for i := range plan.Music.Roots {
			plan.Music.Roots[i].Suggested = suggestedLocalPath(plan.Music.Roots[i].Path)
		}
	}
	if s.movies != nil {
		if profiles, err := s.movies.Store.Profiles(ctx); err == nil {
			for i := range plan.Profiles {
				plan.Profiles[i].Suggested = suggestProfile(profiles, plan.Profiles[i])
				for _, unsupported := range plan.Profiles[i].Unsupported {
					plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "profile", Detail: fmt.Sprintf("%s profile %q quality %q has no Constellarr equivalent", plan.Profiles[i].Source, plan.Profiles[i].Name, unsupported)})
				}
			}
		}
	}
	if plan.Music != nil && s.music != nil {
		if cfg, err := s.music.ConfigView(ctx); err == nil {
			for i := range plan.Music.Profiles {
				plan.Music.Profiles[i].Suggested = suggestMusicProfile(cfg.QualityProfiles, plan.Music.Profiles[i])
				for _, unsupported := range plan.Music.Profiles[i].Unsupported {
					plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "music profile", Detail: fmt.Sprintf("Lidarr profile %q quality %q has no Constellarr equivalent", plan.Music.Profiles[i].Name, unsupported)})
				}
			}
		}
	}
	plan.Counts = planCounts(plan, found)
	if len(plan.Indexers) > 1 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d NZB indexers are available; select one in the wizard", len(plan.Indexers)))
	}
	if len(plan.UsenetSources) > 1 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("%d news servers are available; select a primary and compatible fallbacks in the wizard", len(plan.UsenetSources)))
	}
	plan.Unsupported = append(plan.Unsupported,
		Unsupported{Area: "general", Detail: "download history, queue state, and manual import records are not migrated"},
		Unsupported{Area: "general", Detail: "source applications stay unchanged and keep working alongside Constellarr"},
	)
	if plan.Subtitles != nil {
		if plan.Subtitles.MinimumScoreMovie > 0 && plan.Subtitles.MinimumScoreMovie != plan.Subtitles.MinimumScore {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "subtitles", Detail: fmt.Sprintf("Bazarr's movie-only minimum score (%d) is not represented separately; the series score is applied", plan.Subtitles.MinimumScoreMovie)})
		}
		if plan.Subtitles.UpgradeSubtitles {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "subtitles", Detail: "Bazarr's subtitle upgrade schedule is not imported"})
		}
		if plan.Subtitles.Sync.Threshold > 0 || plan.Subtitles.Sync.MovieThreshold > 0 {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "subtitles", Detail: "Bazarr's alignment quality threshold uses a different scale and is not imported"})
		}
		if !plan.Subtitles.Sync.Enabled {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "subtitles", Detail: "Bazarr reports automatic syncing disabled; only the offset limit is imported"})
		}
	}
	if plan.Torrents != nil {
		plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "torrents", Detail: "private tracker passkeys are not exportable through Transmission's RPC; imported jobs rely on the magnet link"})
	}
	if err := validatePlanItems(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// buildMusicPlan keeps only importable artists while reporting every skipped entry.
func (s *Service) buildMusicPlan(found *musicSnapshot, plan *Plan) *MusicPlan {
	music := &MusicPlan{Roots: found.Roots, Profiles: found.Profiles, Naming: found.Naming}
	tagged := 0
	for _, artist := range found.Artists {
		if artist.Unsupported != "" {
			plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "artist", Detail: fmt.Sprintf("%s: %s", artist.Name, artist.Unsupported)})
			continue
		}
		importable := artist
		importable.Albums = nil
		for _, album := range artist.Albums {
			if album.Unsupported != "" {
				plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "album", Detail: fmt.Sprintf("%s - %s: %s", artist.Name, album.Title, album.Unsupported)})
				continue
			}
			importable.Albums = append(importable.Albums, album)
		}
		if len(artist.Tags) > 0 {
			tagged++
		}
		music.Artists = append(music.Artists, importable)
	}
	if tagged > 0 {
		plan.Unsupported = append(plan.Unsupported, Unsupported{Area: "music", Detail: fmt.Sprintf("Lidarr artist tags are not imported yet (%d artists carry tags)", tagged)})
	}
	return music
}

func planCounts(plan *Plan, found snapshot) Counts {
	counts := Counts{
		Movies: len(plan.Movies), Series: len(plan.Series),
		Profiles: len(plan.Profiles), Roots: len(plan.Roots),
		Indexers: len(plan.Indexers) + len(plan.Torznab), NewsServer: len(plan.UsenetSources),
	}
	for _, series := range plan.Series {
		counts.Seasons += len(series.UnmonitoredSeasons)
		for _, file := range series.Files {
			if file.Unsupported == "" && file.RelativePath != "" {
				counts.Files++
			}
		}
	}
	for _, movie := range plan.Movies {
		if movie.FilePath != "" {
			counts.Files++
		}
	}
	if plan.Music != nil {
		counts.Artists = len(plan.Music.Artists)
		counts.Profiles += len(plan.Music.Profiles)
		counts.Roots += len(plan.Music.Roots)
		for _, artist := range plan.Music.Artists {
			counts.Albums += len(artist.Albums)
		}
	}
	if found.Torrents != nil {
		counts.Torrents = found.Torrents.Torrents
	}
	if found.Subtitles != nil {
		counts.Languages = len(found.Subtitles.Languages)
	}
	return counts
}

// suggestMusicProfile only offers a profile that decides releases exactly like the source.
func suggestMusicProfile(profiles []music.QualityProfile, source MusicProfilePlan) string {
	if len(source.Formats) == 0 {
		return ""
	}
	for _, profile := range profiles {
		if musicProfileEquivalent(profile, source) {
			return profile.ID
		}
	}
	return ""
}

// musicProfileEquivalent compares every setting the music module scores releases with.
func musicProfileEquivalent(profile music.QualityProfile, source MusicProfilePlan) bool {
	if !sameQualityOrder(profile.Formats, source.Formats) ||
		profile.LosslessOnly != source.LosslessOnly ||
		profile.MinBitrateKbps != source.MinBitrateKbps ||
		profile.Upgrade != source.Upgrade ||
		!sameText(profile.Cutoff, source.Cutoff) {
		return false
	}
	// Existing size limits narrow decisions, so they are never mapped automatically.
	return profile.MinMB == 0 && profile.MaxMB == 0
}

func suggestedLocalPath(sourcePath string) string {
	if localDirectory(sourcePath) == nil {
		return sourcePath
	}
	return ""
}

// suggestProfile only offers a profile that decides releases exactly like the source.
func suggestProfile(profiles []quality.Profile, source ProfilePlan) string {
	if len(source.Qualities) == 0 {
		return ""
	}
	for _, profile := range profiles {
		if videoProfileEquivalent(profile, source) {
			return profile.ID
		}
	}
	return ""
}

// videoProfileEquivalent compares every setting the video modules score releases with.
func videoProfileEquivalent(profile quality.Profile, source ProfilePlan) bool {
	if !sameQualityOrder(profile.Qualities, source.Qualities) ||
		profile.Upgrade != source.Upgrade ||
		profile.MinMB != source.MinMB || profile.MaxMB != source.MaxMB ||
		!sameText(profile.Cutoff, source.Cutoff) ||
		!sameText(profile.Language, source.Language) {
		return false
	}
	// Existing rules and score thresholds change decisions, so they are never mapped automatically.
	return len(profile.Rules) == 0 && profile.MinScore == 0 && profile.CutoffScore == 0
}

func sameText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// sameQualityOrder compares preference order, because both modules rank a profile by its listed order.
func sameQualityOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameText(a[i], b[i]) {
			return false
		}
	}
	return true
}

// planItems lists work in dependency order: roots and profiles before catalog entries, files last.
func planItems(plan *Plan) []PlanItem {
	var items []PlanItem
	add := func(kind, target, label string) {
		items = append(items, PlanItem{Position: len(items), Kind: kind, Target: target, Label: label, Status: itemPending})
	}
	seenRoots := map[string]bool{}
	for _, root := range plan.Roots {
		key := fmt.Sprintf("%s|%s", root.Media, root.Path)
		if seenRoots[key] {
			continue
		}
		seenRoots[key] = true
		add(itemRoot, key, fmt.Sprintf("%s root %s", mediaLabel(root.Media), root.Path))
	}
	for _, profile := range plan.Profiles {
		add(itemProfile, profile.Key, fmt.Sprintf("%s profile %s", profile.Source, profile.Name))
	}
	if plan.Music != nil {
		seenRoots := map[string]bool{}
		for _, root := range plan.Music.Roots {
			key := fmt.Sprintf("%s|%s", root.Media, root.Path)
			if seenRoots[key] {
				continue
			}
			seenRoots[key] = true
			add(itemRoot, key, fmt.Sprintf("%s root %s", mediaLabel(root.Media), root.Path))
		}
		for _, profile := range plan.Music.Profiles {
			add(itemMusicProfile, profile.Key, fmt.Sprintf("%s profile %s", profile.Source, profile.Name))
		}
		for _, naming := range plan.Music.Naming {
			add(itemNaming, string(naming.Media), fmt.Sprintf("%s naming", mediaLabel(naming.Media)))
		}
	}
	for _, naming := range plan.Naming {
		add(itemNaming, string(naming.Media), fmt.Sprintf("%s naming", mediaLabel(naming.Media)))
	}
	for _, movie := range plan.Movies {
		add(itemMovie, fmt.Sprintf("%s:%d", movie.Source, movie.ID), fmt.Sprintf("%s (%d)", movie.Title, movie.Year))
	}
	for _, series := range plan.Series {
		add(itemSeries, fmt.Sprintf("%s:%d", series.Source, series.ID), fmt.Sprintf("%s (%d)", series.Title, series.Year))
		for _, season := range series.UnmonitoredSeasons {
			add(itemSeason, fmt.Sprintf("%s:%d:%d", series.Source, series.ID, season), fmt.Sprintf("%s season %d (unmonitored)", series.Title, season))
		}
	}
	for _, movie := range plan.Movies {
		if movie.FilePath != "" {
			add(itemMovieFile, fmt.Sprintf("%s:%d", movie.Source, movie.ID), fmt.Sprintf("File for %s (%d)", movie.Title, movie.Year))
		}
	}
	for _, series := range plan.Series {
		for _, file := range series.Files {
			if file.Unsupported != "" || file.RelativePath == "" {
				continue
			}
			add(itemEpisodeFile, fmt.Sprintf("%s:%s", series.Source, file.Key), fmt.Sprintf("File %s", file.RelativePath))
		}
	}
	if plan.Music != nil {
		for _, artist := range plan.Music.Artists {
			add(itemArtist, strconv.Itoa(artist.ID), fmt.Sprintf("%s (%d albums)", artist.Name, len(artist.Albums)))
			for _, album := range artist.Albums {
				add(itemAlbum, fmt.Sprintf("%d:%d", artist.ID, album.ID), fmt.Sprintf("%s - %s", artist.Name, album.Title))
			}
		}
	}
	for _, candidate := range plan.Indexers {
		add(itemIndexer, candidate.Key, fmt.Sprintf("%s NZB indexer %s", candidate.Source, candidate.Name))
	}
	for _, candidate := range plan.UsenetSources {
		add(itemUsenet, candidate.Key, fmt.Sprintf("%s news server %s:%d", candidate.Source, candidate.Host, candidate.Port))
	}
	for _, candidate := range plan.Torznab {
		add(itemTorznab, candidate.Key, fmt.Sprintf("Torznab source %s", candidate.Name))
	}
	if plan.Jellyfin != nil {
		add(itemJellyfin, "connection", "Jellyfin connection")
	}
	if plan.Subtitles != nil {
		add(itemSubtitleConfig, "config", "Subtitle scoring and sync settings")
		for _, profile := range plan.Subtitles.Profiles {
			add(itemSubtitleProfile, profile.Key, fmt.Sprintf("Subtitle languages %s", profile.Name))
		}
		for _, provider := range plan.Subtitles.ProviderPlans {
			add(itemSubtitleProvider, provider.Key, fmt.Sprintf("Subtitle provider %s", provider.Name))
		}
	}
	if plan.Torrents != nil {
		add(itemTorrentSettings, "settings", "Torrent speed and seeding settings")
		for _, transfer := range plan.Torrents.Transfers {
			add(itemTorrentJob, transfer.Hash, fmt.Sprintf("Torrent %s (%.0f%%)", transfer.Name, transfer.PercentDone*100))
		}
	}
	return items
}

func validatePlanItems(plan *Plan) error {
	if len(planItems(plan)) > maxPlanItems {
		return fmt.Errorf("%w: this catalog is larger than the supported %d migration items", ErrInvalid, maxPlanItems)
	}
	return nil
}

func mediaLabel(media Media) string {
	switch media {
	case MediaTV:
		return "TV"
	case MediaMusic:
		return "music"
	default:
		return "movies"
	}
}

func (s *Service) store(ctx context.Context, plan *Plan, connections []Connection, secrets providerSecrets) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return errors.New("migration: the plan could not be stored")
	}
	items := planItems(plan)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errors.New("migration: the plan could not be stored")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO migration_plans (id, data, status, expires_at, created_at, updated_at) VALUES ($1, $2::jsonb, $3, $4, $5, $5)`,
		plan.ID, string(data), plan.Status, plan.ExpiresAt, plan.CreatedAt); err != nil {
		return errors.New("migration: the plan could not be stored")
	}
	if len(items) > 0 {
		rows := pgx.CopyFromSlice(len(items), func(i int) ([]any, error) {
			item := items[i]
			return []any{plan.ID, item.Position, item.Kind, item.Target, item.Label, item.Status, item.Message}, nil
		})
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"migration_plan_items"},
			[]string{"plan_id", "position", "kind", "target", "label", "status", "message"}, rows); err != nil {
			return errors.New("migration: the plan could not be stored")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("migration: the plan could not be stored")
	}
	s.secrets.put(plan.ID, secretEntry{connections: connections, providers: secrets, expires: plan.ExpiresAt})
	return nil
}

func (s *Service) load(ctx context.Context, id string) (*Plan, error) {
	if id == "" || len(id) > 128 {
		return nil, ErrNotFound
	}
	var data []byte
	var status string
	var expires time.Time
	if err := s.pool.QueryRow(ctx, `SELECT data, status, expires_at FROM migration_plans WHERE id = $1`, id).Scan(&data, &status, &expires); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, errors.New("migration: the plan could not be read")
	}
	if !expires.After(s.now()) {
		return nil, ErrExpired
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, errors.New("migration: the stored plan is invalid")
	}
	plan.Status = status
	plan.ExpiresAt = expires
	return &plan, nil
}

func (s *Service) view(ctx context.Context, plan *Plan) (PlanView, error) {
	totals, err := s.totals(ctx, plan.ID)
	if err != nil {
		return PlanView{}, err
	}
	failures, err := s.items(ctx, plan.ID, itemFailed, 200)
	if err != nil {
		return PlanView{}, err
	}
	view := PlanView{
		ID: plan.ID, Status: plan.Status, CreatedAt: plan.CreatedAt, ExpiresAt: plan.ExpiresAt,
		Sources: plan.Sources, Counts: plan.Counts, Roots: plan.Roots, Profiles: plan.Profiles,
		Naming: plan.Naming, Indexers: plan.Indexers, UsenetSources: plan.UsenetSources, Torznab: plan.Torznab,
		Jellyfin: plan.Jellyfin, Subtitles: plan.Subtitles, Torrents: plan.Torrents,
		Warnings: plan.Warnings, Unsupported: plan.Unsupported, Totals: totals, Failures: failures,
	}
	if plan.Music != nil {
		view.Music = &MusicPlanView{
			Roots: plan.Music.Roots, Profiles: plan.Music.Profiles, Naming: plan.Music.Naming,
			Counts: Counts{Artists: len(plan.Music.Artists), Profiles: len(plan.Music.Profiles), Roots: len(plan.Music.Roots)},
		}
		for _, artist := range plan.Music.Artists {
			view.Music.Counts.Albums += len(artist.Albums)
		}
	}
	return view, nil
}

func (s *Service) totals(ctx context.Context, planID string) (ItemTotals, error) {
	var totals ItemTotals
	err := s.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE status = $2), count(*) FILTER (WHERE status = $3),
		        count(*) FILTER (WHERE status = $4), count(*) FILTER (WHERE status = $5)
		 FROM migration_plan_items WHERE plan_id = $1`,
		planID, itemPending, itemDone, itemFailed, itemSkipped).Scan(
		&totals.Total, &totals.Pending, &totals.Done, &totals.Failed, &totals.Skipped)
	if err != nil {
		return ItemTotals{}, errors.New("migration: the plan progress could not be read")
	}
	return totals, nil
}

func (s *Service) items(ctx context.Context, planID, status string, limit int) ([]PlanItem, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT position, kind, target, label, status, message FROM migration_plan_items
			 WHERE plan_id = $1 ORDER BY position LIMIT $2`, planID, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT position, kind, target, label, status, message FROM migration_plan_items
			 WHERE plan_id = $1 AND status = $2 ORDER BY position LIMIT $3`, planID, status, limit)
	}
	if err != nil {
		return nil, errors.New("migration: the plan items could not be read")
	}
	defer rows.Close()
	items := []PlanItem{}
	for rows.Next() {
		var item PlanItem
		if err := rows.Scan(&item.Position, &item.Kind, &item.Target, &item.Label, &item.Status, &item.Message); err != nil {
			return nil, errors.New("migration: the plan items could not be read")
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, errors.New("migration: the plan items could not be read")
	}
	return items, nil
}

// movieFileTarget and friends decode item targets back into plan lookups.
func splitTarget(target string, parts int) []string {
	segments := strings.SplitN(target, ":", parts)
	return segments
}

func cleanRelativePath(rel string) string {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}
