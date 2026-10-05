package music

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	musicAutomationLock int64 = 0x4D75736963417574
	automationTick            = 30 * time.Second
	artistRefreshHours        = 24
	albumRefreshHours         = 72
)

// Start runs the monitor loop; it keeps working without a browser session.
func (s *Service) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		s.workers.Go(func() {
			ticker := time.NewTicker(automationTick)
			defer ticker.Stop()
			for {
				if _, err := s.Sync(runCtx, false); err != nil && runCtx.Err() == nil && !errors.Is(err, ErrConflict) {
					slog.Warn("music automation needs attention", "error", err)
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

// Sync imports finished downloads and runs monitoring, RSS, retry, and upgrade work.
func (s *Service) Sync(ctx context.Context, force bool) (SyncResult, error) {
	result := SyncResult{}
	if !s.syncMu.TryLock() {
		return result, ErrConflict
	}
	defer s.syncMu.Unlock()
	conn, err := s.Store.pool.Acquire(ctx)
	if err != nil {
		return result, errors.New("music: automation cannot reach the database")
	}
	defer conn.Release()
	var held bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, musicAutomationLock).Scan(&held); err != nil {
		return result, errors.New("music: automation cannot acquire its lock")
	}
	if !held {
		return result, ErrConflict
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, musicAutomationLock); err != nil {
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return result, err
	}
	var backgroundErrors []error
	before := map[string]string{}
	if acquisitions, err := s.Store.Acquisitions(ctx); err == nil {
		for _, acquisition := range acquisitions {
			before[acquisition.JobID] = acquisition.Status
		}
	}
	result.Imported, err = s.SyncDownloads(ctx)
	if err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if cfg.RetryFailed {
		if err := s.rescheduleFailed(ctx, before); err != nil {
			backgroundErrors = append(backgroundErrors, err)
		}
	}
	if err := s.refreshArtists(ctx, cfg, force); err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if err := s.refreshAlbums(ctx, cfg, force); err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if err := s.syncFeed(ctx, cfg, force, &result); err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	if err := s.runScheduledSearches(ctx, cfg, force, &result); err != nil {
		backgroundErrors = append(backgroundErrors, err)
	}
	return result, errors.Join(backgroundErrors...)
}

// rescheduleFailed clears the search throttle so a failed album is retried with another release.
func (s *Service) rescheduleFailed(ctx context.Context, before map[string]string) error {
	acquisitions, err := s.Store.Acquisitions(ctx)
	if err != nil {
		return err
	}
	for _, acquisition := range acquisitions {
		if acquisition.Status != "failed" || before[acquisition.JobID] == "failed" {
			continue
		}
		album, err := s.Store.Album(ctx, acquisition.AlbumID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !album.Monitored {
			continue
		}
		if err := s.Store.PatchAlbumData(ctx, album.ID, map[string]any{"lastSearchAt": nil}); err != nil {
			return err
		}
	}
	return nil
}

// refreshArtists adds newly released albums for monitored artists.
func (s *Service) refreshArtists(ctx context.Context, cfg Config, force bool) error {
	artists, err := s.Store.Artists(ctx)
	if err != nil {
		return err
	}
	refreshed := 0
	for _, artist := range artists {
		if refreshed >= maxArtistsRefresh || ctx.Err() != nil {
			break
		}
		if !artist.Monitored || artist.MusicBrainzID == "" || artist.MonitorOption == "none" {
			continue
		}
		if !force && artist.LastRefreshAt != nil && time.Since(*artist.LastRefreshAt) < artistRefreshHours*time.Hour {
			continue
		}
		if !force && !s.Store.TaskDue(ctx, "artist:"+artist.ID, artistRefreshHours*time.Hour) {
			continue
		}
		if err := s.Store.RecordTask(ctx, "artist:"+artist.ID); err != nil {
			return err
		}
		refreshed++
		if _, err := s.RefreshArtist(ctx, artist.ID); err != nil {
			slog.Warn("music artist refresh failed", "artist", artist.ID, "error", err)
		}
	}
	return nil
}

// refreshAlbums fills in tracklists for albums that have none yet.
func (s *Service) refreshAlbums(ctx context.Context, cfg Config, force bool) error {
	albums, err := s.Store.Albums(ctx)
	if err != nil {
		return err
	}
	refreshed := 0
	for _, album := range albums {
		if refreshed >= maxArtistsRefresh || ctx.Err() != nil {
			break
		}
		if album.MusicBrainzID == "" || !album.Monitored {
			continue
		}
		if !force && !s.Store.TaskDue(ctx, "album:"+album.ID, albumRefreshHours*time.Hour) {
			continue
		}
		tracks, err := s.Store.Tracks(ctx, album.ID)
		if err != nil {
			return err
		}
		if len(tracks) > 0 && !force {
			if err := s.Store.RecordTask(ctx, "album:"+album.ID); err != nil {
				return err
			}
			continue
		}
		if err := s.Store.RecordTask(ctx, "album:"+album.ID); err != nil {
			return err
		}
		refreshed++
		if _, err := s.RefreshAlbum(ctx, album.ID); err != nil {
			slog.Warn("music album refresh failed", "album", album.ID, "error", err)
		}
	}
	return nil
}

// syncFeed matches the recent audio feed against monitored albums.
func (s *Service) syncFeed(ctx context.Context, cfg Config, force bool, result *SyncResult) error {
	if !force && !s.Store.TaskDue(ctx, "rss", time.Duration(cfg.PollMinutes)*time.Minute) {
		return nil
	}
	albums, err := s.candidates(ctx, cfg)
	if err != nil {
		return err
	}
	if len(albums) == 0 {
		return nil
	}
	client, err := s.indexer()
	if err != nil {
		return err
	}
	if err := s.Store.RecordTask(ctx, "rss"); err != nil {
		return err
	}
	feed, err := client.Feed(ctx)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if result.Queued >= maxScheduledSearches || ctx.Err() != nil {
			break
		}
		matches := make([]Release, 0, 4)
		for _, release := range feed {
			if !releaseMatchesAlbum(album, release) {
				continue
			}
			matches = append(matches, release)
		}
		if len(matches) == 0 {
			continue
		}
		profile, _ := profileByID(cfg, album.ProfileID)
		evaluated, err := s.evaluateReleases(ctx, cfg, profile, album, matches)
		if err != nil {
			return err
		}
		for _, release := range evaluated {
			if !release.Decision.Allowed {
				continue
			}
			if _, err := s.Grab(ctx, album.ID, release.ID, false); err == nil {
				result.Queued++
				break
			} else if !errors.Is(err, ErrConflict) {
				_ = s.Store.Block(ctx, album.ID, release.ID)
				break
			}
			break
		}
	}
	return nil
}

// runScheduledSearches retries wanted albums and upgrades files below their cutoff.
func (s *Service) runScheduledSearches(ctx context.Context, cfg Config, force bool, result *SyncResult) error {
	albums, err := s.candidates(ctx, cfg)
	if err != nil {
		return err
	}
	for _, album := range albums {
		if result.Searched >= maxScheduledSearches || ctx.Err() != nil {
			break
		}
		if !force && album.LastSearchAt != nil && time.Since(*album.LastSearchAt) < time.Duration(cfg.SearchHours)*time.Hour {
			continue
		}
		if !cfg.RetryFailed && album.Error != "" {
			continue
		}
		// Stamp only the search time so a concurrent import or monitor change survives.
		if err := s.Store.PatchAlbumData(ctx, album.ID, map[string]any{"lastSearchAt": time.Now().UTC()}); err != nil {
			return err
		}
		result.Searched++
		releases, err := s.Search(ctx, album.ID)
		if err != nil {
			slog.Warn("music search failed", "album", album.ID, "error", err)
			continue
		}
		for _, release := range releases {
			if !release.Decision.Allowed {
				continue
			}
			if _, err := s.Grab(ctx, album.ID, release.ID, false); err == nil {
				result.Queued++
			} else if !errors.Is(err, ErrConflict) {
				_ = s.Store.Block(ctx, album.ID, release.ID)
			}
			break
		}
	}
	return nil
}

// candidates lists monitored albums that still need files or an upgrade.
func (s *Service) candidates(ctx context.Context, cfg Config) ([]Album, error) {
	albums, err := s.Albums(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make([]Album, 0, 16)
	for _, album := range albums {
		if !album.Monitored || album.Status == "downloading" {
			continue
		}
		profile, _ := profileByID(cfg, album.ProfileID)
		switch album.Status {
		case "wanted":
			wanted = append(wanted, album)
		case "failed":
			if cfg.RetryFailed {
				wanted = append(wanted, album)
			}
		case "cutoff-unmet":
			if profile.Upgrade {
				wanted = append(wanted, album)
			}
		}
	}
	return wanted, nil
}
