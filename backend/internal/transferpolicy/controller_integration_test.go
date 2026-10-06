package transferpolicy_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
)

// testSchema creates an isolated schema so policy tests never touch real tables.
func testSchema(t *testing.T) *pgxpool.Pool {
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
	schema := "transferpolicy_test_" + strings.ToLower(rand.Text())
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
	config.ConnConfig.RuntimeParams["application_name"] = schema
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
	return pool
}

// applyMigrations mirrors the shared downloads migration runner from the test side.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "downloads", "migrations", "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot find the shared migrations: %v", err)
	}
	sort.Strings(paths)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("cannot create schema_migrations: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read migration %s: %v", filepath.Base(path), err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("cannot apply migration %s: %v", filepath.Base(path), err)
		}
	}
}

// testController applies the shared downloads migrations and returns a controller over them.
func testController(t *testing.T) (*transferpolicy.Controller, *pgxpool.Pool) {
	t.Helper()
	pool := testSchema(t)
	applyMigrations(t, pool)
	controller, err := transferpolicy.New(context.Background(), pool)
	if err != nil {
		t.Fatalf("transferpolicy.New: %v", err)
	}
	return controller, pool
}

func richConfig() transferpolicy.Config {
	return transferpolicy.Config{
		Timezone: "Europe/Berlin", ConnectionMbps: 100,
		Limit:           transferpolicy.Limit{Mode: transferpolicy.ModePercent, Value: 20},
		ScheduleEnabled: true, OutsideSchedule: transferpolicy.OutsideNormal,
		Windows: []transferpolicy.Window{
			{ID: "night", Name: "Night", Days: []int{0, 6}, Start: "22:00", End: "02:00", Action: transferpolicy.ActionPaused,
				Limit: transferpolicy.Limit{Mode: transferpolicy.ModeUnlimited}},
			{ID: "evening", Name: "Evening", Days: []int{1, 2, 3, 4, 5}, Start: "18:00", End: "20:00", Action: transferpolicy.ActionLimited,
				Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 512}},
		},
	}
}

func TestControllerDefaultsPersistAndRestore(t *testing.T) {
	controller, pool := testController(t)
	ctx := context.Background()
	snapshot := controller.Snapshot()
	if want := transferpolicy.Default(); !reflect.DeepEqual(snapshot.Config, want) {
		t.Fatalf("default config = %+v, want %+v", snapshot.Config, want)
	}
	if snapshot.Effective.Paused || snapshot.Effective.LimitBytesPerSecond != 0 || snapshot.Effective.NextChange != nil {
		t.Fatalf("default effective = %+v", snapshot.Effective)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_policy`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("policy rows = %d, %v", rows, err)
	}

	updated, err := controller.Update(ctx, richConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Config, richConfig()) {
		t.Fatalf("updated config = %+v", updated.Config)
	}
	if updated.Effective.NextChange == nil {
		t.Fatalf("expected a scheduled next change: %+v", updated.Effective)
	}
	restored, err := transferpolicy.New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	again := restored.Snapshot()
	if !reflect.DeepEqual(again.Config, updated.Config) {
		t.Fatalf("restored config = %+v, want %+v", again.Config, updated.Config)
	}
	if again.Effective.Paused != updated.Effective.Paused || again.Effective.LimitBytesPerSecond != updated.Effective.LimitBytesPerSecond {
		t.Fatalf("restored effective = %+v, want %+v", again.Effective, updated.Effective)
	}
	if again.Effective.NextChange == nil {
		t.Fatalf("restored effective lost the next change: %+v", again.Effective)
	}
}

func TestControllerSetPausedKeepsSchedule(t *testing.T) {
	controller, pool := testController(t)
	ctx := context.Background()
	config := richConfig()
	if _, err := controller.Update(ctx, config); err != nil {
		t.Fatal(err)
	}
	before := controller.Limiter().Limit()
	if before <= 0 {
		t.Fatalf("policy left an unusable limiter rate: %v", before)
	}
	paused, err := controller.SetPaused(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.Config.Paused || !paused.Effective.Paused || paused.Effective.Reason != "paused manually" {
		t.Fatalf("paused snapshot = %+v", paused)
	}
	expectedPaused := config
	expectedPaused.Paused = true
	if !reflect.DeepEqual(paused.Config, expectedPaused) {
		t.Fatalf("manual pause changed the config = %+v", paused.Config)
	}
	// A pause must not zero the shared rate: torrent clients cannot run with a zero limit.
	if got := controller.Limiter().Limit(); got != before {
		t.Fatalf("limiter while paused = %v, want the usable rate %v", got, before)
	}
	restored, err := transferpolicy.New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if after := restored.Snapshot(); !after.Config.Paused || !reflect.DeepEqual(after.Config, expectedPaused) {
		t.Fatalf("restored paused config = %+v", after.Config)
	}

	resumed, err := controller.SetPaused(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Config.Paused {
		t.Fatalf("resumed snapshot is still paused: %+v", resumed)
	}
	if !reflect.DeepEqual(resumed.Config, config) {
		t.Fatalf("resume changed the config = %+v", resumed.Config)
	}
	want := transferpolicy.Evaluate(config, time.Now())
	if resumed.Effective.Paused != want.Paused || resumed.Effective.LimitBytesPerSecond != want.LimitBytesPerSecond {
		t.Fatalf("resumed effective = %+v, want %+v", resumed.Effective, want)
	}
}

func TestControllerRejectsInvalidUpdate(t *testing.T) {
	controller, pool := testController(t)
	ctx := context.Background()
	valid := richConfig()
	if _, err := controller.Update(ctx, valid); err != nil {
		t.Fatal(err)
	}
	overlapping := valid
	overlapping.Windows = append(append([]transferpolicy.Window{}, valid.Windows...), transferpolicy.Window{
		ID: "clash", Days: []int{1}, Start: "19:00", End: "21:00", Action: transferpolicy.ActionPaused,
	})
	if _, err := controller.Update(ctx, overlapping); !errors.Is(err, transferpolicy.ErrInvalid) {
		t.Fatalf("overlapping update error = %v, want ErrInvalid", err)
	}
	badZone := valid
	badZone.Timezone = "Nowhere/Land"
	if _, err := controller.Update(ctx, badZone); !errors.Is(err, transferpolicy.ErrInvalid) {
		t.Fatalf("timezone update error = %v, want ErrInvalid", err)
	}
	restored, err := transferpolicy.New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := restored.Snapshot(); !reflect.DeepEqual(snapshot.Config, valid) {
		t.Fatalf("invalid update reached the database: %+v", snapshot.Config)
	}
}

func TestControllerConcurrentSetPausedKeepsWindows(t *testing.T) {
	controller, pool := testController(t)
	ctx := context.Background()
	config := richConfig()
	if _, err := controller.Update(ctx, config); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, write := range []func() error{
		func() error { _, err := controller.Update(ctx, config); return err },
		func() error { _, err := controller.SetPaused(ctx, true); return err },
	} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- write()
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	restored, err := transferpolicy.New(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := restored.Snapshot(); !reflect.DeepEqual(snapshot.Config.Windows, config.Windows) {
		t.Fatalf("concurrent writes lost windows: %+v", snapshot.Config.Windows)
	}
}

func TestControllerSharesOneLimiter(t *testing.T) {
	controller, _ := testController(t)
	ctx := context.Background()
	limiter := controller.Limiter()
	if limiter.Burst() != 1<<20 {
		t.Fatalf("limiter burst = %d, want 1 MiB", limiter.Burst())
	}
	limited := transferpolicy.Config{Timezone: "UTC", Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 512}}
	if _, err := controller.Update(ctx, limited); err != nil {
		t.Fatal(err)
	}
	if limiter != controller.Limiter() || limiter.Limit() != rate.Limit(512*1024) {
		t.Fatalf("limiter after update = %v", limiter.Limit())
	}
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if limiter.Limit() != rate.Limit(512*1024) {
		t.Fatalf("limiter while paused = %v, want the usable 512 KiB/s", limiter.Limit())
	}
	if _, err := controller.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	if limiter.Limit() != rate.Limit(512*1024) {
		t.Fatalf("limiter after resume = %v", limiter.Limit())
	}
	if _, err := controller.Update(ctx, transferpolicy.Config{Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if limiter.Limit() != rate.Inf {
		t.Fatalf("limiter after an unlimited update = %v", limiter.Limit())
	}
}

func TestControllerFailedPersistLeavesStateUnchanged(t *testing.T) {
	controller, _ := testController(t)
	ctx := context.Background()
	first := transferpolicy.Config{Timezone: "UTC", Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 256}, OutsideSchedule: transferpolicy.OutsideNormal, Windows: []transferpolicy.Window{}}
	if _, err := controller.Update(ctx, first); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	second := transferpolicy.Config{Timezone: "UTC", Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 999}, Windows: []transferpolicy.Window{}}
	if _, err := controller.Update(canceled, second); err == nil {
		t.Fatal("Update with a cancelled context: want an error")
	}
	if _, err := controller.SetPaused(canceled, true); err == nil {
		t.Fatal("SetPaused with a cancelled context: want an error")
	}
	if snapshot := controller.Snapshot(); !reflect.DeepEqual(snapshot.Config, first) {
		t.Fatalf("failed persist changed the config: %+v", snapshot.Config)
	}
	if controller.Limiter().Limit() != rate.Limit(256*1024) {
		t.Fatalf("failed persist changed the limiter: %v", controller.Limiter().Limit())
	}
}

func TestControllerRunBlocksUntilCancelled(t *testing.T) {
	controller, _ := testController(t)
	ctx := context.Background()
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		controller.Run(runCtx)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for controller.Allowed() {
		if time.Now().After(deadline) {
			t.Fatalf("Run did not apply the pause: limiter = %v, allowed = %v", controller.Limiter().Limit(), controller.Allowed())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := controller.Limiter().Limit(); got != rate.Inf {
		t.Fatalf("a pause must keep the limiter usable, got %v", got)
	}
	select {
	case <-done:
		t.Fatal("Run returned before the context was cancelled")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestNewRequiresMigrations(t *testing.T) {
	pool := testSchema(t)
	if _, err := transferpolicy.New(context.Background(), pool); err == nil {
		t.Fatal("New without the policy table: want a clear error")
	}
}
