package movies

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

const automationLock int64 = 0x436F6E7374656C74

func (s *Service) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		s.workers.Go(func() {
			ticker := time.NewTicker(20 * time.Second)
			defer ticker.Stop()
			for {
				_, err := s.Sync(runCtx, false)
				if err != nil && runCtx.Err() == nil && !errors.Is(err, ErrConflict) {
					slog.Warn("movie automation needs attention", "error", err)
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

func (s *Service) Sync(ctx context.Context, force bool) (SyncResult, error) {
	result := SyncResult{}
	if !s.syncMu.TryLock() {
		return result, ErrConflict
	}
	defer s.syncMu.Unlock()
	conn, err := s.Store.pool.Acquire(ctx)
	if err != nil {
		return result, errors.New("movie automation cannot reach the database")
	}
	defer conn.Release()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, automationLock).Scan(&held); err != nil {
		return result, errors.New("movie automation cannot acquire its lock")
	}
	if !held {
		return result, ErrConflict
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, automationLock); err != nil {
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
	result.Imported, err = s.SyncDownloads(ctx)
	if err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if cfg.RetryFailed {
		after, err := s.Store.Acquisitions(ctx)
		if err != nil {
			return result, err
		}
		old := map[string]string{}
		for _, acquisition := range before {
			old[acquisition.JobID] = acquisition.Status
		}
		for _, acquisition := range after {
			if acquisition.Status == "failed" && old[acquisition.JobID] != "failed" {
				if _, err := s.Store.pool.Exec(ctx, `UPDATE movies SET data = jsonb_set(data, '{lastSearchAt}', 'null'::jsonb) WHERE id = $1`, acquisition.MovieID); err != nil {
					return result, errors.New("movie retry scheduling failed")
				}
			}
		}
	}
	watchlists, err := s.Store.Watchlists(ctx)
	if err != nil {
		return result, err
	}
	for _, list := range watchlists {
		if force || list.LastSyncAt == nil || time.Since(*list.LastSyncAt) >= time.Duration(list.IntervalHours)*time.Hour {
			if _, err := s.SyncWatchlist(ctx, list.ID); err != nil {
				backgroundErrors = append(backgroundErrors, err)
			}
		}
	}
	movies, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	profiles, err := s.Store.Profiles(ctx)
	if err != nil {
		return result, err
	}
	byID := map[string]quality.Profile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	eligible := []Movie{}
	for _, movie := range movies {
		if automaticCandidate(movie, cfg) {
			eligible = append(eligible, movie)
		}
	}
	if len(eligible) > 0 && (force || s.taskDue(ctx, "rss", time.Duration(cfg.PollMinutes)*time.Minute)) {
		if err := s.recordTask(ctx, "rss"); err != nil {
			return result, err
		}
		feed, err := s.Downloads.RSS(ctx)
		if err != nil {
			backgroundErrors = append(backgroundErrors, err)
		} else {
			for _, movie := range eligible {
				profile := byID[movie.ProfileID]
				releases := []Release{}
				for _, item := range feed {
					if !rssMatches(movie, item) {
						continue
					}
					decision := quality.Evaluate(profile, item.Title, item.Size, bestCurrent(profile, presentFiles(cfg, movie.Files)))
					if decision.Allowed {
						releases = append(releases, Release{Release: item, Decision: decision})
					}
				}
				sort.SliceStable(releases, func(i, j int) bool {
					if releases[i].Decision.Rank != releases[j].Decision.Rank {
						return releases[i].Decision.Rank < releases[j].Decision.Rank
					}
					return releases[i].Decision.Score > releases[j].Decision.Score
				})
				for _, release := range releases {
					blocked, err := s.Store.Blocked(ctx, movie.ID, release.ID)
					if err != nil {
						return result, err
					}
					if blocked {
						continue
					}
					if _, err := s.Grab(ctx, movie.ID, release.ID, false); err == nil {
						result.Queued++
						break
					} else if errors.Is(err, ErrConflict) {
						break
					} else {
						backgroundErrors = append(backgroundErrors, err)
						break
					}
				}
			}
		}
	}
	// Reload after RSS grabs so a scheduled search cannot queue a second release.
	movies, err = s.List(ctx)
	if err != nil {
		return result, err
	}
	for _, movie := range movies {
		if result.Searched >= 10 || ctx.Err() != nil {
			break
		}
		if !automaticCandidate(movie, cfg) || (!force && movie.LastSearchAt != nil && time.Since(*movie.LastSearchAt) < time.Duration(cfg.SearchHours)*time.Hour) {
			continue
		}
		if !cfg.RetryFailed && movie.Error != "" {
			continue
		}
		now := time.Now().UTC()
		if _, err := s.Store.pool.Exec(ctx, `UPDATE movies SET data = jsonb_set(data, '{lastSearchAt}', to_jsonb($2::timestamptz)) WHERE id = $1`, movie.ID, now); err != nil {
			return result, errors.New("movie search scheduling failed")
		}
		result.Searched++
		releases, err := s.Search(ctx, movie.ID)
		if err != nil {
			backgroundErrors = append(backgroundErrors, err)
			continue
		}
		for _, release := range releases {
			if !release.Decision.Allowed {
				continue
			}
			if _, err := s.Grab(ctx, movie.ID, release.ID, false); err == nil {
				result.Queued++
			} else if !errors.Is(err, ErrConflict) {
				backgroundErrors = append(backgroundErrors, err)
			}
			break
		}
	}
	if cfg.MetadataAPIKey != "" {
		refreshed := 0
		for _, movie := range movies {
			if refreshed >= 5 || ctx.Err() != nil {
				break
			}
			if movie.Metadata.IMDbID == "" || !s.taskDue(ctx, "metadata:"+movie.ID, 24*time.Hour) {
				continue
			}
			if err := s.recordTask(ctx, "metadata:"+movie.ID); err != nil {
				return result, err
			}
			refreshed++
			if _, err := s.Refresh(ctx, movie.ID); err != nil {
				backgroundErrors = append(backgroundErrors, err)
			}
		}
	}
	return result, errors.Join(backgroundErrors...)
}

func (s *Service) taskDue(ctx context.Context, name string, interval time.Duration) bool {
	var last time.Time
	err := s.Store.pool.QueryRow(ctx, `SELECT last_run FROM movie_automation WHERE name = $1`, name).Scan(&last)
	return err != nil || time.Since(last) >= interval
}

func (s *Service) recordTask(ctx context.Context, name string) error {
	_, err := s.Store.pool.Exec(ctx, `INSERT INTO movie_automation(name) VALUES ($1) ON CONFLICT(name) DO UPDATE SET last_run = now()`, name)
	if err != nil {
		return errors.New("movie task state could not be saved")
	}
	return nil
}

func automaticCandidate(movie Movie, cfg Config) bool {
	if !movie.Monitored || movie.Status == "downloading" || movie.Status == "importing" || movie.Status == "available" || movie.Status == "import-failed" || (!cfg.RetryFailed && movie.Error != "") {
		return false
	}
	return availabilityReached(movie, cfg)
}

func rssMatches(movie Movie, release indexer.Release) bool {
	if release.IMDbID != "" && movie.Metadata.IMDbID != "" {
		return release.IMDbID == movie.Metadata.IMDbID
	}
	if movie.Metadata.Year <= 0 {
		return false
	}
	prefix := normalizeReleaseTitle(movie.Metadata.Title) + strconv.Itoa(movie.Metadata.Year)
	return strings.HasPrefix(normalizeReleaseTitle(release.Title), prefix)
}

func normalizeReleaseTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, title)
}
