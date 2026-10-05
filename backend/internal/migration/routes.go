package migration

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	testBodyBytes     = 64 << 10
	previewBodyBytes  = 64 << 10
	applyBodyBytes    = 512 << 10
	previewTimeout    = 50 * time.Second
	connectionTimeout = 25 * time.Second
)

type connectionTest struct {
	App     App    `json:"app"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Register adds the migration wizard routes; the API layer's origin and content-type checks run first.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/migration/connections/test", s.handleTest)
	mux.HandleFunc("POST /api/v1/migration/preview", s.handlePreview)
	mux.HandleFunc("GET /api/v1/migration/plans/{id}", s.handlePlan)
	mux.HandleFunc("POST /api/v1/migration/plans/{id}/apply", s.handleApply)
}

func (s *Service) handleTest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Connections []Connection `json:"connections"`
	}
	if !decodeBody(w, r, &input, testBodyBytes) {
		return
	}
	connections, err := validateConnections(input.Connections)
	if err != nil {
		respond(w, 0, nil, sanitizeErr(err, connectSecrets(input.Connections)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectionTimeout)
	defer cancel()
	results := make([]connectionTest, len(connections))
	var group sync.WaitGroup
	for i, connection := range connections {
		group.Add(1)
		go func(i int, connection Connection) {
			defer group.Done()
			version, err := s.connect(ctx, connection)
			result := connectionTest{App: connection.App, Version: version}
			if err != nil {
				result.Error = sanitize(err.Error(), connection.secrets())
			} else {
				result.OK = true
			}
			results[i] = result
		}(i, connection)
	}
	group.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].App < results[j].App })
	writeJSON(w, http.StatusOK, struct {
		Results []connectionTest `json:"results"`
	}{results})
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Connections []Connection `json:"connections"`
	}
	if !decodeBody(w, r, &input, previewBodyBytes) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), previewTimeout)
	defer cancel()
	view, err := s.Preview(ctx, input.Connections)
	if err != nil {
		respond(w, 0, nil, sanitizeErr(err, connectSecrets(input.Connections)))
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Service) handlePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		respond(w, 0, nil, ErrNotFound)
		return
	}
	plan, err := s.load(r.Context(), id)
	if err != nil {
		respond(w, 0, nil, err)
		return
	}
	view, err := s.view(r.Context(), plan)
	respond(w, http.StatusOK, view, err)
}

func (s *Service) handleApply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		respond(w, 0, nil, ErrNotFound)
		return
	}
	var input ApplyInput
	if !decodeBody(w, r, &input, applyBodyBytes) {
		return
	}
	for _, roots := range []*map[string]string{&input.MoviesRoots, &input.TVRoots, &input.MusicRoots, &input.TorrentRoots, &input.Profiles, &input.MusicProfiles} {
		if *roots == nil {
			*roots = map[string]string{}
		}
	}
	result, err := s.Apply(r.Context(), id, input)
	if err != nil {
		respond(w, 0, nil, sanitizeErr(err, connectSecrets(input.Connections)))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
