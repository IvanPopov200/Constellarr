package operations

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T, migrate bool) *pgxpool.Pool {
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
	schema := "operations_test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgIdent(schema)); err != nil {
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
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgIdent(schema)+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	if migrate {
		applyMigrations(t, pool)
	}
	return pool
}

func pgIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// applyMigrations mirrors the downloads migration runner, including its schema_migrations bookkeeping.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "downloads", "migrations", "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot find migrations: %v", err)
	}
	sort.Strings(paths)
	if _, err := pool.Exec(context.Background(),
		`CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("cannot create schema_migrations: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read migration %s: %v", filepath.Base(path), err)
		}
		if _, err := pool.Exec(context.Background(), string(body)); err != nil {
			t.Fatalf("cannot apply migration %s: %v", filepath.Base(path), err)
		}
		prefix, _, ok := strings.Cut(filepath.Base(path), "_")
		version, convErr := strconv.Atoi(prefix)
		if !ok || convErr != nil {
			t.Fatalf("migration %s has no version prefix", filepath.Base(path))
		}
		if _, err := pool.Exec(context.Background(), `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			t.Fatalf("cannot record migration %s: %v", filepath.Base(path), err)
		}
	}
}

func testDatabase(t *testing.T) (*pgxpool.Pool, string) {
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
	name := "constellarr_ops_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgIdent(name)); err != nil {
		admin.Close()
		t.Skipf("cannot create an isolated test database: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("TEST_DATABASE_URL is invalid: %v", err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("cannot create a pool for the test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP DATABASE IF EXISTS "+pgIdent(name)+" WITH (FORCE)"); err != nil {
			t.Errorf("cannot drop the test database: %v", err)
		}
		admin.Close()
	})
	applyMigrations(t, pool)
	target, err := databaseDSN(databaseURL, name)
	if err != nil {
		t.Skip("TEST_DATABASE_URL cannot be reused for PostgreSQL client tools")
	}
	return pool, target
}

func newTestService(t *testing.T, pool *pgxpool.Pool, options Options) *Service {
	t.Helper()
	if options.DataDir == "" {
		options.DataDir = t.TempDir()
	}
	service, err := New(context.Background(), pool, options)
	if err != nil {
		t.Fatalf("operations.New: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func requireBackupTools(t *testing.T, service *Service) {
	t.Helper()
	if err := service.requireCompatibleTools(context.Background()); err != nil {
		t.Skipf("PostgreSQL client tools are unavailable: %v", err)
	}
}

func TestNewRequiresMigratedSchema(t *testing.T) {
	pool := testPool(t, false)
	if _, err := New(context.Background(), pool, Options{DataDir: t.TempDir()}); err == nil {
		t.Fatal("expected the missing operations schema to be reported")
	}
	applyMigrations(t, pool)
	service := newTestService(t, pool, Options{})
	if len(service.rulesSnapshot()) != len(ruleNames) {
		t.Fatalf("expected %d alert rules, got %d", len(ruleNames), len(service.rulesSnapshot()))
	}
}

func TestAlertTransitionsRecordEventsAndNotify(t *testing.T) {
	pool := testPool(t, true)
	var (
		mu         sync.Mutex
		deliveries []alertNotification
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload alertNotification
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		deliveries = append(deliveries, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour, HTTPClient: server.Client()})
	ctx := context.Background()

	config := defaultWebhook()
	config.Enabled = true
	config.URL = server.URL
	config.Secret = "hook-secret"
	config.MinimumIntervalSeconds = 0
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal webhook: %v", err)
	}
	if err := service.saveConfigData(ctx, "webhook", body); err != nil {
		t.Fatalf("save webhook: %v", err)
	}
	service.configMu.Lock()
	service.webhook = config
	service.configMu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go service.runNotifier(runCtx)

	fire := alertRule{name: "filesystem_low_space", enabled: true, severity: severityWarning, thresholds: alertThresholds{MinimumFreeBytes: int64Pointer(1 << 50), MinimumFreePercent: floatPointer(0)}}
	if err := service.saveRules(ctx, []alertRule{fire}); err != nil {
		t.Fatalf("save rules: %v", err)
	}
	reloadRules(t, service, ctx)
	service.collect(ctx)
	state := stateForName(service.statesSnapshot(), "filesystem_low_space")
	if !state.Firing {
		t.Fatalf("the filesystem alert should be firing: %+v", state)
	}
	events, err := service.listAlertEvents(ctx, 10, "filesystem_low_space")
	if err != nil || len(events) != 1 || !events[0].Firing {
		t.Fatalf("expected one firing alert event, got %+v (%v)", events, err)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(deliveries) == 1
	})
	mu.Lock()
	if deliveries[0].Status != "firing" || strings.Contains(deliveries[0].Message, "hook-secret") {
		t.Fatalf("unexpected firing notification: %+v", deliveries[0])
	}
	mu.Unlock()

	resolve := fire
	resolve.thresholds = alertThresholds{MinimumFreeBytes: int64Pointer(1), MinimumFreePercent: floatPointer(0)}
	if err := service.saveRules(ctx, []alertRule{resolve}); err != nil {
		t.Fatalf("save resolved rules: %v", err)
	}
	reloadRules(t, service, ctx)
	service.collect(ctx)
	if state := stateForName(service.statesSnapshot(), "filesystem_low_space"); state.Firing {
		t.Fatalf("the filesystem alert should be resolved: %+v", state)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(deliveries) == 2
	})
	mu.Lock()
	if deliveries[1].Status != "resolved" {
		t.Fatalf("expected a recovery notification, got %+v", deliveries[1])
	}
	mu.Unlock()
	history, err := service.listAlertEvents(ctx, 10, "filesystem_low_space")
	if err != nil || len(history) != 2 || history[0].Firing {
		t.Fatalf("expected firing and resolved history, got %+v (%v)", history, err)
	}
	logged, err := service.listEvents(ctx, eventFilter{kinds: []string{KindAlert}})
	if err != nil || len(logged) != 2 {
		t.Fatalf("expected two alert log entries, got %+v (%v)", logged, err)
	}
}

func stateForName(states []alertState, name string) alertState {
	for _, state := range states {
		if state.Name == name {
			return state
		}
	}
	return alertState{}
}

func reloadRules(t *testing.T, service *Service, ctx context.Context) {
	t.Helper()
	rules, err := service.loadRules(ctx)
	if err != nil {
		t.Fatalf("reload rules: %v", err)
	}
	service.configMu.Lock()
	service.rules = rules
	service.configMu.Unlock()
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met in time")
}

func TestDownloadFailureEventsAreIdempotent(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, release_id, title, nzb, status, error)
		VALUES ('job-1', 'release-1', 'A Private Release Title', '\x00'::bytea, 'failed', 'verification failed')`); err != nil {
		t.Fatalf("insert failed download: %v", err)
	}
	service.collect(ctx)
	service.collect(ctx)
	events, err := service.listEvents(ctx, eventFilter{kinds: []string{KindDownloadFailed}})
	if err != nil || len(events) != 1 {
		t.Fatalf("expected exactly one download failure event, got %+v (%v)", events, err)
	}
	if events[0].Ref != "job-1" || !strings.Contains(events[0].Message, "verification failed") {
		t.Fatalf("unexpected failure event: %+v", events[0])
	}
	counts, err := service.countEventsByKind(ctx)
	if err != nil || counts[KindDownloadFailed] != 1 {
		t.Fatalf("unexpected failure counters: %+v (%v)", counts, err)
	}
}

func TestMetricsExpositionKeepsLabelsBounded(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO downloads (id, release_id, title, nzb, status, error)
		VALUES ('job-2', 'release-2', 'Private Movie Title 42', '\x00'::bytea, 'failed', 'boom')`); err != nil {
		t.Fatalf("insert failed download: %v", err)
	}
	service.collect(ctx)

	mux := http.NewServeMux()
	service.Register(mux)
	handler := service.WrapHTTP(mux)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics endpoint returned %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("unexpected metrics content type: %q", contentType)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`constellarr_downloads{state="failed"} 1`,
		"constellarr_download_failures_total 1",
		`constellarr_storage_free_bytes{volume="data"}`,
		`constellarr_db_pool_connections{state="max"}`,
		"go_goroutines",
		"process_resident_memory_bytes",
		"process_cpu_seconds_total",
		"constellarr_jobs_active",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics output is missing %q", expected)
		}
	}
	for _, forbidden := range []string{"Private Movie Title 42", service.dataDir, `title=`, `path=`, `user=`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics output exposes %q", forbidden)
		}
	}

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status endpoint returned %d: %s", statusResponse.Code, statusResponse.Body.String())
	}
	scrape := httptest.NewRecorder()
	handler.ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body = scrape.Body.String()
	if !strings.Contains(body, `constellarr_http_requests_total{method="GET",code="200"} 2`) {
		t.Fatalf("http metrics did not count requests:\n%s", metricLines(body, "constellarr_http_requests_total"))
	}
	for _, expected := range []string{
		"# TYPE constellarr_http_request_duration_seconds histogram",
		`constellarr_http_request_duration_seconds_bucket{method="GET",le="+Inf"}`,
		`constellarr_http_request_duration_seconds_sum{method="GET"}`,
		`constellarr_http_request_duration_seconds_count{method="GET"}`,
		"constellarr_http_in_flight_requests",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("http metrics are missing %q", expected)
		}
	}
}

func metricLines(body, prefix string) string {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func TestAlertsAPIReadsAndValidatesConfiguration(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	mux := http.NewServeMux()
	service.Register(mux)
	handler := service.WrapHTTP(mux)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/operations/alerts", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("alerts endpoint returned %d", response.Code)
	}
	var alerts struct {
		Alerts []alertStateView `json:"alerts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &alerts); err != nil || len(alerts.Alerts) != len(ruleNames) {
		t.Fatalf("unexpected alerts response: %s (%v)", response.Body.String(), err)
	}

	invalid := `{"rules":[{"name":"job_stuck","enabled":true,"severity":"warning","thresholds":{"stuckMinutes":0}}]}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/operations/alerts/config", strings.NewReader(invalid)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "error") {
		t.Fatalf("invalid thresholds must be rejected: %d %s", response.Code, response.Body.String())
	}
	unknownField := `{"rules":[],"nonsense":true}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/operations/alerts/config", strings.NewReader(unknownField)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected: %d", response.Code)
	}
	valid := `{"rules":[{"name":"job_stuck","enabled":true,"severity":"critical","thresholds":{"stuckMinutes":45}}],
		"webhook":{"enabled":true,"url":"https://example.com/hook","secret":"s3cret","timeoutSeconds":4,"minimumIntervalSeconds":60,"notifyRecovery":true,"headerName":"X-Constellarr-Secret"}}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/operations/alerts/config", strings.NewReader(valid)))
	if response.Code != http.StatusOK {
		t.Fatalf("valid configuration was rejected: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "s3cret") {
		t.Fatal("the webhook secret must never be returned")
	}
	var saved struct {
		Rules   []alertRuleView `json:"rules"`
		Webhook webhookView     `json:"webhook"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved configuration: %v", err)
	}
	if !saved.Webhook.SecretConfigured || !saved.Webhook.Enabled || saved.Webhook.URL != "https://example.com/hook" {
		t.Fatalf("the webhook configuration was not saved: %+v", saved.Webhook)
	}
	for _, rule := range saved.Rules {
		if rule.Name == "job_stuck" && (rule.Thresholds.StuckMinutes != 45 || rule.Severity != severityCritical) {
			t.Fatalf("the rule update was not applied: %+v", rule)
		}
	}
	reloaded := service.rulesSnapshot()
	if rule, ok := ruleByName(reloaded, "job_stuck"); !ok || *rule.thresholds.StuckMinutes != 45 {
		t.Fatalf("the rule update is not active in memory: %+v", rule)
	}
}

func TestEventsAndBackupConfigAPI(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	mux := http.NewServeMux()
	service.Register(mux)
	handler := service.WrapHTTP(mux)
	ctx := context.Background()
	if err := service.RecordImportError(ctx, "movies", "import failed for a file"); err != nil {
		t.Fatalf("record import error: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/operations/events?kind=import_error&limit=500", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "import failed") {
		t.Fatalf("unexpected events response: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/operations/events?kind=wrong", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown event kinds must be rejected: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/operations/backups/config",
		strings.NewReader(`{"scheduleEnabled":true,"intervalHours":6,"retentionCount":3}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("backup config update failed: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/operations/backups/config",
		strings.NewReader(`{"scheduleEnabled":true,"intervalHours":0,"retentionCount":3}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid backup intervals must be rejected: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/operations/backups", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"backups"`) {
		t.Fatalf("unexpected backup list: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/operations/backups/../escape", nil))
	if response.Code < 300 {
		t.Fatalf("unsafe backup identifiers must not resolve: %d", response.Code)
	}
}

func TestRecordEventSanitizesInput(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if err := service.RecordEvent(ctx, Event{Kind: KindProviderFailure, Severity: "", Source: strings.Repeat("s", 500), Message: strings.Repeat("m", 900), Ref: strings.Repeat("r", 500)}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	events, err := service.listEvents(ctx, eventFilter{kinds: []string{KindProviderFailure}})
	if err != nil || len(events) != 1 {
		t.Fatalf("expected one event, got %+v (%v)", events, err)
	}
	if len(events[0].Source) > 64 || len(events[0].Message) > 500 || len(events[0].Ref) > 128 {
		t.Fatalf("event fields were not bounded: %+v", events[0])
	}
	if events[0].Severity != severityWarning {
		t.Fatalf("missing severities must default to warning: %+v", events[0])
	}
	if err := service.RecordEvent(ctx, Event{Kind: "unknown_kind"}); err == nil {
		t.Fatal("unknown event kinds must be refused")
	}
}
