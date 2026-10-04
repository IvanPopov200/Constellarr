package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

const (
	maxURLBytes        = 2048
	maxCredentialBytes = 512
)

type InvalidSettingError string

func (e InvalidSettingError) Error() string { return string(e) }

func (e InvalidSettingError) Is(target error) bool { return target == ErrInvalid }

type SettingsUpdate struct {
	IndexerURL     string
	APIKey         string
	UsenetHost     string
	UsenetPort     int
	UsenetUsername string
	UsenetPassword string
	Connections    int
	FallbackHosts  []string
}

// Config returns a snapshot of the active settings with an independent fallback list.
func (m *Manager) Config() Config {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	cfg := m.cfg
	cfg.Usenet.FallbackHosts = append([]string{}, m.cfg.Usenet.FallbackHosts...)
	return cfg
}

func (m *Manager) UpdateSettings(ctx context.Context, update SettingsUpdate) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	merged, client, err := mergeSettings(m.Config(), update)
	if err != nil {
		return err
	}
	if err := saveSettings(ctx, m.pool, merged); err != nil {
		return err
	}
	m.configMu.Lock()
	m.cfg, m.indexer = merged, client
	m.configMu.Unlock()
	return nil
}

func (m *Manager) indexerClient() *indexer.Client {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.indexer
}

func indexerClient(cfg Config) (*indexer.Client, error) {
	if cfg.IndexerURL == "" || cfg.APIKey == "" {
		return nil, nil
	}
	client, err := indexer.New(cfg.IndexerURL, cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("downloads: %w", err)
	}
	return client, nil
}

// mergeSettings keeps saved secrets when the update leaves them blank.
func mergeSettings(current Config, update SettingsUpdate) (Config, *indexer.Client, error) {
	url := strings.TrimSpace(update.IndexerURL)
	if url == "" || len(url) > maxURLBytes {
		return Config{}, nil, InvalidSettingError("indexer URL is not a valid absolute HTTP(S) URL")
	}
	apiKey := strings.TrimSpace(update.APIKey)
	if apiKey == "" {
		apiKey = current.APIKey
	}
	if len(apiKey) > maxCredentialBytes {
		return Config{}, nil, InvalidSettingError("indexer API key is too long")
	}
	client, err := indexer.New(url, apiKey)
	if err != nil {
		return Config{}, nil, InvalidSettingError(err.Error())
	}
	if apiKey == "" {
		client = nil
	}
	host, err := usenet.ValidateHost(update.UsenetHost)
	if err != nil {
		return Config{}, nil, InvalidSettingError(usenetMessage(err))
	}
	if update.UsenetPort < 1 || update.UsenetPort > 65535 {
		return Config{}, nil, InvalidSettingError("usenet port must be between 1 and 65535")
	}
	if update.Connections < 1 || update.Connections > 32 {
		return Config{}, nil, InvalidSettingError("usenet connections must be between 1 and 32")
	}
	username := strings.TrimSpace(update.UsenetUsername)
	if len(username) > maxCredentialBytes {
		return Config{}, nil, InvalidSettingError("usenet username is too long")
	}
	password := update.UsenetPassword
	if strings.TrimSpace(password) == "" {
		password = current.Usenet.Password
	}
	if len(password) > maxCredentialBytes {
		return Config{}, nil, InvalidSettingError("usenet password is too long")
	}
	if username != "" && password == "" {
		return Config{}, nil, InvalidSettingError("usenet password is required when a username is set")
	}
	fallbacks := make([]string, 0, len(update.FallbackHosts))
	if len(update.FallbackHosts) > usenet.MaxFallbackHosts {
		return Config{}, nil, InvalidSettingError(fmt.Sprintf("usenet supports at most %d fallback hosts", usenet.MaxFallbackHosts))
	}
	seen := map[string]bool{strings.ToLower(host): true}
	for i, raw := range update.FallbackHosts {
		fallback, err := usenet.ValidateHost(raw)
		if err != nil {
			return Config{}, nil, InvalidSettingError(fmt.Sprintf("fallback host %d: %s", i+1, usenetMessage(err)))
		}
		key := strings.ToLower(fallback)
		if seen[key] {
			continue
		}
		seen[key] = true
		fallbacks = append(fallbacks, fallback)
	}
	return Config{
		IndexerURL: url,
		APIKey:     apiKey,
		Directory:  current.Directory,
		Usenet: usenet.Config{
			Host: host, Port: update.UsenetPort, Username: username, Password: password,
			Connections: update.Connections, FallbackHosts: fallbacks,
		},
	}, client, nil
}

func usenetMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "usenet: ")
}

const settingsColumns = `indexer_url, indexer_api_key, usenet_host, usenet_port, ` +
	`usenet_username, usenet_password, usenet_connections, usenet_fallback_hosts`

func loadSettings(ctx context.Context, q querier, cfg *Config) error {
	var fallbacks []byte
	err := q.QueryRow(ctx, `SELECT `+settingsColumns+` FROM settings WHERE id`).Scan(
		&cfg.IndexerURL, &cfg.APIKey, &cfg.Usenet.Host, &cfg.Usenet.Port, &cfg.Usenet.Username,
		&cfg.Usenet.Password, &cfg.Usenet.Connections, &fallbacks)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return dbError("load settings", err)
	}
	if err := json.Unmarshal(fallbacks, &cfg.Usenet.FallbackHosts); err != nil {
		return errors.New("downloads: saved settings are invalid")
	}
	return nil
}

func saveSettings(ctx context.Context, q querier, cfg Config) error {
	fallbacks, err := json.Marshal(cfg.Usenet.FallbackHosts)
	if err != nil {
		return errors.New("downloads: settings could not be encoded")
	}
	_, err = q.Exec(ctx,
		`INSERT INTO settings (id, `+settingsColumns+`)
		 VALUES (true, $1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		 ON CONFLICT (id) DO UPDATE SET
		 indexer_url = EXCLUDED.indexer_url, indexer_api_key = EXCLUDED.indexer_api_key,
		 usenet_host = EXCLUDED.usenet_host, usenet_port = EXCLUDED.usenet_port,
		 usenet_username = EXCLUDED.usenet_username, usenet_password = EXCLUDED.usenet_password,
		 usenet_connections = EXCLUDED.usenet_connections, usenet_fallback_hosts = EXCLUDED.usenet_fallback_hosts,
		 updated_at = now()`,
		cfg.IndexerURL, cfg.APIKey, cfg.Usenet.Host, cfg.Usenet.Port, cfg.Usenet.Username,
		cfg.Usenet.Password, cfg.Usenet.Connections, string(fallbacks))
	if err != nil {
		return dbError("save settings", err)
	}
	return nil
}
