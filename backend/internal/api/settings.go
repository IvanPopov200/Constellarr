package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const maxSettingsBytes = 16 << 10

type settingsResponse struct {
	Indexer struct {
		URL              string `json:"url"`
		APIKeyConfigured bool   `json:"apiKeyConfigured"`
	} `json:"indexer"`
	Usenet struct {
		Host               string   `json:"host"`
		Port               int      `json:"port"`
		Username           string   `json:"username"`
		PasswordConfigured bool     `json:"passwordConfigured"`
		Connections        int      `json:"connections"`
		FallbackHosts      []string `json:"fallbackHosts"`
	} `json:"usenet"`
	Storage struct {
		Directory string `json:"directory"`
	} `json:"storage"`
}

func settingsView(cfg downloads.Config) settingsResponse {
	view := settingsResponse{}
	view.Indexer.URL = cfg.IndexerURL
	view.Indexer.APIKeyConfigured = cfg.APIKey != ""
	view.Usenet.Host = cfg.Usenet.Host
	view.Usenet.Port = cfg.Usenet.Port
	view.Usenet.Username = cfg.Usenet.Username
	view.Usenet.PasswordConfigured = cfg.Usenet.Password != ""
	view.Usenet.Connections = cfg.Usenet.Connections
	view.Usenet.FallbackHosts = cfg.Usenet.FallbackHosts
	if view.Usenet.FallbackHosts == nil {
		view.Usenet.FallbackHosts = []string{}
	}
	view.Storage.Directory = cfg.Directory
	return view
}

func registerSettings(mux *http.ServeMux, m *downloads.Manager) {
	mux.HandleFunc("GET /api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, settingsView(m.Config()))
	})
	mux.HandleFunc("PUT /api/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Indexer struct {
				URL    string `json:"url"`
				APIKey string `json:"apiKey"`
			} `json:"indexer"`
			Usenet struct {
				Host          string   `json:"host"`
				Port          int      `json:"port"`
				Username      string   `json:"username"`
				Password      string   `json:"password"`
				Connections   int      `json:"connections"`
				FallbackHosts []string `json:"fallbackHosts"`
			} `json:"usenet"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid settings request"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send one settings request"})
			return
		}
		err := m.UpdateSettings(r.Context(), downloads.SettingsUpdate{
			IndexerURL:     input.Indexer.URL,
			APIKey:         input.Indexer.APIKey,
			UsenetHost:     input.Usenet.Host,
			UsenetPort:     input.Usenet.Port,
			UsenetUsername: input.Usenet.Username,
			UsenetPassword: input.Usenet.Password,
			Connections:    input.Usenet.Connections,
			FallbackHosts:  input.Usenet.FallbackHosts,
		})
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, settingsView(m.Config()))
	})
}
