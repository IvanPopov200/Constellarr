package subtitles

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

const (
	defaultScanMinutes   = 30
	defaultSearchHours   = 6
	defaultRetryMinutes  = 60
	defaultCutoffScore   = 55
	defaultProviderSecs  = 20
	maxProviders         = 8
	maxProfileLanguages  = 8
	maxProfiles          = 32
	libraryLimit         = 200
	maxLibraryLimit      = 1000
	maxQuotaEvents       = 200
	tickInterval         = 30 * time.Second
	scanTimeout          = 5 * time.Minute
	defaultSyncTimeout   = 600
	defaultAITimeoutSecs = 120
)

var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Options wires the subtitle service to the movie and TV catalogs.
type Options struct {
	Movies *movies.Service
	TV     *tv.Service
	// Catalog overrides the movie and TV adapters.
	Catalog Catalog
	// Translator overrides the configured OpenAI-compatible client, for a shared AI provider.
	Translator Translator
	Logger     *slog.Logger
}

// Service owns subtitle inventory, providers, jobs, and automation.
type Service struct {
	pool    *pgxpool.Pool
	store   *Store
	catalog Catalog
	logger  *slog.Logger

	translatorMu sync.RWMutex
	translator   Translator

	providerMu   sync.Mutex
	providerStat map[string]ProviderStatus

	jobsMu sync.Mutex
	jobs   map[string]context.CancelFunc

	lastScan    atomic.Int64
	scanRunning atomic.Bool
	wake        chan struct{}

	startOnce sync.Once
	cancel    context.CancelFunc
	workers   sync.WaitGroup

	scansMu sync.Mutex
	scans   sync.WaitGroup
	closed  bool
	runCtx  context.Context
}

// New creates the subtitle service and seeds its configuration and default language profile.
func New(ctx context.Context, pool *pgxpool.Pool, opts Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("subtitles: a PostgreSQL pool is required")
	}
	store, err := NewStore(ctx, pool, defaultConfig())
	if err != nil {
		return nil, err
	}
	catalog := opts.Catalog
	if catalog == nil {
		if opts.Movies == nil && opts.TV == nil {
			return nil, errors.New("subtitles: the movie or TV service is required")
		}
		catalog = NewCatalog(opts.Movies, opts.TV)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool:         pool,
		store:        store,
		catalog:      catalog,
		logger:       logger,
		translator:   opts.Translator,
		providerStat: map[string]ProviderStatus{},
		jobs:         map[string]context.CancelFunc{},
		wake:         make(chan struct{}, 1),
	}, nil
}

// SetTranslator installs a shared AI translation provider; nil restores the configured client.
func (s *Service) SetTranslator(translator Translator) {
	s.translatorMu.Lock()
	s.translator = translator
	s.translatorMu.Unlock()
}

// Start launches the durable job worker and the recurring search scheduler.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.scansMu.Lock()
		s.runCtx = runCtx
		s.scansMu.Unlock()
		s.cancel = cancel
		if recovered, err := s.store.RecoverJobs(runCtx); err != nil {
			s.logger.Error("subtitles: could not recover jobs", "error", err)
		} else if recovered > 0 {
			s.logger.Info("subtitles: requeued interrupted jobs", "count", recovered)
		}
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.loop(runCtx)
		}()
		s.wakeup()
	})
}

// Close stops the worker, the scheduler, tracked scans, and any running helper process.
func (s *Service) Close() {
	s.scansMu.Lock()
	s.closed = true
	s.scansMu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.workers.Wait()
	s.scans.Wait()
	s.jobsMu.Lock()
	for _, cancel := range s.jobs {
		cancel()
	}
	s.jobsMu.Unlock()
}

func (s *Service) wakeup() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) loop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		s.tick(ctx)
		timer.Reset(tickInterval)
	}
}

func (s *Service) tick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	if jobs, err := s.store.Jobs(ctx, 5, true); err == nil {
		for _, job := range jobs {
			if job.Status == "queued" {
				s.execute(ctx, job)
				return
			}
		}
	} else if ctx.Err() == nil {
		s.logger.Error("subtitles: job queue could not be read", "error", err)
	}
	s.schedule(ctx)
}

func (s *Service) schedule(ctx context.Context) {
	cfg, err := s.store.Config(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	last := time.Unix(s.lastScan.Load(), 0)
	if last.IsZero() || time.Since(last) >= time.Duration(cfg.ScanMinutes)*time.Minute {
		if s.beginScan() {
			_, err := s.scan(ctx)
			s.scanRunning.Store(false)
			if err != nil {
				s.logger.Error("subtitles: scheduled scan failed", "error", err)
				return
			}
		}
	}
	if !cfg.AutoSearch {
		return
	}
	if len(s.configuredProviders(cfg)) == 0 {
		return
	}
	due, err := s.store.DueWanted(ctx, 10)
	if err != nil {
		s.logger.Error("subtitles: wanted queue could not be read", "error", err)
		return
	}
	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		s.autoSearchItem(ctx, cfg, item)
	}
}

// Scan reconciles stored sidecars and wanted rows with the files on disk.
func (s *Service) Scan(ctx context.Context) (ScanSummary, error) {
	if !s.beginScan() {
		return ScanSummary{}, fmt.Errorf("%w: a subtitle scan is already running", ErrConflict)
	}
	defer s.scanRunning.Store(false)
	return s.scan(ctx)
}

// beginScan claims the single scan slot shared by the API and the scheduler.
func (s *Service) beginScan() bool { return s.scanRunning.CompareAndSwap(false, true) }

// launchScan runs one tracked background scan under the service context so a restore can drain it.
func (s *Service) launchScan() error {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: subtitles are shutting down", ErrUnavailable)
	}
	if !s.beginScan() {
		return fmt.Errorf("%w: a subtitle scan is already running", ErrConflict)
	}
	base := s.runCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, scanTimeout)
	s.scans.Add(1)
	go func() {
		defer s.scans.Done()
		defer cancel()
		defer s.scanRunning.Store(false)
		if _, err := s.scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("subtitles: manual scan failed", "error", err)
		}
	}()
	return nil
}

func (s *Service) scan(ctx context.Context) (ScanSummary, error) {
	summary := ScanSummary{}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return summary, err
	}
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return summary, err
	}
	byID := map[string]Profile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	assignments, err := s.store.Assignments(ctx)
	if err != nil {
		return summary, err
	}
	videos, err := s.catalog.Videos(ctx)
	if err != nil {
		return summary, err
	}
	for _, video := range videos {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		summary.Videos++
		found, err := ListSidecars(video.rootPath, video.Path)
		if err != nil {
			// A missing or unreadable directory keeps the previous inventory instead of dropping it.
			continue
		}
		records := make([]Sidecar, 0, len(found))
		for _, file := range found {
			rel := file.Name
			if dir := path.Dir(video.Path); dir != "." {
				rel = dir + "/" + file.Name
			}
			records = append(records, Sidecar{
				Kind: video.Kind, VideoID: video.ID, Path: rel, Language: file.Language,
				Format: string(file.Format), Forced: file.Forced, HI: file.HI,
				Source: "scan", Size: file.Size, UpdatedAt: time.Now().UTC(),
			})
		}
		if err := s.store.ReplaceSidecars(ctx, video.Kind, video.ID, records); err != nil {
			return summary, err
		}
		summary.Sidecars += len(records)
		assignment := assignments[videoKey(video.Kind, video.ID)]
		profile := byID[assignment.ProfileID]
		if profile.ID == "" {
			profile = byID[cfg.DefaultProfileID]
		}
		if profile.ID == "" && len(profiles) > 0 {
			profile = profiles[0]
		}
		monitored := true
		if _, ok := assignments[videoKey(video.Kind, video.ID)]; ok {
			monitored = assignment.Monitored
		}
		rows := wantedRows(profile, records, monitored)
		if err := s.store.ReplaceWanted(ctx, video.Kind, video.ID, rows); err != nil {
			return summary, err
		}
		for _, row := range rows {
			if row.Status == "wanted" {
				summary.Wanted++
			}
		}
	}
	s.lastScan.Store(time.Now().Unix())
	return summary, nil
}

// wantedRows computes the wanted variants of one video from its profile and sidecars.
func wantedRows(profile Profile, sidecars []Sidecar, monitored bool) []Wanted {
	variants := profileVariants(profile)
	if len(variants) == 0 {
		return nil
	}
	if !monitored {
		rows := make([]Wanted, 0, len(variants))
		for _, variant := range variants {
			rows = append(rows, Wanted{Language: variant.Code, Forced: variant.Forced, HI: variant.HI, Status: "ignored"})
		}
		return rows
	}
	languageOrder := []string{}
	satisfiedLanguages := map[string]bool{}
	for _, variant := range variants {
		if _, ok := satisfiedLanguages[variant.Code]; !ok {
			satisfiedLanguages[variant.Code] = false
			languageOrder = append(languageOrder, variant.Code)
		}
		if sidecarSatisfies(sidecars, variant) {
			satisfiedLanguages[variant.Code] = true
		}
	}
	cutoff := profile.Cutoff
	if cutoff <= 0 || cutoff > len(languageOrder) {
		cutoff = len(languageOrder)
	}
	covered := 0
	for _, language := range languageOrder {
		if satisfiedLanguages[language] {
			covered++
		}
	}
	cutoffReached := covered >= cutoff
	rows := make([]Wanted, 0, len(variants))
	for _, variant := range variants {
		row := Wanted{Language: variant.Code, Forced: variant.Forced, HI: variant.HI, Status: "wanted"}
		switch {
		case sidecarSatisfies(sidecars, variant):
			row.Status = "satisfied"
		case cutoffReached:
			row.Status = "cutoff"
		}
		rows = append(rows, row)
	}
	return rows
}

func sidecarSatisfies(sidecars []Sidecar, want LanguagePreference) bool {
	for _, sidecar := range sidecars {
		if sidecar.Language == "" || sidecar.Forced != want.Forced {
			continue
		}
		// A hearing-impaired file still covers plain dialogue; a plain file never covers a HI request.
		if want.HI && !sidecar.HI {
			continue
		}
		if languageMatches(sidecar.Language, want.Code) {
			return true
		}
	}
	return false
}

// profileVariants expands a profile into the wanted language variants.
func profileVariants(profile Profile) []LanguagePreference {
	variants := make([]LanguagePreference, 0, len(profile.Languages))
	seen := map[string]bool{}
	for _, item := range profile.Languages {
		language, ok := NormalizeLanguage(item.Code)
		if !ok {
			continue
		}
		candidates := []LanguagePreference{{Code: language}}
		if item.Forced {
			candidates = append(candidates, LanguagePreference{Code: language, Forced: true})
		}
		if item.HI {
			candidates = append(candidates, LanguagePreference{Code: language, HI: true})
		}
		for _, candidate := range candidates {
			key := fmt.Sprintf("%s|%t|%t", candidate.Code, candidate.Forced, candidate.HI)
			if seen[key] {
				continue
			}
			seen[key] = true
			variants = append(variants, candidate)
		}
	}
	return variants
}

// Library lists catalog videos with their subtitle state.
func (s *Service) Library(ctx context.Context, filter LibraryFilter) ([]LibraryItem, error) {
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return nil, err
	}
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]Profile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	assignments, err := s.store.Assignments(ctx)
	if err != nil {
		return nil, err
	}
	sidecars, err := s.store.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	wanted, err := s.store.Wanted(ctx)
	if err != nil {
		return nil, err
	}
	outputs, err := s.store.Outputs(ctx, "", "")
	if err != nil {
		return nil, err
	}
	outputsByVideo := map[string][]Output{}
	for _, output := range outputs {
		key := videoKey(output.VideoKind, output.VideoID)
		outputsByVideo[key] = append(outputsByVideo[key], output)
	}
	videos, err := s.catalog.Videos(ctx)
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > maxLibraryLimit {
		limit = libraryLimit
	}
	items := make([]LibraryItem, 0, len(videos))
	for _, video := range videos {
		key := videoKey(video.Kind, video.ID)
		assignment := assignments[key]
		profileID := assignment.ProfileID
		if profileID == "" {
			profileID = cfg.DefaultProfileID
		}
		if profileID == "" && len(profiles) > 0 {
			profileID = profiles[0].ID
		}
		monitored := true
		if _, ok := assignments[key]; ok {
			monitored = assignment.Monitored
		}
		item := LibraryItem{
			Video:     video,
			Sidecars:  sidecars[key],
			Wanted:    wanted[key],
			Outputs:   outputsByVideo[key],
			Profile:   profileID,
			Monitored: monitored,
		}
		if item.Sidecars == nil {
			item.Sidecars = []Sidecar{}
		}
		if item.Wanted == nil {
			item.Wanted = []Wanted{}
		}
		if item.Outputs == nil {
			item.Outputs = []Output{}
		}
		for _, row := range item.Wanted {
			if row.Status == "wanted" {
				item.Missing++
			}
		}
		if !matchesLibraryFilter(item, filter) {
			continue
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	return items, nil
}

func matchesLibraryFilter(item LibraryItem, filter LibraryFilter) bool {
	if filter.Kind != "" && item.Video.Kind != filter.Kind {
		return false
	}
	switch filter.Status {
	case "missing":
		if item.Missing == 0 {
			return false
		}
	case "satisfied":
		if item.Missing != 0 {
			return false
		}
	case "unmonitored":
		if item.Monitored {
			return false
		}
	}
	if filter.Missing && item.Missing == 0 {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		item.Video.Title, item.Video.SeriesTitle, item.Video.Path, item.Video.IMDbID, item.Video.SeriesIMDbID,
	}, " "))
	return strings.Contains(haystack, query)
}

// WantedItem pairs one wanted variant with its video.
type WantedItem struct {
	Video  Video  `json:"video"`
	Wanted Wanted `json:"wanted"`
}

// Wanted lists pending wanted variants across the catalog.
func (s *Service) Wanted(ctx context.Context) ([]WantedItem, error) {
	wanted, err := s.store.Wanted(ctx)
	if err != nil {
		return nil, err
	}
	videos, err := s.catalog.Videos(ctx)
	if err != nil {
		return nil, err
	}
	items := []WantedItem{}
	for _, video := range videos {
		for _, row := range wanted[videoKey(video.Kind, video.ID)] {
			if row.Status != "wanted" && row.Status != "cutoff" {
				continue
			}
			items = append(items, WantedItem{Video: video, Wanted: row})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Wanted.Attempts != items[j].Wanted.Attempts {
			return items[i].Wanted.Attempts < items[j].Wanted.Attempts
		}
		return items[i].Video.Title < items[j].Video.Title
	})
	return items, nil
}

func (s *Service) History(ctx context.Context, kind, id string, limit int) ([]HistoryEntry, error) {
	if kind != "" && kind != KindMovie && kind != KindEpisode {
		return nil, fmt.Errorf("%w: video kind must be movie or episode", ErrInvalid)
	}
	return s.store.History(ctx, kind, id, limit)
}

func (s *Service) Jobs(ctx context.Context, activeOnly bool, limit int) ([]Job, error) {
	return s.store.Jobs(ctx, limit, activeOnly)
}

func (s *Service) Job(ctx context.Context, id string) (Job, error) {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return Job{}, fmt.Errorf("%w: job id is invalid", ErrInvalid)
	}
	return s.store.Job(ctx, id)
}

// CancelJob stops a queued or running job.
func (s *Service) CancelJob(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return fmt.Errorf("%w: job id is invalid", ErrInvalid)
	}
	err := s.store.CancelJob(ctx, id)
	s.jobsMu.Lock()
	cancel := s.jobs[id]
	s.jobsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return err
}

// Config returns the configuration with credentials removed.
func (s *Service) Config(ctx context.Context) (Config, error) {
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	return configView(cfg), nil
}

// SetConfig validates and persists a configuration update.
func (s *Service) SetConfig(ctx context.Context, input Config) (Config, error) {
	next, err := normalizeConfig(input)
	if err != nil {
		return Config{}, err
	}
	saved, err := s.store.SaveConfig(ctx, next)
	if err != nil {
		return Config{}, err
	}
	return configView(saved), nil
}

func defaultConfig() Config {
	return Config{
		Enabled:                true,
		AutoSearch:             true,
		AutoDownload:           true,
		ScanMinutes:            defaultScanMinutes,
		SearchIntervalHours:    defaultSearchHours,
		RetryMinutes:           defaultRetryMinutes,
		CutoffScore:            defaultCutoffScore,
		ProviderTimeoutSeconds: defaultProviderSecs,
		DefaultProfileID:       "default",
		Providers: []Provider{{
			ID: "opensubtitles", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
			Endpoint: "https://api.opensubtitles.com/api/v1", Enabled: true,
		}},
		Sync: SyncConfig{
			TimeoutSeconds:         defaultSyncTimeout,
			MaxOffsetSeconds:       60,
			MinScore:               0,
			QualityMaxOffsetSecs:   30,
			MaxFramerateDeviation:  0.1,
			AudioReferenceSeconds:  3600,
			MaxEmbeddedStreamIndex: 64,
		},
		AI: AIConfig{
			TimeoutSeconds: defaultAITimeoutSecs,
			MaxTokens:      defaultMaxTokens,
			MaxRequests:    defaultMaxRequests,
			MaxTotalTokens: defaultTotalTokens,
			MaxCharacters:  defaultChunkChars,
			Temperature:    0.2,
		},
	}
}

func configView(cfg Config) Config {
	cfg.Providers = append([]Provider(nil), cfg.Providers...)
	for i := range cfg.Providers {
		cfg.Providers[i].PasswordSet = cfg.Providers[i].Password != ""
		cfg.Providers[i].APIKeySet = cfg.Providers[i].APIKey != ""
		cfg.Providers[i].Password = ""
		cfg.Providers[i].APIKey = ""
	}
	cfg.AI.APIKeySet = cfg.AI.APIKey != ""
	cfg.AI.APIKey = ""
	return cfg
}

func normalizeConfig(input Config) (Config, error) {
	cfg := input
	cfg.ScanMinutes = clamp(cfg.ScanMinutes, 5, 1440, defaultScanMinutes)
	cfg.SearchIntervalHours = clamp(cfg.SearchIntervalHours, 1, 168, defaultSearchHours)
	cfg.RetryMinutes = clamp(cfg.RetryMinutes, 5, 1440, defaultRetryMinutes)
	cfg.CutoffScore = clamp(cfg.CutoffScore, 0, 200, defaultCutoffScore)
	cfg.ProviderTimeoutSeconds = clamp(cfg.ProviderTimeoutSeconds, 5, 120, defaultProviderSecs)
	cfg.DefaultProfileID = strings.TrimSpace(cfg.DefaultProfileID)
	if cfg.DefaultProfileID != "" && !profileIDPattern.MatchString(cfg.DefaultProfileID) {
		return Config{}, fmt.Errorf("%w: default profile id is invalid", ErrInvalid)
	}
	if len(cfg.Providers) == 0 {
		cfg.Providers = defaultConfig().Providers
	}
	if len(cfg.Providers) > maxProviders {
		return Config{}, fmt.Errorf("%w: at most %d providers are supported", ErrInvalid, maxProviders)
	}
	seen := map[string]bool{}
	normalized := make([]Provider, 0, len(cfg.Providers))
	for _, item := range cfg.Providers {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = providerTypeOpenSubtitles + "-" + rand.Text()[:6]
		}
		if !profileIDPattern.MatchString(item.ID) || seen[item.ID] {
			return Config{}, fmt.Errorf("%w: provider id %q is invalid", ErrInvalid, item.ID)
		}
		seen[item.ID] = true
		item.Name = truncate(item.Name, 64)
		if item.Name == "" {
			item.Name = "OpenSubtitles"
		}
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		if item.Type == "" {
			item.Type = providerTypeOpenSubtitles
		}
		if item.Type != providerTypeOpenSubtitles {
			return Config{}, fmt.Errorf("%w: unsupported provider type %q", ErrInvalid, item.Type)
		}
		item.Endpoint = strings.TrimRight(strings.TrimSpace(item.Endpoint), "/")
		if item.Endpoint == "" {
			item.Endpoint = "https://api.opensubtitles.com/api/v1"
		}
		parsed, err := url.Parse(item.Endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(item.Endpoint) > maxPathBytes {
			return Config{}, fmt.Errorf("%w: provider endpoint must be an http(s) URL", ErrInvalid)
		}
		item.Username = truncate(item.Username, 128)
		if len(item.Password) > 512 || len(item.APIKey) > 512 {
			return Config{}, fmt.Errorf("%w: provider credentials are too long", ErrInvalid)
		}
		normalized = append(normalized, item)
	}
	cfg.Providers = normalized
	if cfg.DefaultProfileID == "" {
		cfg.DefaultProfileID = "default"
	}
	cfg.Sync.HelperPath = strings.TrimSpace(cfg.Sync.HelperPath)
	cfg.Sync.FFmpegPath = strings.TrimSpace(cfg.Sync.FFmpegPath)
	cfg.Sync.TimeoutSeconds = clamp(cfg.Sync.TimeoutSeconds, 30, maxHelperTimeout, defaultSyncTimeout)
	cfg.Sync.MaxOffsetSeconds = clampFloat(cfg.Sync.MaxOffsetSeconds, 0, 600, 60)
	cfg.Sync.MinScore = clampFloat(cfg.Sync.MinScore, -1000, 1000, 0)
	cfg.Sync.QualityMaxOffsetSecs = clampFloat(cfg.Sync.QualityMaxOffsetSecs, 0, 600, 30)
	cfg.Sync.MaxFramerateDeviation = clampFloat(cfg.Sync.MaxFramerateDeviation, 0, 1, 0.1)
	cfg.Sync.AudioReferenceSeconds = clamp(cfg.Sync.AudioReferenceSeconds, 0, maxAudioSeconds, 3600)
	cfg.Sync.MaxEmbeddedStreamIndex = clamp(cfg.Sync.MaxEmbeddedStreamIndex, 0, maxStreamIndex, 64)
	cfg.Sync.VAD = strings.ToLower(strings.TrimSpace(cfg.Sync.VAD))
	if !allowedVAD[cfg.Sync.VAD] {
		return Config{}, fmt.Errorf("%w: unsupported VAD %q", ErrInvalid, cfg.Sync.VAD)
	}
	cfg.AI.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.AI.BaseURL), "/")
	cfg.AI.Model = truncate(cfg.AI.Model, 128)
	if cfg.AI.Enabled {
		parsed, err := url.Parse(cfg.AI.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Config{}, fmt.Errorf("%w: the AI base URL must be an http(s) URL", ErrInvalid)
		}
		if cfg.AI.Model == "" {
			return Config{}, fmt.Errorf("%w: an AI model name is required", ErrInvalid)
		}
	}
	if len(cfg.AI.APIKey) > 512 {
		return Config{}, fmt.Errorf("%w: the AI key is too long", ErrInvalid)
	}
	cfg.AI.TimeoutSeconds = clamp(cfg.AI.TimeoutSeconds, 10, 900, defaultAITimeoutSecs)
	cfg.AI.MaxTokens = clamp(cfg.AI.MaxTokens, 256, 16384, defaultMaxTokens)
	cfg.AI.MaxRequests = clamp(cfg.AI.MaxRequests, 1, 2000, defaultMaxRequests)
	cfg.AI.MaxTotalTokens = clamp(cfg.AI.MaxTotalTokens, 1000, 2_000_000, defaultTotalTokens)
	cfg.AI.MaxCharacters = clamp(cfg.AI.MaxCharacters, 500, maxChunkChars, defaultChunkChars)
	cfg.AI.Temperature = clampFloat(cfg.AI.Temperature, 0, 2, 0.2)
	return cfg, nil
}

func clamp(value, low, high, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func clampFloat(value, low, high, fallback float64) float64 {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// Profiles lists language profiles.
func (s *Service) Profiles(ctx context.Context) ([]Profile, error) {
	return s.store.Profiles(ctx)
}

// SaveProfile validates and stores one language profile.
func (s *Service) SaveProfile(ctx context.Context, profile Profile) (Profile, error) {
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return Profile{}, err
	}
	profile.ID = strings.TrimSpace(profile.ID)
	if profile.ID == "" {
		profile.ID = "profile-" + rand.Text()[:8]
	}
	if !profileIDPattern.MatchString(profile.ID) {
		return Profile{}, fmt.Errorf("%w: profile id is invalid", ErrInvalid)
	}
	profile.Name = truncate(profile.Name, 64)
	if profile.Name == "" {
		profile.Name = "Subtitle profile"
	}
	if len(profile.Languages) > maxProfileLanguages {
		return Profile{}, fmt.Errorf("%w: at most %d languages are supported per profile", ErrInvalid, maxProfileLanguages)
	}
	languages := make([]LanguagePreference, 0, len(profile.Languages))
	seen := map[string]bool{}
	for _, item := range profile.Languages {
		language, ok := NormalizeLanguage(item.Code)
		if !ok {
			return Profile{}, fmt.Errorf("%w: unsupported language code %q", ErrInvalid, item.Code)
		}
		key := fmt.Sprintf("%s|%t|%t", language, item.Forced, item.HI)
		if seen[key] {
			continue
		}
		seen[key] = true
		languages = append(languages, LanguagePreference{Code: language, Forced: item.Forced, HI: item.HI})
	}
	if len(languages) == 0 {
		return Profile{}, fmt.Errorf("%w: add at least one language to the profile", ErrInvalid)
	}
	profile.Languages = languages
	profile.Cutoff = clamp(profile.Cutoff, 1, len(languages), len(languages))
	if _, exists := findProfile(profiles, profile.ID); !exists && len(profiles) >= maxProfiles {
		return Profile{}, fmt.Errorf("%w: at most %d profiles are supported", ErrInvalid, maxProfiles)
	}
	return s.store.SaveProfile(ctx, profile)
}

func findProfile(profiles []Profile, id string) (Profile, bool) {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

// DeleteProfile removes a language profile that is not assigned to a video.
func (s *Service) DeleteProfile(ctx context.Context, id string) error {
	assignments, err := s.store.Assignments(ctx)
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.ProfileID == id {
			return fmt.Errorf("%w: the profile is assigned to a video", ErrConflict)
		}
	}
	return s.store.DeleteProfile(ctx, id)
}

// SetAssignment binds a language profile to one catalog video.
func (s *Service) SetAssignment(ctx context.Context, kind, id, profileID string, monitored bool) (Assignment, error) {
	video, err := s.catalog.Video(ctx, kind, id)
	if err != nil {
		return Assignment{}, err
	}
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return Assignment{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return Assignment{}, err
	}
	if profileID == "" {
		profileID = cfg.DefaultProfileID
	}
	if _, ok := findProfile(profiles, profileID); !ok {
		return Assignment{}, fmt.Errorf("%w: language profile does not exist", ErrInvalid)
	}
	item := Assignment{Kind: video.Kind, VideoID: video.ID, ProfileID: profileID, Monitored: monitored}
	if err := s.store.SetAssignment(ctx, item); err != nil {
		return Assignment{}, err
	}
	return item, nil
}

// ResetAssignment reverts a video to the default profile and monitoring.
func (s *Service) ResetAssignment(ctx context.Context, kind, id string) error {
	if _, err := s.catalog.Video(ctx, kind, id); err != nil {
		return err
	}
	return s.store.DeleteAssignment(ctx, kind, id)
}

// Providers reports provider health and quota without exposing credentials.
func (s *Service) Providers(ctx context.Context) ([]ProviderStatus, error) {
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]ProviderStatus, 0, len(cfg.Providers))
	for _, item := range cfg.Providers {
		status := ProviderStatus{
			ProviderID: item.ID, Name: item.Name, Type: item.Type,
			Configured: item.APIKey != "", Enabled: item.Enabled,
		}
		s.providerMu.Lock()
		if known, ok := s.providerStat[item.ID]; ok {
			status.LastError = known.LastError
			status.QuotaRemaining = known.QuotaRemaining
			status.QuotaReset = known.QuotaReset
		}
		s.providerMu.Unlock()
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// TestProviders checks each enabled provider with a bounded request.
func (s *Service) TestProviders(ctx context.Context) ([]ProviderTest, error) {
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return nil, err
	}
	client := s.providerClient()
	results := []ProviderTest{}
	for _, item := range cfg.Providers {
		if !item.Enabled {
			continue
		}
		test := ProviderTest{ProviderID: item.ID, Name: item.Name}
		provider, err := newProvider(item, client)
		if err != nil {
			test.Error = err.Error()
			results = append(results, test)
			continue
		}
		testCtx, cancel := context.WithTimeout(ctx, time.Duration(clamp(cfg.ProviderTimeoutSeconds, 5, 120, defaultProviderSecs))*time.Second)
		message, err := provider.Test(testCtx)
		cancel()
		if err != nil {
			test.Error = publicError(err)
			s.noteProviderError(item.ID, test.Error, 0, "")
		} else {
			test.OK, test.Message = true, truncate(message, 200)
		}
		results = append(results, test)
	}
	return results, nil
}

func (s *Service) providerClient() *http.Client {
	// Per-request contexts bound every provider call; this client only shares connection state.
	return &http.Client{}
}

// configuredProviders builds the enabled, credentialled provider clients.
func (s *Service) configuredProviders(cfg Config) []provider {
	client := s.providerClient()
	out := []provider{}
	for _, item := range cfg.Providers {
		if !item.Enabled {
			continue
		}
		provider, err := newProvider(item, client)
		if err != nil {
			continue
		}
		if !provider.Configured() {
			continue
		}
		out = append(out, provider)
	}
	return out
}

func (s *Service) noteProviderError(id, message string, remaining int, reset string) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	status := s.providerStat[id]
	status.LastError = truncate(message, maxTextRunes)
	if remaining > 0 || reset != "" {
		value := remaining
		status.QuotaRemaining = &value
		status.QuotaReset = reset
	}
	s.providerStat[id] = status
}

func (s *Service) clearProviderError(id string, remaining int, reset string) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	status := s.providerStat[id]
	status.LastError = ""
	if remaining > 0 || reset != "" {
		value := remaining
		status.QuotaRemaining = &value
		status.QuotaReset = reset
	}
	s.providerStat[id] = status
}

// execute runs one durable job and records its terminal state.
func (s *Service) execute(parent context.Context, job Job) {
	ctx, cancel := context.WithCancel(parent)
	s.jobsMu.Lock()
	if _, running := s.jobs[job.ID]; running {
		s.jobsMu.Unlock()
		cancel()
		return
	}
	s.jobs[job.ID] = cancel
	s.jobsMu.Unlock()
	defer func() {
		s.jobsMu.Lock()
		delete(s.jobs, job.ID)
		s.jobsMu.Unlock()
		cancel()
	}()

	bg, bgCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer bgCancel()
	started, err := s.store.StartJob(bg, job.ID)
	if err != nil || !started {
		return
	}
	detail, err := s.runJob(ctx, job, bg)
	if ctx.Err() != nil {
		_ = s.store.UpdateJob(bg, job.ID, "cancelled", 100, "cancelled", "")
		return
	}
	if err != nil {
		message := publicError(err)
		if updateErr := s.store.UpdateJob(bg, job.ID, "failed", 100, publicDetail(detail, message), message); updateErr != nil {
			s.logger.Error("subtitles: job failure could not be recorded", "job", job.ID, "error", updateErr)
		}
		return
	}
	if err := s.store.UpdateJob(bg, job.ID, "done", 100, detail, ""); err != nil {
		s.logger.Error("subtitles: job completion could not be recorded", "job", job.ID, "error", err)
	}
}

// runJob dispatches one job payload; background is a separate context for durable progress writes.
func (s *Service) runJob(ctx context.Context, job Job, background context.Context) (string, error) {
	switch job.Kind {
	case "download":
		return s.runDownload(ctx, job, background)
	case "sync":
		return s.runSync(ctx, job)
	case "translate":
		return s.runTranslate(ctx, job, background)
	case "extract":
		return s.runExtract(ctx, job)
	default:
		return "", fmt.Errorf("%w: unsupported job kind %q", ErrInvalid, job.Kind)
	}
}

// VideoDetail is one video with its subtitle state and recent history.
type VideoDetail struct {
	Video     Video          `json:"video"`
	Sidecars  []Sidecar      `json:"sidecars"`
	Wanted    []Wanted       `json:"wanted"`
	Outputs   []Output       `json:"outputs"`
	History   []HistoryEntry `json:"history"`
	Profile   string         `json:"profileId"`
	Monitored bool           `json:"monitored"`
}

// Detail returns one catalog video with its stored subtitle state.
func (s *Service) Detail(ctx context.Context, kind, id string, historyLimit int) (VideoDetail, error) {
	video, err := s.resolveVideo(ctx, kind, id)
	if err != nil {
		return VideoDetail{}, err
	}
	cfg, err := s.store.Config(ctx)
	if err != nil {
		return VideoDetail{}, err
	}
	profiles, err := s.store.Profiles(ctx)
	if err != nil {
		return VideoDetail{}, err
	}
	assignments, err := s.store.Assignments(ctx)
	if err != nil {
		return VideoDetail{}, err
	}
	sidecars, err := s.store.Inventory(ctx)
	if err != nil {
		return VideoDetail{}, err
	}
	wanted, err := s.store.Wanted(ctx)
	if err != nil {
		return VideoDetail{}, err
	}
	outputs, err := s.store.Outputs(ctx, kind, id)
	if err != nil {
		return VideoDetail{}, err
	}
	history, err := s.store.History(ctx, kind, id, historyLimit)
	if err != nil {
		return VideoDetail{}, err
	}
	key := videoKey(kind, id)
	assignment := assignments[key]
	profileID := assignment.ProfileID
	if profileID == "" {
		profileID = cfg.DefaultProfileID
	}
	if _, ok := findProfile(profiles, profileID); !ok && len(profiles) > 0 {
		profileID = profiles[0].ID
	}
	monitored := true
	if _, ok := assignments[key]; ok {
		monitored = assignment.Monitored
	}
	detail := VideoDetail{
		Video: video, Sidecars: sidecars[key], Wanted: wanted[key], Outputs: outputs,
		History: history, Profile: profileID, Monitored: monitored,
	}
	if detail.Sidecars == nil {
		detail.Sidecars = []Sidecar{}
	}
	if detail.Wanted == nil {
		detail.Wanted = []Wanted{}
	}
	if detail.Outputs == nil {
		detail.Outputs = []Output{}
	}
	if detail.History == nil {
		detail.History = []HistoryEntry{}
	}
	return detail, nil
}

// OpenSidecar returns a subtitle file that belongs to the given catalog video.
func (s *Service) OpenSidecar(ctx context.Context, kind, id, rel string) (*os.File, error) {
	video, err := s.catalog.Video(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if err := ValidateSidecarPath(video.Path, rel); err != nil {
		return nil, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	handle, err := library.Open(video.rootPath, clean)
	if err != nil {
		return nil, fmt.Errorf("%w: subtitle file is not available", ErrNotFound)
	}
	return handle, nil
}

// MoveVideoSidecars renames sidecars after the video itself moved and updates stored paths.
func (s *Service) MoveVideoSidecars(ctx context.Context, kind, id, fromVideo, toVideo string) ([]MovedSidecar, error) {
	video, err := s.catalog.Video(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	from, err := cleanRel(fromVideo)
	if err != nil {
		return nil, err
	}
	to, err := cleanRel(toVideo)
	if err != nil {
		return nil, err
	}
	if path.Dir(from) != path.Dir(video.Path) || path.Dir(to) != path.Dir(video.Path) {
		return nil, fmt.Errorf("%w: sidecars can only move with their video directory", ErrUnsafe)
	}
	moved, err := MoveSidecars(video.rootPath, from, to)
	if err != nil {
		return moved, err
	}
	if len(moved) == 0 {
		return moved, nil
	}
	sidecars, err := s.store.Inventory(ctx)
	if err != nil {
		return moved, err
	}
	for _, sidecar := range sidecars[videoKey(kind, id)] {
		for _, item := range moved {
			if sidecar.Path == item.From {
				sidecar.Path = item.To
				if err := s.store.UpsertSidecar(ctx, sidecar); err != nil {
					return moved, err
				}
				if err := s.store.DeleteSidecar(ctx, kind, id, item.From); err != nil {
					return moved, err
				}
				break
			}
		}
	}
	return moved, nil
}

// publicError renders an error for API responses without leaking database internals.
func publicError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errDatabase) {
		return "the subtitle database operation failed"
	}
	return truncate(strings.TrimSpace(err.Error()), 400)
}

func publicDetail(detail, message string) string {
	detail = strings.TrimSpace(detail)
	message = strings.TrimSpace(message)
	if detail == "" {
		return truncate(message, maxTextRunes)
	}
	if message == "" || strings.Contains(detail, message) {
		return truncate(detail, maxTextRunes)
	}
	return truncate(detail+" ("+message+")", maxTextRunes)
}
