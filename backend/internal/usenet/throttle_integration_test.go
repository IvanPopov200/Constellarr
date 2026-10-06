package usenet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// throttleFixture posts one file whose parts carry the given payloads, with byte sizes the NZB states honestly.
func throttleFixture(t *testing.T, name string, partBytes, count int) (map[string][]byte, []byte, []byte) {
	t.Helper()
	content := pattern(partBytes * count)
	articles := map[string][]byte{}
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	fmt.Fprintf(&b, `<file poster="tests" date="1700000000" subject="%s">`, escaped(fmt.Sprintf(`[1/%d] "%s" yEnc`, count, name)))
	b.WriteString(`<groups><group>alt.test</group></groups><segments>`)
	for i := range count {
		id := fmt.Sprintf("throttle-%d@test", i+1)
		part := content[i*partBytes : (i+1)*partBytes]
		articles[id] = yencPart(t, name, part, i+1, count, int64(i*partBytes), int64(len(content)))
		fmt.Fprintf(&b, `<segment bytes="%d" number="%d">%s</segment>`, len(part), i+1, escaped(id))
	}
	b.WriteString(`</segments></file></nzb>`)
	return articles, []byte(b.String()), content
}

func assertAssembled(t *testing.T, result Result, content []byte) {
	t.Helper()
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want a complete file", result)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("assembled bytes differ from the posted file: %v", err)
	}
}

// The cap is shared by every connection, so parallel workers cannot multiply it.
func TestDownloadThrottlesAggregateRate(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, content := throttleFixture(t, "throttled.mkv", 100<<10, 4)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	cfg := testConfig(server, 2)
	cfg.Limiter = rate.NewLimiter(rate.Limit(200<<10), 64<<10)

	start := time.Now()
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("throttled download: %v", err)
	}
	assertAssembled(t, result, content)
	// 400 KiB at 200 KiB/s cannot finish in under ~1.7s even with the burst.
	if elapsed < 1400*time.Millisecond {
		t.Fatalf("capped download finished in %s, faster than the shared cap allows", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("capped download took %s", elapsed)
	}
}

// Raising the cap mid-transfer must speed up work already in flight.
func TestDownloadLimitChangeAppliesDuringTransfer(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, content := throttleFixture(t, "retuned.mkv", 100<<10, 6)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	limiter := rate.NewLimiter(rate.Limit(100<<10), 64<<10)
	cfg := testConfig(server, 2)
	cfg.Limiter = limiter
	go func() {
		time.Sleep(1200 * time.Millisecond)
		limiter.SetLimit(rate.Limit(1 << 20))
	}()

	start := time.Now()
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	assertAssembled(t, result, content)
	if elapsed < time.Second {
		t.Fatalf("download finished in %s before the raised cap could matter", elapsed)
	}
	// 600 KiB at the original 100 KiB/s would need ~6s; the raised cap must beat that.
	if elapsed > 4*time.Second {
		t.Fatalf("download took %s; the limit change was not applied mid-transfer", elapsed)
	}
}

// Lowering the cap mid-transfer must slow the job down, not fail it or cut off a segment.
func TestDownloadCapReductionMidTransferCompletes(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, content := throttleFixture(t, "reduced.mkv", 50<<10, 6)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	limiter := rate.NewLimiter(rate.Limit(400<<10), 32<<10)
	cfg := testConfig(server, 2)
	cfg.Limiter = limiter
	go func() {
		time.Sleep(400 * time.Millisecond)
		limiter.SetLimit(rate.Limit(64 << 10))
	}()

	start := time.Now()
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	assertAssembled(t, result, content)
	if elapsed < 1800*time.Millisecond {
		t.Fatalf("download finished in %s, so the reduced cap never applied", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("download took %s after the reduction", elapsed)
	}
}

// A low cap must not look like a stalled provider or an expired segment deadline.
func TestDownloadCompletesUnderLowLimit(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, content := throttleFixture(t, "slow.mkv", 80<<10, 3)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	cfg := testConfig(server, 2)
	cfg.Limiter = rate.NewLimiter(rate.Limit(60<<10), 16<<10)
	start := time.Now()
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("download at a low cap: %v", err)
	}
	assertAssembled(t, result, content)
	if elapsed < 2*time.Second {
		t.Fatalf("low-cap download finished in %s, faster than the cap allows", elapsed)
	}
}

// Cancelling a heavily throttled download must return promptly and keep the cached parts.
func TestDownloadCancellationStopsPromptlyUnderLimit(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, _ := throttleFixture(t, "cancel.mkv", 25<<10, 20)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	cfg := testConfig(server, 2)
	cfg.Limiter = rate.NewLimiter(rate.Limit(50<<10), 16<<10)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(4 * time.Second)
		cancel()
	}()

	start := time.Now()
	_, err := Download(ctx, nzb, dir, cfg, nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capped download returned %v, want context.Canceled", err)
	}
	// Draining the remaining parts at the cap would take far longer than two more seconds.
	if elapsed > 8*time.Second {
		t.Fatalf("cancellation took %s under a 50 KiB/s cap", elapsed)
	}
	if _, err := os.Stat(filepath.Join(dir, "cancel.mkv")); !os.IsNotExist(err) {
		t.Fatal("a cancelled download assembled an output file")
	}
	if cached := cachedSegments(t, dir); len(cached) == 0 {
		t.Fatal("a cancelled capped download kept no cached parts")
	}
}

// The factory path must verify TLS exactly like the pool's own dialer.
func TestDownloadRejectsHostnameMismatchWithLimiter(t *testing.T) {
	tlsCfg, roots := testTLSMaterialFor(t, []string{"example.invalid"}, nil)
	withTestRoots(t, roots)
	article := yencPart(t, "tls.bin", pattern(100), 1, 1, 0, 100)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"tls@test": article})

	cfg := testConfig(server, 1)
	cfg.Limiter = rate.NewLimiter(rate.Limit(1<<20), 64<<10)
	_, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "tls.bin" yEnc`, ids: []string{"tls@test"}}), t.TempDir(), cfg, nil)
	if err == nil {
		t.Fatal("certificate for the wrong hostname was accepted through the limiter factory")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("unexpected TLS error: %v", err)
	}
}

// A very low cap on several connections must finish the payload in one run and follow live changes.
func TestDownloadVeryLowCapCompletesAndTracksLiveCap(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	articles, nzb, content := throttleFixture(t, "tiny.mkv", 1<<10, 8)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	cfg := testConfig(server, 2)
	limiter := rate.NewLimiter(rate.Limit(1<<10), 1<<10) // 1 KiB/s with a 1 KiB burst
	cfg.Limiter = limiter
	go func() {
		time.Sleep(400 * time.Millisecond)
		limiter.SetLimit(rate.Limit(512)) // reduce the cap while payments are parked
		time.Sleep(500 * time.Millisecond)
		limiter.SetLimit(rate.Limit(64 << 10)) // raise it again before the payload is paid off
	}()

	start := time.Now()
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("very low cap download: %v", err)
	}
	assertAssembled(t, result, content)
	// 8 KiB cannot be paid for before the raise at 900 ms, so the low cap must have been in effect.
	if elapsed < 900*time.Millisecond {
		t.Fatalf("download finished in %s, so the very low cap never applied", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("download took %s under a very low cap", elapsed)
	}
}

// A paused policy must not make a connectivity check hang behind the transfer cap.
func TestTestIgnoresTransferLimiter(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", nil)

	cfg := testConfig(server, 1)
	cfg.Limiter = rate.NewLimiter(0, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Test(ctx, cfg); err != nil {
		t.Fatalf("Test with a paused limiter: %v", err)
	}
}
