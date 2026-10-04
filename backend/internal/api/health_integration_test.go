package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
)

func TestHealthReportsDatabaseAvailability(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a reachable PostgreSQL server to run this test")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal("cannot create a pool from TEST_DATABASE_URL")
	}
	defer pool.Close()
	handler := api.New(pool)

	status, body := health(t, handler)
	if status != http.StatusOK || body.Status != "ok" || body.Database != "connected" {
		t.Fatalf("reachable database: got status %d, body %+v", status, body)
	}

	pool.Close()

	status, body = health(t, handler)
	if status != http.StatusServiceUnavailable || body.Status != "degraded" || body.Database != "unavailable" {
		t.Fatalf("closed pool: got status %d, body %+v", status, body)
	}

	if status, _ := get(t, handler, "/healthz"); status != http.StatusOK {
		t.Fatalf("liveness with unavailable database: got status %d", status)
	}
}

type healthBody struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

func health(t *testing.T, handler http.Handler) (int, healthBody) {
	t.Helper()
	status, raw := get(t, handler, "/api/v1/health")
	var body healthBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("health response is not JSON: %s", raw)
	}
	return status, body
}

func get(t *testing.T, handler http.Handler, target string) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder.Code, recorder.Body.Bytes()
}
