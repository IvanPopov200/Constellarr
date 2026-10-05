package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const quiesceTimeout = 60 * time.Second

type RestorePlan struct {
	Backup                   Backup   `json:"backup"`
	LiveRestoreEnabled       bool     `json:"liveRestoreEnabled"`
	QuiesceConfigured        bool     `json:"quiesceConfigured"`
	ChecksumVerified         bool     `json:"checksumVerified"`
	SchemaCompatible         bool     `json:"schemaCompatible"`
	CurrentSchemaVersion     int      `json:"currentSchemaVersion"`
	WillCreateRollbackBackup bool     `json:"willCreateRollbackBackup"`
	RequiresRestart          bool     `json:"requiresRestart"`
	AutoRestart              bool     `json:"autoRestart"`
	Warnings                 []string `json:"warnings,omitempty"`
}

type RestoreResult struct {
	BackupID         string   `json:"backupId"`
	RollbackBackupID string   `json:"rollbackBackupId,omitempty"`
	RestartRequired  bool     `json:"restartRequired"`
	AutoRestart      bool     `json:"autoRestart"`
	DurationSeconds  float64  `json:"durationSeconds"`
	Notes            []string `json:"notes,omitempty"`
}

func (s *Service) PreviewRestore(ctx context.Context, id string) (RestorePlan, error) {
	if !validBackupID(id) {
		return RestorePlan{}, ErrNotFound
	}
	value, err := loadManifest(s.backupDir(id))
	if err != nil {
		return RestorePlan{}, err
	}
	plan := RestorePlan{
		Backup: viewOfManifest(value, false), LiveRestoreEnabled: s.options.AllowLiveRestore,
		QuiesceConfigured: s.options.Quiesce != nil, WillCreateRollbackBackup: true,
		RequiresRestart: true, AutoRestart: s.options.AutoRestart,
	}
	if err := verifyBackup(s.backupDir(id), value); err != nil {
		plan.Warnings = append(plan.Warnings, err.Error())
	} else {
		plan.ChecksumVerified = true
		plan.Backup.Verified = true
	}
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return RestorePlan{}, err
	}
	plan.CurrentSchemaVersion = current
	plan.SchemaCompatible = value.SchemaVersion <= current
	if !plan.SchemaCompatible {
		plan.Warnings = append(plan.Warnings, "The backup was created by a newer schema version.")
	}
	if _, err := s.pgRestorePath(); err != nil {
		plan.Warnings = append(plan.Warnings, "pg_restore is not available, so the archive cannot be validated.")
	}
	if !plan.LiveRestoreEnabled {
		plan.Warnings = append(plan.Warnings, "Live restores are disabled; the database is never replaced automatically.")
	}
	return plan, nil
}

// Restore replaces the live database from a verified backup after pausing automation.
func (s *Service) Restore(ctx context.Context, id, confirm string) (RestoreResult, error) {
	started := time.Now()
	if !validBackupID(id) {
		return RestoreResult{}, ErrNotFound
	}
	if strings.TrimSpace(confirm) != id {
		return RestoreResult{}, InvalidError("type the backup identifier to confirm the restore")
	}
	value, err := loadManifest(s.backupDir(id))
	if err != nil {
		return RestoreResult{}, err
	}
	if !s.options.AllowLiveRestore {
		return RestoreResult{}, ConflictError("live restores are disabled; the database was not changed")
	}
	if s.options.Quiesce == nil {
		return RestoreResult{}, ConflictError("live restores need a Quiesce callback that stops automation")
	}
	// Admission happens before any blocking lock so a queued request cannot start a second restore.
	if !s.maintenance.CompareAndSwap(false, true) {
		return RestoreResult{}, ConflictError("another restore is already running")
	}
	defer s.maintenance.Store(false)
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	s.collectMu.Lock()
	defer s.collectMu.Unlock()
	if err := ctx.Err(); err != nil {
		return RestoreResult{}, ConflictError("the restore request was cancelled")
	}
	release, err := s.acquireMaintenanceLock(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	defer release()
	if err := s.requireCompatibleTools(ctx); err != nil {
		return RestoreResult{}, err
	}
	dumpFile := filepath.Join(s.backupDir(id), backupDatabaseName)
	if err := verifyBackup(s.backupDir(id), value); err != nil {
		return RestoreResult{}, err
	}
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return RestoreResult{}, err
	}
	if value.SchemaVersion > current {
		return RestoreResult{}, InvalidError("the backup was created by a newer schema version and cannot be restored")
	}
	restorePath, err := s.pgRestorePath()
	if err != nil {
		return RestoreResult{}, err
	}
	liveEnv, err := s.pgEnv("restore")
	if err != nil {
		return RestoreResult{}, err
	}
	listOutput, err := runTool(ctx, restorePath, liveEnv, "--list", dumpFile)
	if err != nil {
		return RestoreResult{}, InvalidError("the backup database dump is not readable: " + sanitizeToolError(listOutput, s.databaseURL))
	}
	if err := validateTOC(parseTOC(listOutput)); err != nil {
		return RestoreResult{}, err
	}
	quiesceCtx, cancelQuiesce := context.WithTimeout(ctx, quiesceTimeout)
	pauseErr := s.options.Quiesce(quiesceCtx)
	cancelQuiesce()
	if pauseErr != nil {
		return RestoreResult{}, errors.New("operations: automation could not be paused: " + truncate(pauseErr.Error(), 200))
	}
	var notes []string
	defer func() {
		resumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quiesceTimeout)
		defer cancel()
		if err := s.options.Resume(resumeCtx); err != nil {
			s.logger.Warn("operations: automation resume failed after restore", "error", truncate(err.Error(), 200))
		}
	}()
	rollback, err := s.createBackupLocked(ctx, "rollback", id)
	if err != nil {
		return RestoreResult{}, errors.New("operations: the rollback copy could not be created; the database was not changed")
	}
	notes = append(notes, "Rollback copy "+rollback.ID+" holds the database state from before this restore.")
	if !s.options.DisableRestoreVerification {
		if err := s.verifyInScratch(ctx, restorePath, dumpFile); err != nil {
			notes = append(notes, "The backup was not restored because verification failed.")
			return RestoreResult{}, err
		}
	}
	connection, err := parseConnection(s.databaseURL)
	if err != nil {
		return RestoreResult{}, ErrNotConfigured
	}
	restoreCtx, cancelRestore := context.WithTimeout(ctx, s.options.RestoreTimeout)
	defer cancelRestore()
	output, err := runTool(restoreCtx, restorePath, liveEnv,
		"--dbname="+connection.database, "--clean", "--if-exists", "--no-owner", "--no-privileges",
		"--exit-on-error", "--single-transaction", dumpFile)
	if err != nil {
		message := sanitizeToolError(output, s.databaseURL)
		notes = append(notes, "The restore failed inside a single transaction; the previous database state remains in place.")
		s.recordRestoreFailure(ctx, id, message)
		return RestoreResult{BackupID: id, RollbackBackupID: rollback.ID, Notes: notes}, errors.New("operations: the database restore failed: " + message)
	}
	if err := s.loadConfig(ctx); err != nil {
		notes = append(notes, "Restored settings could not be reloaded; restart the server.")
	}
	if err := s.insertEvent(ctx, Event{
		Kind: KindRestore, Severity: severityWarning, Source: "backups",
		Message: "Database restored from backup " + id + " (rollback copy " + rollback.ID + ").", Ref: id,
	}); err != nil {
		s.logger.Debug("operations: restore event could not be recorded")
	}
	s.logger.Warn("operations: database restored", "backup", id, "rollback", rollback.ID)
	notes = append(notes, s.restartNote())
	return RestoreResult{
		BackupID: id, RollbackBackupID: rollback.ID, RestartRequired: true, AutoRestart: s.options.AutoRestart,
		DurationSeconds: time.Since(started).Seconds(), Notes: notes,
	}, nil
}

func (s *Service) restartNote() string {
	if s.options.AutoRestart {
		return "The server restarts and reconnects automatically after this restore."
	}
	return "Restart the server so every service reconnects to the restored database."
}

func (s *Service) verifyInScratch(ctx context.Context, restorePath, dumpFile string) error {
	scratch := "constellarr_verify_" + randomHex(6)
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return errors.New("operations: the verification database could not be created")
	}
	defer conn.Release()
	identifier := pgx.Identifier{scratch}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		return errors.New("operations: the restore was refused because a verification database could not be created")
	}
	dropScratch := func() {
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := conn.Exec(dropCtx, "DROP DATABASE IF EXISTS "+identifier+" WITH (FORCE)"); err != nil {
			s.logger.Warn("operations: the verification database could not be removed")
		}
	}
	env, err := s.pgEnv("verify")
	if err != nil {
		dropScratch()
		return err
	}
	scratchEnv := replaceDatabase(env, scratch)
	verifyCtx, cancel := context.WithTimeout(ctx, s.options.RestoreTimeout)
	defer cancel()
	output, err := runTool(verifyCtx, restorePath, scratchEnv,
		"--dbname="+scratch, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", dumpFile)
	if err != nil {
		dropScratch()
		return InvalidError("the backup could not be restored into a verification database: " + sanitizeToolError(output, s.databaseURL))
	}
	if err := verifyScratchTables(ctx, s.databaseURL, scratch); err != nil {
		dropScratch()
		return err
	}
	dropScratch()
	return nil
}

func verifyScratchTables(ctx context.Context, databaseURL, database string) error {
	target, err := databaseDSN(databaseURL, database)
	if err != nil {
		return errors.New("operations: the verification database could not be checked")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(checkCtx, target)
	if err != nil {
		return errors.New("operations: the verification database could not be checked")
	}
	defer conn.Close(checkCtx)
	var present int
	if err := conn.QueryRow(checkCtx,
		`SELECT count(*) FROM pg_class WHERE relname = ANY($1)`, expectedTables).Scan(&present); err != nil {
		return errors.New("operations: the verification database could not be checked")
	}
	if present == 0 {
		return InvalidError("the backup was refused because the restored database has none of the expected tables")
	}
	return nil
}

// databaseDSN rebuilds the connection with another database name for verification only.
func databaseDSN(databaseURL, database string) (string, error) {
	config, err := parseConnection(databaseURL)
	if err != nil {
		return "", err
	}
	parts := []string{
		"host=" + quoteDSN(config.host), "port=" + quoteDSN(config.port),
		"user=" + quoteDSN(config.user), "dbname=" + quoteDSN(database),
	}
	if config.password != "" {
		parts = append(parts, "password="+quoteDSN(config.password))
	}
	if config.sslmode != "" {
		parts = append(parts, "sslmode="+quoteDSN(config.sslmode))
	}
	return strings.Join(parts, " "), nil
}

func quoteDSN(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func replaceDatabase(env []string, database string) []string {
	updated := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "PGDATABASE=") {
			updated = append(updated, "PGDATABASE="+database)
			continue
		}
		updated = append(updated, entry)
	}
	return updated
}

func randomHex(bytes int) string {
	random := make([]byte, bytes)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
}

func (s *Service) recordRestoreFailure(ctx context.Context, id, message string) {
	if err := s.insertEvent(ctx, Event{
		Kind: KindRestore, Severity: severityCritical, Source: "backups",
		Message: "Restore of backup " + id + " failed: " + message, Ref: id,
	}); err != nil {
		s.logger.Debug("operations: restore failure could not be recorded")
	}
}
