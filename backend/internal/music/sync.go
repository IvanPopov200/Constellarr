package music

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

// maxImportAttempts bounds automatic import retries before the release is abandoned.
const maxImportAttempts = 3

// SyncDownloads reconciles completed downloads into the library, recovering after restarts.
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
	return imported, errors.Join(problems...)
}

func (s *Service) syncAcquisition(ctx context.Context, cfg Config, acquisition Acquisition) (int, error) {
	switch acquisition.Status {
	case "imported", "superseded":
		return 0, nil
	}
	album, err := s.Store.Album(ctx, acquisition.AlbumID)
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
		return 0, s.failAcquisition(ctx, acquisition, "the download no longer exists")
	}
	if err != nil {
		return 0, err
	}
	switch job.Status {
	case "completed":
		return s.importCompleted(ctx, cfg, album, job, acquisition)
	case "failed":
		if acquisition.Status == "failed" {
			return 0, nil
		}
		return 0, s.failAcquisition(ctx, acquisition, firstNonEmpty(job.Error, "the download failed"))
	default:
		if acquisition.Status != job.Status {
			acquisition.Status = job.Status
			if err := s.Store.SaveAcquisition(ctx, acquisition); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
}

// failAcquisition records the failure, blocklists the release, and leaves the album wanted.
func (s *Service) failAcquisition(ctx context.Context, acquisition Acquisition, message string) error {
	message = truncate(firstNonEmpty(message, "the download failed"), maxTextRunes)
	acquisition.Status = "failed"
	acquisition.Error = message
	if err := s.Store.SaveAcquisition(ctx, acquisition); err != nil {
		return err
	}
	// A failed release is never grabbed again automatically.
	if acquisition.ReleaseID != "" {
		_ = s.Store.Block(ctx, acquisition.AlbumID, acquisition.ReleaseID)
	}
	_ = s.Store.Event(ctx, acquisition.AlbumID, "", "failed", message)
	// Only the error field belongs to this operation.
	_ = s.Store.PatchAlbumData(ctx, acquisition.AlbumID, map[string]any{"error": message})
	return nil
}

func (s *Service) importCompleted(ctx context.Context, cfg Config, album Album, job downloads.Job, acquisition Acquisition) (int, error) {
	imported := 0
	err := s.withAlbumLock(ctx, album.ID, func(ctx context.Context) error {
		fresh, err := s.Store.Album(ctx, album.ID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		artist, err := s.Store.Artist(ctx, fresh.ArtistID)
		if err != nil {
			return err
		}
		root, ok := rootByID(cfg, fresh.RootID)
		if !ok {
			return fmt.Errorf("%w: the album root folder is not configured", ErrInvalid)
		}
		outputDir, err := s.Downloads.OutputDirectory(job.ID)
		if err != nil {
			return fmt.Errorf("%w: the download output is not available", ErrUnsafe)
		}
		mode, err := s.importModeFor(ctx, cfg, job.ID)
		if err != nil {
			return err
		}
		saved, err := s.runImport(ctx, cfg, fresh, artist, root, outputDir, "", acquisition.Override, mode)
		if err != nil {
			return err
		}
		acquisition.Status = "imported"
		acquisition.Error = ""
		if err := s.Store.SaveAcquisition(ctx, acquisition); err != nil {
			return err
		}
		_ = s.Store.Event(ctx, saved.ID, saved.ArtistID, "imported", fmt.Sprintf("Imported %d file(s)", len(saved.Files)))
		imported = 1
		return nil
	})
	switch {
	case errors.Is(err, errAlbumBusy):
		return 0, nil
	case err != nil:
		// An import that keeps failing releases the album for a new release instead of retrying forever.
		message := truncate(err.Error(), maxTextRunes)
		acquisition.Attempts++
		acquisition.Error = message
		if acquisition.Attempts >= maxImportAttempts {
			acquisition.Status = "failed"
			if acquisition.ReleaseID != "" {
				_ = s.Store.Block(ctx, acquisition.AlbumID, acquisition.ReleaseID)
			}
		} else {
			acquisition.Status = "import-failed"
		}
		if saveErr := s.Store.SaveAcquisition(ctx, acquisition); saveErr != nil {
			return 0, saveErr
		}
		_ = s.Store.Event(ctx, acquisition.AlbumID, "", "error", "Import failed: "+message)
		return 0, err
	}
	return imported, nil
}

// importModeFor asks the download manager for the effective mode; torrent payloads keep seeding.
func (s *Service) importModeFor(ctx context.Context, cfg Config, jobID string) (string, error) {
	mode, err := s.Downloads.ImportMode(ctx, jobID, cfg.ImportMode)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(mode) == "" {
		mode = cfg.ImportMode
	}
	return normalizeImportMode(mode), nil
}

// importModeForSource protects torrent payloads when a folder is imported manually.
func (s *Service) importModeForSource(ctx context.Context, cfg Config, sourceRoot string) (string, error) {
	for _, output := range s.torrentOutputs(ctx) {
		if pathWithin(output.directory, sourceRoot) {
			return s.importModeFor(ctx, cfg, output.jobID)
		}
	}
	return normalizeImportMode(cfg.ImportMode), nil
}

// torrentOutput pairs a torrent job with the payload folder the manager reports for it.
type torrentOutput struct {
	jobID     string
	directory string
}

// maxTorrentOutputs bounds payload discovery during manual imports.
const maxTorrentOutputs = 32

func (s *Service) torrentOutputs(ctx context.Context) []torrentOutput {
	ids, err := s.Store.TorrentJobIDs(ctx, maxTorrentOutputs)
	if err != nil {
		return nil
	}
	outputs := make([]torrentOutput, 0, len(ids))
	for _, id := range ids {
		directory, err := s.Downloads.OutputDirectory(id)
		if err != nil || directory == "" {
			continue
		}
		outputs = append(outputs, torrentOutput{jobID: id, directory: directory})
	}
	return outputs
}

// pathWithin reports whether target is base itself or lives below it.
func pathWithin(base, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// runImport imports audio files from one source folder into the album's library root.
func (s *Service) runImport(ctx context.Context, cfg Config, album Album, artist Artist, root RootFolder, sourceRoot, onlyRel string, override bool, importMode string) (Album, error) {
	sources, err := scanAudioSources(ctx, sourceRoot)
	if err != nil {
		return Album{}, err
	}
	if onlyRel != "" {
		filtered := make([]sourceFile, 0, 1)
		for _, source := range sources {
			if source.rel == onlyRel {
				filtered = append(filtered, source)
			}
		}
		if len(filtered) == 0 {
			return Album{}, errNoAudio
		}
		sources = filtered
	}
	if err := probeSources(ctx, cfg, sourceRoot, sources); err != nil {
		return Album{}, err
	}
	target := albumIdentity{artist: artist.Name, album: album.Title, year: album.Year}
	if identity, err := sourceAlbumIdentity(sources); err != nil {
		return Album{}, err
	} else if !override && !identityMatches(target, identity) {
		return Album{}, fmt.Errorf("%w: the files appear to be %q, not %q", ErrConflict, identity.album, album.Title)
	}
	profile, ok := profileByID(cfg, album.ProfileID)
	if !ok {
		return Album{}, fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
	}
	assignments, tracks, err := matchTracks(profile, album, sources)
	if err != nil {
		return Album{}, err
	}
	folder, planned, err := planAlbumImport(cfg, album, artist, assignments)
	if err != nil {
		return Album{}, err
	}
	previous := album.Files
	owned := map[string]bool{}
	for _, file := range previous {
		if file.RootID == root.ID {
			owned[file.Path] = true
		}
	}
	keep := mergeImportedFiles(previous, planned, root.ID)
	published, err := publishFiles(ctx, importMode, root, sourceRoot, folder, planned, owned, keep)
	if err != nil {
		return Album{}, err
	}
	album.Files = mergePublished(previous, published)
	linkTracks(tracks, planned, published, root.ID)
	if cover, err := importCover(ctx, cfg, album, root, sourceRoot, folder); err == nil && cover != "" {
		album.CoverPath = cover
	}
	// An import owns files, tracks, and its status only; monitoring and metadata stay untouched.
	fields := map[string]any{
		"files":        album.Files,
		"error":        "",
		"lastSearchAt": nil,
	}
	if album.CoverPath != "" {
		fields["coverPath"] = album.CoverPath
	}
	if err := s.Store.PatchAlbum(ctx, album.ID, fields, tracks); err != nil {
		return Album{}, err
	}
	album.Tracks = tracks
	return album, nil
}

// mergeImportedFiles keeps previously imported files unless the new import replaces their position.
func mergeImportedFiles(previous []File, planned []plannedFile, rootID string) []string {
	replacedPath := map[string]bool{}
	replacedPosition := map[[2]int]bool{}
	for _, plan := range planned {
		replacedPath[plan.destRel] = true
		if plan.number > 0 {
			replacedPosition[[2]int{plan.disc, plan.number}] = true
		}
	}
	keep := make([]string, 0, len(previous)+len(planned))
	seen := map[string]bool{}
	for _, file := range previous {
		if file.RootID != rootID {
			continue
		}
		if replacedPath[file.Path] {
			continue
		}
		if file.Number > 0 && replacedPosition[[2]int{file.Disc, file.Number}] {
			continue
		}
		if seen[file.Path] {
			continue
		}
		seen[file.Path] = true
		keep = append(keep, file.Path)
	}
	for _, plan := range planned {
		if !seen[plan.destRel] {
			seen[plan.destRel] = true
			keep = append(keep, plan.destRel)
		}
	}
	sort.Strings(keep)
	return keep
}

// mergePublished drops previous records that the new import replaced at the same path or position.
func mergePublished(previous, published []File) []File {
	pathTaken := map[string]bool{}
	positionTaken := map[[2]int]bool{}
	for _, file := range published {
		pathTaken[file.Path] = true
		if file.Number > 0 {
			positionTaken[[2]int{file.Disc, file.Number}] = true
		}
	}
	merged := make([]File, 0, len(previous)+len(published))
	merged = append(merged, published...)
	for _, file := range previous {
		if pathTaken[file.Path] {
			continue
		}
		if file.Number > 0 && positionTaken[[2]int{file.Disc, file.Number}] {
			continue
		}
		merged = append(merged, file)
	}
	return merged
}

// linkTracks records the published path on each matched track.
func linkTracks(tracks []Track, planned []plannedFile, published []File, rootID string) {
	byTrack := map[string]File{}
	for index, plan := range planned {
		if index < len(published) {
			byTrack[plan.track.ID] = published[index]
		}
	}
	for index := range tracks {
		track := &tracks[index]
		file, ok := byTrack[track.ID]
		if !ok {
			continue
		}
		track.File = &TrackFile{
			RootID: rootID, Path: file.Path, Size: file.Size, Format: file.Format,
			BitrateKbps: file.BitrateKbps, Lossless: file.Lossless, Score: file.Score, ImportedAt: file.ImportedAt,
		}
		track.Missing = false
	}
}

// Import imports a folder or file the user selected, matching the album by metadata when needed.
func (s *Service) Import(ctx context.Context, input ImportInput) (Album, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	sourceRoot, onlyRel, err := s.resolveImportSource(ctx, cfg, input)
	if err != nil {
		return Album{}, err
	}
	var album Album
	if strings.TrimSpace(input.AlbumID) != "" {
		album, err = s.Store.Album(ctx, strings.TrimSpace(input.AlbumID))
		if err != nil {
			return Album{}, err
		}
	} else {
		album, err = s.matchImportAlbum(ctx, cfg, sourceRoot, onlyRel)
		if err != nil {
			return Album{}, err
		}
	}
	artist, err := s.Store.Artist(ctx, album.ArtistID)
	if err != nil {
		return Album{}, err
	}
	root, ok := rootByID(cfg, album.RootID)
	if !ok {
		return Album{}, fmt.Errorf("%w: the album root folder is not configured", ErrInvalid)
	}
	importMode, err := s.importModeForSource(ctx, cfg, sourceRoot)
	if err != nil {
		return Album{}, err
	}
	acquired := false
	err = s.withAlbumLock(ctx, album.ID, func(ctx context.Context) error {
		acquired = true
		fresh, err := s.Store.Album(ctx, album.ID)
		if err != nil {
			return err
		}
		saved, err := s.runImport(ctx, cfg, fresh, artist, root, sourceRoot, onlyRel, true, importMode)
		if err != nil {
			return err
		}
		_ = s.Store.Event(ctx, saved.ID, saved.ArtistID, "imported", fmt.Sprintf("Imported %d file(s) manually", len(saved.Files)))
		album = saved
		return nil
	})
	if errors.Is(err, errAlbumBusy) {
		return Album{}, ErrConflict
	}
	if err != nil {
		return Album{}, err
	}
	if !acquired {
		return Album{}, ErrConflict
	}
	return s.Album(ctx, album.ID)
}

// matchImportAlbum identifies an album from the files before the user picks one.
func (s *Service) matchImportAlbum(ctx context.Context, cfg Config, sourceRoot, onlyRel string) (Album, error) {
	sources, err := scanAudioSources(ctx, sourceRoot)
	if err != nil {
		return Album{}, err
	}
	if onlyRel != "" {
		for index := range sources {
			if sources[index].rel == onlyRel {
				sources = []sourceFile{sources[index]}
				break
			}
		}
	}
	if err := probeSources(ctx, cfg, sourceRoot, sources); err != nil {
		return Album{}, err
	}
	identity, err := sourceAlbumIdentity(sources)
	if err != nil {
		return Album{}, err
	}
	if identity.album == "" {
		return Album{}, fmt.Errorf("%w: the files do not identify an album; choose one manually", ErrNotFound)
	}
	return s.matchCatalogAlbum(ctx, identity)
}

// matchCatalogAlbum finds the catalog album matching a sanitized identity.
func (s *Service) matchCatalogAlbum(ctx context.Context, identity albumIdentity) (Album, error) {
	albums, err := s.Store.Albums(ctx)
	if err != nil {
		return Album{}, err
	}
	artistName := normalizeText(identity.artist)
	albumTitle := normalizeText(identity.album)
	var best *Album
	for index := range albums {
		candidate := albums[index]
		if normalizeText(candidate.Title) != albumTitle {
			continue
		}
		name := normalizeText(s.artistName(ctx, candidate))
		if artistName != "" && name != "" && name != artistName && !strings.Contains(name, artistName) && !strings.Contains(artistName, name) {
			continue
		}
		if identity.year > 0 && candidate.Year > 0 && abs(candidate.Year-identity.year) > 1 {
			continue
		}
		value := candidate
		best = &value
		break
	}
	if best == nil {
		return Album{}, fmt.Errorf("%w: no library album matches %q", ErrNotFound, identity.album)
	}
	return s.Store.Album(ctx, best.ID)
}

// resolveImportSource accepts a music root, the download directory, or a torrent payload.
func (s *Service) resolveImportSource(ctx context.Context, cfg Config, input ImportInput) (string, string, error) {
	raw := strings.TrimSpace(input.Path)
	if raw == "" {
		return "", "", fmt.Errorf("%w: an import path is required", ErrInvalid)
	}
	bases := make([]string, 0, len(cfg.RootFolders)+2)
	for _, root := range cfg.RootFolders {
		bases = append(bases, root.Path)
	}
	if directory := strings.TrimSpace(s.Downloads.Config().Directory); directory != "" {
		bases = append(bases, directory)
	}
	bases = append(bases, s.torrentOutputDirectories(ctx)...)
	for _, base := range bases {
		rel, ok := relWithin(base, raw)
		if !ok {
			continue
		}
		full := filepath.Join(base, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return "", "", fmt.Errorf("%w: the import path is not a regular file or folder", ErrUnsafe)
		}
		if info.IsDir() {
			return full, "", nil
		}
		return filepath.Dir(full), filepath.ToSlash(rel), nil
	}
	return "", "", fmt.Errorf("%w: the import path must be inside a configured music root", ErrUnsafe)
}

// torrentOutputDirectories lists payload roots so manual imports can target them safely.
func (s *Service) torrentOutputDirectories(ctx context.Context) []string {
	outputs := s.torrentOutputs(ctx)
	directories := make([]string, 0, len(outputs))
	for _, output := range outputs {
		directories = append(directories, output.directory)
	}
	return directories
}

// relWithinRoot returns a local relative path for a target below base.
func relWithin(base, target string) (string, bool) {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) {
		if !filepath.IsLocal(target) {
			return "", false
		}
		return filepath.ToSlash(target), true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Scan lists album folders in a music root with their catalog match.
func (s *Service) Scan(ctx context.Context, rootID string) ([]Candidate, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.chooseRoot(cfg, rootID)
	if err != nil {
		return nil, err
	}
	select {
	case s.lockSlots <- struct{}{}:
		defer func() { <-s.lockSlots }()
	default:
		return nil, ErrConflict
	}
	return s.scanLibrary(ctx, cfg, root)
}

const maxScanGroups = 200

func (s *Service) scanLibrary(ctx context.Context, cfg Config, root RootFolder) ([]Candidate, error) {
	handle, err := os.OpenRoot(root.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: the music root is not readable", ErrUnsafe)
	}
	defer handle.Close()
	groups := map[string][]string{}
	err = fs.WalkDir(handle.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if p != "." && skipImportDirs[strings.ToLower(entry.Name())] {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || entry.Type()&fs.ModeSymlink != 0 || !audioExtension(p) {
			return nil
		}
		rel := filepath.ToSlash(p)
		dir := path.Dir(rel)
		if len(groups) >= maxScanGroups && groups[dir] == nil {
			return nil
		}
		groups[dir] = append(groups[dir], rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(groups))
	for dir := range groups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	candidates := make([]Candidate, 0, len(dirs))
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := s.scanCandidate(ctx, cfg, root, dir, groups[dir])
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (s *Service) scanCandidate(ctx context.Context, cfg Config, root RootFolder, dir string, paths []string) Candidate {
	sort.Strings(paths)
	candidate := Candidate{Path: dir, Discs: 1}
	files := make([]sourceFile, 0, len(paths))
	for _, rel := range paths {
		info, err := os.Stat(filepath.Join(root.Path, filepath.FromSlash(rel)))
		if err != nil || info.Size() <= 0 {
			continue
		}
		files = append(files, sourceFile{rel: rel, size: info.Size(), inferred: inferTrack(rel)})
		candidate.Size += info.Size()
	}
	candidate.Tracks = len(files)
	discs := map[int]bool{}
	bestScore := -1
	for index := range files {
		discs[files[index].inferred.disc] = true
		format := formatFromExtension(files[index].rel)
		if score, _ := fileQuality(QualityProfile{}, format, 0, losslessFormats[format]); score > bestScore {
			bestScore, candidate.Format, candidate.BitrateKbps = score, format, parseFormat(files[index].rel).BitrateKbps
		}
	}
	candidate.Discs = max(len(discs), 1)
	sample := files
	if len(sample) > 3 {
		sample = sample[:3]
	}
	if err := probeSources(ctx, cfg, root.Path, sample); err == nil && len(sample) > 0 {
		identity, err := sourceAlbumIdentity(sample)
		if err == nil {
			candidate.Artist, candidate.Album, candidate.Year = identity.artist, identity.album, identity.year
		}
		if info := sample[0].info; info.Audio {
			candidate.Format = firstNonEmpty(info.Format, candidate.Format)
			candidate.BitrateKbps = info.BitrateKbps
		}
	}
	if candidate.Album == "" {
		if artist, album, year := parseReleaseFolder(path.Base(dir)); album != "" {
			candidate.Artist, candidate.Album, candidate.Year = artist, album, year
		} else {
			candidate.Album = path.Base(dir)
		}
	}
	matched, err := s.matchCatalogAlbum(ctx, albumIdentity{artist: candidate.Artist, album: candidate.Album, year: candidate.Year})
	if err == nil {
		candidate.MatchedAlbumID = matched.ID
	} else if errors.Is(err, ErrNotFound) {
		candidate.Error = "no matching album in the library"
	} else {
		candidate.Error = truncate(err.Error(), maxTextRunes)
	}
	return candidate
}

// Rename previews or applies the configured folder and file templates to imported files.
func (s *Service) Rename(ctx context.Context, albumID string, preview bool) (RenameResult, error) {
	album, err := s.Store.Album(ctx, albumID)
	if err != nil {
		return RenameResult{}, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return RenameResult{}, err
	}
	if len(album.Files) == 0 {
		return RenameResult{Files: []RenameFile{}}, nil
	}
	artist, err := s.Store.Artist(ctx, album.ArtistID)
	if err != nil {
		return RenameResult{}, err
	}
	root, ok := rootByID(cfg, album.RootID)
	if !ok {
		return RenameResult{}, fmt.Errorf("%w: the album root folder is not configured", ErrInvalid)
	}
	assignments := assignmentsForFiles(album)
	folder, planned, err := planAlbumImport(cfg, album, artist, assignments)
	if err != nil {
		return RenameResult{}, err
	}
	renameable := make([]RenameFile, 0, len(planned))
	plans := make([]plannedFile, 0, len(planned))
	for _, plan := range planned {
		from := plan.source.rel
		if from == "" {
			continue
		}
		if from != plan.destRel {
			renameable = append(renameable, RenameFile{From: from, To: plan.destRel})
		}
		plans = append(plans, plan)
	}
	if preview {
		return RenameResult{Files: renameable, Applied: false}, nil
	}
	busy := false
	err = s.withAlbumLock(ctx, album.ID, func(ctx context.Context) error {
		busy = true
		applied, err := s.applyRename(ctx, cfg, album, root, folder, plans)
		if err != nil {
			return err
		}
		renameable = applied
		return nil
	})
	if errors.Is(err, errAlbumBusy) {
		return RenameResult{}, ErrConflict
	}
	if err != nil {
		return RenameResult{}, err
	}
	if !busy {
		return RenameResult{}, ErrConflict
	}
	return RenameResult{Files: renameable, Applied: true}, nil
}

// assignmentsForFiles rebuilds import assignments from stored file records.
func assignmentsForFiles(album Album) []trackAssignment {
	byPosition := map[[2]int]Track{}
	byTitle := map[string]Track{}
	for _, track := range album.Tracks {
		if track.Number > 0 {
			byPosition[[2]int{max(track.Disc, 1), track.Number}] = track
		}
		if key := normalizeText(track.Title); key != "" {
			byTitle[key] = track
		}
	}
	files := append([]File(nil), album.Files...)
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Disc != files[j].Disc {
			return files[i].Disc < files[j].Disc
		}
		if files[i].Number != files[j].Number {
			return files[i].Number < files[j].Number
		}
		return files[i].Path < files[j].Path
	})
	assignments := make([]trackAssignment, 0, len(files))
	for _, file := range files {
		track, ok := byPosition[[2]int{max(file.Disc, 1), file.Number}]
		if !ok && file.Number == 0 {
			track, ok = byTitle[normalizeText(file.TrackTitle)]
		}
		if !ok {
			track = Track{ID: newID(), Disc: max(file.Disc, 1), Number: file.Number, Title: file.TrackTitle}
		}
		source := sourceFile{rel: file.Path, size: file.Size, inferred: inferTrack(file.Path)}
		assignments = append(assignments, trackAssignment{
			source: &source, track: track, disc: max(file.Disc, 1), number: track.Number, title: track.Title,
			format: file.Format, bitrate: file.BitrateKbps, lossless: file.Lossless, score: file.Score,
			originalExt: strings.ToLower(path.Ext(file.Path)),
		})
	}
	return assignments
}

func (s *Service) applyRename(ctx context.Context, cfg Config, album Album, root RootFolder, folder string, plans []plannedFile) ([]RenameFile, error) {
	handle, err := os.OpenRoot(root.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: the library root is not writable", ErrUnsafe)
	}
	defer handle.Close()
	owned := map[string]bool{}
	for _, file := range album.Files {
		owned[file.Path] = true
	}
	rec := &importRollback{}
	var applied []RenameFile
	fail := func(err error) ([]RenameFile, error) {
		rec.rollback(handle)
		return nil, err
	}
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		from := plan.source.rel
		if from == "" || from == plan.destRel {
			continue
		}
		if err := mkdirAllTracked(handle, path.Dir(plan.destRel), &rec.created); err != nil {
			return fail(err)
		}
		if info, err := handle.Lstat(plan.destRel); err == nil {
			if !info.Mode().IsRegular() {
				return fail(fmt.Errorf("%w: %s is not a regular file", ErrConflict, plan.destRel))
			}
			if err := archiveOwned(handle, plan.destRel, rec); err != nil {
				return fail(err)
			}
		}
		if err := renameNoReplace(handle, from, plan.destRel); err != nil {
			return fail(err)
		}
		rec.published = append(rec.published, plan.destRel)
		rec.archived = append(rec.archived, archiveRecord{original: from, archived: plan.destRel})
		applied = append(applied, RenameFile{From: from, To: plan.destRel})
	}
	moved := map[string]string{}
	for _, change := range applied {
		moved[change.From] = change.To
		delete(owned, change.From)
	}
	for index := range album.Files {
		if to, ok := moved[album.Files[index].Path]; ok {
			album.Files[index].Path = to
		}
	}
	for index := range album.Tracks {
		if album.Tracks[index].File == nil {
			continue
		}
		if to, ok := moved[album.Tracks[index].File.Path]; ok {
			album.Tracks[index].File.Path = to
		}
	}
	if cover := album.CoverPath; cover != "" && folder != "" && strings.HasPrefix(cover, folder+"/") == false {
		if base := path.Base(cover); base != "" {
			dest := path.Join(folder, base)
			if _, err := handle.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
				if err := renameNoReplace(handle, cover, dest); err == nil {
					album.CoverPath = dest
				}
			}
		}
	}
	// Remove the rollback record for successful moves before saving.
	rec.published = nil
	rec.archived = nil
	// A rename owns file and track paths only.
	fields := map[string]any{"files": album.Files}
	if album.CoverPath != "" {
		fields["coverPath"] = album.CoverPath
	}
	if err := s.Store.PatchAlbum(ctx, album.ID, fields, album.Tracks); err != nil {
		return nil, err
	}
	_ = s.Store.Event(ctx, album.ID, album.ArtistID, "renamed", fmt.Sprintf("Renamed %d file(s)", len(applied)))
	return applied, nil
}
