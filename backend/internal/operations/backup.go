package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maintenanceWait  = 2 * time.Minute
	maintenanceRetry = 500 * time.Millisecond
	minRetention     = 1
	maxRetention     = 200
)

type backupConfig struct {
	ScheduleEnabled bool `json:"scheduleEnabled"`
	IntervalHours   int  `json:"intervalHours"`
	RetentionCount  int  `json:"retentionCount"`
}

func defaultBackupConfig() backupConfig {
	return backupConfig{IntervalHours: 24, RetentionCount: 7}
}

func (c backupConfig) normalized() backupConfig {
	if c.IntervalHours < 1 || c.IntervalHours > 8760 {
		c.IntervalHours = defaultBackupConfig().IntervalHours
	}
	if c.RetentionCount < minRetention || c.RetentionCount > maxRetention {
		c.RetentionCount = defaultBackupConfig().RetentionCount
	}
	return c
}

type Backup struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	Origin         string    `json:"origin"`
	FormatVersion  int       `json:"formatVersion"`
	SchemaVersion  int       `json:"schemaVersion"`
	Bytes          int64     `json:"bytes"`
	DatabaseBytes  int64     `json:"databaseBytes"`
	FilesBytes     int64     `json:"filesBytes"`
	DatabaseSHA256 string    `json:"databaseSha256"`
	Verified       bool      `json:"verified"`
	ServerVersion  string    `json:"serverVersion,omitempty"`
	Included       []string  `json:"included"`
	Excluded       []string  `json:"excluded"`
	RollbackFor    string    `json:"rollbackFor,omitempty"`
	Problem        string    `json:"problem,omitempty"`
}

var backupIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validBackupID(id string) bool { return backupIDPattern.MatchString(id) }

func newBackupID(now time.Time) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return now.UTC().Format("20060102T150405Z") + "-" + strconv.FormatInt(now.UnixNano()%1000000000, 36)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random)
}

func viewOfManifest(value manifest, verified bool) Backup {
	return Backup{
		ID: value.ID, CreatedAt: value.CreatedAt, Origin: value.Origin, FormatVersion: value.FormatVersion,
		SchemaVersion: value.SchemaVersion, Bytes: value.DatabaseBytes + value.FilesBytes,
		DatabaseBytes: value.DatabaseBytes, FilesBytes: value.FilesBytes, DatabaseSHA256: value.DatabaseSHA256,
		Verified: verified, ServerVersion: value.ServerVersion, Included: value.Included,
		Excluded: value.Excluded, RollbackFor: value.RollbackFor,
	}
}

func (s *Service) backupDir(id string) string { return filepath.Join(s.backupRoot, id) }

func (s *Service) CreateBackup(ctx context.Context, origin string) (Backup, error) {
	if origin != "manual" && origin != "scheduled" {
		origin = "manual"
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	release, err := s.acquireMaintenanceLock(ctx)
	if err != nil {
		return Backup{}, err
	}
	defer release()
	return s.createBackupLocked(ctx, origin, "")
}

func (s *Service) createBackupLocked(ctx context.Context, origin, rollbackFor string) (Backup, error) {
	if err := s.requireCompatibleTools(ctx); err != nil {
		return Backup{}, err
	}
	env, err := s.pgEnv("backup")
	if err != nil {
		return Backup{}, err
	}
	dumpPath, err := s.pgDumpPath()
	if err != nil {
		return Backup{}, err
	}
	restorePath, err := s.pgRestorePath()
	if err != nil {
		return Backup{}, err
	}
	schemaVersion, err := s.schemaVersion(ctx)
	if err != nil {
		return Backup{}, err
	}
	serverVersion, err := s.serverVersion(ctx)
	if err != nil {
		return Backup{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, s.options.BackupTimeout)
	defer cancel()
	id := newBackupID(time.Now())
	tempDir := filepath.Join(s.backupRoot, ".tmp-"+id)
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return Backup{}, errors.New("operations: the backup directory could not be prepared")
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tempDir)
		}
	}()
	dumpFile := filepath.Join(tempDir, backupDatabaseName)
	output, err := runTool(runCtx, dumpPath, env,
		"--format=custom", "--no-owner", "--no-privileges", "--no-password", "--file="+dumpFile)
	if err != nil {
		message := sanitizeToolError(output, s.databaseURL)
		s.recordBackupFailure(ctx, "Database dump failed: "+message)
		return Backup{}, errors.New("operations: the database dump failed: " + message)
	}
	if err := os.Chmod(dumpFile, 0o600); err != nil {
		return Backup{}, errors.New("operations: the database dump could not be secured")
	}
	listOutput, err := runTool(runCtx, restorePath, env, "--list", dumpFile)
	if err != nil {
		message := sanitizeToolError(listOutput, s.databaseURL)
		s.recordBackupFailure(ctx, "Database dump validation failed: "+message)
		return Backup{}, errors.New("operations: the database dump could not be validated: " + message)
	}
	if err := validateTOC(parseTOC(listOutput)); err != nil {
		s.recordBackupFailure(ctx, "Database dump validation failed.")
		return Backup{}, err
	}
	databaseSum, databaseBytes, err := hashFile(dumpFile)
	if err != nil {
		return Backup{}, errors.New("operations: the database dump could not be read")
	}
	filesPath := filepath.Join(tempDir, backupFilesName)
	if _, err := s.writeFilesArchive(filesPath); err != nil {
		s.recordBackupFailure(ctx, "Configuration files could not be archived.")
		return Backup{}, err
	}
	filesSum, filesBytes, err := hashFile(filesPath)
	if err != nil {
		return Backup{}, errors.New("operations: the configuration archive could not be read")
	}
	value := manifest{
		ID: id, Format: backupFormatName, FormatVersion: backupFormatVersion, SchemaVersion: schemaVersion,
		CreatedAt: time.Now().UTC(), Origin: origin, DatabaseSHA256: databaseSum, DatabaseBytes: databaseBytes,
		FilesSHA256: filesSum, FilesBytes: filesBytes, ServerVersion: serverVersion,
		Included: []string{
			"PostgreSQL database: catalogs, configuration, permissions, task history and stored credentials",
			"Configuration files from the data directory",
		},
		Excluded: []string{
			"Media files and library roots", "Downloads, posters and probe caches", "Application binaries and logs",
		},
		RollbackFor: rollbackFor,
	}
	if err := writeManifest(tempDir, value); err != nil {
		return Backup{}, err
	}
	if err := os.Rename(tempDir, s.backupDir(id)); err != nil {
		return Backup{}, errors.New("operations: the backup could not be stored")
	}
	committed = true
	message := "Backup " + id + " created (" + origin + ")."
	if rollbackFor != "" {
		message = "Rollback backup " + id + " created before restoring " + rollbackFor + "."
	}
	if err := s.insertEvent(ctx, Event{Kind: KindBackup, Severity: severityInfo, Source: "backups", Message: message, Ref: id}); err != nil {
		s.logger.Debug("operations: backup event could not be recorded")
	}
	s.logger.Info("operations: backup created", "backup", id, "origin", origin, "bytes", value.DatabaseBytes)
	if origin != "rollback" {
		s.pruneBackups(ctx)
	}
	return viewOfManifest(value, true), nil
}

func (s *Service) recordBackupFailure(ctx context.Context, message string) {
	s.logger.Warn("operations: backup failed", "error", truncate(message, 200))
	if err := s.insertEvent(ctx, Event{Kind: KindBackupFailed, Severity: severityWarning, Source: "backups", Message: message}); err != nil {
		s.logger.Debug("operations: backup failure could not be recorded")
	}
}

func (s *Service) ListBackups(ctx context.Context) ([]Backup, error) {
	entries, err := os.ReadDir(s.backupRoot)
	if err != nil {
		return nil, errors.New("operations: the backup directory could not be read")
	}
	backups := make([]Backup, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !validBackupID(name) {
			s.logger.Warn("operations: ignoring an unexpected entry in the backup directory")
			continue
		}
		value, err := loadManifest(filepath.Join(s.backupRoot, name))
		if err != nil {
			s.logger.Warn("operations: a backup manifest could not be read", "backup", name)
			continue
		}
		backups = append(backups, viewOfManifest(value, false))
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	return backups, nil
}

// Backup verifies both checksums; a damaged archive is reported instead of hidden.
func (s *Service) Backup(ctx context.Context, id string) (Backup, error) {
	if !validBackupID(id) {
		return Backup{}, ErrNotFound
	}
	dir := s.backupDir(id)
	value, err := loadManifest(dir)
	if err != nil {
		return Backup{}, err
	}
	view := viewOfManifest(value, false)
	if err := verifyBackup(dir, value); err != nil {
		view.Problem = err.Error()
		return view, nil
	}
	view.Verified = true
	return view, nil
}

func (s *Service) DeleteBackup(ctx context.Context, id string) error {
	if !validBackupID(id) {
		return ErrNotFound
	}
	if s.maintenance.Load() {
		return ConflictError("a restore is running; delete backups after it finishes")
	}
	dir := s.backupDir(id)
	if _, err := os.Stat(dir); err != nil {
		return ErrNotFound
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	release, err := s.acquireMaintenanceLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("operations: the backup could not be deleted")
	}
	if err := s.insertEvent(ctx, Event{Kind: KindBackup, Severity: severityInfo, Source: "backups", Message: "Backup " + id + " deleted.", Ref: id}); err != nil {
		s.logger.Debug("operations: backup event could not be recorded")
	}
	return nil
}

func (s *Service) DownloadBackup(ctx context.Context, id string, writer io.Writer) error {
	if !validBackupID(id) {
		return ErrNotFound
	}
	if _, err := loadManifest(s.backupDir(id)); err != nil {
		return err
	}
	return writeBackupArchive(s.backupDir(id), writer)
}

// ImportBackup validates an uploaded tar or zip archive and stores it without executing any SQL.
func (s *Service) ImportBackup(ctx context.Context, source io.Reader, limit int64) (Backup, error) {
	if limit <= 0 || limit > maxArchiveBytes {
		limit = maxArchiveBytes
	}
	if err := s.requireCompatibleTools(ctx); err != nil {
		return Backup{}, err
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	release, err := s.acquireMaintenanceLock(ctx)
	if err != nil {
		return Backup{}, err
	}
	defer release()
	upload, err := os.CreateTemp(s.backupRoot, ".upload-")
	if err != nil {
		return Backup{}, errors.New("operations: the upload could not be stored")
	}
	defer os.Remove(upload.Name())
	written, err := io.Copy(upload, io.LimitReader(source, limit+1))
	closeErr := upload.Close()
	if err != nil || closeErr != nil {
		return Backup{}, errors.New("operations: the upload could not be stored")
	}
	if written > limit {
		return Backup{}, InvalidError("the backup upload is too large")
	}
	tempDir := filepath.Join(s.backupRoot, ".import")
	os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return Backup{}, errors.New("operations: the import directory could not be prepared")
	}
	defer os.RemoveAll(tempDir)
	if err := extractBackupArchive(upload.Name(), tempDir); err != nil {
		return Backup{}, err
	}
	value, err := loadManifest(tempDir)
	if err != nil {
		return Backup{}, err
	}
	schemaVersion, err := s.schemaVersion(ctx)
	if err != nil {
		return Backup{}, err
	}
	if value.SchemaVersion > schemaVersion {
		return Backup{}, InvalidError("the backup was created by a newer schema version and cannot be imported")
	}
	if err := verifyBackup(tempDir, value); err != nil {
		return Backup{}, err
	}
	restorePath, err := s.pgRestorePath()
	if err != nil {
		return Backup{}, err
	}
	env, err := s.pgEnv("verify")
	if err != nil {
		return Backup{}, err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, s.options.BackupTimeout)
	defer cancel()
	output, err := runTool(verifyCtx, restorePath, env, "--list", filepath.Join(tempDir, backupDatabaseName))
	if err != nil {
		return Backup{}, InvalidError("the backup database dump is not readable: " + sanitizeToolError(output, s.databaseURL))
	}
	if err := validateTOC(parseTOC(output)); err != nil {
		return Backup{}, err
	}
	finalDir := s.backupDir(value.ID)
	if _, err := os.Stat(finalDir); err == nil {
		return Backup{}, ConflictError("a backup with this identifier already exists")
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return Backup{}, errors.New("operations: the backup could not be stored")
	}
	if err := s.insertEvent(ctx, Event{Kind: KindBackup, Severity: severityInfo, Source: "backups", Message: "Backup " + value.ID + " imported.", Ref: value.ID}); err != nil {
		s.logger.Debug("operations: backup event could not be recorded")
	}
	return viewOfManifest(value, true), nil
}

func (s *Service) backupConfigSnapshot() backupConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.backups
}

func (s *Service) UpdateBackupConfig(ctx context.Context, update backupConfig) (backupConfig, error) {
	if update.IntervalHours < 1 || update.IntervalHours > 8760 {
		return backupConfig{}, InvalidError("backup interval must be between 1 and 8760 hours")
	}
	if update.RetentionCount < minRetention || update.RetentionCount > maxRetention {
		return backupConfig{}, InvalidError("backup retention must be between 1 and 200 backups")
	}
	body, err := json.Marshal(update)
	if err != nil {
		return backupConfig{}, InvalidError("backup settings could not be encoded")
	}
	if err := s.saveConfigData(ctx, "backups", body); err != nil {
		return backupConfig{}, err
	}
	s.configMu.Lock()
	s.backups = update
	s.configMu.Unlock()
	return update, nil
}

func (s *Service) maybeScheduleBackup() {
	config := s.backupConfigSnapshot()
	if !config.ScheduleEnabled || s.maintenance.Load() {
		return
	}
	interval := time.Duration(config.IntervalHours) * time.Hour
	s.scheduleMu.Lock()
	if time.Since(s.scheduleAttempt) < interval {
		s.scheduleMu.Unlock()
		return
	}
	s.scheduleMu.Unlock()
	last, err := s.lastBackupAt("scheduled")
	if err != nil {
		return
	}
	if time.Since(last) < interval {
		return
	}
	if !s.backupMu.TryLock() {
		return
	}
	s.scheduleMu.Lock()
	s.scheduleAttempt = time.Now()
	s.scheduleMu.Unlock()
	go func() {
		defer s.backupMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), s.options.BackupTimeout+time.Minute)
		defer cancel()
		release, err := s.acquireMaintenanceLock(ctx)
		if err != nil {
			s.logger.Warn("operations: scheduled backup could not start", "error", err.Error())
			return
		}
		defer release()
		if _, err := s.createBackupLocked(ctx, "scheduled", ""); err != nil {
			s.logger.Warn("operations: scheduled backup failed", "error", err.Error())
		}
	}()
}

func (s *Service) lastBackupAt(origin string) (time.Time, error) {
	backups, err := s.ListBackups(context.Background())
	if err != nil {
		return time.Time{}, err
	}
	for _, backup := range backups {
		if backup.Origin == origin {
			return backup.CreatedAt, nil
		}
	}
	return time.Time{}, nil
}

// pruneBackups keeps the newest automatic and manual backups; rollback copies are never pruned.
func (s *Service) pruneBackups(ctx context.Context) {
	config := s.backupConfigSnapshot()
	backups, err := s.ListBackups(ctx)
	if err != nil {
		return
	}
	kept := 0
	for _, backup := range backups {
		if backup.Origin != "manual" && backup.Origin != "scheduled" {
			continue
		}
		kept++
		if kept <= config.RetentionCount {
			continue
		}
		if err := os.RemoveAll(s.backupDir(backup.ID)); err != nil {
			s.logger.Warn("operations: a backup could not be pruned", "backup", backup.ID)
			continue
		}
		if err := s.insertEvent(ctx, Event{
			Kind: KindBackup, Severity: severityInfo, Source: "backups",
			Message: "Backup " + backup.ID + " removed by retention.", Ref: backup.ID,
		}); err != nil {
			s.logger.Debug("operations: backup event could not be recorded")
		}
	}
}

// acquireMaintenanceLock holds one cross-process advisory lock for the duration of a backup or restore.
func (s *Service) acquireMaintenanceLock(ctx context.Context) (func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, maintenanceWait)
	defer cancel()
	conn, err := s.pool.Acquire(waitCtx)
	if err != nil {
		return nil, errors.New("operations: the database is unavailable")
	}
	for {
		var locked bool
		if err := conn.QueryRow(waitCtx, `SELECT pg_try_advisory_lock($1)`, maintenanceLock).Scan(&locked); err != nil {
			conn.Release()
			return nil, errors.New("operations: the maintenance lock could not be acquired")
		}
		if locked {
			return func() {
				unlockCtx, unlockCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer unlockCancel()
				if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, maintenanceLock); err != nil {
					s.logger.Warn("operations: the maintenance lock could not be released")
				}
				conn.Release()
			}, nil
		}
		select {
		case <-waitCtx.Done():
			conn.Release()
			return nil, ConflictError("another backup or restore is still running")
		case <-time.After(maintenanceRetry):
		}
	}
}
