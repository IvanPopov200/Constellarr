package usenet

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const limitReadChunk = 16 << 10

type limitedConn struct {
	net.Conn
	limiter      *rate.Limiter
	waitCtx      context.Context
	cancel       context.CancelFunc
	stopClose    func() bool
	closed       atomic.Bool
	debt         int
	stallTimeout time.Duration

	mu            sync.Mutex
	deadline      time.Time
	deadlineSet   time.Time
	writeDeadline time.Time
	credit        time.Duration
	forced        bool
}

func newLimitedConn(ctx context.Context, conn net.Conn, limiter *rate.Limiter) *limitedConn {
	waitCtx, cancel := context.WithCancel(ctx)
	c := &limitedConn{Conn: conn, limiter: limiter, waitCtx: waitCtx, cancel: cancel, stallTimeout: providerStallTimeout}
	c.stopClose = context.AfterFunc(waitCtx, func() { _ = conn.Close() })
	return c
}

func (c *limitedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	start := time.Now()
	if err := c.payDebt(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	if c.forced {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	// Token waits do not spend the pool's network progress budget.
	if !c.deadline.IsZero() {
		if c.deadlineSet.After(start) {
			start = c.deadlineSet
		}
		c.credit += time.Since(start)
	}
	err := c.Conn.SetReadDeadline(c.readDeadline(time.Now()))
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	size := min(len(p), limitReadChunk)
	if limit := c.limiter.Limit(); limit > 0 && limit < rate.Limit(size)/10 {
		size = max(1, int(limit*10))
	}
	if burst := c.limiter.Burst(); burst > 0 {
		size = min(size, burst)
	}
	n, err := c.Conn.Read(p[:size])
	c.debt += n
	return n, err
}

func (c *limitedConn) payDebt() error {
	for c.debt > 0 {
		if err := c.waitCtx.Err(); err != nil {
			if c.closed.Load() {
				return net.ErrClosed
			}
			return err
		}
		c.mu.Lock()
		forced := c.forced
		c.mu.Unlock()
		if forced {
			return os.ErrDeadlineExceeded
		}
		count := min(c.debt, c.limiter.Burst())
		limit := c.limiter.Limit()
		// Short token payments respond to live rate changes without stale reservations.
		if limit > 0 && limit < rate.Limit(count)*10 {
			count = min(count, max(1, int(limit/10)))
		}
		if count > 0 && limit > 0 && c.limiter.AllowN(time.Now(), count) {
			c.debt -= count
			continue
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-c.waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

func (c *limitedConn) readDeadline(now time.Time) time.Time {
	target := now.Add(c.stallTimeout)
	if !c.deadline.IsZero() {
		if requested := c.deadline.Add(c.credit); requested.Before(target) {
			target = requested
		}
	}
	return target
}

func (c *limitedConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if !t.Equal(c.deadline) {
		c.credit, c.deadlineSet = 0, now
	}
	c.deadline, c.forced = t, !t.IsZero() && !t.After(now)
	if c.forced {
		c.credit = 0
	}
	return c.Conn.SetReadDeadline(c.readDeadline(now))
}

func (c *limitedConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	if bound := time.Now().Add(c.stallTimeout); t.IsZero() || bound.Before(t) {
		t = bound
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *limitedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	target := time.Now().Add(c.stallTimeout)
	if !c.writeDeadline.IsZero() && c.writeDeadline.Before(target) {
		target = c.writeDeadline
	}
	err := c.Conn.SetWriteDeadline(target)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

func (c *limitedConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *limitedConn) Close() error {
	c.closed.Store(true)
	c.stopClose()
	c.cancel()
	return c.Conn.Close()
}
