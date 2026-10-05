package tv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

// missingEpisodeError marks a requested episode whose release arrived without it; it stays wanted.
const missingEpisodeError = "the download did not include this episode; it stays wanted"

var (
	episodeVideoExtensions = map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".m4v": true,
		".webm": true, ".mpeg": true, ".mpg": true, ".ts": true, ".wmv": true,
	}
	opaqueEpisodeName    = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
	episodeSamplePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(sample|samples|trailer|trailers)([^a-z0-9]|$)`)
)

type importQuality struct {
	Title   string
	Quality string
	Score   int
}

// importSource is one download output file and the catalog episodes it holds.
type importSource struct {
	library.Source
	EpisodeIDs []string
}

type evaluatedFile struct {
	file    library.File
	quality string
	score   int
}

type journalFile struct {
	Name     string
	RootPath string
	Path     string
	Size     int64
	SHA256   string
	Ready    bool
}

type importPayload struct {
	Event      string   `json:"event"`
	SeriesID   string   `json:"seriesId"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	IMDbID     string   `json:"imdbId,omitempty"`
	EpisodeIDs []string `json:"episodeIds"`
	Files      []string `json:"files"`
}

// SyncDownloads imports completed jobs and adopts catalog-matched legacy downloads.
func (s *Service) SyncDownloads(ctx context.Context) (int, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return 0, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return 0, err
	}
	imported := 0
	var problems []error
	for _, acquisition := range acquisitions {
		count, err := s.syncAcquisition(ctx, cfg, acquisition)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		imported += count
	}
	legacy, err := s.syncLegacy(ctx, cfg)
	if err != nil {
		problems = append(problems, err)
	}
	imported += legacy
	return imported, errors.Join(problems...)
}

func (s *Service) syncAcquisition(ctx context.Context, cfg Config, acquisition Acquisition) (int, error) {
	switch acquisition.Status {
	case "imported", "superseded":
		return 0, nil
	}
	if _, err := s.Store.Get(ctx, acquisition.SeriesID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	job, err := s.Downloads.Get(ctx, acquisition.JobID)
	if errors.Is(err, downloads.ErrNotFound) {
		if acquisition.Status == "failed" {
			return 0, nil
		}
		return 0, s.failAcquisition(ctx, acquisition, "the download no longer exists")
	}
	if err != nil {
		return 0, err
	}
	switch job.Status {
	case "completed":
		return s.runImport(ctx, cfg, acquisition.SeriesID, job, acquisition, nil)
	case "failed":
		if acquisition.Status == "failed" {
			return 0, nil
		}
		return 0, s.failAcquisition(ctx, acquisition, firstText(job.Error, "the download failed"))
	default:
		return 0, nil
	}
}

func (s *Service) runImport(ctx context.Context, cfg Config, seriesID string, job downloads.Job, acquisition Acquisition, narrowed []library.Source) (int, error) {
	imported := 0
	var notifySeries Series
	var notifyEpisodes, notifyPaths []string
	err := s.withSeriesLock(ctx, seriesID, func(ctx context.Context) error {
		fresh, err := s.Store.Get(ctx, seriesID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		sources, unmapped, err := s.importSourcesFor(ctx, fresh, job, acquisition, narrowed)
		if err != nil {
			return err
		}
		sources, needed, reason, err := s.importableSources(ctx, cfg, fresh, acquisition, sources)
		if err != nil {
			return err
		}
		gap := s.gapEpisodes(cfg, unmapped)
		if len(sources) == 0 && len(gap) == 0 {
			return s.markSuperseded(ctx, fresh, acquisition, reason)
		}
		saved, episodes, paths, err := s.importJob(ctx, cfg, fresh, job, acquisition, sources, needed, gap, "")
		if err != nil {
			return err
		}
		notifySeries, notifyEpisodes, notifyPaths = saved, episodes, paths
		imported = 1
		return nil
	})
	if errors.Is(err, ErrConflict) {
		// Another process is importing this series right now.
		return 0, nil
	}
	if err != nil {
		return 0, s.recordImportFailure(ctx, acquisition, err)
	}
	if len(notifyPaths) > 0 {
		s.notifyImported(ctx, notifySeries, notifyEpisodes, notifyPaths)
	}
	return imported, nil
}

// importSourcesFor maps every download output video onto the acquisition's requested episodes.
func (s *Service) importSourcesFor(ctx context.Context, series Series, job downloads.Job, acquisition Acquisition, narrowed []library.Source) ([]importSource, []Episode, error) {
	raw := narrowed
	if raw == nil {
		raw = make([]library.Source, 0, len(job.Files))
		for _, file := range job.Files {
			raw = append(raw, library.Source{Name: file.Name, Size: file.Size})
		}
	}
	raw = episodeSources(raw)
	if len(raw) == 0 && job.ID != "" {
		// Older jobs may have no stored file list; the download output still enumerates sources.
		if directory, err := s.Downloads.OutputDirectory(job.ID); err == nil {
			if candidates, err := library.ScanTV(ctx, directory); err == nil {
				for _, candidate := range candidates {
					raw = append(raw, library.Source{Name: candidate.Path, Size: candidate.Size})
				}
				raw = episodeSources(raw)
			}
		}
	}
	if len(raw) == 0 {
		return nil, nil, errors.New("tv: the download contains no episode video")
	}
	catalog, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, nil, err
	}
	requested := map[string]bool{}
	for _, id := range acquisition.EpisodeIDs {
		if id = strings.TrimSpace(id); id != "" {
			requested[id] = true
		}
	}
	if len(requested) == 0 {
		return nil, nil, errors.New("tv: the download has no requested episodes")
	}
	sources := make([]importSource, 0, len(raw))
	mapped := map[string]bool{}
	for _, source := range raw {
		identity, ok := sourceIdentity(source.Name)
		// One opaque video can inherit a strictly matched single-episode release identity.
		if !ok && len(raw) == 1 && len(requested) == 1 && job.ID != "" && opaqueEpisodeName.MatchString(strings.TrimSuffix(path.Base(source.Name), path.Ext(source.Name))) {
			candidate, parsed := library.ParseEpisode(job.Title)
			if parsed && !candidate.Pack && (len(candidate.Numbers) == 1 || candidate.AirDate != "") {
				identity, ok = candidate, true
			}
		}
		if !ok {
			return nil, nil, fmt.Errorf("tv: %s does not carry episode numbering; run a manual import with the season and episodes", source.Name)
		}
		if !seriesMatches(series, identity, job.Title) {
			return nil, nil, fmt.Errorf("tv: %s does not match %s; run a manual import to match it", source.Name, tvSeriesLabel(series))
		}
		episodeIDs, err := mapEpisodes(catalog, requested, identity, source.Name)
		if err != nil {
			return nil, nil, err
		}
		sources = append(sources, importSource{Source: source, EpisodeIDs: episodeIDs})
		for _, id := range episodeIDs {
			mapped[id] = true
		}
	}
	var unmapped []Episode
	for _, episode := range catalog {
		if requested[episode.ID] && !mapped[episode.ID] {
			unmapped = append(unmapped, episode)
		}
	}
	return sources, unmapped, nil
}

// sourceIdentity parses a file name, falling back to the containing folder for the series name.
func sourceIdentity(name string) (library.EpisodeIdentity, bool) {
	identity, ok := library.ParseEpisode(name)
	if !ok {
		return library.EpisodeIdentity{}, false
	}
	if identity.Title == "" {
		if dir := path.Dir(filepath.ToSlash(strings.TrimSpace(name))); dir != "." && dir != "/" {
			if parent, parentOK := library.ParseEpisode(dir); parentOK {
				identity.Title = parent.Title
				if identity.Year == 0 {
					identity.Year = parent.Year
				}
			}
		}
	}
	return identity, true
}

// seriesMatches requires a full normalized title match; a differing parsed title is never overridden.
func seriesMatches(series Series, identity library.EpisodeIdentity, jobTitle string) bool {
	key := normalizeTVTitle(series.Metadata.Title)
	if key == "" {
		return false
	}
	if identity.Title != "" {
		if normalizeTVTitle(identity.Title) != key {
			return false
		}
		return identity.Year == 0 || series.Metadata.Year == 0 || identity.Year == series.Metadata.Year
	}
	parsed, ok := library.ParseEpisode(jobTitle)
	if !ok || normalizeTVTitle(parsed.Title) != key {
		return false
	}
	return parsed.Year == 0 || series.Metadata.Year == 0 || parsed.Year == series.Metadata.Year
}

// mapEpisodes maps one file's parsed numbering onto requested catalog episodes.
func mapEpisodes(catalog []Episode, requested map[string]bool, identity library.EpisodeIdentity, name string) ([]string, error) {
	if identity.AirDate != "" {
		matched := ""
		for _, episode := range catalog {
			if episode.AirDate != identity.AirDate || !requested[episode.ID] {
				continue
			}
			if matched != "" {
				return nil, fmt.Errorf("tv: %s matches more than one requested episode; run a manual import", name)
			}
			matched = episode.ID
		}
		if matched == "" {
			return nil, fmt.Errorf("tv: %s has air date %s which is not a requested episode", name, identity.AirDate)
		}
		return []string{matched}, nil
	}
	if len(identity.Numbers) == 0 {
		// A pack name describes the release, not a single video holding a whole season.
		return nil, fmt.Errorf("tv: %s names a season pack but is a single video; run a manual import with episode numbers", name)
	}
	ids := make([]string, 0, len(identity.Numbers))
	for _, number := range identity.Numbers {
		id := ""
		for _, episode := range catalog {
			if episode.Season == identity.Season && episode.Number == number {
				id = episode.ID
				break
			}
		}
		if id == "" {
			return nil, fmt.Errorf("tv: %s names episode %d but it is not in the series catalog", name, number)
		}
		if !requested[id] {
			return nil, fmt.Errorf("tv: %s names an episode that was not requested; run a manual import", name)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func episodeSources(sources []library.Source) []library.Source {
	filtered := make([]library.Source, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		name := filepath.ToSlash(strings.TrimSpace(source.Name))
		if name == "" || seen[name] || !isEpisodeVideo(name) {
			continue
		}
		seen[name] = true
		filtered = append(filtered, library.Source{Name: name, Size: source.Size})
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
	return filtered
}

func isEpisodeVideo(name string) bool {
	if !episodeVideoExtensions[strings.ToLower(path.Ext(name))] {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if episodeSamplePattern.MatchString(strings.TrimSuffix(part, path.Ext(part))) {
			return false
		}
	}
	return true
}

// importableSources drops sources whose episodes already satisfy the profile and reports the replaceable episodes.
func (s *Service) importableSources(ctx context.Context, cfg Config, series Series, acquisition Acquisition, sources []importSource) ([]importSource, map[string]bool, string, error) {
	if acquisition.Override {
		needed := make(map[string]bool)
		for _, source := range sources {
			for _, id := range source.EpisodeIDs {
				needed[id] = true
			}
		}
		return sources, needed, "", nil
	}
	profile, err := s.Shared.Profile(ctx, series.ProfileID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("tv: the quality profile is not available: %w", err)
	}
	catalog, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, nil, "", err
	}
	byID := make(map[string]Episode, len(catalog))
	for _, episode := range catalog {
		byID[episode.ID] = episode
	}
	kept := make([]importSource, 0, len(sources))
	needed := map[string]bool{}
	var reasons []string
	for _, source := range sources {
		title := firstText(acquisition.Title, source.Name)
		if quality.Parse(title).Quality == "" {
			title += " " + acquisition.Decision.Details.Quality
		}
		sourceNeeds := false
		for _, id := range source.EpisodeIDs {
			episode, ok := byID[id]
			if !ok {
				needed[id] = true
				sourceNeeds = true
				continue
			}
			current := bestCurrent(profile, s.presentEpisodeFiles(cfg, episode.Files))
			if current == nil {
				needed[id] = true
				sourceNeeds = true
				continue
			}
			decision := quality.Evaluate(profile, title, source.Size/int64(max(1, len(source.EpisodeIDs))), current)
			if decision.Allowed {
				needed[id] = true
				sourceNeeds = true
				continue
			}
			if len(decision.Reasons) > 0 {
				reasons = append(reasons, decision.Reasons...)
			}
		}
		if sourceNeeds {
			kept = append(kept, source)
		}
	}
	if len(kept) > 0 {
		return kept, needed, "", nil
	}
	reason := "the existing episodes already satisfy the quality profile"
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	return nil, nil, reason, nil
}

// gapEpisodes keeps requested episodes left without any usable file.
func (s *Service) gapEpisodes(cfg Config, episodes []Episode) []Episode {
	gap := make([]Episode, 0, len(episodes))
	for _, episode := range episodes {
		if len(s.presentEpisodeFiles(cfg, episode.Files)) == 0 {
			gap = append(gap, episode)
		}
	}
	return gap
}

func (s *Service) importJob(ctx context.Context, cfg Config, series Series, job downloads.Job, acquisition Acquisition, sources []importSource, needed map[string]bool, gap []Episode, manualSourceRoot string) (Series, []string, []string, error) {
	destRoot, ok := rootFor(cfg, series.RootID)
	if !ok {
		return Series{}, nil, nil, fmt.Errorf("%w: the series root folder is not configured", ErrInvalid)
	}
	sourceRoot := manualSourceRoot
	if sourceRoot == "" {
		sourceRoot = destRoot.Path
	}
	if job.ID != "" {
		directory, err := s.Downloads.OutputDirectory(job.ID)
		if err != nil {
			return Series{}, nil, nil, errors.New("tv: the download output is not available")
		}
		sourceRoot = directory
	}
	profile, profileErr := s.Shared.Profile(ctx, series.ProfileID)
	if profileErr != nil {
		profile = quality.Profile{}
	}
	catalog, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return Series{}, nil, nil, err
	}
	byID := make(map[string]Episode, len(catalog))
	for _, episode := range catalog {
		byID[episode.ID] = episode
	}
	info := importQuality{Title: acquisition.Title, Quality: acquisition.Decision.Details.Quality, Score: acquisition.Decision.Score}
	journal := map[string]journalFile{}
	if job.ID != "" {
		journal, err = s.journalRows(ctx, job.ID)
		if err != nil {
			return Series{}, nil, nil, err
		}
	}
	published := make(map[string]library.File, len(sources))
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceSet := make(map[string]bool, len(source.EpisodeIDs))
		for _, id := range source.EpisodeIDs {
			if needed[id] {
				sourceSet[id] = true
			}
		}
		existing := replacementPaths(catalog, destRoot, needed, sourceSet)
		qualityName := quality.Parse(source.Name).Quality
		if qualityName == "" {
			qualityName = info.Quality
		}
		file, err := s.publishSource(ctx, cfg, series, destRoot, sourceRoot, job.ID, source, catalog, qualityName, existing, journal)
		if err != nil {
			return Series{}, nil, nil, err
		}
		published[source.Name] = file
		names = append(names, source.Name)
	}
	now := time.Now().UTC()
	paths := []string{}
	episodeIDs := []string{}
	for _, id := range sortedKeys(needed) {
		episode, ok := byID[id]
		if !ok {
			continue
		}
		added := make([]evaluatedFile, 0, len(sources))
		for _, source := range sources {
			file, ok := published[source.Name]
			if !ok || !tvContains(source.EpisodeIDs, id) {
				continue
			}
			added = append(added, evaluatedEpisodeFile(profile, info, source, file))
		}
		files := mergeEpisodeFiles(destRoot, episode.Files, added, now)
		if _, err := s.Store.PatchEpisode(ctx, id, map[string]any{"files": files, "error": ""}); err != nil {
			return Series{}, nil, nil, err
		}
		episodeIDs = append(episodeIDs, id)
		for _, item := range added {
			paths = append(paths, item.file.Path)
		}
	}
	// The journal turns ready only after every episode row is saved.
	if err := s.completeJournal(ctx, job.ID, names); err != nil {
		return Series{}, nil, nil, err
	}
	for _, episode := range gap {
		if _, err := s.Store.PatchEpisode(ctx, episode.ID, map[string]any{"error": missingEpisodeError}); err != nil {
			return Series{}, nil, nil, err
		}
		if strings.TrimSpace(episode.Error) != missingEpisodeError {
			_ = s.Store.Event(ctx, series.ID, episode.ID, "import-incomplete", truncate("The download did not include "+tvEpisodeLabel(episode)+"; the episode stays wanted", maxTextRunes))
		}
	}
	if len(gap) > 0 && acquisition.ReleaseID != "" {
		// Keep the incomplete release from being grabbed again; alternates stay allowed.
		if err := s.Store.Block(ctx, series.ID, acquisition.ReleaseID); err != nil && !errors.Is(err, ErrNotFound) {
			return Series{}, nil, nil, err
		}
	}
	if job.ID != "" {
		if err := s.saveAcquisitionStatus(ctx, Acquisition{
			SeriesID: series.ID, EpisodeIDs: acquisition.EpisodeIDs, JobID: job.ID, ReleaseID: acquisition.ReleaseID,
			Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "imported",
		}); err != nil {
			return Series{}, nil, nil, err
		}
	}
	if err := s.patchSeriesError(ctx, series.ID, ""); err != nil {
		return Series{}, nil, nil, err
	}
	if len(paths) > 0 {
		message := truncate("Imported "+strings.Join(uniqueStrings(paths), ", "), maxTextRunes)
		for _, id := range episodeIDs {
			_ = s.Store.Event(ctx, series.ID, id, "imported", message)
		}
	}
	saved, err := s.Store.Get(ctx, series.ID)
	if err != nil {
		return Series{}, nil, nil, err
	}
	return saved, episodeIDs, uniqueStrings(paths), nil
}

// publishSource imports one source file, recovering a moved destination through the journal.
func (s *Service) publishSource(ctx context.Context, cfg Config, series Series, destRoot movies.RootFolder, sourceRoot, jobID string, source importSource, catalog []Episode, qualityName string, existing []string, journal map[string]journalFile) (library.File, error) {
	opts := s.importOptions(cfg, series, destRoot, sourceRoot, episodeOption(catalog, source), qualityName, existing)
	if handle, err := library.Open(sourceRoot, source.Name); err == nil {
		info, statErr := handle.Stat()
		handle.Close()
		if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
			planned, err := library.Preview(opts, []library.Source{source.Source})
			if err != nil {
				return library.File{}, fmt.Errorf("tv: plan import: %w", err)
			}
			if len(planned) != 1 {
				return library.File{}, errors.New("tv: the import plan does not match the source file")
			}
			if err := s.planJournal(ctx, jobID, sourceRoot, destRoot.Path, []library.Source{source.Source}, planned); err != nil {
				return library.File{}, err
			}
			files, err := library.Import(ctx, opts, []library.Source{source.Source})
			if err != nil {
				return library.File{}, fmt.Errorf("tv: import files: %w", err)
			}
			if len(files) != 1 {
				return library.File{}, errors.New("tv: import published an unexpected file set")
			}
			return files[0], nil
		}
	}
	entry, ok := journal[source.Name]
	if !ok {
		return library.File{}, fmt.Errorf("tv: source file %s is missing", source.Name)
	}
	if entry.RootPath != destRoot.Path {
		return library.File{}, fmt.Errorf("tv: %s is missing after a move and its recorded destination changed", source.Name)
	}
	if !entry.Ready && !fileMatchesSHA256(ctx, entry.RootPath, entry.Path, entry.SHA256) {
		return library.File{}, fmt.Errorf("tv: %s is missing after a move and the recorded destination does not match its hash", source.Name)
	}
	handle, err := library.Open(entry.RootPath, entry.Path)
	if err != nil {
		return library.File{}, fmt.Errorf("tv: %s is missing after a move and its destination is gone", source.Name)
	}
	info, statErr := handle.Stat()
	handle.Close()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return library.File{}, fmt.Errorf("tv: %s is missing after a move and its destination is not usable", source.Name)
	}
	return library.File{Path: entry.Path, Size: entry.Size}, nil
}

// replacementPaths lists files below one root owned only by this source's replaced episodes.
func replacementPaths(catalog []Episode, root movies.RootFolder, replaced, sourceSet map[string]bool) []string {
	paths := []string{}
	seen := map[string]bool{}
	for _, episode := range catalog {
		if !sourceSet[episode.ID] {
			continue
		}
		for _, file := range episode.Files {
			if file.RootID != root.ID {
				continue
			}
			// A file shared with an episode outside this import must survive it.
			if sharedFilePath(catalog, replaced, file) {
				continue
			}
			if !seen[file.Path] {
				seen[file.Path] = true
				paths = append(paths, file.Path)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func sharedFilePath(catalog []Episode, replaced map[string]bool, file movies.File) bool {
	for _, episode := range catalog {
		if replaced[episode.ID] {
			continue
		}
		for _, owned := range episode.Files {
			if owned.RootID == file.RootID && owned.Path == file.Path {
				return true
			}
		}
	}
	return false
}

// episodeOption renders naming and NFO identity from the full physical source episode set.
func episodeOption(catalog []Episode, source importSource) *library.Episode {
	episodes := episodesForIDs(catalog, source.EpisodeIDs)
	if len(episodes) == 0 {
		return nil
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		return episodes[i].Number < episodes[j].Number
	})
	primary := episodes[0]
	season := primary.Season
	if season < 0 {
		season = 0
	}
	numbers := []int{}
	for _, episode := range episodes {
		if episode.Season == primary.Season && episode.Number > 0 && !containsInt(numbers, episode.Number) {
			numbers = append(numbers, episode.Number)
		}
	}
	if len(numbers) == 0 {
		numbers = []int{1}
	}
	sort.Ints(numbers)
	return &library.Episode{
		Season: season, Numbers: numbers, Title: primary.Title, AirDate: primary.AirDate, IMDbID: primary.IMDbID,
		Details: episodeDetails(episodes, primary.Season, season),
	}
}

// episodeDetails carries per-episode NFO identity in catalog order.
func episodeDetails(episodes []Episode, ownerSeason, season int) []metadata.Episode {
	details := make([]metadata.Episode, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Season != ownerSeason || episode.Number < 1 {
			continue
		}
		details = append(details, metadata.Episode{
			IMDbID: episode.IMDbID, Title: episode.Title, Season: season,
			Number: episode.Number, AirDate: episode.AirDate, Rating: episode.Rating,
		})
	}
	return details
}

func episodesForIDs(catalog []Episode, ids []string) []Episode {
	episodes := make([]Episode, 0, len(ids))
	for _, id := range ids {
		for _, episode := range catalog {
			if episode.ID == id {
				episodes = append(episodes, episode)
				break
			}
		}
	}
	return episodes
}

func evaluatedEpisodeFile(profile quality.Profile, info importQuality, source importSource, file library.File) evaluatedFile {
	title := firstText(info.Title, source.Name)
	decision := quality.Evaluate(profile, title, file.Size, nil)
	score := decision.Score
	if info.Score > score {
		score = info.Score
	}
	return evaluatedFile{file: file, quality: firstText(info.Quality, decision.Details.Quality, quality.Parse(source.Name).Quality), score: score}
}

// mergeEpisodeFiles keeps untouched present files and refreshes only imported paths.
func mergeEpisodeFiles(root movies.RootFolder, existing []movies.File, published []evaluatedFile, now time.Time) []movies.File {
	byPath := make(map[string]evaluatedFile, len(published))
	for _, item := range published {
		byPath[item.file.Path] = item
	}
	files := make([]movies.File, 0, len(existing)+len(published))
	for _, file := range existing {
		if file.RootID != root.ID {
			files = append(files, file)
			continue
		}
		if item, ok := byPath[file.Path]; ok {
			file.Size = item.file.Size
			file.Quality = item.quality
			file.Score = item.score
			file.ImportedAt = now
			files = append(files, file)
			continue
		}
		if handle, err := library.Open(root.Path, file.Path); err == nil {
			info, statErr := handle.Stat()
			handle.Close()
			if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				files = append(files, file)
			}
		}
	}
	for _, item := range published {
		found := false
		for _, file := range files {
			if file.RootID == root.ID && file.Path == item.file.Path {
				found = true
				break
			}
		}
		if found {
			continue
		}
		files = append(files, movies.File{
			RootID: root.ID, Path: item.file.Path, Size: item.file.Size,
			Quality: item.quality, Score: item.score, ImportedAt: now,
		})
	}
	return files
}

func (s *Service) importOptions(cfg Config, series Series, root movies.RootFolder, sourceRoot string, episode *library.Episode, qualityName string, existing []string) library.Options {
	mode := cfg.ImportMode
	if mode == "" {
		mode = library.ModeLink
	}
	return library.Options{
		SourceRoot:     sourceRoot,
		Root:           root.Path,
		FolderTemplate: cfg.FolderTemplate,
		FileTemplate:   cfg.FileTemplate,
		Mode:           mode,
		Metadata:       series.Metadata,
		Quality:        qualityName,
		WriteNFO:       cfg.WriteNFO,
		Episode:        episode,
		Existing:       existing,
	}
}

func (s *Service) journalRows(ctx context.Context, jobID string) (map[string]journalFile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, root_path, path, size, sha256, ready FROM download_library_files WHERE job_id = $1`, jobID)
	if err != nil {
		return nil, errors.New("tv: the import journal could not be loaded")
	}
	defer rows.Close()
	journal := map[string]journalFile{}
	for rows.Next() {
		var entry journalFile
		if err := rows.Scan(&entry.Name, &entry.RootPath, &entry.Path, &entry.Size, &entry.SHA256, &entry.Ready); err != nil {
			return nil, errors.New("tv: the import journal could not be loaded")
		}
		journal[entry.Name] = entry
	}
	return journal, rows.Err()
}

func (s *Service) planJournal(ctx context.Context, jobID, sourceRoot, destRootPath string, sources []library.Source, planned []library.File) error {
	if jobID == "" {
		return nil
	}
	if len(planned) != len(sources) {
		return errors.New("tv: the import plan does not match the source files")
	}
	for i, source := range sources {
		digest, err := fileSHA256(ctx, sourceRoot, source.Name)
		if err != nil {
			return fmt.Errorf("tv: hash source %s: %w", source.Name, err)
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO download_library_files (job_id, name, root_path, path, size, sha256, ready)
			 VALUES ($1, $2, $3, $4, $5, $6, false)
			 ON CONFLICT (job_id, name) DO UPDATE SET root_path = EXCLUDED.root_path, path = EXCLUDED.path,
			 size = EXCLUDED.size, sha256 = EXCLUDED.sha256, ready = false`,
			jobID, source.Name, destRootPath, planned[i].Path, planned[i].Size, digest); err != nil {
			return errors.New("tv: the import journal could not be saved")
		}
	}
	return nil
}

func (s *Service) completeJournal(ctx context.Context, jobID string, names []string) error {
	if jobID == "" || len(names) == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE download_library_files SET ready = true WHERE job_id = $1 AND name = ANY($2)`, jobID, names); err != nil {
		return errors.New("tv: the import journal could not be completed")
	}
	return nil
}

func fileSHA256(ctx context.Context, rootPath, name string) (string, error) {
	handle, err := library.Open(rootPath, name)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	digest := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		read, readErr := handle.Read(buffer)
		if read > 0 {
			if _, err := digest.Write(buffer[:read]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func fileMatchesSHA256(ctx context.Context, rootPath, name, expected string) bool {
	if expected == "" {
		return false
	}
	actual, err := fileSHA256(ctx, rootPath, name)
	return err == nil && actual == expected
}

func (s *Service) failAcquisition(ctx context.Context, acquisition Acquisition, message string) error {
	message = truncate(strings.TrimSpace(message), maxTextRunes)
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		SeriesID: acquisition.SeriesID, EpisodeIDs: acquisition.EpisodeIDs, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "failed", Error: message,
	}); err != nil {
		return err
	}
	if acquisition.ReleaseID != "" {
		if err := s.Store.Block(ctx, acquisition.SeriesID, acquisition.ReleaseID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if err := s.patchSeriesError(ctx, acquisition.SeriesID, message); err != nil {
		return err
	}
	for _, id := range acquisition.EpisodeIDs {
		_, _ = s.Store.PatchEpisode(ctx, id, map[string]any{"error": message})
	}
	_ = s.Store.Event(ctx, acquisition.SeriesID, "", "download-failed", truncate("Download failed: "+message, maxTextRunes))
	return nil
}

func (s *Service) recordImportFailure(ctx context.Context, acquisition Acquisition, cause error) error {
	message := truncate(strings.TrimSpace(cause.Error()), maxTextRunes)
	if acquisition.Status == "import-failed" && acquisition.Error == message {
		return nil
	}
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		SeriesID: acquisition.SeriesID, EpisodeIDs: acquisition.EpisodeIDs, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "import-failed", Error: message,
	}); err != nil {
		return err
	}
	if err := s.patchSeriesError(ctx, acquisition.SeriesID, message); err != nil {
		return err
	}
	for _, id := range acquisition.EpisodeIDs {
		_, _ = s.Store.PatchEpisode(ctx, id, map[string]any{"error": message})
	}
	_ = s.Store.Event(ctx, acquisition.SeriesID, "", "import-failed", truncate("Import failed: "+message, maxTextRunes))
	return nil
}

func (s *Service) markSuperseded(ctx context.Context, series Series, acquisition Acquisition, reason string) error {
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		SeriesID: series.ID, EpisodeIDs: acquisition.EpisodeIDs, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "superseded",
	}); err != nil {
		return err
	}
	if err := s.patchSeriesError(ctx, series.ID, ""); err != nil {
		return err
	}
	_ = s.Store.Event(ctx, series.ID, "", "superseded", truncate("Skipped "+firstText(acquisition.Title, acquisition.ReleaseID)+": "+reason, maxTextRunes))
	return nil
}

func (s *Service) saveAcquisitionStatus(ctx context.Context, acquisition Acquisition) error {
	if err := s.Store.SaveAcquisition(ctx, acquisition); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) patchSeriesError(ctx context.Context, seriesID, message string) error {
	if _, err := s.Store.Patch(ctx, seriesID, map[string]any{"error": message}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// syncLegacy scans one bounded job page per cycle so old unknown jobs cannot starve matched ones.
func (s *Service) syncLegacy(ctx context.Context, cfg Config) (int, error) {
	cursorTime, cursorID := s.legacyCursor(ctx)
	jobs, err := s.Downloads.UnlinkedTV(ctx, cursorTime, cursorID)
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		s.resetLegacyCursor(ctx)
		return 0, nil
	}
	s.advanceLegacyCursor(ctx, jobs[len(jobs)-1])
	seriesList, err := s.Store.List(ctx)
	if err != nil {
		return 0, err
	}
	imported := 0
	var problems []error
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		outputDir, err := s.Downloads.OutputDirectory(job.ID)
		if err != nil {
			continue
		}
		candidates, err := library.ScanTV(ctx, outputDir)
		if err != nil || len(candidates) == 0 {
			continue
		}
		series, sources, episodeIDs, err := s.legacySources(ctx, candidates, seriesList, job, outputDir)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if series.ID == "" {
			continue
		}
		acquisition := Acquisition{
			SeriesID: series.ID, EpisodeIDs: episodeIDs, JobID: job.ID, ReleaseID: job.ReleaseID,
			Title: job.Title, Status: "importing",
		}
		if err := s.saveAcquisitionStatus(ctx, acquisition); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
				continue
			}
			problems = append(problems, err)
			continue
		}
		count, err := s.runImport(ctx, cfg, series.ID, job, acquisition, sources)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		imported += count
	}
	return imported, errors.Join(problems...)
}

// legacyCursorPrefix names the durable page cursor rows in tv_automation.
const legacyCursorPrefix = "tv-legacy-"

func (s *Service) legacyCursor(ctx context.Context) (time.Time, string) {
	var name string
	var last time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT name, last_run FROM tv_automation WHERE name LIKE 'tv-legacy-%' ORDER BY last_run DESC, name DESC LIMIT 1`).Scan(&name, &last)
	if err != nil {
		return time.Time{}, ""
	}
	id := strings.TrimPrefix(name, legacyCursorPrefix)
	if !validID(id) {
		return time.Time{}, ""
	}
	return last, id
}

func (s *Service) advanceLegacyCursor(ctx context.Context, job downloads.Job) {
	name := legacyCursorPrefix + job.ID
	_, _ = s.pool.Exec(ctx, `DELETE FROM tv_automation WHERE name LIKE 'tv-legacy-%' AND name <> $1`, name)
	_, _ = s.pool.Exec(ctx, `INSERT INTO tv_automation (name, last_run) VALUES ($1, $2)
	 ON CONFLICT (name) DO UPDATE SET last_run = EXCLUDED.last_run`, name, job.CreatedAt)
}

func (s *Service) resetLegacyCursor(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `DELETE FROM tv_automation WHERE name LIKE 'tv-legacy-%'`)
}

// legacySources matches a legacy job strictly; unknown series stay unmatched for manual catalog adds.
func (s *Service) legacySources(ctx context.Context, candidates []library.TVCandidate, seriesList []Series, job downloads.Job, outputDir string) (Series, []library.Source, []string, error) {
	series, matched := matchLegacySeries(candidates, seriesList)
	if !matched {
		return Series{}, nil, nil, nil
	}
	catalog, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return Series{}, nil, nil, err
	}
	requested := make(map[string]bool, len(catalog))
	for _, episode := range catalog {
		requested[episode.ID] = true
	}
	sources := make([]library.Source, 0, len(candidates))
	var episodeIDs []string
	for _, candidate := range candidates {
		if !seriesCandidateMatches(series, candidate) {
			return Series{}, nil, nil, nil
		}
		identity := library.EpisodeIdentity{Season: candidate.Season, Numbers: candidate.Numbers, AirDate: candidate.AirDate, Pack: candidate.Pack}
		mapped, err := mapEpisodes(catalog, requested, identity, candidate.Path)
		if err != nil {
			return Series{}, nil, nil, nil
		}
		sources = append(sources, library.Source{Name: candidate.Path, Size: candidate.Size})
		episodeIDs = append(episodeIDs, mapped...)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.Path] = true
	}
	if legacyHasUnparsedVideo(ctx, outputDir, seen) {
		return Series{}, nil, nil, nil
	}
	for _, file := range job.Files {
		name := filepath.ToSlash(strings.TrimSpace(file.Name))
		if isEpisodeVideo(name) && !seen[name] {
			return Series{}, nil, nil, nil
		}
	}
	episodeIDs = normalizeList(episodeIDs)
	if len(episodeIDs) == 0 {
		return Series{}, nil, nil, nil
	}
	return series, sources, episodeIDs, nil
}

// legacyHasUnparsedVideo rejects a job whose output holds video the strict scan cannot number.
func legacyHasUnparsedVideo(ctx context.Context, outputDir string, seen map[string]bool) bool {
	scanned, err := library.Scan(ctx, outputDir)
	if err != nil {
		return false
	}
	for _, candidate := range scanned {
		if seen[candidate.Path] {
			continue
		}
		if _, ok := library.ParseEpisode(candidate.Path); !ok {
			return true
		}
	}
	return false
}

func matchLegacySeries(candidates []library.TVCandidate, seriesList []Series) (Series, bool) {
	var matched Series
	for _, candidate := range candidates {
		found, ok := strictSeriesMatch(seriesList, candidate.Title, candidate.Year, candidate.IMDbID)
		if !ok || (matched.ID != "" && matched.ID != found.ID) {
			return Series{}, false
		}
		matched = found
	}
	if matched.ID == "" {
		return Series{}, false
	}
	return matched, true
}

func strictSeriesMatch(seriesList []Series, title string, year int, imdbID string) (Series, bool) {
	if imdbID = strings.ToLower(strings.TrimSpace(imdbID)); imdbID != "" {
		var matches []Series
		for _, series := range seriesList {
			if strings.EqualFold(strings.TrimSpace(series.Metadata.IMDbID), imdbID) {
				matches = append(matches, series)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
		return Series{}, false
	}
	key := normalizeTVTitle(title)
	if key == "" || year <= 0 {
		return Series{}, false
	}
	var matches []Series
	for _, series := range seriesList {
		if normalizeTVTitle(series.Metadata.Title) == key && series.Metadata.Year == year {
			matches = append(matches, series)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Series{}, false
}

func seriesCandidateMatches(series Series, candidate library.TVCandidate) bool {
	imdbID := strings.ToLower(strings.TrimSpace(candidate.IMDbID))
	if imdbID != "" && series.Metadata.IMDbID != "" {
		return strings.EqualFold(imdbID, series.Metadata.IMDbID)
	}
	key := normalizeTVTitle(series.Metadata.Title)
	if key == "" || normalizeTVTitle(candidate.Title) != key {
		return false
	}
	return candidate.Year == 0 || series.Metadata.Year == 0 || candidate.Year == series.Metadata.Year
}

func (s *Service) notifyImported(ctx context.Context, series Series, episodeIDs, paths []string) {
	cfg, err := s.Shared.Config(ctx)
	if err != nil {
		return
	}
	payload := importPayload{
		Event: "tv.imported", SeriesID: series.ID, Title: series.Metadata.Title, Year: series.Metadata.Year,
		IMDbID: series.Metadata.IMDbID, EpisodeIDs: episodeIDs, Files: paths,
	}
	for _, err := range movies.NotifyImport(ctx, cfg, payload) {
		message := truncate(strings.TrimSpace(err.Error()), maxTextRunes)
		slog.Warn("TV import notification failed", "series", series.ID, "error", message)
		_ = s.Store.Event(ctx, series.ID, "", "integration-failed", message)
	}
}

// Scan lists library video files with parsed episode numbering and catalog matches.
func (s *Service) Scan(ctx context.Context, rootID string) ([]Candidate, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	root, ok := rootFor(cfg, strings.TrimSpace(rootID))
	if !ok {
		return nil, fmt.Errorf("%w: root folder does not exist", ErrInvalid)
	}
	found, err := library.ScanTV(ctx, root.Path)
	if err != nil {
		return nil, fmt.Errorf("tv: scan library root: %w", err)
	}
	seriesList, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	episodes, err := s.Store.Episodes(ctx, "")
	if err != nil {
		return nil, err
	}
	byIMDb := map[string][]string{}
	byTitleYear := map[string][]string{}
	byPath := map[string]string{}
	for _, series := range seriesList {
		if imdbID := strings.ToLower(strings.TrimSpace(series.Metadata.IMDbID)); imdbID != "" {
			byIMDb[imdbID] = append(byIMDb[imdbID], series.ID)
		}
		if key := normalizeTVTitle(series.Metadata.Title); key != "" && series.Metadata.Year > 0 {
			byTitleYear[seriesYearKey(key, series.Metadata.Year)] = append(byTitleYear[seriesYearKey(key, series.Metadata.Year)], series.ID)
		}
	}
	for _, episode := range episodes {
		for _, file := range episode.Files {
			if file.RootID == root.ID {
				byPath[file.Path] = episode.SeriesID
			}
		}
	}
	results := make([]Candidate, 0, len(found))
	for _, candidate := range found {
		item := Candidate{
			Path: candidate.Path, Size: candidate.Size, Title: candidate.Title, Year: candidate.Year,
			IMDbID: candidate.IMDbID, Quality: candidate.Quality, Season: candidate.Season,
			Episodes: candidate.Numbers, AirDate: candidate.AirDate,
		}
		if imdbID := strings.ToLower(strings.TrimSpace(candidate.IMDbID)); imdbID != "" {
			if ids := byIMDb[imdbID]; len(ids) == 1 {
				item.MatchedSeriesID = ids[0]
			}
		}
		if item.MatchedSeriesID == "" && normalizeTVTitle(candidate.Title) != "" && candidate.Year > 0 {
			if ids := byTitleYear[seriesYearKey(normalizeTVTitle(candidate.Title), candidate.Year)]; len(ids) == 1 {
				item.MatchedSeriesID = ids[0]
			}
		}
		if owner, ok := byPath[candidate.Path]; ok {
			item.MatchedSeriesID = owner
			item.Error = "file is already in the library"
		}
		results = append(results, item)
	}
	return results, nil
}

// Import matches one existing video below a configured root and imports it in place.
func (s *Service) Import(ctx context.Context, input ImportInput) (Series, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Series{}, err
	}
	sourceRoot, ok := rootFor(cfg, strings.TrimSpace(input.RootID))
	if !ok {
		return Series{}, fmt.Errorf("%w: source root folder does not exist", ErrInvalid)
	}
	seriesID := strings.TrimSpace(input.SeriesID)
	if seriesID == "" {
		return Series{}, fmt.Errorf("%w: select a series for this file", ErrInvalid)
	}
	series, err := s.Store.Get(ctx, seriesID)
	if errors.Is(err, ErrNotFound) {
		return Series{}, fmt.Errorf("%w: series does not exist", ErrInvalid)
	}
	if err != nil {
		return Series{}, err
	}
	filePath := filepath.ToSlash(strings.TrimSpace(input.Path))
	if filePath == "" || len(filePath) > maxPathBytes {
		return Series{}, fmt.Errorf("%w: a video file path is required", ErrInvalid)
	}
	if !isEpisodeVideo(filePath) {
		return Series{}, fmt.Errorf("%w: %s is not a video file", ErrInvalid, filePath)
	}
	handle, err := library.Open(sourceRoot.Path, filePath)
	if err != nil {
		return Series{}, fmt.Errorf("%w: the file is not available below the source root", ErrInvalid)
	}
	info, statErr := handle.Stat()
	handle.Close()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		return Series{}, fmt.Errorf("%w: the file is not usable", ErrInvalid)
	}
	explicit := len(input.Episodes) > 0
	season := input.Season
	numbers := input.Episodes
	if !explicit {
		identity, ok := sourceIdentity(filePath)
		if !ok {
			return Series{}, fmt.Errorf("%w: cannot tell which episodes %s holds; pass a season and episode numbers", ErrInvalid, filePath)
		}
		if !seriesMatches(series, identity, "") {
			return Series{}, fmt.Errorf("%w: %s does not match this series", ErrInvalid, filePath)
		}
		switch {
		case identity.AirDate != "":
			episode, err := s.episodeByAirDate(ctx, series.ID, identity.AirDate)
			if err != nil {
				return Series{}, err
			}
			season, numbers = episode.Season, []int{episode.Number}
		case identity.Season < 0:
			return Series{}, fmt.Errorf("%w: pass a season and episode numbers for this file", ErrInvalid)
		case len(identity.Numbers) == 0:
			return Series{}, fmt.Errorf("%w: season packs need explicit episode numbers", ErrInvalid)
		default:
			season, numbers = identity.Season, identity.Numbers
		}
	}
	if season < 0 || season > maxSeason || len(numbers) == 0 || len(numbers) > maxEpisodeNumber {
		return Series{}, fmt.Errorf("%w: a season and episode numbers are required", ErrInvalid)
	}
	seenNumbers := make(map[int]bool, len(numbers))
	for _, number := range numbers {
		if number < 1 || number > maxEpisodeNumber || seenNumbers[number] {
			return Series{}, fmt.Errorf("%w: episode numbers must be unique and between 1 and %d", ErrInvalid, maxEpisodeNumber)
		}
		seenNumbers[number] = true
	}
	var saved Series
	var importedEpisodes, importedPaths []string
	err = s.withSeriesLock(ctx, series.ID, func(ctx context.Context) error {
		fresh, err := s.Store.Get(ctx, series.ID)
		if err != nil {
			return err
		}
		episodes, err := s.ensureEpisodes(ctx, fresh, season, numbers, explicit)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(episodes))
		needed := make(map[string]bool, len(episodes))
		for _, episode := range episodes {
			ids = append(ids, episode.ID)
			needed[episode.ID] = true
		}
		source := importSource{Source: library.Source{Name: filePath, Size: info.Size()}, EpisodeIDs: ids}
		acquisition := Acquisition{SeriesID: fresh.ID, EpisodeIDs: ids, Title: fresh.Metadata.Title, Status: "importing"}
		saved, importedEpisodes, importedPaths, err = s.importJob(ctx, cfg, fresh, downloads.Job{}, acquisition, []importSource{source}, needed, nil, sourceRoot.Path)
		return err
	})
	if err != nil {
		return Series{}, err
	}
	if len(importedPaths) > 0 {
		s.notifyImported(ctx, saved, importedEpisodes, importedPaths)
	}
	return saved, nil
}

// ensureEpisodes resolves explicit season and episode numbers, adding missing catalog rows offline.
func (s *Service) ensureEpisodes(ctx context.Context, series Series, season int, numbers []int, explicit bool) ([]Episode, error) {
	catalog, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	byNumber := map[int]Episode{}
	for _, episode := range catalog {
		if episode.Season == season {
			byNumber[episode.Number] = episode
		}
	}
	episodes := make([]Episode, 0, len(numbers))
	for _, number := range numbers {
		if episode, ok := byNumber[number]; ok {
			episodes = append(episodes, episode)
			continue
		}
		if !explicit {
			return nil, fmt.Errorf("%w: episode %d is not in the series catalog; refresh metadata or pass a season and episode numbers", ErrInvalid, number)
		}
		created, err := s.Store.UpsertEpisode(ctx, Episode{
			SeriesID: series.ID, Season: season, Number: number,
			Title: "Episode " + strconv.Itoa(number), Monitored: true,
		})
		if err != nil {
			return nil, err
		}
		byNumber[number] = created
		episodes = append(episodes, created)
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].Number < episodes[j].Number })
	return episodes, nil
}

func (s *Service) episodeByAirDate(ctx context.Context, seriesID, airDate string) (Episode, error) {
	catalog, err := s.Store.Episodes(ctx, seriesID)
	if err != nil {
		return Episode{}, err
	}
	var matched *Episode
	for i := range catalog {
		if catalog[i].AirDate != airDate {
			continue
		}
		if matched != nil {
			return Episode{}, fmt.Errorf("%w: air date %s matches more than one episode; pass a season and episode numbers", ErrInvalid, airDate)
		}
		matched = &catalog[i]
	}
	if matched == nil {
		return Episode{}, fmt.Errorf("%w: no episode has air date %s", ErrInvalid, airDate)
	}
	return *matched, nil
}

// Rename renames every present episode file safely one by one.
func (s *Service) Rename(ctx context.Context, seriesID string, preview bool) (movies.RenameResult, error) {
	seriesID = strings.TrimSpace(seriesID)
	if preview {
		series, err := s.Store.Get(ctx, seriesID)
		if err != nil {
			return movies.RenameResult{}, err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return movies.RenameResult{}, err
		}
		return s.renameFiles(ctx, cfg, series, false)
	}
	var result movies.RenameResult
	err := s.withSeriesLock(ctx, seriesID, func(ctx context.Context) error {
		series, err := s.Store.Get(ctx, seriesID)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		result, err = s.renameFiles(ctx, cfg, series, true)
		return err
	})
	if err != nil {
		return result, err
	}
	if len(result.Files) > 0 {
		result.Applied = true
		_ = s.Store.Event(ctx, seriesID, "", "renamed", truncate("Renamed "+strconv.Itoa(len(result.Files))+" file(s)", maxTextRunes))
	}
	return result, nil
}

type renameEntry struct {
	root     movies.RootFolder
	path     string
	size     int64
	quality  string
	episodes []Episode
	option   *library.Episode
}

// renamePlan records one published rename so the catalog transaction or a disk restore can use it.
type renamePlan struct {
	root movies.RootFolder
	from string
	to   string
	size int64
}

func (s *Service) renameFiles(ctx context.Context, cfg Config, series Series, apply bool) (movies.RenameResult, error) {
	result := movies.RenameResult{Files: []movies.RenameFile{}}
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return result, err
	}
	var planned []renamePlan
	restore := func(cause error) (movies.RenameResult, error) {
		if restoreErr := s.restoreRenamedFiles(planned); restoreErr != nil {
			return result, fmt.Errorf("tv: rename: %w (disk restore: %v)", cause, restoreErr)
		}
		return result, fmt.Errorf("tv: rename: %w", cause)
	}
	for _, entry := range s.renameEntries(cfg, episodes) {
		if err := ctx.Err(); err != nil {
			return restore(err)
		}
		opts := s.importOptions(cfg, series, entry.root, entry.root.Path, entry.option, entry.quality, []string{entry.path})
		// Rename always moves so no stale source copy remains beside the new name.
		opts.Mode = library.ModeMove
		preview, err := library.Preview(opts, []library.Source{{Name: entry.path, Size: entry.size}})
		if err != nil {
			return restore(err)
		}
		if len(preview) != 1 {
			return restore(errors.New("tv: rename plan does not match the source file"))
		}
		result.Files = append(result.Files, movies.RenameFile{From: entry.path, To: preview[0].Path})
		if !apply || entry.path == preview[0].Path {
			continue
		}
		files, err := library.Import(ctx, opts, []library.Source{{Name: entry.path, Size: entry.size}})
		if err != nil {
			return restore(err)
		}
		if len(files) != 1 {
			return restore(errors.New("tv: rename published an unexpected file set"))
		}
		planned = append(planned, renamePlan{root: entry.root, from: entry.path, to: files[0].Path, size: files[0].Size})
	}
	if !apply || len(planned) == 0 {
		return result, nil
	}
	if err := s.commitRename(ctx, episodes, planned); err != nil {
		return restore(err)
	}
	return result, nil
}

// commitRename updates every owner episode and journal row in one transaction after publication.
func (s *Service) commitRename(ctx context.Context, episodes []Episode, planned []renamePlan) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errors.New("tv: rename cannot reach the database")
	}
	defer tx.Rollback(ctx)
	for i := range episodes {
		owner := episodes[i]
		changed := false
		for j := range owner.Files {
			for _, plan := range planned {
				if owner.Files[j].RootID == plan.root.ID && owner.Files[j].Path == plan.from {
					owner.Files[j].Path = plan.to
					owner.Files[j].Size = plan.size
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		body, err := encode(owner.Files)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE tv_episodes SET data = data || jsonb_build_object('files', $2::jsonb) WHERE id = $1`, owner.ID, string(body))
		if err != nil {
			return errors.New("tv: rename could not update episode files")
		}
		if tag.RowsAffected() != 1 {
			return errors.New("tv: rename lost its episode row")
		}
	}
	for _, plan := range planned {
		if _, err := tx.Exec(ctx,
			`UPDATE download_library_files SET path = $3 WHERE ready AND root_path = $1 AND path = $2`,
			plan.root.Path, plan.from, plan.to); err != nil {
			return errors.New("tv: rename could not update the import journal")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("tv: rename could not be saved")
	}
	return nil
}

func (s *Service) restoreRenamedFiles(planned []renamePlan) error {
	var problems []error
	for i := len(planned) - 1; i >= 0; i-- {
		plan := planned[i]
		if err := library.MoveWithSubtitles(plan.root.Path, plan.to, plan.from); err != nil {
			problems = append(problems, err)
			continue
		}
		if root, err := os.OpenRoot(plan.root.Path); err == nil {
			_ = root.Remove(strings.TrimSuffix(plan.to, path.Ext(plan.to)) + ".nfo")
			root.Close()
		}
	}
	return errors.Join(problems...)
}

// renameEntries groups owned files once so a multi-episode file keeps every owner.
func (s *Service) renameEntries(cfg Config, episodes []Episode) []renameEntry {
	byKey := map[string]*renameEntry{}
	order := []string{}
	for _, episode := range episodes {
		for _, file := range episode.Files {
			if !tvFilePresent(cfg.RootFolders, file) {
				continue
			}
			key := file.RootID + "\x00" + file.Path
			entry, ok := byKey[key]
			if !ok {
				root, found := rootFor(cfg, file.RootID)
				if !found {
					continue
				}
				entry = &renameEntry{root: root, path: file.Path, size: file.Size, quality: file.Quality}
				byKey[key] = entry
				order = append(order, key)
			}
			entry.episodes = append(entry.episodes, episode)
		}
	}
	entries := make([]renameEntry, 0, len(order))
	for _, key := range order {
		entry := byKey[key]
		sort.Slice(entry.episodes, func(i, j int) bool {
			if entry.episodes[i].Season != entry.episodes[j].Season {
				return entry.episodes[i].Season < entry.episodes[j].Season
			}
			return entry.episodes[i].Number < entry.episodes[j].Number
		})
		primary := entry.episodes[0]
		season := primary.Season
		if season < 0 {
			season = 0
		}
		numbers := []int{}
		for _, episode := range entry.episodes {
			if episode.Season == primary.Season && episode.Number > 0 && !containsInt(numbers, episode.Number) {
				numbers = append(numbers, episode.Number)
			}
		}
		sort.Ints(numbers)
		if len(numbers) == 0 {
			numbers = []int{1}
		}
		entry.option = &library.Episode{
			Season: season, Numbers: numbers, Title: primary.Title, AirDate: primary.AirDate, IMDbID: primary.IMDbID,
			Details: episodeDetails(entry.episodes, primary.Season, season),
		}
		entries = append(entries, *entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries
}

// OpenFile serves one catalog-owned path below its configured root.
func (s *Service) OpenFile(ctx context.Context, seriesID, filePath string) (*os.File, error) {
	series, err := s.Store.Get(ctx, strings.TrimSpace(seriesID))
	if err != nil {
		return nil, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	want := filepath.ToSlash(filepath.Clean(strings.TrimSpace(filePath)))
	episodes, err := s.Store.Episodes(ctx, series.ID)
	if err != nil {
		return nil, err
	}
	var matches []movies.File
	seen := map[string]bool{}
	for _, episode := range episodes {
		for _, file := range episode.Files {
			if filepath.ToSlash(filepath.Clean(file.Path)) != want {
				continue
			}
			key := file.RootID + "\x00" + file.Path
			if seen[key] {
				continue
			}
			seen[key] = true
			matches = append(matches, file)
		}
	}
	if len(matches) != 1 {
		return nil, ErrNotFound
	}
	root, ok := rootFor(cfg, matches[0].RootID)
	if !ok {
		return nil, ErrNotFound
	}
	handle, err := library.Open(root.Path, matches[0].Path)
	if err != nil {
		return nil, ErrNotFound
	}
	return handle, nil
}

func (s *Service) presentEpisodeFiles(cfg Config, files []movies.File) []movies.File {
	present := make([]movies.File, 0, len(files))
	for _, file := range files {
		if tvFilePresent(cfg.RootFolders, file) {
			present = append(present, file)
		}
	}
	return present
}

func seriesYearKey(key string, year int) string {
	return key + strconv.Itoa(year)
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueStrings(values []string) []string {
	unique := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}
