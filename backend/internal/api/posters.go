package api

import (
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
)

func registerMoviePosters(mux *http.ServeMux, service *movies.Service) {
	mux.HandleFunc("GET /api/v1/movie-poster", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("imdbId")
		if !metadata.ValidIMDbID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid IMDb ID"})
			return
		}
		cache := filepath.Join(service.Downloads.Config().Directory, "posters")
		if err := os.MkdirAll(cache, 0o700); err != nil {
			posterError(w)
			return
		}
		root, err := os.OpenRoot(cache)
		if err != nil {
			posterError(w)
			return
		}
		defer root.Close()
		name := id + ".image"
		if file, err := root.Open(name); err == nil {
			info, statErr := file.Stat()
			if statErr == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) < 24*time.Hour {
				defer file.Close()
				servePoster(w, r, file)
				return
			}
			file.Close()
		}
		cfg, err := service.Store.Config(r.Context())
		if err != nil || cfg.MetadataAPIKey == "" {
			posterError(w)
			return
		}
		baseURL := os.Getenv("OMDB_POSTER_URL")
		if baseURL == "" {
			baseURL = "https://img.omdbapi.com/"
		}
		base, err := url.Parse(baseURL)
		if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			posterError(w)
			return
		}
		target := *base
		target.RawQuery = url.Values{"i": {id}, "h": {"900"}, "apikey": {cfg.MetadataAPIKey}}.Encode()
		client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Host != base.Host || req.URL.Scheme != base.Scheme {
				return errors.New("poster redirect rejected")
			}
			return nil
		}}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			posterError(w)
			return
		}
		response, err := client.Do(request)
		if err != nil {
			posterError(w)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			posterError(w)
			return
		}
		temporary := id + "." + rand.Text() + ".tmp"
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			posterError(w)
			return
		}
		defer root.Remove(temporary)
		count, copyErr := io.Copy(file, io.LimitReader(response.Body, (10<<20)+1))
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || count == 0 || count > 10<<20 {
			posterError(w)
			return
		}
		check, err := root.Open(temporary)
		if err != nil {
			posterError(w)
			return
		}
		valid := posterType(check) != ""
		check.Close()
		if !valid {
			posterError(w)
			return
		}
		if err := root.Rename(temporary, name); err != nil {
			posterError(w)
			return
		}
		file, err = root.Open(name)
		if err != nil {
			posterError(w)
			return
		}
		defer file.Close()
		servePoster(w, r, file)
	})
}

func posterType(file *os.File) string {
	bytes := make([]byte, 512)
	count, _ := file.ReadAt(bytes, 0)
	kind := http.DetectContentType(bytes[:count])
	if kind == "image/jpeg" || kind == "image/png" || kind == "image/webp" {
		return kind
	}
	return ""
}

func servePoster(w http.ResponseWriter, r *http.Request, file *os.File) {
	info, err := file.Stat()
	kind := posterType(file)
	if err != nil || kind == "" {
		posterError(w)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func posterError(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "movie poster is currently unavailable"})
}

func posterURLs(body any) any {
	convert := func(title metadata.Title) metadata.Title {
		if title.Poster != "" && metadata.ValidIMDbID(title.IMDbID) {
			title.Poster = "/api/v1/movie-poster?imdbId=" + url.QueryEscape(title.IMDbID)
		}
		return title
	}
	switch value := body.(type) {
	case metadata.Title:
		return convert(value)
	case []metadata.Title:
		result := make([]metadata.Title, len(value))
		for i, title := range value {
			result[i] = convert(title)
		}
		return result
	case movies.Movie:
		value.Metadata = convert(value.Metadata)
		return value
	case []movies.Movie:
		result := make([]movies.Movie, len(value))
		for i, movie := range value {
			movie.Metadata = convert(movie.Metadata)
			result[i] = movie
		}
		return result
	}
	return body
}
