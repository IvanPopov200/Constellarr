package operations

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxSmallBody      = 16 << 10
	maxRestoreBody    = 4 << 10
	maxImportJSON     = 96 << 20
	maxEventLimit     = 200
	defaultEventLimit = 50
)

// Register adds the metrics endpoint and the operations routes to the server mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/v1/operations/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/operations/events", s.handleEvents)
	mux.HandleFunc("GET /api/v1/operations/alerts", s.handleAlerts)
	mux.HandleFunc("GET /api/v1/operations/alerts/config", s.handleAlertsConfig)
	mux.HandleFunc("PUT /api/v1/operations/alerts/config", s.handleSaveAlertsConfig)
	mux.HandleFunc("GET /api/v1/operations/alerts/history", s.handleAlertHistory)
	mux.HandleFunc("GET /api/v1/operations/backups", s.handleBackups)
	mux.HandleFunc("POST /api/v1/operations/backups", s.handleCreateBackup)
	mux.HandleFunc("GET /api/v1/operations/backups/config", s.handleBackupConfig)
	mux.HandleFunc("PUT /api/v1/operations/backups/config", s.handleSaveBackupConfig)
	mux.HandleFunc("POST /api/v1/operations/backups/import", s.handleImportBackup)
	mux.HandleFunc("GET /api/v1/operations/backups/{id}", s.handleBackup)
	mux.HandleFunc("DELETE /api/v1/operations/backups/{id}", s.handleDeleteBackup)
	mux.HandleFunc("GET /api/v1/operations/backups/{id}/download", s.handleDownloadBackup)
	mux.HandleFunc("POST /api/v1/operations/backups/{id}/restore", s.handleRestoreBackup)
}

func (s *Service) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.writeMetrics(w)
}

// extendDeadlines lifts the server's global read and write timeouts for one long operation.
func (s *Service) extendDeadlines(w http.ResponseWriter, r *http.Request, timeout time.Duration) {
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(timeout)
	_ = controller.SetWriteDeadline(deadline)
	if r.Body != nil {
		_ = controller.SetReadDeadline(deadline)
	}
}

type storageView struct {
	FreeBytes   int64   `json:"freeBytes"`
	TotalBytes  int64   `json:"totalBytes"`
	UsedPercent float64 `json:"usedPercent"`
	Known       bool    `json:"known"`
}

type downloadsView struct {
	Queued     int64 `json:"queued"`
	Active     int64 `json:"active"`
	Failed     int64 `json:"failed"`
	BytesDone  int64 `json:"bytesDone"`
	BytesTotal int64 `json:"bytesTotal"`
}

type torrentsView struct {
	Queued     int64 `json:"queued"`
	Active     int64 `json:"active"`
	Seeding    int64 `json:"seeding"`
	Failed     int64 `json:"failed"`
	BytesDone  int64 `json:"bytesDone"`
	BytesTotal int64 `json:"bytesTotal"`
}

type libraryView struct {
	Movies   int64 `json:"movies"`
	Series   int64 `json:"series"`
	Episodes int64 `json:"episodes"`
	Artists  int64 `json:"artists"`
	Albums   int64 `json:"albums"`
	Tracks   int64 `json:"tracks"`
}

type failuresView struct {
	DownloadFailures int64 `json:"downloadFailures"`
	ImportErrors     int64 `json:"importErrors"`
	ProviderFailures int64 `json:"providerFailures"`
}

type alertsSummaryView struct {
	Firing int `json:"firing"`
	Total  int `json:"total"`
}

type backupsSummaryView struct {
	ScheduleEnabled bool       `json:"scheduleEnabled"`
	IntervalHours   int        `json:"intervalHours"`
	RetentionCount  int        `json:"retentionCount"`
	Count           int        `json:"count"`
	LastCreatedAt   *time.Time `json:"lastCreatedAt,omitempty"`
}

type statusView struct {
	Database    string             `json:"database"`
	StartedAt   time.Time          `json:"startedAt"`
	Storage     storageView        `json:"storage"`
	Downloads   downloadsView      `json:"downloads"`
	Torrents    torrentsView       `json:"torrents"`
	Library     libraryView        `json:"library"`
	Failures    failuresView       `json:"failures"`
	Alerts      alertsSummaryView  `json:"alerts"`
	Backups     backupsSummaryView `json:"backups"`
	MetricsPath string             `json:"metricsPath"`
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := s.metrics.current()
	view := statusView{
		Database: "connected", StartedAt: s.metrics.startedAt, MetricsPath: "/metrics",
		Storage: storageView{
			FreeBytes: snapshot.Storage.Free, TotalBytes: snapshot.Storage.Total,
			UsedPercent: storagePercent(snapshot.Storage.Free, snapshot.Storage.Total), Known: snapshot.Storage.Known,
		},
	}
	if !snapshot.DBUp {
		view.Database = "unavailable"
	}
	view.Alerts.Firing = snapshot.AlertsFiring
	rules := s.rulesSnapshot()
	view.Alerts.Total = len(rules)
	for _, stat := range snapshot.Downloads {
		switch stat.State {
		case "queued":
			view.Downloads.Queued = stat.Count
		case "failed":
			view.Downloads.Failed = stat.Count
		}
		view.Downloads.BytesDone += stat.BytesDone
		view.Downloads.BytesTotal += stat.BytesTotal
	}
	view.Downloads.Active = snapshot.JobsActive
	for _, stat := range snapshot.Torrents {
		switch stat.State {
		case "queued", "metadata":
			view.Torrents.Queued += stat.Count
		case "checking", "downloading":
			view.Torrents.Active += stat.Count
		case "seeding":
			view.Torrents.Seeding += stat.Count
		case "failed":
			view.Torrents.Failed = stat.Count
		}
		view.Torrents.BytesDone += stat.BytesDone
		view.Torrents.BytesTotal += stat.BytesTotal
	}
	view.Library = libraryView{
		Movies: snapshot.Library["movies"], Series: snapshot.Library["series"], Episodes: snapshot.Library["episodes"],
		Artists: snapshot.Library["artists"], Albums: snapshot.Library["albums"], Tracks: snapshot.Library["tracks"],
	}
	view.Failures = failuresView{
		DownloadFailures: snapshot.Failures[KindDownloadFailed],
		ImportErrors:     snapshot.Failures[KindImportError],
		ProviderFailures: snapshot.Failures[KindProviderFailure],
	}
	config := s.backupConfigSnapshot()
	view.Backups = backupsSummaryView{
		ScheduleEnabled: config.ScheduleEnabled, IntervalHours: config.IntervalHours, RetentionCount: config.RetentionCount,
	}
	if backups, err := s.ListBackups(r.Context()); err == nil {
		view.Backups.Count = len(backups)
		if len(backups) > 0 {
			created := backups[0].CreatedAt
			view.Backups.LastCreatedAt = &created
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	kind := strings.TrimSpace(query.Get("kind"))
	severity := strings.TrimSpace(query.Get("severity"))
	if kind != "" && !validKind(kind) {
		s.respondError(w, InvalidError("unknown event kind"))
		return
	}
	if severity != "" && severity != severityInfo && severity != severityWarning && severity != severityCritical {
		s.respondError(w, InvalidError("unknown severity"))
		return
	}
	filter := eventFilter{limit: parseLimit(query.Get("limit"))}
	if kind != "" {
		filter.kinds = []string{kind}
	}
	if severity != "" {
		filter.severities = []string{severity}
	}
	events, err := s.listEvents(r.Context(), filter)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Events []EventRecord `json:"events"`
	}{events})
}

type alertRuleView struct {
	Name       string        `json:"name"`
	Enabled    bool          `json:"enabled"`
	Severity   string        `json:"severity"`
	Thresholds ThresholdView `json:"thresholds"`
}

func (s *Service) alertRuleViews() []alertRuleView {
	rules := s.rulesSnapshot()
	views := make([]alertRuleView, 0, len(rules))
	for _, rule := range rules {
		views = append(views, alertRuleView{
			Name: rule.name, Enabled: rule.enabled, Severity: rule.severity, Thresholds: viewOf(rule.thresholds),
		})
	}
	return views
}

type alertStateView struct {
	Name       string        `json:"name"`
	Enabled    bool          `json:"enabled"`
	Severity   string        `json:"severity"`
	Thresholds ThresholdView `json:"thresholds"`
	Firing     bool          `json:"firing"`
	Value      float64       `json:"value"`
	Message    string        `json:"message,omitempty"`
	Since      *time.Time    `json:"since,omitempty"`
}

func (s *Service) handleAlerts(w http.ResponseWriter, r *http.Request) {
	rules := s.alertRuleViews()
	states := map[string]alertState{}
	for _, state := range s.statesSnapshot() {
		states[state.Name] = state
	}
	views := make([]alertStateView, 0, len(rules))
	for _, rule := range rules {
		state := states[rule.Name]
		view := alertStateView{
			Name: rule.Name, Enabled: rule.Enabled, Severity: rule.Severity, Thresholds: rule.Thresholds,
			Firing: state.Firing, Value: state.Value, Message: state.Message,
		}
		if !state.Since.IsZero() {
			since := state.Since
			view.Since = &since
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, struct {
		Alerts      []alertStateView `json:"alerts"`
		GeneratedAt time.Time        `json:"generatedAt"`
	}{views, time.Now()})
}

func (s *Service) handleAlertsConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Rules   []alertRuleView `json:"rules"`
		Webhook webhookView     `json:"webhook"`
	}{s.alertRuleViews(), s.webhookConfig().view()})
}

type alertsConfigInput struct {
	Rules []struct {
		Name       string        `json:"name"`
		Enabled    bool          `json:"enabled"`
		Severity   string        `json:"severity"`
		Thresholds ThresholdView `json:"thresholds"`
	} `json:"rules"`
	Webhook struct {
		Enabled                *bool   `json:"enabled"`
		URL                    *string `json:"url"`
		HeaderName             *string `json:"headerName"`
		Secret                 *string `json:"secret"`
		TimeoutSeconds         *int    `json:"timeoutSeconds"`
		MinimumIntervalSeconds *int    `json:"minimumIntervalSeconds"`
		NotifyRecovery         *bool   `json:"notifyRecovery"`
	} `json:"webhook"`
}

func (s *Service) handleSaveAlertsConfig(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[alertsConfigInput](w, r, maxSmallBody)
	if err != nil {
		s.respondError(w, err)
		return
	}
	updates := make([]alertRule, 0, len(input.Rules))
	seen := map[string]bool{}
	for _, rule := range input.Rules {
		if !validRuleName(rule.Name) {
			s.respondError(w, InvalidError("unknown alert rule"))
			return
		}
		if seen[rule.Name] {
			s.respondError(w, InvalidError("an alert rule was sent twice"))
			return
		}
		seen[rule.Name] = true
		thresholds, err := thresholdsFromView(rule.Name, rule.Thresholds)
		if err != nil {
			s.respondError(w, err)
			return
		}
		severity := normalizeSeverity(rule.Severity)
		if rule.Severity != severity {
			s.respondError(w, InvalidError("alert severity must be info, warning or critical"))
			return
		}
		updates = append(updates, alertRule{name: rule.Name, enabled: rule.Enabled, severity: severity, thresholds: thresholds})
	}
	// Both parts are validated before anything is written so a rejected webhook cannot leave rules half-applied.
	var pendingWebhook *webhookConfig
	if input.Webhook.URL != nil || input.Webhook.Enabled != nil || input.Webhook.Secret != nil ||
		input.Webhook.TimeoutSeconds != nil || input.Webhook.MinimumIntervalSeconds != nil ||
		input.Webhook.HeaderName != nil || input.Webhook.NotifyRecovery != nil {
		current := s.webhookConfig()
		update := current
		if input.Webhook.Enabled != nil {
			update.Enabled = *input.Webhook.Enabled
		}
		if input.Webhook.URL != nil {
			update.URL = *input.Webhook.URL
		}
		if input.Webhook.HeaderName != nil {
			update.HeaderName = *input.Webhook.HeaderName
		}
		if input.Webhook.Secret != nil {
			update.Secret = *input.Webhook.Secret
		}
		if input.Webhook.TimeoutSeconds != nil {
			update.TimeoutSeconds = *input.Webhook.TimeoutSeconds
		}
		if input.Webhook.MinimumIntervalSeconds != nil {
			update.MinimumIntervalSeconds = *input.Webhook.MinimumIntervalSeconds
		}
		if input.Webhook.NotifyRecovery != nil {
			update.NotifyRecovery = *input.Webhook.NotifyRecovery
		}
		validated, err := validateWebhook(current, update)
		if err != nil {
			s.respondError(w, err)
			return
		}
		pendingWebhook = &validated
	}
	if len(updates) > 0 {
		if err := s.saveRules(r.Context(), updates); err != nil {
			s.respondError(w, err)
			return
		}
	}
	if pendingWebhook != nil {
		body, err := json.Marshal(*pendingWebhook)
		if err != nil {
			s.respondError(w, InvalidError("webhook settings could not be encoded"))
			return
		}
		if err := s.saveConfigData(r.Context(), "webhook", body); err != nil {
			s.respondError(w, err)
			return
		}
		s.configMu.Lock()
		s.webhook = *pendingWebhook
		s.configMu.Unlock()
	}
	if len(updates) > 0 {
		rules, err := s.loadRules(r.Context())
		if err != nil {
			s.respondError(w, err)
			return
		}
		s.configMu.Lock()
		s.rules = rules
		s.configMu.Unlock()
	}
	s.handleAlertsConfig(w, r)
}

func (s *Service) handleAlertHistory(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name != "" && !validRuleName(name) {
		s.respondError(w, InvalidError("unknown alert rule"))
		return
	}
	events, err := s.listAlertEvents(r.Context(), parseLimit(r.URL.Query().Get("limit")), name)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Events []AlertEventRecord `json:"events"`
	}{events})
}

func (s *Service) handleBackups(w http.ResponseWriter, r *http.Request) {
	backups, err := s.ListBackups(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Backups []Backup `json:"backups"`
	}{backups})
}

func (s *Service) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > 0 {
		if _, err := decodeJSON[struct{}](w, r, maxRestoreBody); err != nil {
			s.respondError(w, err)
			return
		}
	}
	s.extendDeadlines(w, r, s.options.BackupTimeout)
	backup, err := s.CreateBackup(r.Context(), "manual")
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, backup)
}

func (s *Service) handleBackup(w http.ResponseWriter, r *http.Request) {
	backup, err := s.Backup(r.Context(), r.PathValue("id"))
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, backup)
}

func (s *Service) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteBackup(r.Context(), r.PathValue("id")); err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validBackupID(id) {
		s.respondError(w, ErrNotFound)
		return
	}
	if _, err := loadManifest(s.backupDir(id)); err != nil {
		s.respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": id + ".tar"}))
	w.Header().Set("Cache-Control", "no-store")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	tracker := &writeTracker{writer: w}
	if err := s.DownloadBackup(r.Context(), id, tracker); err != nil && !tracker.started {
		s.respondError(w, err)
		return
	} else if err != nil {
		s.logger.Warn("operations: the backup download was interrupted", "backup", id)
	}
}

type writeTracker struct {
	writer  io.Writer
	started bool
}

func (t *writeTracker) Write(body []byte) (int, error) {
	t.started = true
	return t.writer.Write(body)
}

func (s *Service) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[struct {
		Preview bool   `json:"preview"`
		Confirm string `json:"confirm"`
	}](w, r, maxRestoreBody)
	if err != nil {
		s.respondError(w, err)
		return
	}
	id := r.PathValue("id")
	if input.Preview {
		s.extendDeadlines(w, r, s.options.BackupTimeout)
		plan, err := s.PreviewRestore(r.Context(), id)
		if err != nil {
			s.respondError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
		return
	}
	s.extendDeadlines(w, r, s.options.RestoreTimeout)
	result, err := s.Restore(r.Context(), id, input.Confirm)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backupConfigSnapshot())
}

func (s *Service) handleSaveBackupConfig(w http.ResponseWriter, r *http.Request) {
	input, err := decodeJSON[backupConfig](w, r, maxSmallBody)
	if err != nil {
		s.respondError(w, err)
		return
	}
	config, err := s.UpdateBackupConfig(r.Context(), input)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

// handleImportBackup accepts a JSON base64 payload or a raw archive stream.
func (s *Service) handleImportBackup(w http.ResponseWriter, r *http.Request) {
	s.extendDeadlines(w, r, s.options.BackupTimeout)
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var (
		source io.Reader = r.Body
		limit            = int64(maxArchiveBytes)
	)
	if contentType == "application/json" {
		input, err := decodeJSON[struct {
			Data string `json:"data"`
		}](w, r, maxImportJSON)
		if err != nil {
			s.respondError(w, err)
			return
		}
		source = base64.NewDecoder(base64.StdEncoding, strings.NewReader(input.Data))
		limit = maxImportJSON / 4 * 3
	}
	backup, err := s.ImportBackup(r.Context(), source, limit)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, backup)
}

func (s *Service) respondError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "the operations request failed"
	switch {
	case errors.Is(err, ErrNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, ErrNotConfigured):
		status, message = http.StatusServiceUnavailable, err.Error()
	default:
		s.logger.Warn("operations: request failed", "error", err.Error())
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON[T any](w http.ResponseWriter, r *http.Request, limit int64) (T, error) {
	var value T
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, InvalidError("invalid request body")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return value, InvalidError("send one JSON object")
	}
	return value, nil
}

func parseLimit(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 || value > maxEventLimit {
		return defaultEventLimit
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
