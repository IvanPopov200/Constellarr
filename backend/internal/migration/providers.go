package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxIndexerInspections = 25

type prowlarrIndexer struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Enable         bool   `json:"enable"`
	Implementation string `json:"implementation"`
	DefinitionName string `json:"definitionName"`
	Protocol       string `json:"protocol"`
	Fields         []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	} `json:"fields"`
}

func indexerCandidateKey(id int) string { return fmt.Sprintf("%s:%d", AppProwlarr, id) }

// discoverProwlarr lists NZB indexer candidates and torrent indexers for the torrent module.
func (s *Service) discoverProwlarr(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	headers := map[string]string{"X-Api-Key": connection.APIKey}
	var status arrStatus
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/system/status", headers, nil, &status); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var indexers []prowlarrIndexer
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api/v1/indexer", headers, nil, &indexers); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	out := snapshot{Versions: map[App]string{AppProwlarr: status.Version}}
	secrets := providerSecrets{indexers: map[string]indexerSecret{}}
	inspected := 0
	for _, indexer := range indexers {
		if !indexer.Enable || inspected >= maxIndexerInspections {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(indexer.Protocol))
		if protocol != "usenet" && protocol != "torrent" {
			continue
		}
		inspected++
		detail := indexer
		if len(detail.Fields) == 0 {
			if err := s.requestJSON(ctx, connection, http.MethodGet, fmt.Sprintf("/api/v1/indexer/%d", indexer.ID), headers, nil, &detail); err != nil {
				out.Warnings = append(out.Warnings, sanitize(fmt.Sprintf("Prowlarr indexer %s could not be read: %s", indexer.Name, err.Error()), connection.secrets()))
				continue
			}
		}
		baseURL, _ := prowlarrField(detail, "baseurl")
		apiPath, _ := prowlarrField(detail, "apipath")
		apiKey, _ := prowlarrField(detail, "apikey")
		name := strings.TrimSpace(detail.Name)
		if name == "" {
			name = fmt.Sprintf("Indexer %d", detail.ID)
		}
		if protocol == "torrent" {
			out.Torznab = append(out.Torznab, TorznabPlan{
				Key: indexerCandidateKey(indexer.ID), Source: AppProwlarr, Name: name,
				URL:      fmt.Sprintf("%s/%d/api", connection.URL, indexer.ID),
				Protocol: "torrent", APIKeySet: connection.APIKey != "",
			})
			continue
		}
		feedURL := newznabURL(baseURL, apiPath)
		if !prowlarrLooksLikeNZBGeek(name) && !prowlarrLooksLikeNZBGeek(feedURL) {
			out.Unsupported = append(out.Unsupported, Unsupported{
				Area:   "indexer",
				Detail: fmt.Sprintf("Prowlarr indexer %q is not NZBGeek; Constellarr's built-in Usenet search supports NZBGeek only for now", name),
			})
			continue
		}
		if feedURL == "" || apiKey == "" {
			out.Unsupported = append(out.Unsupported, Unsupported{Area: "indexer", Detail: fmt.Sprintf("Prowlarr indexer %q has no readable base URL or API key", name)})
			continue
		}
		key := indexerCandidateKey(indexer.ID)
		out.Indexers = append(out.Indexers, IndexerPlan{Key: key, Source: AppProwlarr, Name: name, URL: feedURL, APIKeySet: true})
		secrets.indexers[key] = indexerSecret{Name: name, URL: feedURL, APIKey: apiKey}
	}
	if len(out.Torznab) > 0 && connection.APIKey == "" {
		out.Warnings = append(out.Warnings, "Prowlarr torrent indexers need an API key to be imported as Torznab sources")
	}
	return out, secrets, nil
}

func newznabURL(baseURL, apiPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	apiPath = strings.Trim(apiPath, "/")
	if apiPath != "" && !strings.HasSuffix(base, "/"+apiPath) {
		base += "/" + apiPath
	}
	return base
}

func prowlarrField(indexer prowlarrIndexer, name string) (string, bool) {
	for _, field := range indexer.Fields {
		if strings.EqualFold(field.Name, name) {
			value, _ := field.Value.(string)
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func prowlarrLooksLikeNZBGeek(value string) bool {
	return strings.Contains(strings.ToLower(value), "nzbgeek")
}

type sabServer struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	SSL         int    `json:"ssl"`
	Enable      int    `json:"enable"`
}

type sabConfig struct {
	Config struct {
		Servers []sabServer `json:"servers"`
	} `json:"config"`
}

func (s *Service) discoverSABnzbd(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	var config sabConfig
	query := url.Values{"mode": {"get_config"}, "output": {"json"}, "apikey": {connection.APIKey}}
	if err := s.requestJSON(ctx, connection, http.MethodGet, "/api?"+query.Encode(), nil, nil, &config); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var server *sabServer
	for i := range config.Config.Servers {
		if config.Config.Servers[i].Enable != 0 {
			server = &config.Config.Servers[i]
			break
		}
	}
	if server == nil {
		return snapshot{}, providerSecrets{}, fmt.Errorf("%w: SABnzbd has no enabled news server", ErrInvalid)
	}
	host := strings.TrimSpace(server.Host)
	if host == "" {
		host = strings.TrimSpace(server.Name)
	}
	if host == "" {
		return snapshot{}, providerSecrets{}, fmt.Errorf("%w: the SABnzbd news server has no host", ErrInvalid)
	}
	plan := UsenetPlan{
		Key: string(AppSABnzbd), Source: AppSABnzbd, Host: host, Port: server.Port,
		UsernameSet: server.Username != "", Connections: server.Connections, TLS: server.SSL != 0,
	}
	if server.SSL == 0 {
		plan.Notes = append(plan.Notes, "SABnzbd serves this server without TLS; Constellarr always connects with TLS")
	}
	secrets := providerSecrets{usenet: map[string]usenetSecret{plan.Key: {
		Host: host, Port: server.Port, Username: server.Username, Password: server.Password, Connections: server.Connections,
	}}}
	return snapshot{UsenetSources: []UsenetPlan{plan}}, secrets, nil
}

type nzbgetEnvelope struct {
	Result json.RawMessage `json:"result"`
}

func (s *Service) discoverNZBGet(ctx context.Context, connection Connection) (snapshot, providerSecrets, error) {
	var version nzbgetEnvelope
	if err := s.requestJSON(ctx, connection, http.MethodPost, "/jsonrpc", nil, map[string]any{"method": "version"}, &version); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var config nzbgetEnvelope
	if err := s.requestJSON(ctx, connection, http.MethodPost, "/jsonrpc", nil, map[string]any{"method": "config"}, &config); err != nil {
		return snapshot{}, providerSecrets{}, err
	}
	var entries []struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	if err := json.Unmarshal(config.Result, &entries); err != nil {
		return snapshot{}, providerSecrets{}, fmt.Errorf("%w: NZBGet returned an unexpected configuration payload", ErrInvalid)
	}
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		values[entry.Name] = entry.Value
	}
	if !strings.EqualFold(values["Server1.Active"], "yes") {
		return snapshot{}, providerSecrets{}, fmt.Errorf("%w: NZBGet has no active news server", ErrInvalid)
	}
	port, portErr := strconv.Atoi(values["Server1.Port"])
	connections, connectionsErr := strconv.Atoi(values["Server1.Connections"])
	host := strings.TrimSpace(values["Server1.Host"])
	if portErr != nil || port < 1 || port > 65535 || connectionsErr != nil || host == "" {
		return snapshot{}, providerSecrets{}, fmt.Errorf("%w: the NZBGet news server settings are incomplete", ErrInvalid)
	}
	plan := UsenetPlan{
		Key: string(AppNZBGet), Source: AppNZBGet, Host: host, Port: port,
		UsernameSet: values["Server1.Username"] != "", Connections: connections,
		TLS: strings.EqualFold(values["Server1.Encryption"], "yes"),
	}
	if !plan.TLS {
		plan.Notes = append(plan.Notes, "NZBGet serves this server without encryption; Constellarr always connects with TLS")
	}
	var extra []string
	for name, value := range values {
		if strings.HasPrefix(name, "Server") && strings.HasSuffix(name, ".Active") && name != "Server1.Active" && strings.EqualFold(value, "yes") {
			extra = append(extra, strings.TrimSuffix(name, ".Active"))
		}
	}
	if len(extra) > 0 {
		sortStrings(extra)
		plan.Notes = append(plan.Notes, "additional NZBGet news servers are not imported: "+strings.Join(extra, ", "))
	}
	secrets := providerSecrets{usenet: map[string]usenetSecret{plan.Key: {
		Host: host, Port: port, Username: values["Server1.Username"], Password: values["Server1.Password"], Connections: connections,
	}}}
	return snapshot{UsenetSources: []UsenetPlan{plan}}, secrets, nil
}

// selectIndexer resolves the single NZB indexer slot and explains why a choice is missing.
func selectIndexer(plan *Plan, key string) (IndexerPlan, string) {
	if key == "" {
		if len(plan.Indexers) == 1 {
			return plan.Indexers[0], ""
		}
		if len(plan.Indexers) == 0 {
			return IndexerPlan{}, "no NZB indexer was found"
		}
		return IndexerPlan{}, fmt.Sprintf("%d indexers were found; select one in the wizard", len(plan.Indexers))
	}
	for _, candidate := range plan.Indexers {
		if candidate.Key == key {
			return candidate, ""
		}
	}
	return IndexerPlan{}, "the selected indexer is not part of this plan"
}

// newsSelection is the deterministic news server choice plus a reason for every unused source.
type newsSelection struct {
	primaryKey    string
	primarySource App
	fallbackHosts []string
	fallbackHost  map[string]string
	skipped       map[string]string
	reason        string
}

// selectNewsServers resolves the primary news server and compatible fallback hosts.
func selectNewsServers(plan *Plan, primaryKey string, fallbackKeys []string, secrets providerSecrets) newsSelection {
	selection := newsSelection{fallbackHost: map[string]string{}, skipped: map[string]string{}}
	primary := strings.TrimSpace(primaryKey)
	switch {
	case primary != "":
	case len(plan.UsenetSources) == 1:
		primary = plan.UsenetSources[0].Key
	case len(plan.UsenetSources) == 0:
		selection.reason = "no news server was found"
		return selection
	default:
		selection.reason = fmt.Sprintf("%d news servers were found; select one in the wizard", len(plan.UsenetSources))
		return selection
	}
	var chosen UsenetPlan
	found := false
	for _, candidate := range plan.UsenetSources {
		if candidate.Key == primary {
			chosen, found = candidate, true
			break
		}
	}
	if !found {
		selection.reason = "the selected news server is not part of this plan"
		return selection
	}
	selection.primaryKey, selection.primarySource = chosen.Key, chosen.Source
	primarySecret, hasPrimary := secrets.newsServer(primary)
	for _, key := range fallbackKeys {
		if key == primary {
			continue
		}
		var candidate UsenetPlan
		matched := false
		for _, option := range plan.UsenetSources {
			if option.Key == key {
				candidate, matched = option, true
				break
			}
		}
		if !matched {
			selection.skipped[key] = "this source is not part of the plan"
			continue
		}
		secret, ok := secrets.newsServer(key)
		if !hasPrimary || !ok || secret.Username != primarySecret.Username || secret.Password != primarySecret.Password {
			selection.skipped[key] = fmt.Sprintf("%s has different credentials and cannot share the primary account", candidate.Host)
			continue
		}
		selection.fallbackHost[candidate.Key] = candidate.Host
		selection.fallbackHosts = append(selection.fallbackHosts, candidate.Host)
	}
	for _, candidate := range plan.UsenetSources {
		if candidate.Key == primary {
			continue
		}
		if _, ok := selection.skipped[candidate.Key]; !ok {
			selection.skipped[candidate.Key] = "another news server was selected in the wizard"
		}
	}
	return selection
}

// refreshProviderSecrets re-reads provider credentials and keeps the plan usable after a restart.
func (s *Service) refreshProviderSecrets(ctx context.Context, plan *Plan, connections []Connection) *secretEntry {
	secrets, err := s.discoverProviderSecrets(ctx, connections)
	if err != nil || (len(secrets.indexers) == 0 && len(secrets.usenet) == 0 && secrets.subtitle == nil) {
		return nil
	}
	current := s.secrets.get(plan.ID, s.now())
	if current == nil {
		current = &secretEntry{expires: plan.ExpiresAt}
	}
	current.connections = connections
	current.providers = secrets
	current.expires = s.now().Add(s.ttl)
	s.secrets.put(plan.ID, *current)
	return current
}

// providerWorkPending reports whether the batch needs credentials the in-memory entry lost.
func providerWorkPending(plan *Plan, pending []PlanItem, entry *secretEntry) bool {
	for _, item := range pending {
		switch item.Kind {
		case itemIndexer:
			if len(entry.providers.indexers) == 0 {
				return true
			}
		case itemUsenet:
			if len(entry.providers.usenet) == 0 {
				return true
			}
		case itemSubtitleProvider:
			if entry.providers.subtitle == nil && plan != nil && plan.Subtitles != nil {
				for _, provider := range plan.Subtitles.ProviderPlans {
					if provider.UsernameSet || provider.PasswordSet {
						return true
					}
				}
			}
		case itemTorznab:
			if _, ok := connectionFor(entry.connections, AppProwlarr); !ok {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
