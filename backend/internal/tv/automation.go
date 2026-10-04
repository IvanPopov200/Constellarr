package tv

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

// Distinct cross-process advisory lock for TV automation; imports stay first in every cycle.
const tvAutomationLock int64 = 0x54564175746F6D61

const (
	automationPoll          = 20 * time.Second
	metadataRefreshInterval = 24 * time.Hour
	metadataRetryInterval   = 5 * time.Minute
	maxRefreshSeries        = 5
	maxAutoEpisodes         = 10
)

// Start polls completed downloads and automation on the server until Close.
func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		s.workers.Go(func() {
			ticker := time.NewTicker(automationPoll)
			defer ticker.Stop()
			for {
				_, err := s.Sync(runCtx, false)
				if err != nil && runCtx.Err() == nil && !errors.Is(err, ErrConflict) && !errors.Is(err, downloads.ErrNotConfigured) {
					slog.Warn("TV automation needs attention", "error", err)
				}
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	})
}

func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.workers.Wait()
}

type seriesWork struct {
	series  Series
	profile quality.Profile
	wanted  []Episode
}

// Sync imports completed downloads, refreshes metadata, and acquires monitored wanted episodes.
func (s *Service) Sync(ctx context.Context, force bool) (movies.SyncResult, error) {
	result := movies.SyncResult{}
	if !s.syncMu.TryLock() {
		return result, ErrConflict
	}
	defer s.syncMu.Unlock()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return result, errors.New("tv: TV automation cannot reach the database")
	}
	defer conn.Release()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, tvAutomationLock).Scan(&held); err != nil {
		return result, errors.New("tv: TV automation cannot acquire its lock")
	}
	if !held {
		return result, ErrConflict
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, tvAutomationLock); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return result, err
	}
	before, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return result, err
	}
	var backgroundErrors []error
	// Imports run before any provider or metadata work so finished downloads never wait.
	result.Imported, err = s.SyncDownloads(ctx)
	if err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if cfg.RetryFailed {
		if err := s.resetFailedSearches(ctx, before); err != nil {
			return result, err
		}
	}
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return result, err
	}
	active := activeEpisodeIDs(acquisitions)
	seriesList, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	profiles, err := s.Shared.Profiles(ctx)
	if err != nil {
		return result, err
	}
	byProfile := make(map[string]quality.Profile, len(profiles))
	for _, profile := range profiles {
		byProfile[profile.ID] = profile
	}
	fallbackProfile := quality.Profile{}
	if len(profiles) > 0 {
		fallbackProfile = profiles[0]
	}
	if err := s.refreshDueSeries(ctx, seriesList, &backgroundErrors); err != nil {
		return result, err
	}
	seriesList, err = s.List(ctx)
	if err != nil {
		return result, err
	}
	work, err := s.seriesWork(ctx, cfg, seriesList, byProfile, fallbackProfile)
	if err != nil {
		return result, err
	}
	episodesQueued := 0
	if len(work) > 0 {
		due, err := s.Store.TaskDue(ctx, "rss", pollInterval(cfg))
		if err != nil {
			return result, err
		}
		if force || due {
			feed, err := s.Downloads.RSSTV(ctx)
			switch {
			case errors.Is(err, downloads.ErrNotConfigured):
			case err != nil:
				backgroundErrors = append(backgroundErrors, err)
			default:
				queued, err := s.acquireFromRSS(ctx, feed, work, active, maxAutoEpisodes-episodesQueued, &result)
				episodesQueued += queued
				if err != nil {
					backgroundErrors = append(backgroundErrors, err)
				}
			}
		}
	}
	searched := 0
	searchBudget := maxAutoEpisodes - episodesQueued
	if searchBudget < 0 {
		searchBudget = 0
	}
	searchInterval := searchWindow(cfg)
	for i := range work {
		if ctx.Err() != nil || searched >= searchBudget {
			break
		}
		for _, episode := range work[i].wanted {
			if ctx.Err() != nil || searched >= searchBudget {
				break
			}
			if active[episode.ID] {
				continue
			}
			if !force && episode.LastSearchAt != nil && time.Since(*episode.LastSearchAt) < searchInterval {
				continue
			}
			// Persist the search before the provider call so a restart cannot hammer the indexer.
			now := time.Now().UTC()
			if _, err := s.pool.Exec(ctx,
				`UPDATE tv_episodes SET data = jsonb_set(data, '{lastSearchAt}', to_jsonb($2::timestamptz)) WHERE id = $1`,
				episode.ID, now); err != nil {
				return result, errors.New("tv: search scheduling failed")
			}
			searched++
			releases, err := s.Search(ctx, work[i].series.ID, Target{Season: episode.Season, Episode: episode.Number})
			if err != nil {
				if errors.Is(err, downloads.ErrNotConfigured) {
					s.restoreLastSearch(ctx, episode)
				} else {
					backgroundErrors = append(backgroundErrors, err)
				}
				continue
			}
			s.grabSearchResult(ctx, work[i].series, episode, releases, active, &result)
		}
	}
	result.Searched = searched
	return result, errors.Join(backgroundErrors...)
}

// grabSearchResult grabs the first allowed fresh result and marks every episode it covers.
func (s *Service) grabSearchResult(ctx context.Context, series Series, episode Episode, releases []Release, active map[string]bool, result *movies.SyncResult) {
	for _, release := range releases {
		if !release.Decision.Allowed {
			continue
		}
		if len(release.EpisodeIDs) > 0 && !tvContains(release.EpisodeIDs, episode.ID) {
			continue
		}
		target := Target{Season: episode.Season, Episode: episode.Number}
		if release.Pack {
			target = Target{Season: episode.Season}
		}
		covered := release.EpisodeIDs
		if len(covered) == 0 {
			covered = []string{episode.ID}
		}
		if _, err := s.Grab(ctx, series.ID, GrabInput{Target: target, ReleaseID: release.ID}); err == nil {
			result.Queued++
		} else if !errors.Is(err, ErrConflict) {
			slog.Warn("TV grab failed", "series", series.ID, "release", release.ID, "error", err)
		}
		for _, id := range covered {
			active[id] = true
		}
		return
	}
}

// acquireFromRSS matches the feed with the same strict identity and quality policy as searches.
func (s *Service) acquireFromRSS(ctx context.Context, feed []indexer.Release, work []seriesWork, active map[string]bool, remaining int, result *movies.SyncResult) (int, error) {
	if remaining <= 0 {
		return 0, nil
	}
	queued := 0
	var problems []error
	for i := range work {
		if ctx.Err() != nil || queued >= remaining {
			break
		}
		series := &work[i]
		if len(series.wanted) == 0 {
			continue
		}
		candidates := make([]Release, 0, len(feed))
		for _, release := range feed {
			item := evaluateRelease(series.profile, series.series, series.wanted, release)
			if !item.Decision.Allowed || len(item.EpisodeIDs) == 0 || coversActive(item.EpisodeIDs, active) {
				continue
			}
			blocked, err := s.Store.Blocked(ctx, series.series.ID, release.ID)
			if err != nil {
				problems = append(problems, err)
				candidates = nil
				break
			}
			if blocked {
				continue
			}
			candidates = append(candidates, item)
		}
		tvSortReleases(candidates)
		for _, item := range candidates {
			target, ok := grabTarget(item, series.wanted)
			if !ok {
				continue
			}
			if _, err := s.Grab(ctx, series.series.ID, GrabInput{Target: target, ReleaseID: item.ID}); err == nil {
				result.Queued++
				queued += len(item.EpisodeIDs)
			} else if !errors.Is(err, ErrConflict) {
				problems = append(problems, err)
			}
			for _, id := range item.EpisodeIDs {
				active[id] = true
			}
			break
		}
	}
	return queued, errors.Join(problems...)
}

func coversActive(episodeIDs []string, active map[string]bool) bool {
	for _, id := range episodeIDs {
		if active[id] {
			return true
		}
	}
	return false
}

// grabTarget builds a provider-safe target; the provider accepts season 0 specials.
func grabTarget(release Release, episodes []Episode) (Target, bool) {
	if !release.Pack {
		for _, episode := range episodes {
			if tvContains(release.EpisodeIDs, episode.ID) && episode.Season >= 0 {
				return Target{Season: episode.Season, Episode: episode.Number}, true
			}
		}
	}
	if release.Season >= 0 {
		return Target{Season: release.Season}, true
	}
	for _, episode := range episodes {
		if tvContains(release.EpisodeIDs, episode.ID) && episode.Season >= 0 {
			return Target{Season: episode.Season}, true
		}
	}
	return Target{}, false
}

func (s *Service) refreshDueSeries(ctx context.Context, seriesList []Series, backgroundErrors *[]error) error {
	sharedCfg, err := s.Shared.Config(ctx)
	if err != nil || strings.TrimSpace(sharedCfg.MetadataAPIKey) == "" {
		return nil
	}
	refreshed := 0
	for _, series := range seriesList {
		if refreshed >= maxRefreshSeries || ctx.Err() != nil {
			break
		}
		if series.Metadata.IMDbID == "" {
			continue
		}
		if series.LastRefreshAt != nil && time.Since(*series.LastRefreshAt) < metadataRefreshInterval {
			continue
		}
		// A partial catalog resumes on a short window; a complete one waits for the daily refresh.
		interval := metadataRetryInterval
		if series.LastRefreshAt != nil {
			interval = metadataRefreshInterval
		}
		claimed, err := s.Store.TaskDue(ctx, "metadata-"+series.ID, interval)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		refreshed++
		if _, err := s.Refresh(ctx, series.ID); err != nil {
			*backgroundErrors = append(*backgroundErrors, err)
		}
	}
	return nil
}

func (s *Service) seriesWork(ctx context.Context, cfg Config, seriesList []Series, byProfile map[string]quality.Profile, fallback quality.Profile) ([]seriesWork, error) {
	work := make([]seriesWork, 0, len(seriesList))
	for _, series := range seriesList {
		if !series.Monitored {
			continue
		}
		profile, ok := byProfile[series.ProfileID]
		if series.ProfileID == "" {
			profile, ok = fallback, fallback.ID != ""
		}
		if !ok || profile.ID == "" {
			continue
		}
		episodes, err := s.Store.Episodes(ctx, series.ID)
		if err != nil {
			return nil, err
		}
		work = append(work, seriesWork{series: series, profile: profile, wanted: s.wantedEpisodes(cfg, profile, episodes)})
	}
	return work, nil
}

// wantedEpisodes requires a known past air date and keeps the shared Needed policy.
func (s *Service) wantedEpisodes(cfg Config, profile quality.Profile, episodes []Episode) []Episode {
	wanted := make([]Episode, 0, len(episodes))
	for _, episode := range episodes {
		if !tvAired(episode.AirDate) {
			continue
		}
		if !cfg.RetryFailed && strings.TrimSpace(episode.Error) != "" && !strings.HasPrefix(episode.Error, missingEpisodeError) {
			continue
		}
		fresh := episode
		fresh.Files = s.presentEpisodeFiles(cfg, episode.Files)
		if Needed(profile, fresh) {
			wanted = append(wanted, episode)
		}
	}
	return wanted
}

// restoreLastSearch keeps an episode due when the provider was never reached.
func (s *Service) restoreLastSearch(ctx context.Context, episode Episode) {
	if episode.LastSearchAt == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE tv_episodes SET data = data - 'lastSearchAt' WHERE id = $1`, episode.ID)
		return
	}
	_, _ = s.pool.Exec(ctx, `UPDATE tv_episodes SET data = jsonb_set(data, '{lastSearchAt}', to_jsonb($2::timestamptz)) WHERE id = $1`,
		episode.ID, *episode.LastSearchAt)
}

func (s *Service) resetFailedSearches(ctx context.Context, before []Acquisition) error {
	after, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return err
	}
	old := make(map[string]string, len(before))
	for _, acquisition := range before {
		old[acquisition.JobID] = acquisition.Status
	}
	for _, acquisition := range after {
		if acquisition.Status != "failed" || old[acquisition.JobID] == "failed" {
			continue
		}
		ids := normalizeList(acquisition.EpisodeIDs)
		if len(ids) == 0 {
			continue
		}
		// Blocking the failed release frees the episodes for alternate searches.
		if _, err := s.pool.Exec(ctx,
			`UPDATE tv_episodes SET data = jsonb_set(data, '{lastSearchAt}', 'null'::jsonb) WHERE id = ANY($1)`, ids); err != nil {
			return errors.New("tv: retry scheduling failed")
		}
	}
	return nil
}

// activeEpisodeIDs maps episodes with an in-flight acquisition so only they are held back.
func activeEpisodeIDs(acquisitions []Acquisition) map[string]bool {
	active := map[string]bool{}
	for _, acquisition := range acquisitions {
		switch acquisition.Status {
		case "queued", "downloading", "importing", "import-failed", "":
			for _, id := range acquisition.EpisodeIDs {
				active[id] = true
			}
		}
	}
	return active
}

func pollInterval(cfg Config) time.Duration {
	if cfg.PollMinutes <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(cfg.PollMinutes) * time.Minute
}

func searchWindow(cfg Config) time.Duration {
	if cfg.SearchHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(cfg.SearchHours) * time.Hour
}
