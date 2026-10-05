package migration

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

type App string

const (
	AppRadarr       App = "radarr"
	AppSonarr       App = "sonarr"
	AppLidarr       App = "lidarr"
	AppProwlarr     App = "prowlarr"
	AppBazarr       App = "bazarr"
	AppSABnzbd      App = "sabnzbd"
	AppNZBGet       App = "nzbget"
	AppTransmission App = "transmission"
	AppJellyfin     App = "jellyfin"
)

var apps = map[App]bool{
	AppRadarr: true, AppSonarr: true, AppLidarr: true, AppProwlarr: true, AppBazarr: true,
	AppSABnzbd: true, AppNZBGet: true, AppTransmission: true, AppJellyfin: true,
}

// Connection describes one source application; credentials stay in memory for the live plan.
type Connection struct {
	App      App    `json:"app"`
	URL      string `json:"url"`
	APIKey   string `json:"apiKey,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func (c Connection) normalize() Connection {
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Username = strings.TrimSpace(c.Username)
	return c
}

func (c Connection) validate() error {
	if !apps[c.App] {
		return fmt.Errorf("%w: %q is not a supported application", ErrInvalid, c.App)
	}
	parsed, err := url.Parse(c.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(c.URL) > 2048 {
		return fmt.Errorf("%w: %s needs an absolute http or https URL", ErrInvalid, c.App)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: %s credentials belong in the credential fields, not the URL", ErrInvalid, c.App)
	}
	if len(c.APIKey) > 512 || len(c.Username) > 512 || len(c.Password) > 512 {
		return fmt.Errorf("%w: %s credentials are too long", ErrInvalid, c.App)
	}
	switch c.App {
	case AppRadarr, AppSonarr, AppLidarr, AppProwlarr, AppBazarr, AppJellyfin, AppSABnzbd:
		if c.APIKey == "" {
			return fmt.Errorf("%w: %s requires an API key", ErrInvalid, c.App)
		}
	case AppNZBGet:
		if c.Username == "" || c.Password == "" {
			return fmt.Errorf("%w: %s requires a username and password", ErrInvalid, c.App)
		}
	case AppTransmission:
		if (c.Username == "") != (c.Password == "") {
			return fmt.Errorf("%w: %s requires both a username and a password or neither", ErrInvalid, c.App)
		}
	}
	return nil
}

// secrets lists values that must never appear in responses or error text.
func (c Connection) secrets() []string {
	return []string{c.APIKey, c.Password, c.Username}
}

func validateConnections(connections []Connection) ([]Connection, error) {
	if len(connections) == 0 || len(connections) > maxConnections {
		return nil, fmt.Errorf("%w: provide between 1 and %d connections", ErrInvalid, maxConnections)
	}
	normalized := make([]Connection, 0, len(connections))
	seen := make(map[App]bool, len(connections))
	for _, connection := range connections {
		connection = connection.normalize()
		if err := connection.validate(); err != nil {
			return nil, err
		}
		if seen[connection.App] {
			return nil, fmt.Errorf("%w: %s is listed twice", ErrInvalid, connection.App)
		}
		seen[connection.App] = true
		normalized = append(normalized, connection)
	}
	return normalized, nil
}

func connectionFor(connections []Connection, app App) (Connection, bool) {
	for _, connection := range connections {
		if connection.App == app {
			return connection, true
		}
	}
	return Connection{}, false
}

// sanitize removes credential values from any text headed to a client or log line.
func sanitize(message string, secrets []string) string {
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")
	for _, secret := range secrets {
		if len(secret) < 3 {
			continue
		}
		message = strings.ReplaceAll(message, secret, "***")
	}
	return strings.TrimSpace(truncateRunes(message, maxReasonRunes))
}

func connectSecrets(connections []Connection) []string {
	var secrets []string
	for _, connection := range connections {
		secrets = append(secrets, connection.secrets()...)
	}
	return secrets
}

type indexerSecret struct {
	Name   string
	URL    string
	APIKey string
}

type usenetSecret struct {
	Host        string
	Port        int
	Username    string
	Password    string
	Connections int
}

type providerSecrets struct {
	indexers map[string]indexerSecret
	usenet   map[string]usenetSecret
	subtitle *subtitleProviderSecret
}

type subtitleProviderSecret struct {
	Username string
	Password string
}

func (s *providerSecrets) indexer(key string) (indexerSecret, bool) {
	secret, ok := s.indexers[key]
	return secret, ok
}

func (s *providerSecrets) newsServer(key string) (usenetSecret, bool) {
	secret, ok := s.usenet[key]
	return secret, ok
}

type secretEntry struct {
	connections []Connection
	providers   providerSecrets
	expires     time.Time
}

// secretStore keeps credentials for live plans only; restarting the server discards them.
type secretStore struct {
	mu      sync.Mutex
	entries map[string]*secretEntry
}

func (s *secretStore) put(planID string, entry secretEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*secretEntry)
	}
	s.entries[planID] = &entry
}

func (s *secretStore) get(planID string, now time.Time) *secretEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[planID]
	if !ok || !entry.expires.After(now) {
		delete(s.entries, planID)
		return nil
	}
	return entry
}

func (s *secretStore) drop(planID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, planID)
}

func (s *secretStore) purge(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for planID, entry := range s.entries {
		if !entry.expires.After(now) {
			delete(s.entries, planID)
		}
	}
}

// rearm keeps a plan usable after a restart by accepting credentials again.
func (s *secretStore) rearm(planID string, connections []Connection, ttl time.Duration, now time.Time) {
	if len(connections) == 0 {
		return
	}
	entry := s.get(planID, now)
	if entry == nil {
		entry = &secretEntry{}
	}
	entry.connections = connections
	entry.expires = now.Add(ttl)
	s.put(planID, *entry)
}

var errMissingCredentials = errors.New("credentials are no longer held; run the connection check again or resend connections")
