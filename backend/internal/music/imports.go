package music

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	importModeCopy     = "copy"
	importModeHardlink = "hardlink"
	importModeMove     = "move"
	maxImportFiles     = 500
	maxCoverImageBytes = 20 << 20
	tempMarker         = ".constellarr-part-"
	copyBufferBytes    = 1 << 20
)

var (
	errNoAudio   = errors.New("music: the download contains no music files")
	errAmbiguous = errors.New("music: the files do not identify one album")
	errNotAudio  = errors.New("music: a file with an audio extension is not audio")
)

var skipImportDirs = map[string]bool{
	".recycle": true, "temp": true, "tmp": true, ".tmp": true, "sample": true, "samples": true,
}

var imageExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

var (
	coverNamePattern = regexp.MustCompile(`(?i)(cover|folder|front|album)`)
	parenYearPattern = regexp.MustCompile(`\((\d{4})\)`)
)

type sourceFile struct {
	rel      string
	size     int64
	info     audioInfo
	probed   bool
	inferred inferredTrack
}

type inferredTrack struct {
	artist string
	album  string
	year   int
	disc   int
	number int
	title  string
}

type trackAssignment struct {
	source      *sourceFile
	track       Track
	disc        int
	number      int
	title       string
	format      string
	bitrate     int
	lossless    bool
	score       int
	qualityOK   bool
	originalExt string
}

type plannedFile struct {
	trackAssignment
	destRel string
}

type albumIdentity struct {
	artist string
	album  string
	year   int
}

// scanAudioSources lists audio files below a download root, rejecting symlinks and video containers.
func scanAudioSources(ctx context.Context, sourceRoot string) ([]sourceFile, error) {
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: the source folder is not readable", ErrUnsafe)
	}
	defer root.Close()
	files := make([]sourceFile, 0, 16)
	err = fs.WalkDir(root.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
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
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: the source contains a symbolic link", ErrUnsafe)
		}
		if !entry.Type().IsRegular() || !audioExtension(p) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() <= 0 {
			return nil
		}
		if len(files) >= maxImportFiles {
			return fmt.Errorf("%w: the source contains too many files", ErrInvalid)
		}
		rel := filepath.ToSlash(p)
		files = append(files, sourceFile{rel: rel, size: info.Size(), inferred: inferTrack(rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errNoAudio
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

// probeSources reads real metadata for each file when the configured helper is available.
func probeSources(ctx context.Context, cfg Config, sourceRoot string, files []sourceFile) error {
	if !ffprobeAvailable(cfg.FFprobePath) {
		return nil
	}
	for i := range files {
		info, err := probeAudio(ctx, cfg.FFprobePath, filepath.Join(sourceRoot, filepath.FromSlash(files[i].rel)))
		if err != nil {
			if errors.Is(err, errNotAudio) {
				return fmt.Errorf("%w: %s is not an audio file", ErrInvalid, files[i].rel)
			}
			return fmt.Errorf("music: %s: %w", files[i].rel, err)
		}
		files[i].info = info
		files[i].probed = true
	}
	return nil
}

func sourceIdentity(source sourceFile) albumIdentity {
	tags := source.info.Tags
	artist := firstNonEmpty(tags["albumartist"], tags["artist"], source.inferred.artist)
	album := firstNonEmpty(tags["album"], source.inferred.album)
	year := yearFromDate(tags["date"])
	if year == 0 {
		year = source.inferred.year
	}
	return albumIdentity{artist: artist, album: album, year: year}
}

// sourceAlbumIdentity refuses to import when the files name clearly different albums.
func sourceAlbumIdentity(files []sourceFile) (albumIdentity, error) {
	type group struct {
		identity albumIdentity
		names    map[string]bool
	}
	groups := map[string]*group{}
	for _, source := range files {
		identity := sourceIdentity(source)
		if strings.TrimSpace(identity.album) == "" {
			continue
		}
		key := normalizeText(identity.artist) + "|" + normalizeText(identity.album) + "|" + yearBucket(identity.year)
		current, ok := groups[key]
		if !ok {
			current = &group{identity: identity, names: map[string]bool{}}
			groups[key] = current
		}
		current.names[strings.TrimSpace(identity.album)] = true
	}
	if len(groups) == 0 {
		return albumIdentity{}, nil
	}
	if len(groups) > 1 {
		labels := make([]string, 0, 3)
		for _, current := range groups {
			label := strings.TrimSpace(current.identity.album)
			if current.identity.artist != "" {
				label = current.identity.artist + " - " + label
			}
			labels = append(labels, label)
		}
		sort.Strings(labels)
		return albumIdentity{}, fmt.Errorf("%w: the files name different albums (%s)", errAmbiguous, strings.Join(labels, "; "))
	}
	for _, current := range groups {
		return current.identity, nil
	}
	return albumIdentity{}, nil
}

func yearBucket(year int) string {
	if year == 0 {
		return ""
	}
	return strconv.Itoa(year)
}

// identityMatches compares sanitized identities, allowing a one-year pressing difference.
func identityMatches(target, source albumIdentity) bool {
	if normalizeText(target.album) == "" || normalizeText(source.album) == "" {
		return true
	}
	if normalizeText(target.album) != normalizeText(source.album) {
		return false
	}
	targetArtist, sourceArtist := normalizeText(target.artist), normalizeText(source.artist)
	if targetArtist != "" && sourceArtist != "" && targetArtist != sourceArtist {
		return false
	}
	if target.year > 0 && source.year > 0 && abs(target.year-source.year) > 1 {
		return false
	}
	return true
}

// matchTracks maps every file to a distinct catalog track or a new bonus track.
func matchTracks(profile QualityProfile, album Album, files []sourceFile) ([]trackAssignment, []Track, error) {
	tracks := append([]Track(nil), album.Tracks...)
	byMB := map[string]int{}
	byPosition := map[[2]int]int{}
	byTitle := map[string][]int{}
	usedNumbers := map[[2]int]bool{}
	for index, track := range tracks {
		if track.MusicBrainzID != "" {
			byMB[track.MusicBrainzID] = index
		}
		if track.Number > 0 {
			byPosition[[2]int{max(track.Disc, 1), track.Number}] = index
			usedNumbers[[2]int{max(track.Disc, 1), track.Number}] = true
		}
		if key := normalizeText(track.Title); key != "" {
			byTitle[key] = append(byTitle[key], index)
		}
	}
	assigned := map[int]bool{}
	nextNumber := map[int]int{}
	for disc := 1; disc <= 9; disc++ {
		for key := range usedNumbers {
			if key[0] == disc && key[1] > nextNumber[disc] {
				nextNumber[disc] = key[1]
			}
		}
	}

	assignments := make([]trackAssignment, 0, len(files))
	for index := range files {
		source := &files[index]
		tags := source.info.Tags
		disc := max(firstPositive(intValue(tags["disc"]), source.inferred.disc), 1)
		number := firstPositive(intValue(tags["track"]), source.inferred.number)
		title := firstNonEmpty(tags["title"], source.inferred.title, stemOf(source.rel))
		match := -1
		if mbid := strings.TrimSpace(tags["musicbrainz_trackid"]); mbid != "" {
			if candidate, ok := byMB[mbid]; ok && !assigned[candidate] {
				match = candidate
			}
		}
		if match < 0 && number > 0 {
			if candidate, ok := byPosition[[2]int{disc, number}]; ok {
				if assigned[candidate] {
					return nil, nil, fmt.Errorf("%w: two files claim disc %d track %d", errAmbiguous, disc, number)
				}
				match = candidate
			}
		}
		if match < 0 && number == 0 {
			if candidates := byTitle[normalizeText(title)]; len(candidates) == 1 && !assigned[candidates[0]] {
				match = candidates[0]
			} else if len(candidates) > 1 {
				return nil, nil, fmt.Errorf("%w: two tracks share the title %q", errAmbiguous, title)
			}
		}
		track := Track{ID: newID(), Disc: disc, Number: number, Title: title}
		if match >= 0 {
			track = tracks[match]
			assigned[match] = true
		} else {
			if number == 0 {
				nextNumber[disc]++
				number = nextNumber[disc]
				track.Number = number
			}
			if usedNumbers[[2]int{disc, number}] {
				return nil, nil, fmt.Errorf("%w: two files claim disc %d track %d", errAmbiguous, disc, number)
			}
			usedNumbers[[2]int{disc, number}] = true
			tracks = append(tracks, track)
		}
		format := source.info.Format
		bitrate := source.info.BitrateKbps
		lossless := source.info.Lossless
		if !source.probed {
			parsed := parseFormat(source.rel)
			format = firstNonEmpty(parsed.Format, formatFromExtension(source.rel))
			bitrate = parsed.BitrateKbps
			lossless = losslessFormats[format]
		}
		score, allowed := fileQuality(profile, format, bitrate, lossless)
		track.Artist = firstNonEmpty(tags["artist"], source.inferred.artist, track.Artist, album.ArtistName)
		track.File = &TrackFile{Size: source.size, Format: format, BitrateKbps: bitrate, Lossless: lossless, Score: score}
		assignments = append(assignments, trackAssignment{
			source: source, track: track, disc: track.Disc, number: track.Number, title: track.Title,
			format: format, bitrate: bitrate, lossless: lossless, score: score, qualityOK: allowed,
			originalExt: strings.ToLower(path.Ext(source.rel)),
		})
	}
	sort.SliceStable(assignments, func(i, j int) bool {
		if assignments[i].disc != assignments[j].disc {
			return assignments[i].disc < assignments[j].disc
		}
		if assignments[i].number != assignments[j].number {
			return assignments[i].number < assignments[j].number
		}
		return assignments[i].source.rel < assignments[j].source.rel
	})
	return assignments, tracks, nil
}

// planAlbumImport renders deterministic destinations for every assignment without touching the library.
func planAlbumImport(cfg Config, album Album, artist Artist, assignments []trackAssignment) (string, []plannedFile, error) {
	base := albumTokenValues(album, artist)
	if base["format"] == "" {
		base["format"] = bestAssignmentFormat(assignments)
		base["quality"] = base["format"]
	}
	folder, err := musicFolderRel(cfg, base)
	if err != nil {
		return "", nil, err
	}
	discs := 1
	for _, assignment := range assignments {
		discs = max(discs, assignment.disc)
	}
	seen := map[string]bool{}
	planned := make([]plannedFile, 0, len(assignments))
	for _, assignment := range assignments {
		name, err := musicFileName(cfg, discs, fileTokenValues(base, assignment), assignment.originalExt)
		if err != nil {
			return "", nil, err
		}
		dest := name
		if folder != "" {
			dest = path.Join(folder, name)
		}
		dest = uniqueRel(dest, seen)
		seen[strings.ToLower(dest)] = true
		planned = append(planned, plannedFile{trackAssignment: assignment, destRel: dest})
	}
	return folder, planned, nil
}

func bestAssignmentFormat(assignments []trackAssignment) string {
	best, bestScore := "", -1
	for _, assignment := range assignments {
		if assignment.score > bestScore {
			best, bestScore = assignment.format, assignment.score
		}
	}
	return best
}

func albumTokenValues(album Album, artist Artist) map[string]string {
	values := map[string]string{
		"artist":      album.ArtistName,
		"album":       album.Title,
		"year":        yearString(album.Year),
		"date":        album.ReleaseDate,
		"type":        album.Type,
		"format":      album.Format,
		"quality":     album.Format,
		"mbAlbumId":   album.MusicBrainzID,
		"mbArtistId":  artist.MusicBrainzID,
		"disc":        "1",
		"track":       "",
		"title":       "",
		"trackArtist": "",
		"original":    "",
	}
	sanitizeValues(values)
	return values
}

func fileTokenValues(base map[string]string, assignment trackAssignment) map[string]string {
	values := make(map[string]string, len(base)+4)
	for key, value := range base {
		values[key] = value
	}
	values["track"] = strconv.Itoa(assignment.number)
	values["disc"] = strconv.Itoa(assignment.disc)
	values["title"] = assignment.title
	values["trackArtist"] = firstNonEmpty(assignment.track.Artist, base["artist"])
	values["format"] = assignment.format
	values["quality"] = assignment.format
	values["original"] = stemOf(assignment.source.rel)
	sanitizeValues(values)
	return values
}

// sanitizeValues keeps separator characters out of substituted path components.
func sanitizeValues(values map[string]string) {
	for key, value := range values {
		values[key] = sanitizeComponent(value)
	}
}

// publishFiles stages, verifies, and publishes planned files, recycling replaced originals.
func publishFiles(ctx context.Context, importMode string, root RootFolder, sourceRoot, folder string, planned []plannedFile, owned map[string]bool, keep []string) ([]File, error) {
	rootHandle, err := os.OpenRoot(root.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: the library root is not writable", ErrUnsafe)
	}
	defer rootHandle.Close()
	srcHandle, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: the source folder is not readable", ErrUnsafe)
	}
	defer srcHandle.Close()
	if err := removeStaleTemps(rootHandle, folder); err != nil {
		return nil, err
	}

	rec := &importRollback{}
	fail := func(err error) ([]File, error) {
		rec.rollback(rootHandle)
		return nil, err
	}
	mode := normalizeImportMode(importMode)
	files := make([]File, 0, len(planned))
	for _, plan := range planned {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := mkdirAllTracked(rootHandle, path.Dir(plan.destRel), &rec.created); err != nil {
			return fail(err)
		}
		keep, replace, err := resolveDestination(ctx, rootHandle, srcHandle, plan, owned)
		if err != nil {
			return fail(err)
		}
		if replace {
			if err := archiveOwned(rootHandle, plan.destRel, rec); err != nil {
				return fail(err)
			}
		}
		if !keep {
			tempRel, err := stageSource(ctx, rootHandle, srcHandle, plan, mode)
			if err != nil {
				return fail(err)
			}
			if err := publishNoReplace(rootHandle, tempRel, plan.destRel); err != nil {
				rootHandle.Remove(tempRel)
				return fail(err)
			}
			if err := verifyPublished(rootHandle, plan.destRel, plan.source.size); err != nil {
				return fail(err)
			}
		}
		files = append(files, plan.fileRecord(root.ID))
	}
	if err := publishSidecars(ctx, mode, rootHandle, srcHandle, folder, planned, rec); err != nil {
		return fail(err)
	}
	if err := recycleStale(rootHandle, keep, owned, folder, rec); err != nil {
		return fail(err)
	}
	if mode == importModeMove {
		if err := deleteSources(srcHandle, planned); err != nil {
			return files, err
		}
	}
	return files, nil
}

// sidecarExtensions keep cue sheets, lyrics, and rip logs next to the imported audio.
var sidecarExtensions = []string{".cue", ".lrc", ".log"}

const (
	maxSidecarBytes = 2 << 20
	maxSidecarFiles = 50
)

// publishSidecars copies audio companions beside their file and any album-level sheet.
func publishSidecars(ctx context.Context, mode string, root, srcRoot *os.Root, folder string, planned []plannedFile, rec *importRollback) error {
	if folder == "" {
		return nil
	}
	copied := map[string]bool{}
	for _, plan := range planned {
		stem := strings.TrimSuffix(plan.source.rel, path.Ext(plan.source.rel))
		for _, extension := range sidecarExtensions {
			source := stem + extension
			dest := path.Join(folder, sanitizeComponent(stemOf(plan.destRel))+extension)
			if err := copySidecar(ctx, root, srcRoot, source, dest, copied, rec, mode); err != nil {
				return err
			}
		}
	}
	// Remaining sheets, lyrics, and logs belong to the album folder.
	remaining := 0
	err := fs.WalkDir(srcRoot.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			if p != "." && entry != nil && entry.IsDir() && skipImportDirs[strings.ToLower(entry.Name())] {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !sidecarExtension(p) {
			return nil
		}
		source := filepath.ToSlash(p)
		if copied[source] {
			return nil
		}
		if remaining >= maxSidecarFiles {
			return nil
		}
		remaining++
		dest := path.Join(folder, sanitizeComponent(stemOf(source))+strings.ToLower(path.Ext(source)))
		return copySidecar(ctx, root, srcRoot, source, dest, copied, rec, mode)
	})
	if err != nil {
		return err
	}
	return nil
}

func sidecarExtension(name string) bool {
	extension := strings.ToLower(path.Ext(name))
	for _, known := range sidecarExtensions {
		if extension == known {
			return true
		}
	}
	return false
}

func copySidecar(ctx context.Context, root, srcRoot *os.Root, source, dest string, copied map[string]bool, rec *importRollback, mode string) error {
	if copied[source] {
		return nil
	}
	info, err := srcRoot.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSidecarBytes {
		return nil
	}
	copied[source] = true
	if err := mkdirAllTracked(root, path.Dir(dest), &rec.created); err != nil {
		return err
	}
	if existing, err := root.Lstat(dest); err == nil && existing.Mode().IsRegular() {
		same, err := sameContent(ctx, root, dest, existing.Size(), srcRoot, source, info.Size())
		if err != nil {
			return err
		}
		if same {
			return nil
		}
		if err := archiveOwned(root, dest, rec); err != nil {
			return err
		}
	}
	if err := stageSidecar(ctx, root, srcRoot, source, dest, info.Size()); err != nil {
		return err
	}
	rec.published = append(rec.published, dest)
	if mode == importModeMove {
		srcRoot.Remove(source)
	}
	return nil
}

func stageSidecar(ctx context.Context, root, srcRoot *os.Root, source, dest string, size int64) error {
	sourceHandle, err := srcRoot.Open(source)
	if err != nil {
		return fmt.Errorf("%w: a source sidecar disappeared", ErrUnsafe)
	}
	defer sourceHandle.Close()
	tempRel := path.Join(path.Dir(dest), tempName(path.Base(dest)))
	target, err := root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%w: the sidecar could not be staged", ErrUnsafe)
	}
	written, err := copyContext(ctx, target, sourceHandle, size)
	if err == nil {
		err = target.Sync()
	}
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	if err == nil && written != size {
		err = fmt.Errorf("%w: copied %d of %d sidecar bytes", ErrUnsafe, written, size)
	}
	if err == nil {
		err = root.Chmod(tempRel, 0o644)
	}
	if err != nil {
		root.Remove(tempRel)
		return err
	}
	if err := publishNoReplace(root, tempRel, dest); err != nil {
		root.Remove(tempRel)
		return err
	}
	return nil
}

// resolveDestination reports identical content to adopt and owned replacements to archive.
func resolveDestination(ctx context.Context, rootHandle, srcHandle *os.Root, plan plannedFile, owned map[string]bool) (bool, bool, error) {
	info, err := rootHandle.Lstat(plan.destRel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() {
		return false, false, fmt.Errorf("%w: %s is not a regular file", ErrConflict, plan.destRel)
	}
	same, err := sameContent(ctx, rootHandle, plan.destRel, info.Size(), srcHandle, plan.source.rel, plan.source.size)
	if err != nil {
		return false, false, err
	}
	if same {
		return true, false, nil
	}
	if !owned[plan.destRel] {
		return false, false, fmt.Errorf("%w: %s already exists and is not owned by this album", ErrConflict, plan.destRel)
	}
	return false, true, nil
}

// recycleStale keeps originals by recycling owned files the new layout no longer uses.
func recycleStale(root *os.Root, keepPaths []string, owned map[string]bool, folder string, rec *importRollback) error {
	keep := make(map[string]bool, len(keepPaths))
	for _, rel := range keepPaths {
		keep[rel] = true
	}
	stale := make([]string, 0, len(owned))
	for rel := range owned {
		if keep[rel] {
			continue
		}
		if folder != "" && rel != folder && !strings.HasPrefix(rel, folder+"/") {
			continue
		}
		stale = append(stale, rel)
	}
	sort.Strings(stale)
	for _, rel := range stale {
		if err := archiveOwned(root, rel, rec); err != nil {
			return err
		}
	}
	return nil
}

// archiveOwned moves a replaced file below .recycle and records it for rollback.
func archiveOwned(root *os.Root, rel string, rec *importRollback) error {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrConflict, rel)
	}
	for attempt := 0; attempt < 16; attempt++ {
		dest := recycleRel(rel, attempt)
		if dir := path.Dir(dest); dir != "." {
			if err := mkdirAllTracked(root, dir, &rec.created); err != nil {
				return err
			}
		}
		if err := renameNoReplace(root, rel, dest); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		rec.archived = append(rec.archived, archiveRecord{original: rel, archived: dest})
		return nil
	}
	return fmt.Errorf("%w: no free recycle name for %s", ErrConflict, rel)
}

func recycleRel(rel string, attempt int) string {
	base := path.Join(".recycle", rel)
	if attempt == 0 {
		return base
	}
	ext := path.Ext(base)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), attempt, ext)
}

func (p plannedFile) fileRecord(rootID string) File {
	return File{
		RootID: rootID, Path: p.destRel, Size: p.source.size, Format: p.format, BitrateKbps: p.bitrate,
		Lossless: p.lossless, Score: p.score, Disc: p.disc, Number: p.number, TrackTitle: p.title,
		ImportedAt: time.Now().UTC(),
	}
}

type archiveRecord struct {
	original string
	archived string
}

type importRollback struct {
	published []string
	archived  []archiveRecord
	created   []string
}

func (r *importRollback) rollback(root *os.Root) {
	for i := len(r.published) - 1; i >= 0; i-- {
		if info, err := root.Lstat(r.published[i]); err == nil && info.Mode().IsRegular() {
			root.Remove(r.published[i])
		}
	}
	for i := len(r.archived) - 1; i >= 0; i-- {
		record := r.archived[i]
		if dir := path.Dir(record.original); dir != "." {
			root.MkdirAll(dir, 0o755)
		}
		renameNoReplace(root, record.archived, record.original)
	}
	for i := len(r.created) - 1; i >= 0; i-- {
		root.Remove(r.created[i])
	}
}

func mkdirAllTracked(root *os.Root, dir string, created *[]string) error {
	if dir == "." || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		current := strings.Join(parts[:i+1], "/")
		err := root.Mkdir(current, 0o755)
		if err == nil {
			*created = append(*created, current)
			continue
		}
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: the library folder could not be created", ErrUnsafe)
		}
	}
	return nil
}

func removeStaleTemps(root *os.Root, folder string) error {
	if folder == "" {
		return nil
	}
	entries, err := fs.ReadDir(root.FS(), folder)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), tempMarker) {
			continue
		}
		root.Remove(path.Join(folder, entry.Name()))
	}
	return nil
}

// stageSource prepares the destination temp file by hardlink when possible, otherwise by copy.
func stageSource(ctx context.Context, rootHandle, srcHandle *os.Root, plan plannedFile, mode string) (string, error) {
	tempRel := path.Join(path.Dir(plan.destRel), tempName(path.Base(plan.destRel)))
	if mode != importModeCopy {
		if err := linkStaged(rootHandle, srcHandle, plan, tempRel); err == nil {
			return tempRel, nil
		}
	}
	source, err := srcHandle.Open(plan.source.rel)
	if err != nil {
		return "", fmt.Errorf("%w: a source file disappeared", ErrUnsafe)
	}
	defer source.Close()
	dst, err := rootHandle.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("%w: the destination could not be staged", ErrUnsafe)
	}
	written, err := copyContext(ctx, dst, source, plan.source.size)
	if err == nil {
		err = dst.Sync()
	}
	if closeErr := dst.Close(); err == nil {
		err = closeErr
	}
	if err == nil && (written != plan.source.size || written <= 0) {
		err = fmt.Errorf("%w: copied %d of %d bytes", ErrUnsafe, written, plan.source.size)
	}
	if err == nil {
		err = rootHandle.Chmod(tempRel, 0o644)
	}
	if err != nil {
		rootHandle.Remove(tempRel)
		return "", err
	}
	return tempRel, nil
}

// linkStaged links through the pinned roots and verifies the linked inode before publishing.
func linkStaged(rootHandle, srcHandle *os.Root, plan plannedFile, tempRel string) error {
	source, err := srcHandle.Open(plan.source.rel)
	if err != nil {
		return err
	}
	info, err := source.Stat()
	source.Close()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != plan.source.size {
		return fmt.Errorf("%w: %s changed after planning", ErrUnsafe, plan.source.rel)
	}
	sourcePath := srcHandle.Name()
	rootPath := rootHandle.Name()
	sourceAbs := filepath.Join(sourcePath, filepath.FromSlash(plan.source.rel))
	tempAbs := filepath.Join(rootPath, filepath.FromSlash(tempRel))
	if err := os.Link(sourceAbs, tempAbs); err != nil {
		return err
	}
	linked, err := rootHandle.Open(tempRel)
	var linkedInfo fs.FileInfo
	if err == nil {
		linkedInfo, err = linked.Stat()
		linked.Close()
	}
	if err != nil || !linkedInfo.Mode().IsRegular() || !os.SameFile(info, linkedInfo) {
		rootHandle.Remove(tempRel)
		return fmt.Errorf("%w: the hardlink for %s is not the planned file", ErrUnsafe, plan.source.rel)
	}
	return nil
}

func tempName(name string) string {
	return name + tempMarker + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func normalizeImportMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case importModeHardlink, "link":
		return importModeHardlink
	case importModeMove:
		return importModeMove
	default:
		return importModeCopy
	}
}

func deleteSources(srcHandle *os.Root, planned []plannedFile) error {
	var failures []error
	for _, plan := range planned {
		if err := srcHandle.Remove(plan.source.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, fmt.Errorf("music: removing source %s: %w", plan.source.rel, err))
		}
	}
	return errors.Join(failures...)
}

func verifyPublished(root *os.Root, rel string, size int64) error {
	handle, err := root.Open(rel)
	if err != nil {
		return fmt.Errorf("%w: the published file is missing", ErrUnsafe)
	}
	info, err := handle.Stat()
	handle.Close()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("%w: the published file is incomplete", ErrUnsafe)
	}
	return nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader, size int64) (int64, error) {
	buffer := make([]byte, copyBufferBytes)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if written+int64(n) > size {
				return written, fmt.Errorf("%w: the source grew while copying", ErrUnsafe)
			}
			wrote, writeErr := dst.Write(buffer[:n])
			written += int64(wrote)
			if writeErr != nil {
				return written, writeErr
			}
			if wrote != n {
				return written, fmt.Errorf("%w: short write", ErrUnsafe)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

func sameContent(ctx context.Context, aRoot *os.Root, aRel string, aSize int64, bRoot *os.Root, bRel string, bSize int64) (bool, error) {
	if aSize != bSize || aSize <= 0 {
		return false, nil
	}
	aSum, err := hashFile(ctx, aRoot, aRel)
	if err != nil {
		return false, err
	}
	bSum, err := hashFile(ctx, bRoot, bRel)
	if err != nil {
		return false, err
	}
	return aSum == bSum, nil
}

func hashFile(ctx context.Context, root *os.Root, rel string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	file, err := root.Open(rel)
	if err != nil {
		return sum, err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, copyBufferBytes)
	for {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return sum, readErr
		}
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

// importCover copies source art or the configured cover provider image and reports its library path.
func importCover(ctx context.Context, cfg Config, album Album, root RootFolder, sourceRoot, folder string) (string, error) {
	if folder == "" {
		return "", nil
	}
	if rel, data, ok := findSourceImage(sourceRoot); ok {
		dest := path.Join(folder, "cover"+strings.ToLower(path.Ext(rel)))
		if err := writeCoverArt(root, album, dest, data); err != nil {
			return "", err
		}
		return dest, nil
	}
	if strings.TrimSpace(cfg.CoverArtURL) == "" || album.MusicBrainzID == "" {
		return "", nil
	}
	imageBase, err := coverImageBase(cfg.CoverArtURL)
	if err != nil || imageBase == nil {
		return "", nil
	}
	client, err := newBrainz(cfg.MusicBrainzURL, time.Duration(cfg.MusicBrainzRateMs)*time.Millisecond)
	if err != nil {
		return "", nil
	}
	data, contentType, err := client.cover(ctx, imageBase, album.MusicBrainzID)
	if err != nil {
		return "", nil
	}
	extension := ".jpg"
	if contentType == "image/png" {
		extension = ".png"
	}
	dest := path.Join(folder, "cover"+extension)
	if err := writeCoverArt(root, album, dest, data); err != nil {
		return "", err
	}
	return dest, nil
}

func writeCoverArt(root RootFolder, album Album, dest string, data []byte) error {
	if len(data) == 0 || len(data) > maxCoverImageBytes {
		return nil
	}
	handle, err := os.OpenRoot(root.Path)
	if err != nil {
		return fmt.Errorf("%w: the library root is not writable", ErrUnsafe)
	}
	defer handle.Close()
	rec := &importRollback{}
	if err := mkdirAllTracked(handle, path.Dir(dest), &rec.created); err != nil {
		return err
	}
	if info, err := handle.Lstat(dest); err == nil && info.Mode().IsRegular() && album.CoverPath == dest {
		if err := archiveOwned(handle, dest, rec); err != nil {
			return err
		}
	}
	temp := path.Join(path.Dir(dest), tempName(path.Base(dest)))
	file, err := handle.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("%w: the cover could not be written", ErrUnsafe)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		handle.Remove(temp)
		return fmt.Errorf("%w: the cover could not be written", ErrUnsafe)
	}
	if err := publishNoReplace(handle, temp, dest); err != nil {
		handle.Remove(temp)
		return err
	}
	return nil
}

// findSourceImage prefers named cover art, then the largest image in the source.
func findSourceImage(sourceRoot string) (string, []byte, bool) {
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return "", nil, false
	}
	defer root.Close()
	var (
		bestRel  string
		bestSize int64
		namedRel string
	)
	_ = fs.WalkDir(root.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if p != "." && skipImportDirs[strings.ToLower(entry.Name())] {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		extension := strings.ToLower(path.Ext(p))
		if !imageExtensions[extension] {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 || info.Size() > maxCoverImageBytes {
			return nil
		}
		if coverNamePattern.MatchString(stemOf(p)) {
			if namedRel == "" || info.Size() > bestSize {
				namedRel = filepath.ToSlash(p)
			}
		}
		if info.Size() > bestSize {
			bestRel, bestSize = filepath.ToSlash(p), info.Size()
		}
		return nil
	})
	chosen := namedRel
	if chosen == "" {
		chosen = bestRel
	}
	if chosen == "" {
		return "", nil, false
	}
	file, err := root.Open(chosen)
	if err != nil {
		return "", nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCoverImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxCoverImageBytes {
		return "", nil, false
	}
	return chosen, data, true
}

func stemOf(rel string) string {
	base := path.Base(rel)
	return strings.TrimSuffix(base, path.Ext(base))
}

func formatFromExtension(rel string) string {
	switch strings.ToLower(path.Ext(rel)) {
	case ".flac":
		return "flac"
	case ".m4a", ".aac":
		return "aac"
	case ".mp3":
		return "mp3"
	case ".ogg", ".oga":
		return "vorbis"
	case ".opus":
		return "opus"
	case ".wav":
		return "wav"
	case ".aiff", ".aif":
		return "aiff"
	case ".ape":
		return "ape"
	case ".wv":
		return "wavpack"
	case ".wma":
		return "wma"
	case ".dsf", ".dff":
		return "dsd"
	default:
		return ""
	}
}

// inferTrack guesses identity from release folder and file names when ffprobe is unavailable.
func inferTrack(rel string) inferredTrack {
	inferred := inferredTrack{disc: 1}
	if disc, ok := discFromPath(rel); ok {
		inferred.disc = disc
	}
	if dir := path.Dir(rel); dir != "." {
		for _, part := range strings.Split(dir, "/") {
			if artist, album, year := parseReleaseFolder(part); album != "" {
				inferred.artist, inferred.album, inferred.year = artist, album, year
				break
			}
			if inferred.artist == "" {
				inferred.artist = tidyInfer(part)
			}
		}
	}
	stem := stemOf(rel)
	if match := leadingTrackPattern.FindStringSubmatch(stem); match != nil {
		inferred.number = atoiSafe(match[1])
		inferred.title = tidyInfer(match[2])
		return inferred
	}
	stem = tidyInfer(stem)
	inferred.title = stem
	fields := splitFields(stem)
	switch {
	case len(fields) >= 4 && isTrackNumber(fields[2]):
		inferred.artist = firstNonEmpty(inferred.artist, fields[0])
		inferred.album = firstNonEmpty(inferred.album, fields[1])
		inferred.number = atoiSafe(fields[2])
		inferred.title = strings.Join(fields[3:], " ")
	case len(fields) >= 3 && isTrackNumber(fields[0]):
		inferred.number = atoiSafe(fields[0])
		inferred.title = strings.Join(fields[1:], " ")
	case len(fields) >= 3 && isTrackNumber(fields[1]):
		inferred.artist = firstNonEmpty(inferred.artist, fields[0])
		inferred.number = atoiSafe(fields[1])
		inferred.title = strings.Join(fields[2:], " ")
	case len(fields) >= 2 && isTrackNumber(fields[0]):
		inferred.number = atoiSafe(fields[0])
		inferred.title = strings.Join(fields[1:], " ")
	case len(fields) >= 1 && isTrackNumber(fields[0]):
		inferred.number = atoiSafe(fields[0])
	}
	return inferred
}

var (
	trackNumberPattern  = regexp.MustCompile(`^\d{1,3}$`)
	leadingTrackPattern = regexp.MustCompile(`^(\d{1,3})[\s._)-]+(.+)$`)
	discNumberPattern   = regexp.MustCompile(`(?i)^(?:cd|disc|disk|vol(?:ume)?)\s*0*(\d{1,2})$`)
)

func isTrackNumber(token string) bool {
	return trackNumberPattern.MatchString(strings.TrimSpace(token))
}

func atoiSafe(value string) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number < 0 || number > 1000 {
		return 0
	}
	return number
}

func discFromPath(rel string) (int, bool) {
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if match := discNumberPattern.FindStringSubmatch(strings.TrimSpace(part)); match != nil {
			if disc := atoiSafe(match[1]); disc > 0 {
				return disc, true
			}
		}
	}
	return 0, false
}

func splitFields(value string) []string {
	parts := strings.Split(value, " - ")
	fields := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.Trim(strings.TrimSpace(part), "._"); trimmed != "" {
			fields = append(fields, trimmed)
		}
	}
	return fields
}

// parseReleaseFolder reads "Artist - Album (Year)" folder names.
func parseReleaseFolder(folder string) (string, string, int) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return "", "", 0
	}
	year := 0
	if match := parenYearPattern.FindStringSubmatch(folder); match != nil {
		if parsed, err := strconv.Atoi(match[1]); err == nil && parsed >= 1900 && parsed <= time.Now().Year()+2 {
			year = parsed
		}
		folder = strings.Replace(folder, match[0], " ", 1)
	}
	parts := strings.SplitN(stripReleaseJunk(folder), " - ", 2)
	if len(parts) != 2 {
		return "", "", 0
	}
	artist, album := tidyInfer(parts[0]), tidyInfer(parts[1])
	if artist == "" || album == "" {
		return "", "", 0
	}
	return artist, album, year
}

var releaseJunkPattern = regexp.MustCompile(`(?i)\b(flac|mp3|aac|alac|wav|24bit|16bit|web|webrip|cd|vinyl|remaster(?:ed)?|deluxe|retail|scene|lossless|hires)\b|\[[^\]]*\]`)

func stripReleaseJunk(value string) string {
	return strings.Join(strings.Fields(releaseJunkPattern.ReplaceAllString(value, " ")), " ")
}

func tidyInfer(value string) string {
	value = strings.NewReplacer("_", " ", ".", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func intValue(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	digits := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '/' {
			break
		}
		if raw[i] < '0' || raw[i] > '9' {
			return 0
		}
		digits = append(digits, raw[i])
	}
	if len(digits) == 0 {
		return 0
	}
	return atoiSafe(string(digits))
}

func yearString(year int) string {
	if year <= 0 {
		return ""
	}
	return strconv.Itoa(year)
}
