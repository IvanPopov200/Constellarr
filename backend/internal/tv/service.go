package tv

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

const (
	tvSeriesLockBase  int64 = 0x5456536572696573
	tvBulkIDLimit           = 500
	tvMetadataQuery   int   = 256
	tvMetadataPage    int   = 100
	tvSeasonsPerCall        = 5
	tvSeasonRefresh         = 24 * time.Hour
	tvSeasonMissRetry       = 24 * time.Hour
	tvEpisodesLoading       = "Episode metadata is still loading"
)

func New(ctx context.Context, pool *pgxpool.Pool, manager *downloads.Manager, shared *movies.Store) (*Service, error) {
	if pool == nil {
		return nil, errors.New("tv: a PostgreSQL pool is required")
	}
	if manager == nil {
		return nil, errors.New("tv: the download manager is required")
	}
	if shared == nil {
		return nil, errors.New("tv: the movie store with shared settings is required")
	}
	defaults := defaultConfig(manager.Config().Directory)
	store, err := NewStore(ctx, pool, defaults)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(defaults.RootFolders[0].Path, 0o755); err != nil {
		return nil, errors.New("tv: the default library root could not be created")
	}
	return &Service{
		Store: store, Shared: shared, Downloads: manager, pool: pool,
		lockSlots: shared.OperationSlots(),
	}, nil
}

func defaultConfig(directory string) Config {
	return Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: filepath.Join(directory, "library", "tv")}},
		FolderTemplate: "{title} ({year}) [imdb-{imdbId}]/Season {season}",
		FileTemplate:   "{title} - {episodeCode} - {episodeTitle} [{quality}]",
		ImportMode:     library.ModeLink,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	}
}

func (s *Service) Config(ctx context.Context) (Config, error) {
	return s.Store.Config(ctx)
}

func (s *Service) SetConfig(ctx context.Context, input Config) (Config, error) {
	input = normalizeConfig(input)
	if err := validateConfig(input); err != nil {
		return Config{}, err
	}
	current, err := s.Store.Config(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Config{}, err
	}
	if err := s.tvCheckRootOwnership(ctx, current, input); err != nil {
		return Config{}, err
	}
	if err := tvEnsureRoots(input.RootFolders); err != nil {
		return Config{}, err
	}
	return s.Store.SaveConfig(ctx, input)
}

// tvCheckRootOwnership rejects path changes while a root still holds series or imported files.
func (s *Service) tvCheckRootOwnership(ctx context.Context, current, next Config) error {
	kept := make(map[string]string, len(next.RootFolders))
	for _, root := range next.RootFolders {
		kept[root.ID] = root.Path
	}
	for _, root := range current.RootFolders {
		if path, ok := kept[root.ID]; ok && path == root.Path {
			continue
		}
		var used bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tv_series WHERE data->>'rootId' = $1)
			     OR EXISTS (SELECT 1 FROM tv_episodes
			                WHERE data->'files' @> jsonb_build_array(jsonb_build_object('rootId', $1::text)))
			     OR EXISTS (SELECT 1 FROM download_library_files WHERE ready AND root_path = $2)`,
			root.ID, root.Path).Scan(&used); err != nil {
			return errors.New("tv: root folder usage could not be checked")
		}
		if used {
			return fmt.Errorf("%w: root folder %q is used by a series or episode file", ErrConflict, root.ID)
		}
	}
	return nil
}

func tvEnsureRoots(roots []movies.RootFolder) error {
	for _, root := range roots {
		path := strings.TrimSpace(root.Path)
		if path == "" {
			continue
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("%w: root folder could not be created", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) List(ctx context.Context) ([]Series, error) {
	series, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	return s.tvDecorateList(ctx, series)
}

func (s *Service) Get(ctx context.Context, id string) (Series, error) {
	series, err := s.Store.Get(ctx, id)
	if err != nil {
		return Series{}, err
	}
	return s.tvDecorateOne(ctx, series)
}

func (s *Service) tvDecorateList(ctx context.Context, series []Series) ([]Series, error) {
	cfg, profiles, acquisitions, err := s.tvDecorationInputs(ctx)
	if err != nil {
		return nil, err
	}
	episodes, err := s.Store.Episodes(ctx, "")
	if err != nil {
		return nil, err
	}
	bySeries := map[string][]Episode{}
	for _, episode := range episodes {
		bySeries[episode.SeriesID] = append(bySeries[episode.SeriesID], episode)
	}
	byAcquisition := map[string][]Acquisition{}
	for _, acquisition := range acquisitions {
		byAcquisition[acquisition.SeriesID] = append(byAcquisition[acquisition.SeriesID], acquisition)
	}
	for i := range series {
		profile := profiles[series[i].ProfileID]
		decorated := tvDecorateEpisodes(profile, series[i], bySeries[series[i].ID], cfg.RootFolders, byAcquisition[series[i].ID])
		series[i] = tvSummarize(series[i], decorated, profile)
		series[i].Episodes = nil
	}
	return series, nil
}

func (s *Service) tvDecorateOne(ctx context.Context, series Series) (Series, error) {
	cfg, profiles, acquisitions, err := s.tvDecorationInputs(ctx)
	if err != nil {
		return Series{}, err
	}
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return Series{}, err
	}
	seriesAcquisitions := make([]Acquisition, 0, 4)
	for _, acquisition := range acquisitions {
		if acquisition.SeriesID == series.ID {
			seriesAcquisitions = append(seriesAcquisitions, acquisition)
		}
	}
	decorated := tvDecorateEpisodes(profiles[series.ProfileID], series, episodes, cfg.RootFolders, seriesAcquisitions)
	series.Episodes = decorated
	return tvSummarize(series, decorated, profiles[series.ProfileID]), nil
}

func (s *Service) tvDecorationInputs(ctx context.Context) (Config, map[string]quality.Profile, []Acquisition, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Config{}, nil, nil, err
	}
	profiles, err := s.Shared.Profiles(ctx)
	if err != nil {
		return Config{}, nil, nil, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return Config{}, nil, nil, err
	}
	byProfile := make(map[string]quality.Profile, len(profiles))
	for _, profile := range profiles {
		byProfile[profile.ID] = profile
	}
	return cfg, byProfile, acquisitions, nil
}

func tvDecorateEpisodes(profile quality.Profile, series Series, episodes []Episode, roots []movies.RootFolder, acquisitions []Acquisition) []Episode {
	decorated := make([]Episode, 0, len(episodes))
	for _, episode := range episodes {
		decorated = append(decorated, decorateEpisode(profile, series, episode, roots, acquisitions))
	}
	return decorated
}

// decorateEpisode annotates one episode with file presence, acquisition state, and wanted status.
func decorateEpisode(profile quality.Profile, series Series, episode Episode, roots []movies.RootFolder, acquisitions []Acquisition) Episode {
	files, present := tvAnnotateFiles(roots, episode.Files)
	episode.Files = files
	episode.Status = ""
	if acquisition := tvEpisodeAcquisition(episode.ID, acquisitions); acquisition != nil {
		switch acquisition.Status {
		case "queued", "downloading", "":
			episode.Status = "downloading"
			episode.Error = ""
		case "paused", "cancelled":
			episode.Status = acquisition.Status
			episode.Error = ""
		case "importing":
			episode.Status = "importing"
			episode.Error = ""
		case "import-failed":
			episode.Status = "import-failed"
			episode.Error = firstText(acquisition.Error, episode.Error)
		case "failed":
			episode.Status = "failed"
			episode.Error = firstText(acquisition.Error, episode.Error)
		}
	}
	if episode.Status != "" {
		return episode
	}
	switch {
	case len(present) > 0:
		if series.Monitored && episode.Monitored && len(profile.Qualities) > 0 && profile.Upgrade && !quality.Satisfied(profile, *bestCurrent(profile, present)) {
			episode.Status = "cutoff-unmet"
		} else {
			episode.Status = "available"
		}
	case !series.Monitored || !episode.Monitored:
		episode.Status = "unmonitored"
	case tvFutureDate(episode.AirDate):
		episode.Status = "missing"
	default:
		// A missing episode with an unknown air date stays wanted until it is matched or aired.
		episode.Status = "wanted"
	}
	return episode
}

func tvSummarize(series Series, episodes []Episode, profile quality.Profile) Series {
	series.Total = len(episodes)
	series.Downloaded, series.Wanted = 0, 0
	downloading, importing, failed, paused, cancelled := false, false, false, false, false
	cutoffUnmet := false
	monitored := 0
	failure := ""
	for _, episode := range episodes {
		if episode.Monitored {
			monitored++
		}
		if len(tvUsableFiles(episode.Files)) > 0 {
			series.Downloaded++
		}
		if tvEpisodeWanted(series, profile, episode) {
			series.Wanted++
		}
		switch episode.Status {
		case "downloading":
			downloading = true
		case "paused":
			paused = true
		case "cancelled":
			cancelled = true
		case "importing":
			importing = true
		case "failed", "import-failed":
			if !failed {
				failure, failed = episode.Error, true
			}
		case "cutoff-unmet":
			cutoffUnmet = true
		}
	}
	switch {
	case downloading:
		series.Status = "downloading"
	case importing:
		series.Status = "importing"
	case paused:
		series.Status = "paused"
	case cancelled:
		series.Status = "cancelled"
	case failed:
		series.Status = "failed"
		if series.Error == "" {
			series.Error = truncate(failure, maxTextRunes)
		}
	case !series.Monitored || (series.Total > 0 && monitored == 0):
		series.Status = "unmonitored"
	case series.Wanted > 0:
		series.Status = "wanted"
	case series.Downloaded > 0 && cutoffUnmet:
		series.Status = "cutoff-unmet"
	case series.Downloaded > 0:
		series.Status = "available"
	default:
		series.Status = "missing"
	}
	return series
}

func tvEpisodeWanted(series Series, profile quality.Profile, episode Episode) bool {
	if !series.Monitored || !episode.Monitored {
		return false
	}
	switch episode.Status {
	case "downloading", "paused", "cancelled", "importing":
		return false
	}
	if tvFutureDate(episode.AirDate) {
		return false
	}
	files := tvUsableFiles(episode.Files)
	if len(files) == 0 {
		return true
	}
	current := bestCurrent(profile, files)
	if current == nil || quality.Satisfied(profile, *current) {
		return false
	}
	if episode.Status == "failed" || episode.Status == "import-failed" {
		return true
	}
	return profile.Upgrade
}

func tvAnnotateEpisodes(cfg Config, episodes []Episode) []Episode {
	annotated := make([]Episode, len(episodes))
	copy(annotated, episodes)
	for i := range annotated {
		annotated[i].Files, _ = tvAnnotateFiles(cfg.RootFolders, annotated[i].Files)
	}
	return annotated
}

func tvAnnotateFiles(roots []movies.RootFolder, files []movies.File) ([]movies.File, []movies.File) {
	annotated := make([]movies.File, len(files))
	copy(annotated, files)
	present := make([]movies.File, 0, len(files))
	for i := range annotated {
		if !tvFilePresent(roots, annotated[i]) {
			annotated[i].Missing = true
			continue
		}
		annotated[i].Missing = false
		present = append(present, annotated[i])
	}
	return annotated, present
}

func tvFilePresent(roots []movies.RootFolder, file movies.File) bool {
	root, ok := tvRootByID(roots, file.RootID)
	if !ok {
		return false
	}
	handle, err := library.Open(root.Path, file.Path)
	if err != nil {
		return false
	}
	info, err := handle.Stat()
	handle.Close()
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func tvRootByID(roots []movies.RootFolder, id string) (movies.RootFolder, bool) {
	id = strings.TrimSpace(id)
	for _, root := range roots {
		if root.ID == id {
			return root, true
		}
	}
	return movies.RootFolder{}, false
}

// rootFor resolves a configured root folder by ID for imports and file ownership checks.
func rootFor(cfg Config, id string) (movies.RootFolder, bool) {
	return tvRootByID(cfg.RootFolders, id)
}

func bestCurrent(profile quality.Profile, files []movies.File) *quality.Current {
	var best *quality.Current
	rank := func(id string) int {
		if index := slices.Index(profile.Qualities, id); index >= 0 {
			return index
		}
		if index := slices.Index(quality.Qualities, id); index >= 0 {
			return len(profile.Qualities) + index
		}
		return len(profile.Qualities) + len(quality.Qualities)
	}
	for _, file := range files {
		candidate := quality.Current{Quality: file.Quality, Score: file.Score}
		if best != nil {
			candidateResolution, bestResolution := quality.Parse(file.Quality).Resolution, quality.Parse(best.Quality).Resolution
			if !slices.Contains(profile.Qualities, best.Quality) && bestResolution > candidateResolution {
				continue
			}
			if (slices.Contains(profile.Qualities, file.Quality) || candidateResolution <= bestResolution) &&
				(rank(file.Quality) > rank(best.Quality) || (rank(file.Quality) == rank(best.Quality) && file.Score <= best.Score)) {
				continue
			}
		}
		best = &candidate
	}
	return best
}

func (s *Service) Discover(ctx context.Context, q string, page int) ([]metadata.Title, error) {
	query := strings.TrimSpace(q)
	if query == "" || utf8.RuneCountInString(query) > tvMetadataQuery {
		return nil, fmt.Errorf("%w: enter a series title up to %d characters", ErrInvalid, tvMetadataQuery)
	}
	if page < 1 || page > tvMetadataPage {
		return nil, fmt.Errorf("%w: search page must be between 1 and %d", ErrInvalid, tvMetadataPage)
	}
	client, err := s.tvMetadataClient(ctx)
	if err != nil {
		return nil, err
	}
	titles, err := client.SearchSeries(ctx, query, page)
	if err != nil {
		return nil, fmt.Errorf("tv: %w", err)
	}
	if titles == nil {
		titles = []metadata.Title{}
	}
	return titles, nil
}

func (s *Service) Add(ctx context.Context, input AddInput) (Series, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Series{}, err
	}
	root, err := tvChooseRoot(cfg, input.RootID)
	if err != nil {
		return Series{}, err
	}
	profile, err := s.tvChooseProfile(ctx, input.ProfileID)
	if err != nil {
		return Series{}, err
	}
	title, imdbID, err := s.tvAddMetadata(ctx, input)
	if err != nil {
		return Series{}, err
	}
	if imdbID != "" {
		existing, err := s.Store.FindIMDb(ctx, imdbID)
		if err == nil {
			return s.tvDecorateOne(ctx, existing)
		}
		if !errors.Is(err, ErrNotFound) {
			return Series{}, err
		}
	}
	mode := tvNormalizeMode(input.MonitorMode)
	if mode == "" {
		mode = "all"
	}
	if !monitorModes[mode] {
		return Series{}, fmt.Errorf("%w: monitor mode must be all, future, missing, existing, first, latest, or none", ErrInvalid)
	}
	series := Series{
		ID: rand.Text(), Metadata: title, Monitored: input.Monitored, MonitorMode: mode,
		ProfileID: profile.ID, RootID: root.ID, Tags: normalizeList(input.Tags),
	}
	saved, err := s.Store.Create(ctx, series)
	if err != nil {
		if errors.Is(err, ErrConflict) && imdbID != "" {
			if existing, findErr := s.Store.FindIMDb(ctx, imdbID); findErr == nil {
				return s.tvDecorateOne(ctx, existing)
			}
		}
		return Series{}, err
	}
	_ = s.Store.Event(ctx, saved.ID, "", "added", truncate("Added "+tvSeriesLabel(saved), maxTextRunes))
	updated := saved
	err = s.withSeriesLock(ctx, saved.ID, func(ctx context.Context) error {
		var err error
		updated, err = s.tvDiscoverInitial(ctx, saved)
		return err
	})
	if errors.Is(err, ErrConflict) {
		updated, err = s.Store.Get(ctx, saved.ID)
	}
	if err != nil {
		return Series{}, err
	}
	return s.tvDecorateOne(ctx, updated)
}

func (s *Service) tvAddMetadata(ctx context.Context, input AddInput) (metadata.Title, string, error) {
	title := input.Metadata
	imdbID := strings.ToLower(strings.TrimSpace(input.IMDbID))
	if imdbID == "" {
		imdbID = strings.ToLower(strings.TrimSpace(title.IMDbID))
	}
	if imdbID != "" && !metadata.ValidIMDbID(imdbID) {
		return metadata.Title{}, "", fmt.Errorf("%w: IMDb ID must look like tt1234567", ErrInvalid)
	}
	if kind := strings.TrimSpace(title.Type); kind != "" && kind != "series" {
		return metadata.Title{}, "", fmt.Errorf("%w: series metadata type must be series", ErrInvalid)
	}
	title.Type = "series"
	title.IMDbID = imdbID
	client, configured, err := s.tvOptionalMetadataClient(ctx)
	if err != nil {
		return metadata.Title{}, "", err
	}
	if imdbID != "" && configured {
		looked, err := client.LookupSeries(ctx, imdbID)
		if err != nil {
			return metadata.Title{}, "", fmt.Errorf("tv: %w", err)
		}
		if looked.IMDbID != "" && !strings.EqualFold(looked.IMDbID, imdbID) {
			return metadata.Title{}, "", fmt.Errorf("%w: the metadata provider returned a different series", ErrInvalid)
		}
		looked.IMDbID = imdbID
		return looked, imdbID, nil
	}
	if strings.TrimSpace(title.Title) == "" {
		if imdbID != "" {
			return metadata.Title{}, "", fmt.Errorf("tv: OMDb API key is not configured: %w", downloads.ErrNotConfigured)
		}
		return metadata.Title{}, "", fmt.Errorf("%w: provide an IMDb ID or a series title", ErrInvalid)
	}
	// Without a provider an IMDb ID stays a manual claim next to the entered title data.
	title.Rating = nil
	title.Votes = 0
	return title, imdbID, nil
}

func (s *Service) tvDiscoverInitial(ctx context.Context, series Series) (Series, error) {
	if series.Metadata.IMDbID == "" {
		return series, nil
	}
	client, configured, err := s.tvOptionalMetadataClient(ctx)
	if err != nil {
		return series, err
	}
	if !configured {
		return series, nil
	}
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return series, err
	}
	total := tvKnownSeasons(series, episodes)
	season := 1
	if series.MonitorMode == "latest" {
		season = total
	}
	var failures []error
	if _, err := s.tvFetchSeason(ctx, client, series, tvEpisodeKeys(episodes), season); err != nil {
		failures = append(failures, err)
	}
	return s.tvFinalizeRefresh(ctx, series.ID, total, failures)
}

func (s *Service) Refresh(ctx context.Context, id string) (Series, error) {
	var refreshed Series
	err := s.withSeriesLock(ctx, id, func(ctx context.Context) error {
		series, err := s.Store.Get(ctx, id)
		if err != nil {
			return err
		}
		if series.Metadata.IMDbID == "" {
			return fmt.Errorf("%w: this series has no IMDb ID to refresh", ErrInvalid)
		}
		client, err := s.tvMetadataClient(ctx)
		if err != nil {
			return err
		}
		title, err := client.LookupSeries(ctx, series.Metadata.IMDbID)
		if err != nil {
			return fmt.Errorf("tv: %w", err)
		}
		if title.IMDbID != "" && !strings.EqualFold(title.IMDbID, series.Metadata.IMDbID) {
			return fmt.Errorf("%w: the metadata provider returned a different series", ErrInvalid)
		}
		title.IMDbID = series.Metadata.IMDbID
		series, err = s.Store.Patch(ctx, id, map[string]any{"metadata": title})
		if err != nil {
			return err
		}
		episodes, err := s.Store.Episodes(ctx, id)
		if err != nil {
			return err
		}
		total := tvKnownSeasons(series, episodes)
		loaded, missed, err := s.tvSeasonMarks(ctx, id, total)
		if err != nil {
			return err
		}
		existing := tvEpisodeKeys(episodes)
		var failures []error
		due := []int{}
		for season := 1; season <= total; season++ {
			if tvSeasonDue(loaded, missed, season) {
				due = append(due, season)
			}
		}
		// Load missing seasons first, then refresh the oldest catalog data without starving long series.
		sort.SliceStable(due, func(i, j int) bool {
			a, aLoaded := loaded[due[i]]
			b, bLoaded := loaded[due[j]]
			if aLoaded != bLoaded {
				return !aLoaded
			}
			if !aLoaded {
				return due[i] < due[j]
			}
			return a.Before(b)
		})
		for _, season := range due[:min(len(due), tvSeasonsPerCall)] {
			if ctx.Err() != nil {
				break
			}
			if _, err := s.tvFetchSeason(ctx, client, series, existing, season); err != nil {
				failures = append(failures, err)
				break
			}
		}
		refreshed, err = s.tvFinalizeRefresh(ctx, id, total, failures)
		return err
	})
	if err != nil {
		return Series{}, err
	}
	return s.tvDecorateOne(ctx, refreshed)
}

func (s *Service) tvFetchSeason(ctx context.Context, client *metadata.Client, series Series, existing map[episodeKey]bool, season int) (bool, error) {
	episodes, err := client.Season(ctx, series.Metadata.IMDbID, season)
	if err != nil {
		if tvMetadataNotFound(err) {
			return false, s.tvMarkSeasonMissed(ctx, series.ID, season)
		}
		return false, fmt.Errorf("tv: %w", err)
	}
	first, latest := tvCatalogBoundaries(existing, season)
	for _, item := range episodes {
		stored, err := s.Store.UpsertEpisode(ctx, Episode{
			SeriesID: series.ID, IMDbID: item.IMDbID, Title: item.Title,
			Season: item.Season, Number: item.Number, AirDate: item.AirDate, Rating: item.Rating,
		})
		if err != nil {
			return false, err
		}
		key := episodeKey{season: stored.Season, number: stored.Number}
		if existing[key] {
			continue
		}
		existing[key] = true
		want := tvMonitorModeWants(series.MonitorMode, stored, false, first, latest)
		if want != stored.Monitored {
			if _, err := s.Store.PatchEpisode(ctx, stored.ID, map[string]any{"monitored": want}); err != nil {
				return false, err
			}
		}
	}
	if err := s.tvMarkSeasonLoaded(ctx, series.ID, season); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) tvFinalizeRefresh(ctx context.Context, seriesID string, total int, failures []error) (Series, error) {
	loaded, _, err := s.tvSeasonMarks(ctx, seriesID, total)
	if err != nil {
		return Series{}, err
	}
	patch := map[string]any{"error": tvEpisodesLoading}
	if len(loaded) == total && len(failures) == 0 {
		patch = map[string]any{"error": "", "lastRefreshAt": time.Now().UTC()}
	}
	saved, err := s.Store.Patch(ctx, seriesID, patch)
	if err != nil {
		return Series{}, err
	}
	for _, failure := range failures {
		_ = s.Store.Event(ctx, seriesID, "", "metadata-failed", truncate(failure.Error(), maxTextRunes))
	}
	return saved, nil
}

func tvKnownSeasons(series Series, episodes []Episode) int {
	total := series.Metadata.TotalSeasons
	for _, episode := range episodes {
		if episode.Season > total {
			total = episode.Season
		}
	}
	if total < 1 {
		total = 1
	}
	if total > maxSeason {
		total = maxSeason
	}
	return total
}

func (s *Service) tvSeasonMarks(ctx context.Context, seriesID string, total int) (map[int]time.Time, map[int]time.Time, error) {
	names := make([]string, 0, total*2)
	for season := 1; season <= total; season++ {
		names = append(names, tvSeasonTaskName(seriesID, season), tvSeasonMissTaskName(seriesID, season))
	}
	rows, err := s.pool.Query(ctx, `SELECT name, last_run FROM tv_automation WHERE name = ANY($1::text[])`, names)
	if err != nil {
		return nil, nil, errors.New("tv: season metadata state could not be loaded")
	}
	defer rows.Close()
	loaded := make(map[int]time.Time, total)
	missed := make(map[int]time.Time, total)
	loadedPrefix := "season-" + seriesID + "-"
	missPrefix := "season-miss-" + seriesID + "-"
	for rows.Next() {
		var name string
		var last time.Time
		if err := rows.Scan(&name, &last); err != nil {
			return nil, nil, errors.New("tv: season metadata state could not be loaded")
		}
		if season, err := strconv.Atoi(strings.TrimPrefix(name, loadedPrefix)); err == nil && season >= 1 && season <= total {
			loaded[season] = last
			continue
		}
		if season, err := strconv.Atoi(strings.TrimPrefix(name, missPrefix)); err == nil && season >= 1 && season <= total {
			missed[season] = last
		}
	}
	return loaded, missed, rows.Err()
}

func tvSeasonDue(loaded, missed map[int]time.Time, season int) bool {
	if last, ok := loaded[season]; ok {
		return time.Since(last) >= tvSeasonRefresh
	}
	if last, ok := missed[season]; ok {
		return time.Since(last) >= tvSeasonMissRetry
	}
	return true
}

func (s *Service) tvMarkSeasonLoaded(ctx context.Context, seriesID string, season int) error {
	return s.Store.MarkTask(ctx, tvSeasonTaskName(seriesID, season))
}

func (s *Service) tvMarkSeasonMissed(ctx context.Context, seriesID string, season int) error {
	return s.Store.MarkTask(ctx, tvSeasonMissTaskName(seriesID, season))
}

func tvSeasonTaskName(seriesID string, season int) string {
	return fmt.Sprintf("season-%s-%d", seriesID, season)
}

func tvSeasonMissTaskName(seriesID string, season int) string {
	return fmt.Sprintf("season-miss-%s-%d", seriesID, season)
}

type episodeKey struct {
	season int
	number int
}

func tvEpisodeKeys(episodes []Episode) map[episodeKey]bool {
	keys := make(map[episodeKey]bool, len(episodes))
	for _, episode := range episodes {
		keys[episodeKey{season: episode.Season, number: episode.Number}] = true
	}
	return keys
}

func tvCatalogBoundaries(keys map[episodeKey]bool, season int) (int, int) {
	first, latest := 0, 0
	for key := range keys {
		if key.season <= 0 {
			continue
		}
		if first == 0 || key.season < first {
			first = key.season
		}
		if key.season > latest {
			latest = key.season
		}
	}
	if season > 0 {
		if first == 0 || season < first {
			first = season
		}
		if season > latest {
			latest = season
		}
	}
	return first, latest
}

func tvNormalizeMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func tvMonitorModeWants(mode string, episode Episode, hasFile bool, first, latest int) bool {
	mode = tvNormalizeMode(mode)
	if mode == "" {
		mode = "all"
	}
	if episode.Season == 0 && mode != "all" {
		return false
	}
	switch mode {
	case "all":
		return true
	case "future":
		return tvUpcoming(episode.AirDate)
	case "missing":
		return !hasFile
	case "existing":
		return hasFile
	case "first":
		return episode.Season == first
	case "latest":
		return episode.Season == latest
	default:
		return false
	}
}

// tvMonitorFlags recomputes every episode flag for one explicit mode from fresh catalog state.
func (s *Service) tvMonitorFlags(ctx context.Context, cfg Config, series Series, mode string) (map[string]bool, error) {
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	if mode == "existing" || mode == "missing" {
		episodes = tvAnnotateEpisodes(cfg, episodes)
	}
	first, latest := tvCatalogBoundaries(tvEpisodeKeys(episodes), 0)
	if total := series.Metadata.TotalSeasons; total > latest {
		latest = total
	}
	flags := make(map[string]bool, len(episodes))
	for _, episode := range episodes {
		flags[episode.ID] = tvMonitorModeWants(mode, episode, len(tvUsableFiles(episode.Files)) > 0, first, latest)
	}
	return flags, nil
}

func tvEditFields(input Series, profileID, rootID string) map[string]any {
	fields := map[string]any{
		"monitored": input.Monitored,
		"tags":      normalizeList(input.Tags),
	}
	if profileID != "" {
		fields["profileId"] = profileID
	}
	if rootID != "" {
		fields["rootId"] = rootID
	}
	return fields
}

func tvModeChanged(mode string, current Series) (bool, error) {
	if mode == "" {
		return false, nil
	}
	if !monitorModes[mode] {
		return false, fmt.Errorf("%w: monitor mode must be all, future, missing, existing, first, latest, or none", ErrInvalid)
	}
	return mode != current.MonitorMode, nil
}

func (s *Service) Update(ctx context.Context, id string, input Series) (Series, error) {
	id = strings.TrimSpace(id)
	profileID := strings.TrimSpace(input.ProfileID)
	if profileID != "" {
		if _, err := s.tvProfileByID(ctx, profileID); err != nil {
			return Series{}, err
		}
	}
	rootID := strings.TrimSpace(input.RootID)
	if rootID != "" {
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return Series{}, err
		}
		if _, ok := rootFor(cfg, rootID); !ok {
			return Series{}, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
	}
	mode := tvNormalizeMode(input.MonitorMode)
	var saved Series
	err := s.withSeriesLock(ctx, id, func(ctx context.Context) error {
		// The fresh series decides whether this edit is an explicit mode change.
		current, err := s.Store.Get(ctx, id)
		if err != nil {
			return err
		}
		changed, err := tvModeChanged(mode, current)
		if err != nil {
			return err
		}
		fields := tvEditFields(input, profileID, rootID)
		monitors := map[string]bool{}
		if changed {
			fields["monitorMode"] = mode
			cfg, err := s.Store.Config(ctx)
			if err != nil {
				return err
			}
			if monitors, err = s.tvMonitorFlags(ctx, cfg, current, mode); err != nil {
				return err
			}
		}
		edited, err := s.Store.Edit(ctx, map[string]map[string]any{id: fields}, monitors)
		if err != nil {
			return err
		}
		if len(edited) != 1 {
			return errors.New("tv: series edit returned an unexpected result")
		}
		saved = edited[0]
		return nil
	})
	if err != nil {
		return Series{}, err
	}
	return s.tvDecorateOne(ctx, saved)
}

func (s *Service) Bulk(ctx context.Context, input BulkInput) ([]Series, error) {
	ids := normalizeList(input.IDs)
	if len(ids) == 0 || len(ids) > tvBulkIDLimit {
		return nil, fmt.Errorf("%w: provide between 1 and %d series IDs", ErrInvalid, tvBulkIDLimit)
	}
	var updated []Series
	err := s.withSeriesLocks(ctx, ids, func(ctx context.Context) error {
		var err error
		updated, err = s.bulkLocked(ctx, input)
		return err
	})
	return updated, err
}

func (s *Service) bulkLocked(ctx context.Context, input BulkInput) ([]Series, error) {
	ids := normalizeList(input.IDs)
	if len(ids) == 0 || len(ids) > tvBulkIDLimit {
		return nil, fmt.Errorf("%w: provide between 1 and %d series IDs", ErrInvalid, tvBulkIDLimit)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	template := map[string]any{}
	if input.Monitored != nil {
		template["monitored"] = *input.Monitored
	}
	if input.Tags != nil {
		template["tags"] = normalizeList(*input.Tags)
	}
	if input.ProfileID != nil {
		profileID := strings.TrimSpace(*input.ProfileID)
		if profileID == "" {
			return nil, fmt.Errorf("%w: a quality profile is required", ErrInvalid)
		}
		if _, err := s.tvProfileByID(ctx, profileID); err != nil {
			return nil, err
		}
		template["profileId"] = profileID
	}
	if input.RootID != nil {
		rootID := strings.TrimSpace(*input.RootID)
		if _, ok := rootFor(cfg, rootID); !ok {
			return nil, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
		template["rootId"] = rootID
	}
	mode := ""
	if input.MonitorMode != nil {
		mode = tvNormalizeMode(*input.MonitorMode)
		if !monitorModes[mode] {
			return nil, fmt.Errorf("%w: monitor mode must be all, future, missing, existing, first, latest, or none", ErrInvalid)
		}
	}
	if len(template) == 0 && mode == "" {
		return nil, fmt.Errorf("%w: provide at least one change", ErrInvalid)
	}
	changes := make(map[string]map[string]any, len(ids))
	monitors := map[string]bool{}
	for _, id := range ids {
		series, err := s.Store.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("tv: %w: %s", err, id)
		}
		fields := make(map[string]any, len(template)+1)
		for name, value := range template {
			fields[name] = value
		}
		if changed, err := tvModeChanged(mode, series); err != nil {
			return nil, err
		} else if changed {
			fields["monitorMode"] = mode
			flags, err := s.tvMonitorFlags(ctx, cfg, series, mode)
			if err != nil {
				return nil, err
			}
			for episodeID, monitored := range flags {
				monitors[episodeID] = monitored
			}
		}
		changes[id] = fields
	}
	if len(monitors) > 0 && len(monitors) > maxEditEpisodes {
		return nil, fmt.Errorf("%w: too many episode flags in one edit", ErrInvalid)
	}
	edited, err := s.Store.Edit(ctx, changes, monitors)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Series, len(edited))
	for _, series := range edited {
		byID[series.ID] = series
	}
	updated := make([]Series, 0, len(ids))
	for _, id := range ids {
		series, ok := byID[id]
		if !ok {
			return nil, errors.New("tv: bulk edit returned an unexpected result")
		}
		updated = append(updated, series)
	}
	return s.tvDecorateList(ctx, updated)
}

func (s *Service) AddEpisode(ctx context.Context, seriesID string, input Episode) (Episode, error) {
	var episode Episode
	err := s.withSeriesLock(ctx, strings.TrimSpace(seriesID), func(ctx context.Context) error {
		var err error
		episode, err = s.addEpisode(ctx, seriesID, input)
		return err
	})
	return episode, err
}

func (s *Service) addEpisode(ctx context.Context, seriesID string, input Episode) (Episode, error) {
	series, err := s.Store.Get(ctx, strings.TrimSpace(seriesID))
	if err != nil {
		return Episode{}, err
	}
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return Episode{}, err
	}
	existing := tvEpisodeKeys(episodes)
	input.ID = ""
	input.SeriesID = series.ID
	input.Files = nil
	input.LastSearchAt = nil
	input.Error = ""
	stored, err := s.Store.UpsertEpisode(ctx, input)
	if err != nil {
		return Episode{}, err
	}
	if !existing[episodeKey{season: stored.Season, number: stored.Number}] {
		first, latest := tvCatalogBoundaries(existing, stored.Season)
		want := tvMonitorModeWants(series.MonitorMode, stored, false, first, latest)
		if want != stored.Monitored {
			stored, err = s.Store.PatchEpisode(ctx, stored.ID, map[string]any{"monitored": want})
			if err != nil {
				return Episode{}, err
			}
		}
	}
	_ = s.Store.Event(ctx, series.ID, stored.ID, "episode-added", truncate("Added "+tvEpisodeLabel(stored), maxTextRunes))
	return stored, nil
}

func (s *Service) Monitor(ctx context.Context, seriesID string, input MonitorInput) (Series, error) {
	var series Series
	err := s.withSeriesLock(ctx, strings.TrimSpace(seriesID), func(ctx context.Context) error {
		if _, err := s.Store.Monitor(ctx, seriesID, input); err != nil {
			return err
		}
		var err error
		series, err = s.Get(ctx, seriesID)
		return err
	})
	return series, err
}

// tvOwnedFile is one archived path with the root it lives under.
type tvOwnedFile struct {
	root movies.RootFolder
	path string
}

func tvOwnedFiles(cfg Config, episodes []Episode) ([]tvOwnedFile, error) {
	seen := map[string]bool{}
	owned := make([]tvOwnedFile, 0, len(episodes)*2)
	add := func(root movies.RootFolder, rel string) {
		if rel == "" || rel == "." {
			return
		}
		key := root.ID + "\x00" + rel
		if seen[key] {
			return
		}
		seen[key] = true
		owned = append(owned, tvOwnedFile{root: root, path: rel})
	}
	for _, episode := range episodes {
		for _, file := range episode.Files {
			root, ok := rootFor(cfg, file.RootID)
			if !ok || !filepath.IsLocal(file.Path) {
				continue
			}
			add(root, file.Path)
			subtitles, err := library.SubtitleSidecars(root.Path, file.Path)
			if err != nil {
				return nil, err
			}
			for _, subtitle := range subtitles {
				add(root, subtitle.Path)
			}
			for _, sidecar := range tvSidecarCandidates(file.Path) {
				if tvOwnedSidecarPresent(root, sidecar) {
					add(root, sidecar)
				}
			}
		}
	}
	return owned, nil
}

// tvSidecarCandidates never returns a path above the series folder derived from the owned video.
func tvSidecarCandidates(rel string) []string {
	rel = filepath.ToSlash(rel)
	base := strings.TrimSuffix(rel, path.Ext(rel)) + ".nfo"
	candidates := []string{base}
	dir := path.Dir(rel)
	if dir == "." || dir == "/" {
		return candidates
	}
	candidates = append(candidates, path.Join(dir, "tvshow.nfo"))
	if tvSeasonDir(path.Base(dir)) {
		candidates = append(candidates, path.Join(path.Dir(dir), "tvshow.nfo"))
	}
	return candidates
}

func tvSeasonDir(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, "season") || strings.HasPrefix(name, "specials")
}

func tvOwnedSidecarPresent(root movies.RootFolder, rel string) bool {
	handle, err := library.Open(root.Path, rel)
	if err != nil {
		return false
	}
	info, err := handle.Stat()
	handle.Close()
	return err == nil && info.Mode().IsRegular()
}

func (s *Service) tvForgetJournal(ctx context.Context, files []tvOwnedFile) error {
	for _, file := range files {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM download_library_files WHERE root_path = $1 AND path = $2`, file.root.Path, file.path); err != nil {
			return errors.New("tv: archived file links could not be removed")
		}
	}
	return nil
}

func (s *Service) Remove(ctx context.Context, id string, deleteFiles bool) error {
	id = strings.TrimSpace(id)
	return s.withSeriesLock(ctx, id, func(ctx context.Context) error {
		if _, err := s.Store.Get(ctx, id); err != nil {
			return err
		}
		if deleteFiles {
			cfg, err := s.Store.Config(ctx)
			if err != nil {
				return err
			}
			episodes, err := s.Store.Episodes(ctx, id)
			if err != nil {
				return err
			}
			owned, err := tvOwnedFiles(cfg, episodes)
			if err != nil {
				return err
			}
			for _, file := range owned {
				if err := library.Archive(file.root.Path, file.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("tv: archive %s: %w", file.path, err)
				}
			}
			if err := s.tvForgetJournal(ctx, owned); err != nil {
				return err
			}
		}
		return s.Store.Delete(ctx, id)
	})
}

func (s *Service) Calendar(ctx context.Context) ([]CalendarEntry, error) {
	series, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	episodes, err := s.Store.Episodes(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Series, len(series))
	for _, item := range series {
		byID[item.ID] = item
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	type dated struct {
		entry CalendarEntry
		date  time.Time
	}
	upcoming := make([]dated, 0, len(episodes))
	for _, episode := range episodes {
		date, ok := tvAirDate(episode.AirDate)
		if !ok || date.Before(today) {
			continue
		}
		item, ok := byID[episode.SeriesID]
		if !ok {
			continue
		}
		upcoming = append(upcoming, dated{
			entry: CalendarEntry{
				SeriesID: item.ID, SeriesTitle: item.Metadata.Title,
				SeriesIMDbID: item.Metadata.IMDbID, Poster: item.Metadata.Poster, Episode: episode,
			},
			date: date,
		})
	}
	sort.SliceStable(upcoming, func(i, j int) bool {
		if !upcoming[i].date.Equal(upcoming[j].date) {
			return upcoming[i].date.Before(upcoming[j].date)
		}
		if upcoming[i].entry.SeriesTitle != upcoming[j].entry.SeriesTitle {
			return upcoming[i].entry.SeriesTitle < upcoming[j].entry.SeriesTitle
		}
		if upcoming[i].entry.Season != upcoming[j].entry.Season {
			return upcoming[i].entry.Season < upcoming[j].entry.Season
		}
		return upcoming[i].entry.Number < upcoming[j].entry.Number
	})
	entries := make([]CalendarEntry, 0, len(upcoming))
	for _, item := range upcoming {
		entries = append(entries, item.entry)
	}
	return entries, nil
}

func (s *Service) Search(ctx context.Context, seriesID string, target Target) ([]Release, error) {
	if err := tvValidateTarget(target); err != nil {
		return nil, err
	}
	series, err := s.Store.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	profile, err := s.tvRequireProfile(ctx, series)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	annotated := tvAnnotateEpisodes(cfg, episodes)
	byID := make(map[string]Episode, len(annotated))
	for _, episode := range annotated {
		byID[episode.ID] = episode
	}
	releases, err := s.Downloads.SearchTV(ctx, series.Metadata.IMDbID, series.Metadata.Title, target.Season, target.Episode)
	if err != nil {
		return nil, fmt.Errorf("tv: %w", err)
	}
	blocked, err := s.tvBlockedReleases(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	acquisitions, err := s.tvSeriesAcquisitions(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	results := make([]Release, 0, len(releases))
	for _, release := range releases {
		item := evaluateRelease(profile, series, annotated, release)
		reasons := make([]string, 0, 3)
		if len(item.EpisodeIDs) > 0 {
			if !tvTargetCovers(target, item.EpisodeIDs, byID) {
				reasons = append(reasons, "release does not match the requested episode")
			}
		}
		if blocked[release.ID] {
			reasons = append(reasons, "release is blocked")
		}
		if reason := tvAcquisitionReason(acquisitions, release.ID, item.EpisodeIDs); reason != "" {
			reasons = append(reasons, reason)
		}
		if len(reasons) > 0 {
			item.Decision.Allowed = false
			item.Decision.Reasons = append(item.Decision.Reasons, reasons...)
		}
		results = append(results, item)
	}
	tvSortReleases(results)
	return results, nil
}

func (s *Service) Grab(ctx context.Context, seriesID string, input GrabInput) (downloads.Job, error) {
	releaseID := strings.TrimSpace(input.ReleaseID)
	if releaseID == "" || len(releaseID) > maxReleaseBytes {
		return downloads.Job{}, fmt.Errorf("%w: a release ID is required", ErrInvalid)
	}
	if err := tvValidateTarget(input.Target); err != nil {
		return downloads.Job{}, err
	}
	var job downloads.Job
	err := s.withSeriesLock(ctx, seriesID, func(ctx context.Context) error {
		series, err := s.Store.Get(ctx, seriesID)
		if err != nil {
			return err
		}
		profile, err := s.tvRequireProfile(ctx, series)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		episodes, err := s.Store.Episodes(ctx, series.ID)
		if err != nil {
			return err
		}
		annotated := tvAnnotateEpisodes(cfg, episodes)
		byID := make(map[string]Episode, len(annotated))
		for _, episode := range annotated {
			byID[episode.ID] = episode
		}
		releases, err := s.Downloads.SearchTV(ctx, series.Metadata.IMDbID, series.Metadata.Title, input.Season, input.Episode)
		if err != nil {
			return fmt.Errorf("tv: %w", err)
		}
		chosen, found := tvFindRelease(releases, releaseID)
		if !found {
			// A scheduler RSS grab may be absent from a targeted search; the feed is still evidence when identity is strict.
			if feed, err := s.Downloads.RSSTV(ctx); err == nil {
				chosen, found = tvFindRelease(feed, releaseID)
			}
		}
		if !found {
			return fmt.Errorf("%w: release does not appear in a fresh search", ErrInvalid)
		}
		item := evaluateRelease(profile, series, annotated, chosen)
		if len(item.EpisodeIDs) == 0 {
			return fmt.Errorf("%w: %s", ErrInvalid, tvDecisionReason(item.Decision))
		}
		if !tvTargetCovers(input.Target, item.EpisodeIDs, byID) {
			return fmt.Errorf("%w: release does not match the requested episode", ErrInvalid)
		}
		blocked, err := s.Store.Blocked(ctx, series.ID, releaseID)
		if err != nil {
			return err
		}
		acquisitions, err := s.tvSeriesAcquisitions(ctx, series.ID)
		if err != nil {
			return err
		}
		var abandoned []Acquisition
		for _, acquisition := range acquisitions {
			same := acquisition.ReleaseID == releaseID
			if !same && !tvOverlap(acquisition.EpisodeIDs, item.EpisodeIDs) {
				continue
			}
			switch acquisition.Status {
			case "superseded", "imported":
				continue
			case "cancelled":
				abandoned = append(abandoned, acquisition)
			case "failed":
				if same && !input.Override {
					return fmt.Errorf("%w: this release failed before; retry it with override", ErrConflict)
				}
				abandoned = append(abandoned, acquisition)
			case "import-failed":
				if !input.Override {
					return fmt.Errorf("%w: another download covering these episodes is already being processed", ErrConflict)
				}
				abandoned = append(abandoned, acquisition)
			default:
				if same {
					return fmt.Errorf("%w: this release is already being processed", ErrConflict)
				}
				return fmt.Errorf("%w: another download covering these episodes is already being processed", ErrConflict)
			}
		}
		if blocked && !input.Override {
			return fmt.Errorf("%w: this release is blocked", ErrConflict)
		}
		if !item.Decision.Allowed && !input.Override {
			return fmt.Errorf("%w: %s", ErrConflict, tvDecisionReason(item.Decision))
		}
		for _, acquisition := range abandoned {
			if err := s.Store.SaveAcquisition(ctx, Acquisition{
				SeriesID: series.ID, EpisodeIDs: acquisition.EpisodeIDs, JobID: acquisition.JobID,
				ReleaseID: acquisition.ReleaseID, Title: acquisition.Title, Decision: acquisition.Decision,
				Override: acquisition.Override, Status: "superseded", Error: "superseded by a newer release grab",
			}); err != nil {
				return err
			}
		}
		job, err = s.Downloads.Add(ctx, releaseID, chosen.Title)
		if err != nil {
			return fmt.Errorf("tv: %w", err)
		}
		if job.Status == "failed" || job.Status == "cancelled" {
			if job.Status == "failed" && !input.Override {
				return fmt.Errorf("%w: the download failed before; retry it with override", ErrConflict)
			}
			job, err = s.Downloads.Retry(ctx, job.ID)
			if err != nil {
				return fmt.Errorf("tv: %w", err)
			}
		}
		if err := s.Store.SaveAcquisition(ctx, Acquisition{
			SeriesID: series.ID, EpisodeIDs: item.EpisodeIDs, JobID: job.ID, ReleaseID: releaseID,
			Title: chosen.Title, Decision: item.Decision, Override: input.Override, Status: "queued",
		}); err != nil {
			if errors.Is(err, ErrConflict) {
				return fmt.Errorf("%w: download already belongs to another series or a movie", ErrConflict)
			}
			return err
		}
		_ = s.Store.Event(ctx, series.ID, "", "grabbed", truncate("Grabbed "+chosen.Title, maxTextRunes))
		return nil
	})
	if err != nil {
		return downloads.Job{}, err
	}
	return job, nil
}

func (s *Service) tvSeriesAcquisitions(ctx context.Context, seriesID string) ([]Acquisition, error) {
	all, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return nil, err
	}
	acquisitions := make([]Acquisition, 0, 4)
	for _, acquisition := range all {
		if acquisition.SeriesID == seriesID {
			acquisitions = append(acquisitions, acquisition)
		}
	}
	return acquisitions, nil
}

// tvChooseRoot defaults to the first configured root when no ID is posted.
func tvChooseRoot(cfg Config, id string) (movies.RootFolder, error) {
	if id = strings.TrimSpace(id); id != "" {
		if root, ok := rootFor(cfg, id); ok {
			return root, nil
		}
		return movies.RootFolder{}, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
	}
	if len(cfg.RootFolders) == 0 {
		return movies.RootFolder{}, fmt.Errorf("%w: add a root folder in TV configuration first", ErrInvalid)
	}
	return cfg.RootFolders[0], nil
}

func (s *Service) tvChooseProfile(ctx context.Context, id string) (quality.Profile, error) {
	if id = strings.TrimSpace(id); id != "" {
		return s.tvProfileByID(ctx, id)
	}
	profiles, err := s.Shared.Profiles(ctx)
	if err != nil {
		return quality.Profile{}, err
	}
	if len(profiles) == 0 {
		return quality.Profile{}, fmt.Errorf("%w: create a quality profile first", ErrInvalid)
	}
	return profiles[0], nil
}

func (s *Service) tvProfileByID(ctx context.Context, id string) (quality.Profile, error) {
	profile, err := s.Shared.Profile(ctx, strings.TrimSpace(id))
	if errors.Is(err, movies.ErrNotFound) {
		return quality.Profile{}, fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
	}
	if err != nil {
		return quality.Profile{}, err
	}
	return profile, nil
}

func (s *Service) tvRequireProfile(ctx context.Context, series Series) (quality.Profile, error) {
	if series.ProfileID == "" {
		return s.tvChooseProfile(ctx, "")
	}
	return s.tvProfileByID(ctx, series.ProfileID)
}

// tvOptionalMetadataClient reports whether the shared OMDb settings allow provider calls.
func (s *Service) tvOptionalMetadataClient(ctx context.Context) (*metadata.Client, bool, error) {
	cfg, err := s.Shared.Config(ctx)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(cfg.MetadataURL) == "" || strings.TrimSpace(cfg.MetadataAPIKey) == "" {
		return nil, false, nil
	}
	client, err := metadata.New(cfg.MetadataURL, cfg.MetadataAPIKey)
	if err != nil {
		return nil, false, fmt.Errorf("tv: %w", err)
	}
	return client, true, nil
}

func (s *Service) tvMetadataClient(ctx context.Context) (*metadata.Client, error) {
	client, configured, err := s.tvOptionalMetadataClient(ctx)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, fmt.Errorf("tv: OMDb API key is not configured: %w", downloads.ErrNotConfigured)
	}
	return client, nil
}

func tvValidateTarget(target Target) error {
	if target.Season < -1 || target.Season > maxSeason {
		return fmt.Errorf("%w: season must be -1 for the whole series, 0 for specials, or 1 to %d", ErrInvalid, maxSeason)
	}
	if target.Episode < 0 || target.Episode > maxEpisodeNumber {
		return fmt.Errorf("%w: episode must be between 0 and %d", ErrInvalid, maxEpisodeNumber)
	}
	if target.Season < 0 && target.Episode > 0 {
		return fmt.Errorf("%w: a whole-series search cannot target one episode", ErrInvalid)
	}
	return nil
}

func tvTargetCovers(target Target, ids []string, byID map[string]Episode) bool {
	if len(ids) == 0 {
		return false
	}
	found := false
	for _, id := range ids {
		episode, ok := byID[id]
		if !ok {
			return false
		}
		if target.Season >= 0 && episode.Season != target.Season {
			return false
		}
		if target.Episode > 0 && episode.Number == target.Episode {
			found = true
		}
	}
	return target.Season < 0 || target.Episode == 0 || found
}

func tvDecisionReason(decision quality.Decision) string {
	if len(decision.Reasons) == 0 {
		return "release is not allowed"
	}
	return strings.Join(decision.Reasons, "; ")
}

func (s *Service) tvBlockedReleases(ctx context.Context, seriesID string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT release_id FROM tv_blocklist WHERE series_id = $1`, seriesID)
	if err != nil {
		return nil, errors.New("tv: blocked releases could not be loaded")
	}
	defer rows.Close()
	blocked := map[string]bool{}
	for rows.Next() {
		var releaseID string
		if err := rows.Scan(&releaseID); err != nil {
			return nil, errors.New("tv: blocked releases could not be loaded")
		}
		blocked[releaseID] = true
	}
	return blocked, rows.Err()
}

func tvAcquisitionReason(acquisitions []Acquisition, releaseID string, episodeIDs []string) string {
	for _, acquisition := range acquisitions {
		if acquisition.ReleaseID != releaseID {
			continue
		}
		switch acquisition.Status {
		case "failed":
			return "release previously failed"
		case "import-failed":
			return "release was already downloaded; its import is retrying"
		case "imported":
			return "release is already imported"
		case "superseded", "cancelled":
			continue
		default:
			return "release is already downloading"
		}
	}
	for _, acquisition := range acquisitions {
		switch acquisition.Status {
		case "queued", "downloading", "paused", "importing", "import-failed", "":
		default:
			continue
		}
		if tvOverlap(acquisition.EpisodeIDs, episodeIDs) {
			return "a download covering these episodes is already in progress"
		}
	}
	return ""
}

func (s *Service) withSeriesLock(ctx context.Context, seriesID string, fn func(context.Context) error) error {
	return s.withSeriesLocks(ctx, []string{seriesID}, fn)
}

func (s *Service) withSeriesLocks(ctx context.Context, ids []string, fn func(context.Context) error) error {
	select {
	case s.lockSlots <- struct{}{}:
		defer func() { <-s.lockSlots }()
	default:
		return fmt.Errorf("%w: TV series operations are busy; try again shortly", ErrConflict)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return errors.New("tv: series operation cannot reach the database")
	}
	defer conn.Release()
	var locked []int64
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, key := range locked {
			if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
				_ = conn.Hijack().Close(unlockCtx)
				return
			}
		}
	}()
	normalized := normalizeList(ids)
	sort.Strings(normalized)
	for _, id := range normalized {
		var held bool
		key := tvSeriesLockKey(id)
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&held); err != nil {
			return errors.New("tv: series operation cannot acquire its lock")
		}
		if !held {
			return fmt.Errorf("%w: another operation for this series is already running", ErrConflict)
		}
		locked = append(locked, key)
	}
	return fn(ctx)
}

func tvSeriesLockKey(seriesID string) int64 {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(seriesID))
	return tvSeriesLockBase ^ int64(digest.Sum64())
}

func tvSeriesLabel(series Series) string {
	label := strings.TrimSpace(series.Metadata.Title)
	if series.Metadata.Year > 0 {
		label += fmt.Sprintf(" (%d)", series.Metadata.Year)
	}
	if label == "" {
		label = series.ID
	}
	return label
}

func tvEpisodeLabel(episode Episode) string {
	return fmt.Sprintf("S%02dE%02d %s", episode.Season, episode.Number, strings.TrimSpace(episode.Title))
}
