package music

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
)

const (
	albumLockBase        = 0x4D75736963416C62
	discoverPageSize     = 20
	maxDiscoverPage      = 50
	maxQueryRunes        = 256
	maxAlbumTitleRunes   = 512
	maxArtistsRefresh    = 5
	maxScheduledSearches = 10
	providerTimeout      = 20 * time.Second
)

// errAlbumBusy reports that another process already holds the album lock.
var errAlbumBusy = errors.New("music: the album is busy")

type Service struct {
	Store     *Store
	Downloads *downloads.Manager
	syncMu    sync.Mutex
	startOnce sync.Once
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	lockSlots chan struct{}
}

// New builds the music service on the shared pool and download manager.
func New(ctx context.Context, pool *pgxpool.Pool, manager *downloads.Manager) (*Service, error) {
	if pool == nil {
		return nil, errors.New("music: a PostgreSQL pool is required")
	}
	if manager == nil {
		return nil, errors.New("music: the download manager is required")
	}
	defaults := defaultConfig(manager.Config().Directory)
	store, err := NewStore(ctx, pool, defaults)
	if err != nil {
		return nil, err
	}
	if err := ensureRoots(defaults.RootFolders); err != nil {
		return nil, err
	}
	return &Service{Store: store, Downloads: manager, lockSlots: store.OperationSlots()}, nil
}

func defaultConfig(directory string) Config {
	return Config{
		RootFolders:       []RootFolder{{ID: "music", Path: filepath.Join(directory, "library", "music")}},
		FolderTemplate:    defaultFolderTemplate,
		FileTemplate:      defaultFileTemplate,
		ImportMode:        importModeCopy,
		FFprobePath:       firstNonEmpty(os.Getenv("FFPROBE_PATH"), defaultFFprobe),
		MusicBrainzURL:    defaultMusicBrainzURL,
		MusicBrainzRateMs: 1100,
		CoverArtURL:       defaultCoverArtURL,
		PollMinutes:       30,
		SearchHours:       12,
		RetryFailed:       true,
		QualityProfiles:   defaultQualityProfiles(),
		DefaultProfileID:  "standard",
	}
}

func defaultQualityProfiles() []QualityProfile {
	return []QualityProfile{
		{ID: "standard", Name: "Standard", Formats: []string{"flac", "alac", "aac", "mp3"}, MinBitrateKbps: 256, Cutoff: "flac", Upgrade: true},
		{ID: "lossless", Name: "Lossless", Formats: []string{"flac", "alac", "wav"}, LosslessOnly: true, Cutoff: "flac"},
		{ID: "any", Name: "Any audio", Formats: []string{}, Cutoff: "", Upgrade: false},
	}
}

// ensureRoots rejects roots that could escape into unrelated filesystem locations.
func ensureRoots(roots []RootFolder) error {
	for _, root := range roots {
		path := strings.TrimSpace(root.Path)
		if !filepath.IsAbs(path) || len(path) > maxPathBytes {
			return fmt.Errorf("%w: music root folders must be absolute paths", ErrInvalid)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("%w: the music root folder could not be created", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) ConfigView(ctx context.Context) (Config, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	cfg.MusicBrainzReady = strings.TrimSpace(cfg.MusicBrainzURL) != ""
	cfg.FFprobeAvailable = ffprobeAvailable(cfg.FFprobePath)
	return cfg, nil
}

func (s *Service) SetConfig(ctx context.Context, input Config) (Config, error) {
	current, err := s.Store.Config(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Config{}, err
	}
	input = normalizeConfig(input)
	if err := validateConfig(input); err != nil {
		return Config{}, err
	}
	if err := s.checkRootChanges(ctx, current, input); err != nil {
		return Config{}, err
	}
	if err := ensureRoots(input.RootFolders); err != nil {
		return Config{}, err
	}
	if _, err := s.Store.SaveConfig(ctx, input); err != nil {
		return Config{}, err
	}
	return s.ConfigView(ctx)
}

func normalizeRoots(roots []RootFolder) []RootFolder {
	normalized := make([]RootFolder, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		id := strings.TrimSpace(root.ID)
		path := strings.TrimSpace(root.Path)
		if id == "" || path == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, RootFolder{ID: id, Path: path})
	}
	return normalized
}

func normalizeConfig(cfg Config) Config {
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	cfg.FolderTemplate = strings.TrimSpace(cfg.FolderTemplate)
	cfg.FileTemplate = strings.TrimSpace(cfg.FileTemplate)
	cfg.ImportMode = normalizeImportMode(cfg.ImportMode)
	cfg.FFprobePath = firstNonEmpty(strings.TrimSpace(cfg.FFprobePath), defaultFFprobe)
	cfg.MusicBrainzURL = strings.TrimSpace(cfg.MusicBrainzURL)
	cfg.CoverArtURL = strings.TrimSpace(cfg.CoverArtURL)
	cfg.DefaultProfileID = strings.TrimSpace(cfg.DefaultProfileID)
	profiles := make([]QualityProfile, 0, len(cfg.QualityProfiles))
	for _, profile := range cfg.QualityProfiles {
		profile.ID = strings.TrimSpace(profile.ID)
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Cutoff = strings.ToLower(strings.TrimSpace(profile.Cutoff))
		formats := make([]string, 0, len(profile.Formats))
		for _, format := range profile.Formats {
			format = strings.ToLower(strings.TrimSpace(format))
			if format != "" {
				formats = append(formats, format)
			}
		}
		profile.Formats = formats
		profiles = append(profiles, profile)
	}
	cfg.QualityProfiles = profiles
	return cfg
}

func validateConfig(cfg Config) error {
	if len(cfg.RootFolders) == 0 || len(cfg.RootFolders) > maxRoots {
		return fmt.Errorf("%w: configure between 1 and %d music root folders", ErrInvalid, maxRoots)
	}
	if err := validateMusicTemplate(cfg.FolderTemplate, false); err != nil {
		return err
	}
	if err := validateMusicTemplate(effectiveFileTemplate(cfg, 1), true); err != nil {
		return err
	}
	if cfg.PollMinutes < 1 || cfg.PollMinutes > 1440 || cfg.SearchHours < 1 || cfg.SearchHours > 8760 {
		return fmt.Errorf("%w: automation intervals are outside their allowed range", ErrInvalid)
	}
	if cfg.MusicBrainzRateMs < 0 || cfg.MusicBrainzRateMs > 10000 {
		return fmt.Errorf("%w: the MusicBrainz rate limit must be between 0 and 10000 ms", ErrInvalid)
	}
	if _, err := apiBaseURL(cfg.MusicBrainzURL, defaultMusicBrainzURL); err != nil {
		return err
	}
	if _, err := coverImageBase(cfg.CoverArtURL); err != nil {
		return err
	}
	if len(cfg.QualityProfiles) == 0 || len(cfg.QualityProfiles) > maxFormats {
		return fmt.Errorf("%w: configure at least one quality profile", ErrInvalid)
	}
	ids := map[string]bool{}
	defaultFound := false
	for _, profile := range cfg.QualityProfiles {
		if !validID(profile.ID) || profile.Name == "" {
			return fmt.Errorf("%w: every quality profile needs an ID and name", ErrInvalid)
		}
		if ids[profile.ID] {
			return fmt.Errorf("%w: duplicate quality profile ID", ErrInvalid)
		}
		ids[profile.ID] = true
		for _, format := range profile.Formats {
			if !knownFormats[format] {
				return fmt.Errorf("%w: unsupported quality format %q", ErrInvalid, format)
			}
		}
		if profile.Cutoff != "" && profile.Cutoff != "any" && profile.Cutoff != "lossless" && !knownFormats[profile.Cutoff] {
			return fmt.Errorf("%w: unsupported cutoff %q", ErrInvalid, profile.Cutoff)
		}
		if profile.MinBitrateKbps < 0 || profile.MinBitrateKbps > 1536 || profile.MinMB < 0 || profile.MaxMB < 0 {
			return fmt.Errorf("%w: quality limits are outside their allowed range", ErrInvalid)
		}
		if profile.ID == cfg.DefaultProfileID {
			defaultFound = true
		}
	}
	if !defaultFound {
		return fmt.Errorf("%w: the default quality profile does not exist", ErrInvalid)
	}
	return nil
}

// checkRootChanges refuses root changes that would orphan imported files.
func (s *Service) checkRootChanges(ctx context.Context, current, next Config) error {
	kept := make(map[string]string, len(next.RootFolders))
	for _, root := range next.RootFolders {
		kept[root.ID] = root.Path
	}
	changed := map[string]bool{}
	for _, root := range current.RootFolders {
		if path, ok := kept[root.ID]; ok && path == root.Path {
			continue
		}
		changed[root.ID] = true
	}
	if len(changed) == 0 {
		return nil
	}
	albums, err := s.Store.Albums(ctx)
	if err != nil {
		return err
	}
	for _, album := range albums {
		for _, file := range album.Files {
			if changed[file.RootID] {
				return fmt.Errorf("%w: a root folder is used by existing music files", ErrConflict)
			}
		}
	}
	return nil
}

func rootByID(cfg Config, id string) (RootFolder, bool) {
	id = strings.TrimSpace(id)
	for _, root := range cfg.RootFolders {
		if root.ID == id {
			return root, true
		}
	}
	if id == "" && len(cfg.RootFolders) > 0 {
		return cfg.RootFolders[0], true
	}
	return RootFolder{}, false
}

func (s *Service) chooseRoot(cfg Config, id string) (RootFolder, error) {
	root, ok := rootByID(cfg, id)
	if !ok {
		return RootFolder{}, fmt.Errorf("%w: add a music root folder first", ErrInvalid)
	}
	return root, nil
}

func profileByID(cfg Config, id string) (QualityProfile, bool) {
	id = strings.TrimSpace(id)
	for _, profile := range cfg.QualityProfiles {
		if profile.ID == id {
			return profile, true
		}
	}
	if id == "" {
		for _, profile := range cfg.QualityProfiles {
			if profile.ID == cfg.DefaultProfileID {
				return profile, true
			}
		}
		if len(cfg.QualityProfiles) > 0 {
			return cfg.QualityProfiles[0], true
		}
	}
	return QualityProfile{}, false
}

func (s *Service) requireProfile(ctx context.Context, id string) (QualityProfile, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return QualityProfile{}, err
	}
	profile, ok := profileByID(cfg, id)
	if !ok {
		return QualityProfile{}, fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
	}
	return profile, nil
}

func (s *Service) artistName(ctx context.Context, album Album) string {
	if strings.TrimSpace(album.ArtistName) != "" {
		return album.ArtistName
	}
	artist, err := s.Store.Artist(ctx, album.ArtistID)
	if err != nil {
		return ""
	}
	return artist.Name
}

func (s *Service) indexer() (*newznabClient, error) {
	client, err := newNewznab(s.Downloads.Config())
	if err != nil {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(client.key) == "" {
		return nil, fmt.Errorf("%w: the indexer API key is not configured", ErrNotConfigured)
	}
	return client, nil
}

func (s *Service) brainz(cfg Config) (*brainzClient, error) {
	return newBrainz(cfg.MusicBrainzURL, time.Duration(cfg.MusicBrainzRateMs)*time.Millisecond)
}

func (s *Service) withAlbumLock(ctx context.Context, albumID string, fn func(context.Context) error) error {
	conn, err := s.Store.pool.Acquire(ctx)
	if err != nil {
		return errors.New("music: the operation cannot reach the database")
	}
	defer conn.Release()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, albumLockKey(albumID)).Scan(&held); err != nil {
		return errors.New("music: the operation cannot acquire its lock")
	}
	if !held {
		return errAlbumBusy
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, albumLockKey(albumID)); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	return fn(ctx)
}

// albumLockKey derives a stable advisory lock slot from an album ID.
func albumLockKey(id string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(id))
	return albumLockBase + int64(hash.Sum64()%1_000_000)
}

// Artists lists every artist with its albums decorated for the library view.
func (s *Service) Artists(ctx context.Context) ([]Artist, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	artists, err := s.Store.Artists(ctx)
	if err != nil {
		return nil, err
	}
	albums, err := s.Store.Albums(ctx)
	if err != nil {
		return nil, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return nil, err
	}
	byArtist := map[string][]Album{}
	for _, album := range albums {
		byArtist[album.ArtistID] = append(byArtist[album.ArtistID], album)
	}
	for index := range artists {
		s.decorateAlbums(cfg, byArtist[artists[index].ID], acquisitions)
		artists[index].Albums = byArtist[artists[index].ID]
		artists[index].Status = artistStatus(artists[index])
	}
	return artists, nil
}

func (s *Service) Artist(ctx context.Context, id string) (Artist, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Artist{}, err
	}
	artist, err := s.Store.Artist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return Artist{}, err
	}
	s.decorateAlbums(cfg, artist.Albums, acquisitions)
	artist.Status = artistStatus(artist)
	return artist, nil
}

// Albums lists the whole album library.
func (s *Service) Albums(ctx context.Context) ([]Album, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	albums, err := s.Store.Albums(ctx)
	if err != nil {
		return nil, err
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return nil, err
	}
	s.decorateAlbums(cfg, albums, acquisitions)
	sortAlbums(albums)
	return albums, nil
}

// Album loads one album with its tracklist and files.
func (s *Service) Album(ctx context.Context, id string) (Album, error) {
	album, err := s.Store.Album(ctx, id)
	if err != nil {
		return Album{}, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	acquisitions, err := s.Store.AcquisitionsFor(ctx, id)
	if err != nil {
		return Album{}, err
	}
	s.decorateAlbum(cfg, &album, acquisitions)
	return album, nil
}

func (s *Service) decorateAlbums(cfg Config, albums []Album, acquisitions []Acquisition) {
	byAlbum := map[string][]Acquisition{}
	for _, acquisition := range acquisitions {
		byAlbum[acquisition.AlbumID] = append(byAlbum[acquisition.AlbumID], acquisition)
	}
	for index := range albums {
		s.decorateAlbum(cfg, &albums[index], byAlbum[albums[index].ID])
	}
	sortAlbums(albums)
}

func (s *Service) decorateAlbum(cfg Config, album *Album, acquisitions []Acquisition) {
	profile, _ := profileByID(cfg, album.ProfileID)
	present := make([]File, 0, len(album.Files))
	for index := range album.Files {
		album.Files[index].Missing = !filePresent(cfg, album.Files[index])
		if !album.Files[index].Missing {
			present = append(present, album.Files[index])
		}
	}
	album.Size = 0
	album.Format, album.Score = "", 0
	for _, file := range present {
		album.Size += file.Size
		if file.Score > album.Score {
			album.Format, album.Score = file.Format, file.Score
		}
	}
	for index := range album.Tracks {
		track := &album.Tracks[index]
		switch {
		case track.File == nil:
			track.Missing = presentTrackMissing(track, present)
		default:
			track.File.Missing = !filePresent(cfg, File{RootID: track.File.RootID, Path: track.File.Path})
			track.Missing = track.File.Missing
		}
	}
	pending := pendingAcquisition(acquisitions)
	failed := failedAcquisition(acquisitions)
	switch {
	case pending != nil:
		album.Status = "downloading"
		album.Error = pending.Error
	case failed != nil && len(present) == 0:
		album.Status = "failed"
		album.Error = failed.Error
	case album.Error != "" && len(present) == 0:
		album.Status = "failed"
	case len(present) == 0 && album.Monitored:
		album.Status = "wanted"
	case len(present) == 0:
		album.Status = "unmonitored"
	case album.Monitored && profile.Upgrade && !albumAtCutoff(profile, present):
		album.Status = "cutoff-unmet"
	default:
		album.Status = "available"
	}
}

func presentTrackMissing(track *Track, files []File) bool {
	for _, file := range files {
		if file.Disc == track.Disc && file.Number == track.Number && track.Number > 0 {
			return false
		}
	}
	return true
}

func pendingAcquisition(acquisitions []Acquisition) *Acquisition {
	for index := range acquisitions {
		switch acquisitions[index].Status {
		case "queued", "downloading", "importing", "":
			return &acquisitions[index]
		}
	}
	return nil
}

// failedAcquisition reports a download or import that stopped without files.
func failedAcquisition(acquisitions []Acquisition) *Acquisition {
	for index := range acquisitions {
		switch acquisitions[index].Status {
		case "failed", "import-failed":
			return &acquisitions[index]
		}
	}
	return nil
}

func artistStatus(artist Artist) string {
	anyWanted, anyPending, anyAvailable, anyFailed := false, false, false, false
	for _, album := range artist.Albums {
		switch album.Status {
		case "downloading":
			anyPending = true
		case "failed":
			anyFailed = true
		case "wanted", "cutoff-unmet":
			if album.Monitored {
				anyWanted = true
			}
		case "available":
			anyAvailable = true
		}
	}
	switch {
	case anyPending:
		return "downloading"
	case anyWanted:
		return "wanted"
	case anyFailed && !anyAvailable:
		return "failed"
	case anyAvailable:
		return "available"
	default:
		return "unmonitored"
	}
}

func sortAlbums(albums []Album) {
	sort.SliceStable(albums, func(i, j int) bool {
		if albums[i].ArtistName != albums[j].ArtistName {
			return strings.ToLower(albums[i].ArtistName) < strings.ToLower(albums[j].ArtistName)
		}
		if albums[i].ReleaseDate != albums[j].ReleaseDate {
			return albums[i].ReleaseDate < albums[j].ReleaseDate
		}
		return strings.ToLower(albums[i].Title) < strings.ToLower(albums[j].Title)
	})
}

func filePresent(cfg Config, file File) bool {
	root, ok := rootByID(cfg, file.RootID)
	if !ok || file.Path == "" {
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

func (s *Service) AddArtist(ctx context.Context, input AddArtistInput) (Artist, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Artist{}, err
	}
	root, err := s.chooseRoot(cfg, input.RootID)
	if err != nil {
		return Artist{}, err
	}
	profile, ok := profileByID(cfg, input.ProfileID)
	if !ok {
		return Artist{}, fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
	}
	option, err := normalizeMonitorOption(input.MonitorOption)
	if err != nil {
		return Artist{}, err
	}
	artist := Artist{
		ID: newID(), Monitored: input.Monitored, MonitorOption: option,
		ProfileID: profile.ID, RootID: root.ID,
	}
	var albums []Album
	if mbid := strings.TrimSpace(input.MusicBrainzID); mbid != "" {
		if !validMBID(mbid) {
			return Artist{}, fmt.Errorf("%w: the MusicBrainz artist ID is invalid", ErrInvalid)
		}
		if existing, err := s.Store.ArtistByMusicBrainz(ctx, mbid); err == nil {
			return Artist{}, fmt.Errorf("%w: %s is already in the library", ErrConflict, existing.Name)
		} else if !errors.Is(err, ErrNotFound) {
			return Artist{}, err
		}
		client, err := s.brainz(cfg)
		if err != nil {
			return Artist{}, err
		}
		looked, err := client.lookupArtist(ctx, mbid)
		if err != nil {
			return Artist{}, err
		}
		artist.Name = firstNonEmpty(input.Name, looked.Name)
		artist.SortName, artist.Disambiguation, artist.Country, artist.Type = looked.SortName, looked.Disambiguation, looked.Country, looked.Type
		artist.MusicBrainzID = looked.MusicBrainzID
		if option != "none" {
			groups, err := client.artistAlbums(ctx, mbid, maxBrainzSearch)
			if err != nil {
				return Artist{}, err
			}
			albums = s.newAlbums(ctx, artist, groups, option, cfg)
		}
	} else {
		artist.Name = strings.TrimSpace(input.Name)
	}
	if artist.Name == "" {
		return Artist{}, fmt.Errorf("%w: an artist name or MusicBrainz ID is required", ErrInvalid)
	}
	saved, err := s.Store.SaveArtistWithAlbums(ctx, artist, albums)
	if err != nil {
		return Artist{}, err
	}
	_ = s.Store.Event(ctx, "", saved.ID, "added", "Added artist "+saved.Name)
	return s.Artist(ctx, saved.ID)
}

// newAlbums converts provider results into monitored catalog entries.
func (s *Service) newAlbums(ctx context.Context, artist Artist, groups []AlbumResult, option string, cfg Config) []Album {
	albums := make([]Album, 0, len(groups))
	seen := map[string]bool{}
	for _, group := range groups {
		if group.MusicBrainzID == "" || seen[group.MusicBrainzID] {
			continue
		}
		if _, err := s.Store.AlbumByMusicBrainz(ctx, group.MusicBrainzID); err == nil {
			continue
		}
		seen[group.MusicBrainzID] = true
		monitored := option != "none"
		if option == "future" && group.Year > 0 && group.Year < time.Now().Year() {
			monitored = false
		}
		albums = append(albums, Album{
			ID: newID(), ArtistID: artist.ID, ArtistName: artist.Name,
			MusicBrainzID: group.MusicBrainzID, Title: group.Title, Year: group.Year,
			Type: group.Type, Monitored: monitored, ProfileID: artist.ProfileID, RootID: artist.RootID,
		})
	}
	return albums
}

func normalizeMonitorOption(option string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(option)) {
	case "", "all":
		return "all", nil
	case "future":
		return "future", nil
	case "missing":
		return "missing", nil
	case "none":
		return "none", nil
	default:
		return "", fmt.Errorf("%w: the monitor option must be all, future, missing, or none", ErrInvalid)
	}
}

func (s *Service) UpdateArtist(ctx context.Context, id string, input Artist) (Artist, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Artist{}, err
	}
	fields := map[string]any{"monitored": input.Monitored}
	if name := strings.TrimSpace(input.Name); name != "" {
		fields["name"] = truncate(name, maxNameRunes)
	}
	if _, ok := rootByID(cfg, input.RootID); ok && strings.TrimSpace(input.RootID) != "" {
		fields["rootId"] = strings.TrimSpace(input.RootID)
	}
	if _, ok := profileByID(cfg, input.ProfileID); ok && strings.TrimSpace(input.ProfileID) != "" {
		fields["profileId"] = strings.TrimSpace(input.ProfileID)
	}
	if option, err := normalizeMonitorOption(input.MonitorOption); err == nil {
		fields["monitorOption"] = option
	}
	if err := s.Store.PatchArtistData(ctx, id, fields); err != nil {
		return Artist{}, err
	}
	return s.Artist(ctx, id)
}

func (s *Service) MonitorArtist(ctx context.Context, id string, input MonitorInput) (Artist, error) {
	option, err := normalizeMonitorOption(input.MonitorOption)
	if err != nil {
		return Artist{}, err
	}
	if err := s.Store.PatchArtistData(ctx, id, map[string]any{"monitored": input.Monitored, "monitorOption": option}); err != nil {
		return Artist{}, err
	}
	artist, err := s.Store.Artist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	if option == "none" || !input.Monitored || artist.MusicBrainzID == "" {
		return s.Artist(ctx, id)
	}
	return s.RefreshArtist(ctx, id)
}

// RefreshArtist pulls metadata and new release groups for a monitored artist.
func (s *Service) RefreshArtist(ctx context.Context, id string) (Artist, error) {
	artist, err := s.Store.Artist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	if artist.MusicBrainzID == "" {
		return Artist{}, fmt.Errorf("%w: the artist has no MusicBrainz ID", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Artist{}, err
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return Artist{}, err
	}
	looked, err := client.lookupArtist(ctx, artist.MusicBrainzID)
	if err != nil {
		_ = s.Store.Event(ctx, "", artist.ID, "error", "Artist refresh failed: "+err.Error())
		return Artist{}, err
	}
	groups, err := client.artistAlbums(ctx, artist.MusicBrainzID, maxBrainzSearch)
	if err != nil {
		_ = s.Store.Event(ctx, "", artist.ID, "error", "Artist release refresh failed: "+err.Error())
		return Artist{}, err
	}
	now := time.Now().UTC()
	// Provider work stays outside the write; monitoring edits made meanwhile survive.
	fields := map[string]any{
		"name":           truncate(firstNonEmpty(looked.Name, artist.Name), maxNameRunes),
		"sortName":       looked.SortName,
		"disambiguation": looked.Disambiguation,
		"country":        looked.Country,
		"type":           looked.Type,
		"lastRefreshAt":  now,
	}
	if err := s.Store.PatchArtistData(ctx, id, fields); err != nil {
		return Artist{}, err
	}
	// New albums follow the monitoring state as it is now, not as it was before the write.
	current, err := s.Store.Artist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	added := s.newAlbums(ctx, current, groups, current.MonitorOption, cfg)
	if err := s.Store.AddArtistAlbums(ctx, id, added); err != nil {
		return Artist{}, err
	}
	if len(added) > 0 {
		_ = s.Store.Event(ctx, "", id, "refreshed", fmt.Sprintf("Added %d monitored release(s)", len(added)))
	}
	return s.Artist(ctx, id)
}

func (s *Service) RemoveArtist(ctx context.Context, id string, deleteFiles bool) error {
	artist, err := s.Store.Artist(ctx, id)
	if err != nil {
		return err
	}
	if deleteFiles {
		for _, album := range artist.Albums {
			if err := s.removeAlbumFiles(ctx, album); err != nil {
				return err
			}
		}
	}
	return s.Store.DeleteArtist(ctx, id)
}

func (s *Service) removeAlbumFiles(ctx context.Context, album Album) error {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, file := range album.Files {
		root, ok := rootByID(cfg, file.RootID)
		if !ok {
			continue
		}
		if err := library.Archive(root.Path, file.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	if album.CoverPath != "" {
		if root, ok := rootByID(cfg, album.RootID); ok {
			_ = library.Archive(root.Path, album.CoverPath)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) AddAlbum(ctx context.Context, input AddAlbumInput) (Album, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	artist, err := s.Store.Artist(ctx, strings.TrimSpace(input.ArtistID))
	if err != nil {
		return Album{}, err
	}
	album := Album{
		ID: newID(), ArtistID: artist.ID, ArtistName: artist.Name,
		Title: truncate(input.Title, maxAlbumTitleRunes), Year: input.Year,
		Monitored: input.Monitored, ProfileID: artist.ProfileID, RootID: artist.RootID,
	}
	if _, ok := profileByID(cfg, input.ProfileID); ok && strings.TrimSpace(input.ProfileID) != "" {
		album.ProfileID = strings.TrimSpace(input.ProfileID)
	}
	if root, ok := rootByID(cfg, input.RootID); ok && strings.TrimSpace(input.RootID) != "" {
		album.RootID = root.ID
	}
	if mbid := strings.TrimSpace(input.MusicBrainzID); mbid != "" {
		if !validMBID(mbid) {
			return Album{}, fmt.Errorf("%w: the MusicBrainz release group ID is invalid", ErrInvalid)
		}
		if existing, err := s.Store.AlbumByMusicBrainz(ctx, mbid); err == nil {
			return Album{}, fmt.Errorf("%w: %s is already in the library", ErrConflict, existing.Title)
		} else if !errors.Is(err, ErrNotFound) {
			return Album{}, err
		}
		client, err := s.brainz(cfg)
		if err != nil {
			return Album{}, err
		}
		looked, err := client.lookupAlbum(ctx, mbid)
		if err != nil {
			return Album{}, err
		}
		album.MusicBrainzID = looked.MusicBrainzID
		album.Title = firstNonEmpty(looked.Title, album.Title)
		if looked.Year > 0 {
			album.Year = looked.Year
		}
		album.ReleaseDate, album.Type = looked.ReleaseDate, looked.Type
		album.Tracks = looked.Tracks
	}
	if album.Title == "" {
		return Album{}, fmt.Errorf("%w: an album title is required", ErrInvalid)
	}
	saved, err := s.Store.SaveAlbum(ctx, album)
	if err != nil {
		return Album{}, err
	}
	_ = s.Store.Event(ctx, saved.ID, artist.ID, "added", "Added album "+saved.Title)
	return s.Album(ctx, saved.ID)
}

func (s *Service) UpdateAlbum(ctx context.Context, id string, input Album) (Album, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	fields := map[string]any{"monitored": input.Monitored}
	if title := strings.TrimSpace(input.Title); title != "" {
		fields["title"] = truncate(title, maxAlbumTitleRunes)
	}
	if input.Year > 0 {
		fields["year"] = input.Year
	}
	if _, ok := profileByID(cfg, input.ProfileID); ok && strings.TrimSpace(input.ProfileID) != "" {
		fields["profileId"] = strings.TrimSpace(input.ProfileID)
	}
	if root, ok := rootByID(cfg, input.RootID); ok && strings.TrimSpace(input.RootID) != "" {
		fields["rootId"] = root.ID
	}
	if err := s.Store.PatchAlbumData(ctx, id, fields); err != nil {
		return Album{}, err
	}
	return s.Album(ctx, id)
}

func (s *Service) MonitorAlbum(ctx context.Context, id string, input AlbumMonitorInput) (Album, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	fields := map[string]any{"monitored": input.Monitored}
	if profile, ok := profileByID(cfg, input.ProfileID); ok && strings.TrimSpace(input.ProfileID) != "" {
		fields["profileId"] = profile.ID
	}
	if err := s.Store.PatchAlbumData(ctx, id, fields); err != nil {
		return Album{}, err
	}
	saved, err := s.Album(ctx, id)
	if err != nil {
		return Album{}, err
	}
	_ = s.Store.Event(ctx, id, saved.ArtistID, "monitor", fmt.Sprintf("Monitoring set to %v", input.Monitored))
	return saved, nil
}

// RefreshAlbum reloads release metadata and its tracklist from the provider.
func (s *Service) RefreshAlbum(ctx context.Context, id string) (Album, error) {
	album, err := s.Store.Album(ctx, id)
	if err != nil {
		return Album{}, err
	}
	if album.MusicBrainzID == "" {
		return Album{}, fmt.Errorf("%w: the album has no MusicBrainz ID", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return Album{}, err
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return Album{}, err
	}
	looked, err := client.lookupAlbum(ctx, album.MusicBrainzID)
	if err != nil {
		_ = s.Store.Event(ctx, id, album.ArtistID, "error", "Album refresh failed: "+err.Error())
		return Album{}, err
	}
	fields := map[string]any{
		"title":       truncate(firstNonEmpty(looked.Title, album.Title), maxAlbumTitleRunes),
		"releaseDate": firstNonEmpty(looked.ReleaseDate, album.ReleaseDate),
		"type":        firstNonEmpty(looked.Type, album.Type),
		"artistName":  firstNonEmpty(looked.ArtistName, album.ArtistName),
	}
	if looked.Year > 0 {
		fields["year"] = looked.Year
	}
	// Provider work stays outside the lock; only the merge of current state is serialized.
	err = s.withAlbumLock(ctx, id, func(ctx context.Context) error {
		fresh, err := s.Store.Album(ctx, id)
		if err != nil {
			return err
		}
		return s.Store.PatchAlbum(ctx, id, fields, mergeTracks(fresh.Tracks, looked.Tracks))
	})
	if errors.Is(err, errAlbumBusy) {
		return Album{}, ErrConflict
	}
	if err != nil {
		_ = s.Store.Event(ctx, id, album.ArtistID, "error", "Album refresh failed: "+err.Error())
		return Album{}, err
	}
	_ = s.Store.Event(ctx, id, album.ArtistID, "refreshed", "Album metadata refreshed")
	return s.Album(ctx, id)
}

// mergeTracks keeps imported file links when the provider tracklist is refreshed.
func mergeTracks(existing, fresh []Track) []Track {
	type key struct {
		disc   int
		number int
	}
	byKey := map[key]Track{}
	byMB := map[string]Track{}
	for _, track := range existing {
		byKey[key{track.Disc, track.Number}] = track
		if track.MusicBrainzID != "" {
			byMB[track.MusicBrainzID] = track
		}
	}
	merged := make([]Track, 0, len(fresh))
	for _, track := range fresh {
		if old, ok := byKey[key{track.Disc, track.Number}]; ok && track.Number > 0 {
			track.ID = old.ID
			track.File = old.File
			track.Missing = old.Missing
		} else if track.MusicBrainzID != "" {
			if old, ok := byMB[track.MusicBrainzID]; ok {
				track.ID = old.ID
				track.File = old.File
				track.Missing = old.Missing
			}
		}
		merged = append(merged, track)
	}
	for _, track := range existing {
		if track.Number == 0 && track.File != nil {
			merged = append(merged, track)
		}
	}
	return merged
}

func (s *Service) RemoveAlbum(ctx context.Context, id string, deleteFiles bool) error {
	album, err := s.Store.Album(ctx, id)
	if err != nil {
		return err
	}
	if deleteFiles {
		if err := s.removeAlbumFiles(ctx, album); err != nil {
			return err
		}
	}
	return s.Store.DeleteAlbum(ctx, id)
}

// Discover searches MusicBrainz for artists and albums.
func (s *Service) Discover(ctx context.Context, query string, page int) (DiscoverResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > maxQueryRunes {
		return DiscoverResult{}, fmt.Errorf("%w: a discovery query is required", ErrInvalid)
	}
	if page < 1 || page > maxDiscoverPage {
		return DiscoverResult{}, fmt.Errorf("%w: the discovery page is outside its allowed range", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return DiscoverResult{}, err
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return DiscoverResult{}, err
	}
	offset := (page - 1) * discoverPageSize
	artists, err := client.searchArtists(ctx, query, discoverPageSize, offset)
	if err != nil {
		return DiscoverResult{}, err
	}
	albums, err := client.searchAlbums(ctx, query, discoverPageSize, offset)
	if err != nil {
		return DiscoverResult{}, err
	}
	for index := range artists {
		if _, err := s.Store.ArtistByMusicBrainz(ctx, artists[index].MusicBrainzID); err == nil {
			artists[index].InLibrary = true
		}
	}
	for index := range albums {
		if _, err := s.Store.AlbumByMusicBrainz(ctx, albums[index].MusicBrainzID); err == nil {
			albums[index].InLibrary = true
		}
	}
	return DiscoverResult{Artists: artists, Albums: albums}, nil
}

// Search returns scored release candidates for one album.
func (s *Service) Search(ctx context.Context, albumID string) ([]Release, error) {
	album, err := s.Store.Album(ctx, albumID)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	profile, ok := profileByID(cfg, album.ProfileID)
	if !ok {
		return nil, fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
	}
	releases, err := s.searchProviders(ctx, releaseQuery{Artist: s.artistName(ctx, album), Album: album.Title, Year: album.Year})
	if err != nil {
		return nil, err
	}
	return s.evaluateReleases(ctx, cfg, profile, album, releases)
}

// SearchReleases runs a manual release search; albumID applies the album's profile and duplicate checks.
func (s *Service) SearchReleases(ctx context.Context, query, albumID string) ([]Release, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, err
	}
	album := Album{}
	profile, _ := profileByID(cfg, "")
	if strings.TrimSpace(albumID) != "" {
		album, err = s.Store.Album(ctx, albumID)
		if err != nil {
			return nil, err
		}
		profile, _ = profileByID(cfg, album.ProfileID)
	}
	search := releaseQuery{Text: strings.TrimSpace(query)}
	if album.ID != "" && search.Text == "" {
		search = releaseQuery{Artist: s.artistName(ctx, album), Album: album.Title, Year: album.Year}
	}
	releases, err := s.searchProviders(ctx, search)
	if err != nil {
		return nil, err
	}
	return s.evaluateReleases(ctx, cfg, profile, album, releases)
}

func (s *Service) evaluateReleases(ctx context.Context, cfg Config, profile QualityProfile, album Album, releases []Release) ([]Release, error) {
	current := bestCurrent(profile, album.Files)
	var blocked map[string]bool
	var acquisitions []Acquisition
	if album.ID != "" {
		ids, err := s.Store.Blocklist(ctx, album.ID)
		if err != nil {
			return nil, err
		}
		blocked = make(map[string]bool, len(ids))
		for _, id := range ids {
			blocked[id] = true
		}
		acquisitions, err = s.Store.AcquisitionsFor(ctx, album.ID)
		if err != nil {
			return nil, err
		}
	}
	pending := pendingAcquisition(acquisitions)
	results := make([]Release, 0, len(releases))
	for _, release := range releases {
		release.Decision = evaluate(profile, release.Title, release.Size, current)
		reasons := make([]string, 0, 3)
		if album.ID != "" && !releaseMatchesAlbum(album, release) {
			reasons = append(reasons, "the release does not match this album")
		}
		if blocked[release.ID] {
			reasons = append(reasons, "the release is blocked")
		}
		if release.Protocol == "torrent" && release.Seeders <= 0 {
			reasons = append(reasons, "the torrent has no seeders")
		}
		if reason := duplicateReason(acquisitions, release.ID); reason != "" {
			reasons = append(reasons, reason)
		} else if pending != nil {
			reasons = append(reasons, "another release for this album is already being processed")
		}
		if len(reasons) > 0 {
			release.Decision.Allowed = false
			release.Decision.Reasons = append(release.Decision.Reasons, reasons...)
		}
		results = append(results, release)
	}
	sortReleases(results)
	return results, nil
}

func duplicateReason(acquisitions []Acquisition, releaseID string) string {
	for _, acquisition := range acquisitions {
		if acquisition.ReleaseID != releaseID {
			continue
		}
		switch acquisition.Status {
		case "failed":
			return "the release previously failed"
		case "imported":
			return "the release is already imported"
		case "superseded":
			continue
		default:
			return "the release is already downloading"
		}
	}
	return ""
}

// Grab queues a release for the album and records durable music ownership of the download.
func (s *Service) Grab(ctx context.Context, albumID, releaseID string, override bool) (downloads.Job, error) {
	releaseID = strings.TrimSpace(releaseID)
	if !validReleaseID(releaseID) {
		return downloads.Job{}, fmt.Errorf("%w: a release ID is required", ErrInvalid)
	}
	var job downloads.Job
	err := s.withAlbumLock(ctx, albumID, func(ctx context.Context) error {
		album, err := s.Store.Album(ctx, albumID)
		if err != nil {
			return err
		}
		cfg, err := s.Store.Config(ctx)
		if err != nil {
			return err
		}
		profile, ok := profileByID(cfg, album.ProfileID)
		if !ok {
			return fmt.Errorf("%w: the quality profile does not exist", ErrInvalid)
		}
		artist := s.artistName(ctx, album)
		releases, err := s.searchProviders(ctx, releaseQuery{Artist: artist, Album: album.Title, Year: album.Year})
		if err != nil {
			return err
		}
		chosen, found := findRelease(releases, releaseID)
		if !found {
			// Scheduler RSS grabs come from the usenet feed, which is not part of an album search.
			if client, err := s.indexer(); err == nil {
				if feed, err := client.Feed(ctx); err == nil {
					chosen, found = findRelease(feed, releaseID)
				}
			}
		}
		if !found {
			return fmt.Errorf("%w: the release does not appear in a fresh search", ErrInvalid)
		}
		decision := evaluate(profile, chosen.Title, chosen.Size, bestCurrent(profile, album.Files))
		if !override && !releaseMatchesAlbum(album, chosen) {
			return fmt.Errorf("%w: the release does not match this album", ErrInvalid)
		}
		if !override && chosen.Protocol == "torrent" && chosen.Seeders <= 0 {
			return fmt.Errorf("%w: the torrent has no seeders", ErrConflict)
		}
		acquisitions, err := s.Store.AcquisitionsFor(ctx, album.ID)
		if err != nil {
			return err
		}
		for _, acquisition := range acquisitions {
			if acquisition.ReleaseID == releaseID {
				if acquisition.Status == "failed" && override {
					continue
				}
				return fmt.Errorf("%w: this release is already being processed", ErrConflict)
			}
			switch acquisition.Status {
			case "queued", "downloading", "importing", "":
				return fmt.Errorf("%w: another release for this album is already being processed", ErrConflict)
			}
		}
		blocked, err := s.Store.Blocked(ctx, album.ID, releaseID)
		if err != nil {
			return err
		}
		if blocked && !override {
			return fmt.Errorf("%w: this release is blocked", ErrConflict)
		}
		if !decision.Allowed && !override {
			return fmt.Errorf("%w: %s", ErrConflict, strings.Join(decision.Reasons, "; "))
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
			AlbumID: album.ID, JobID: job.ID, ReleaseID: releaseID, Title: chosen.Title,
			Decision: decision, Override: override, Status: "queued",
		}); err != nil {
			return err
		}
		_ = s.Store.Event(ctx, album.ID, album.ArtistID, "grabbed", truncate("Grabbed "+chosen.Title, maxTextRunes))
		return nil
	})
	if errors.Is(err, errAlbumBusy) {
		return downloads.Job{}, ErrConflict
	}
	if err != nil {
		return downloads.Job{}, err
	}
	return job, nil
}

func findRelease(releases []Release, id string) (Release, bool) {
	for _, release := range releases {
		if release.ID == id {
			return release, true
		}
	}
	return Release{}, false
}

// Wanted lists monitored albums that are missing files or below their cutoff.
func (s *Service) Wanted(ctx context.Context) ([]Album, error) {
	albums, err := s.Albums(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make([]Album, 0, 16)
	for _, album := range albums {
		switch album.Status {
		case "wanted", "cutoff-unmet", "downloading", "failed":
			if album.Monitored {
				wanted = append(wanted, album)
			}
		}
	}
	return wanted, nil
}

// Calendar lists albums with a known release date in a thirty-day window.
func (s *Service) Calendar(ctx context.Context) ([]CalendarEntry, error) {
	albums, err := s.Albums(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30)
	to := now.AddDate(1, 0, 0)
	entries := make([]CalendarEntry, 0, 16)
	for _, album := range albums {
		date, ok := parseCalendarDate(album.ReleaseDate)
		if !ok || date.Before(from) || date.After(to) {
			continue
		}
		entries = append(entries, CalendarEntry{
			ID: album.ID + "@" + date.Format("2006-01-02"), AlbumID: album.ID, ArtistID: album.ArtistID,
			Title: album.Title, ArtistName: album.ArtistName, ReleaseDate: date.Format("2006-01-02"), Type: album.Type,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ReleaseDate < entries[j].ReleaseDate })
	return entries, nil
}

func parseCalendarDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (s *Service) History(ctx context.Context, albumID string) ([]History, error) {
	return s.Store.History(ctx, albumID)
}

// ArtistHistory returns one artist's events.
func (s *Service) ArtistHistory(ctx context.Context, artistID string) ([]History, error) {
	return s.Store.ArtistHistory(ctx, artistID)
}

// Cover streams the album cover from the configured provider.
func (s *Service) Cover(ctx context.Context, albumID string) ([]byte, string, error) {
	album, err := s.Store.Album(ctx, albumID)
	if err != nil {
		return nil, "", err
	}
	if album.MusicBrainzID == "" {
		return nil, "", fmt.Errorf("%w: the album has no MusicBrainz ID", ErrInvalid)
	}
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return nil, "", err
	}
	imageBase, err := coverImageBase(cfg.CoverArtURL)
	if err != nil || imageBase == nil {
		return nil, "", fmt.Errorf("%w: cover art is disabled", ErrNotConfigured)
	}
	client, err := s.brainz(cfg)
	if err != nil {
		return nil, "", err
	}
	coverCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	return client.cover(coverCtx, imageBase, album.MusicBrainzID)
}

// TestConnections checks the metadata provider, the indexer, and the probe helper.
func (s *Service) TestConnections(ctx context.Context) (ConnectionTests, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return ConnectionTests{}, err
	}
	tests := ConnectionTests{}
	if strings.TrimSpace(cfg.MusicBrainzURL) == "" {
		tests.MusicBrainz.Error = "the MusicBrainz URL is not configured"
	} else if client, err := s.brainz(cfg); err != nil {
		tests.MusicBrainz.Error = err.Error()
	} else {
		checkCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		defer cancel()
		if _, err := client.searchArtists(checkCtx, "constellarr", 1, 0); err != nil {
			tests.MusicBrainz.Error = err.Error()
		} else {
			tests.MusicBrainz.OK = true
		}
	}
	if client, err := s.indexer(); err != nil {
		tests.Indexer.Error = err.Error()
	} else {
		checkCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		defer cancel()
		if err := client.Test(checkCtx); err != nil {
			tests.Indexer.Error = err.Error()
		} else {
			tests.Indexer.OK = true
		}
	}
	if !ffprobeAvailable(cfg.FFprobePath) {
		tests.FFprobe.Error = "ffprobe is not available; imports fall back to file names"
	} else {
		tests.FFprobe.OK = true
	}
	return tests, nil
}
