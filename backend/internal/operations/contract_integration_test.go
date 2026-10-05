package operations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestListRouteEnvelopes pins the JSON envelope every operations list route returns.
func TestListRouteEnvelopes(t *testing.T) {
	pool := testPool(t, true)
	service := newTestService(t, pool, Options{MonitorInterval: time.Hour})
	ctx := context.Background()
	if err := service.RecordImportError(ctx, "movies", "import failed"); err != nil {
		t.Fatalf("record import error: %v", err)
	}
	if _, err := service.recordAlertEvent(ctx, alertRule{name: "job_stuck", severity: severityWarning}, evaluation{firing: true, value: 1}); err != nil {
		t.Fatalf("record alert event: %v", err)
	}
	mux := http.NewServeMux()
	service.Register(mux)

	for _, route := range []struct {
		path     string
		envelope string
	}{
		{"/api/v1/operations/events", "events"},
		{"/api/v1/operations/alerts/history", "events"},
		{"/api/v1/operations/backups", "backups"},
	} {
		t.Run(route.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route.path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not an object: %v", err)
			}
			raw, ok := body[route.envelope]
			if !ok {
				t.Fatalf("response has no %q envelope: %s", route.envelope, recorder.Body.String())
			}
			var list []json.RawMessage
			if err := json.Unmarshal(raw, &list); err != nil {
				t.Fatalf("%q envelope is not an array: %s", route.envelope, recorder.Body.String())
			}
			if len(body) != 1 {
				t.Fatalf("unexpected extra envelope fields: %s", recorder.Body.String())
			}
		})
	}

	for _, route := range []struct {
		path string
		want string
	}{
		{"/api/v1/operations/status", "database"},
		{"/api/v1/operations/alerts", "alerts"},
		{"/api/v1/operations/alerts/config", "rules"},
		{"/api/v1/operations/backups/config", "intervalHours"},
	} {
		t.Run(route.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route.path, nil))
			var body map[string]json.RawMessage
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not an object: %v", err)
			}
			if _, ok := body[route.want]; !ok {
				t.Fatalf("response has no %q field: %s", route.want, recorder.Body.String())
			}
		})
	}
}
