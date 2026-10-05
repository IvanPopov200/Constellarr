package subtitles

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testSRT = "1\n00:00:01,000 --> 00:00:02,000\nHello\n"

func newTestProvider(t *testing.T, handler http.HandlerFunc) (*openSubtitles, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := newOpenSubtitles(Provider{
		ID: "os", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
		Endpoint: server.URL, Username: "user", Password: "hunter2", APIKey: "key-123",
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return provider, server
}

func TestOpenSubtitlesLoginSearchAndDownload(t *testing.T) {
	logins := 0
	var searches []map[string]string
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			logins++
			if got := r.Header.Get("Api-Key"); got != "key-123" {
				t.Errorf("login Api-Key = %q", got)
			}
			w.Write([]byte(`{"token":"tok-1","status":200}`))
		case "/subtitles":
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				t.Errorf("search authorization = %q", r.Header.Get("Authorization"))
			}
			searches = append(searches, map[string]string{
				"parent_imdb_id": r.URL.Query().Get("parent_imdb_id"),
				"type":           r.URL.Query().Get("type"),
				"season_number":  r.URL.Query().Get("season_number"),
				"episode_number": r.URL.Query().Get("episode_number"),
				"languages":      r.URL.Query().Get("languages"),
			})
			w.Write([]byte(`{"total_pages":1,"total_count":2,"data":[
				{"attributes":{"subtitle_id":"999","language":"en","download_count":1500,"hearing_impaired":false,"foreign_parts_only":false,"format":"srt","fps":23.976,"ratings":8.1,"release":"Show.S02E03.WEB","files":[{"file_id":42,"file_name":"show.s02e03.srt"}]}},
				{"attributes":{"language":"en","files":[]}}]}`))
		case "/download":
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				t.Errorf("download authorization = %q", r.Header.Get("Authorization"))
			}
			w.Write([]byte(`{"link":"` + "http://" + r.Host + `/files/42.srt","file_name":"show.s02e03.srt","requests":1,"remaining":19,"reset_time_utc":"2026-10-06 00:00:00"}`))
		case "/files/42.srt":
			w.Write([]byte(testSRT))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	results, err := provider.Search(ctx, SearchQuery{
		Type: KindEpisode, ParentIMDbID: "tt1234567", Season: 2, Episode: 3, Languages: []string{"en", "pt-BR"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if len(searches) != 1 || searches[0]["parent_imdb_id"] != "1234567" || searches[0]["type"] != "episode" ||
		searches[0]["season_number"] != "2" || searches[0]["episode_number"] != "3" || searches[0]["languages"] != "en,pt-BR" {
		t.Fatalf("search params: %v", searches)
	}
	if results[0].FileID != "42" || results[0].SubtitleID != "999" || results[0].Language != "en" ||
		results[0].FPS != 23.976 || results[0].Downloads != 1500 || results[0].MatchedBy != "imdb" {
		t.Fatalf("mapped result = %+v", results[0])
	}
	// A second search reuses the cached token instead of signing in again.
	if _, err := provider.Search(ctx, SearchQuery{Type: KindEpisode, ParentIMDbID: "tt1234567", Languages: []string{"en"}}); err != nil {
		t.Fatal(err)
	}
	if logins != 1 || len(searches) != 2 {
		t.Fatalf("logins %d searches %d", logins, len(searches))
	}
	downloaded, err := provider.Download(ctx, "42")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(downloaded.Data) != testSRT || downloaded.Format != FormatSRT || downloaded.Remaining != 19 {
		t.Fatalf("downloaded = %+v", downloaded)
	}
}

func TestOpenSubtitlesReloginsOnceAfterUnauthorized(t *testing.T) {
	logins, searches := 0, 0
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			logins++
			w.Write([]byte(`{"token":"tok-` + string(rune('0'+logins)) + `"}`))
		case "/subtitles":
			searches++
			if searches == 1 {
				// The cached token was revoked; the client must sign in again and retry once.
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"message":"token expired"}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer tok-2" {
				t.Errorf("retry authorization = %q", r.Header.Get("Authorization"))
			}
			w.Write([]byte(`{"data":[]}`))
		}
	})
	if _, err := provider.Search(context.Background(), SearchQuery{Type: KindMovie, IMDbID: "tt0133093", Languages: []string{"en"}}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if logins != 2 || searches != 2 {
		t.Fatalf("logins %d searches %d", logins, searches)
	}
}

func TestOpenSubtitlesErrorsAreBoundedAndSanitized(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			w.Write([]byte(`{"token":"tok-1"}`))
		case "/subtitles":
			w.WriteHeader(http.StatusNotAcceptable)
			w.Write([]byte(`{"message":"You have downloaded your allowed 20 subtitles today with key-123"}`))
		case "/download":
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"too many requests"}`))
		}
	})
	ctx := context.Background()
	_, err := provider.Search(ctx, SearchQuery{Type: KindMovie, IMDbID: "tt0133093", Languages: []string{"en"}})
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("quota error = %v", err)
	}
	if strings.Contains(err.Error(), "key-123") {
		t.Fatalf("API key leaked in error: %v", err)
	}
	_, err = provider.Download(ctx, "42")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
}

func TestOpenSubtitlesDownloadValidatesPayload(t *testing.T) {
	payload := ""
	var server *httptest.Server
	provider, server := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			w.Write([]byte(`{"token":"tok-1"}`))
		case "/download":
			w.Write([]byte(`{"link":"` + server.URL + `/file"}`))
		case "/file":
			w.Write([]byte(payload))
		}
	})
	if _, err := provider.Download(context.Background(), "not-a-number"); err == nil {
		t.Fatal("non-numeric file ID must be rejected before the request")
	}
	payload = string([]byte{0xff, 0xfe, 0x00, 0x41})
	if _, err := provider.Download(context.Background(), "42"); err == nil {
		t.Fatal("non-UTF8 payload must be rejected")
	}
	payload = "not a subtitle"
	if _, err := provider.Download(context.Background(), "42"); err == nil {
		t.Fatal("unparseable payload must be rejected")
	}
}

func TestScoreResultRanksWantedVariant(t *testing.T) {
	want := LanguagePreference{Code: "en"}
	plain := Result{Language: "en", Format: "srt", Downloads: 2000, Rating: 9}
	hi := Result{Language: "en", Format: "srt", HI: true, Downloads: 3000}
	forced := Result{Language: "en", Format: "srt", Forced: true}
	if scoreResult(want, plain) <= scoreResult(want, hi) {
		t.Fatal("plain subtitles must outrank hearing-impaired ones when HI is not wanted")
	}
	if scoreResult(want, forced) != -1 {
		t.Fatal("forced subtitles must not satisfy a plain request")
	}
	if scoreResult(LanguagePreference{Code: "pt-BR"}, Result{Language: "pt"}) < 0 {
		t.Fatal("base language must match a regional request")
	}
	if scoreResult(want, Result{Language: "en", Forced: true}) != -1 {
		t.Fatal("forced result must not match a non-forced request")
	}
	if scoreResult(LanguagePreference{Code: "en", Forced: true}, Result{Language: "en", Forced: true}) <= 0 {
		t.Fatal("forced result must match a forced request")
	}
	if scoreResult(want, Result{Language: "de"}) != -1 {
		t.Fatal("other languages must not match")
	}
}

func TestNumericIMDb(t *testing.T) {
	for input, want := range map[string]string{"tt0133093": "133093", "tt12345678": "12345678", "31452": "31452"} {
		if got := numericIMDb(input); got != want {
			t.Fatalf("numericIMDb(%q) = %q", input, got)
		}
	}
}

func TestProviderTestWithoutCredentialsUsesAnonymousSearch(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			t.Error("anonymous test must not sign in")
		case "/subtitles":
			w.Write([]byte(`{"data":[]}`))
		}
	})
	provider.username, provider.password = "", ""
	message, err := provider.Test(context.Background())
	if err != nil || !strings.Contains(message, "API key accepted") {
		t.Fatalf("test = %q, %v", message, err)
	}
	provider.apiKey = ""
	if _, err := provider.Test(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing key error = %v", err)
	}
}

// Two-listener regression: a credential header must never reach a different origin.
func TestOpenSubtitlesRefusesCrossOriginRedirectCarryingSecret(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "" || r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		leaked.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/subtitles", http.StatusFound)
	}))
	defer origin.Close()
	provider, err := newOpenSubtitles(Provider{
		ID: "os", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
		Endpoint: origin.URL, APIKey: "key-123",
	}, origin.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), SearchQuery{Type: KindMovie, IMDbID: "tt0133093", Languages: []string{"en"}}); err == nil {
		t.Fatal("a cross-origin redirect carrying the API key must fail")
	}
	if leaked.Load() != 0 {
		t.Fatalf("the redirected request reached the other origin %d time(s)", leaked.Load())
	}
}

// The same-origin case keeps working, including the credential header.
func TestOpenSubtitlesFollowsSameOriginRedirect(t *testing.T) {
	var withKey atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("redirected") != "1" {
			http.Redirect(w, r, r.URL.Path+"?redirected=1", http.StatusFound)
			return
		}
		if r.Header.Get("Api-Key") == "key-123" {
			withKey.Add(1)
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	provider, err := newOpenSubtitles(Provider{
		ID: "os", Name: "OpenSubtitles", Type: providerTypeOpenSubtitles,
		Endpoint: server.URL, APIKey: "key-123",
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), SearchQuery{Type: KindMovie, IMDbID: "tt0133093", Languages: []string{"en"}}); err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	if withKey.Load() != 1 {
		t.Fatalf("redirected same-origin request carried the key %d time(s)", withKey.Load())
	}
}

func TestGuardedRedirectPolicyIsFiniteAndCredentialScoped(t *testing.T) {
	policy := guardedRedirects(nil)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://api.example/v1/subtitles", nil)}
	via[0].Header.Set("Api-Key", "key-123")
	request := httptest.NewRequest(http.MethodGet, "https://cdn.example/subtitles", nil)
	if err := policy(request, via); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("cross-origin credential redirect error = %v", err)
	}
	anonymous := httptest.NewRequest(http.MethodGet, "https://api.example/v1/subtitles", nil)
	if err := policy(request, []*http.Request{anonymous}); err != nil {
		t.Fatalf("anonymous redirect must be allowed: %v", err)
	}
	chain := make([]*http.Request, 0, maxProviderRedirects)
	for i := 0; i < maxProviderRedirects; i++ {
		chain = append(chain, httptest.NewRequest(http.MethodGet, "https://api.example/v1/subtitles", nil))
	}
	if err := policy(request, chain); err == nil {
		t.Fatal("too many redirects must fail")
	}
}
