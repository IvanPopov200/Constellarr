package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
)

func TestRequestLifecycleOwnershipAndDedupe(t *testing.T) {
	env := newEnvironment(t, envOptions{withActor: true})
	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)
	if request.Status != discovery.StatusPending || request.UserID != testUserA || request.Provider != "omdb" {
		t.Fatalf("created request = %+v", request)
	}

	// The same active media returns the existing request instead of a duplicate.
	status, raw := env.call(t, http.MethodPost, "/api/v1/requests",
		`{"mediaType":"movie","providerId":"tt0133093","title":"The Matrix","year":1999}`, callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("duplicate request: status %d, body %s; want the existing request", status, raw)
	}
	if duplicate := decode[discovery.Request](t, raw); duplicate.ID != request.ID {
		t.Fatalf("duplicate request created %s alongside %s", duplicate.ID, request.ID)
	}

	// Cross-user filtering happens in the service, not only in the interface.
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests", "", callOptions{user: testUserB})
	if status != http.StatusOK {
		t.Fatalf("list as another user: status %d, body %s", status, raw)
	}
	if list := decode[discovery.List](t, raw); len(list.Requests) != 0 {
		t.Fatalf("another user sees %d requests", len(list.Requests))
	}
	for _, probe := range []struct {
		name, method, path, body string
	}{
		{"detail", http.MethodGet, "/api/v1/requests/" + url.PathEscape(request.ID), ""},
		{"cancel", http.MethodPost, "/api/v1/requests/" + url.PathEscape(request.ID) + "/cancel", ""},
		{"comment", http.MethodPost, "/api/v1/requests/" + url.PathEscape(request.ID) + "/comments", `{"body":"hello"}`},
	} {
		status, raw := env.call(t, probe.method, probe.path, probe.body, callOptions{user: testUserB})
		if status != http.StatusNotFound {
			t.Errorf("%s as another user: status %d, body %s; want 404", probe.name, status, raw)
		}
	}

	// Comments are bounded plain text and stay visible to the owner and approvers.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/comments",
		`{"body":"Please add <b>this</b>"}`, callOptions{user: testUserA}); status != http.StatusBadRequest {
		t.Fatalf("HTML comment: status %d, body %s; want 400", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/comments",
		`{"body":"Please add this one"}`, callOptions{user: testUserA}); status != http.StatusCreated {
		t.Fatalf("comment: status %d, body %s", status, raw)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("approver detail: status %d, body %s", status, raw)
	}
	detail := decode[discovery.Detail](t, raw)
	if !detail.CanApprove || len(detail.Comments) != 1 || len(detail.Events) == 0 || !strings.Contains(detail.Comments[0].Body, "Please add this one") {
		t.Fatalf("detail = %+v", detail)
	}

	// Non-approvers cannot decide requests.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/reject",
		`{"reason":"no"}`, callOptions{user: testUserB}); status != http.StatusForbidden {
		t.Fatalf("reject as a non-approver: status %d, body %s; want 403", status, raw)
	}

	// Cancelling frees the media so it can be requested again.
	status, raw = env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/cancel", "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("cancel: status %d, body %s", status, raw)
	}
	if cancelled := decode[discovery.Request](t, raw); cancelled.Status != discovery.StatusCancelled {
		t.Fatalf("cancelled request = %+v", cancelled)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/cancel", "", callOptions{user: testUserA}); status != http.StatusConflict {
		t.Fatalf("second cancel: status %d, body %s; want 409", status, raw)
	}
	second := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)
	if second.ID == request.ID || second.Status != discovery.StatusPending {
		t.Fatalf("re-request = %+v", second)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(second.ID), "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("re-request detail: status %d, body %s", status, raw)
	}
	if detail := decode[discovery.Detail](t, raw); !hasEvent(detail, "re-requested") {
		t.Fatalf("re-request audit = %+v", detail.Events)
	}

	// Rejection requires a reason and allows a later request.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(second.ID)+"/reject",
		`{"reason":""}`, callOptions{user: testApprover, canApprove: true}); status != http.StatusBadRequest {
		t.Fatalf("reject without a reason: status %d, body %s; want 400", status, raw)
	}
	status, raw = env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(second.ID)+"/reject",
		`{"reason":"Already planned"}`, callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("reject: status %d, body %s", status, raw)
	}
	rejected := decode[discovery.Request](t, raw)
	if rejected.Status != discovery.StatusRejected || rejected.DecisionNote != "Already planned" || rejected.DecidedBy != testApprover {
		t.Fatalf("rejected request = %+v", rejected)
	}
	third := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)
	if third.ID == second.ID {
		t.Fatalf("rejected media could not be requested again")
	}

	// Approval of a rejected request is refused; approvers approve the new one.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(second.ID)+"/approve", "",
		callOptions{user: testApprover, canApprove: true}); status != http.StatusConflict {
		t.Fatalf("approve a rejected request: status %d, body %s; want 409", status, raw)
	}

	// Music stays unavailable without the module instead of pretending to work.
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests", "", callOptions{user: testUserA})
	if list := decode[discovery.List](t, raw); status != http.StatusOK || strings.Join(list.Types, ",") != "movie,tv" {
		t.Fatalf("capabilities = %s (status %d)", raw, status)
	}
	status, raw = env.call(t, http.MethodPost, "/api/v1/requests",
		`{"mediaType":"music","providerId":"release-group-1","title":"Synthetic Album","year":2024}`, callOptions{user: testUserA})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("music request without a music module: status %d, body %s; want 503", status, raw)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/discover?type=music&q=album", "", callOptions{user: testUserA})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("music discover without a music module: status %d, body %s; want 503", status, raw)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/requests/discover?type=movie&q=matrix", "", callOptions{user: testUserA}); status != http.StatusServiceUnavailable {
		t.Fatalf("movie discover without a metadata provider: status %d, body %s; want 503", status, raw)
	}

	// Contract enforcement.
	cases := []struct {
		name, method, path, body string
	}{
		{"unknown field", http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"tt0133093","title":"A","unexpected":true}`},
		{"trailing document", http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"tt0133093","title":"A"}{"x":1}`},
		{"unknown media type", http.MethodPost, "/api/v1/requests", `{"mediaType":"book","providerId":"tt0133093","title":"A"}`},
		{"malformed IMDb ID", http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"1234","title":"A"}`},
		{"missing title", http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"tt0133093"}`},
		{"oversized body", http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"tt0133093","title":"A","message":"` + strings.Repeat("x", 257<<10) + `"}`},
		{"unknown list status", http.MethodGet, "/api/v1/requests?status=bogus", ""},
		{"unknown discover type", http.MethodGet, "/api/v1/requests/discover?type=book&q=matrix", ""},
		{"discover page zero", http.MethodGet, "/api/v1/requests/discover?type=movie&q=matrix&page=0", ""},
		{"unknown approve field", http.MethodPost, "/api/v1/requests/" + url.PathEscape(third.ID) + "/approve", `{"unexpected":true}`},
		{"list limit out of range", http.MethodGet, "/api/v1/requests?limit=500", ""},
	}
	for _, tc := range cases {
		status, raw := env.call(t, tc.method, tc.path, tc.body, callOptions{user: testApprover, canApprove: true})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s; want 400", tc.name, status, raw)
		}
	}
}

func hasEvent(detail discovery.Detail, action string) bool {
	for _, event := range detail.Events {
		if event.Action == action {
			return true
		}
	}
	return false
}

func TestApprovalCreatesAndKeepsOneLibraryItem(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)

	status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", `{}`,
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", status, raw)
	}
	approved := decode[discovery.Request](t, raw)
	if approved.LibraryID == "" || (approved.Status != discovery.StatusApproved && approved.Status != discovery.StatusAvailable) {
		t.Fatalf("approved request = %+v", approved)
	}
	library, err := env.movies.List(context.Background())
	if err != nil {
		t.Fatalf("list movies: %v", err)
	}
	if len(library) != 1 || library[0].Metadata.IMDbID != "tt0133093" || library[0].ID != approved.LibraryID {
		t.Fatalf("library = %+v (request library %s)", library, approved.LibraryID)
	}

	// Approving again keeps the same association instead of adding a duplicate.
	status, raw = env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", `{}`,
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("second approve: status %d, body %s", status, raw)
	}
	if again := decode[discovery.Request](t, raw); again.LibraryID != approved.LibraryID {
		t.Fatalf("second approve changed the library association: %+v", again)
	}
	if library, err = env.movies.List(context.Background()); err != nil || len(library) != 1 {
		t.Fatalf("library after a second approve = %+v (%v)", library, err)
	}

	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true})
	detail := decode[discovery.Detail](t, raw)
	if status != http.StatusOK || !hasEvent(detail, "approving") || !hasEvent(detail, "approved") {
		t.Fatalf("approval audit = %s (status %d)", raw, status)
	}

	// Concurrent approvals of the same request must not create a second library item.
	request2 := addMovieRequest(t, env, testUserB, "tt0234215", "The Matrix Reloaded", 2003)
	var wait sync.WaitGroup
	results := make([]int, 2)
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], _ = env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request2.ID)+"/approve", `{}`,
				callOptions{user: testApprover, canApprove: true})
		}(i)
	}
	wait.Wait()
	succeeded := 0
	for _, code := range results {
		if code == http.StatusOK {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatalf("concurrent approvals: %v; want at least one success", results)
	}
	if library, err = env.movies.List(context.Background()); err != nil || len(library) != 2 {
		t.Fatalf("library after concurrent approvals = %+v (%v)", library, err)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request2.ID), "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("request detail after concurrent approvals: status %d, body %s", status, raw)
	} else if detail := decode[discovery.Detail](t, raw); detail.Request.LibraryID == "" {
		t.Fatalf("concurrent approval left no library association: %+v", detail.Request)
	}
}

func TestApprovalEditsProfileRootAndMonitoring(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	config, err := env.movies.ConfigView(context.Background())
	if err != nil {
		t.Fatalf("movie config: %v", err)
	}
	if len(config.RootFolders) == 0 {
		t.Fatal("no default root folder was configured")
	}
	rootID := config.RootFolders[0].ID
	profiles, err := env.movies.Store.Profiles(context.Background())
	if err != nil || len(profiles) == 0 {
		t.Fatalf("profiles: %v (%v)", profiles, err)
	}
	profileID := profiles[0].ID

	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)
	// Invalid edits stay pending and are rejected before any provider work.
	for _, body := range []string{
		`{"profileId":"missing-profile"}`,
		`{"rootId":"missing-root"}`,
		`{"monitorMode":"sometimes"}`,
	} {
		if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", body,
			callOptions{user: testApprover, canApprove: true}); status != http.StatusBadRequest {
			t.Fatalf("invalid edits %s: status %d, body %s; want 400", body, status, raw)
		}
	}
	body := fmt.Sprintf(`{"profileId":%q,"rootId":%q,"monitored":false}`, profileID, rootID)
	status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", body,
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("approve with edits: status %d, body %s", status, raw)
	}
	library, err := env.movies.List(context.Background())
	if err != nil || len(library) != 1 {
		t.Fatalf("library = %+v (%v)", library, err)
	}
	if library[0].Monitored || library[0].ProfileID != profileID || library[0].RootID != rootID {
		t.Fatalf("approved movie did not keep the approver's edits: %+v", library[0])
	}
}

func TestApprovalFailureIsNotReportedAsApproved(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	metadataFixture.setHealthy(false)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)

	status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", `{}`,
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusBadGateway {
		t.Fatalf("failed approval: status %d, body %s; want 502", status, raw)
	}
	if bytes.Contains(raw, []byte("synthetic-metadata-key")) {
		t.Fatalf("approval error leaked the metadata key: %s", raw)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true})
	detail := decode[discovery.Detail](t, raw)
	if status != http.StatusOK {
		t.Fatalf("detail: status %d, body %s", status, raw)
	}
	if detail.Request.Status != discovery.StatusApproving || detail.Request.LibraryID != "" {
		t.Fatalf("failed approval claimed success: %+v", detail.Request)
	}
	if detail.Request.Delivery.Attempts != 1 || detail.Request.Delivery.Message == "" || !hasEvent(detail, "approve-failed") {
		t.Fatalf("failed approval state = %+v", detail)
	}
	if library, err := env.movies.List(context.Background()); err != nil || len(library) != 0 {
		t.Fatalf("library after a failed approval = %+v (%v)", library, err)
	}

	// The background pass resumes the approval once the provider recovers.
	backdate(t, env, request.ID, "5 minutes")
	metadataFixture.setHealthy(true)
	result, err := env.service.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Approved != 1 {
		t.Fatalf("Sync result = %+v; want one resumed approval", result)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("detail after resume: status %d, body %s", status, raw)
	} else if detail := decode[discovery.Detail](t, raw); detail.Request.Status != discovery.StatusApproved || detail.Request.LibraryID == "" {
		t.Fatalf("resumed approval = %+v", detail.Request)
	}

	// Repeated failures return the request to the queue instead of looping forever.
	failingFixture := newOMDbFixture(t)
	failingFixture.setHealthy(false)
	env2 := newEnvironment(t, envOptions{withActor: true, metadata: failingFixture})
	request2 := addMovieRequest(t, env2, testUserA, "tt0133093", "The Matrix", 1999)
	if status, raw := env2.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request2.ID)+"/approve", `{}`,
		callOptions{user: testApprover, canApprove: true}); status != http.StatusBadGateway {
		t.Fatalf("approve with a failing metadata provider: status %d, body %s", status, raw)
	}
	execSQL(t, env2, `UPDATE discovery_requests SET delivery = jsonb_set(delivery, '{attempts}', '5') WHERE id = $1`, request2.ID)
	backdate(t, env2, request2.ID, "5 minutes")
	result, err = env2.service.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Reverted != 1 {
		t.Fatalf("Sync result = %+v; want one reverted approval", result)
	}
	if status, raw := env2.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request2.ID), "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("detail after revert: status %d, body %s", status, raw)
	} else {
		detail := decode[discovery.Detail](t, raw)
		if detail.Request.Status != discovery.StatusPending || detail.Request.Delivery.Phase != discovery.PhaseFailed {
			t.Fatalf("reverted request = %+v", detail.Request)
		}
		if !hasEvent(detail, "approve-reverted") {
			t.Fatalf("revert audit = %+v", detail.Events)
		}
	}
}

func TestUnauthenticatedRoutesDenyAccess(t *testing.T) {
	env := newEnvironment(t, envOptions{})
	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/requests", ""},
		{http.MethodPost, "/api/v1/requests", `{"mediaType":"movie","providerId":"tt0133093","title":"A"}`},
		{http.MethodGet, "/api/v1/calendar", ""},
		{http.MethodGet, "/api/v1/calendar.ics", ""},
		{http.MethodGet, "/api/v1/ai/config", ""},
		{http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"]}`},
	} {
		status, raw := env.call(t, probe.method, probe.path, probe.body, callOptions{})
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s without identity: status %d, body %s; want 401", probe.method, probe.path, status, raw)
		}
	}
}

func backdate(t *testing.T, env *environment, id, interval string) {
	t.Helper()
	execSQL(t, env, `UPDATE discovery_requests SET updated_at = now() - $2::interval WHERE id = $1`, id, interval)
}

func execSQL(t *testing.T, env *environment, sql string, args ...any) {
	t.Helper()
	if _, err := env.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func TestUpstreamTimeoutsStayBoundedAndSanitized(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	metadataFixture.setHang(true)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := env.service.Approve(ctx, testApprover, request.ID, discovery.ApproveInput{})
	if err == nil {
		t.Fatal("approval against a hanging provider succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("approval took %s against a hanging provider", elapsed)
	}
	if strings.Contains(err.Error(), "synthetic-metadata-key") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("approval error leaked credentials: %v", err)
	}
	status, raw := env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("detail: status %d, body %s", status, raw)
	}
	if detail := decode[discovery.Detail](t, raw); detail.Request.Status != discovery.StatusApproving {
		t.Fatalf("timed-out approval status = %q; want approving", detail.Request.Status)
	}

	// An unreachable provider reported through HTTP is sanitized as well.
	metadataFixture.setHang(false)
	metadataFixture.setHealthy(false)
	if status, raw := env.call(t, http.MethodGet, "/api/v1/requests/discover?type=movie&q=matrix", "", callOptions{user: testUserA}); status != http.StatusBadGateway {
		t.Fatalf("discover with a failing provider: status %d, body %s; want 502", status, raw)
	} else if bytes.Contains(raw, []byte("synthetic-metadata-key")) || !bytes.Contains(raw, []byte(`"error"`)) {
		t.Fatalf("provider failure response = %s", raw)
	}
}

func TestAIProviderTimeoutsAreSanitized(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	aiFixture := newAIFixture(t, `{"candidates":[]}`)
	aiFixture.setHang(true)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	status, raw := env.call(t, http.MethodPut, "/api/v1/ai/config",
		fmt.Sprintf(`{"baseURL":%q,"apiKey":"synthetic-ai-secret","model":"test-model","maxTokens":64,"temperature":0.2}`, aiFixture.URL+"/v1"),
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("PUT ai config: status %d, body %s", status, raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := env.service.Generate(ctx, testUserA, discovery.RecommendationInput{Genres: []string{"Action"}, Count: 2})
	if err == nil {
		t.Fatal("generation against a hanging provider succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("generation took %s against a hanging provider", elapsed)
	}
	if strings.Contains(err.Error(), "synthetic-ai-secret") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("generation error leaked credentials: %v", err)
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "context deadline") {
		t.Fatalf("generation error = %v; want a timeout report", err)
	}
}

func TestRequestAuditRecordsActorAndTime(t *testing.T) {
	env := newEnvironment(t, envOptions{withActor: true})
	request := addMovieRequest(t, env, testUserA, "tt0133093", "The Matrix", 1999)
	status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/comments",
		`{"body":"Any progress?"}`, callOptions{user: testApprover, canApprove: true})
	if status != http.StatusCreated {
		t.Fatalf("approver comment: status %d, body %s", status, raw)
	}
	comment := decode[discovery.Comment](t, raw)
	if comment.UserID != testApprover || comment.CreatedAt.IsZero() {
		t.Fatalf("comment = %+v", comment)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests/"+url.PathEscape(request.ID), "", callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("detail: status %d, body %s", status, raw)
	}
	detail := decode[discovery.Detail](t, raw)
	if len(detail.Events) == 0 || detail.Events[0].Actor == "" || detail.Events[0].CreatedAt.IsZero() {
		t.Fatalf("audit events = %+v", detail.Events)
	}
	if _, err := json.Marshal(detail); err != nil {
		t.Fatalf("detail is not serializable: %v", err)
	}
}
