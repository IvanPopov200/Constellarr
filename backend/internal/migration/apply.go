package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

// ApplyInput carries the explicit mappings and source selections the wizard collected.
type ApplyInput struct {
	Movies          bool              `json:"movies"`
	TV              bool              `json:"tv"`
	Music           bool              `json:"music"`
	Files           bool              `json:"files"`
	Naming          bool              `json:"naming"`
	Providers       bool              `json:"providers"`
	Subtitles       bool              `json:"subtitles"`
	Torrents        bool              `json:"torrents"`
	TorrentJobs     bool              `json:"torrentJobs"`
	CreateProfiles  bool              `json:"createProfiles"`
	RetryFailed     bool              `json:"retryFailed"`
	MoviesRoots     map[string]string `json:"moviesRoots"`
	TVRoots         map[string]string `json:"tvRoots"`
	MusicRoots      map[string]string `json:"musicRoots"`
	TorrentRoots    map[string]string `json:"torrentRoots"`
	Profiles        map[string]string `json:"profiles"`
	MusicProfiles   map[string]string `json:"musicProfiles"`
	Indexer         string            `json:"indexer"`
	Usenet          string            `json:"usenet"`
	UsenetFallbacks []string          `json:"usenetFallbacks"`
	Torznab         []string          `json:"torznab"`
	Redownload      []string          `json:"redownload"`
	Connections     []Connection      `json:"connections"`
}

type ApplyResult struct {
	PlanID    string     `json:"planId"`
	Status    string     `json:"status"`
	Remaining int        `json:"remaining"`
	Totals    ItemTotals `json:"totals"`
	Applied   []PlanItem `json:"applied"`
	Failures  []PlanItem `json:"failures"`
	Message   string     `json:"message,omitempty"`
}

func (input ApplyInput) validate() error {
	for _, roots := range []map[string]string{input.MoviesRoots, input.TVRoots, input.MusicRoots, input.TorrentRoots} {
		for source, local := range roots {
			if strings.TrimSpace(source) == "" {
				return fmt.Errorf("%w: a source path is empty", ErrInvalid)
			}
			if err := localDirectory(strings.TrimSpace(local)); err != nil {
				return fmt.Errorf("%w: %s: %s", ErrInvalid, source, err)
			}
		}
	}
	if len(input.Connections) > 0 {
		if _, err := validateConnections(input.Connections); err != nil {
			return err
		}
	}
	return nil
}

// Apply processes one bounded batch of pending items so a long import stays restartable.
func (s *Service) Apply(ctx context.Context, planID string, input ApplyInput) (ApplyResult, error) {
	if err := input.validate(); err != nil {
		return ApplyResult{}, err
	}
	plan, err := s.load(ctx, planID)
	if err != nil {
		return ApplyResult{}, err
	}
	lock := s.applyLock(planID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.claim(ctx, planID); err != nil {
		return ApplyResult{}, err
	}
	if input.RetryFailed {
		if _, err := s.pool.Exec(ctx,
			`UPDATE migration_plan_items SET status = $2, message = '', updated_at = now()
			 WHERE plan_id = $1 AND status = $3`, planID, itemPending, itemFailed); err != nil {
			return ApplyResult{}, errors.New("migration: the failed items could not be reset")
		}
	}
	connections := input.Connections
	if len(connections) > 0 {
		normalized, err := validateConnections(connections)
		if err != nil {
			return ApplyResult{}, err
		}
		connections = normalized
	}
	if entry := s.secrets.get(planID, s.now()); entry == nil && len(connections) > 0 {
		s.secrets.rearm(planID, connections, s.ttl, s.now())
	}
	entry := s.secrets.get(planID, s.now())
	if entry == nil {
		entry = &secretEntry{expires: plan.ExpiresAt}
	}
	resolver := &resolver{service: s, ctx: ctx, plan: plan, input: input, profiles: map[string]string{}, musicProfiles: map[string]string{}, roots: map[string]string{}}
	deadline := s.now().Add(s.budget)
	pending, err := s.items(ctx, planID, itemPending, s.batch)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(connections) > 0 && providerWorkPending(plan, pending, entry) {
		if refreshed := s.refreshProviderSecrets(ctx, plan, connections); refreshed != nil {
			entry = refreshed
		}
	}
	resolver.news = selectNewsServers(plan, input.Usenet, input.UsenetFallbacks, entry.providers)
	resolver.secrets = entry.providers
	result := ApplyResult{PlanID: planID, Applied: []PlanItem{}, Failures: []PlanItem{}}
	for _, item := range pending {
		if s.now().After(deadline) || ctx.Err() != nil {
			break
		}
		status, message := s.applyItem(ctx, plan, item, resolver, entry)
		message = sanitize(message, entry.sanitizer())
		if err := s.mark(ctx, planID, item.Position, status, message); err != nil {
			return ApplyResult{}, err
		}
		item.Status, item.Message = status, message
		if status == itemFailed {
			result.Failures = append(result.Failures, item)
		} else {
			result.Applied = append(result.Applied, item)
		}
	}
	totals, err := s.totals(ctx, planID)
	if err != nil {
		return ApplyResult{}, err
	}
	result.Totals = totals
	result.Remaining = totals.Pending
	switch {
	case totals.Pending > 0:
		result.Status = statusReady
		result.Message = "more items remain; apply again to continue"
	case totals.Failed > 0:
		result.Status = statusPartial
		result.Message = "some items need attention; fix them and apply again with retryFailed"
	default:
		result.Status = statusApplied
	}
	if err := s.setStatus(ctx, planID, result.Status); err != nil {
		return ApplyResult{}, err
	}
	if result.Status != statusReady {
		s.secrets.drop(planID)
	}
	return result, nil
}

// claim prevents concurrent duplicate applies while letting a crashed apply be reclaimed.
func (s *Service) claim(ctx context.Context, planID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE migration_plans SET status = $2, updated_at = now()
		 WHERE id = $1 AND expires_at > now()
		   AND (status <> $2 OR updated_at < now() - interval '2 minutes')`,
		planID, statusApplying)
	if err != nil {
		return errors.New("migration: the plan could not be claimed")
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status string
	var expired bool
	if err := s.pool.QueryRow(ctx,
		`SELECT status, expires_at <= now() FROM migration_plans WHERE id = $1`, planID).Scan(&status, &expired); err != nil {
		return ErrNotFound
	}
	if expired {
		return ErrExpired
	}
	if status == statusApplying {
		return fmt.Errorf("%w: another apply is running for this plan", ErrConflict)
	}
	return errors.New("migration: the plan could not be claimed")
}

func (s *Service) mark(ctx context.Context, planID string, position int, status, message string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE migration_plan_items SET status = $3, message = $4, updated_at = now()
		 WHERE plan_id = $1 AND position = $2`, planID, position, status, message)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("migration: the plan progress could not be stored")
	}
	return nil
}

func (s *Service) setStatus(ctx context.Context, planID, status string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE migration_plans SET status = $2, updated_at = now() WHERE id = $1`, planID, status); err != nil {
		return errors.New("migration: the plan status could not be stored")
	}
	return nil
}

func (entry *secretEntry) sanitizer() []string {
	secrets := connectSecrets(entry.connections)
	for _, secret := range entry.providers.indexers {
		secrets = append(secrets, secret.APIKey)
	}
	for _, secret := range entry.providers.usenet {
		secrets = append(secrets, secret.Username, secret.Password)
	}
	if entry.providers.subtitle != nil {
		secrets = append(secrets, entry.providers.subtitle.Username, entry.providers.subtitle.Password)
	}
	return secrets
}

// resolver caches validated root and profile mappings for one apply run.
type resolver struct {
	service       *Service
	ctx           context.Context
	plan          *Plan
	input         ApplyInput
	profiles      map[string]string
	musicProfiles map[string]string
	roots         map[string]string
	news          newsSelection
	secrets       providerSecrets
}

// root resolves a source root to a local root; skip reports an unmapped choice.
func (r *resolver) root(media Media, sourcePath string) (id, reason string, skip bool) {
	key := fmt.Sprintf("%s|%s", media, sourcePath)
	if cached, ok := r.roots[key]; ok {
		return cached, "", false
	}
	mapping := r.input.MoviesRoots[sourcePath]
	switch media {
	case MediaTV:
		mapping = r.input.TVRoots[sourcePath]
	case MediaMusic:
		mapping = r.input.MusicRoots[sourcePath]
	}
	if mapping == "" {
		return "", fmt.Sprintf("map the %s root %s to a local folder before importing its items", mediaLabel(media), sourcePath), true
	}
	if err := localDirectory(mapping); err != nil {
		return "", err.Error(), true
	}
	id, err := r.service.ensureRoot(r.ctx, media, mapping)
	if err != nil {
		return "", err.Error(), false
	}
	r.roots[key] = id
	return id, "", false
}

// profile resolves a movie or TV quality profile; skip reports an unmapped choice.
func (r *resolver) profile(sourceKey string) (id, reason string, skip bool) {
	if sourceKey == "" {
		return "", "the source profile is unknown; map it or create profiles first", true
	}
	if cached, ok := r.profiles[sourceKey]; ok {
		return cached, "", false
	}
	if r.service.movies == nil {
		return "", ErrUnready.Error(), false
	}
	if mapped := strings.TrimSpace(r.input.Profiles[sourceKey]); mapped != "" {
		if _, err := r.service.movies.Store.Profile(r.ctx, mapped); err != nil {
			return "", fmt.Sprintf("the mapped Constellarr profile %q does not exist", mapped), false
		}
		r.profiles[sourceKey] = mapped
		return mapped, "", false
	}
	if !r.input.CreateProfiles {
		return "", "map this profile to a Constellarr profile or enable profile creation", true
	}
	source, ok := r.plan.profile(sourceKey)
	if !ok {
		return "", "the source profile is not part of this plan", false
	}
	if len(source.Qualities) == 0 {
		return "", fmt.Sprintf("profile %q has no qualities that Constellarr supports", source.Name), false
	}
	existing, err := r.service.movies.Store.Profiles(r.ctx)
	if err != nil {
		return "", "quality profiles could not be read", false
	}
	profileID := migratedProfileID(source.Key, source.Source)
	if already, ok := findVideoProfile(existing, profileID, source); ok {
		r.profiles[sourceKey] = already
		return already, "", false
	}
	created := profileFromPlan(source)
	created.ID = profileID
	created.Name = uniqueProfileName(source.Name, source.Key, videoProfileNames(existing))
	saved, err := r.service.movies.Store.SaveProfile(r.ctx, created)
	if err != nil {
		return "", err.Error(), false
	}
	r.profiles[sourceKey] = saved.ID
	return saved.ID, "", false
}

// findVideoProfile reuses a migrated profile or an exact equivalent, never a name lookalike.
func findVideoProfile(existing []quality.Profile, id string, source ProfilePlan) (string, bool) {
	for _, profile := range existing {
		if profile.ID == id {
			return profile.ID, true
		}
	}
	for _, profile := range existing {
		if videoProfileEquivalent(profile, source) {
			return profile.ID, true
		}
	}
	return "", false
}

// profileFromPlan keeps every mapped setting so a created profile never widens the source.
func profileFromPlan(source ProfilePlan) quality.Profile {
	return quality.Profile{
		Name: source.Name, Qualities: append([]string{}, source.Qualities...), Cutoff: source.Cutoff,
		Upgrade: source.Upgrade, MinMB: source.MinMB, MaxMB: source.MaxMB, Language: source.Language,
	}
}

func videoProfileNames(profiles []quality.Profile) map[string]bool {
	names := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		names[strings.ToLower(strings.TrimSpace(profile.Name))] = true
	}
	return names
}

// uniqueProfileName suffixes a taken name so the existing profile stays untouched.
func uniqueProfileName(name, key string, taken map[string]bool) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "Migrated profile"
	}
	candidate := base
	if taken[strings.ToLower(candidate)] {
		candidate = fmt.Sprintf("%s (%s)", base, key)
		for suffix := 2; taken[strings.ToLower(candidate)]; suffix++ {
			candidate = fmt.Sprintf("%s (%s %d)", base, key, suffix)
		}
	}
	return candidate
}

// musicProfile resolves a Lidarr profile into the music module's profile list.
func (r *resolver) musicProfile(sourceKey string) (id, reason string, skip bool) {
	if sourceKey == "" {
		return "", "the source profile is unknown; map it or create profiles first", true
	}
	if cached, ok := r.musicProfiles[sourceKey]; ok {
		return cached, "", false
	}
	if r.service.music == nil {
		return "", ErrUnready.Error(), false
	}
	cfg, err := r.service.music.ConfigView(r.ctx)
	if err != nil {
		return "", "the music configuration could not be read", false
	}
	if mapped := strings.TrimSpace(r.input.MusicProfiles[sourceKey]); mapped != "" {
		for _, profile := range cfg.QualityProfiles {
			if profile.ID == mapped {
				r.musicProfiles[sourceKey] = mapped
				return mapped, "", false
			}
		}
		return "", fmt.Sprintf("the mapped music profile %q does not exist", mapped), false
	}
	if !r.input.CreateProfiles {
		return "", "map this profile to a music profile or enable profile creation", true
	}
	source, ok := r.plan.musicProfile(sourceKey)
	if !ok {
		return "", "the source profile is not part of this plan", false
	}
	if len(source.Formats) == 0 {
		return "", fmt.Sprintf("profile %q has no formats that Constellarr supports", source.Name), false
	}
	newID := migratedProfileID(source.Key, source.Source)
	reused := ""
	for _, profile := range cfg.QualityProfiles {
		if profile.ID == newID || musicProfileEquivalent(profile, source) {
			reused = profile.ID
			break
		}
	}
	if reused != "" {
		r.musicProfiles[sourceKey] = reused
		return reused, "", false
	}
	names := make(map[string]bool, len(cfg.QualityProfiles))
	for _, profile := range cfg.QualityProfiles {
		names[strings.ToLower(strings.TrimSpace(profile.Name))] = true
	}
	created := musicProfileFromPlan(source)
	created.ID = newID
	created.Name = uniqueProfileName(source.Name, source.Key, names)
	cfg.QualityProfiles = append(cfg.QualityProfiles, created)
	if _, err := r.service.music.SetConfig(r.ctx, cfg); err != nil {
		return "", err.Error(), false
	}
	r.musicProfiles[sourceKey] = newID
	return newID, "", false
}

// musicProfileFromPlan keeps every mapped setting so a created profile never widens the source.
func musicProfileFromPlan(source MusicProfilePlan) music.QualityProfile {
	return music.QualityProfile{
		Name: source.Name, Formats: append([]string{}, source.Formats...), LosslessOnly: source.LosslessOnly,
		MinBitrateKbps: source.MinBitrateKbps, Cutoff: source.Cutoff, Upgrade: source.Upgrade,
	}
}

func migratedProfileID(key string, source App) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("mig-%s-%s", source, hex.EncodeToString(sum[:4]))
}

func (plan *Plan) profile(key string) (ProfilePlan, bool) {
	for _, profile := range plan.Profiles {
		if profile.Key == key {
			return profile, true
		}
	}
	return ProfilePlan{}, false
}

func (plan *Plan) musicProfile(key string) (MusicProfilePlan, bool) {
	if plan.Music == nil {
		return MusicProfilePlan{}, false
	}
	for _, profile := range plan.Music.Profiles {
		if profile.Key == key {
			return profile, true
		}
	}
	return MusicProfilePlan{}, false
}

func (plan *Plan) movie(source App, id int) (MoviePlan, bool) {
	for _, movie := range plan.Movies {
		if movie.Source == source && movie.ID == id {
			return movie, true
		}
	}
	return MoviePlan{}, false
}

func (plan *Plan) series(source App, id int) (SeriesPlan, bool) {
	for _, series := range plan.Series {
		if series.Source == source && series.ID == id {
			return series, true
		}
	}
	return SeriesPlan{}, false
}

func (plan *Plan) artist(id int) (MusicArtistPlan, bool) {
	if plan.Music == nil {
		return MusicArtistPlan{}, false
	}
	for _, artist := range plan.Music.Artists {
		if artist.ID == id {
			return artist, true
		}
	}
	return MusicArtistPlan{}, false
}

func (plan *Plan) episodeFile(key string) (EpisodeFilePlan, bool) {
	for _, series := range plan.Series {
		for _, file := range series.Files {
			if file.Key == key {
				return file, true
			}
		}
	}
	return EpisodeFilePlan{}, false
}

func (plan *Plan) torznab(key string) (TorznabPlan, bool) {
	for _, candidate := range plan.Torznab {
		if candidate.Key == key {
			return candidate, true
		}
	}
	return TorznabPlan{}, false
}

func (plan *Plan) transfer(hash string) (TorrentTransfer, bool) {
	if plan.Torrents == nil {
		return TorrentTransfer{}, false
	}
	for _, transfer := range plan.Torrents.Transfers {
		if strings.EqualFold(transfer.Hash, hash) {
			return transfer, true
		}
	}
	return TorrentTransfer{}, false
}

// ensureRoot attaches a configured root for a mapped local folder.
func (s *Service) ensureRoot(ctx context.Context, media Media, localPath string) (string, error) {
	path := filepath.Clean(strings.TrimSpace(localPath))
	switch media {
	case MediaTV:
		if s.tv == nil {
			return "", ErrUnready
		}
		cfg, err := s.tv.Config(ctx)
		if err != nil {
			return "", err
		}
		for _, root := range cfg.RootFolders {
			if root.Path == path {
				return root.ID, nil
			}
		}
		id := rootIDFor(path, cfg.RootFolders)
		cfg.RootFolders = append(cfg.RootFolders, movies.RootFolder{ID: id, Path: path})
		if _, err := s.tv.SetConfig(ctx, cfg); err != nil {
			return "", err
		}
		return id, nil
	case MediaMusic:
		if s.music == nil {
			return "", ErrUnready
		}
		cfg, err := s.music.ConfigView(ctx)
		if err != nil {
			return "", err
		}
		for _, root := range cfg.RootFolders {
			if root.Path == path {
				return root.ID, nil
			}
		}
		id := musicRootIDFor(path, cfg.RootFolders)
		cfg.RootFolders = append(cfg.RootFolders, music.RootFolder{ID: id, Path: path})
		if _, err := s.music.SetConfig(ctx, cfg); err != nil {
			return "", err
		}
		return id, nil
	}
	if s.movies == nil {
		return "", ErrUnready
	}
	cfg, err := s.movies.Store.Config(ctx)
	if err != nil {
		return "", errors.New("the movie configuration could not be read")
	}
	for _, root := range cfg.RootFolders {
		if root.Path == path {
			return root.ID, nil
		}
	}
	id := rootIDFor(path, cfg.RootFolders)
	cfg.RootFolders = append(cfg.RootFolders, movies.RootFolder{ID: id, Path: path})
	if _, err := s.movies.SetConfig(ctx, cfg); err != nil {
		return "", err
	}
	return id, nil
}

func rootIDFor(path string, existing []movies.RootFolder) string {
	sum := sha256.Sum256([]byte(path))
	base := "mig-" + hex.EncodeToString(sum[:5])
	taken := map[string]bool{}
	for _, root := range existing {
		taken[root.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func musicRootIDFor(path string, existing []music.RootFolder) string {
	sum := sha256.Sum256([]byte(path))
	base := "mig-" + hex.EncodeToString(sum[:5])
	taken := map[string]bool{}
	for _, root := range existing {
		taken[root.ID] = true
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func (s *Service) applyItem(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver, entry *secretEntry) (string, string) {
	switch item.Kind {
	case itemRoot:
		return s.applyRoot(resolver, item)
	case itemProfile:
		if _, reason, skip := resolver.profile(item.Target); reason != "" {
			return itemStatus(skip), reason
		}
		return itemDone, "profile mapping ready"
	case itemMusicProfile:
		if _, reason, skip := resolver.musicProfile(item.Target); reason != "" {
			return itemStatus(skip), reason
		}
		return itemDone, "music profile ready"
	case itemNaming:
		return s.applyNaming(ctx, plan, item, resolver.input)
	case itemMovie:
		return s.applyMovie(ctx, plan, item, resolver)
	case itemSeries:
		return s.applySeries(ctx, plan, item, resolver)
	case itemSeason:
		return s.applySeason(ctx, plan, item, resolver)
	case itemMovieFile:
		return s.applyMovieFile(ctx, plan, item, resolver)
	case itemEpisodeFile:
		return s.applyEpisodeFile(ctx, plan, item, resolver)
	case itemArtist:
		return s.applyArtist(ctx, plan, item, resolver)
	case itemAlbum:
		return s.applyAlbum(ctx, plan, item, resolver)
	case itemIndexer:
		return s.applyIndexer(ctx, item, resolver.input, entry)
	case itemUsenet:
		return s.applyUsenet(ctx, item, resolver)
	case itemTorznab:
		return s.applyTorznab(ctx, plan, item, resolver.input, entry)
	case itemJellyfin:
		return s.applyJellyfin(ctx, item, entry)
	case itemSubtitleConfig:
		return s.applySubtitleConfig(ctx, plan, resolver.input)
	case itemSubtitleProfile:
		return s.applySubtitleProfile(ctx, plan, item, resolver.input)
	case itemSubtitleProvider:
		return s.applySubtitleProvider(ctx, plan, item, resolver.input, entry)
	case itemTorrentSettings:
		return s.applyTorrentSettings(ctx, plan, resolver.input)
	case itemTorrentJob:
		return s.applyTorrentJob(ctx, plan, item, resolver.input)
	}
	return itemFailed, "this migration item is not supported"
}

// itemStatus keeps an unmet mapping choice a skip and a real error a failure.
func itemStatus(skip bool) string {
	if skip {
		return itemSkipped
	}
	return itemFailed
}

func (s *Service) applyRoot(resolver *resolver, item PlanItem) (string, string) {
	mediaName, sourcePath, ok := strings.Cut(item.Target, "|")
	media := Media(mediaName)
	if !ok || (media != MediaMovies && media != MediaTV && media != MediaMusic) || sourcePath == "" {
		return itemFailed, "the root mapping is invalid"
	}
	if _, reason, skip := resolver.root(media, sourcePath); reason != "" {
		return itemStatus(skip), reason
	}
	return itemDone, "root folder ready"
}

func (s *Service) applyNaming(ctx context.Context, plan *Plan, item PlanItem, input ApplyInput) (string, string) {
	if !input.Naming {
		return itemSkipped, "naming import was not requested"
	}
	media := Media(item.Target)
	var naming *NamingPlan
	for i := range plan.Naming {
		if plan.Naming[i].Media == media {
			naming = &plan.Naming[i]
			break
		}
	}
	if naming == nil && media == MediaMusic && plan.Music != nil {
		for i := range plan.Music.Naming {
			if plan.Music.Naming[i].Media == media {
				naming = &plan.Music.Naming[i]
				break
			}
		}
	}
	if naming == nil {
		return itemFailed, "the naming plan is missing"
	}
	if !naming.Applicable {
		return itemFailed, fmt.Sprintf("naming uses tokens Constellarr cannot render: %s", strings.Join(naming.Unsupported, ", "))
	}
	switch media {
	case MediaTV:
		if s.tv == nil {
			return itemFailed, ErrUnready.Error()
		}
		cfg, err := s.tv.Config(ctx)
		if err != nil {
			return itemFailed, err.Error()
		}
		cfg.FolderTemplate, cfg.FileTemplate = naming.Folder, naming.File
		if _, err := s.tv.SetConfig(ctx, cfg); err != nil {
			return itemFailed, err.Error()
		}
		return itemDone, "TV naming updated"
	case MediaMusic:
		if s.music == nil {
			return itemFailed, ErrUnready.Error()
		}
		cfg, err := s.music.ConfigView(ctx)
		if err != nil {
			return itemFailed, err.Error()
		}
		cfg.FolderTemplate, cfg.FileTemplate = naming.Folder, naming.File
		if _, err := s.music.SetConfig(ctx, cfg); err != nil {
			return itemFailed, err.Error()
		}
		return itemDone, "music naming updated"
	}
	if s.movies == nil {
		return itemFailed, ErrUnready.Error()
	}
	cfg, err := s.movies.Store.Config(ctx)
	if err != nil {
		return itemFailed, "the movie configuration could not be read"
	}
	cfg.FolderTemplate, cfg.FileTemplate = naming.Folder, naming.File
	if _, err := s.movies.SetConfig(ctx, cfg); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "movie naming updated"
}

func (s *Service) applyMovie(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Movies {
		return itemSkipped, "movie import was not requested"
	}
	if s.movies == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 2)
	if len(parts) != 2 {
		return itemFailed, "the movie item is invalid"
	}
	id, err := parseID(parts[1])
	if err != nil {
		return itemFailed, "the movie item is invalid"
	}
	movie, ok := plan.movie(App(parts[0]), id)
	if !ok {
		return itemFailed, "the movie is not part of this plan"
	}
	rootID, reason, skip := resolver.root(MediaMovies, movie.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	profileID, reason, skip := resolver.profile(movie.ProfileKey)
	if reason != "" {
		return itemStatus(skip), reason
	}
	saved, err := s.movies.Add(ctx, movies.AddInput{
		IMDbID: movie.IMDbID, Monitored: movie.Monitored, ProfileID: profileID, RootID: rootID, Tags: movie.Tags,
		Metadata: metadata.Title{IMDbID: movie.IMDbID, Title: movie.Title, Year: movie.Year, Type: "movie", Poster: movie.Poster, Plot: movie.Plot},
	})
	if err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("imported as %s", movieLabel(saved.Metadata.Title, saved.Metadata.Year))
}

func (s *Service) applySeries(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.TV {
		return itemSkipped, "TV import was not requested"
	}
	if s.tv == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 2)
	if len(parts) != 2 {
		return itemFailed, "the series item is invalid"
	}
	id, err := parseID(parts[1])
	if err != nil {
		return itemFailed, "the series item is invalid"
	}
	series, ok := plan.series(App(parts[0]), id)
	if !ok {
		return itemFailed, "the series is not part of this plan"
	}
	rootID, reason, skip := resolver.root(MediaTV, series.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	profileID, reason, skip := resolver.profile(series.ProfileKey)
	if reason != "" {
		return itemStatus(skip), reason
	}
	saved, err := s.tv.Add(ctx, tvAddInput(series, profileID, rootID))
	if err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("imported as %s", movieLabel(saved.Metadata.Title, saved.Metadata.Year))
}

func (s *Service) applySeason(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.TV {
		return itemSkipped, "TV import was not requested"
	}
	if s.tv == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 3)
	if len(parts) != 3 {
		return itemFailed, "the season item is invalid"
	}
	id, err := parseID(parts[1])
	season, seasonErr := parseID(parts[2])
	if err != nil || seasonErr != nil {
		return itemFailed, "the season item is invalid"
	}
	series, ok := plan.series(App(parts[0]), id)
	if !ok {
		return itemFailed, "the series is not part of this plan"
	}
	stored, err := s.tv.Store.FindIMDb(ctx, series.IMDbID)
	if err != nil {
		return itemSkipped, "the series is not imported yet"
	}
	if _, err := s.tv.Monitor(ctx, stored.ID, tvMonitorInput(season, false)); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("season %d unmonitored", season)
}

func (s *Service) applyMovieFile(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Files {
		return itemSkipped, "existing file import was not requested"
	}
	if s.movies == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 2)
	if len(parts) != 2 {
		return itemFailed, "the file item is invalid"
	}
	id, err := parseID(parts[1])
	if err != nil {
		return itemFailed, "the file item is invalid"
	}
	movie, ok := plan.movie(App(parts[0]), id)
	if !ok || movie.FilePath == "" {
		return itemFailed, "the movie file is not part of this plan"
	}
	relative := cleanRelativePath(movie.FilePath)
	if relative == "" {
		return itemFailed, "the movie file path is not usable"
	}
	rootID, reason, skip := resolver.root(MediaMovies, movie.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	mapping := resolver.input.MoviesRoots[movie.RootPath]
	if err := localFileExists(mapping, relative); err != nil {
		return itemSkipped, err.Error()
	}
	stored, err := s.movies.Store.FindIMDb(ctx, movie.IMDbID)
	if err != nil {
		return itemSkipped, "the movie is not imported yet"
	}
	if hasLocalFile(stored.Files, relative) {
		return itemDone, "the file is already associated"
	}
	if _, err := s.movies.Import(ctx, movies.ImportInput{RootID: rootID, Path: relative, MovieID: stored.ID}); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "existing file imported"
}

func (s *Service) applyEpisodeFile(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Files {
		return itemSkipped, "existing file import was not requested"
	}
	if s.tv == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 3)
	if len(parts) != 3 {
		return itemFailed, "the file item is invalid"
	}
	fileKey := parts[1] + ":" + parts[2]
	file, ok := plan.episodeFile(fileKey)
	if !ok || len(file.Numbers) == 0 {
		return itemFailed, "the episode file is not part of this plan"
	}
	seriesID, err := parseID(parts[1])
	if err != nil {
		return itemFailed, "the file item is invalid"
	}
	series, ok := plan.series(App(parts[0]), seriesID)
	if !ok {
		return itemFailed, "the series is not part of this plan"
	}
	relative := cleanRelativePath(file.RelativePath)
	if relative == "" {
		return itemFailed, "the episode file path is not usable"
	}
	rootID, reason, skip := resolver.root(MediaTV, series.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	mapping := resolver.input.TVRoots[series.RootPath]
	if err := localFileExists(mapping, relative); err != nil {
		return itemSkipped, err.Error()
	}
	stored, err := s.tv.Store.FindIMDb(ctx, series.IMDbID)
	if err != nil {
		return itemSkipped, "the series is not imported yet"
	}
	episodes, err := s.tv.Store.Episodes(ctx, stored.ID)
	if err != nil {
		return itemFailed, "the episode list could not be read"
	}
	if episodesHaveFile(episodes, relative) {
		return itemDone, "the file is already associated"
	}
	if _, err := s.tv.Import(ctx, tvImportInput(rootID, relative, stored.ID, file)); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "existing file imported"
}

func (s *Service) applyArtist(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Music {
		return itemSkipped, "music import was not requested"
	}
	if s.music == nil {
		return itemFailed, ErrUnready.Error()
	}
	id, err := parseID(item.Target)
	if err != nil {
		return itemFailed, "the artist item is invalid"
	}
	artist, ok := plan.artist(id)
	if !ok {
		return itemFailed, "the artist is not part of this plan"
	}
	rootID, reason, skip := resolver.root(MediaMusic, artist.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	profileID, reason, skip := resolver.musicProfile(artist.ProfileKey)
	if reason != "" {
		return itemStatus(skip), reason
	}
	// Lidarr already supplies the MusicBrainz identity; importing it needs no metadata lookup.
	if existing, err := s.music.Store.ArtistByMusicBrainz(ctx, artist.MusicBrainzID); err == nil {
		existing.Monitored, existing.MonitorOption = artist.Monitored, artist.MonitorOption
		saved, err := s.music.Store.SaveArtist(ctx, existing)
		if err != nil {
			return itemFailed, err.Error()
		}
		return itemDone, fmt.Sprintf("already in the library as %s", saved.Name)
	} else if !errors.Is(err, music.ErrNotFound) {
		return itemFailed, "the music catalog could not be read"
	}
	saved, err := s.music.Store.SaveArtist(ctx, music.Artist{
		ID: migratedArtistID(artist.MusicBrainzID), MusicBrainzID: artist.MusicBrainzID, Name: artist.Name,
		SortName: artist.SortName, Type: artist.Type, Monitored: artist.Monitored,
		MonitorOption: artist.MonitorOption, ProfileID: profileID, RootID: rootID,
	})
	if err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("imported as %s", saved.Name)
}

// migratedArtistID derives a stable catalog id so a repeated migration reuses the same artist.
func migratedArtistID(mbid string) string {
	sum := sha256.Sum256([]byte("lidarr|" + strings.ToLower(mbid)))
	return "lidarr-" + hex.EncodeToString(sum[:6])
}

func (s *Service) applyAlbum(ctx context.Context, plan *Plan, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Music {
		return itemSkipped, "music import was not requested"
	}
	if s.music == nil {
		return itemFailed, ErrUnready.Error()
	}
	parts := splitTarget(item.Target, 2)
	if len(parts) != 2 {
		return itemFailed, "the album item is invalid"
	}
	artistID, err := parseID(parts[0])
	albumID, albumErr := parseID(parts[1])
	if err != nil || albumErr != nil {
		return itemFailed, "the album item is invalid"
	}
	artist, ok := plan.artist(artistID)
	if !ok {
		return itemFailed, "the artist is not part of this plan"
	}
	var album MusicAlbumPlan
	found := false
	for _, candidate := range artist.Albums {
		if candidate.ID == albumID {
			album, found = candidate, true
			break
		}
	}
	if !found || album.Unsupported != "" {
		return itemFailed, "the album is not part of this plan"
	}
	rootID, reason, skip := resolver.root(MediaMusic, artist.RootPath)
	if reason != "" {
		return itemStatus(skip), reason
	}
	profileID, reason, skip := resolver.musicProfile(artist.ProfileKey)
	if reason != "" {
		return itemStatus(skip), reason
	}
	storedArtist, err := s.music.Store.ArtistByMusicBrainz(ctx, artist.MusicBrainzID)
	if err != nil {
		return itemSkipped, "the artist is not imported yet"
	}
	if existing, err := s.music.Store.AlbumByMusicBrainz(ctx, album.MusicBrainzID); err == nil {
		if _, err := s.music.MonitorAlbum(ctx, existing.ID, music.AlbumMonitorInput{Monitored: album.Monitored}); err != nil {
			return itemFailed, err.Error()
		}
		return itemDone, fmt.Sprintf("already in the library as %s", existing.Title)
	}
	saved, err := s.music.AddAlbum(ctx, music.AddAlbumInput{
		ArtistID: storedArtist.ID, MusicBrainzID: album.MusicBrainzID, Title: album.Title, Year: album.Year,
		Monitored: album.Monitored, ProfileID: profileID, RootID: rootID,
	})
	if err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("imported as %s", saved.Title)
}

func (s *Service) applyIndexer(ctx context.Context, item PlanItem, input ApplyInput, entry *secretEntry) (string, string) {
	if !input.Providers {
		return itemSkipped, "provider import was not requested"
	}
	if s.downloads == nil {
		return itemFailed, ErrUnready.Error()
	}
	selected, reason := selectIndexerSelection(input, item.Target)
	if reason != "" {
		return itemSkipped, reason
	}
	secret, ok := entry.providers.indexer(item.Target)
	if !ok {
		return itemFailed, errMissingCredentials.Error()
	}
	current := s.downloads.Config()
	update := downloads.SettingsUpdate{
		IndexerURL: secret.URL, APIKey: secret.APIKey,
		UsenetHost: current.Usenet.Host, UsenetPort: current.Usenet.Port,
		UsenetUsername: current.Usenet.Username, UsenetPassword: current.Usenet.Password,
		Connections: current.Usenet.Connections, FallbackHosts: current.Usenet.FallbackHosts,
	}
	if err := s.downloads.UpdateSettings(ctx, update); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, fmt.Sprintf("NZBGeek indexer settings updated from %s", selected.Name)
}

// selectIndexerSelection reports whether this candidate is the explicitly selected indexer.
func selectIndexerSelection(input ApplyInput, key string) (IndexerPlan, string) {
	if input.Indexer == "" {
		return IndexerPlan{Key: key}, ""
	}
	if input.Indexer != key {
		return IndexerPlan{}, "another indexer was selected in the wizard"
	}
	return IndexerPlan{Key: key}, ""
}

func (s *Service) applyUsenet(ctx context.Context, item PlanItem, resolver *resolver) (string, string) {
	if !resolver.input.Providers {
		return itemSkipped, "provider import was not requested"
	}
	if s.downloads == nil {
		return itemFailed, ErrUnready.Error()
	}
	selection := resolver.news
	if selection.primaryKey == "" {
		reason := selection.reason
		if reason == "" {
			reason = "no news server was selected"
		}
		return itemSkipped, reason
	}
	if item.Target != selection.primaryKey {
		if host, ok := selection.fallbackHost[item.Target]; ok {
			return itemDone, fmt.Sprintf("host %s added as a fallback for the primary account", host)
		}
		if reason, ok := selection.skipped[item.Target]; ok {
			return itemSkipped, reason
		}
		return itemSkipped, "another news server was selected in the wizard"
	}
	secret, ok := resolver.secrets.newsServer(item.Target)
	if !ok {
		return itemFailed, errMissingCredentials.Error()
	}
	current := s.downloads.Config()
	fallbacks := append([]string{}, selection.fallbackHosts...)
	update := downloads.SettingsUpdate{
		IndexerURL: current.IndexerURL, APIKey: current.APIKey,
		UsenetHost: secret.Host, UsenetPort: secret.Port,
		UsenetUsername: secret.Username, UsenetPassword: secret.Password,
		Connections: secret.Connections, FallbackHosts: fallbacks,
	}
	if err := s.downloads.UpdateSettings(ctx, update); err != nil {
		return itemFailed, err.Error()
	}
	message := fmt.Sprintf("%s news server settings updated", selection.primarySource)
	if len(fallbacks) > 0 {
		message += fmt.Sprintf(" with %d fallback host(s)", len(fallbacks))
	}
	return itemDone, message
}

func (s *Service) applyTorznab(ctx context.Context, plan *Plan, item PlanItem, input ApplyInput, entry *secretEntry) (string, string) {
	if !input.Torrents {
		return itemSkipped, "torrent import was not requested"
	}
	if s.torrents == nil {
		return itemFailed, ErrUnready.Error()
	}
	if !containsString(input.Torznab, item.Target) {
		return itemSkipped, "this indexer was not selected for the torrent module"
	}
	candidate, ok := plan.torznab(item.Target)
	if !ok {
		return itemFailed, "the torrent indexer is not part of this plan"
	}
	connection, ok := connectionFor(entry.connections, AppProwlarr)
	if !ok || connection.APIKey == "" {
		return itemFailed, errMissingCredentials.Error()
	}
	name := "Prowlarr: " + candidate.Name
	sources, err := s.torrents.ListSources(ctx)
	if err != nil {
		return itemFailed, "the torrent sources could not be read"
	}
	for _, source := range sources {
		if !strings.EqualFold(source.Name, name) {
			continue
		}
		if source.URL == candidate.URL {
			return itemDone, "the Torznab source is already configured"
		}
		if _, err := s.torrents.UpdateSource(ctx, source.ID, torrents.SourceInput{
			Name: name, URL: candidate.URL, APIKey: connection.APIKey, Categories: source.Categories, Enabled: true,
		}); err != nil {
			return itemFailed, err.Error()
		}
		return itemDone, "the Torznab source URL was updated"
	}
	if _, err := s.torrents.CreateSource(ctx, torrents.SourceInput{
		Name: name, URL: candidate.URL, APIKey: connection.APIKey, Enabled: true,
	}); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "the Torznab source was added to the torrent module"
}

func (s *Service) applyJellyfin(ctx context.Context, item PlanItem, entry *secretEntry) (string, string) {
	if s.movies == nil {
		return itemFailed, ErrUnready.Error()
	}
	connection, ok := connectionFor(entry.connections, AppJellyfin)
	if !ok {
		return itemSkipped, "no Jellyfin connection was provided"
	}
	cfg, err := s.movies.Store.Config(ctx)
	if err != nil {
		return itemFailed, "the movie configuration could not be read"
	}
	cfg.JellyfinURL, cfg.JellyfinAPIKey = connection.URL, connection.APIKey
	if _, err := s.movies.SetConfig(ctx, cfg); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "Jellyfin connection saved"
}

// applySubtitleConfig writes scoring, search cadence, and alignment limits into the subtitle service.
func (s *Service) applySubtitleConfig(ctx context.Context, plan *Plan, input ApplyInput) (string, string) {
	if !input.Subtitles {
		return itemSkipped, "subtitle import was not requested"
	}
	if s.subtitles == nil {
		return itemFailed, ErrUnready.Error()
	}
	if plan.Subtitles == nil {
		return itemFailed, "the subtitle plan is missing"
	}
	cfg, err := s.subtitles.Config(ctx)
	if err != nil {
		return itemFailed, err.Error()
	}
	applied := []string{}
	if plan.Subtitles.MinimumScore > 0 {
		cfg.CutoffScore = plan.Subtitles.MinimumScore
		applied = append(applied, fmt.Sprintf("score cutoff %d", cfg.CutoffScore))
	}
	if plan.Subtitles.SearchHours > 0 {
		cfg.SearchIntervalHours = plan.Subtitles.SearchHours
		applied = append(applied, fmt.Sprintf("search interval %dh", cfg.SearchIntervalHours))
	}
	if plan.Subtitles.Sync.MaxOffsetSeconds > 0 {
		cfg.Sync.MaxOffsetSeconds = float64(plan.Subtitles.Sync.MaxOffsetSeconds)
		applied = append(applied, fmt.Sprintf("sync offset limit %ds", plan.Subtitles.Sync.MaxOffsetSeconds))
	}
	if len(applied) == 0 {
		return itemSkipped, "no subtitle settings could be mapped from Bazarr"
	}
	if _, err := s.subtitles.SetConfig(ctx, cfg); err != nil {
		return itemFailed, err.Error()
	}
	return itemDone, "subtitle settings updated: " + strings.Join(applied, ", ")
}

// applySubtitleProfile creates the language profile and keeps Bazarr's default assignment.
func (s *Service) applySubtitleProfile(ctx context.Context, plan *Plan, item PlanItem, input ApplyInput) (string, string) {
	if !input.Subtitles {
		return itemSkipped, "subtitle import was not requested"
	}
	if s.subtitles == nil {
		return itemFailed, ErrUnready.Error()
	}
	var source *SubtitleProfilePlan
	for i := range plan.Subtitles.Profiles {
		if plan.Subtitles.Profiles[i].Key == item.Target {
			source = &plan.Subtitles.Profiles[i]
			break
		}
	}
	if source == nil {
		return itemFailed, "the subtitle profile is not part of this plan"
	}
	if len(source.Languages) == 0 {
		return itemSkipped, "no supported languages in this profile"
	}
	profile := subtitles.Profile{
		ID:     subtitleProfileID(source.Key),
		Name:   subtitleProfileName(source),
		Cutoff: source.Cutoff,
	}
	for _, language := range source.Languages {
		profile.Languages = append(profile.Languages, subtitles.LanguagePreference{
			Code: language.Code, Forced: language.Forced, HI: language.HI,
		})
	}
	saved, err := s.subtitles.SaveProfile(ctx, profile)
	if err != nil {
		return itemFailed, err.Error()
	}
	if isBazarrDefault(plan, source.Key) {
		cfg, err := s.subtitles.Config(ctx)
		if err != nil {
			return itemFailed, err.Error()
		}
		if cfg.DefaultProfileID != saved.ID {
			cfg.DefaultProfileID = saved.ID
			if _, err := s.subtitles.SetConfig(ctx, cfg); err != nil {
				return itemFailed, err.Error()
			}
		}
	}
	return itemDone, fmt.Sprintf("%s ready with %d language(s)", saved.Name, len(saved.Languages))
}

// applySubtitleProvider updates the OpenSubtitles account Bazarr used.
func (s *Service) applySubtitleProvider(ctx context.Context, plan *Plan, item PlanItem, input ApplyInput, entry *secretEntry) (string, string) {
	if !input.Subtitles {
		return itemSkipped, "subtitle import was not requested"
	}
	if s.subtitles == nil {
		return itemFailed, ErrUnready.Error()
	}
	var source *SubtitleProviderPlan
	for i := range plan.Subtitles.ProviderPlans {
		if plan.Subtitles.ProviderPlans[i].Key == item.Target {
			source = &plan.Subtitles.ProviderPlans[i]
			break
		}
	}
	if source == nil {
		return itemFailed, "the subtitle provider is not part of this plan"
	}
	if source.Type == "" {
		return itemSkipped, "Constellarr has no adapter for this provider; keep using it in Bazarr"
	}
	cfg, err := s.subtitles.Config(ctx)
	if err != nil {
		return itemFailed, err.Error()
	}
	secret := entry.providers.subtitle
	if secret == nil && (source.UsernameSet || source.PasswordSet) {
		return itemFailed, errMissingCredentials.Error()
	}
	index := -1
	for i := range cfg.Providers {
		if cfg.Providers[i].Type == source.Type {
			index = i
			break
		}
	}
	provider := subtitles.Provider{Name: source.Name, Type: source.Type, Endpoint: source.Endpoint, Enabled: true}
	if index >= 0 {
		provider = cfg.Providers[index]
	} else {
		provider.ID = "bazarr-" + source.Type
	}
	if secret != nil {
		provider.Username, provider.Password = secret.Username, secret.Password
	}
	missingKey := ""
	if provider.APIKey == "" {
		missingKey = "add the provider API key in Subtitles settings"
	}
	if index >= 0 {
		cfg.Providers[index] = provider
	} else {
		cfg.Providers = append(cfg.Providers, provider)
	}
	if _, err := s.subtitles.SetConfig(ctx, cfg); err != nil {
		return itemFailed, err.Error()
	}
	if missingKey != "" {
		return itemDone, "OpenSubtitles account imported; " + missingKey
	}
	return itemDone, "OpenSubtitles account imported"
}

func subtitleProfileID(key string) string {
	sum := sha256.Sum256([]byte("bazarr|" + key))
	return "bazarr-" + hex.EncodeToString(sum[:6])
}

func subtitleProfileName(source *SubtitleProfilePlan) string {
	name := strings.TrimSpace(source.Name)
	if name == "" {
		name = "Bazarr languages"
	}
	if len([]rune(name)) > 48 {
		name = string([]rune(name)[:48])
	}
	return name
}

func isBazarrDefault(plan *Plan, key string) bool {
	if plan.Subtitles == nil {
		return false
	}
	return key != "" && (key == plan.Subtitles.DefaultProfile || key == plan.Subtitles.MovieProfile)
}

// applyTorrentSettings copies Transmission's bandwidth and seeding policy into the torrent module.
func (s *Service) applyTorrentSettings(ctx context.Context, plan *Plan, input ApplyInput) (string, string) {
	if !input.Torrents {
		return itemSkipped, "torrent import was not requested"
	}
	if s.torrents == nil {
		return itemFailed, ErrUnready.Error()
	}
	if plan.Torrents == nil {
		return itemFailed, "the torrent plan is missing"
	}
	source := plan.Torrents
	current := s.torrents.Settings()
	update := torrents.SettingsUpdate{
		ListenPort: current.ListenPort, DHTEnabled: source.DHTEnabled, PEXEnabled: source.PEXEnabled,
		MaxActiveJobs: current.MaxActiveJobs, DownloadLimitKBps: current.DownloadLimitKBps,
		UploadLimitKBps: current.UploadLimitKBps, SeedRatioLimit: current.SeedRatioLimit,
		SeedTimeLimitMinutes: current.SeedTimeLimitMinutes,
	}
	if source.SpeedDownEnabled {
		update.DownloadLimitKBps = int(source.SpeedLimitDown)
	} else {
		update.DownloadLimitKBps = 0
	}
	if source.SpeedUpEnabled {
		update.UploadLimitKBps = int(source.SpeedLimitUp)
	} else {
		update.UploadLimitKBps = 0
	}
	notes := []string{}
	if source.DownloadQueueSize > 0 {
		update.MaxActiveJobs = min(max(source.DownloadQueueSize, 1), 20)
	} else {
		notes = append(notes, "Transmission runs an unlimited download queue; Constellarr keeps its active torrent limit")
	}
	if source.SeedRatioLimited {
		update.SeedRatioLimit = source.SeedRatioLimit
	} else {
		notes = append(notes, "Transmission seeds without a ratio limit; Constellarr keeps its current seed ratio")
	}
	if source.IdleSeedingLimited {
		update.SeedTimeLimitMinutes = source.IdleSeedingMinutes
	} else {
		notes = append(notes, "Transmission has no idle seeding limit; Constellarr keeps its current seed time")
	}
	saved, err := s.torrents.UpdateSettings(ctx, update)
	if err != nil {
		return itemFailed, err.Error()
	}
	message := fmt.Sprintf("torrent settings updated (down %d KB/s, up %d KB/s, %d active)", saved.DownloadLimitKBps, saved.UploadLimitKBps, saved.MaxActiveJobs)
	if len(notes) > 0 {
		message += "; " + strings.Join(notes, "; ")
	}
	return itemDone, message
}

// applyTorrentJob imports a torrent paused, reusing verified local data or asking for a redownload.
func (s *Service) applyTorrentJob(ctx context.Context, plan *Plan, item PlanItem, input ApplyInput) (string, string) {
	if !input.Torrents || !input.TorrentJobs {
		return itemSkipped, "torrent import was not requested"
	}
	if s.torrents == nil {
		return itemFailed, ErrUnready.Error()
	}
	transfer, ok := plan.transfer(item.Target)
	if !ok {
		return itemFailed, "the torrent is not part of this plan"
	}
	if !validInfoHash(transfer.Hash) {
		return itemFailed, "the torrent info hash is not usable"
	}
	magnet := strings.TrimSpace(transfer.MagnetLink)
	if magnet == "" {
		magnet = magnetFor(transfer.Hash, transfer.Name)
	}
	redownload := containsString(input.Redownload, transfer.Hash)
	jobs, err := s.torrents.List(ctx)
	if err != nil {
		return itemFailed, "the torrent list could not be read"
	}
	for _, existing := range jobs {
		if !strings.EqualFold(existing.InfoHash, transfer.Hash) {
			continue
		}
		if _, err := s.torrents.Pause(ctx, existing.ID); err != nil && !errors.Is(err, torrents.ErrConflict) {
			return itemFailed, err.Error()
		}
		return itemDone, fmt.Sprintf("already imported as %s (left paused)", existing.Status)
	}
	message := "imported paused; it will download again when resumed"
	if !redownload {
		reuse, err := s.placeTransmissionData(ctx, transfer, input)
		switch {
		case err == nil && reuse.placed > 0:
			message = fmt.Sprintf("imported paused with %d existing file(s) placed for verification", reuse.placed)
		case err == nil && reuse.skipped > 0:
			message = fmt.Sprintf("imported paused with %d of %d existing file(s) placed; the rest must be downloaded", reuse.placed, reuse.placed+reuse.skipped)
		default:
			reason := "the existing data could not be reused"
			if err != nil {
				reason = err.Error()
			}
			if transfer.BytesDone > 0 || transfer.PercentDone > 0 {
				return itemSkipped, reason + "; add this hash to the redownload list to start over explicitly"
			}
			message = "imported paused; no existing data was found locally"
		}
	}
	job, err := s.torrents.Add(ctx, torrents.AddInput{
		Magnet: magnet, Source: "magnet", ReleaseID: "transmission:" + strings.ToLower(transfer.Hash), Title: transfer.Name,
	})
	if err != nil {
		return itemFailed, err.Error()
	}
	if _, err := s.torrents.Pause(ctx, job.ID); err != nil && !errors.Is(err, torrents.ErrConflict) {
		return itemFailed, "the torrent was added but could not be paused: " + err.Error()
	}
	return itemDone, message
}

type reuseResult struct {
	placed  int
	skipped int
}

// placeTransmissionData links verified files into the torrent job directory so a recheck keeps them.
func (s *Service) placeTransmissionData(ctx context.Context, transfer TorrentTransfer, input ApplyInput) (reuseResult, error) {
	if len(transfer.Files) == 0 {
		return reuseResult{}, errors.New("Transmission reported no file list for this torrent")
	}
	localDir := strings.TrimSpace(input.TorrentRoots[transfer.DownloadDir])
	if localDir == "" {
		return reuseResult{}, errors.New("map the Transmission download folder to reuse existing data")
	}
	if err := localDirectory(localDir); err != nil {
		return reuseResult{}, err
	}
	settings := s.torrents.Settings()
	if strings.TrimSpace(settings.Directory) == "" {
		return reuseResult{}, errors.New("the torrent download directory is not configured")
	}
	base := filepath.Join(settings.Directory, "torrents", strings.ToLower(transfer.Hash))
	result := reuseResult{}
	var totalSize int64
	for _, file := range transfer.Files {
		relative := cleanRelativePath(file.Name)
		if relative == "" {
			return reuseResult{}, errors.New("the torrent contains a file path that cannot be placed safely")
		}
		source := filepath.Join(localDir, filepath.FromSlash(relative))
		info, err := os.Stat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			result.skipped++
			continue
		}
		if file.Done > 0 && file.Done < file.Size {
			result.skipped++
			continue
		}
		target := filepath.Join(base, filepath.FromSlash(relative))
		if existing, err := os.Stat(target); err == nil && existing.Size() == file.Size {
			result.placed++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return reuseResult{}, errors.New("the torrent data directory could not be created")
		}
		if err := os.Link(source, target); err != nil {
			totalSize += file.Size
			if totalSize > maxReuseCopyBytes {
				return reuseResult{}, errors.New("the existing data is on another filesystem; move it next to the torrent directory or choose to download again")
			}
			if err := copyFile(source, target); err != nil {
				return reuseResult{}, errors.New("the existing data could not be reused: " + err.Error())
			}
		}
		result.placed++
	}
	if result.placed == 0 {
		return reuseResult{}, errors.New("no verified existing file was found for this torrent")
	}
	return result, nil
}

const maxReuseCopyBytes = 512 << 20

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// magnetFor rebuilds a magnet link when Transmission reports only the info hash.
func magnetFor(hash, name string) string {
	magnet := "magnet:?xt=urn:btih:" + strings.ToLower(hash)
	if name = strings.TrimSpace(name); name != "" {
		magnet += "&dn=" + url.QueryEscape(name)
	}
	return magnet
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func parseID(value string) (int, error) {
	if value == "" || len(value) > 12 {
		return 0, errors.New("invalid id")
	}
	id := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid id")
		}
		id = id*10 + int(r-'0')
	}
	if id == 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func localFileExists(mappedRoot, relative string) error {
	if mappedRoot == "" {
		return errors.New("the file is not available because its source root is not mapped")
	}
	info, err := os.Stat(filepath.Join(mappedRoot, filepath.FromSlash(relative)))
	if err != nil {
		return errors.New("the file is not present at the mapped local path; nothing was changed")
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("the mapped local file is not usable; nothing was changed")
	}
	return nil
}

func hasLocalFile(files []movies.File, relative string) bool {
	target := filepath.Base(filepath.ToSlash(relative))
	for _, file := range files {
		if file.Missing {
			continue
		}
		if filepath.Base(filepath.ToSlash(file.Path)) == target {
			return true
		}
	}
	return false
}

func validInfoHash(hash string) bool {
	if len(hash) != 40 {
		return false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') && !('A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}
