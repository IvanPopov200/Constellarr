// Package discovery owns media requests, the combined release calendar, and optional AI recommendations.
package discovery

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery/ai"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

type Service struct {
	pool   *pgxpool.Pool
	movies *movies.Service
	tv     *tv.Service
	music  MusicLibrary
	// AI is the shared OpenAI-compatible provider used by recommendations and subtitle translation.
	AI *ai.Service

	actorFn  Actor
	canFn    Can
	notifyFn func(context.Context, Request)
	now      func() time.Time

	syncMu  sync.Mutex
	queueMu sync.Mutex

	startOnce sync.Once
	cancelMu  sync.Mutex
	cancel    context.CancelFunc
	runCtx    context.Context
	workers   sync.WaitGroup
}

// permits resolves one operation permission; without a hook every extra permission is denied.
func (s *Service) permits(r *http.Request, permission string) bool {
	return s.canFn != nil && s.canFn(r, permission)
}

// permissions reports the caller's rights for the UI without trusting the client.
func (s *Service) permissions(r *http.Request, canApprove bool) Permissions {
	return Permissions{
		Approve:       canApprove || s.permits(r, PermissionRequestsApprove),
		RequestsWrite: s.permits(r, PermissionRequestsWrite),
		LibraryWrite:  s.permits(r, PermissionLibraryWrite),
	}
}

// musicHook treats a typed nil implementation as no hook at all.
func musicHook(hook MusicLibrary) MusicLibrary {
	if hook == nil {
		return nil
	}
	value := reflect.ValueOf(hook)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		if value.IsNil() {
			return nil
		}
	}
	return hook
}

// New wires requests, calendar, and recommendations onto the movie, TV, and optional music services.
func New(ctx context.Context, pool *pgxpool.Pool, movieService *movies.Service, tvService *tv.Service, opts Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("discovery: a PostgreSQL pool is required")
	}
	if movieService == nil {
		return nil, errors.New("discovery: the movie service is required")
	}
	if tvService == nil {
		return nil, errors.New("discovery: the TV service is required")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('discovery_requests') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, errors.New("discovery: the request store is unavailable")
	}
	if !exists {
		return nil, errors.New("discovery: database migrations have not been applied")
	}
	aiService, err := ai.New(ctx, pool)
	if err != nil {
		return nil, err
	}
	now := opts.Clock
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		pool:     pool,
		movies:   movieService,
		tv:       tvService,
		music:    musicHook(opts.Music),
		AI:       aiService,
		actorFn:  opts.Actor,
		canFn:    opts.Can,
		notifyFn: opts.Notify,
		now:      now,
	}, nil
}
