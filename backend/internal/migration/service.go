// Package migration runs the guided import of an existing media stack into Constellarr.
package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

var (
	ErrInvalid  = errors.New("migration: invalid request")
	ErrNotFound = errors.New("migration: plan not found")
	ErrExpired  = errors.New("migration: the plan expired")
	ErrConflict = errors.New("migration: the plan is being applied")
	ErrUnready  = errors.New("migration: library services are not available")
)

const (
	defaultPlanTTL     = 30 * time.Minute
	defaultApplyBudget = 40 * time.Second
	defaultApplyBatch  = 25
	defaultRetries     = 2
	defaultRetryDelay  = 300 * time.Millisecond
	defaultCallTimeout = 20 * time.Second
	// Catalog listings can be large; every upstream response stays within a finite bound.
	defaultResponseBytes = 32 << 20
	maxConnections       = 12
	maxReasonRunes       = 300
)

// Options wires the shared services the migration imports into. Only the pool is required.
type Options struct {
	Movies           *movies.Service
	TV               *tv.Service
	Music            *music.Service
	Subtitles        *subtitles.Service
	Torrents         *torrents.Service
	Downloads        *downloads.Manager
	PlanTTL          time.Duration
	ApplyBudget      time.Duration
	ApplyBatch       int
	HTTPClient       *http.Client
	Retries          int
	RetryDelay       time.Duration
	CallTimeout      time.Duration
	MaxResponseBytes int64
	Now              func() time.Time
}

type Service struct {
	pool      *pgxpool.Pool
	movies    *movies.Service
	tv        *tv.Service
	music     *music.Service
	subtitles *subtitles.Service
	torrents  *torrents.Service
	downloads *downloads.Manager

	client      *http.Client
	http        *http.Client
	retries     int
	retryDelay  time.Duration
	callTimeout time.Duration
	maxResponse int64
	ttl         time.Duration
	budget      time.Duration
	batch       int
	now         func() time.Time

	secrets secretStore
	applies [64]sync.Mutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func New(ctx context.Context, pool *pgxpool.Pool, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("migration: a PostgreSQL pool is required")
	}
	s := &Service{
		pool: pool, movies: options.Movies, tv: options.TV, music: options.Music,
		subtitles: options.Subtitles, torrents: options.Torrents, downloads: options.Downloads,
		client: options.HTTPClient, retries: options.Retries, retryDelay: options.RetryDelay,
		callTimeout: options.CallTimeout, maxResponse: options.MaxResponseBytes,
		ttl: options.PlanTTL, budget: options.ApplyBudget, batch: options.ApplyBatch,
		now: options.Now,
	}
	s.withDefaults()
	// The downloads migrations own these tables; a clear failure beats a mid-wizard surprise.
	if _, err := pool.Exec(ctx, `SELECT 1 FROM migration_plans LIMIT 0`); err != nil {
		return nil, errors.New("migration: schema is missing; start the downloads service first")
	}
	return s, nil
}

func (s *Service) withDefaults() {
	if s.client == nil {
		s.client = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	}
	// Preserve caller transport settings while enforcing credential-safe redirects.
	guarded := *s.client
	guarded.CheckRedirect = checkRedirect
	s.http = &guarded
	if s.retries == 0 {
		s.retries = defaultRetries
	}
	if s.retryDelay == 0 {
		s.retryDelay = defaultRetryDelay
	}
	if s.callTimeout == 0 {
		s.callTimeout = defaultCallTimeout
	}
	if s.maxResponse == 0 {
		s.maxResponse = defaultResponseBytes
	}
	if s.ttl == 0 {
		s.ttl = defaultPlanTTL
	}
	if s.budget == 0 {
		s.budget = defaultApplyBudget
	}
	if s.batch == 0 {
		s.batch = defaultApplyBatch
	}
	if s.now == nil {
		s.now = time.Now
	}
}

// Start removes expired plans and their held credentials until the context is canceled.
func (s *Service) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.purge(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.purge(ctx)
			}
		}
	}()
}

func (s *Service) Close() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
	s.cancel = nil
}

func (s *Service) purge(ctx context.Context) {
	s.secrets.purge(s.now())
	_, _ = s.pool.Exec(ctx, `DELETE FROM migration_plans WHERE expires_at <= now() - interval '1 hour'`)
}

// applyLock serializes apply calls for one plan inside this process; the database claim covers other processes.
func (s *Service) applyLock(planID string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(planID))
	return &s.applies[hash.Sum32()%uint32(len(s.applies))]
}

func respond(w http.ResponseWriter, status int, body any, err error) {
	if err == nil {
		writeJSON(w, status, body)
		return
	}
	status = http.StatusBadGateway
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrExpired):
		status = http.StatusGone
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, ErrUnready):
		status = http.StatusServiceUnavailable
	case errors.Is(err, ErrInvalid), errors.Is(err, downloads.ErrInvalid), errors.Is(err, movies.ErrInvalid),
		errors.Is(err, tv.ErrInvalid), errors.Is(err, music.ErrInvalid), errors.Is(err, subtitles.ErrInvalid),
		errors.Is(err, torrents.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, movies.ErrNotFound), errors.Is(err, tv.ErrNotFound), errors.Is(err, downloads.ErrNotFound),
		errors.Is(err, music.ErrNotFound), errors.Is(err, subtitles.ErrNotFound), errors.Is(err, torrents.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, movies.ErrConflict), errors.Is(err, tv.ErrConflict), errors.Is(err, downloads.ErrConflict),
		errors.Is(err, music.ErrConflict), errors.Is(err, subtitles.ErrConflict), errors.Is(err, torrents.ErrConflict):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError keeps messages from reaching clients as HTML or with credentials attached.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// decodeBody reads one bounded JSON document and rejects unknown fields and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, body any, limit int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid migration request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "send one migration request")
		return false
	}
	return true
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return fmt.Sprintf("%s…", string(runes[:limit]))
}
