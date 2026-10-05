package torrents

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
)

const settingsColumns = `listen_port, dht_enabled, pex_enabled, max_active_jobs, download_limit_kbps,
	upload_limit_kbps, seed_ratio_limit, seed_time_limit_minutes`

func loadSettings(ctx context.Context, q querier) (Settings, bool, error) {
	var cfg Settings
	err := q.QueryRow(ctx, `SELECT `+settingsColumns+` FROM torrent_settings WHERE id`).Scan(
		&cfg.ListenPort, &cfg.DHTEnabled, &cfg.PEXEnabled, &cfg.MaxActiveJobs, &cfg.DownloadLimitKBps,
		&cfg.UploadLimitKBps, &cfg.SeedRatioLimit, &cfg.SeedTimeLimitMinutes)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, storeError("load settings", err)
	}
	return cfg, true, nil
}

func saveSettings(ctx context.Context, q querier, cfg Settings) error {
	_, err := q.Exec(ctx, `INSERT INTO torrent_settings (id, `+settingsColumns+`)
		VALUES (true, $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET listen_port = EXCLUDED.listen_port, dht_enabled = EXCLUDED.dht_enabled,
		pex_enabled = EXCLUDED.pex_enabled, max_active_jobs = EXCLUDED.max_active_jobs,
		download_limit_kbps = EXCLUDED.download_limit_kbps, upload_limit_kbps = EXCLUDED.upload_limit_kbps,
		seed_ratio_limit = EXCLUDED.seed_ratio_limit, seed_time_limit_minutes = EXCLUDED.seed_time_limit_minutes,
		updated_at = now()`,
		cfg.ListenPort, cfg.DHTEnabled, cfg.PEXEnabled, cfg.MaxActiveJobs, cfg.DownloadLimitKBps,
		cfg.UploadLimitKBps, cfg.SeedRatioLimit, cfg.SeedTimeLimitMinutes)
	if err != nil {
		return storeError("save settings", err)
	}
	return nil
}

func (s *Service) Settings() Settings {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	cfg := s.cfg
	cfg.Directory = s.directory
	return cfg
}

func (s *Service) UpdateSettings(ctx context.Context, update SettingsUpdate) (Settings, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := validateSettings(update); err != nil {
		return Settings{}, err
	}
	previous := s.cfg
	next := s.cfg
	next.ListenPort = update.ListenPort
	next.DHTEnabled = update.DHTEnabled
	next.PEXEnabled = update.PEXEnabled
	next.MaxActiveJobs = update.MaxActiveJobs
	next.DownloadLimitKBps = update.DownloadLimitKBps
	next.UploadLimitKBps = update.UploadLimitKBps
	next.SeedRatioLimit = update.SeedRatioLimit
	next.SeedTimeLimitMinutes = update.SeedTimeLimitMinutes
	if err := saveSettings(ctx, s.pool, next); err != nil {
		return Settings{}, err
	}
	s.cfg = next
	s.applyRuntimeSettings(next)
	result := next
	result.Directory = s.directory
	result.RestartRequired = previous.ListenPort != next.ListenPort || previous.DHTEnabled != next.DHTEnabled ||
		previous.PEXEnabled != next.PEXEnabled
	return result, nil
}

func validateSettings(update SettingsUpdate) error {
	switch {
	case update.ListenPort < 0 || update.ListenPort > 65535:
		return invalid("the listening port must be between 0 and 65535")
	case update.MaxActiveJobs < 1 || update.MaxActiveJobs > 20:
		return invalid("active torrents must be between 1 and 20")
	case update.DownloadLimitKBps < 0 || update.DownloadLimitKBps > 10_000_000:
		return invalid("the download limit is out of range")
	case update.UploadLimitKBps < 0 || update.UploadLimitKBps > 10_000_000:
		return invalid("the upload limit is out of range")
	case update.SeedRatioLimit < 0 || update.SeedRatioLimit > 1000 || math.IsNaN(update.SeedRatioLimit):
		return invalid("the seed ratio limit must be between 0 and 1000")
	case update.SeedTimeLimitMinutes < 0 || update.SeedTimeLimitMinutes > 525_600:
		return invalid("the seed time limit must be between 0 and 525600 minutes")
	}
	return nil
}

// seedPolicy describes when a completed download stops seeding; a zero ratio means no seeding.
func seedPolicy(ratio float64, minutes int) (float64, int) {
	if ratio < 0 {
		ratio = 0
	}
	if minutes < 0 {
		minutes = 0
	}
	return ratio, minutes
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
