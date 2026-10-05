package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

type upstreamError struct {
	app     App
	status  int
	message string
}

func (e *upstreamError) Error() string { return e.message }

func (e *upstreamError) retryable() bool {
	return e.status == http.StatusTooManyRequests || e.status >= 500
}

// request performs one bounded call, retrying transient failures and limiting the response size.
func (s *Service) request(ctx context.Context, connection Connection, method, target string, headers map[string]string, payload []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	ctx = withCredentials(ctx, connection)
	if strings.HasPrefix(target, "/") {
		target = connection.URL + target
	}
	var lastErr error
	for attempt := 0; attempt <= s.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return &upstreamError{app: connection.App, message: fmt.Sprintf("%s: %s", connection.App, requestFailure(ctx.Err()))}
			case <-time.After(s.retryDelay * time.Duration(attempt)):
			}
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return &upstreamError{app: connection.App, message: fmt.Sprintf("%s: the request could not be built", connection.App)}
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if connection.Username != "" || connection.Password != "" {
			req.SetBasicAuth(connection.Username, connection.Password)
		}
		resp, err := s.http.Do(req)
		if err != nil {
			lastErr = &upstreamError{app: connection.App, message: fmt.Sprintf("%s: %s", connection.App, requestFailure(err))}
			if ctx.Err() != nil || redirectRefused(err) {
				return lastErr
			}
			continue
		}
		err = readResponse(resp, connection.App, s.maxResponse, out)
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.retryable() {
			lastErr = err
			continue
		}
		return err
	}
	return lastErr
}

// readResponse never includes URLs, request bodies, or credentials in its errors.
func readResponse(resp *http.Response, app App, limit int64, out any) error {
	defer resp.Body.Close()
	fail := func(message string) error {
		return &upstreamError{app: app, status: resp.StatusCode, message: fmt.Sprintf("%s: %s", app, message)}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fail("authorization failed; check the API key or credentials")
	case resp.StatusCode == http.StatusNotFound:
		return fail("the endpoint was not found; check the URL and application version")
	case resp.StatusCode >= 400:
		return fail(fmt.Sprintf("the request was rejected with status %d", resp.StatusCode))
	}
	data, err := readLimited(resp.Body, limit)
	if err != nil {
		return fail(err.Error())
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fail("the response was not understood")
	}
	return nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("the response could not be read")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the response exceeded the supported size")
	}
	return data, nil
}

// requestFailure keeps host names and URLs out of error text while staying diagnosable.
func requestFailure(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	switch {
	case errors.Is(err, errRedirectOrigin), errors.Is(err, errRedirectScheme), errors.Is(err, errRedirectHops):
		return err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "the request timed out"
	case errors.Is(err, context.Canceled):
		return "the request was canceled"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "the host could not be resolved"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "the request timed out"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "the connection failed"
	}
	return "the request failed"
}

const maxRedirects = 3

var (
	errRedirectOrigin = errors.New("the application redirected credentials to another host; use its final URL instead")
	errRedirectScheme = errors.New("the application redirected to an insecure address")
	errRedirectHops   = errors.New("the application redirected too many times")
)

type credentialContextKey struct{}

// withCredentials marks a request that carries credentials, so redirects cannot hand them to another host.
func withCredentials(ctx context.Context, connection Connection) context.Context {
	authenticated := connection.APIKey != "" || connection.Username != "" || connection.Password != ""
	return context.WithValue(ctx, credentialContextKey{}, authenticated)
}

// checkRedirect refuses cross-origin authenticated redirects, scheme downgrades, and long chains.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errRedirectHops
	}
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1]
	if previous.URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errRedirectScheme
	}
	if authenticated, _ := req.Context().Value(credentialContextKey{}).(bool); authenticated && !sameOrigin(previous.URL, req.URL) {
		return errRedirectOrigin
	}
	return nil
}

// redirectRefused reports a permanent redirect refusal, which must not be retried.
func redirectRefused(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return errors.Is(err, errRedirectOrigin) || errors.Is(err, errRedirectScheme) || errors.Is(err, errRedirectHops)
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (s *Service) requestJSON(ctx context.Context, connection Connection, method, target string, headers map[string]string, in any, out any) error {
	var payload []byte
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return &upstreamError{app: connection.App, message: fmt.Sprintf("%s: the request could not be encoded", connection.App)}
		}
		payload = encoded
	}
	return s.request(ctx, connection, method, target, headers, payload, out)
}

type arrQualityItem struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Allowed bool   `json:"allowed"`
	Quality struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"quality"`
	Items []arrQualityItem `json:"items"`
}

type arrProfile struct {
	ID             int              `json:"id"`
	Name           string           `json:"name"`
	UpgradeAllowed bool             `json:"upgradeAllowed"`
	Cutoff         int              `json:"cutoff"`
	Items          []arrQualityItem `json:"items"`
}

type arrStatus struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

type arrRoot struct {
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
}

type arrTag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

type arrImage struct {
	CoverType string `json:"coverType"`
	RemoteURL string `json:"remoteUrl"`
	URL       string `json:"url"`
}

type arrMovie struct {
	ID               int    `json:"id"`
	Title            string `json:"title"`
	Year             int    `json:"year"`
	IMDbID           string `json:"imdbId"`
	Monitored        bool   `json:"monitored"`
	Tags             []int  `json:"tags"`
	QualityProfileID int    `json:"qualityProfileId"`
	RootFolderPath   string `json:"rootFolderPath"`
	Path             string `json:"path"`
	HasFile          bool   `json:"hasFile"`
	Overview         string `json:"overview"`
	MovieFile        *struct {
		RelativePath string `json:"relativePath"`
		Path         string `json:"path"`
		Size         int64  `json:"size"`
	} `json:"movieFile"`
	Images []arrImage `json:"images"`
}

type arrSeries struct {
	ID               int    `json:"id"`
	Title            string `json:"title"`
	Year             int    `json:"year"`
	IMDbID           string `json:"imdbId"`
	TVDBID           int    `json:"tvdbId"`
	Monitored        bool   `json:"monitored"`
	MonitorNewItems  string `json:"monitorNewItems"`
	Tags             []int  `json:"tags"`
	QualityProfileID int    `json:"qualityProfileId"`
	RootFolderPath   string `json:"rootFolderPath"`
	Path             string `json:"path"`
	Overview         string `json:"overview"`
	Seasons          []struct {
		SeasonNumber int  `json:"seasonNumber"`
		Monitored    bool `json:"monitored"`
	} `json:"seasons"`
	Statistics struct {
		EpisodeFileCount int `json:"episodeFileCount"`
	} `json:"statistics"`
	Images []arrImage `json:"images"`
}

type arrEpisodeFile struct {
	ID           int    `json:"id"`
	SeriesID     int    `json:"seriesId"`
	RelativePath string `json:"relativePath"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
}

type arrNaming struct {
	MovieFolderFormat     string `json:"movieFolderFormat"`
	StandardMovieFormat   string `json:"standardMovieFormat"`
	SeriesFolderFormat    string `json:"seriesFolderFormat"`
	SeasonFolderFormat    string `json:"seasonFolderFormat"`
	StandardEpisodeFormat string `json:"standardEpisodeFormat"`
}

func (s *Service) discoverRadarr(ctx context.Context, connection Connection) (snapshot, error) {
	headers := map[string]string{"X-Api-Key": connection.APIKey}
	var status arrStatus
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/system/status", headers, nil, &status); err != nil {
		return snapshot{}, err
	}
	var roots []arrRoot
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/rootfolder", headers, nil, &roots); err != nil {
		return snapshot{}, err
	}
	var profiles []arrProfile
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/qualityprofile", headers, nil, &profiles); err != nil {
		return snapshot{}, err
	}
	var tags []arrTag
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/tag", headers, nil, &tags); err != nil {
		return snapshot{}, err
	}
	var movies []arrMovie
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/movie", headers, nil, &movies); err != nil {
		return snapshot{}, err
	}
	labels := tagLabels(tags)
	out := snapshot{Versions: map[App]string{AppRadarr: status.Version}}
	for _, root := range roots {
		out.Roots = append(out.Roots, RootPlan{Source: AppRadarr, Media: MediaMovies, Path: root.Path, Accessible: root.Accessible})
	}
	out.Profiles = append(out.Profiles, arrProfiles(AppRadarr, profiles)...)
	for _, movie := range movies {
		item := MoviePlan{
			Source: AppRadarr, ID: movie.ID, Title: movie.Title, Year: movie.Year,
			IMDbID: strings.ToLower(strings.TrimSpace(movie.IMDbID)), Monitored: movie.Monitored,
			Tags: labelsFor(labels, movie.Tags), ProfileKey: profileKey(AppRadarr, movie.QualityProfileID),
			RootPath: movie.RootFolderPath, Plot: movie.Overview, Poster: posterURL(movie.Images),
		}
		if movie.HasFile && movie.MovieFile != nil {
			item.FilePath, item.FileSize = sourceFile(movie.RootFolderPath, movie.Path, movie.MovieFile.Path, movie.MovieFile.RelativePath, movie.MovieFile.Size)
		}
		out.Movies = append(out.Movies, item)
	}
	var naming arrNaming
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/config/naming", headers, nil, &naming); err != nil {
		out.Warnings = append(out.Warnings, sanitize("Radarr naming settings could not be read: "+err.Error(), connection.secrets()))
	} else {
		out.Naming = append(out.Naming, movieNaming(AppRadarr, naming.MovieFolderFormat, naming.StandardMovieFormat))
	}
	return out, nil
}

func (s *Service) discoverSonarr(ctx context.Context, connection Connection) (snapshot, error) {
	headers := map[string]string{"X-Api-Key": connection.APIKey}
	var status arrStatus
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/system/status", headers, nil, &status); err != nil {
		return snapshot{}, err
	}
	var roots []arrRoot
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/rootfolder", headers, nil, &roots); err != nil {
		return snapshot{}, err
	}
	var profiles []arrProfile
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/qualityprofile", headers, nil, &profiles); err != nil {
		return snapshot{}, err
	}
	var tags []arrTag
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/tag", headers, nil, &tags); err != nil {
		return snapshot{}, err
	}
	var series []arrSeries
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/series", headers, nil, &series); err != nil {
		return snapshot{}, err
	}
	files, missingFiles := s.seriesEpisodeFiles(ctx, connection, headers, series)
	labels := tagLabels(tags)
	out := snapshot{Versions: map[App]string{AppSonarr: status.Version}}
	if missingFiles > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("Sonarr episode files for %d series could not be read", missingFiles))
	}
	for _, root := range roots {
		out.Roots = append(out.Roots, RootPlan{Source: AppSonarr, Media: MediaTV, Path: root.Path, Accessible: root.Accessible})
	}
	out.Profiles = append(out.Profiles, arrProfiles(AppSonarr, profiles)...)
	for _, show := range series {
		item := SeriesPlan{
			Source: AppSonarr, ID: show.ID, Title: show.Title, Year: show.Year,
			IMDbID: strings.ToLower(strings.TrimSpace(show.IMDbID)), TVDBID: show.TVDBID, Monitored: show.Monitored,
			MonitorMode: sonarrMonitorMode(show.MonitorNewItems), Tags: labelsFor(labels, show.Tags),
			ProfileKey: profileKey(AppSonarr, show.QualityProfileID), RootPath: show.RootFolderPath,
			Plot: show.Overview, Poster: posterURL(show.Images),
		}
		for _, season := range show.Seasons {
			if season.SeasonNumber > 0 && !season.Monitored {
				item.UnmonitoredSeasons = append(item.UnmonitoredSeasons, season.SeasonNumber)
			}
		}
		for _, file := range files[show.ID] {
			relPath, size := sourceFile(show.RootFolderPath, show.Path, file.Path, file.RelativePath, file.Size)
			episodeFile := EpisodeFilePlan{Key: fmt.Sprintf("%d:%d", show.ID, file.ID), RelativePath: relPath, Size: size}
			if identity, ok := library.ParseEpisode(relPath); ok && identity.Season > 0 && len(identity.Numbers) > 0 {
				episodeFile.Season, episodeFile.Numbers = identity.Season, identity.Numbers
			} else {
				episodeFile.Unsupported = "episode numbers could not be read from this file name"
			}
			item.Files = append(item.Files, episodeFile)
		}
		out.Series = append(out.Series, item)
	}
	var naming arrNaming
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/config/naming", headers, nil, &naming); err != nil {
		out.Warnings = append(out.Warnings, sanitize("Sonarr naming settings could not be read: "+err.Error(), connection.secrets()))
	} else {
		out.Naming = append(out.Naming, seriesNaming(AppSonarr, naming.SeriesFolderFormat, naming.SeasonFolderFormat, naming.StandardEpisodeFormat))
	}
	return out, nil
}

// seriesEpisodeFiles reads per-series files; Sonarr rejects the bulk endpoint without a series id.
func (s *Service) seriesEpisodeFiles(ctx context.Context, connection Connection, headers map[string]string, series []arrSeries) (map[int][]arrEpisodeFile, int) {
	filesBySeries := make(map[int][]arrEpisodeFile, len(series))
	missing := 0
	var mu sync.Mutex
	var group sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, show := range series {
		if show.Statistics.EpisodeFileCount == 0 {
			continue
		}
		group.Add(1)
		go func(show arrSeries) {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			var files []arrEpisodeFile
			if err := s.requestJSON(ctx, connection, http.MethodGet,
				fmt.Sprintf("/api/v3/episodefile?seriesId=%d", show.ID), headers, nil, &files); err != nil {
				mu.Lock()
				missing++
				mu.Unlock()
				return
			}
			mu.Lock()
			filesBySeries[show.ID] = files
			mu.Unlock()
		}(show)
	}
	group.Wait()
	return filesBySeries, missing
}

func tagLabels(tags []arrTag) map[int]string {
	labels := make(map[int]string, len(tags))
	for _, tag := range tags {
		labels[tag.ID] = strings.TrimSpace(tag.Label)
	}
	return labels
}

func labelsFor(labels map[int]string, ids []int) []string {
	var out []string
	for _, id := range ids {
		if label := labels[id]; label != "" {
			out = append(out, label)
		}
	}
	return out
}

func posterURL(images []arrImage) string {
	for _, image := range images {
		if image.CoverType != "poster" {
			continue
		}
		if image.RemoteURL != "" {
			return image.RemoteURL
		}
		if image.URL != "" {
			return image.URL
		}
	}
	return ""
}

func profileKey(app App, id int) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", app, id)
}

// sourceFile prefers the absolute file path and falls back to the folder plus relative name.
func sourceFile(rootPath, folderPath, filePath, relativePath string, size int64) (string, int64) {
	if rel := relativeSourcePath(rootPath, filePath); rel != "" {
		return rel, size
	}
	if folder := relativeSourcePath(rootPath, folderPath); folder != "" && relativePath != "" {
		return path.Join(folder, filepath.ToSlash(relativePath)), size
	}
	return "", size
}

func relativeSourcePath(root, target string) string {
	if target == "" {
		return ""
	}
	rootSlash := strings.TrimRight(filepath.ToSlash(root), "/")
	targetSlash := filepath.ToSlash(target)
	if rootSlash == "" || !strings.HasPrefix(targetSlash, rootSlash+"/") {
		return ""
	}
	rel := strings.TrimPrefix(targetSlash, rootSlash+"/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return ""
	}
	return rel
}

func sonarrMonitorMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "none":
		return "none"
	case "new":
		return "future"
	default:
		return "all"
	}
}

func arrProfiles(app App, profiles []arrProfile) []ProfilePlan {
	out := make([]ProfilePlan, 0, len(profiles))
	for _, profile := range profiles {
		plan := ProfilePlan{Source: app, Key: profileKey(app, profile.ID), Name: profile.Name, Upgrade: profile.UpgradeAllowed}
		seen := map[string]bool{}
		for _, quality := range arrQualities(profile.Items) {
			mapped, ok := mapQualityName(quality)
			if !ok {
				if !seen["q:"+quality] {
					plan.Unsupported = append(plan.Unsupported, quality)
					seen["q:"+quality] = true
				}
				continue
			}
			if !seen["m:"+mapped] {
				plan.Qualities = append(plan.Qualities, mapped)
				seen["m:"+mapped] = true
			}
		}
		sortQualities(plan.Qualities)
		plan.Cutoff = cutoffQuality(plan.Qualities, arrCutoffName(profile))
		out = append(out, plan)
	}
	return out
}

func arrQualities(items []arrQualityItem) []string {
	var out []string
	for _, item := range items {
		if !item.Allowed {
			continue
		}
		if item.Quality.Name != "" {
			out = append(out, item.Quality.Name)
		}
		out = append(out, arrQualities(item.Items)...)
	}
	return out
}

func arrCutoffName(profile arrProfile) string {
	for _, item := range profile.Items {
		// Plain items carry the quality id inside the quality object; groups carry their own id.
		if item.Quality.ID != profile.Cutoff && item.ID != profile.Cutoff {
			continue
		}
		if item.Quality.Name != "" {
			return item.Quality.Name
		}
		if children := arrQualities([]arrQualityItem{item}); len(children) > 0 {
			return children[len(children)-1]
		}
	}
	return ""
}

// cutoffQuality maps a source cutoff onto the allowed set, falling back to the least preferred quality.
func cutoffQuality(qualities []string, name string) string {
	if mapped, ok := mapQualityName(name); ok {
		for _, quality := range qualities {
			if quality == mapped {
				return mapped
			}
		}
	}
	if len(qualities) == 0 {
		return ""
	}
	return qualities[len(qualities)-1]
}

var qualityAliases = map[string]string{
	"remux-2160p":  "Remux-2160p",
	"bluray-2160p": "Bluray-2160p", "brdisk": "Bluray-2160p",
	"webdl-2160p": "WEB-2160p", "webrip-2160p": "WEB-2160p", "web-dl-2160p": "WEB-2160p",
	"hdtv-2160p":   "HDTV-2160p",
	"remux-1080p":  "Remux-1080p",
	"bluray-1080p": "Bluray-1080p", "brscr": "Bluray-1080p",
	"webdl-1080p": "WEB-1080p", "webrip-1080p": "WEB-1080p", "web-dl-1080p": "WEB-1080p",
	"hdtv-1080p":  "HDTV-1080p",
	"remux-720p":  "Remux-720p",
	"bluray-720p": "Bluray-720p",
	"webdl-720p":  "WEB-720p", "webrip-720p": "WEB-720p", "web-dl-720p": "WEB-720p",
	"hdtv-720p":   "HDTV-720p",
	"bluray-480p": "Bluray-480p", "bluray-576p": "Bluray-480p",
	"webdl-480p": "WEB-480p", "webrip-480p": "WEB-480p", "web-dl-480p": "WEB-480p",
	"hdtv-480p": "HDTV-480p",
	"dvd":       "DVD", "dvd-r": "DVD", "dvdscr": "DVD", "sdtv": "SD",
}

// qualityOrder is best to least preferred, matching the shared quality profile order.
var qualityOrder = map[string]int{
	"Remux-2160p": 0, "Bluray-2160p": 1, "WEB-2160p": 2, "HDTV-2160p": 3,
	"Remux-1080p": 4, "Bluray-1080p": 5, "WEB-1080p": 6, "HDTV-1080p": 7,
	"Remux-720p": 8, "Bluray-720p": 9, "WEB-720p": 10, "HDTV-720p": 11,
	"Bluray-480p": 12, "WEB-480p": 13, "HDTV-480p": 14, "DVD": 15, "SD": 16,
}

func mapQualityName(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, " ", "")
	mapped, ok := qualityAliases[key]
	return mapped, ok
}

func sortQualities(qualities []string) {
	sort.Slice(qualities, func(i, j int) bool { return qualityOrder[qualities[i]] < qualityOrder[qualities[j]] })
}

type bazarrSettings struct {
	General struct {
		SingleLanguage    bool     `json:"single_language"`
		LanguageEquals    []string `json:"language_equals"`
		AdaptiveSearching bool     `json:"adaptive_searching"`
		MinimumScore      int      `json:"minimum_score"`
		MinimumScoreMovie int      `json:"minimum_score_movie"`
		UpgradeSubs       bool     `json:"upgrade_subs"`
		UpgradeFrequency  int      `json:"upgrade_frequency"`
		DaysToUpgradeSubs int      `json:"days_to_upgrade_subs"`
		EnabledProviders  []string `json:"enabled_providers"`
		PathMappings      []string `json:"path_mappings"`
		PathMappingsMovie []string `json:"path_mappings_movie"`
		SerieProfile      string   `json:"serie_default_profile"`
		MovieProfile      string   `json:"movie_default_profile"`
		WantedSearchHours int      `json:"wanted_search_frequency"`
		WantedMovieHours  int      `json:"wanted_search_frequency_movie"`
	} `json:"general"`
	Subsync struct {
		UseSubsync       bool `json:"use_subsync"`
		UseSubsyncMovie  bool `json:"use_subsync_movie"`
		MaxOffsetSeconds int  `json:"max_offset_seconds"`
		Threshold        int  `json:"subsync_threshold"`
		MovieThreshold   int  `json:"subsync_movie_threshold"`
		NoFixFramerate   bool `json:"no_fix_framerate"`
		GSS              bool `json:"gss"`
	} `json:"subsync"`
	OpenSubtitlesCom struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		UseHash        bool   `json:"use_hash"`
		IncludeAI      bool   `json:"include_ai_translated"`
		IncludeMachine bool   `json:"include_machine_translated"`
	} `json:"opensubtitlescom"`
}

type bazarrProfile struct {
	ProfileID      string   `json:"profileId"`
	Name           string   `json:"name"`
	Cutoff         string   `json:"cutoff"`
	MustContain    []string `json:"mustContain"`
	MustNotContain []string `json:"mustNotContain"`
	Items          []struct {
		Language string `json:"language"`
		Forced   bool   `json:"forced"`
		HI       bool   `json:"hi"`
	} `json:"items"`
}

// discoverBazarr reads languages, profiles, scoring, sync limits, and the OpenSubtitles account.
func (s *Service) discoverBazarr(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	headers := map[string]string{"X-API-KEY": connection.APIKey}
	var status struct {
		Data struct {
			BazarrVersion string `json:"bazarr_version"`
		} `json:"data"`
	}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/system/status", headers, nil, &status); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var languages []struct {
		Name    string `json:"name"`
		Code2   string `json:"code2"`
		Enabled bool   `json:"enabled"`
	}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/system/languages?enabled=true", headers, nil, &languages); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var profiles []bazarrProfile
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/system/languages/profiles", headers, nil, &profiles); err != nil {
		// Older Bazarr builds have no profile endpoint; enabled languages still describe the intent.
		profiles = nil
	}
	var settings bazarrSettings
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/system/settings", headers, nil, &settings); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	out := snapshot{Subtitles: &SubtitlePlan{
		Source: AppBazarr, Version: status.Data.BazarrVersion,
		SingleLanguage: settings.General.SingleLanguage, LanguageEquals: settings.General.LanguageEquals,
		MinimumScore: settings.General.MinimumScore, MinimumScoreMovie: settings.General.MinimumScoreMovie,
		UpgradeSubtitles: settings.General.UpgradeSubs, AdaptiveSearching: settings.General.AdaptiveSearching,
		SearchHours: settings.General.WantedSearchHours, DefaultProfile: settings.General.SerieProfile,
		MovieProfile: settings.General.MovieProfile, Providers: settings.General.EnabledProviders,
		PathMappings: append(append([]string{}, settings.General.PathMappings...), settings.General.PathMappingsMovie...),
		Sync: SubtitleSyncPlan{
			Enabled: settings.Subsync.UseSubsync, Movies: settings.Subsync.UseSubsyncMovie,
			MaxOffsetSeconds: settings.Subsync.MaxOffsetSeconds, Threshold: settings.Subsync.Threshold,
			MovieThreshold: settings.Subsync.MovieThreshold, NoFixFramerate: settings.Subsync.NoFixFramerate,
			GoldenSection: settings.Subsync.GSS,
		},
	}}
	enabled := map[string]bool{}
	for _, language := range languages {
		if !language.Enabled {
			continue
		}
		code, ok := subtitles.NormalizeLanguage(language.Code2)
		if !ok {
			out.Warnings = append(out.Warnings, sanitize(fmt.Sprintf("Bazarr language %q has no Constellarr equivalent", language.Code2), connection.secrets()))
			continue
		}
		out.Subtitles.Languages = append(out.Subtitles.Languages, code)
		enabled[code] = true
	}
	sortStrings(out.Subtitles.Languages)
	for _, profile := range profiles {
		plan := SubtitleProfilePlan{Key: strings.TrimSpace(profile.ProfileID), Name: strings.TrimSpace(profile.Name)}
		if plan.Key == "" {
			plan.Key = plan.Name
		}
		for _, item := range profile.Items {
			code, ok := subtitles.NormalizeLanguage(item.Language)
			if !ok {
				plan.Unsupported = append(plan.Unsupported, fmt.Sprintf("language %q", item.Language))
				continue
			}
			plan.Languages = append(plan.Languages, SubtitleLanguagePlan{Code: code, Forced: item.Forced, HI: item.HI})
			if cutoff, ok := subtitles.NormalizeLanguage(profile.Cutoff); ok && cutoff == code {
				plan.Cutoff = len(plan.Languages)
			}
		}
		if len(plan.Languages) == 0 {
			plan.Unsupported = append(plan.Unsupported, "no supported languages")
		}
		if plan.Cutoff == 0 || plan.Cutoff > len(plan.Languages) {
			plan.Cutoff = len(plan.Languages)
		}
		if len(profile.MustContain) > 0 || len(profile.MustNotContain) > 0 {
			plan.Unsupported = append(plan.Unsupported, "profile must-contain and must-not-contain release rules")
		}
		out.Subtitles.Profiles = append(out.Subtitles.Profiles, plan)
	}
	if len(out.Subtitles.Profiles) == 0 && len(out.Subtitles.Languages) > 0 {
		cutoff := len(out.Subtitles.Languages)
		if settings.General.SingleLanguage {
			cutoff = 1
		}
		plan := SubtitleProfilePlan{Key: "bazarr", Name: "Bazarr languages", Cutoff: cutoff}
		for _, code := range out.Subtitles.Languages {
			plan.Languages = append(plan.Languages, SubtitleLanguagePlan{Code: code})
		}
		out.Subtitles.Profiles = append(out.Subtitles.Profiles, plan)
	}
	out.Subtitles.ProviderPlans = bazarrProviders(settings)
	secrets := providerSecrets{}
	if settings.OpenSubtitlesCom.Username != "" || settings.OpenSubtitlesCom.Password != "" {
		secrets.subtitle = &subtitleProviderSecret{Username: settings.OpenSubtitlesCom.Username, Password: settings.OpenSubtitlesCom.Password}
	}
	return out, secrets, nil
}

// bazarrProviders maps provider accounts that Constellarr can talk to and reports the rest.
func bazarrProviders(settings bazarrSettings) []SubtitleProviderPlan {
	supported := map[string]string{"opensubtitlescom": "opensubtitles"}
	names := map[string]string{"opensubtitlescom": "OpenSubtitles.com"}
	plans := make([]SubtitleProviderPlan, 0, len(settings.General.EnabledProviders))
	seen := map[string]bool{}
	for _, name := range settings.General.EnabledProviders {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		plan := SubtitleProviderPlan{Key: key, Name: firstNonEmpty(names[key], name)}
		if providerType, ok := supported[key]; !ok {
			plan.Unsupported = append(plan.Unsupported, "Constellarr has no adapter for this provider")
		} else {
			plan.Type = providerType
			if key == "opensubtitlescom" {
				plan.UsernameSet = settings.OpenSubtitlesCom.Username != ""
				plan.PasswordSet = settings.OpenSubtitlesCom.Password != ""
				if settings.OpenSubtitlesCom.UseHash {
					plan.Unsupported = append(plan.Unsupported, "hash-based matching")
				}
				if settings.OpenSubtitlesCom.IncludeAI || settings.OpenSubtitlesCom.IncludeMachine {
					plan.Unsupported = append(plan.Unsupported, "AI and machine-translated result filters")
				}
			}
		}
		plans = append(plans, plan)
	}
	return plans
}

type transmissionTorrent struct {
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	HashString    string   `json:"hashString"`
	MagnetLink    string   `json:"magnetLink"`
	Status        int      `json:"status"`
	PercentDone   float64  `json:"percentDone"`
	DownloadDir   string   `json:"downloadDir"`
	Labels        []string `json:"labels"`
	RateDownload  int64    `json:"rateDownload"`
	UploadRatio   float64  `json:"uploadRatio"`
	TotalSize     int64    `json:"totalSize"`
	LeftUntilDone int64    `json:"leftUntilDone"`
	AddedDate     int64    `json:"addedDate"`
	IsFinished    bool     `json:"isFinished"`
	Trackers      []struct {
		Announce string `json:"announce"`
	} `json:"trackers"`
	Files []struct {
		Name           string `json:"name"`
		Length         int64  `json:"length"`
		BytesCompleted int64  `json:"bytesCompleted"`
	} `json:"files"`
}

// discoverTransmission also reads the magnet link and file list needed to reuse local data.
func (s *Service) discoverTransmission(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	var sessionInfo struct {
		Arguments struct {
			Version            string  `json:"version"`
			DownloadDir        string  `json:"download-dir"`
			IncompleteDir      string  `json:"incomplete-dir"`
			SpeedLimitDown     int64   `json:"speed-limit-down"`
			SpeedLimitDownOn   bool    `json:"speed-limit-down-enabled"`
			SpeedLimitUp       int64   `json:"speed-limit-up"`
			SpeedLimitUpOn     bool    `json:"speed-limit-up-enabled"`
			SeedRatioLimit     float64 `json:"seedRatioLimit"`
			SeedRatioOn        bool    `json:"seedRatioLimited"`
			IdleSeedingMinutes int     `json:"idle-seeding-limit"`
			IdleSeedingOn      bool    `json:"idle-seeding-limit-enabled"`
			DHTEnabled         bool    `json:"dht-enabled"`
			PEXEnabled         bool    `json:"pex-enabled"`
			DownloadQueueSize  int     `json:"download-queue-size"`
		} `json:"arguments"`
	}
	if err := s.transmissionCall(ctx, connection, map[string]any{"method": "session-get"}, &sessionInfo); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var torrents struct {
		Arguments struct {
			Torrents []transmissionTorrent `json:"torrents"`
		} `json:"arguments"`
	}
	if err := s.transmissionCall(ctx, connection, map[string]any{
		"method": "torrent-get",
		"arguments": map[string]any{"fields": []string{
			"id", "name", "hashString", "magnetLink", "status", "percentDone", "downloadDir", "labels",
			"rateDownload", "uploadRatio", "totalSize", "leftUntilDone", "addedDate", "isFinished",
			"trackers", "files",
		}},
	}, &torrents); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	args := sessionInfo.Arguments
	plan := &TorrentPlan{
		Source: AppTransmission, Version: args.Version,
		DownloadDir: args.DownloadDir, IncompleteDir: args.IncompleteDir,
		SpeedLimitDown: args.SpeedLimitDown, SpeedLimitUp: args.SpeedLimitUp,
		SpeedDownEnabled: args.SpeedLimitDownOn, SpeedUpEnabled: args.SpeedLimitUpOn,
		SeedRatioLimit: args.SeedRatioLimit, SeedRatioLimited: args.SeedRatioOn,
		IdleSeedingMinutes: args.IdleSeedingMinutes, IdleSeedingLimited: args.IdleSeedingOn,
		DHTEnabled: args.DHTEnabled, PEXEnabled: args.PEXEnabled, DownloadQueueSize: args.DownloadQueueSize,
		Torrents: len(torrents.Arguments.Torrents),
	}
	labels := map[string]bool{}
	plan.Transfers = make([]TorrentTransfer, 0, len(torrents.Arguments.Torrents))
	for _, torrent := range torrents.Arguments.Torrents {
		transfer := TorrentTransfer{
			Name: torrent.Name, Hash: torrent.HashString, MagnetLink: torrent.MagnetLink, Status: torrent.Status,
			PercentDone: torrent.PercentDone, DownloadDir: torrent.DownloadDir, Labels: torrent.Labels,
			BytesDone: torrent.TotalSize - torrent.LeftUntilDone, TotalSize: torrent.TotalSize,
			UploadRatio: torrent.UploadRatio, AddedAt: time.Unix(torrent.AddedDate, 0).UTC(), Finished: torrent.IsFinished,
		}
		for _, label := range torrent.Labels {
			labels[label] = true
		}
		hosts := map[string]bool{}
		for _, tracker := range torrent.Trackers {
			if host := trackerHost(tracker.Announce); host != "" {
				hosts[host] = true
			}
		}
		for host := range hosts {
			transfer.TrackerHosts = append(transfer.TrackerHosts, host)
		}
		sortStrings(transfer.TrackerHosts)
		for _, file := range torrent.Files {
			transfer.Files = append(transfer.Files, TorrentFilePlan{Name: file.Name, Size: file.Length, Done: file.BytesCompleted})
		}
		plan.Transfers = append(plan.Transfers, transfer)
	}
	for label := range labels {
		plan.Labels = append(plan.Labels, label)
	}
	sortStrings(plan.Labels)
	return snapshot{Torrents: plan, Versions: map[App]string{AppTransmission: args.Version}}, providerSecrets{}, nil
}

// trackerHost keeps the tracker host only; announce URLs can embed private passkeys.
func trackerHost(announce string) string {
	parsed, err := url.Parse(strings.TrimSpace(announce))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// transmissionCall performs the RPC session handshake Transmission requires.
func (s *Service) transmissionCall(ctx context.Context, connection Connection, payload any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	ctx = withCredentials(ctx, connection)
	body, err := json.Marshal(payload)
	if err != nil {
		return &upstreamError{app: AppTransmission, message: "transmission: the request could not be encoded"}
	}
	session := ""
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, connection.URL+"/transmission/rpc", bytes.NewReader(body))
		if err != nil {
			return &upstreamError{app: AppTransmission, message: "transmission: the request could not be built"}
		}
		req.Header.Set("Content-Type", "application/json")
		if session != "" {
			req.Header.Set("X-Transmission-Session-Id", session)
		}
		if connection.Username != "" || connection.Password != "" {
			req.SetBasicAuth(connection.Username, connection.Password)
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return &upstreamError{app: AppTransmission, message: fmt.Sprintf("transmission: %s", requestFailure(err))}
		}
		if resp.StatusCode == http.StatusConflict {
			session = resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			if session == "" {
				return &upstreamError{app: AppTransmission, message: "transmission: the RPC session was rejected"}
			}
			continue
		}
		err = readResponse(resp, AppTransmission, s.maxResponse, out)
		var upstream *upstreamError
		if errors.As(err, &upstream) && upstream.retryable() {
			continue
		}
		return err
	}
	return &upstreamError{app: AppTransmission, message: "transmission: the RPC session could not be established"}
}

func (s *Service) discoverJellyfin(ctx context.Context, connection Connection) (*JellyfinPlan, error) {
	headers := map[string]string{"X-Emby-Token": connection.APIKey}
	var info struct {
		Version string `json:"Version"`
		Product string `json:"ProductName"`
	}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/System/Info", headers, nil, &info); err != nil {
		return nil, err
	}
	var folders []struct {
		CollectionType string   `json:"CollectionType"`
		Locations      []string `json:"Locations"`
	}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/Library/VirtualFolders", headers, nil, &folders); err != nil {
		return nil, err
	}
	plan := &JellyfinPlan{Version: info.Version, Product: info.Product, Libraries: map[string]int{}}
	for _, folder := range folders {
		kind := folder.CollectionType
		if kind == "" {
			kind = "mixed"
		}
		plan.Libraries[kind]++
		plan.Locations += len(folder.Locations)
	}
	return plan, nil
}

// connect checks one application, returning its version when the credentials work.
func (s *Service) connect(ctx context.Context, connection Connection) (string, error) {
	switch connection.App {
	case AppRadarr, AppSonarr:
		var status arrStatus
		err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v3/system/status", map[string]string{"X-Api-Key": connection.APIKey}, nil, &status)
		return status.Version, err
	case AppLidarr:
		var status arrStatus
		err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/system/status", map[string]string{"X-Api-Key": connection.APIKey}, nil, &status)
		return status.Version, err
	case AppProwlarr:
		var status arrStatus
		err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/system/status", map[string]string{"X-Api-Key": connection.APIKey}, nil, &status)
		return status.Version, err
	case AppBazarr:
		var status struct {
			Data struct {
				BazarrVersion string `json:"bazarr_version"`
			} `json:"data"`
		}
		err := s.requestJSON(ctx, connection, http.MethodGet, "/api/system/status", map[string]string{"X-API-KEY": connection.APIKey}, nil, &status)
		return status.Data.BazarrVersion, err
	case AppSABnzbd:
		var version struct {
			Version string `json:"version"`
		}
		query := url.Values{"mode": {"version"}, "output": {"json"}, "apikey": {connection.APIKey}}
		err := s.requestJSON(ctx, connection, http.MethodGet, "/api?"+query.Encode(), nil, nil, &version)
		return version.Version, err
	case AppNZBGet:
		var version nzbgetEnvelope
		if err := s.requestJSON(ctx, connection, http.MethodPost, "/jsonrpc", nil, map[string]any{"method": "version"}, &version); err != nil {
			return "", err
		}
		var value string
		_ = json.Unmarshal(version.Result, &value)
		return value, nil
	case AppTransmission:
		out, _, err := s.discoverTransmission(ctx, connection)
		if err != nil {
			return "", err
		}
		if out.Torrents != nil {
			return out.Torrents.Version, nil
		}
		return "", nil
	case AppJellyfin:
		plan, err := s.discoverJellyfin(ctx, connection)
		if err != nil {
			return "", err
		}
		return plan.Version, nil
	}
	return "", fmt.Errorf("%w: %s is not a supported application", ErrInvalid, connection.App)
}

// discoverAll inspects every connection concurrently and fails if any application is unreachable.
func (s *Service) discoverAll(ctx context.Context, connections []Connection) (snapshot, providerSecrets, error) {
	results := make([]snapshot, len(connections))
	failures := make([]error, len(connections))
	var secrets providerSecrets
	var mu sync.Mutex
	var group sync.WaitGroup
	for i, connection := range connections {
		group.Add(1)
		go func(i int, connection Connection) {
			defer group.Done()
			out, appSecrets, err := s.discoverOne(ctx, connection)
			if err != nil {
				failures[i] = err
				return
			}
			results[i] = out
			mu.Lock()
			mergeSecrets(&secrets, appSecrets)
			mu.Unlock()
		}(i, connection)
	}
	group.Wait()
	merged := snapshot{}
	for i, result := range results {
		if failures[i] != nil {
			return snapshot{}, providerSecrets{}, sanitizeErr(failures[i], connections[i].secrets())
		}
		mergeSnapshot(&merged, result)
	}
	return merged, secrets, nil
}

// discoverProviderSecrets re-reads provider credentials so a restarted service can finish a plan.
func (s *Service) discoverProviderSecrets(ctx context.Context, connections []Connection) (providerSecrets, error) {
	var secrets providerSecrets
	for _, connection := range connections {
		switch connection.App {
		case AppProwlarr, AppSABnzbd, AppNZBGet, AppBazarr:
		default:
			continue
		}
		_, appSecrets, err := s.discoverOne(ctx, connection)
		if err != nil {
			return providerSecrets{}, sanitizeErr(err, connection.secrets())
		}
		mergeSecrets(&secrets, appSecrets)
	}
	return secrets, nil
}

func mergeSecrets(into *providerSecrets, from providerSecrets) {
	for key, secret := range from.indexers {
		if into.indexers == nil {
			into.indexers = map[string]indexerSecret{}
		}
		into.indexers[key] = secret
	}
	for key, secret := range from.usenet {
		if into.usenet == nil {
			into.usenet = map[string]usenetSecret{}
		}
		into.usenet[key] = secret
	}
	if from.subtitle != nil {
		into.subtitle = from.subtitle
	}
}

func (s *Service) discoverOne(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	switch connection.App {
	case AppRadarr:
		out, err := s.discoverRadarr(ctx, connection)
		return out, providerSecrets{}, err
	case AppSonarr:
		out, err := s.discoverSonarr(ctx, connection)
		return out, providerSecrets{}, err
	case AppProwlarr, AppSABnzbd, AppNZBGet:
		return s.discoverProvider(ctx, connection)
	case AppLidarr:
		out, err := s.discoverLidarr(ctx, connection)
		return out, providerSecrets{}, err
	case AppBazarr:
		return s.discoverBazarr(ctx, connection)
	case AppTransmission:
		return s.discoverTransmission(ctx, connection)
	case AppJellyfin:
		plan, err := s.discoverJellyfin(ctx, connection)
		return snapshot{Jellyfin: plan}, providerSecrets{}, err
	}
	return snapshot{}, providerSecrets{}, fmt.Errorf("%w: %s is not a supported application", ErrInvalid, connection.App)
}

func (s *Service) discoverProvider(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	switch connection.App {
	case AppProwlarr:
		return s.discoverProwlarr(ctx, connection)
	case AppSABnzbd:
		return s.discoverSABnzbd(ctx, connection)
	default:
		return s.discoverNZBGet(ctx, connection)
	}
}

func mergeSnapshot(into *snapshot, from snapshot) {
	for app, version := range from.Versions {
		if into.Versions == nil {
			into.Versions = map[App]string{}
		}
		into.Versions[app] = version
	}
	into.Roots = append(into.Roots, from.Roots...)
	into.Indexers = append(into.Indexers, from.Indexers...)
	into.UsenetSources = append(into.UsenetSources, from.UsenetSources...)
	into.Torznab = append(into.Torznab, from.Torznab...)
	if from.Music != nil {
		if into.Music == nil {
			into.Music = &musicSnapshot{}
		}
		into.Music.Roots = append(into.Music.Roots, from.Music.Roots...)
		into.Music.Profiles = append(into.Music.Profiles, from.Music.Profiles...)
		into.Music.Naming = append(into.Music.Naming, from.Music.Naming...)
		into.Music.Artists = append(into.Music.Artists, from.Music.Artists...)
	}
	into.Profiles = append(into.Profiles, from.Profiles...)
	into.Movies = append(into.Movies, from.Movies...)
	into.Series = append(into.Series, from.Series...)
	into.Naming = append(into.Naming, from.Naming...)
	if from.Jellyfin != nil {
		into.Jellyfin = from.Jellyfin
	}
	if from.Subtitles != nil {
		into.Subtitles = from.Subtitles
	}
	if from.Torrents != nil {
		into.Torrents = from.Torrents
	}
	into.Warnings = append(into.Warnings, from.Warnings...)
	into.Unsupported = append(into.Unsupported, from.Unsupported...)
}

func sortStrings(values []string) { sort.Strings(values) }

// sanitizeErr strips credential values from an error headed to a client and keeps sentinels for status codes.
func sanitizeErr(err error, secrets []string) error {
	message := sanitize(err.Error(), secrets)
	for _, sentinel := range []error{
		ErrInvalid, ErrNotFound, ErrExpired, ErrConflict, ErrUnready,
		downloads.ErrInvalid, downloads.ErrNotFound, downloads.ErrConflict, downloads.ErrNotConfigured,
		movies.ErrInvalid, movies.ErrNotFound, movies.ErrConflict,
		tv.ErrInvalid, tv.ErrNotFound, tv.ErrConflict,
	} {
		if errors.Is(err, sentinel) {
			return fmt.Errorf("%s: %w", message, sentinel)
		}
	}
	return errors.New(message)
}

// localDirectory reports whether a mapped path exists on this server and is readable.
func localDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || len(path) > 4096 || filepath.Clean(path) == string(filepath.Separator) {
		return errors.New("enter an absolute local folder path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("the local folder is not reachable from this server")
	}
	if !info.IsDir() {
		return errors.New("the local path is not a folder")
	}
	handle, err := os.Open(path)
	if err != nil {
		return errors.New("the local folder cannot be read")
	}
	defer handle.Close()
	if _, err := handle.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("the local folder cannot be read")
	}
	return nil
}
