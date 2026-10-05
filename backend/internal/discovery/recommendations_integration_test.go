package discovery_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
)

const candidateReply = `{"candidates":[
  {"title":"Synthetic Lookup","mediaType":"movie","year":2026,"reason":"Matches your genres"},
  {"title":"Totally Unknown Title","mediaType":"movie","year":2019,"reason":"A plausible guess"},
  {"title":"Ignored Medium","mediaType":"game","year":2020,"reason":"not requestable"},
  {"title":"Ignored Music","mediaType":"music","year":2020,"reason":"music is not configured"}
]}`

var (
	readRecommendations = []string{discovery.PermissionLibraryRead}
	requesterRights     = []string{discovery.PermissionLibraryRead, discovery.PermissionRequestsWrite}
	librarianRights     = []string{discovery.PermissionLibraryRead, discovery.PermissionLibraryWrite}
)

type aiConfigBody struct {
	BaseURL          string  `json:"baseURL"`
	APIKey           string  `json:"apiKey"`
	Model            string  `json:"model"`
	MaxTokens        int     `json:"maxTokens"`
	Temperature      float64 `json:"temperature"`
	APIKeyConfigured bool    `json:"apiKeyConfigured"`
}

func configureAI(t *testing.T, env *environment, fixture *aiFixture, key string) aiConfigBody {
	t.Helper()
	body := fmt.Sprintf(`{"baseURL":%q,"apiKey":%q,"model":"test-model","maxTokens":256,"temperature":0.4}`,
		fixture.URL+"/v1", key)
	status, raw := env.call(t, http.MethodPut, "/api/v1/ai/config", body, callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("PUT ai config: status %d, body %s", status, raw)
	}
	return decode[aiConfigBody](t, raw)
}

func TestAIConfigIsWriteOnlyAndValidated(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	fixture := newAIFixture(t, `{"candidates":[]}`)

	status, raw := env.call(t, http.MethodGet, "/api/v1/ai/config", "", callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK {
		t.Fatalf("GET ai config: status %d, body %s", status, raw)
	}
	if config := decode[aiConfigBody](t, raw); config.BaseURL != "" || config.APIKeyConfigured || config.MaxTokens != 700 {
		t.Fatalf("unconfigured AI settings = %+v", config)
	}

	for _, body := range []string{
		fmt.Sprintf(`{"baseURL":%q,"apiKey":"secret","model":"test-model"}`, "https://user:pass@ai.example/v1"),
		`{"baseURL":"ftp://ai.example/v1","apiKey":"secret","model":"test-model"}`,
		`{"baseURL":"/v1","apiKey":"secret","model":"test-model"}`,
		`{"baseURL":"https://ai.example/v1","apiKey":"secret","model":""}`,
		`{"baseURL":"https://ai.example/v1","apiKey":"secret","model":"m","unexpected":true}`,
	} {
		if status, raw := env.call(t, http.MethodPut, "/api/v1/ai/config", body, callOptions{user: testApprover, canApprove: true}); status != http.StatusBadRequest {
			t.Errorf("PUT ai config %s: status %d, body %s; want 400", body, status, raw)
		}
	}

	saved := configureAI(t, env, fixture, "synthetic-ai-secret")
	if saved.BaseURL != fixture.URL+"/v1" || !saved.APIKeyConfigured || saved.APIKey != "" {
		t.Fatalf("saved AI settings = %+v", saved)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/ai/config", "", callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK || bytes.Contains(raw, []byte("synthetic-ai-secret")) {
		t.Fatalf("GET ai config leaked the key: status %d, body %s", status, raw)
	}
	if config := decode[aiConfigBody](t, raw); !config.APIKeyConfigured || config.APIKey != "" || config.Model != "test-model" {
		t.Fatalf("AI view = %+v", config)
	}

	// A blank key keeps the stored secret, matching other write-only credentials.
	status, raw = env.call(t, http.MethodPut, "/api/v1/ai/config",
		fmt.Sprintf(`{"baseURL":%q,"apiKey":"","model":"test-model","maxTokens":128,"temperature":0.1}`, fixture.URL+"/v1"),
		callOptions{user: testApprover, canApprove: true})
	if status != http.StatusOK || bytes.Contains(raw, []byte("synthetic-ai-secret")) {
		t.Fatalf("blank key update: status %d, body %s", status, raw)
	}
	if config := decode[aiConfigBody](t, raw); !config.APIKeyConfigured || config.MaxTokens != 128 {
		t.Fatalf("AI view after a blank key = %+v", config)
	}
	var stored []byte
	if err := env.pool.QueryRow(context.Background(), `SELECT data FROM discovery_ai_config WHERE id`).Scan(&stored); err != nil {
		t.Fatalf("read stored AI config: %v", err)
	}
	if !strings.Contains(string(stored), "synthetic-ai-secret") {
		t.Fatal("the AI API key was not retained")
	}

	status, raw = env.call(t, http.MethodPost, "/api/v1/ai/test", "", callOptions{user: testApprover, canApprove: true})
	answer := decode[struct {
		OK     bool     `json:"ok"`
		Error  string   `json:"error"`
		Models []string `json:"models"`
	}](t, raw)
	if status != http.StatusOK || !answer.OK || len(answer.Models) != 2 {
		t.Fatalf("AI test = %s (status %d)", raw, status)
	}
	_, auths, _ := fixture.snapshot()
	if len(auths) == 0 || auths[0] != "Bearer synthetic-ai-secret" {
		t.Fatalf("AI provider received authorization %v", auths)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/ai/models", "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("GET ai models: status %d, body %s", status, raw)
	} else if !bytes.Contains(raw, []byte("test-model")) {
		t.Fatalf("models response = %s", raw)
	}

}

// TestKeylessLocalProviderStaysUsable proves a private OpenAI-compatible endpoint needs no key.
func TestKeylessLocalProviderStaysUsable(t *testing.T) {
	env := newEnvironment(t, envOptions{withActor: true, metadata: newOMDbFixture(t)})
	keyless := newAIFixture(t, `{"candidates":[]}`)
	if status, raw := env.call(t, http.MethodPut, "/api/v1/ai/config",
		fmt.Sprintf(`{"baseURL":%q,"apiKey":"","model":"local-model","maxTokens":64,"temperature":0.2}`, keyless.URL+"/v1"),
		callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("PUT keyless ai config: status %d, body %s", status, raw)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/ai/config", "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("GET keyless ai config: status %d, body %s", status, raw)
	} else if config := decode[aiConfigBody](t, raw); config.APIKeyConfigured {
		t.Fatalf("keyless configuration reports a stored key: %+v", config)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"],"count":1}`,
		callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusCreated {
		t.Fatalf("keyless recommendation: status %d, body %s", status, raw)
	}
	_, auths, _ := keyless.snapshot()
	if len(auths) != 1 || auths[0] != "" {
		t.Fatalf("keyless provider authorization = %v", auths)
	}
}

func TestRecommendationsResolveCandidatesAndAccept(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	fixture := newAIFixture(t, candidateReply)
	configureAI(t, env, fixture, "synthetic-ai-secret")

	// Another user's history must never reach the provider.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests",
		`{"mediaType":"movie","providerId":"tt0234215","title":"Secret Other User Title","year":2003}`,
		callOptions{user: testUserB}); status != http.StatusCreated {
		t.Fatalf("other user request: status %d, body %s", status, raw)
	}

	status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations",
		`{"mediaType":"movie","useHistory":true,"genres":["Science Fiction"],"titles":["The Matrix"],"count":4}`,
		callOptions{user: testUserA, perms: requesterRights})
	if status != http.StatusCreated {
		t.Fatalf("POST recommendations: status %d, body %s", status, raw)
	}
	rec := decode[discovery.Recommendation](t, raw)
	if rec.ID == "" || rec.Model != "test-model" || len(rec.Warnings) != 0 {
		t.Fatalf("recommendation = %+v", rec)
	}
	if len(rec.Candidates) != 2 {
		t.Fatalf("candidates = %+v; want the two usable movie candidates", rec.Candidates)
	}
	verified, unverified := rec.Candidates[0], rec.Candidates[1]
	if !verified.Verified || verified.ProviderID != "tt1234567" || verified.Provider != "omdb" {
		t.Fatalf("verified candidate = %+v", verified)
	}
	if unverified.Verified || unverified.ProviderID != "" || unverified.Verification == "" {
		t.Fatalf("unverified candidate = %+v", unverified)
	}
	prompts, auths, _ := fixture.snapshot()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Preferred genres: Science Fiction") ||
		!strings.Contains(prompts[0], "Titles the user picked: The Matrix") {
		t.Fatalf("prompt = %v", prompts)
	}
	if strings.Contains(prompts[0], "Secret Other User Title") {
		t.Fatalf("prompt leaked another user's data: %s", prompts[0])
	}
	if strings.Contains(prompts[0], "music") {
		t.Fatalf("prompt offered a media type that is not configured: %s", prompts[0])
	}
	if len(auths) != 1 || auths[0] != "Bearer synthetic-ai-secret" {
		t.Fatalf("provider authorization = %v", auths)
	}

	// Recommendations stay private to their owner.
	if status, raw := env.call(t, http.MethodGet, "/api/v1/recommendations/"+url.PathEscape(rec.ID), "",
		callOptions{user: testUserB, perms: readRecommendations}); status != http.StatusNotFound {
		t.Fatalf("another user reading a recommendation: status %d, body %s; want 404", status, raw)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/recommendations", "", callOptions{user: testUserA, perms: requesterRights})
	if status != http.StatusOK {
		t.Fatalf("list recommendations: status %d, body %s", status, raw)
	}
	list := decode[discovery.RecommendationList](t, raw)
	if len(list.Recommendations) != 1 || strings.Join(list.Types, ",") != "movie,tv" ||
		!list.Permissions.RequestsWrite || list.Permissions.LibraryWrite {
		t.Fatalf("recommendation list = %s", raw)
	}

	// An unconfirmed candidate cannot become a request.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":1,"action":"request"}`, callOptions{user: testUserA, perms: requesterRights}); status != http.StatusBadRequest {
		t.Fatalf("accepting an unverified candidate: status %d, body %s; want 400", status, raw)
	}
	// Adding to the library needs library.write, not the approval right.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"add"}`, callOptions{user: testUserA, canApprove: true, perms: requesterRights}); status != http.StatusForbidden {
		t.Fatalf("add without library.write: status %d, body %s; want 403", status, raw)
	}
	// Requesting needs requests.write.
	status, raw = env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"request"}`, callOptions{user: testUserA, perms: requesterRights})
	if status != http.StatusOK {
		t.Fatalf("accept as request: status %d, body %s", status, raw)
	}
	result := decode[discovery.AcceptResult](t, raw)
	if result.Request == nil || result.Request.ProviderID != "tt1234567" || result.Request.UserID != testUserA {
		t.Fatalf("accept result = %+v", result)
	}
	if !result.Recommendation.Candidates[0].Accepted {
		t.Fatalf("accepted candidate was not recorded: %+v", result.Recommendation.Candidates[0])
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"request"}`, callOptions{user: testUserA, perms: requesterRights}); status != http.StatusConflict {
		t.Fatalf("second accept: status %d, body %s; want 409", status, raw)
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/requests", "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("list requests: status %d, body %s", status, raw)
	}
	if list := decode[discovery.List](t, raw); len(list.Requests) != 1 || list.Requests[0].ProviderID != "tt1234567" {
		t.Fatalf("requests after acceptance = %s", raw)
	}

	// A librarian adds a suggestion straight to the library.
	fixture.set(`{"candidates":[{"title":"Synthetic Lookup","mediaType":"movie","year":2026,"reason":"again"}]}`, 0)
	status, raw = env.call(t, http.MethodPost, "/api/v1/recommendations", `{"mediaType":"movie","titles":["The Matrix"],"count":1}`,
		callOptions{user: testUserA, perms: librarianRights})
	if status != http.StatusCreated {
		t.Fatalf("second generation: status %d, body %s", status, raw)
	}
	second := decode[discovery.Recommendation](t, raw)
	if len(second.Candidates) != 1 || !second.Candidates[0].Verified {
		t.Fatalf("second recommendation = %+v", second.Candidates)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(second.ID)+"/accept",
		`{"candidate":0,"action":"delete"}`, callOptions{user: testUserA, perms: librarianRights}); status != http.StatusBadRequest {
		t.Fatalf("unknown action: status %d, body %s; want 400", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(second.ID)+"/accept",
		`{"candidate":9,"action":"request"}`, callOptions{user: testUserA, perms: librarianRights}); status != http.StatusBadRequest {
		t.Fatalf("missing candidate: status %d, body %s; want 400", status, raw)
	}
	status, raw = env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(second.ID)+"/accept",
		`{"candidate":0,"action":"add","monitored":true}`, callOptions{user: testUserA, perms: librarianRights})
	if status != http.StatusOK {
		t.Fatalf("accept as add: status %d, body %s", status, raw)
	}
	added := decode[discovery.AcceptResult](t, raw)
	if added.LibraryID == "" {
		t.Fatalf("add result = %+v", added)
	}
	library, err := env.movies.List(context.Background())
	if err != nil || len(library) != 1 || library[0].ID != added.LibraryID {
		t.Fatalf("library after add = %+v (%v)", library, err)
	}
}

// TestRecommendationPermissions pins the split between reading, requesting, and library writes.
func TestRecommendationPermissions(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})
	fixture := newAIFixture(t, candidateReply)
	configureAI(t, env, fixture, "synthetic-ai-secret")

	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"]}`,
		callOptions{user: testUserA}); status != http.StatusForbidden {
		t.Fatalf("generate without library.read: status %d, body %s; want 403", status, raw)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/recommendations", "", callOptions{user: testUserA}); status != http.StatusForbidden {
		t.Fatalf("list without library.read: status %d, body %s; want 403", status, raw)
	}
	if _, _, requests := fixture.snapshot(); requests != 0 {
		t.Fatalf("the AI provider was called %d times without library.read", requests)
	}

	// Requests routes keep working with request permissions only.
	if status, raw := env.call(t, http.MethodGet, "/api/v1/requests", "",
		callOptions{user: testUserA, perms: []string{discovery.PermissionRequestsWrite}}); status != http.StatusOK {
		t.Fatalf("list requests: status %d, body %s", status, raw)
	} else if list := decode[discovery.List](t, raw); !list.Permissions.RequestsWrite || list.Permissions.LibraryWrite {
		t.Fatalf("request permissions = %+v", list.Permissions)
	}

	status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"],"count":2}`,
		callOptions{user: testUserA, perms: readRecommendations})
	if status != http.StatusCreated {
		t.Fatalf("generate with library.read: status %d, body %s", status, raw)
	}
	rec := decode[discovery.Recommendation](t, raw)
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"request"}`, callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusForbidden {
		t.Fatalf("accept request without requests.write: status %d, body %s; want 403", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"add"}`, callOptions{user: testUserA, perms: requesterRights}); status != http.StatusForbidden {
		t.Fatalf("accept add without library.write: status %d, body %s; want 403", status, raw)
	}
	status, raw = env.call(t, http.MethodPost, "/api/v1/recommendations/"+url.PathEscape(rec.ID)+"/accept",
		`{"candidate":0,"action":"request"}`, callOptions{user: testUserA, perms: requesterRights})
	if status != http.StatusOK {
		t.Fatalf("accept request with requests.write: status %d, body %s", status, raw)
	}
	// The approval queue stays behind requests.approve, independent of library.write.
	request := addMovieRequest(t, env, testUserB, "tt0133093", "The Matrix", 1999)
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", `{}`,
		callOptions{user: testApprover, perms: librarianRights}); status != http.StatusForbidden {
		t.Fatalf("approve without requests.approve: status %d, body %s; want 403", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/requests/"+url.PathEscape(request.ID)+"/approve", `{}`,
		callOptions{user: testApprover, canApprove: true, perms: librarianRights}); status != http.StatusOK {
		t.Fatalf("approve with requests.approve: status %d, body %s", status, raw)
	}
}

func TestRecommendationsMalformedAndUnconfiguredProviders(t *testing.T) {
	metadataFixture := newOMDbFixture(t)
	env := newEnvironment(t, envOptions{withActor: true, metadata: metadataFixture})

	// Without an endpoint the provider is never called.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"]}`,
		callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured recommendation: status %d, body %s; want 503", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/ai/test", "", callOptions{user: testApprover, canApprove: true}); status != http.StatusOK {
		t.Fatalf("AI test without credentials: status %d, body %s", status, raw)
	} else if answer := decode[struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}](t, raw); answer.OK || answer.Error == "" {
		t.Fatalf("AI test without credentials = %+v", answer)
	}
	fixture := newAIFixture(t, `{"candidates":[]}`)
	configureAI(t, env, fixture, "synthetic-ai-secret")

	// Music recommendations stay unavailable without the music module.
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"mediaType":"music","titles":["Album"]}`,
		callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusServiceUnavailable {
		t.Fatalf("music recommendation: status %d, body %s; want 503", status, raw)
	}
	// Missing taste input is rejected before any provider call.
	for _, body := range []string{
		`{"mediaType":"movie"}`,
		`{"genres":["` + strings.Repeat("x", 60) + `"]}`,
		`{"titles":["a","b","c","d","e","f","g","h","i","j","k"]}`,
		`{"titles":["<script>alert(1)</script>"]}`,
		`{"genres":["Action"],"unexpected":true}`,
	} {
		if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", body, callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusBadRequest {
			t.Errorf("recommendation body %s: status %d, body %s; want 400", body, status, raw)
		}
	}
	if _, _, requests := fixture.snapshot(); requests != 0 {
		t.Fatalf("the AI provider was called %d times for rejected input", requests)
	}

	cases := []struct {
		name       string
		reply      string
		status     int
		wantStatus int
		wantText   string
	}{
		{"malformed JSON", `this is not json`, 0, http.StatusBadGateway, "unusable"},
		{"useless candidates", `{"candidates":[{"title":"","mediaType":"movie"}]}`, 0, http.StatusBadGateway, "unusable"},
		{"provider failure", `{}`, http.StatusInternalServerError, http.StatusBadGateway, "unavailable"},
		{"empty candidates", `{"candidates":[]}`, 0, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		fixture.set(tc.reply, tc.status)
		status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"genres":["Action"],"count":3}`,
			callOptions{user: testUserA, perms: readRecommendations})
		if status != tc.wantStatus {
			t.Errorf("%s: status %d, body %s; want %d", tc.name, status, raw, tc.wantStatus)
		}
		if tc.wantText != "" && !bytes.Contains(bytes.ToLower(raw), []byte(tc.wantText)) {
			t.Errorf("%s: body %s; want %q", tc.name, raw, tc.wantText)
		}
		if bytes.Contains(raw, []byte("synthetic-ai-secret")) || bytes.Contains(raw, []byte("Bearer")) {
			t.Errorf("%s: body leaked credentials: %s", tc.name, raw)
		}
		if tc.name == "empty candidates" && status == http.StatusCreated {
			rec := decode[discovery.Recommendation](t, raw)
			if len(rec.Candidates) != 0 || len(rec.Warnings) == 0 {
				t.Errorf("empty candidate run = %+v", rec)
			}
		}
	}
}

func TestRecommendationInputRequiresTaste(t *testing.T) {
	env := newEnvironment(t, envOptions{withActor: true, metadata: newOMDbFixture(t)})
	fixture := newAIFixture(t, `{"candidates":[]}`)
	configureAI(t, env, fixture, "synthetic-ai-secret")
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"mediaType":"movie"}`,
		callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusBadRequest {
		t.Fatalf("empty taste input: status %d, body %s; want 400", status, raw)
	}
	if status, raw := env.call(t, http.MethodPost, "/api/v1/recommendations", `{"mediaType":"tv","useHistory":true,"count":99}`,
		callOptions{user: testUserA, perms: readRecommendations}); status != http.StatusCreated {
		t.Fatalf("history-only input: status %d, body %s", status, raw)
	}
}
