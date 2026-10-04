package movies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

var (
	movieVideoExtensions = map[string]bool{
		".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".m4v": true,
		".webm": true, ".mpeg": true, ".mpg": true, ".ts": true, ".wmv": true,
	}
	movieSamplePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(sample|samples|trailer|trailers)([^a-z0-9]|$)`)
	moviePartPattern   = regexp.MustCompile(`(?i)[ ._-]?(?:part|cd|disc|disk|pt)[ ._-]?(\d{1,2})$`)
)

type importQuality struct {
	Title   string
	Quality string
	Score   int
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
	movie, err := s.Store.Get(ctx, acquisition.MovieID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	job, err := s.Downloads.Get(ctx, acquisition.JobID)
	if errors.Is(err, downloads.ErrNotFound) {
		if acquisition.Status == "failed" {
			return 0, nil
		}
		return 0, s.failAcquisition(ctx, cfg, movie.ID, acquisition, "the download no longer exists")
	}
	if err != nil {
		return 0, err
	}
	switch job.Status {
	case "completed":
		return s.runImport(ctx, cfg, movie.ID, job, acquisition, nil)
	case "failed":
		if acquisition.Status == "failed" {
			return 0, nil
		}
		return 0, s.failAcquisition(ctx, cfg, movie.ID, acquisition, firstNonEmpty(job.Error, "the download failed"))
	default:
		return 0, nil
	}
}

func (s *Service) runImport(ctx context.Context, cfg Config, movieID string, job downloads.Job, acquisition Acquisition, narrowed []library.Source) (int, error) {
	imported := 0
	var notifyMovie Movie
	var notifyPaths []string
	err := s.withMovieLock(ctx, movieID, func(ctx context.Context) error {
		fresh, err := s.Store.Get(ctx, movieID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		sources, err := s.importSourcesFor(ctx, job, narrowed)
		if err != nil {
			return err
		}
		if reason, superseded := s.superseded(ctx, cfg, fresh, acquisition, sources); superseded {
			return s.markSuperseded(ctx, fresh, acquisition, reason)
		}
		saved, paths, err := s.importJob(ctx, cfg, fresh, job, acquisition, sources)
		if err != nil {
			return err
		}
		notifyMovie, notifyPaths = saved, paths
		imported = 1
		return nil
	})
	if errors.Is(err, ErrConflict) {
		// Another process is importing this movie right now.
		return 0, nil
	}
	if err != nil {
		return 0, s.recordImportFailure(ctx, cfg, movieID, acquisition, err)
	}
	if len(notifyPaths) > 0 {
		s.notifyImport(ctx, cfg, notifyMovie, notifyPaths)
	}
	return imported, nil
}

func (s *Service) importSourcesFor(ctx context.Context, job downloads.Job, narrowed []library.Source) ([]library.Source, error) {
	sources := narrowed
	if sources == nil {
		all := make([]library.Source, 0, len(job.Files))
		for _, file := range job.Files {
			all = append(all, library.Source{Name: file.Name, Size: file.Size})
		}
		sources = all
	}
	sources = movieSources(sources)
	if len(sources) == 0 {
		return nil, errors.New("movies: the download contains no movie video")
	}
	if narrowed == nil {
		if message := s.packConflict(ctx, job.ID, sources); message != "" {
			return nil, errors.New(message)
		}
	}
	return sources, nil
}

func (s *Service) packConflict(ctx context.Context, jobID string, sources []library.Source) string {
	outputDir, err := s.Downloads.OutputDirectory(jobID)
	if err != nil {
		return ""
	}
	candidates, err := library.Scan(ctx, outputDir)
	if err != nil || len(candidates) == 0 {
		return ""
	}
	identities := make(map[string]bool, len(sources))
	for _, source := range sources {
		identities[identityForSource(source.Name, candidates)] = true
	}
	if len(identities) < 2 {
		return ""
	}
	return packMessage(packGroups(candidates))
}

func packMessage(groups map[string]library.Candidate) string {
	return "movies: the download contains multiple movies (" + strings.Join(packLabels(groups), ", ") +
		"); run a library scan and import the files manually"
}

func packLabels(groups map[string]library.Candidate) []string {
	labels := make([]string, 0, len(groups))
	for _, candidate := range groups {
		label := strings.TrimSpace(candidate.Title)
		if label == "" {
			label = candidate.IMDbID
		}
		if candidate.Year > 0 {
			label += " (" + strconv.Itoa(candidate.Year) + ")"
		}
		labels = append(labels, strings.TrimSpace(label))
	}
	sort.Strings(labels)
	return labels
}

func packGroups(candidates []library.Candidate) map[string]library.Candidate {
	groups := make(map[string]library.Candidate, len(candidates))
	for _, candidate := range candidates {
		identity := candidateIdentity(candidate)
		if current, ok := groups[identity]; !ok || candidate.Size > current.Size {
			groups[identity] = candidate
		}
	}
	return groups
}

func candidateIdentity(candidate library.Candidate) string {
	if imdbID := strings.ToLower(strings.TrimSpace(candidate.IMDbID)); imdbID != "" {
		return imdbID
	}
	if key := scanTitleKey(candidate.Title); key != "" && candidate.Year > 0 {
		return key + strconv.Itoa(candidate.Year)
	}
	return "path:" + importGroup(candidate.Path)
}

func identityForSource(name string, candidates []library.Candidate) string {
	group := importGroup(name)
	for _, candidate := range candidates {
		if importGroup(candidate.Path) == group {
			return candidateIdentity(candidate)
		}
	}
	return "path:" + group
}

func (s *Service) failAcquisition(ctx context.Context, cfg Config, movieID string, acquisition Acquisition, message string) error {
	message = sanitizeMessage(cfg, message)
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		MovieID: movieID, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "failed", Error: message,
	}); err != nil {
		return err
	}
	if acquisition.ReleaseID != "" {
		if err := s.Store.Block(ctx, movieID, acquisition.ReleaseID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if err := s.patchMovieError(ctx, movieID, message); err != nil {
		return err
	}
	_ = s.Store.Event(ctx, movieID, "download-failed", truncate("Download failed: "+message, maxTextRunes))
	return nil
}

func (s *Service) recordImportFailure(ctx context.Context, cfg Config, movieID string, acquisition Acquisition, cause error) error {
	message := sanitizeMessage(cfg, cause.Error())
	if acquisition.Status == "import-failed" && acquisition.Error == message {
		return nil
	}
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		MovieID: movieID, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "import-failed", Error: message,
	}); err != nil {
		return err
	}
	if err := s.patchMovieError(ctx, movieID, message); err != nil {
		return err
	}
	_ = s.Store.Event(ctx, movieID, "import-failed", truncate("Import failed: "+message, maxTextRunes))
	return nil
}

func (s *Service) markSuperseded(ctx context.Context, movie Movie, acquisition Acquisition, reason string) error {
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		MovieID: movie.ID, JobID: acquisition.JobID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "superseded",
	}); err != nil {
		return err
	}
	if err := s.patchMovieError(ctx, movie.ID, ""); err != nil {
		return err
	}
	_ = s.Store.Event(ctx, movie.ID, "superseded", truncate("Skipped "+firstNonEmpty(acquisition.Title, acquisition.ReleaseID)+": "+reason, maxTextRunes))
	return nil
}

func (s *Service) superseded(ctx context.Context, cfg Config, movie Movie, acquisition Acquisition, sources []library.Source) (string, bool) {
	if acquisition.Override {
		return "", false
	}
	profile, err := s.Store.Profile(ctx, movie.ProfileID)
	if err != nil {
		return "", false
	}
	current := bestCurrent(profile, presentFiles(cfg, movie.Files))
	if current == nil {
		return "", false
	}
	size := int64(0)
	title := acquisition.Title
	for _, source := range sources {
		if source.Size > size {
			size = source.Size
		}
		title = firstNonEmpty(title, source.Name)
	}
	if quality.Parse(title).Quality == "" {
		title += " " + acquisition.Decision.Details.Quality
	}
	decision := quality.Evaluate(profile, title, size, current)
	return strings.Join(decision.Reasons, "; "), !decision.Allowed
}

func (s *Service) importJob(ctx context.Context, cfg Config, movie Movie, job downloads.Job, acquisition Acquisition, sources []library.Source) (Movie, []string, error) {
	outputDir, err := s.Downloads.OutputDirectory(job.ID)
	if err != nil {
		return Movie{}, nil, errors.New("movies: the download output is not available")
	}
	info := importQuality{Title: acquisition.Title, Quality: acquisition.Decision.Details.Quality, Score: acquisition.Decision.Score}
	saved, paths, err := s.importSourcesResult(ctx, cfg, movie, outputDir, sources, info, job.ID)
	if err != nil {
		return Movie{}, nil, err
	}
	if err := s.saveAcquisitionStatus(ctx, Acquisition{
		MovieID: movie.ID, JobID: job.ID, ReleaseID: acquisition.ReleaseID,
		Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override, Status: "imported",
	}); err != nil {
		return Movie{}, nil, err
	}
	return saved, paths, nil
}

func (s *Service) syncLegacy(ctx context.Context, cfg Config) (int, error) {
	jobs, err := s.Downloads.UnlinkedMovies(ctx)
	if err != nil {
		return 0, err
	}
	imported := 0
	var problems []error
	for _, job := range jobs {
		outputDir, err := s.Downloads.OutputDirectory(job.ID)
		if err != nil {
			continue
		}
		candidates, err := library.Scan(ctx, outputDir)
		if err != nil || len(candidates) == 0 {
			continue
		}
		representative := representativeCandidate(candidates, job.Title)
		if groups := packGroups(candidates); len(groups) > 1 {
			if err := s.rejectLegacyPack(ctx, cfg, job, representative, groups); err != nil {
				problems = append(problems, err)
			}
			continue
		}
		sources := sourcesForCandidate(job.Files, candidates, representative)
		if len(sources) == 0 {
			continue
		}
		movie, err := s.movieForCandidate(ctx, cfg, representative)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		acquisition := Acquisition{MovieID: movie.ID, JobID: job.ID, ReleaseID: job.ReleaseID, Title: job.Title, Status: "importing"}
		if err := s.saveAcquisitionStatus(ctx, acquisition); err != nil {
			problems = append(problems, err)
			continue
		}
		count, err := s.runImport(ctx, cfg, movie.ID, job, acquisition, sources)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		imported += count
	}
	return imported, errors.Join(problems...)
}

func (s *Service) rejectLegacyPack(ctx context.Context, cfg Config, job downloads.Job, representative library.Candidate, groups map[string]library.Candidate) error {
	movie, err := s.movieForCandidate(ctx, cfg, representative)
	if err != nil {
		return err
	}
	message := sanitizeMessage(cfg, packMessage(groups))
	acquisition := Acquisition{MovieID: movie.ID, JobID: job.ID, ReleaseID: job.ReleaseID, Title: job.Title, Status: "import-failed", Error: message}
	if err := s.saveAcquisitionStatus(ctx, acquisition); err != nil {
		return err
	}
	if err := s.patchMovieError(ctx, movie.ID, message); err != nil {
		return err
	}
	_ = s.Store.Event(ctx, movie.ID, "import-failed", truncate(message, maxTextRunes))
	return nil
}

func representativeCandidate(candidates []library.Candidate, jobTitle string) library.Candidate {
	title := normalizeReleaseTitle(jobTitle)
	best, found := library.Candidate{}, false
	for _, candidate := range candidates {
		name := normalizeReleaseTitle(candidate.Title)
		if name == "" || !strings.Contains(title, name) {
			continue
		}
		if !found || candidate.Size > best.Size {
			best, found = candidate, true
		}
	}
	if found {
		return best
	}
	best = candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Size > best.Size {
			best = candidate
		}
	}
	return best
}

func (s *Service) movieForCandidate(ctx context.Context, cfg Config, candidate library.Candidate) (Movie, error) {
	imdbID := strings.ToLower(strings.TrimSpace(candidate.IMDbID))
	if imdbID != "" {
		if movie, err := s.Store.FindIMDb(ctx, imdbID); err == nil {
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
	if imdbID == "" && s.metadataConfigured(cfg) {
		imdbID = s.resolveCandidateIMDb(ctx, candidate)
	}
	if imdbID != "" {
		return s.Add(ctx, AddInput{IMDbID: imdbID, Monitored: false})
	}
	return s.Add(ctx, AddInput{Metadata: metadata.Title{Title: candidate.Title, Year: candidate.Year}, Monitored: false})
}

func (s *Service) resolveCandidateIMDb(ctx context.Context, candidate library.Candidate) string {
	client, err := s.metadataClient(ctx)
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(candidate.Title)
	if title == "" {
		return ""
	}
	results, err := client.Search(ctx, title, 1)
	if err != nil {
		return ""
	}
	key := scanTitleKey(title)
	matched := ""
	for _, result := range results {
		if result.Type != "" && result.Type != "movie" {
			continue
		}
		if scanTitleKey(result.Title) != key {
			continue
		}
		if candidate.Year > 0 && result.Year != candidate.Year {
			continue
		}
		if !metadata.ValidIMDbID(result.IMDbID) {
			continue
		}
		if matched != "" {
			return ""
		}
		matched = result.IMDbID
	}
	return matched
}

func sourcesForCandidate(files []downloads.OutputFile, candidates []library.Candidate, representative library.Candidate) []library.Source {
	identity := candidateIdentity(representative)
	groups := map[string]bool{importGroup(representative.Path): true}
	for _, candidate := range candidates {
		if candidateIdentity(candidate) == identity {
			groups[importGroup(candidate.Path)] = true
		}
	}
	sources := make([]library.Source, 0, len(files))
	for _, file := range files {
		if !isMovieVideo(file.Name) {
			continue
		}
		if groups[importGroup(file.Name)] || file.Name == representative.Path {
			sources = append(sources, library.Source{Name: file.Name, Size: file.Size})
		}
	}
	return movieSources(sources)
}

func (s *Service) importSources(ctx context.Context, cfg Config, movie Movie, sourceRoot string, sources []library.Source, qualityHint, jobID string) (Movie, error) {
	saved, paths, err := s.importSourcesResult(ctx, cfg, movie, sourceRoot, sources, importQuality{Title: qualityHint}, jobID)
	if err != nil {
		return Movie{}, err
	}
	if jobID == "" {
		s.notifyImport(ctx, cfg, saved, paths)
	}
	return saved, nil
}

func (s *Service) importSourcesResult(ctx context.Context, cfg Config, movie Movie, sourceRoot string, sources []library.Source, info importQuality, jobID string) (Movie, []string, error) {
	destRoot, ok := rootByID(cfg, movie.RootID)
	if !ok {
		return Movie{}, nil, fmt.Errorf("%w: the movie root folder is not configured", ErrInvalid)
	}
	sources = movieSources(sources)
	if len(sources) == 0 {
		return Movie{}, nil, errors.New("movies: no movie video files to import")
	}
	templateQuality := info.Quality
	if strings.TrimSpace(templateQuality) == "" {
		templateQuality = quality.Parse(sources[0].Name).Quality
	}
	opts := s.importOptions(cfg, movie, destRoot, sourceRoot, templateQuality)
	present, recovered, err := s.recoverSources(ctx, jobID, sourceRoot, destRoot, sources)
	if err != nil {
		return Movie{}, nil, err
	}
	published := make([]library.File, 0, len(sources))
	names := make([]string, 0, len(sources))
	if len(present) > 0 {
		planned, err := library.Preview(opts, present)
		if err != nil {
			return Movie{}, nil, fmt.Errorf("movies: plan import: %w", err)
		}
		if err := s.planJournal(ctx, jobID, sourceRoot, destRoot.Path, present, planned); err != nil {
			return Movie{}, nil, err
		}
		files, err := library.Import(ctx, opts, present)
		if err != nil {
			return Movie{}, nil, fmt.Errorf("movies: import files: %w", err)
		}
		if len(files) != len(present) {
			return Movie{}, nil, errors.New("movies: import published an unexpected file set")
		}
		published = append(published, files...)
		for _, source := range present {
			names = append(names, source.Name)
		}
	}
	presentNames := make(map[string]bool, len(present))
	for _, source := range present {
		presentNames[source.Name] = true
	}
	for _, source := range sources {
		if !presentNames[source.Name] {
			names = append(names, source.Name)
		}
	}
	published = append(published, recovered...)
	if len(published) == 0 {
		return Movie{}, nil, errors.New("movies: no movie files were imported")
	}
	profile, profileErr := s.Store.Profile(ctx, movie.ProfileID)
	if profileErr != nil {
		profile = quality.Profile{}
	}
	files := mergePublishedFiles(destRoot, movie.Files, evaluatePublished(profile, info, present, published), time.Now().UTC())
	saved, err := s.Store.Patch(ctx, movie.ID, map[string]any{"files": files, "error": ""})
	if err != nil {
		return Movie{}, nil, err
	}
	if err := s.completeJournal(ctx, jobID, names); err != nil {
		return Movie{}, nil, err
	}
	_ = s.Store.Event(ctx, saved.ID, "imported", truncate("Imported "+strings.Join(publishedPaths(published), ", "), maxTextRunes))
	decorated, err := s.decorated(ctx, saved)
	if err != nil {
		return Movie{}, nil, err
	}
	return decorated, publishedPaths(published), nil
}

func evaluatePublished(profile quality.Profile, info importQuality, present []library.Source, published []library.File) []evaluatedFile {
	evaluated := make([]evaluatedFile, 0, len(published))
	for i, file := range published {
		name := file.Path
		if i < len(present) {
			name = present[i].Name
		}
		decision := quality.Evaluate(profile, firstNonEmpty(info.Title, name), file.Size, nil)
		score := decision.Score
		if info.Score > score {
			score = info.Score
		}
		evaluated = append(evaluated, evaluatedFile{file: file, quality: firstNonEmpty(info.Quality, decision.Details.Quality), score: score})
	}
	return evaluated
}

func (s *Service) importOptions(cfg Config, movie Movie, root RootFolder, sourceRoot, qualityName string) library.Options {
	existing := make([]string, 0, len(movie.Files))
	for _, file := range movie.Files {
		if file.RootID == root.ID {
			existing = append(existing, file.Path)
		}
	}
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
		Metadata:       movie.Metadata,
		Quality:        qualityName,
		WriteNFO:       cfg.WriteNFO,
		Existing:       existing,
	}
}

func movieSources(sources []library.Source) []library.Source {
	filtered := make([]library.Source, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		name := filepath.ToSlash(strings.TrimSpace(source.Name))
		if name == "" || seen[name] || !isMovieVideo(name) {
			continue
		}
		seen[name] = true
		filtered = append(filtered, library.Source{Name: name, Size: source.Size})
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
	return filtered
}

func isMovieVideo(name string) bool {
	if !movieVideoExtensions[strings.ToLower(path.Ext(name))] {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if movieSamplePattern.MatchString(strings.TrimSuffix(part, path.Ext(part))) {
			return false
		}
	}
	return true
}

func importGroup(name string) string {
	name = filepath.ToSlash(name)
	dir := path.Dir(name)
	stem := strings.TrimSuffix(path.Base(name), path.Ext(name))
	stem = strings.TrimSpace(moviePartPattern.ReplaceAllString(stem, ""))
	return strings.ToLower(dir + "/" + stem)
}

func publishedPaths(files []library.File) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func mergePublishedFiles(root RootFolder, existing []File, published []evaluatedFile, now time.Time) []File {
	byPath := make(map[string]evaluatedFile, len(published))
	for _, item := range published {
		byPath[item.file.Path] = item
	}
	files := make([]File, 0, len(existing)+len(published))
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
				continue
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
		files = append(files, File{
			RootID: root.ID, Path: item.file.Path, Size: item.file.Size,
			Quality: item.quality, Score: item.score, ImportedAt: now,
		})
	}
	return files
}

func (s *Service) journalRows(ctx context.Context, jobID string) (map[string]journalFile, error) {
	rows, err := s.Store.pool.Query(ctx,
		`SELECT name, root_path, path, size, sha256, ready FROM download_library_files WHERE job_id = $1`, jobID)
	if err != nil {
		return nil, errors.New("movies: the import journal could not be loaded")
	}
	defer rows.Close()
	journal := map[string]journalFile{}
	for rows.Next() {
		var entry journalFile
		if err := rows.Scan(&entry.Name, &entry.RootPath, &entry.Path, &entry.Size, &entry.SHA256, &entry.Ready); err != nil {
			return nil, errors.New("movies: the import journal could not be loaded")
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
		return errors.New("movies: the import plan does not match the source files")
	}
	for i, source := range sources {
		digest, err := fileSHA256(ctx, sourceRoot, source.Name)
		if err != nil {
			return fmt.Errorf("movies: hash source %s: %w", source.Name, err)
		}
		if _, err := s.Store.pool.Exec(ctx,
			`INSERT INTO download_library_files (job_id, name, root_path, path, size, sha256, ready)
			 VALUES ($1, $2, $3, $4, $5, $6, false)
			 ON CONFLICT (job_id, name) DO UPDATE SET root_path = EXCLUDED.root_path, path = EXCLUDED.path,
			 size = EXCLUDED.size, sha256 = EXCLUDED.sha256, ready = false`,
			jobID, source.Name, destRootPath, planned[i].Path, planned[i].Size, digest); err != nil {
			return errors.New("movies: the import journal could not be saved")
		}
	}
	return nil
}

func (s *Service) completeJournal(ctx context.Context, jobID string, names []string) error {
	if jobID == "" || len(names) == 0 {
		return nil
	}
	if _, err := s.Store.pool.Exec(ctx,
		`UPDATE download_library_files SET ready = true WHERE job_id = $1 AND name = ANY($2)`, jobID, names); err != nil {
		return errors.New("movies: the import journal could not be completed")
	}
	return nil
}

func (s *Service) recoverSources(ctx context.Context, jobID, sourceRoot string, destRoot RootFolder, sources []library.Source) ([]library.Source, []library.File, error) {
	var journal map[string]journalFile
	if jobID != "" {
		var err error
		journal, err = s.journalRows(ctx, jobID)
		if err != nil {
			return nil, nil, err
		}
	}
	present := make([]library.Source, 0, len(sources))
	recovered := make([]library.File, 0)
	for _, source := range sources {
		if handle, err := library.Open(sourceRoot, source.Name); err == nil {
			info, statErr := handle.Stat()
			handle.Close()
			if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
				present = append(present, source)
				continue
			}
		}
		entry, ok := journal[source.Name]
		if !ok {
			return nil, nil, fmt.Errorf("movies: source file %s is missing", source.Name)
		}
		if entry.RootPath != destRoot.Path {
			return nil, nil, fmt.Errorf("movies: %s is missing after a move and its recorded destination changed", source.Name)
		}
		if !entry.Ready && !fileMatchesSHA256(ctx, entry.RootPath, entry.Path, entry.SHA256) {
			return nil, nil, fmt.Errorf("movies: %s is missing after a move and the recorded destination does not match its hash", source.Name)
		}
		handle, err := library.Open(entry.RootPath, entry.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("movies: %s is missing after a move and its destination is gone", source.Name)
		}
		info, statErr := handle.Stat()
		handle.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil, nil, fmt.Errorf("movies: %s is missing after a move and its destination is not usable", source.Name)
		}
		recovered = append(recovered, library.File{Path: entry.Path, Size: entry.Size})
	}
	return present, recovered, nil
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

func (s *Service) saveAcquisitionStatus(ctx context.Context, acquisition Acquisition) error {
	if err := s.Store.SaveAcquisition(ctx, acquisition); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func (s *Service) patchMovieError(ctx context.Context, movieID, message string) error {
	if _, err := s.Store.Patch(ctx, movieID, map[string]any{"error": message}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
