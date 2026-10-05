package migration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

// TestLiveSourceContracts is opt-in: point MIGRATION_LIVE_SOURCES at a private JSON credentials file.
func TestLiveSourceContracts(t *testing.T) {
	path := os.Getenv("MIGRATION_LIVE_SOURCES")
	if path == "" {
		t.Skip("set MIGRATION_LIVE_SOURCES to a private credentials file to check real applications")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("live source file is not readable: %v", err)
	}
	var sources map[string]struct {
		URL      string `json:"url"`
		APIKey   string `json:"apiKey"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil {
		t.Fatalf("live source file is not valid JSON: %v", err)
	}
	connections := make([]Connection, 0, len(sources))
	for name, source := range sources {
		connections = append(connections, Connection{
			App: App(name), URL: source.URL, APIKey: source.APIKey,
			Username: source.Username, Password: source.Password,
		})
	}
	if len(connections) == 0 {
		t.Skip("the live source file has no connections")
	}
	service := &Service{retries: 1, retryDelay: 100 * time.Millisecond, callTimeout: 10 * time.Second}
	service.withDefaults()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, connection := range connections {
		version, err := service.connect(ctx, connection)
		if err != nil {
			t.Errorf("%s: %v", connection.App, sanitize(err.Error(), connection.secrets()))
			continue
		}
		t.Logf("%s reachable, version reported", connection.App)
		_ = version
	}
	found, secrets, err := service.discoverAll(ctx, connections)
	if err != nil {
		t.Fatalf("discovery failed: %v", sanitize(err.Error(), connectSecrets(connections)))
	}
	t.Logf("discovered roots=%d profiles=%d movies=%d series=%d naming=%d unsupported=%d warnings=%d",
		len(found.Roots), len(found.Profiles), len(found.Movies), len(found.Series), len(found.Naming), len(found.Unsupported), len(found.Warnings))
	t.Logf("providers: indexerCandidates=%d newsServers=%d torznab=%d",
		len(found.Indexers), len(found.UsenetSources), len(found.Torznab))
	for key, secret := range secrets.indexers {
		t.Logf("indexer %s: keyHeld=%t", key, secret.APIKey != "")
	}
	for key, secret := range secrets.usenet {
		t.Logf("news server %s: passwordHeld=%t", key, secret.Password != "")
	}
	if found.Torrents != nil {
		t.Logf("transmission transfers with magnet=%d files=%d",
			countWithMagnet(found.Torrents.Transfers), countWithFiles(found.Torrents.Transfers))
	}
	if found.Jellyfin != nil {
		t.Logf("jellyfin libraries=%v", found.Jellyfin.Libraries)
	}
	_ = providerSecrets{}
	if found.Subtitles != nil {
		t.Logf("bazarr languages=%d providers=%d sync=%t", len(found.Subtitles.Languages), len(found.Subtitles.Providers), found.Subtitles.Sync.Enabled)
	}
	if found.Music != nil {
		t.Logf("lidarr roots=%d profiles=%d artists=%d", len(found.Music.Roots), len(found.Music.Profiles), len(found.Music.Artists))
	}
}

func countWithMagnet(transfers []TorrentTransfer) int {
	count := 0
	for _, transfer := range transfers {
		if transfer.MagnetLink != "" {
			count++
		}
	}
	return count
}

func countWithFiles(transfers []TorrentTransfer) int {
	count := 0
	for _, transfer := range transfers {
		if len(transfer.Files) > 0 {
			count++
		}
	}
	return count
}

// TestLiveDevStackPreviewApply is opt-in and writes only to the isolated test schema and local temp dirs.
func TestLiveDevStackPreviewApply(t *testing.T) {
	if testing.Short() {
		t.Skip("live dev stack check")
	}
	sourcesPath := os.Getenv("MIGRATION_LIVE_SOURCES")
	if sourcesPath == "" {
		t.Skip("set MIGRATION_LIVE_SOURCES to a private credentials file to run the live wizard")
	}
	raw, err := os.ReadFile(sourcesPath)
	if err != nil {
		t.Skipf("live source file is not readable: %v", err)
	}
	var sources map[string]struct {
		URL      string `json:"url"`
		APIKey   string `json:"apiKey"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil {
		t.Fatalf("live source file is not valid JSON: %v", err)
	}
	pool := migrationSchema(t)
	t.Setenv("OMDB_URL", "")
	t.Setenv("OMDB_API_KEY", "")
	t.Setenv("JELLYFIN_URL", "")
	t.Setenv("JELLYFIN_API_KEY", "")
	t.Setenv("IMPORT_WEBHOOK_URL", "")
	ctx := context.Background()
	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{
		Directory: directory, IndexerURL: "http://127.0.0.1:9/api", APIKey: "live-initial",
		Usenet: usenet.Config{Host: "news.example.com", Port: 563, Username: "live-initial", Password: "live-initial", Connections: 4},
	})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieService, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	tvService, err := tv.New(ctx, pool, manager, movieService.Store)
	if err != nil {
		t.Fatalf("tv.New: %v", err)
	}
	musicService, err := music.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("music.New: %v", err)
	}
	subtitleService, err := subtitles.New(ctx, pool, subtitles.Options{Movies: movieService, TV: tvService})
	if err != nil {
		t.Fatalf("subtitles.New: %v", err)
	}
	torrentService, err := torrents.New(ctx, pool, torrents.Options{Directory: directory, Testing: true})
	if err != nil {
		t.Fatalf("torrents.New: %v", err)
	}
	service, err := New(ctx, pool, Options{
		Movies: movieService, TV: tvService, Music: musicService, Subtitles: subtitleService,
		Torrents: torrentService, Downloads: manager, RetryDelay: time.Second, CallTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("migration.New: %v", err)
	}
	order := []App{AppRadarr, AppSonarr, AppLidarr, AppProwlarr, AppBazarr, AppSABnzbd, AppNZBGet, AppTransmission, AppJellyfin}
	connections := make([]Connection, 0, len(order))
	for _, app := range order {
		source, ok := sources[string(app)]
		if !ok {
			continue
		}
		connections = append(connections, Connection{
			App: app, URL: source.URL, APIKey: source.APIKey, Username: source.Username, Password: source.Password,
		})
	}
	if len(connections) == 0 {
		t.Skip("the live source file has no connections")
	}
	view, err := service.Preview(ctx, connections)
	if err != nil {
		t.Fatalf("live preview failed: %v", sanitize(err.Error(), connectSecrets(connections)))
	}
	t.Logf("live preview: movies=%d series=%d artists=%d albums=%d files=%d indexers=%d newsServers=%d torznab=%d torrents=%d languages=%d items=%d",
		view.Counts.Movies, view.Counts.Series, view.Counts.Artists, view.Counts.Albums, view.Counts.Files,
		view.Counts.Indexers, view.Counts.NewsServer, len(view.Torznab), view.Counts.Torrents, view.Counts.Languages, view.Totals.Total)
	for _, entry := range view.Unsupported {
		t.Logf("live unsupported [%s]: %s", entry.Area, sanitize(entry.Detail, nil))
	}
	for _, warning := range view.Warnings {
		t.Logf("live warning: %s", sanitize(warning, nil))
	}

	selectedIndexer, selectedUsenet := "", ""
	if len(view.Indexers) == 1 {
		selectedIndexer = view.Indexers[0].Key
	}
	var fallbacks []string
	for _, candidate := range view.UsenetSources {
		if selectedUsenet == "" {
			selectedUsenet = candidate.Key
			continue
		}
		fallbacks = append(fallbacks, candidate.Key)
	}
	var torznab []string
	for _, candidate := range view.Torznab {
		torznab = append(torznab, candidate.Key)
	}
	// Empty mapped roots and disabled file imports keep development media untouched.
	moviesRoot := filepath.Join(directory, "movies")
	tvRoot := filepath.Join(directory, "tv")
	for _, root := range []string{moviesRoot, tvRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create mapped root: %v", err)
		}
	}
	moviesRoots := map[string]string{}
	tvRoots := map[string]string{}
	for _, root := range view.Roots {
		switch root.Media {
		case MediaMovies:
			moviesRoots[root.Path] = moviesRoot
		case MediaTV:
			tvRoots[root.Path] = tvRoot
		}
	}
	input := ApplyInput{
		Movies: true, TV: true, Files: false, Naming: true, Providers: true, Subtitles: true,
		Torrents: true, CreateProfiles: true,
		MoviesRoots: moviesRoots, TVRoots: tvRoots, MusicRoots: map[string]string{},
		TorrentRoots: map[string]string{}, Profiles: map[string]string{}, MusicProfiles: map[string]string{},
		Indexer: selectedIndexer, Usenet: selectedUsenet, UsenetFallbacks: fallbacks, Torznab: torznab, Connections: connections,
	}
	result, err := service.Apply(ctx, view.ID, input)
	if err != nil {
		t.Fatalf("live apply failed: %v", sanitize(err.Error(), connectSecrets(connections)))
	}
	for round := 0; result.Remaining > 0 && round < 20; round++ {
		result, err = service.Apply(ctx, view.ID, input)
		if err != nil {
			t.Fatalf("live apply round %d: %v", round, sanitize(err.Error(), connectSecrets(connections)))
		}
	}
	t.Logf("live apply: status=%s done=%d skipped=%d failed=%d remaining=%d",
		result.Status, result.Totals.Done, result.Totals.Skipped, result.Totals.Failed, result.Remaining)
	for _, item := range result.Failures {
		t.Logf("live failure [%s %s]: %s", item.Kind, item.Label, sanitize(item.Message, connectSecrets(connections)))
	}
	if items, err := service.items(ctx, view.ID, "", 500); err == nil {
		byKind := map[string][2]int{}
		for _, item := range items {
			counts := byKind[item.Kind]
			switch item.Status {
			case itemDone:
				counts[0]++
			case itemSkipped, itemFailed:
				counts[1]++
			}
			byKind[item.Kind] = counts
		}
		kinds := make([]string, 0, len(byKind))
		for kind := range byKind {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			t.Logf("live items %s: applied=%d skipped-or-failed=%d", kind, byKind[kind][0], byKind[kind][1])
		}
	}
	settings := manager.Config()
	t.Logf("live settings: indexerURL=%t indexerKey=%t usenetHost=%t usenetUser=%t usenetConnections=%d",
		settings.IndexerURL != "", settings.APIKey != "", settings.Usenet.Host != "", settings.Usenet.Username != "", settings.Usenet.Connections)
	subtitleCfg, err := subtitleService.Config(ctx)
	if err != nil {
		t.Fatalf("subtitle config: %v", err)
	}
	migratedProfiles := 0
	if profiles, err := subtitleService.Profiles(ctx); err == nil {
		for _, profile := range profiles {
			if strings.HasPrefix(profile.ID, "bazarr-") {
				migratedProfiles++
			}
		}
	}
	t.Logf("live subtitles: cutoffScore=%d searchHours=%d syncOffset=%g migratedProfiles=%d providers=%d migratedAccount=%t",
		subtitleCfg.CutoffScore, subtitleCfg.SearchIntervalHours, subtitleCfg.Sync.MaxOffsetSeconds,
		migratedProfiles, len(subtitleCfg.Providers), migratedProvider(subtitleCfg))
	torrentSettings := torrentService.Settings()
	t.Logf("live torrents: downLimit=%d upLimit=%d active=%d seedRatio=%g torznab=%d appliedItems=%d",
		torrentSettings.DownloadLimitKBps, torrentSettings.UploadLimitKBps, torrentSettings.MaxActiveJobs,
		torrentSettings.SeedRatioLimit, len(torznab), len(result.Applied))
	if status := result.Status; status != statusApplied && status != statusReady {
		t.Fatalf("live apply ended in %s", status)
	}
}

func migratedProvider(cfg subtitles.Config) bool {
	for _, provider := range cfg.Providers {
		if provider.Username != "" {
			return true
		}
	}
	return false
}
