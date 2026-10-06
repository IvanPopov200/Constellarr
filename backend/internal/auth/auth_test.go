package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRoutePermissions(t *testing.T) {
	cases := []struct {
		method      string
		path        string
		permission  string
		sessionOnly bool
		selfAudited bool
	}{
		{"GET", "/api/v1/movies", PermLibraryRead, false, false},
		{"POST", "/api/v1/movies", PermLibraryWrite, false, false},
		{"PUT", "/api/v1/movies/abc", PermLibraryWrite, false, false},
		{"POST", "/api/v1/movies/abc/grab", PermLibraryWrite, false, false},
		{"GET", "/api/v1/movie-poster", PermLibraryRead, false, false},
		{"GET", "/api/v1/tv/abc/history", PermLibraryRead, false, false},
		{"POST", "/api/v1/tv/abc/monitor", PermLibraryWrite, false, false},
		{"GET", "/api/v1/music/albums/abc/cover", PermLibraryRead, false, false},
		{"DELETE", "/api/v1/music/artists/abc", PermLibraryWrite, false, false},
		{"POST", "/api/v1/music/scan", PermLibraryWrite, false, false},
		{"GET", "/api/v1/calendar", PermLibraryRead, false, false},
		{"POST", "/api/v1/recommendations", PermLibraryRead, false, false},
		{"POST", "/api/v1/recommendations/abc/accept", PermLibraryRead, false, false},
		{"GET", "/api/v1/downloads", PermDownloadsRead, false, false},
		{"POST", "/api/v1/downloads", PermDownloadsWrite, false, false},
		{"GET", "/api/v1/downloads/policy", PermDownloadsRead, false, false},
		{"PUT", "/api/v1/downloads/policy", PermSettingsWrite, false, false},
		{"POST", "/api/v1/downloads/pause", PermDownloadsWrite, false, false},
		{"POST", "/api/v1/downloads/resume", PermDownloadsWrite, false, false},
		{"POST", "/api/v1/downloads/abc/pause", PermDownloadsWrite, false, false},
		{"POST", "/api/v1/downloads/abc/resume", PermDownloadsWrite, false, false},
		{"POST", "/api/v1/downloads/abc/cancel", PermDownloadsWrite, false, false},
		{"GET", "/api/v1/downloads/abc/file", PermDownloadsRead, false, false},
		{"GET", "/api/v1/releases", PermDownloadsRead, false, false},
		{"GET", "/api/v1/torrents", PermDownloadsRead, false, false},
		{"GET", "/api/v1/torrents/health", PermDownloadsRead, false, false},
		{"GET", "/api/v1/torrents/abc/file", PermDownloadsRead, false, false},
		{"PUT", "/api/v1/torrents/abc/limits", PermDownloadsWrite, false, false},
		{"POST", "/api/v1/torrents/abc/recheck", PermDownloadsWrite, false, false},
		{"GET", "/api/v1/requests/discover", PermRequestsRead, false, false},
		{"POST", "/api/v1/requests/abc/comments", PermRequestsWrite, false, false},
		{"POST", "/api/v1/requests/abc/cancel", PermRequestsWrite, false, false},
		{"POST", "/api/v1/requests/abc/approve", PermRequestsApprove, false, false},
		{"GET", "/api/v1/settings", PermSettingsRead, false, false},
		{"PUT", "/api/v1/settings", PermSettingsWrite, false, false},
		{"GET", "/api/v1/sources", PermSettingsRead, false, false},
		{"POST", "/api/v1/sources/test", PermSettingsWrite, false, false},
		{"DELETE", "/api/v1/movie-profiles/abc", PermSettingsWrite, false, false},
		{"GET", "/api/v1/music/config", PermSettingsRead, false, false},
		{"PUT", "/api/v1/music/config", PermSettingsWrite, false, false},
		{"POST", "/api/v1/music/config/test", PermSettingsWrite, false, false},
		{"GET", "/api/v1/torrents/settings", PermSettingsRead, false, false},
		{"PUT", "/api/v1/torrents/settings", PermSettingsWrite, false, false},
		{"GET", "/api/v1/torrent-sources", PermSettingsRead, false, false},
		{"POST", "/api/v1/torrent-sources/abc/test", PermSettingsWrite, false, false},
		{"GET", "/api/v1/ai/config", PermSettingsRead, false, false},
		{"GET", "/api/v1/subtitle-config", PermSettingsRead, false, false},
		{"GET", "/api/v1/subtitle-profiles", PermSettingsRead, false, false},
		{"PUT", "/api/v1/subtitle-config", PermSettingsWrite, false, false},
		{"POST", "/api/v1/subtitles/providers/test", PermSettingsWrite, false, false},
		{"PUT", "/api/v1/ai/config", PermSettingsWrite, false, false},
		{"POST", "/api/v1/ai/test", PermSettingsWrite, false, false},
		{"GET", "/api/v1/operations/status", PermMonitoringRead, false, false},
		{"GET", "/api/v1/operations/alerts/history", PermMonitoringRead, false, false},
		{"PUT", "/api/v1/operations/alerts/config", PermSettingsWrite, false, false},
		{"GET", "/api/v1/operations/backups", PermBackupsManage, false, false},
		{"GET", "/api/v1/operations/backups/config", PermBackupsManage, false, false},
		{"POST", "/api/v1/operations/backups/import", PermBackupsManage, false, false},
		{"DELETE", "/api/v1/operations/backups/abc", PermBackupsManage, false, false},
		{"GET", "/metrics", PermMonitoringRead, false, false},
		{"GET", "/api/v1/health", PermMonitoringRead, false, false},
		{"GET", "/api/v1/migration/plans/abc", PermMigrationManage, false, false},
		{"POST", "/api/v1/migration/preview", PermMigrationManage, false, false},
		{"POST", "/api/v1/migration/plans/abc/apply", PermMigrationManage, false, false},
		{"GET", "/api/v1/users", PermUsersManage, false, true},
		{"PUT", "/api/v1/users/abc/password", PermUsersManage, false, true},
		{"GET", "/api/v1/roles", PermUsersManage, false, true},
		{"GET", "/api/v1/audit", PermUsersManage, false, true},
		{"GET", "/api/v1/auth/me", "", false, true},
		{"GET", "/api/v1/permissions", "", false, true},
		{"PUT", "/api/v1/auth/password", "", true, true},
		{"POST", "/api/v1/auth/tokens", "", true, true},
		{"GET", "/api/v1/auth/tokens", "", true, true},
		{"DELETE", "/api/v1/auth/tokens/abc", "", true, true},
		{"HEAD", "/api/v1/movies", PermLibraryRead, false, false},
	}
	for _, test := range cases {
		match, pathKnown, methodKnown := routePermission(test.method, test.path)
		if !pathKnown || !methodKnown {
			t.Errorf("%s %s: pathKnown=%v methodKnown=%v", test.method, test.path, pathKnown, methodKnown)
			continue
		}
		if match.permission != test.permission || match.sessionOnly != test.sessionOnly || match.selfAudited != test.selfAudited {
			t.Errorf("%s %s: got %+v, want permission=%q sessionOnly=%v selfAudited=%v",
				test.method, test.path, match, test.permission, test.sessionOnly, test.selfAudited)
		}
	}

	for _, unknown := range []string{
		"/api/v1/nothing-here",
		"/api/v1/settings/extra",
		"/api/v1/subtitles/orphan",
		"/api/v1/backups",
		"/api/v1/system",
		"/api/v1/metrics",
		"/api/v1/tv-profiles",
		"/api/v1/movies/abc/subtitles",
		"/api/v1/alerts",
		"/api/info",
	} {
		if _, pathKnown, _ := routePermission("GET", unknown); pathKnown {
			t.Errorf("%s must stay unmapped until its module registers it", unknown)
		}
	}
	if _, pathKnown, methodKnown := routePermission("OPTIONS", "/api/v1/settings"); !pathKnown || methodKnown {
		t.Error("OPTIONS /api/v1/settings must be a known path with an unknown method")
	}
}

// New routes must declare permissions; unmapped routes are denied at runtime.
func TestEveryRegisteredRouteIsMapped(t *testing.T) {
	var files []string
	for _, glob := range []string{"../*/*.go", "../*/*/*.go"} {
		matches, err := filepath.Glob(glob)
		if err != nil {
			t.Fatalf("scan %s: %v", glob, err)
		}
		files = append(files, matches...)
	}
	pattern := regexp.MustCompile(`"((GET|HEAD|POST|PUT|PATCH|DELETE) /[^"]*)"`)
	routes := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(body), -1) {
			routes[match[1]+" ("+file+")"] = true
		}
	}
	if len(routes) < 100 {
		t.Fatalf("route scan found only %d registrations; the scanner is broken", len(routes))
	}
	for route := range routes {
		registration, _, _ := strings.Cut(route, " (")
		method, path, _ := strings.Cut(registration, " ")
		if publicRoute(method, path) {
			continue
		}
		if _, pathKnown, methodKnown := routePermission(method, path); !pathKnown || !methodKnown {
			t.Errorf("%s is registered but not covered by the permission map", route)
		}
	}
}

func TestOnlyAuthRoutesAllowAnyPrincipal(t *testing.T) {
	for _, rule := range routeRules {
		if rule.permission != "" {
			continue
		}
		for _, pattern := range rule.patterns {
			if !strings.HasPrefix(pattern, "/api/v1/auth/") && pattern != "/api/v1/permissions" {
				t.Errorf("route %s requires no permission beyond authentication", pattern)
			}
		}
	}
}

func TestAccountCredentialsRequireASession(t *testing.T) {
	for _, route := range []string{"/api/v1/auth/password", "/api/v1/auth/tokens", "/api/v1/auth/tokens/abc"} {
		for _, method := range []string{"POST", "PUT", "DELETE", "GET"} {
			match, pathKnown, methodKnown := routePermission(method, route)
			if !pathKnown || !methodKnown {
				continue
			}
			if !match.sessionOnly {
				t.Errorf("%s %s must require a cookie session", method, route)
			}
		}
	}
	for _, route := range []string{"/api/v1/auth/me", "/api/v1/permissions"} {
		match, _, _ := routePermission("GET", route)
		if match.sessionOnly {
			t.Errorf("%s must stay available to bearer tokens", route)
		}
	}
}

func TestBuiltinRolePermissions(t *testing.T) {
	byID := map[string][]string{}
	for _, role := range builtinRoles {
		byID[role.id] = role.permissions
	}
	if len(byID[roleAdmin]) != len(PermissionCatalog) {
		t.Errorf("admin has %d permissions, want %d", len(byID[roleAdmin]), len(PermissionCatalog))
	}
	for _, excluded := range []string{PermUsersManage, PermMigrationManage, PermBackupsManage} {
		if contains(byID[roleOperator], excluded) {
			t.Errorf("operator must not have %s", excluded)
		}
	}
	if len(byID[roleOperator]) != len(PermissionCatalog)-3 {
		t.Errorf("operator has %d permissions, want %d", len(byID[roleOperator]), len(PermissionCatalog)-3)
	}
	if want := []string{PermLibraryRead, PermRequestsRead, PermRequestsWrite}; !equalStrings(byID[roleRequester], want) {
		t.Errorf("requester permissions = %v, want %v", byID[roleRequester], want)
	}
	if want := []string{PermLibraryRead, PermDownloadsRead}; !equalStrings(byID[roleViewer], want) {
		t.Errorf("viewer permissions = %v, want %v", byID[roleViewer], want)
	}
}

func TestNormalizePermissions(t *testing.T) {
	normalized, err := normalizePermissions([]string{PermLibraryWrite, PermLibraryRead, PermLibraryRead})
	if err != nil || !equalStrings(normalized, []string{PermLibraryRead, PermLibraryWrite}) {
		t.Fatalf("normalize = %v, %v", normalized, err)
	}
	if _, err := normalizePermissions([]string{"library.delete"}); err == nil {
		t.Fatal("unknown permission was accepted")
	}
	shared := intersectPermissions([]string{PermLibraryRead, PermDownloadsRead}, []string{PermDownloadsRead})
	if !equalStrings(shared, []string{PermDownloadsRead}) {
		t.Fatalf("intersection = %v", shared)
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") {
		t.Fatalf("hash parameters are not encoded: %s", hash)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Fatal("the correct password was rejected")
	}
	if verifyPassword("wrong password", hash) {
		t.Fatal("a wrong password was accepted")
	}
	other, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if other == hash {
		t.Fatal("hashes must use unique salts")
	}
	for _, malformed := range []string{"", "plain", "pbkdf2-sha256$abc$c2FsdA$aGFzaA", "pbkdf2-sha256$0$c2FsdA$aGFzaA", "md5$1$c2FsdA$aGFzaA"} {
		if verifyPassword("correct horse battery staple", malformed) {
			t.Fatalf("malformed hash %q was accepted", malformed)
		}
	}
	if err := validPassword("short"); err == nil {
		t.Fatal("a short password was accepted")
	}
	if err := validPassword("long enough"); err != nil {
		t.Fatalf("a valid password was rejected: %v", err)
	}
	if err := validPassword(strings.Repeat("a", passwordMaxBytes+1)); err == nil {
		t.Fatal("an oversized password was accepted")
	}
	if err := validPassword("with\x00control"); err == nil {
		t.Fatal("a control character was accepted")
	}
}

func TestLoginLimiterBlocksAndResets(t *testing.T) {
	limiter := newLoginLimiter()
	for range loginMaxFailures {
		if wait := limiter.blocked("Admin", "192.0.2.10"); wait > 0 {
			t.Fatal("attempts were blocked before the failure limit")
		}
		limiter.fail("Admin", "192.0.2.10")
	}
	if wait := limiter.blocked("admin", "192.0.2.10"); wait <= 0 || wait > loginWindow {
		t.Fatalf("blocked wait = %v", wait)
	}
	if wait := limiter.blocked("someone-else", "192.0.2.11"); wait != 0 {
		t.Fatalf("an unrelated attempt was blocked: %v", wait)
	}
	if wait := limiter.blocked("admin", "192.0.2.11"); wait <= 0 {
		t.Fatal("the account block must follow the account across addresses")
	}
	limiter.reset("admin", "192.0.2.10")
	if wait := limiter.blocked("admin", "192.0.2.10"); wait != 0 {
		t.Fatalf("reset left a block of %v", wait)
	}
}

// Username churn must not evict an address block.
func TestLoginLimiterKeepsBlocksUnderUsernameChurn(t *testing.T) {
	limiter := newLoginLimiter()
	const ip = "192.0.2.10"
	for range loginMaxFailures {
		limiter.fail("admin", ip)
	}
	for index := range limiterMaxNames + 512 {
		limiter.fail(fmt.Sprintf("unknown-%d", index), ip)
	}
	if wait := limiter.blocked("admin", ip); wait <= 0 {
		t.Fatal("username churn erased the account block")
	}
	if wait := limiter.blocked("fresh-name", ip); wait <= 0 {
		t.Fatal("username churn erased the address block")
	}
	if len(limiter.names) > limiterMaxNames || len(limiter.ips) > limiterMaxIPs {
		t.Fatalf("limiter maps grew to names=%d ips=%d", len(limiter.names), len(limiter.ips))
	}

	// Expired entries are dropped and the newest entries are evicted before old blocks.
	expired := time.Now().Add(-time.Minute)
	for key := range limiter.names {
		entry := limiter.names[key]
		entry.windowEnd = expired
		limiter.names[key] = entry
	}
	if wait := limiter.blocked("admin", "192.0.2.99"); wait != 0 {
		t.Fatalf("expired block still applies: %v", wait)
	}
	limiter.purge()
	if len(limiter.names) != 0 {
		t.Fatalf("purge left %d expired entries", len(limiter.names))
	}
}

func TestCanonicalAuthMigrationDefinesTables(t *testing.T) {
	body, err := os.ReadFile("../downloads/migrations/007_auth.sql")
	if err != nil {
		t.Fatalf("read 007_auth.sql: %v", err)
	}
	for _, table := range []string{
		"auth_users", "auth_roles", "auth_role_permissions", "auth_user_roles",
		"auth_sessions", "auth_tokens", "auth_audit",
	} {
		if !strings.Contains(string(body), "CREATE TABLE IF NOT EXISTS "+table+" (") {
			t.Errorf("007_auth.sql does not create %s", table)
		}
	}
}

func TestPrincipalCan(t *testing.T) {
	principal := Principal{Roles: []string{roleViewer}, SessionID: "session", Permissions: []string{PermLibraryRead}}
	if !principal.Can(PermLibraryRead) || principal.Can(PermLibraryWrite) || principal.IsAdmin() {
		t.Fatalf("principal check is wrong: %+v", principal)
	}
	if !principal.sessionCredential() {
		t.Fatal("a cookie session must qualify as an account credential")
	}
	bearer := Principal{Roles: []string{roleAdmin}, TokenID: "token", Permissions: PermissionCatalog}
	if bearer.sessionCredential() {
		t.Fatal("a bearer token must not qualify as an account credential")
	}
	if !bearer.IsAdmin() {
		t.Fatal("admin role was not detected")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
