package operations

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("operations: not found")
	ErrConflict      = errors.New("operations: conflict")
	ErrInvalid       = errors.New("operations: invalid input")
	ErrNotConfigured = errors.New("operations: not configured")
)

// Event kinds; KindAlert, KindBackup, KindRestore and KindNotificationFailed are recorded by the service itself.
const (
	KindDownloadFailed     = "download_failed"
	KindImportError        = "import_error"
	KindProviderFailure    = "provider_failure"
	KindBackup             = "backup"
	KindBackupFailed       = "backup_failed"
	KindRestore            = "restore"
	KindAlert              = "alert"
	KindNotificationFailed = "notification_failed"
)

const (
	severityInfo     = "info"
	severityWarning  = "warning"
	severityCritical = "critical"
)

const (
	// One exclusive lock serializes backup creation, import and restore across processes.
	maintenanceLock     int64 = 0x4F50535F4D41494E
	backupFormatVersion       = 1
)

type Options struct {
	DataDir string
	// DatabaseURL is used for pg_dump and pg_restore only; it is never sent to clients or logs.
	DatabaseURL   string
	PGDumpPath    string
	PGRestorePath string
	// Quiesce pauses every automation worker before a live restore; Resume makes them reconnect afterwards.
	Quiesce func(context.Context) error
	Resume  func(context.Context) error
	// AllowLiveRestore must be set before the database may be replaced by a backup.
	AllowLiveRestore bool
	// AutoRestart reports that Resume restarts or reconnects every service, so clients wait instead of asking for a manual restart.
	AutoRestart bool
	// DisableRestoreVerification skips the isolated scratch-database trial restore used before a live restore.
	DisableRestoreVerification bool
	MonitorInterval            time.Duration
	BackupTimeout              time.Duration
	RestoreTimeout             time.Duration
	HTTPClient                 *http.Client
	Logger                     *slog.Logger
}

type Service struct {
	pool        *pgxpool.Pool
	dataDir     string
	backupRoot  string
	databaseURL string
	logger      *slog.Logger
	options     Options

	// downloadsProtocol marks the migration-014 column that separates Usenet rows from torrent shadow rows.
	downloadsProtocol bool

	tools pgTools

	configMu  sync.RWMutex
	rules     []alertRule
	webhook   webhookConfig
	backups   backupConfig
	alertView []alertState

	metrics metricsState

	maintenance atomic.Bool
	backupMu    sync.Mutex
	scheduleMu  sync.Mutex
	// collectMu keeps the collector from writing while a restore replaces the database.
	collectMu sync.RWMutex

	scheduleAttempt time.Time

	dbFailures atomic.Int64

	storageMu   sync.Mutex
	lastStorage storageStats

	notifyMu   sync.Mutex
	notifiedAt map[string]time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	workers   sync.WaitGroup
	notify    chan alertEvent
}

func New(ctx context.Context, pool *pgxpool.Pool, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("operations: a PostgreSQL pool is required")
	}
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("DOWNLOAD_DIR"))
	}
	if dataDir == "" {
		return nil, errors.New("operations: a data directory is required")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, errors.New("operations: the data directory is invalid")
	}
	root := filepath.Join(absolute, "backups")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("operations: the backup directory could not be created")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if options.MonitorInterval <= 0 {
		options.MonitorInterval = 15 * time.Second
	}
	if options.BackupTimeout <= 0 {
		options.BackupTimeout = 30 * time.Minute
	}
	if options.RestoreTimeout <= 0 {
		options.RestoreTimeout = 30 * time.Minute
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	databaseURL := strings.TrimSpace(options.DatabaseURL)
	if databaseURL == "" {
		databaseURL = pool.Config().ConnString()
	}
	service := &Service{
		pool: pool, dataDir: absolute, backupRoot: root, databaseURL: databaseURL,
		logger: logger, options: options,
		tools:  pgTools{explicitDump: options.PGDumpPath, explicitRestore: options.PGRestorePath},
		notify: make(chan alertEvent, 64), notifiedAt: map[string]time.Time{}, metrics: newMetricsState(),
	}
	if err := service.requireSchema(ctx); err != nil {
		return nil, err
	}
	service.downloadsProtocol = service.columnExists(ctx, "downloads", "protocol")
	if err := service.loadConfig(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) columnExists(ctx context.Context, table, column string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = $1 AND column_name = $2)`, table, column).Scan(&exists)
	return err == nil && exists
}

func (s *Service) requireSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('operations_config') IS NOT NULL`).Scan(&ready); err != nil {
		return errors.New("operations: the database could not be checked for the operations schema")
	}
	if !ready {
		return errors.New("operations: the operations schema is missing; apply migration 012_operations.sql")
	}
	return nil
}

func (s *Service) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		s.workers.Add(2)
		go func() {
			defer s.workers.Done()
			s.monitor(runCtx)
		}()
		go func() {
			defer s.workers.Done()
			s.runNotifier(runCtx)
		}()
	})
}

func (s *Service) Close() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.workers.Wait()
	})
}

type Event struct {
	Kind     string
	Severity string
	Source   string
	Message  string
	Ref      string
}

func (e Event) sanitized() Event {
	e.Message = truncate(e.Message, 500)
	e.Source = truncate(e.Source, 64)
	e.Ref = truncate(e.Ref, 128)
	if e.Severity != severityInfo && e.Severity != severityWarning && e.Severity != severityCritical {
		e.Severity = severityWarning
	}
	if !validKind(e.Kind) {
		e.Kind = ""
	}
	return e
}

func validKind(kind string) bool {
	switch kind {
	case KindDownloadFailed, KindImportError, KindProviderFailure, KindBackup, KindBackupFailed,
		KindRestore, KindAlert, KindNotificationFailed:
		return true
	}
	return false
}

// RecordEvent stores a structured event for other components' failures.
func (s *Service) RecordEvent(ctx context.Context, event Event) error {
	event = event.sanitized()
	if event.Kind == "" {
		return InvalidError("unknown event kind")
	}
	if event.Severity == "" {
		event.Severity = severityWarning
	}
	return s.insertEvent(ctx, event)
}

func (s *Service) RecordImportError(ctx context.Context, source, message string) error {
	return s.RecordEvent(ctx, Event{Kind: KindImportError, Severity: severityWarning, Source: source, Message: message})
}

func (s *Service) RecordProviderFailure(ctx context.Context, provider, message string) error {
	return s.RecordEvent(ctx, Event{Kind: KindProviderFailure, Severity: severityWarning, Source: provider, Message: message})
}

// RecordRequestFailure records a request delivery failure; it feeds the provider failures rule.
func (s *Service) RecordRequestFailure(ctx context.Context, message string) error {
	return s.RecordEvent(ctx, Event{Kind: KindProviderFailure, Severity: severityWarning, Source: "requests", Message: message})
}

type InvalidError string

func (e InvalidError) Error() string { return "operations: " + string(e) }

func (e InvalidError) Is(target error) bool { return target == ErrInvalid }

type ConflictError string

func (e ConflictError) Error() string { return "operations: " + string(e) }

func (e ConflictError) Is(target error) bool { return target == ErrConflict }

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}
