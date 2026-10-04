package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

func TestTVAPICatalogMonitoringAndCalendar(t *testing.T) {
	ctx := context.Background()
	pool, manager, movieService, _ := movieEnvironment(t, downloads.Config{})
	nextAirDate := time.Now().UTC().AddDate(0, 0, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("apikey") != "fixture-key" {
			t.Error("provider key was not forwarded")
			w.WriteHeader(403)
			return
		}
		if r.URL.Query().Get("Season") != "" {
			fmt.Fprintf(w, `{"Title":"Fixture Series","Season":"1","seriesID":"tt1234567","Episodes":[{"Title":"Pilot","Episode":"1","Released":"2020-01-02","imdbID":"tt1234568","imdbRating":"8.2"},{"Title":"Return","Episode":"2","Released":"%s","imdbID":"tt1234569","imdbRating":"N/A"}],"Response":"True"}`, nextAirDate.Format("2006-01-02"))
			return
		}
		fmt.Fprint(w, `{"Title":"Fixture Series","Year":"2020–2030","imdbID":"tt1234567","Type":"series","totalSeasons":"1","Poster":"https://example.com/poster.jpg","Response":"True"}`)
	}))
	defer provider.Close()
	connections, err := movieService.Store.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connections.MetadataURL, connections.MetadataAPIKey = provider.URL, "fixture-key"
	if _, err := movieService.Store.SaveConfig(ctx, connections); err != nil {
		t.Fatal(err)
	}
	service, err := tv.New(ctx, pool, manager, movieService.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	handler := api.New(pool, api.Services{Downloads: manager, Movies: movieService, TV: service})
	status, raw := request(t, handler, http.MethodPost, "/api/v1/tv", `{"imdbId":"tt1234567","monitored":true,"monitorMode":"all"}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("add series: %d %s", status, raw)
	}
	var series tv.Series
	if err := json.Unmarshal(raw, &series); err != nil {
		t.Fatal(err)
	}
	if series.ID == "" || series.Metadata.Type != "series" || !strings.HasPrefix(series.Metadata.Poster, "/api/v1/movie-poster?") || strings.Contains(string(raw), "fixture-key") {
		t.Fatalf("series response did not preserve identity and private poster contract: %s", raw)
	}
	path := "/api/v1/tv/" + series.ID
	status, raw = request(t, handler, http.MethodPost, path+"/refresh", `{}`, nil)
	if status != http.StatusOK {
		t.Fatalf("refresh series: %d %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, path, "", nil)
	if status != http.StatusOK {
		t.Fatalf("read series: %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &series); err != nil {
		t.Fatal(err)
	}
	if len(series.Episodes) != 2 || series.Episodes[0].Season != 1 {
		t.Fatalf("episode catalog: %s", raw)
	}
	status, raw = request(t, handler, http.MethodDelete, "/api/v1/movie-profiles/"+series.ProfileID, "", nil)
	if status != http.StatusConflict {
		t.Fatalf("TV profile deletion must conflict: %d %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/tv/calendar", "", nil)
	var calendar []tv.CalendarEntry
	if status != http.StatusOK || json.Unmarshal(raw, &calendar) != nil || len(calendar) != 1 {
		t.Fatalf("calendar entries: %d %s", status, raw)
	}
	for _, entry := range calendar {
		if entry.SeriesIMDbID != "tt1234567" || !strings.HasPrefix(entry.Poster, "/api/v1/movie-poster?") {
			t.Fatalf("calendar must use the private series poster proxy: %s", raw)
		}
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/tv/calendar.ics", "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "DTSTART;VALUE=DATE:"+nextAirDate.Format("20060102")) {
		t.Fatalf("air dates should remain dates in calendar: %d %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodPost, path+"/monitor", `{"season":1,"monitored":false}`, nil)
	if status != http.StatusOK {
		t.Fatalf("monitor season: %d %s", status, raw)
	}
	var updated tv.Series
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatal(err)
	}
	for _, episode := range updated.Episodes {
		if episode.Monitored {
			t.Errorf("episode %s still monitored", episode.ID)
		}
	}
	status, raw = request(t, handler, http.MethodDelete, path, "", nil)
	if status != http.StatusOK {
		t.Fatalf("remove series: %d %s", status, raw)
	}
	status, _ = request(t, handler, http.MethodGet, path, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("deleted series returned %d", status)
	}
}

func TestTVAPIRejectsMalformedAndCrossSiteWrites(t *testing.T) {
	ctx := context.Background()
	pool, manager, movieService, _ := movieEnvironment(t, downloads.Config{})
	service, err := tv.New(ctx, pool, manager, movieService.Store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	handler := api.New(pool, api.Services{Downloads: manager, Movies: movieService, TV: service})
	for _, body := range []string{`{"privateKey":"x"}`, `{} {}`, strings.Repeat(" ", 256<<10) + `{}`} {
		status, raw := request(t, handler, http.MethodPost, "/api/v1/tv", body, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("malformed TV body: %d %s", status, raw)
		}
	}
	status, raw := request(t, handler, http.MethodPost, "/api/v1/tv", `{}`, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if status != http.StatusForbidden {
		t.Fatalf("cross-site TV write: %d %s", status, raw)
	}
}
