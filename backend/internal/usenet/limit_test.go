package usenet

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// Time spent paying for bytes must not count as a stalled provider for either clock.
func TestLimitedConnPaymentDoesNotSpendTheStallBudget(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	limiter := rate.NewLimiter(5, 1) // one token per 200ms
	if err := limiter.WaitN(context.Background(), 1); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}
	conn := newLimitedConn(context.Background(), client, limiter)
	conn.stallTimeout = 200 * time.Millisecond
	defer conn.Close()

	// The first byte is free, leaving the token debt the next read has to pay.
	go func() { _, _ = server.Write([]byte{'x'}) }()
	buf := make([]byte, 4)
	if n, err := conn.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	go func() {
		time.Sleep(240 * time.Millisecond)
		_, _ = server.Write([]byte{'y'})
	}()
	start := time.Now()
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read after a payment wait: %v", err)
	}
	if n != 1 || buf[0] != 'y' {
		t.Fatalf("read %d bytes %q, want the second byte", n, buf[:n])
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Fatalf("read returned after %s without paying its debt", waited)
	}
}

// A tightened deadline must reach the socket even while a token debt is outstanding.
func TestLimitedConnEnforcesTightenedDeadlineWithTokenDebt(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := newLimitedConn(context.Background(), client, rate.NewLimiter(rate.Inf, 1<<20))
	defer conn.Close()

	go func() { _, _ = server.Write([]byte{'x'}) }()
	buf := make([]byte, 8)
	if n, err := conn.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	start := time.Now()
	_, err := conn.Read(buf)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("read with a tight deadline = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tight deadline took %s to fire", elapsed)
	}
}

// A deadline set while a read is already blocked must reach the socket.
func TestLimitedConnForwardsDeadlineToBlockedRead(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := newLimitedConn(context.Background(), client, rate.NewLimiter(rate.Inf, 1<<20))
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 8))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	select {
	case err := <-done:
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("blocked read error = %v, want a timeout", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("tightened deadline took %s to fire", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SetReadDeadline did not reach a blocked read")
	}
}

// The wrapper enforces its own stall timeout even when the pool's deadline is far away.
func TestLimitedConnStillTimesOutOnStall(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn := newLimitedConn(context.Background(), client, rate.NewLimiter(rate.Inf, 1<<20))
	conn.stallTimeout = 50 * time.Millisecond
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	start := time.Now()
	_, err := conn.Read(make([]byte, 8))
	if err == nil {
		t.Fatal("a silent connection returned data")
	}
	if !timeoutError(err) {
		t.Fatalf("stall error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stall took %s, want it bounded by the wrapper's stall timeout", elapsed)
	}
}

// Paying for bytes must be cancellable through the client context, even at a low cap.
func TestLimitedConnPaymentIsCancellable(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limiter := rate.NewLimiter(0.5, 1) // one token every two seconds
	if err := limiter.WaitN(ctx, 1); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}
	conn := newLimitedConn(ctx, client, limiter)
	defer conn.Close()

	go func() { _, _ = server.Write([]byte{'x'}) }()
	if n, err := conn.Read(make([]byte, 8)); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("payment read error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %s, want it prompt", elapsed)
	}
}

func TestLimitedConnCloseReleasesPayment(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	limiter := rate.NewLimiter(0.5, 1)
	if err := limiter.WaitN(context.Background(), 1); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}
	conn := newLimitedConn(context.Background(), client, limiter)
	go func() { _, _ = server.Write([]byte{'x'}) }()
	if n, err := conn.Read(make([]byte, 8)); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 8))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = conn.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed read error = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release a blocked payment")
	}
}

func TestSegmentContextKeepsCappedTransfersFree(t *testing.T) {
	uncapped, cancel := segmentContext(context.Background(), nil)
	defer cancel()
	if _, ok := uncapped.Deadline(); !ok {
		t.Fatal("an uncapped fetch lost its wall-clock budget")
	}
	capped, cancelCapped := segmentContext(context.Background(), rate.NewLimiter(1<<20, 1<<20))
	defer cancelCapped()
	if _, ok := capped.Deadline(); ok {
		t.Fatal("a capped fetch carries a wall-clock budget that can cut off limiter waits")
	}
}

// A live raise must release a parked payment fast, and an expired deadline must never pay.
func TestLimitedConnPaymentAdaptsAndHonorsExpiredDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	limiter := rate.NewLimiter(rate.Limit(1), 1) // one token per second
	if err := limiter.WaitN(context.Background(), 1); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}
	conn := newLimitedConn(context.Background(), client, limiter)
	defer conn.Close()

	go func() { _, _ = server.Write([]byte{'x'}) }()
	buf := make([]byte, 4)
	if n, err := conn.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	// The unpaid byte parks the next read; raising the limit must free it without a stale wait.
	go func() { _, _ = server.Write([]byte{'y'}) }()
	raised := make(chan time.Time, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		limiter.SetLimit(rate.Inf)
		raised <- time.Now()
	}()
	n, err := conn.Read(buf)
	if err != nil || n != 1 || buf[0] != 'y' {
		t.Fatalf("read after the raise = %d %q, %v", n, buf[:n], err)
	}
	if after := time.Since(<-raised); after > 500*time.Millisecond {
		t.Fatalf("payment released %s after the limit increase", after)
	}

	expiredClient, expiredServer := net.Pipe()
	defer expiredServer.Close()
	expiredLimiter := rate.NewLimiter(rate.Limit(1), 1)
	if err := expiredLimiter.WaitN(context.Background(), 1); err != nil {
		t.Fatalf("consume initial burst: %v", err)
	}
	expired := newLimitedConn(context.Background(), expiredClient, expiredLimiter)
	defer expired.Close()

	go func() { _, _ = expiredServer.Write([]byte{'z'}) }()
	if n, err := expired.Read(buf); err != nil || n != 1 {
		t.Fatalf("first read = %d, %v; want the free byte", n, err)
	}
	expired.SetReadDeadline(time.Now().Add(-time.Minute)) // force-expired, as an abandoned drain sends
	start := time.Now()
	_, err = expired.Read(buf)
	if err == nil {
		t.Fatal("a force-expired deadline returned data instead of failing")
	}
	if !timeoutError(err) {
		t.Fatalf("force-expired read = %v, want a deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("force-expired deadline honored after %s", elapsed)
	}
}

// Context cancellation must release a read that is blocked on a silent socket.
func TestLimitedConnCancellationReleasesASilentSocket(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newLimitedConn(ctx, client, rate.NewLimiter(rate.Inf, 1<<20))
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 8))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the read block on the silent socket
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled silent socket read returned data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not release a silent socket read")
	}
}

// timeoutError matches the deadline shapes a stalled or expired read may surface.
func timeoutError(err error) bool {
	var netErr net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}
