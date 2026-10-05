package operations

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

const (
	collectTimeout = 10 * time.Second
	dbPingTimeout  = 3 * time.Second
	maxFailedScan  = 50
)

func (s *Service) monitor(ctx context.Context) {
	s.collect(ctx)
	ticker := time.NewTicker(s.options.MonitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.maintenance.Load() {
				continue
			}
			s.collect(ctx)
		}
	}
}

func (s *Service) collect(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, collectTimeout)
	defer cancel()
	s.collectMu.RLock()
	defer s.collectMu.RUnlock()
	snapshot := metricsSnapshot{At: time.Now(), Collecting: true}
	snapshot.DBUp = s.ping(ctx)
	if snapshot.DBUp {
		if stats, err := s.downloadStats(ctx); err == nil {
			snapshot.Downloads = stats
			for _, stat := range stats {
				switch stat.State {
				case "queued":
					snapshot.JobsQueued = stat.Count
				case "downloading", "verifying", "repairing", "extracting":
					snapshot.JobsActive += stat.Count
				}
			}
		} else {
			s.logger.Warn("operations: download statistics are unavailable", "error", err.Error())
		}
		if stats, err := s.torrentStats(ctx); err == nil {
			snapshot.Torrents = stats
		} else {
			s.logger.Debug("operations: torrent statistics are unavailable")
		}
		snapshot.Library = s.libraryCounts(ctx)
		if at, err := s.lastEventAt(ctx, KindBackup); err == nil {
			snapshot.BackupAt = at
		}
		s.recordDownloadFailures(ctx)
		s.recordImportFailures(ctx)
		if counts, err := s.countEventsByKind(ctx); err == nil {
			snapshot.Failures = counts
		}
	}
	snapshot.Storage = s.storage()
	snapshot.AlertsFiring = s.evaluateAlerts(ctx, alertInputs{
		freeBytes: snapshot.Storage.Free, totalBytes: snapshot.Storage.Total, storageKnown: snapshot.Storage.Known,
		dbFailures: int(s.dbFailures.Load()),
	})
	s.metrics.setSnapshot(snapshot)
	s.maybeScheduleBackup()
}

func (s *Service) ping(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, dbPingTimeout)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		s.dbFailures.Add(1)
		return false
	}
	s.dbFailures.Store(0)
	return true
}

func (s *Service) libraryCounts(ctx context.Context) map[string]int64 {
	tables := map[string]string{
		"movies": "movies", "series": "tv_series", "episodes": "tv_episodes",
		"artists": "music_artists", "albums": "music_albums", "tracks": "music_tracks",
	}
	counts := map[string]int64{}
	for _, kind := range libraryKinds {
		count, err := s.countTable(ctx, tables[kind])
		if err != nil {
			s.logger.Debug("operations: library count is unavailable", "kind", kind)
		}
		counts[kind] = count
	}
	return counts
}

func (s *Service) recordDownloadFailures(ctx context.Context) {
	failed, err := s.failedDownloads(ctx, maxFailedScan)
	if err != nil {
		s.logger.Warn("operations: failed downloads could not be read", "error", err.Error())
		return
	}
	for _, job := range failed {
		message := truncate(job.Title, 120)
		if job.Error != "" {
			message = truncate(message+": "+job.Error, 400)
		}
		if message == "" {
			message = "download failed"
		}
		source := "downloads"
		if job.Protocol == "torrent" {
			source = "torrents"
		}
		event := Event{Kind: KindDownloadFailed, Severity: severityWarning, Source: source, Message: message, Ref: job.ID}
		if err := s.insertEvent(ctx, event.sanitized()); err != nil {
			s.logger.Warn("operations: download failure could not be recorded", "error", err.Error())
			return
		}
		s.logger.Warn("operations: download failed", "job", job.ID, "source", source, "message", truncate(message, 200))
	}
}

func (s *Service) recordImportFailures(ctx context.Context) {
	pending, err := s.pendingImportFailures(ctx, maxFailedScan)
	if err != nil {
		s.logger.Warn("operations: import failures could not be read", "error", err.Error())
		return
	}
	for _, failure := range pending {
		event := Event{
			Kind: KindImportError, Severity: severityWarning, Source: failure.Source,
			Message: truncate(failure.Message, 400), Ref: failure.Ref,
		}
		if err := s.insertEvent(ctx, event.sanitized()); err != nil {
			s.logger.Warn("operations: import failure could not be recorded", "error", err.Error())
			return
		}
		s.logger.Warn("operations: import failed", "source", failure.Source, "ref", failure.Ref)
	}
}

// storage keeps the last known values when the filesystem cannot be inspected.
func (s *Service) storage() storageStats {
	var stat unix.Statfs_t
	if err := unix.Statfs(s.dataDir, &stat); err == nil {
		total := int64(stat.Blocks) * int64(stat.Bsize)
		free := int64(stat.Bavail) * int64(stat.Bsize)
		if total > 0 {
			s.storageMu.Lock()
			s.lastStorage = storageStats{Free: free, Total: total, Known: true}
			known := s.lastStorage
			s.storageMu.Unlock()
			return known
		}
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	return s.lastStorage
}

func (s *Service) evaluateAlerts(ctx context.Context, inputs alertInputs) int {
	now := time.Now()
	rules := s.rulesSnapshot()
	states := map[string]alertState{}
	for _, state := range s.statesSnapshot() {
		states[state.Name] = state
	}
	type transition struct {
		rule       alertRule
		evaluation evaluation
		state      alertState
	}
	var (
		updated []alertState
		changed []transition
		firing  int
	)
	for _, rule := range rules {
		if !rule.enabled {
			continue
		}
		local := inputs
		switch rule.name {
		case "provider_failures":
			window, _ := windowThreshold(rule.thresholds, 60, 3)
			count, err := s.countEventsSince(ctx, []string{KindProviderFailure}, now.Add(-time.Duration(window)*time.Minute))
			if err != nil {
				continue
			}
			local.providerInWindow = count
		case "download_import_failures":
			window, _ := windowThreshold(rule.thresholds, 60, 3)
			count, err := s.countEventsSince(ctx, []string{KindDownloadFailed, KindImportError}, now.Add(-time.Duration(window)*time.Minute))
			if err != nil {
				continue
			}
			local.failuresInWindow = count
		case "job_stuck":
			stuckMinutes := 120
			if rule.thresholds.StuckMinutes != nil {
				stuckMinutes = *rule.thresholds.StuckMinutes
			}
			count, err := s.countStuckJobs(ctx, stuckMinutes)
			if err != nil {
				continue
			}
			local.stuckJobs = count
		}
		result := evaluateRule(rule, local)
		previous := states[rule.name]
		state := alertState{
			Name: rule.name, Firing: result.firing, Severity: rule.severity,
			Message: result.message, Value: result.value, Since: previous.Since, UpdatedAt: now,
		}
		if previous.Firing != result.firing {
			state.Since = now
			changed = append(changed, transition{rule, result, state})
		}
		updated = append(updated, state)
		if result.firing {
			firing++
		}
	}
	s.applyAlertStates(updated)
	for _, state := range updated {
		rule, ok := ruleByName(rules, state.Name)
		if !ok {
			continue
		}
		if err := s.saveAlertState(ctx, rule, evaluation{firing: state.Firing, value: state.Value, message: state.Message}); err != nil {
			s.logger.Debug("operations: alert state could not be stored", "alert", state.Name)
		}
	}
	for _, transition := range changed {
		s.recordAlertTransition(ctx, transition.rule, transition.evaluation, transition.state)
	}
	return firing
}

func ruleByName(rules []alertRule, name string) (alertRule, bool) {
	for _, rule := range rules {
		if rule.name == name {
			return rule, true
		}
	}
	return alertRule{}, false
}

func (s *Service) applyAlertStates(updated []alertState) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	byName := map[string]alertState{}
	for _, state := range s.alertView {
		byName[state.Name] = state
	}
	for _, state := range updated {
		byName[state.Name] = state
	}
	merged := make([]alertState, 0, len(byName))
	for _, name := range ruleNames {
		if state, ok := byName[name]; ok {
			merged = append(merged, state)
		}
	}
	s.alertView = merged
}

func (s *Service) recordAlertTransition(ctx context.Context, rule alertRule, result evaluation, state alertState) {
	id, err := s.recordAlertEvent(ctx, rule, result)
	if err != nil {
		s.logger.Warn("operations: alert event could not be stored", "alert", rule.name)
		return
	}
	status, severity := "resolved", severityInfo
	if result.firing {
		status, severity = "firing", rule.severity
	}
	message := "Alert " + rule.name + " is " + status + "."
	if state.Message != "" {
		message = "Alert " + rule.name + " " + status + ": " + state.Message
	}
	if err := s.insertEvent(ctx, Event{
		Kind: KindAlert, Severity: severity, Source: "alerts", Message: message, Ref: rule.name,
	}.sanitized()); err != nil {
		s.logger.Debug("operations: alert log entry could not be stored")
	}
	s.logger.Info("operations: alert transition", "alert", rule.name, "status", status, "value", result.value)
	select {
	case s.notify <- alertEvent{
		id: id, name: rule.name, firing: result.firing, severity: rule.severity,
		message: result.message, value: result.value, at: time.Now(),
	}:
	default:
		s.logger.Warn("operations: alert notification queue is full", "alert", rule.name)
	}
}

func (s *Service) rulesSnapshot() []alertRule {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return append([]alertRule{}, s.rules...)
}

func (s *Service) statesSnapshot() []alertState {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return append([]alertState{}, s.alertView...)
}
