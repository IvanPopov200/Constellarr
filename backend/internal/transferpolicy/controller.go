package transferpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

var errDatabase = errors.New("transferpolicy: database operation failed")

// policyLock serializes policy writes across processes, continuing the downloads lock family.
const policyLock int64 = 0x436F6E7374656C + 3

// burstBytes is the shared limiter burst; 1 MiB matches torrent read chunks and keeps limit changes responsive.
const burstBytes = 1 << 20

type Controller struct {
	pool    *pgxpool.Pool
	limiter *rate.Limiter

	// writeMu serializes persistence so SetPaused cannot overwrite a concurrent Update.
	writeMu sync.Mutex

	mu        sync.Mutex
	cfg       Config
	gen       uint64
	effective Effective
	nextAt    time.Time
	nextGen   uint64
	nextValid bool

	now func() time.Time
}

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// New loads the saved policy; downloads migrations must have created the policy table.
func New(ctx context.Context, pool *pgxpool.Pool) (*Controller, error) {
	if pool == nil {
		return nil, errors.New("transferpolicy: a PostgreSQL pool is required")
	}
	cfg, err := loadConfig(ctx, pool)
	if err != nil {
		return nil, err
	}
	c := &Controller{
		pool: pool, limiter: rate.NewLimiter(rate.Inf, burstBytes), cfg: cfg, now: time.Now,
	}
	c.refresh(c.now())
	return c, nil
}

func (c *Controller) Snapshot() Snapshot {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(now)
	eff := c.effective
	if eff.NextChange != nil {
		next := *eff.NextChange
		eff.NextChange = &next
	}
	return Snapshot{Config: cloneConfig(c.cfg), Effective: eff}
}

func (c *Controller) Update(ctx context.Context, cfg Config) (Snapshot, error) {
	next, err := validate(cfg)
	if err != nil {
		return Snapshot{}, err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	stored, err := c.updateStored(ctx, func(current *Config) { *current = next })
	if err != nil {
		return Snapshot{}, err
	}
	c.apply(stored)
	return c.Snapshot(), nil
}

// SetPaused flips only the manual pause flag, leaving the stored schedule intact.
func (c *Controller) SetPaused(ctx context.Context, paused bool) (Snapshot, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	stored, err := c.updateStored(ctx, func(cfg *Config) { cfg.Paused = paused })
	if err != nil {
		return Snapshot{}, err
	}
	c.apply(stored)
	return c.Snapshot(), nil
}

func (c *Controller) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.refresh(c.now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Limiter returns the process-wide limiter; the pointer is stable and its rate is never zero.
func (c *Controller) Limiter() *rate.Limiter { return c.limiter }

// Allowed reports whether transfers may run at the current clock without waiting for the ticker.
func (c *Controller) Allowed() bool {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	return !decide(cfg, c.now(), nil).paused
}

func (c *Controller) apply(cfg Config) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg, c.gen, c.nextValid = cfg, c.gen+1, false
	c.refreshLocked(now)
}

func (c *Controller) refresh(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(now)
}

// refreshLocked recomputes the effect and reuses the cached next change until it passes.
func (c *Controller) refreshLocked(now time.Time) {
	if !c.nextValid || c.nextGen != c.gen || (!c.nextAt.IsZero() && !now.Before(c.nextAt)) {
		c.nextAt = time.Time{}
		if next, ok := nextChangeAt(c.cfg, now); ok {
			c.nextAt = next
		}
		c.nextValid, c.nextGen = true, c.gen
	}
	eff := evaluate(c.cfg, now, nil)
	if !c.nextAt.IsZero() {
		next := c.nextAt
		eff.NextChange = &next
	}
	if eff.Paused != c.effective.Paused || eff.LimitBytesPerSecond != c.effective.LimitBytesPerSecond {
		applyLimiter(c.limiter, eff)
	}
	c.effective = eff
}

// applyLimiter maps the effect onto the shared limiter; a pause keeps the last usable rate.
func applyLimiter(limiter *rate.Limiter, eff Effective) {
	if eff.Paused {
		// The pause is enforced by Allowed() plus stopping or holding runs.
		return
	}
	if eff.LimitBytesPerSecond > 0 {
		limiter.SetLimit(rate.Limit(eff.LimitBytesPerSecond))
		return
	}
	limiter.SetLimit(rate.Inf)
}

func (c *Controller) updateStored(ctx context.Context, mutate func(*Config)) (Config, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return Config{}, dbError("save policy", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, policyLock); err != nil {
		return Config{}, dbError("save policy", err)
	}
	cfg, err := loadConfig(ctx, tx)
	if err != nil {
		return Config{}, err
	}
	mutate(&cfg)
	cfg = normalize(cfg)
	if err := saveConfig(ctx, tx, cfg); err != nil {
		return Config{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, dbError("save policy", err)
	}
	return cfg, nil
}

func loadConfig(ctx context.Context, q querier) (Config, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT config FROM download_policy WHERE id`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, dbError("load policy", err)
	}
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, errors.New("transferpolicy: saved policy is invalid")
	}
	return normalize(cfg), nil
}

func saveConfig(ctx context.Context, q querier, cfg Config) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return errors.New("transferpolicy: policy could not be encoded")
	}
	_, err = q.Exec(ctx,
		`INSERT INTO download_policy (id, config, updated_at) VALUES (true, $1::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET config = EXCLUDED.config, updated_at = now()`, string(raw))
	if err != nil {
		return dbError("save policy", err)
	}
	return nil
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("transferpolicy: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("transferpolicy: %s: %w", op, errDatabase)
}

func cloneConfig(cfg Config) Config {
	clone := cfg
	clone.Windows = make([]Window, len(cfg.Windows))
	for i, window := range cfg.Windows {
		clone.Windows[i] = window
		clone.Windows[i].Days = append([]int{}, window.Days...)
	}
	return clone
}
