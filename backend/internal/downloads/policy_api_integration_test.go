package downloads_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
)

func policyRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	return req
}

func policyServe(t *testing.T, handler http.Handler, req *http.Request) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func TestDownloadControlEndpoints(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Close)
	handler := api.New(pool, api.Services{Downloads: manager})

	// The policy is readable before anything is saved.
	status, body := policyServe(t, handler, policyRequest(t, http.MethodGet, "/api/v1/downloads/policy", ""))
	if status != http.StatusOK {
		t.Fatalf("GET policy: status %d, body %s", status, body)
	}
	var snapshot transferpolicy.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("policy JSON is invalid: %s", body)
	}
	if snapshot.Effective.Paused || snapshot.Effective.LimitBytesPerSecond != 0 {
		t.Fatalf("fresh policy = %+v, want unlimited and running", snapshot.Effective)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("policy JSON is invalid: %s", body)
	}
	for _, key := range []string{"config", "effective"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("policy JSON is missing %q: %s", key, body)
		}
	}
	if strings.Contains(string(body), "limiter") {
		t.Fatalf("policy JSON exposes internal limiter state: %s", body)
	}

	// Unknown fields, multiple documents, and invalid values are rejected.
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"unknown field":    {`{"paused":false,"nonsense":1}`, http.StatusBadRequest},
		"two documents":    {`{"limit":{"mode":"unlimited","value":0}}{"limit":{"mode":"unlimited","value":0}}`, http.StatusBadRequest},
		"zero kbps":        {`{"limit":{"mode":"kbps","value":0}}`, http.StatusBadRequest},
		"invalid timezone": {`{"timezone":"Nowhere/Invalid"}`, http.StatusBadRequest},
		"invalid action":   {`{"outsideSchedule":"sometimes"}`, http.StatusBadRequest},
		"empty body":       {"", http.StatusBadRequest},
	} {
		status, body := policyServe(t, handler, policyRequest(t, http.MethodPut, "/api/v1/downloads/policy", tc.body))
		if status != tc.want {
			t.Fatalf("%s: status %d, body %s; want %d", name, status, body, tc.want)
		}
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil || payload["error"] == "" {
			t.Fatalf("%s: rejection is not an error JSON: %s", name, body)
		}
	}

	status, body = policyServe(t, handler, policyRequest(t, http.MethodPut, "/api/v1/downloads/policy",
		`{"timezone":"UTC","connectionMbps":0,"limit":{"mode":"kbps","value":256},"scheduleEnabled":false,"outsideSchedule":"normal","windows":[]}`))
	if status != http.StatusOK {
		t.Fatalf("PUT policy: status %d, body %s", status, body)
	}
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("policy JSON is invalid: %s", body)
	}
	if want := int64(256 * 1024); snapshot.Effective.LimitBytesPerSecond != want {
		t.Fatalf("saved limit = %d, want %d", snapshot.Effective.LimitBytesPerSecond, want)
	}

	// Global pause and resume return the resolved policy.
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/pause", ""))
	if status != http.StatusOK {
		t.Fatalf("POST pause: status %d, body %s", status, body)
	}
	if err := json.Unmarshal(body, &snapshot); err != nil || !snapshot.Effective.Paused {
		t.Fatalf("paused policy = %+v, %v", snapshot.Effective, err)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/resume", ""))
	if status != http.StatusOK {
		t.Fatalf("POST resume: status %d, body %s", status, body)
	}
	if err := json.Unmarshal(body, &snapshot); err != nil || snapshot.Effective.Paused {
		t.Fatalf("resumed policy = %+v, %v", snapshot.Effective, err)
	}

	// Individual controls return the job.
	job, err := manager.Add(ctx, "api-controls-release", "API controls")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/pause", ""))
	if status != http.StatusOK {
		t.Fatalf("POST job pause: status %d, body %s", status, body)
	}
	var controlled downloads.Job
	if err := json.Unmarshal(body, &controlled); err != nil {
		t.Fatalf("job JSON is invalid: %s", body)
	}
	if controlled.Status != "paused" || controlled.PauseReason != "manual" {
		t.Fatalf("paused job = %+v", controlled)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/resume", ""))
	if status != http.StatusOK || json.Unmarshal(body, &controlled) != nil || controlled.Status != "queued" {
		t.Fatalf("POST job resume: status %d, job %+v, body %s", status, controlled, body)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/cancel", ""))
	if status != http.StatusOK || json.Unmarshal(body, &controlled) != nil || controlled.Status != "cancelled" {
		t.Fatalf("POST job cancel: status %d, job %+v, body %s", status, controlled, body)
	}
	if status, _ = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/cancel", "")); status != http.StatusOK {
		t.Fatalf("repeated cancel: status %d", status)
	}
	// A cancelled job cannot be paused again.
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/pause", ""))
	if status != http.StatusConflict {
		t.Fatalf("pause of a cancelled job: status %d, body %s; want 409", status, body)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/"+job.ID+"/retry", ""))
	if status != http.StatusOK || json.Unmarshal(body, &controlled) != nil || controlled.Status != "queued" {
		t.Fatalf("POST job retry: status %d, job %+v, body %s", status, controlled, body)
	}
	status, body = policyServe(t, handler, policyRequest(t, http.MethodPost, "/api/v1/downloads/missing-job/pause", ""))
	if status != http.StatusNotFound {
		t.Fatalf("pause of a missing job: status %d, body %s; want 404", status, body)
	}
}
