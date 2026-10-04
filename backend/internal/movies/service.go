package movies

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

var ErrNotConfigured = downloads.ErrNotConfigured

const (
	movieLockBase      int64 = 0x4D6F7669654F7073
	releaseIDLimit           = 128
	bulkIDLimit              = 500
	metadataQueryLimit       = 256
	metadataPageLimit        = 100
)

func New(ctx context.Context, pool *pgxpool.Pool, manager *downloads.Manager) (*Service, error) {
	if pool == nil {
		return nil, errors.New("movies: a PostgreSQL pool is required")
	}
	if manager == nil {
		return nil, errors.New("movies: the download manager is required")
	}
	defaults := defaultConfig(manager.Config().Directory)
	store, err := NewStore(ctx, pool, defaults)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(defaults.RootFolders[0].Path, 0o755); err != nil {
		return nil, errors.New("movies: the default library root could not be created")
	}
	return &Service{Store: store, Downloads: manager, lockSlots: store.OperationSlots()}, nil
}

func defaultConfig(directory string) Config {
	return Config{
		RootFolders:         []RootFolder{{ID: "movies", Path: filepath.Join(directory, "library", "movies")}},
		FolderTemplate:      "{title} ({year}) [imdb-{imdbId}]",
		FileTemplate:        "{title} ({year}) [{quality}]",
		ImportMode:          library.ModeLink,
		WriteNFO:            true,
		PollMinutes:         15,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
		MetadataURL:         envValue("OMDB_URL", "https://www.omdbapi.com/"),
		MetadataAPIKey:      strings.TrimSpace(os.Getenv("OMDB_API_KEY")),
		JellyfinURL:         strings.TrimSpace(os.Getenv("JELLYFIN_URL")),
		JellyfinAPIKey:      strings.TrimSpace(os.Getenv("JELLYFIN_API_KEY")),
		WebhookURL:          strings.TrimSpace(os.Getenv("IMPORT_WEBHOOK_URL")),
	}
}

func envValue(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (s *Service) List(ctx context.Context) ([]Movie, error) {
	movies, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	return s.decorate(ctx, movies)
}

func (s *Service) decorate(ctx context.Context, movies []Movie) ([]Movie, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return nil, err
	}
	byProfile := make(map[string]quality.Profile, len(profiles))
	for _, profile := range profiles {
		byProfile[profile.ID] = profile
	}
	byMovie := make(map[string][]Acquisition)
	for _, acquisition := range acquisitions {
		byMovie[acquisition.MovieID] = append(byMovie[acquisition.MovieID], acquisition)
	}
	for i := range movies {
		profile, ok := byProfile[movies[i].ProfileID]
		s.applyState(ctx, cfg, profile, ok, byMovie[movies[i].ID], &movies[i])
	}
	return movies, nil
}

func (s *Service) decorated(ctx context.Context, movie Movie) (Movie, error) {
	movies, err := s.decorate(ctx, []Movie{movie})
	if err != nil {
		return Movie{}, err
	}
	return movies[0], nil
}

func (s *Service) applyState(ctx context.Context, cfg Config, profile quality.Profile, hasProfile bool, acquisitions []Acquisition, movie *Movie) {
	files, present := annotateFiles(cfg, movie.Files)
	movie.Files = files
	movie.Error = ""
	latest := latestAcquisition(acquisitions)
	switch {
	case latest != nil && latest.Status != "failed" && latest.Status != "import-failed" && latest.Status != "imported":
		job, err := s.Downloads.Get(ctx, latest.JobID)
		switch {
		case err == nil && job.Status == "completed":
			movie.Status = "importing"
		case err == nil && job.Status == "failed":
			movie.Status = "failed"
			movie.Error = truncate(firstNonEmpty(latest.Error, job.Error, "download failed"), maxTextRunes)
		case err == nil:
			movie.Status = "downloading"
		default:
			movie.Status = "downloading"
		}
	case latest != nil && latest.Status == "import-failed":
		movie.Status = "import-failed"
		movie.Error = truncate(latest.Error, maxTextRunes)
	case latest != nil && latest.Status == "failed":
		movie.Status = "failed"
		movie.Error = truncate(latest.Error, maxTextRunes)
	case len(present) > 0:
		if !hasProfile || !profile.Upgrade || quality.Satisfied(profile, *bestCurrent(profile, present)) {
			movie.Status = "available"
		} else {
			movie.Status = "cutoff-unmet"
		}
	case !movie.Monitored:
		movie.Status = "unmonitored"
	case availabilityReached(*movie, cfg):
		movie.Status = "wanted"
	default:
		movie.Status = "missing"
	}
}

func latestAcquisition(acquisitions []Acquisition) *Acquisition {
	for i := range acquisitions {
		if acquisitions[i].Status == "superseded" {
			continue
		}
		return &acquisitions[i]
	}
	return nil
}

func annotateFiles(cfg Config, files []File) ([]File, []File) {
	annotated := make([]File, len(files))
	copy(annotated, files)
	present := make([]File, 0, len(files))
	for i := range annotated {
		if !filePresent(cfg, annotated[i]) {
			annotated[i].Missing = true
			continue
		}
		annotated[i].Missing = false
		present = append(present, annotated[i])
	}
	return annotated, present
}

func presentFiles(cfg Config, files []File) []File {
	present := make([]File, 0, len(files))
	for _, file := range files {
		if filePresent(cfg, file) {
			present = append(present, file)
		}
	}
	return present
}

func filePresent(cfg Config, file File) bool {
	root, ok := rootByID(cfg, file.RootID)
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

func bestCurrent(profile quality.Profile, files []File) *quality.Current {
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

func availabilityReached(movie Movie, cfg Config) bool {
	if cfg.MinimumAvailability == "announced" {
		return true
	}
	if date, ok := parseReleasedDate(movie.Metadata.Released); ok {
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return !date.After(today)
	}
	return movie.Metadata.Year > 0 && movie.Metadata.Year < time.Now().UTC().Year()
}

func parseReleasedDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range releasedLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func rootByID(cfg Config, id string) (RootFolder, bool) {
	for _, root := range cfg.RootFolders {
		if root.ID == id {
			return root, true
		}
	}
	return RootFolder{}, false
}

func (s *Service) chooseRoot(cfg Config, id string) (RootFolder, error) {
	if id = strings.TrimSpace(id); id != "" {
		if root, ok := rootByID(cfg, id); ok {
			return root, nil
		}
		return RootFolder{}, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
	}
	if len(cfg.RootFolders) == 0 {
		return RootFolder{}, fmt.Errorf("%w: add a root folder in movie configuration first", ErrInvalid)
	}
	return cfg.RootFolders[0], nil
}

func (s *Service) chooseProfile(ctx context.Context, id string) (quality.Profile, error) {
	if id = strings.TrimSpace(id); id != "" {
		return s.requireProfileByID(ctx, id)
	}
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		return quality.Profile{}, err
	}
	if len(profiles) == 0 {
		return quality.Profile{}, fmt.Errorf("%w: create a quality profile first", ErrInvalid)
	}
	return profiles[0], nil
}

func (s *Service) requireProfileByID(ctx context.Context, id string) (quality.Profile, error) {
	profile, err := s.Store.Profile(ctx, strings.TrimSpace(id))
	if errors.Is(err, ErrNotFound) {
		return quality.Profile{}, fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
	}
	if err != nil {
		return quality.Profile{}, err
	}
	return profile, nil
}

func (s *Service) requireProfile(ctx context.Context, movie Movie) (quality.Profile, error) {
	if movie.ProfileID == "" {
		return s.chooseProfile(ctx, "")
	}
	return s.requireProfileByID(ctx, movie.ProfileID)
}

func (s *Service) metadataConfigured(cfg Config) bool {
	return strings.TrimSpace(cfg.MetadataURL) != "" && strings.TrimSpace(cfg.MetadataAPIKey) != ""
}

func (s *Service) metadataClient(ctx context.Context) (*metadata.Client, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	if !s.metadataConfigured(cfg) {
		return nil, fmt.Errorf("movies: OMDb API key is not configured: %w", ErrNotConfigured)
	}
	client, err := metadata.New(cfg.MetadataURL, cfg.MetadataAPIKey)
	if err != nil {
		return nil, fmt.Errorf("movies: %w", err)
	}
	return client, nil
}

func (s *Service) lookup(ctx context.Context, cfg Config, imdbID string) (metadata.Title, error) {
	client, err := metadata.New(cfg.MetadataURL, cfg.MetadataAPIKey)
	if err != nil {
		return metadata.Title{}, fmt.Errorf("movies: %w", err)
	}
	title, err := client.Lookup(ctx, imdbID)
	if err != nil {
		return metadata.Title{}, fmt.Errorf("movies: %w", err)
	}
	if title.IMDbID == "" {
		title.IMDbID = imdbID
	}
	return title, nil
}

func (s *Service) Add(ctx context.Context, input AddInput) (Movie, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Movie{}, err
	}
	root, err := s.chooseRoot(cfg, input.RootID)
	if err != nil {
		return Movie{}, err
	}
	profile, err := s.chooseProfile(ctx, input.ProfileID)
	if err != nil {
		return Movie{}, err
	}
	imdbID := strings.ToLower(strings.TrimSpace(input.IMDbID))
	if imdbID == "" {
		imdbID = strings.ToLower(strings.TrimSpace(input.Metadata.IMDbID))
	}
	if imdbID != "" && !metadata.ValidIMDbID(imdbID) {
		return Movie{}, fmt.Errorf("%w: IMDb ID must look like tt1234567", ErrInvalid)
	}
	if imdbID != "" {
		existing, err := s.Store.FindIMDb(ctx, imdbID)
		if err == nil {
			return s.decorated(ctx, existing)
		}
		if !errors.Is(err, ErrNotFound) {
			return Movie{}, err
		}
	}
	movie := Movie{
		ID:         rand.Text(),
		Metadata:   input.Metadata,
		Monitored:  input.Monitored,
		ProfileID:  profile.ID,
		RootID:     root.ID,
		Tags:       normalizeList(input.Tags),
		Collection: strings.TrimSpace(input.Collection),
		Files:      []File{},
	}
	switch {
	case imdbID != "" && s.metadataConfigured(cfg):
		title, err := s.lookup(ctx, cfg, imdbID)
		if err != nil {
			return Movie{}, err
		}
		movie.Metadata = title
	case imdbID != "":
		// Without a provider the IMDb ID is only a manual claim next to real title data.
		if strings.TrimSpace(input.Metadata.Title) == "" {
			return Movie{}, fmt.Errorf("movies: OMDb API key is not configured: %w", ErrNotConfigured)
		}
		movie.Metadata.IMDbID = imdbID
		movie.Metadata.Rating = nil
		movie.Metadata.Votes = 0
	default:
		if strings.TrimSpace(movie.Metadata.Title) == "" {
			return Movie{}, fmt.Errorf("%w: provide an IMDb ID or a movie title", ErrInvalid)
		}
		movie.Metadata.IMDbID = ""
		movie.Metadata.Rating = nil
		movie.Metadata.Votes = 0
	}
	saved, err := s.Store.Save(ctx, movie)
	if err != nil {
		return Movie{}, err
	}
	if saved.ID != movie.ID {
		// A concurrent add already owns this IMDb identity.
		return s.decorated(ctx, saved)
	}
	_ = s.Store.Event(ctx, saved.ID, "added", truncate("Added "+movieLabel(saved), maxTextRunes))
	return s.decorated(ctx, saved)
}

func movieLabel(movie Movie) string {
	label := strings.TrimSpace(movie.Metadata.Title)
	if movie.Metadata.Year > 0 {
		label += fmt.Sprintf(" (%d)", movie.Metadata.Year)
	}
	if label == "" {
		label = movie.ID
	}
	return label
}

func (s *Service) Update(ctx context.Context, id string, input Movie) (Movie, error) {
	fields := map[string]any{
		"monitored":  input.Monitored,
		"tags":       normalizeList(input.Tags),
		"collection": strings.TrimSpace(input.Collection),
	}
	if profileID := strings.TrimSpace(input.ProfileID); profileID != "" {
		if _, err := s.requireProfileByID(ctx, profileID); err != nil {
			return Movie{}, err
		}
		fields["profileId"] = profileID
	}
	if rootID := strings.TrimSpace(input.RootID); rootID != "" {
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return Movie{}, err
		}
		if _, ok := rootByID(cfg, rootID); !ok {
			return Movie{}, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
		fields["rootId"] = rootID
	}
	saved, err := s.Store.Patch(ctx, id, fields)
	if err != nil {
		return Movie{}, err
	}
	return s.decorated(ctx, saved)
}

func (s *Service) Remove(ctx context.Context, id string, deleteFiles bool) error {
	return s.withMovieLock(ctx, id, func(ctx context.Context) error {
		movie, err := s.Store.Get(ctx, id)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		if deleteFiles && len(movie.Files) > 0 {
			for _, file := range movie.Files {
				root, ok := rootByID(cfg, file.RootID)
				if !ok {
					continue
				}
				if err := library.Archive(root.Path, file.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("movies: archive %s: %w", file.Path, err)
				}
			}
			s.forgetJournalFiles(ctx, cfg, movie.Files)
		}
		return s.Store.Delete(ctx, id)
	})
}

func (s *Service) forgetJournalFiles(ctx context.Context, cfg Config, files []File) {
	for _, file := range files {
		root, ok := rootByID(cfg, file.RootID)
		if !ok {
			continue
		}
		_, _ = s.Store.pool.Exec(ctx,
			`DELETE FROM download_library_files WHERE ready AND root_path = $1 AND path = $2`,
			root.Path, file.Path)
	}
}

func (s *Service) Bulk(ctx context.Context, input BulkInput) ([]Movie, error) {
	ids := normalizeList(input.IDs)
	if len(ids) == 0 || len(ids) > bulkIDLimit {
		return nil, fmt.Errorf("%w: provide between 1 and %d movie IDs", ErrInvalid, bulkIDLimit)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	profileID := ""
	if input.ProfileID != nil {
		profileID = strings.TrimSpace(*input.ProfileID)
		if profileID == "" {
			return nil, fmt.Errorf("%w: a quality profile is required", ErrInvalid)
		}
		if _, err := s.requireProfileByID(ctx, profileID); err != nil {
			return nil, err
		}
	}
	rootID := ""
	if input.RootID != nil {
		rootID = strings.TrimSpace(*input.RootID)
		if _, ok := rootByID(cfg, rootID); !ok {
			return nil, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
	}
	// Validate every ID before writing so a missing movie cannot leave a partial batch behind.
	for _, id := range ids {
		if _, err := s.Store.Get(ctx, id); err != nil {
			return nil, fmt.Errorf("movies: %w: %s", err, id)
		}
	}
	fields := map[string]any{}
	if input.Monitored != nil {
		fields["monitored"] = *input.Monitored
	}
	if input.ProfileID != nil {
		fields["profileId"] = profileID
	}
	if input.RootID != nil {
		fields["rootId"] = rootID
	}
	if input.Tags != nil {
		fields["tags"] = normalizeList(*input.Tags)
	}
	if input.Collection != nil {
		fields["collection"] = strings.TrimSpace(*input.Collection)
	}
	tx, err := s.Store.pool.Begin(ctx)
	if err != nil {
		return nil, errors.New("movies: bulk update could not start")
	}
	defer tx.Rollback(ctx)
	movies := make([]Movie, 0, len(ids))
	for _, id := range ids {
		movie, err := patchMovie(ctx, tx, id, fields)
		if err != nil {
			return nil, fmt.Errorf("movies: %w: %s", err, id)
		}
		movies = append(movies, movie)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errors.New("movies: bulk update failed")
	}
	return s.decorate(ctx, movies)
}

func (s *Service) Discover(ctx context.Context, q string, page int) ([]metadata.Title, error) {
	query := strings.TrimSpace(q)
	if query == "" || utf8.RuneCountInString(query) > metadataQueryLimit {
		return nil, fmt.Errorf("%w: enter a movie title up to %d characters", ErrInvalid, metadataQueryLimit)
	}
	if page < 1 || page > metadataPageLimit {
		return nil, fmt.Errorf("%w: search page must be between 1 and %d", ErrInvalid, metadataPageLimit)
	}
	client, err := s.metadataClient(ctx)
	if err != nil {
		return nil, err
	}
	titles, err := client.Search(ctx, query, page)
	if err != nil {
		return nil, fmt.Errorf("movies: %w", err)
	}
	if titles == nil {
		titles = []metadata.Title{}
	}
	return titles, nil
}

func (s *Service) Search(ctx context.Context, id string) ([]Release, error) {
	movie, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	profile, err := s.requireProfile(ctx, movie)
	if err != nil {
		return nil, err
	}
	releases, err := s.Downloads.SearchMovie(ctx, movie.Metadata.IMDbID, movie.Metadata.Title, movie.Metadata.Year)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	blocked, err := s.blockedReleases(ctx, movie.ID)
	if err != nil {
		return nil, err
	}
	acquisitions, err := s.acquisitionsFor(ctx, movie.ID)
	if err != nil {
		return nil, err
	}
	current := bestCurrent(profile, presentFiles(cfg, movie.Files))
	pending := pendingAcquisition(acquisitions)
	results := make([]Release, 0, len(releases))
	for _, release := range releases {
		item := Release{Release: release, Decision: quality.Evaluate(profile, release.Title, release.Size, current)}
		reasons := make([]string, 0, 2)
		if !releaseMatches(movie, release) {
			reasons = append(reasons, "release does not match this movie")
		}
		if blocked[release.ID] {
			reasons = append(reasons, "release is blocked")
		}
		if reason := duplicateReason(acquisitions, release.ID); reason != "" {
			reasons = append(reasons, reason)
		} else if pending != nil {
			reasons = append(reasons, "a download for this movie is already in progress")
		}
		if len(reasons) > 0 {
			item.Decision.Allowed = false
			item.Decision.Reasons = append(item.Decision.Reasons, reasons...)
		}
		results = append(results, item)
	}
	sortReleases(results)
	return results, nil
}

func (s *Service) acquisitionsFor(ctx context.Context, movieID string) ([]Acquisition, error) {
	all, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return nil, err
	}
	acquisitions := make([]Acquisition, 0, 2)
	for _, acquisition := range all {
		if acquisition.MovieID == movieID {
			acquisitions = append(acquisitions, acquisition)
		}
	}
	return acquisitions, nil
}

func duplicateReason(acquisitions []Acquisition, releaseID string) string {
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
		case "superseded":
			// An abandoned release can be grabbed again on purpose.
			continue
		default:
			return "release is already downloading"
		}
	}
	return ""
}

func pendingAcquisition(acquisitions []Acquisition) *Acquisition {
	for i := range acquisitions {
		switch acquisitions[i].Status {
		case "queued", "downloading", "importing", "import-failed", "":
			return &acquisitions[i]
		}
	}
	return nil
}

func scanTitleKey(title string) string {
	fields := strings.Fields(title)
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		switch strings.ToLower(strings.Trim(field, ".-_")) {
		case "imdb", "imdbid":
			continue
		}
		kept = append(kept, field)
	}
	return normalizeReleaseTitle(strings.Join(kept, " "))
}

func (s *Service) blockedReleases(ctx context.Context, movieID string) (map[string]bool, error) {
	rows, err := s.Store.pool.Query(ctx, `SELECT release_id FROM movie_blocklist WHERE movie_id = $1`, movieID)
	if err != nil {
		return nil, errors.New("movies: blocked releases could not be loaded")
	}
	defer rows.Close()
	blocked := map[string]bool{}
	for rows.Next() {
		var releaseID string
		if err := rows.Scan(&releaseID); err != nil {
			return nil, errors.New("movies: blocked releases could not be loaded")
		}
		blocked[releaseID] = true
	}
	return blocked, rows.Err()
}

func releaseMatches(movie Movie, release indexer.Release) bool {
	return rssMatches(movie, release)
}

func findRelease(releases []indexer.Release, releaseID string) (indexer.Release, bool) {
	for i := range releases {
		if releases[i].ID == releaseID {
			return releases[i], true
		}
	}
	return indexer.Release{}, false
}

func sortReleases(releases []Release) {
	sort.SliceStable(releases, func(i, j int) bool {
		a, b := releases[i].Decision, releases[j].Decision
		if (a.Rank < 0) != (b.Rank < 0) {
			return b.Rank < 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return releases[i].Published.After(releases[j].Published)
	})
}

func (s *Service) Grab(ctx context.Context, id, releaseID string, override bool) (downloads.Job, error) {
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" || len(releaseID) > releaseIDLimit {
		return downloads.Job{}, fmt.Errorf("%w: a release ID is required", ErrInvalid)
	}
	var job downloads.Job
	err := s.withMovieLock(ctx, id, func(ctx context.Context) error {
		movie, err := s.Store.Get(ctx, id)
		if err != nil {
			return err
		}
		profile, err := s.requireProfile(ctx, movie)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		releases, err := s.Downloads.SearchMovie(ctx, movie.Metadata.IMDbID, movie.Metadata.Title, movie.Metadata.Year)
		if err != nil {
			return err
		}
		chosen, found := findRelease(releases, releaseID)
		if !found {
			// Scheduler RSS grabs may not appear in a per-movie search; the feed is still fresh evidence.
			if feed, err := s.Downloads.RSS(ctx); err == nil {
				chosen, found = findRelease(feed, releaseID)
			}
		}
		if !found {
			return fmt.Errorf("%w: release does not appear in a fresh search", ErrInvalid)
		}
		if !releaseMatches(movie, chosen) {
			return fmt.Errorf("%w: release does not match this movie", ErrInvalid)
		}
		decision := quality.Evaluate(profile, chosen.Title, chosen.Size, bestCurrent(profile, presentFiles(cfg, movie.Files)))
		blocked, err := s.Store.Blocked(ctx, movie.ID, releaseID)
		if err != nil {
			return err
		}
		acquisitions, err := s.acquisitionsFor(ctx, movie.ID)
		if err != nil {
			return err
		}
		var abandoned []Acquisition
		for _, acquisition := range acquisitions {
			if acquisition.ReleaseID == releaseID {
				continue
			}
			switch acquisition.Status {
			case "queued", "downloading", "importing", "":
				return fmt.Errorf("%w: another release for this movie is already being processed", ErrConflict)
			case "import-failed":
				if !override {
					return fmt.Errorf("%w: another release for this movie is already being processed", ErrConflict)
				}
				abandoned = append(abandoned, acquisition)
			}
		}
		for _, acquisition := range acquisitions {
			if acquisition.ReleaseID != releaseID {
				continue
			}
			switch acquisition.Status {
			case "superseded":
				continue
			case "failed":
				if override {
					continue
				}
			}
			return fmt.Errorf("%w: this release is already being processed", ErrConflict)
		}
		if blocked && !override {
			return fmt.Errorf("%w: this release is blocked", ErrConflict)
		}
		if !decision.Allowed && !override {
			return fmt.Errorf("%w: %s", ErrConflict, strings.Join(decision.Reasons, "; "))
		}
		// Abandon stuck imports before the replacement so the scheduler cannot import them later.
		for _, acquisition := range abandoned {
			if err := s.Store.SaveAcquisition(ctx, Acquisition{
				MovieID: movie.ID, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
				Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "superseded",
				Error: "superseded by a newer release grab",
			}); err != nil {
				return err
			}
		}
		job, err = s.Downloads.Add(ctx, releaseID, chosen.Title)
		if err != nil {
			return err
		}
		if job.Status == "failed" {
			if !override {
				return fmt.Errorf("%w: the download failed before; retry it with override", ErrConflict)
			}
			job, err = s.Downloads.Retry(ctx, job.ID)
			if err != nil {
				return err
			}
		}
		if err := s.Store.SaveAcquisition(ctx, Acquisition{
			MovieID: movie.ID, JobID: job.ID, ReleaseID: releaseID, Title: chosen.Title,
			Decision: decision, Override: override, Status: "queued",
		}); err != nil {
			return err
		}
		_ = s.Store.Event(ctx, movie.ID, "grabbed", truncate("Grabbed "+chosen.Title, maxTextRunes))
		return nil
	})
	if err != nil {
		return downloads.Job{}, err
	}
	return job, nil
}

func (s *Service) withMovieLock(ctx context.Context, movieID string, fn func(context.Context) error) error {
	// Keep pooled connections available for queries while operation locks hold their sessions.
	select {
	case s.lockSlots <- struct{}{}:
		defer func() { <-s.lockSlots }()
	default:
		return fmt.Errorf("%w: movie operations are busy; try again shortly", ErrConflict)
	}
	conn, err := s.Store.pool.Acquire(ctx)
	if err != nil {
		return errors.New("movies: movie operation cannot reach the database")
	}
	defer conn.Release()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, movieLockKey(movieID)).Scan(&held); err != nil {
		return errors.New("movies: movie operation cannot acquire its lock")
	}
	if !held {
		return fmt.Errorf("%w: another operation for this movie is already running", ErrConflict)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, movieLockKey(movieID)); err != nil {
			raw := conn.Hijack()
			_ = raw.Close(unlockCtx)
		}
	}()
	return fn(ctx)
}

func movieLockKey(movieID string) int64 {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte(movieID))
	return movieLockBase ^ int64(digest.Sum64())
}

func (s *Service) Refresh(ctx context.Context, id string) (Movie, error) {
	movie, err := s.Store.Get(ctx, id)
	if err != nil {
		return Movie{}, err
	}
	if movie.Metadata.IMDbID == "" {
		return Movie{}, fmt.Errorf("%w: this movie has no IMDb ID to refresh", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Movie{}, err
	}
	if !s.metadataConfigured(cfg) {
		return Movie{}, fmt.Errorf("movies: OMDb API key is not configured: %w", ErrNotConfigured)
	}
	title, err := s.lookup(ctx, cfg, movie.Metadata.IMDbID)
	if err != nil {
		return Movie{}, err
	}
	saved, err := s.Store.Patch(ctx, id, map[string]any{"metadata": title})
	if err != nil {
		return Movie{}, err
	}
	_ = s.Store.Event(ctx, saved.ID, "refreshed", truncate("Metadata refreshed from OMDb", maxTextRunes))
	return s.decorated(ctx, saved)
}

func (s *Service) Rename(ctx context.Context, id string, preview bool) (RenameResult, error) {
	if preview {
		movie, err := s.Store.Get(ctx, id)
		if err != nil {
			return RenameResult{}, err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return RenameResult{}, err
		}
		result, err := s.renameFiles(ctx, cfg, movie, false)
		return result, err
	}
	var result RenameResult
	err := s.withMovieLock(ctx, id, func(ctx context.Context) error {
		movie, err := s.Store.Get(ctx, id)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		result, err = s.renameFiles(ctx, cfg, movie, true)
		return err
	})
	if err != nil {
		return result, err
	}
	if len(result.Files) > 0 {
		result.Applied = true
		_ = s.Store.Event(ctx, id, "renamed", truncate("Renamed "+strconv.Itoa(len(result.Files))+" file(s)", maxTextRunes))
	}
	return result, nil
}

func (s *Service) renameFiles(ctx context.Context, cfg Config, movie Movie, apply bool) (RenameResult, error) {
	result := RenameResult{Files: make([]RenameFile, 0, len(movie.Files))}
	files := append([]File{}, movie.Files...)
	if len(files) == 0 {
		return result, nil
	}
	for _, root := range cfg.RootFolders {
		present := make([]File, 0, len(files))
		for _, file := range files {
			if file.RootID == root.ID && filePresent(cfg, file) {
				present = append(present, file)
			}
		}
		if len(present) == 0 {
			continue
		}
		sources := make([]library.Source, 0, len(present))
		// Multipart siblings share one release quality, so the first stored quality renders the group.
		qualityName := ""
		for _, file := range present {
			sources = append(sources, library.Source{Name: file.Path})
			if qualityName == "" {
				qualityName = file.Quality
			}
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
		opts := s.importOptions(cfg, movie, root, root.Path, qualityName)
		var outputs []library.File
		if apply {
			published, err := library.Import(ctx, opts, sources)
			if err != nil {
				return result, fmt.Errorf("movies: rename: %w", err)
			}
			if len(published) != len(sources) {
				return result, errors.New("movies: rename published an unexpected file set")
			}
			outputs = published
		} else {
			planned, err := library.Preview(opts, sources)
			if err != nil {
				return result, fmt.Errorf("movies: rename: %w", err)
			}
			if len(planned) != len(sources) {
				return result, errors.New("movies: rename plan does not match the source files")
			}
			outputs = planned
		}
		for i, source := range sources {
			result.Files = append(result.Files, RenameFile{From: source.Name, To: outputs[i].Path})
			if !apply || source.Name == outputs[i].Path {
				continue
			}
			s.moveJournalPath(ctx, root.Path, source.Name, outputs[i].Path)
			for j := range files {
				if files[j].RootID == root.ID && files[j].Path == source.Name {
					files[j].Path = outputs[i].Path
					files[j].Size = outputs[i].Size
					break
				}
			}
		}
		if apply {
			if _, err := s.Store.Patch(ctx, movie.ID, map[string]any{"files": files, "error": ""}); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (s *Service) moveJournalPath(ctx context.Context, rootPath, from, to string) {
	_, _ = s.Store.pool.Exec(ctx,
		`UPDATE download_library_files SET path = $3 WHERE ready AND root_path = $1 AND path = $2`,
		rootPath, from, to)
}

func (s *Service) Import(ctx context.Context, input ImportInput) (Movie, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Movie{}, err
	}
	sourceRoot, ok := rootByID(cfg, strings.TrimSpace(input.RootID))
	if !ok {
		return Movie{}, fmt.Errorf("%w: source root folder does not exist", ErrInvalid)
	}
	path := strings.TrimSpace(input.Path)
	if _, episodic := library.ParseEpisode(path); episodic {
		return Movie{}, fmt.Errorf("%w: import episode files from TV Shows", ErrInvalid)
	}
	if path == "" || len(path) > maxPathBytes {
		return Movie{}, fmt.Errorf("%w: a movie file path is required", ErrInvalid)
	}
	if handle, err := library.Open(sourceRoot.Path, path); err != nil {
		return Movie{}, fmt.Errorf("%w: the movie file is not available below the source root", ErrInvalid)
	} else {
		info, statErr := handle.Stat()
		handle.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return Movie{}, fmt.Errorf("%w: the movie file is not usable", ErrInvalid)
		}
	}
	movie, err := s.matchImportMovie(ctx, cfg, input, sourceRoot, path)
	if err != nil {
		return Movie{}, err
	}
	var saved Movie
	err = s.withMovieLock(ctx, movie.ID, func(ctx context.Context) error {
		// Re-read under the lock so files imported meanwhile are merged instead of overwritten.
		fresh, err := s.Store.Get(ctx, movie.ID)
		if err != nil {
			return err
		}
		var importErr error
		saved, importErr = s.importSources(ctx, cfg, fresh, sourceRoot.Path, []library.Source{{Name: path}}, "", "")
		return importErr
	})
	if err != nil {
		return Movie{}, err
	}
	return saved, nil
}

func (s *Service) matchImportMovie(ctx context.Context, cfg Config, input ImportInput, sourceRoot RootFolder, path string) (Movie, error) {
	if movieID := strings.TrimSpace(input.MovieID); movieID != "" {
		return s.Store.Get(ctx, movieID)
	}
	imdbID := strings.ToLower(strings.TrimSpace(input.IMDbID))
	if imdbID != "" {
		if !metadata.ValidIMDbID(imdbID) {
			return Movie{}, fmt.Errorf("%w: IMDb ID must look like tt1234567", ErrInvalid)
		}
		if movie, err := s.Store.FindIMDb(ctx, imdbID); err == nil {
			return movie, nil
		} else if !errors.Is(err, ErrNotFound) {
			return Movie{}, err
		}
		return s.Add(ctx, AddInput{IMDbID: imdbID, RootID: sourceRoot.ID})
	}
	candidate, err := matchCandidate(ctx, sourceRoot, path)
	if err != nil {
		return Movie{}, err
	}
	if candidate == nil {
		return Movie{}, fmt.Errorf("%w: select a movie for this file", ErrInvalid)
	}
	if candidate.IMDbID != "" {
		if movie, err := s.Store.FindIMDb(ctx, strings.ToLower(candidate.IMDbID)); err == nil {
			return movie, nil
		} else if !errors.Is(err, ErrNotFound) {
			return Movie{}, err
		}
	}
	if movie, err := s.matchCatalogTitle(ctx, candidate.Title, candidate.Year); err != nil {
		return Movie{}, err
	} else if movie != nil {
		return *movie, nil
	}
	return Movie{}, fmt.Errorf("%w: select a movie for this file", ErrInvalid)
}

func matchCandidate(ctx context.Context, sourceRoot RootFolder, path string) (*library.Candidate, error) {
	candidates, err := library.Scan(ctx, sourceRoot.Path)
	if err != nil {
		return nil, fmt.Errorf("movies: scan source root: %w", err)
	}
	group := importGroup(path)
	for i := range candidates {
		if strings.EqualFold(candidates[i].Path, path) || importGroup(candidates[i].Path) == group {
			return &candidates[i], nil
		}
	}
	return nil, nil
}

func (s *Service) matchCatalogTitle(ctx context.Context, title string, year int) (*Movie, error) {
	key := scanTitleKey(title)
	if key == "" || year <= 0 {
		return nil, nil
	}
	movies, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var matched *Movie
	for i := range movies {
		if normalizeReleaseTitle(movies[i].Metadata.Title) != key || movies[i].Metadata.Year != year {
			continue
		}
		if matched != nil {
			return nil, nil // ambiguous identities stay unmatched
		}
		matched = &movies[i]
	}
	return matched, nil
}

func (s *Service) Calendar(ctx context.Context) ([]Movie, error) {
	movies, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	type dated struct {
		movie Movie
		date  time.Time
	}
	upcoming := make([]dated, 0, len(movies))
	for _, movie := range movies {
		date, ok := parseReleasedDate(movie.Metadata.Released)
		if !ok || date.Before(today) {
			continue
		}
		upcoming = append(upcoming, dated{movie: movie, date: date})
	}
	sort.SliceStable(upcoming, func(i, j int) bool {
		if !upcoming[i].date.Equal(upcoming[j].date) {
			return upcoming[i].date.Before(upcoming[j].date)
		}
		return upcoming[i].movie.ID < upcoming[j].movie.ID
	})
	calendar := make([]Movie, 0, len(upcoming))
	for _, item := range upcoming {
		calendar = append(calendar, item.movie)
	}
	return calendar, nil
}

func (s *Service) Scan(ctx context.Context, rootID string) ([]Candidate, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	root, ok := rootByID(cfg, strings.TrimSpace(rootID))
	if !ok {
		return nil, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
	}
	candidates, err := library.Scan(ctx, root.Path)
	if err != nil {
		return nil, fmt.Errorf("movies: scan library root: %w", err)
	}
	movies, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	byIMDb := map[string]string{}
	byTitleYear := map[string][]string{}
	byPath := map[string]string{}
	for _, movie := range movies {
		if movie.Metadata.IMDbID != "" {
			byIMDb[strings.ToLower(movie.Metadata.IMDbID)] = movie.ID
		}
		if movie.Metadata.Title != "" && movie.Metadata.Year > 0 {
			key := normalizeReleaseTitle(movie.Metadata.Title) + strconv.Itoa(movie.Metadata.Year)
			byTitleYear[key] = append(byTitleYear[key], movie.ID)
		}
		for _, file := range movie.Files {
			if file.RootID == root.ID {
				byPath[file.Path] = movie.ID
			}
		}
	}
	results := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, episodic := library.ParseEpisode(candidate.Path); episodic {
			continue
		}
		item := Candidate{Candidate: candidate}
		if candidate.IMDbID != "" {
			item.MatchedMovieID = byIMDb[strings.ToLower(candidate.IMDbID)]
		}
		if item.MatchedMovieID == "" && candidate.Title != "" && candidate.Year > 0 {
			key := scanTitleKey(candidate.Title) + strconv.Itoa(candidate.Year)
			if ids := byTitleYear[key]; len(ids) == 1 {
				item.MatchedMovieID = ids[0]
			}
		}
		if owner, ok := byPath[candidate.Path]; ok {
			item.MatchedMovieID = owner
			item.Error = "file is already in the library"
		}
		results = append(results, item)
	}
	return results, nil
}

func (s *Service) OpenFile(ctx context.Context, id, path string) (*os.File, error) {
	movie, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	want := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	for _, file := range movie.Files {
		if filepath.ToSlash(filepath.Clean(file.Path)) != want {
			continue
		}
		root, ok := rootByID(cfg, file.RootID)
		if !ok {
			break
		}
		handle, err := library.Open(root.Path, file.Path)
		if err != nil {
			break
		}
		return handle, nil
	}
	return nil, ErrNotFound
}

func (s *Service) ConfigView(ctx context.Context) (Config, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	return configView(cfg), nil
}

func configView(cfg Config) Config {
	metadataConfigured := cfg.MetadataURL != "" && strings.TrimSpace(cfg.MetadataAPIKey) != ""
	jellyfinConfigured := cfg.JellyfinURL != "" && strings.TrimSpace(cfg.JellyfinAPIKey) != ""
	cfg.MetadataAPIKey = ""
	cfg.JellyfinAPIKey = ""
	cfg.MetadataConfigured = metadataConfigured
	cfg.JellyfinConfigured = jellyfinConfigured
	return cfg
}

func (s *Service) SetConfig(ctx context.Context, input Config) (Config, error) {
	current, err := s.Store.Config(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Config{}, err
	}
	input = normalizeConfigInput(input)
	if err := validateConfig(input); err != nil {
		return Config{}, err
	}
	if err := s.checkRootChanges(ctx, current, input); err != nil {
		return Config{}, err
	}
	if err := ensureRoots(input.RootFolders); err != nil {
		return Config{}, err
	}
	saved, err := s.Store.SaveConfig(ctx, input)
	if err != nil {
		return Config{}, err
	}
	return configView(saved), nil
}

func normalizeConfigInput(cfg Config) Config {
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	cfg.FolderTemplate = strings.TrimSpace(cfg.FolderTemplate)
	cfg.FileTemplate = strings.TrimSpace(cfg.FileTemplate)
	cfg.ImportMode = strings.ToLower(strings.TrimSpace(cfg.ImportMode))
	cfg.MinimumAvailability = strings.TrimSpace(cfg.MinimumAvailability)
	cfg.MetadataURL = strings.TrimSpace(cfg.MetadataURL)
	cfg.JellyfinURL = strings.TrimSpace(cfg.JellyfinURL)
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	return cfg
}

func (s *Service) checkRootChanges(ctx context.Context, current, next Config) error {
	kept := make(map[string]string, len(next.RootFolders))
	for _, root := range next.RootFolders {
		kept[strings.TrimSpace(root.ID)] = strings.TrimSpace(root.Path)
	}
	changed := make(map[string]bool)
	paths := make([]string, 0, len(current.RootFolders))
	for _, root := range current.RootFolders {
		if path, ok := kept[root.ID]; ok && path == root.Path {
			continue
		}
		changed[root.ID] = true
		paths = append(paths, root.Path)
	}
	if len(changed) == 0 {
		return nil
	}
	movies, err := s.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, movie := range movies {
		for _, file := range movie.Files {
			if changed[file.RootID] {
				return fmt.Errorf("%w: root folder is used by existing movie files", ErrConflict)
			}
		}
	}
	for _, path := range paths {
		var imported bool
		if err := s.Store.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM download_library_files WHERE ready AND root_path = $1)`, path).Scan(&imported); err != nil {
			return errors.New("movies: root folder usage could not be checked")
		}
		if imported {
			return fmt.Errorf("%w: root folder contains imported download files", ErrConflict)
		}
	}
	return nil
}

func ensureRoots(roots []RootFolder) error {
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
