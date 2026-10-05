package torrents

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a reachable PostgreSQL server to run this test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("cannot create a pool from TEST_DATABASE_URL: %v", err)
	}
	schema := "torrents_unit_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("cannot create an isolated test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("TEST_DATABASE_URL is invalid: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("cannot create a pool for the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	for _, name := range []string{"011_torrents.sql", "015_torrent_processing.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "downloads", "migrations", name))
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("cannot apply %s: %v", name, err)
		}
	}
	return pool
}

func waitUntil(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// loadJob reads a job through Get so processing state is included.
func loadJob(t *testing.T, service *Service, id string) Job {
	t.Helper()
	detail, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("cannot load job: %v", err)
	}
	return detail.Job
}

// verifiedPayload writes an all-complete piece bitmap so the engine treats the payload as finished.
func verifiedPayload(t *testing.T, service *Service, id, infoHash string, pieces int, files map[string]string) {
	t.Helper()
	known := make(map[int]bool, pieces)
	for index := 0; index < pieces; index++ {
		known[index] = true
	}
	bits := encodePieces(known, pieces)
	if _, err := service.pool.Exec(context.Background(),
		`UPDATE torrent_jobs SET pieces_total = $2, pieces_done = $2, piece_bits = $3 WHERE id = $1`,
		id, pieces, bits); err != nil {
		t.Fatalf("cannot mark pieces verified: %v", err)
	}
	dir, err := service.dataDir(infoHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
