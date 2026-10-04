package downloads

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Distinct advisory lock keys; the migration lock is transaction-scoped.
	migrationLock   int64 = 0x436F6E7374656C6C
	coordinatorLock int64 = migrationLock + 1
	settingsLock    int64 = migrationLock + 2
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbError("apply migrations", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
		return dbError("apply migrations", err)
	}
	if _, err := tx.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return dbError("apply migrations", err)
	}
	var current int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return dbError("apply migrations", err)
	}
	migrations, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + migration.name)
		if err != nil {
			return fmt.Errorf("downloads: read migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return dbError("apply migration "+strconv.Itoa(migration.version), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, migration.version); err != nil {
			return dbError("apply migrations", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("apply migrations", err)
	}
	return nil
}

type migrationFile struct {
	version int
	name    string
}

func migrationFiles() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("downloads: read migrations: %w", err)
	}
	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, convErr := strconv.Atoi(prefix)
		if !ok || convErr != nil || version < 1 {
			return nil, fmt.Errorf("downloads: migration %q has no version prefix", entry.Name())
		}
		files = append(files, migrationFile{version: version, name: entry.Name()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}
