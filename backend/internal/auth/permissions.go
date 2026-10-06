package auth

import (
	"net/http"
	"sort"
	"strings"
)

const (
	PermLibraryRead     = "library.read"
	PermLibraryWrite    = "library.write"
	PermDownloadsRead   = "downloads.read"
	PermDownloadsWrite  = "downloads.write"
	PermRequestsRead    = "requests.read"
	PermRequestsWrite   = "requests.write"
	PermRequestsApprove = "requests.approve"
	PermSubtitlesRead   = "subtitles.read"
	PermSubtitlesWrite  = "subtitles.write"
	PermSettingsRead    = "settings.read"
	PermSettingsWrite   = "settings.write"
	PermUsersManage     = "users.manage"
	PermMigrationManage = "migration.manage"
	PermBackupsManage   = "backups.manage"
	PermMonitoringRead  = "monitoring.read"
)

var PermissionCatalog = []string{
	PermLibraryRead,
	PermLibraryWrite,
	PermDownloadsRead,
	PermDownloadsWrite,
	PermRequestsRead,
	PermRequestsWrite,
	PermRequestsApprove,
	PermSubtitlesRead,
	PermSubtitlesWrite,
	PermSettingsRead,
	PermSettingsWrite,
	PermUsersManage,
	PermMigrationManage,
	PermBackupsManage,
	PermMonitoringRead,
}

// Built-in roles are immutable and re-seeded from these identifiers.
const (
	roleAdmin     = "admin"
	roleOperator  = "operator"
	roleRequester = "requester"
	roleViewer    = "viewer"
)

var builtinRoles = []struct {
	id          string
	permissions []string
}{
	{roleAdmin, PermissionCatalog},
	{roleOperator, operatorPermissions()},
	{roleRequester, []string{PermLibraryRead, PermRequestsRead, PermRequestsWrite}},
	{roleViewer, []string{PermLibraryRead, PermDownloadsRead}},
}

// Operator keeps every capability except account, migration, and backup administration.
func operatorPermissions() []string {
	operator := make([]string, 0, len(PermissionCatalog))
	for _, permission := range PermissionCatalog {
		switch permission {
		case PermUsersManage, PermMigrationManage, PermBackupsManage:
			continue
		}
		operator = append(operator, permission)
	}
	return operator
}

func validPermission(permission string) bool {
	for _, known := range PermissionCatalog {
		if permission == known {
			return true
		}
	}
	return false
}

func normalizePermissions(input []string) ([]string, error) {
	held := make(map[string]bool, len(input))
	for _, permission := range input {
		permission = strings.TrimSpace(permission)
		if !validPermission(permission) {
			return nil, clientError(http.StatusBadRequest, "unknown permission "+truncateRunes(permission, 64), ErrInvalid)
		}
		held[permission] = true
	}
	ordered := make([]string, 0, len(held))
	for _, permission := range PermissionCatalog {
		if held[permission] {
			ordered = append(ordered, permission)
		}
	}
	return ordered, nil
}

func intersectPermissions(held, scope []string) []string {
	inScope := make(map[string]bool, len(scope))
	for _, permission := range scope {
		inScope[permission] = true
	}
	shared := make([]string, 0, len(held))
	for _, permission := range held {
		if inScope[permission] {
			shared = append(shared, permission)
		}
	}
	return shared
}

// orderPermissions reports grants in catalog order so responses stay stable.
func orderPermissions(values []string) []string {
	held := make(map[string]bool, len(values))
	for _, permission := range values {
		held[permission] = true
	}
	ordered := make([]string, 0, len(values))
	for _, permission := range PermissionCatalog {
		if held[permission] {
			delete(held, permission)
			ordered = append(ordered, permission)
		}
	}
	unknown := make([]string, 0, len(held))
	for permission := range held {
		unknown = append(unknown, permission)
	}
	sort.Strings(unknown)
	return append(ordered, unknown...)
}

type routeRule struct {
	permission string
	requires   string
	methods    []string
	patterns   []string
	// Cookie sessions prevent limited tokens from widening their own grants.
	sessionOnly bool
	// selfAudited marks handlers that already record a specific audit event.
	selfAudited bool
}

var (
	getOnly   = []string{http.MethodGet}
	writeOnly = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	readWrite = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
)

// Unlisted routes are denied; literal paths take precedence over parameters.
var routeRules = []routeRule{
	{permission: PermLibraryRead, methods: getOnly, patterns: []string{
		"/api/v1/movies",
		"/api/v1/movies/discover",
		"/api/v1/movies/history",
		"/api/v1/movies/calendar",
		"/api/v1/movies/calendar.ics",
		"/api/v1/movies/{id}",
		"/api/v1/movies/{id}/file",
		"/api/v1/movies/{id}/history",
		"/api/v1/movie-poster",
		"/api/v1/tv",
		"/api/v1/tv/discover",
		"/api/v1/tv/history",
		"/api/v1/tv/calendar",
		"/api/v1/tv/calendar.ics",
		"/api/v1/tv/{id}",
		"/api/v1/tv/{id}/file",
		"/api/v1/tv/{id}/history",
		"/api/v1/music/albums",
		"/api/v1/music/albums/{id}",
		"/api/v1/music/albums/{id}/cover",
		"/api/v1/music/albums/{id}/history",
		"/api/v1/music/artists",
		"/api/v1/music/artists/{id}",
		"/api/v1/music/artists/{id}/history",
		"/api/v1/music/calendar",
		"/api/v1/music/discover",
		"/api/v1/music/history",
		"/api/v1/music/search",
		"/api/v1/music/wanted",
		"/api/v1/calendar",
		"/api/v1/calendar.ics",
		"/api/v1/recommendations",
		"/api/v1/recommendations/{id}",
	}},
	{permission: PermLibraryRead, methods: []string{http.MethodPost}, patterns: []string{
		"/api/v1/recommendations",
		"/api/v1/recommendations/{id}/accept",
	}},
	{permission: PermLibraryWrite, methods: writeOnly, patterns: []string{
		"/api/v1/movies",
		"/api/v1/movies/bulk",
		"/api/v1/movies/import",
		"/api/v1/movies/scan",
		"/api/v1/movies/sync",
		"/api/v1/movies/{id}",
		"/api/v1/movies/{id}/grab",
		"/api/v1/movies/{id}/refresh",
		"/api/v1/movies/{id}/rename",
		"/api/v1/movies/{id}/search",
		"/api/v1/tv",
		"/api/v1/tv/bulk",
		"/api/v1/tv/import",
		"/api/v1/tv/scan",
		"/api/v1/tv/sync",
		"/api/v1/tv/{id}",
		"/api/v1/tv/{id}/episodes",
		"/api/v1/tv/{id}/grab",
		"/api/v1/tv/{id}/monitor",
		"/api/v1/tv/{id}/refresh",
		"/api/v1/tv/{id}/rename",
		"/api/v1/tv/{id}/search",
		"/api/v1/music/albums",
		"/api/v1/music/albums/{id}",
		"/api/v1/music/albums/{id}/grab",
		"/api/v1/music/albums/{id}/monitor",
		"/api/v1/music/albums/{id}/refresh",
		"/api/v1/music/albums/{id}/rename",
		"/api/v1/music/albums/{id}/search",
		"/api/v1/music/artists",
		"/api/v1/music/artists/{id}",
		"/api/v1/music/artists/{id}/monitor",
		"/api/v1/music/artists/{id}/refresh",
		"/api/v1/music/import",
		"/api/v1/music/scan",
		"/api/v1/music/sync",
	}},
	{permission: PermDownloadsRead, methods: getOnly, patterns: []string{
		"/api/v1/downloads",
		"/api/v1/downloads/policy",
		"/api/v1/downloads/{id}/file",
		"/api/v1/releases",
		"/api/v1/torrents",
		"/api/v1/torrents/health",
		"/api/v1/torrents/search",
		"/api/v1/torrents/{id}",
		"/api/v1/torrents/{id}/file",
	}},
	{permission: PermDownloadsWrite, methods: writeOnly, patterns: []string{
		"/api/v1/downloads",
		"/api/v1/downloads/pause",
		"/api/v1/downloads/resume",
		"/api/v1/downloads/{id}/pause",
		"/api/v1/downloads/{id}/resume",
		"/api/v1/downloads/{id}/cancel",
		"/api/v1/downloads/{id}/retry",
		"/api/v1/torrents",
		"/api/v1/torrents/{id}",
		"/api/v1/torrents/{id}/limits",
		"/api/v1/torrents/{id}/pause",
		"/api/v1/torrents/{id}/cancel",
		"/api/v1/torrents/{id}/recheck",
		"/api/v1/torrents/{id}/resume",
	}},
	{permission: PermSubtitlesRead, methods: getOnly, patterns: []string{
		"/api/v1/subtitles",
		"/api/v1/subtitles/history",
		"/api/v1/subtitles/jobs",
		"/api/v1/subtitles/jobs/{id}",
		"/api/v1/subtitles/outputs/{id}",
		"/api/v1/subtitles/wanted",
		"/api/v1/subtitles/{kind}/{id}",
		"/api/v1/subtitles/{kind}/{id}/file",
		"/api/v1/subtitles/{kind}/{id}/streams",
	}},
	{permission: PermSubtitlesWrite, methods: writeOnly, patterns: []string{
		"/api/v1/subtitles/scan",
		"/api/v1/subtitles/jobs/{id}/cancel",
		"/api/v1/subtitles/outputs/{id}",
		"/api/v1/subtitles/outputs/{id}/apply",
		"/api/v1/subtitles/{kind}/{id}/assignment",
		"/api/v1/subtitles/{kind}/{id}/download",
		"/api/v1/subtitles/{kind}/{id}/extract",
		"/api/v1/subtitles/{kind}/{id}/search",
		"/api/v1/subtitles/{kind}/{id}/sync",
		"/api/v1/subtitles/{kind}/{id}/translate",
	}},
	{permission: PermRequestsRead, methods: getOnly, patterns: []string{
		"/api/v1/requests",
		"/api/v1/requests/discover",
		"/api/v1/requests/{id}",
	}},
	{permission: PermRequestsWrite, methods: writeOnly, patterns: []string{
		"/api/v1/requests",
		"/api/v1/requests/{id}/cancel",
		"/api/v1/requests/{id}/comments",
	}},
	{permission: PermRequestsApprove, methods: []string{http.MethodPost}, patterns: []string{
		"/api/v1/requests/{id}/approve",
		"/api/v1/requests/{id}/reject",
	}},
	{permission: PermSettingsRead, methods: getOnly, patterns: []string{
		"/api/v1/settings",
		"/api/v1/sources",
		"/api/v1/movie-config",
		"/api/v1/movie-profiles",
		"/api/v1/movie-watchlists",
		"/api/v1/tv-config",
		"/api/v1/music/config",
		"/api/v1/torrents/settings",
		"/api/v1/torrent-sources",
		"/api/v1/ai/config",
		"/api/v1/ai/models",
		"/api/v1/subtitle-config",
		"/api/v1/subtitle-profiles",
		"/api/v1/subtitles/providers",
	}},
	{permission: PermSettingsWrite, methods: writeOnly, patterns: []string{
		"/api/v1/downloads/policy",
		"/api/v1/settings",
		"/api/v1/sources/test",
		"/api/v1/movie-config",
		"/api/v1/movie-config/test",
		"/api/v1/movie-profiles",
		"/api/v1/movie-profiles/{id}",
		"/api/v1/movie-watchlists",
		"/api/v1/movie-watchlists/{id}",
		"/api/v1/movie-watchlists/{id}/sync",
		"/api/v1/tv-config",
		"/api/v1/music/config",
		"/api/v1/music/config/test",
		"/api/v1/torrents/settings",
		"/api/v1/torrent-sources",
		"/api/v1/torrent-sources/{id}",
		"/api/v1/torrent-sources/{id}/test",
		"/api/v1/ai/config",
		"/api/v1/ai/test",
		"/api/v1/subtitle-config",
		"/api/v1/subtitle-config/test",
		"/api/v1/subtitle-profiles",
		"/api/v1/subtitle-profiles/{id}",
		"/api/v1/subtitles/providers/test",
		"/api/v1/operations/alerts/config",
	}},
	{permission: PermMigrationManage, methods: readWrite, patterns: []string{
		"/api/v1/migration/plans/{id}",
		"/api/v1/migration/plans/{id}/apply",
		"/api/v1/migration/preview",
		"/api/v1/migration/connections/test",
	}},
	{permission: PermBackupsManage, methods: readWrite, patterns: []string{
		"/api/v1/operations/backups",
		"/api/v1/operations/backups/config",
		"/api/v1/operations/backups/import",
		"/api/v1/operations/backups/{id}",
		"/api/v1/operations/backups/{id}/download",
	}},
	{permission: PermBackupsManage, requires: PermUsersManage, methods: []string{http.MethodPost}, patterns: []string{
		"/api/v1/operations/backups/{id}/restore",
	}, sessionOnly: true},
	{permission: PermMonitoringRead, methods: getOnly, patterns: []string{
		"/api/v1/health",
		"/metrics",
		"/api/v1/operations/status",
		"/api/v1/operations/events",
		"/api/v1/operations/alerts",
		"/api/v1/operations/alerts/config",
		"/api/v1/operations/alerts/history",
	}},
	{permission: PermUsersManage, methods: readWrite, patterns: []string{
		"/api/v1/users",
		"/api/v1/users/{id}",
		"/api/v1/users/{id}/password",
		"/api/v1/roles",
		"/api/v1/roles/{id}",
		"/api/v1/audit",
	}, selfAudited: true},
	{permission: "", methods: getOnly, patterns: []string{
		"/api/v1/auth/me",
		"/api/v1/permissions",
	}, selfAudited: true},
	{permission: "", methods: writeOnly, patterns: []string{
		"/api/v1/auth/password",
	}, sessionOnly: true, selfAudited: true},
	{permission: "", methods: readWrite, patterns: []string{
		"/api/v1/auth/tokens",
		"/api/v1/auth/tokens/{id}",
	}, sessionOnly: true, selfAudited: true},
}

type routeMatch struct {
	permission  string
	requires    string
	pattern     string
	sessionOnly bool
	selfAudited bool
}

type compiledRule struct {
	routeMatch
	method   string
	segments []string
	wildcard bool
}

var compiledRoutes = compileRoutes()

func compileRoutes() []compiledRule {
	rules := make([]compiledRule, 0, 256)
	for _, rule := range routeRules {
		for _, method := range rule.methods {
			for _, pattern := range rule.patterns {
				rules = append(rules, compiledRule{
					routeMatch: routeMatch{
						permission:  rule.permission,
						requires:    rule.requires,
						pattern:     pattern,
						sessionOnly: rule.sessionOnly,
						selfAudited: rule.selfAudited,
					},
					method:   method,
					segments: splitPath(pattern),
					wildcard: strings.ContainsAny(pattern, "{*"),
				})
			}
		}
	}
	return rules
}

func splitPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func routePermission(method, path string) (match routeMatch, pathKnown, methodKnown bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	parts := splitPath(path)
	for _, wildcards := range []bool{false, true} {
		for _, rule := range compiledRoutes {
			if rule.wildcard != wildcards || !matchSegments(rule.segments, parts) {
				continue
			}
			pathKnown = true
			if rule.method == method {
				return rule.routeMatch, true, true
			}
		}
	}
	return routeMatch{}, pathKnown, false
}

func matchSegments(pattern, parts []string) bool {
	for index, segment := range pattern {
		if segment == "*" {
			return true
		}
		if index >= len(parts) {
			return false
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			continue
		}
		if segment != parts[index] {
			return false
		}
	}
	return len(pattern) == len(parts)
}
