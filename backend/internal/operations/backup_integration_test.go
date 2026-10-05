package operations

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupLifecycleImportAndTamperDetection(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	service := newTestService(t, pool, Options{DataDir: t.TempDir(), DatabaseURL: databaseURL})
	requireBackupTools(t, service)
	ctx := context.Background()
	if err := service.RecordProviderFailure(ctx, "indexer", "search request failed"); err != nil {
		t.Fatalf("record provider failure: %v", err)
	}

	backup, err := service.CreateBackup(ctx, "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if backup.SchemaVersion < 12 || backup.Origin != "manual" || !backup.Verified {
		t.Fatalf("unexpected backup view: %+v", backup)
	}
	if len(backup.Included) == 0 || len(backup.Excluded) == 0 {
		t.Fatalf("a backup must describe included and excluded data: %+v", backup)
	}
	for _, name := range []string{backupManifestName, backupDatabaseName, backupFilesName} {
		info, err := os.Stat(filepath.Join(service.backupDir(backup.ID), name))
		if err != nil {
			t.Fatalf("backup is missing %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s must be readable only by the owner, got %v", name, info.Mode().Perm())
		}
	}
	detail, err := service.Backup(ctx, backup.ID)
	if err != nil || !detail.Verified || detail.Problem != "" {
		t.Fatalf("a fresh backup must verify: %+v (%v)", detail, err)
	}
	listed, err := service.ListBackups(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != backup.ID {
		t.Fatalf("unexpected backup list: %+v (%v)", listed, err)
	}

	var archive bytes.Buffer
	if err := service.DownloadBackup(ctx, backup.ID, &archive); err != nil {
		t.Fatalf("download backup: %v", err)
	}
	if entries := tarEntryNames(t, archive.Bytes()); strings.Join(entries, ",") != backupManifestName+","+backupDatabaseName+","+backupFilesName {
		t.Fatalf("unexpected archive entries: %v", entries)
	}

	if err := os.WriteFile(filepath.Join(service.backupDir(backup.ID), backupDatabaseName), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper with the dump: %v", err)
	}
	damaged, err := service.Backup(ctx, backup.ID)
	if err != nil || damaged.Verified || damaged.Problem == "" {
		t.Fatalf("tampering must be reported: %+v (%v)", damaged, err)
	}
	plan, err := service.PreviewRestore(ctx, backup.ID)
	if err != nil || plan.ChecksumVerified || len(plan.Warnings) == 0 {
		t.Fatalf("a damaged backup must not preview as restorable: %+v (%v)", plan, err)
	}
	if err := service.DeleteBackup(ctx, backup.ID); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	if err := service.DeleteBackup(ctx, backup.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing backup must report not found, got %v", err)
	}

	imported, err := service.ImportBackup(ctx, bytes.NewReader(archive.Bytes()), 0)
	if err != nil {
		t.Fatalf("import backup: %v", err)
	}
	if imported.ID != backup.ID || !imported.Verified {
		t.Fatalf("unexpected imported backup: %+v", imported)
	}
	if _, err := service.ImportBackup(ctx, bytes.NewReader(archive.Bytes()), 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("importing the same backup twice must conflict, got %v", err)
	}
	if err := service.DeleteBackup(ctx, backup.ID); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	zipped, err := service.ImportBackup(ctx, bytes.NewReader(zipFromTar(t, archive.Bytes())), 0)
	if err != nil {
		t.Fatalf("import zip backup: %v", err)
	}
	if zipped.ID != backup.ID || !zipped.Verified {
		t.Fatalf("unexpected imported zip backup: %+v", zipped)
	}

	hostile := tarArchive(t, []archiveEntry{
		{name: backupManifestName, body: []byte("{}")},
		{name: backupDatabaseName, body: []byte("dump")},
		{name: backupFilesName, body: emptyTar()},
		{name: "payload.sh", body: []byte("echo unsafe")},
	})
	if _, err := service.ImportBackup(ctx, bytes.NewReader(hostile), 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an archive with unexpected entries must be refused, got %v", err)
	}
	oversized := bytes.Repeat([]byte("x"), 1024)
	if _, err := service.ImportBackup(ctx, bytes.NewReader(oversized), 128); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an oversized upload must be refused, got %v", err)
	}
}

func tarEntryNames(t *testing.T, body []byte) []string {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(body))
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		names = append(names, header.Name)
	}
	return names
}

func zipFromTar(t *testing.T, body []byte) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(body))
	var entries []archiveEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read archive entry: %v", err)
		}
		entries = append(entries, archiveEntry{name: header.Name, body: content})
	}
	return zipArchive(t, entries)
}

func TestScheduledBackupRunsOncePerInterval(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	service := newTestService(t, pool, Options{DataDir: t.TempDir(), DatabaseURL: databaseURL})
	requireBackupTools(t, service)
	ctx := context.Background()
	if _, err := service.UpdateBackupConfig(ctx, backupConfig{ScheduleEnabled: true, IntervalHours: 1, RetentionCount: 5}); err != nil {
		t.Fatalf("update backup config: %v", err)
	}
	service.maybeScheduleBackup()
	waitFor(t, func() bool {
		backups, err := service.ListBackups(context.Background())
		return err == nil && len(backups) == 1 && backups[0].Origin == "scheduled"
	})
	service.maybeScheduleBackup()
	time.Sleep(200 * time.Millisecond)
	backups, err := service.ListBackups(ctx)
	if err != nil || len(backups) != 1 {
		t.Fatalf("the schedule must run once per interval, got %+v (%v)", backups, err)
	}
}

func TestRestoreReplacesDatabaseAndKeepsRollbackCopy(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	ctx := context.Background()
	var (
		quiesced = false
		resumed  = false
	)
	options := Options{
		DataDir: t.TempDir(), DatabaseURL: databaseURL, AllowLiveRestore: true, AutoRestart: true,
		Quiesce: func(context.Context) error { quiesced = true; return nil },
		Resume:  func(context.Context) error { resumed = true; return nil },
	}
	service := newTestService(t, pool, options)
	requireBackupTools(t, service)
	if _, err := pool.Exec(ctx, `CREATE TABLE restore_probe (id text PRIMARY KEY)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO restore_probe (id) VALUES ('kept')`); err != nil {
		t.Fatalf("insert probe row: %v", err)
	}
	backup, err := service.CreateBackup(ctx, "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM restore_probe`); err != nil {
		t.Fatalf("delete probe row: %v", err)
	}

	plan, err := service.PreviewRestore(ctx, backup.ID)
	if err != nil || !plan.ChecksumVerified || !plan.SchemaCompatible || !plan.WillCreateRollbackBackup || !plan.LiveRestoreEnabled || !plan.RequiresRestart || !plan.AutoRestart {
		t.Fatalf("unexpected restore plan: %+v (%v)", plan, err)
	}
	if _, err := service.Restore(ctx, backup.ID, "wrong"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a mismatched confirmation must be refused, got %v", err)
	}

	result, err := service.Restore(ctx, backup.ID, backup.ID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !result.RestartRequired || !result.AutoRestart || result.RollbackBackupID == "" || result.BackupID != backup.ID {
		t.Fatalf("unexpected restore result: %+v", result)
	}
	mentionsAutoRestart := false
	for _, note := range result.Notes {
		if strings.Contains(note, "automatically") {
			mentionsAutoRestart = true
		}
	}
	if !mentionsAutoRestart {
		t.Fatalf("an automatic restart must be reported in the notes: %+v", result.Notes)
	}
	if !quiesced || !resumed {
		t.Fatalf("automation must be paused and resumed around the restore (quiesced=%v resumed=%v)", quiesced, resumed)
	}
	var restored string
	if err := pool.QueryRow(ctx, `SELECT id FROM restore_probe`).Scan(&restored); err != nil || restored != "kept" {
		t.Fatalf("the restored database is missing the probe row: %q (%v)", restored, err)
	}
	rollback, err := service.Backup(ctx, result.RollbackBackupID)
	if err != nil || rollback.Origin != "rollback" || rollback.RollbackFor != backup.ID || !rollback.Verified {
		t.Fatalf("unexpected rollback copy: %+v (%v)", rollback, err)
	}
	logged, err := service.listEvents(ctx, eventFilter{kinds: []string{KindRestore}})
	if err != nil || len(logged) != 1 {
		t.Fatalf("the restore must be recorded: %+v (%v)", logged, err)
	}
}

func TestRestoreRequiresExplicitLiveRestoreOptIn(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	service := newTestService(t, pool, Options{DataDir: dataDir, DatabaseURL: databaseURL})
	requireBackupTools(t, service)
	backup, err := service.CreateBackup(ctx, "manual")
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if _, err := service.Restore(ctx, backup.ID, backup.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("restores must be refused without an opt-in, got %v", err)
	}
	withQuiesce := Options{DataDir: dataDir, DatabaseURL: databaseURL, AllowLiveRestore: true}
	other := newTestService(t, pool, withQuiesce)
	if _, err := other.Restore(ctx, backup.ID, backup.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("restores must be refused without a quiesce hook, got %v", err)
	}
}

func TestRestoreRefusesNewerSchemaVersion(t *testing.T) {
	pool, databaseURL := testDatabase(t)
	ctx := context.Background()
	service := newTestService(t, pool, Options{
		DataDir: t.TempDir(), DatabaseURL: databaseURL, AllowLiveRestore: true,
		Quiesce: func(context.Context) error { return nil }, Resume: func(context.Context) error { return nil },
	})
	requireBackupTools(t, service)
	id := "20990101T000000Z-abcdef99"
	value, database, files := testManifest(id, 999)
	writeBackupDir(t, service.backupDir(id), value, database, files)
	plan, err := service.PreviewRestore(ctx, id)
	if err != nil || plan.SchemaCompatible || len(plan.Warnings) == 0 {
		t.Fatalf("a newer schema version must be reported: %+v (%v)", plan, err)
	}
	if _, err := service.Restore(ctx, id, id); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restoring a newer schema version must be refused, got %v", err)
	}
}

func TestRetentionPrunesManualBackupsButKeepsRollbackCopies(t *testing.T) {
	pool, _ := testDatabase(t)
	service := newTestService(t, pool, Options{DataDir: t.TempDir()})
	ctx := context.Background()
	if _, err := service.UpdateBackupConfig(ctx, backupConfig{IntervalHours: 24, RetentionCount: 1}); err != nil {
		t.Fatalf("update backup config: %v", err)
	}
	now := time.Now().UTC()
	manual := []struct {
		id      string
		created time.Time
	}{
		{"20260101T000000Z-00000001", now.Add(-3 * time.Hour)},
		{"20260101T000000Z-00000002", now.Add(-2 * time.Hour)},
		{"20260101T000000Z-00000003", now.Add(-1 * time.Hour)},
	}
	for _, entry := range manual {
		value, database, files := testManifest(entry.id, 12)
		value.Origin = "manual"
		value.CreatedAt = entry.created
		writeBackupDir(t, service.backupDir(entry.id), value, database, files)
	}
	rollback, rollbackDatabase, rollbackFiles := testManifest("20260101T000000Z-00000004", 12)
	rollback.Origin = "rollback"
	rollback.CreatedAt = now.Add(-4 * time.Hour)
	writeBackupDir(t, service.backupDir(rollback.ID), rollback, rollbackDatabase, rollbackFiles)

	service.pruneBackups(ctx)
	remaining, err := service.ListBackups(ctx)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	kept := map[string]bool{}
	for _, backup := range remaining {
		kept[backup.ID] = true
	}
	if !kept["20260101T000000Z-00000003"] || !kept[rollback.ID] || len(remaining) != 2 {
		t.Fatalf("retention kept the wrong backups: %+v", remaining)
	}
}
