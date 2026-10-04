package usenet

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mnightingale/rapidyenc"
)

func TestTestConnectsAndAuthenticates(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)
	primary := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"body@test": yencPart(t, "x.bin", pattern(64), 1, 1, 0, 64)})
	fallback := newFakeNNTP(t, tlsCfg, fmt.Sprintf("[::1]:%d", primary.port()), "user", "secret", nil)

	cfg := Config{Host: "127.0.0.1", Port: primary.port(), Username: "user", Password: "secret", Connections: 2, FallbackHosts: []string{"::1"}}
	if err := Test(context.Background(), cfg); err != nil {
		t.Fatalf("valid credentials: %v", err)
	}
	if fallback.connections() == 0 {
		t.Fatal("the fallback host was never contacted")
	}
	if primary.fetchCount("body@test") != 0 {
		t.Fatal("Test fetched an article body")
	}

	bad := cfg
	bad.Password = "wrong-password"
	err := Test(context.Background(), bad)
	if err == nil {
		t.Fatal("wrong password was accepted")
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Fatalf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error does not name the failing host: %v", err)
	}

	unreachable := Config{Host: "127.0.0.1", Port: 1, Username: "user", Password: "secret"}
	err = Test(context.Background(), unreachable)
	if err == nil {
		t.Fatal("unreachable host was reported as healthy")
	}
	if !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("unreachable host error lacks the fixed message: %v", err)
	}
}

func TestDownloadAssemblesMultipleFiles(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	movie := pattern(300)
	info := pattern(40)
	articles := map[string][]byte{
		"m1@test": yencPart(t, "movie.mkv", movie[:150], 1, 2, 0, 300),
		"m2@test": yencPart(t, "movie.mkv", movie[150:], 2, 2, 150, 300),
		"n1@test": yencPart(t, "movie.nfo", info, 1, 1, 0, 40),
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	nzb := buildNZB(
		nzbSpec{subject: `[1/2] "movie.mkv" yEnc (1/2)`, ids: []string{"m1@test", "m2@test"}},
		nzbSpec{subject: `[1/1] "movie.nfo" yEnc (1/1)`, ids: []string{"n1@test"}},
		nzbSpec{subject: `[1/1] "unavailable.par2" yEnc (1/1)`, ids: []string{"gone@test"}},
	)
	var progress []Progress
	result, err := Download(context.Background(), nzb, t.TempDir(), testConfig(server, 3), func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.MissingSegments != 1 || len(result.Files) != 2 {
		t.Fatalf("got result %+v, want the two available files and one missing segment", result)
	}
	if filepath.Base(result.Files[0]) != "movie.mkv" || filepath.Base(result.Files[1]) != "movie.nfo" {
		t.Fatalf("unexpected file order: %v", result.Files)
	}
	first, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read movie: %v", err)
	}
	if !bytes.Equal(first, movie) {
		t.Fatal("movie.mkv differs from the posted file")
	}
	second, err := os.ReadFile(result.Files[1])
	if err != nil {
		t.Fatalf("read nfo: %v", err)
	}
	if !bytes.Equal(second, info) {
		t.Fatal("movie.nfo differs from the posted file")
	}
	last := progress[len(progress)-1]
	want := Progress{DownloadedBytes: 340, TotalBytes: 4000, CompletedSegments: 3, TotalSegments: 4, MissingSegments: 1}
	if last != want {
		t.Fatalf("final progress %+v, want %+v", last, want)
	}
}

func TestDownloadAssemblesPartsArrivingOutOfOrder(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(1000)
	bounds := []int{0, 300, 700, 1000}
	ids := []string{"part1@test", "part2@test", "part3@test"}
	articles := map[string][]byte{}
	for i, id := range ids {
		articles[id] = yencPart(t, "movie.mkv", content[bounds[i]:bounds[i+1]], i+1, 3, int64(bounds[i]), int64(len(content)))
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)
	server.delays = map[string]time.Duration{ids[0]: 300 * time.Millisecond}

	dir := t.TempDir()
	var progress []Progress
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/3] "movie.mkv" yEnc (1/3)`, ids: ids}), dir, testConfig(server, 3), func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one complete file", result)
	}
	if base := filepath.Base(result.Files[0]); base != "movie.mkv" {
		t.Fatalf("assembled %q, want movie.mkv", base)
	}
	if filepath.Dir(result.Files[0]) != dir {
		t.Fatalf("assembled outside the download dir: %s", result.Files[0])
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("assembled bytes differ from the posted file")
	}
	served := server.servedOrder()
	if len(served) != 3 || served[0] == ids[0] {
		t.Fatalf("part 1 was not delayed last: served %v", served)
	}

	last := progress[len(progress)-1]
	want := Progress{DownloadedBytes: 1000, TotalBytes: 3000, CompletedSegments: 3, TotalSegments: 3, MissingSegments: 0}
	if last != want {
		t.Fatalf("final progress %+v, want %+v", last, want)
	}
}

func TestDownloadAssemblesObfuscatedPartNamesFromSubject(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(900)
	bounds := []int{0, 300, 600, 900}
	ids := []string{"ob1@test", "ob2@test", "ob3@test"}
	articles := map[string][]byte{}
	for i, id := range ids {
		name := fmt.Sprintf("%032x", uint64(i+1)*0x9e3779b97f4a7c15)
		articles[id] = yencPart(t, name, content[bounds[i]:bounds[i+1]], i+1, 3, int64(bounds[i]), int64(len(content)))
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	dir := t.TempDir()
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/3] "movie.mkv" yEnc (1/3)`, ids: ids}), dir, testConfig(server, 3), nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one complete file", result)
	}
	if got := filepath.Base(result.Files[0]); got != "movie.mkv" {
		t.Fatalf("assembled %q, want the NZB subject name movie.mkv", got)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, content) || crc32.ChecksumIEEE(got) != crc32.ChecksumIEEE(content) {
		t.Fatal("assembled file differs from the posted content or CRC")
	}
	for i, id := range ids {
		if count := server.fetchCount(id); count != 1 {
			t.Fatalf("part %d fetched %d times, want 1", i+1, count)
		}
	}
}

func TestDownloadUnstuffsDotStuffedBody(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	payload := dotLeadingPayload(384)
	article := plainYencPart(payload, "plain.bin")
	if !bytes.Contains(article, []byte("\r\n=ypart begin=1 end=384\r\n.")) {
		t.Fatal("test article does not start a body line with '.'")
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"dots@test": article})

	dir := t.TempDir()
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "plain.bin" yEnc`, ids: []string{"dots@test"}}), dir, testConfig(server, 1), nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one complete file", result)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("dot-stuffed body did not round-trip")
	}
}

func TestDownloadRetriesAndReportsCRCFailure(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(600)
	bounds := []int{0, 200, 400, 600}
	ids := []string{"crc1@test", "crc2@test", "crc3@test"}
	articles := map[string][]byte{}
	for i, id := range ids {
		part := yencPart(t, "broken.mkv", content[bounds[i]:bounds[i+1]], i+1, 3, int64(bounds[i]), int64(len(content)))
		if i == 1 {
			part = breakCRC(t, part)
		}
		articles[id] = part
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	dir := t.TempDir()
	var progress []Progress
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/3] "broken.mkv" yEnc (1/3)`, ids: ids}), dir, testConfig(server, 2), func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("a corrupt part should not fail the job: %v", err)
	}
	if result.MissingSegments != 1 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one file with one missing segment", result)
	}
	if count := server.fetchCount(ids[1]); count != maxAttempts {
		t.Fatalf("corrupt part fetched %d times, want %d", count, maxAttempts)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got[:200], content[:200]) || !bytes.Equal(got[400:], content[400:]) {
		t.Fatal("valid parts were not assembled")
	}
	if !bytes.Equal(got[200:400], make([]byte, 200)) {
		t.Fatal("missing part is not a sparse zero gap")
	}
	last := progress[len(progress)-1]
	if last.MissingSegments != 1 || last.CompletedSegments != 2 {
		t.Fatalf("final progress %+v, want 2 completed and 1 missing", last)
	}
}

func TestDownloadUsesFallbackHostForMissingArticle(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(600)
	bounds := []int{0, 200, 400, 600}
	ids := []string{"fb1@test", "fb2@test", "fb3@test"}
	primaryArticles := map[string][]byte{}
	fallbackArticles := map[string][]byte{}
	for i, id := range ids {
		part := yencPart(t, "fallback.mkv", content[bounds[i]:bounds[i+1]], i+1, 3, int64(bounds[i]), int64(len(content)))
		fallbackArticles[id] = part
		if i != 1 {
			primaryArticles[id] = part
		}
	}
	primary := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", primaryArticles)
	fallback := newFakeNNTP(t, tlsCfg, fmt.Sprintf("[::1]:%d", primary.port()), "user", "secret", fallbackArticles)

	nzb := buildNZB(nzbSpec{subject: `[1/3] "fallback.mkv" yEnc (1/3)`, ids: ids})
	dir := t.TempDir()
	cfg := testConfig(primary, 3)
	cfg.FallbackHosts = []string{"::1"}
	result, err := Download(context.Background(), nzb, dir, cfg, nil)
	if err != nil {
		t.Fatalf("download with fallback: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want the fallback to complete the file", result)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("fallback assembly differs from the posted file")
	}
	if fallback.fetchCount(ids[1]) != 1 {
		t.Fatal("the fallback host did not serve the missing article")
	}

	cfg.FallbackHosts = nil
	dirNoFallback := t.TempDir()
	result, err = Download(context.Background(), nzb, dirNoFallback, cfg, nil)
	if err != nil {
		t.Fatalf("download without fallback should report damage, not fail: %v", err)
	}
	if result.MissingSegments != 1 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one file with one missing segment", result)
	}
	got, err = os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got[:200], content[:200]) || !bytes.Equal(got[400:], content[400:]) {
		t.Fatal("partial file lost its valid parts")
	}
}

func TestDownloadTriesEveryHostInOrderOnBODYMiss(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(400)
	served := "ordered@test"
	gone := "gone@test"
	article := yencPart(t, "ordered.mkv", content, 1, 2, 0, 800)

	primary := newFakeNNTPBehavior("user", "secret", nil)
	first := newFakeNNTPBehavior("user", "secret", nil)
	final := newFakeNNTPBehavior("user", "secret", map[string][]byte{served: article})
	for _, host := range []*fakeNNTP{primary, first} {
		host.statAvailable = map[string]bool{served: true, gone: true}
	}

	router := newSNIRouter(t, tlsCfg, "127.0.0.1:0")
	router.add("", primary)
	router.add("localhost", first)
	v6 := newSNIRouter(t, tlsCfg, fmt.Sprintf("[::1]:%d", router.port()))
	v6.add("", final)
	v6.add("localhost", first)

	cfg := Config{Host: "127.0.0.1", Port: router.port(), Username: "user", Password: "secret", Connections: 2, FallbackHosts: []string{"localhost", "::1"}}
	nzb := buildNZB(nzbSpec{subject: `[1/2] "ordered.mkv" yEnc (1/2)`, ids: []string{served, gone}})
	var progress []Progress
	result, err := Download(context.Background(), nzb, t.TempDir(), cfg, func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if result.MissingSegments != 1 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want one file with only the second segment missing", result)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got[:400], content) {
		t.Fatal("the final host's article was not assembled")
	}
	for name, host := range map[string]*fakeNNTP{"primary": primary, "first fallback": first, "final fallback": final} {
		if host.fetchCount(served) != 1 || host.fetchCount(gone) != 1 {
			t.Fatalf("%s was not asked with BODY for both segments (served=%d gone=%d)", name, host.fetchCount(served), host.fetchCount(gone))
		}
	}
	last := progress[len(progress)-1]
	if last.TotalSegments != 2 || last.CompletedSegments != 1 || last.MissingSegments != 1 {
		t.Fatalf("final progress %+v, want 1 of 2 complete", last)
	}
}

func TestDownloadResumesAfterCancellation(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(600)
	ids := []string{"r1@test", "r2@test", "r3@test", "r4@test", "r5@test", "r6@test"}
	articles := map[string][]byte{}
	for i, id := range ids {
		articles[id] = yencPart(t, "slow.bin", content[i*100:(i+1)*100], i+1, 6, int64(i*100), int64(len(content)))
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)
	server.delay = 200 * time.Millisecond

	nzb := buildNZB(nzbSpec{subject: `[1/6] "slow.bin" yEnc (1/6)`, ids: ids})
	dir := t.TempDir()
	cfg := testConfig(server, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Download(ctx, nzb, dir, cfg, func(p Progress) {
		if p.CompletedSegments >= 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled download returned %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "slow.bin")); !os.IsNotExist(err) {
		t.Fatal("a cancelled download assembled an output file")
	}

	cached := cachedSegments(t, dir)
	if len(cached) < 2 || len(cached) >= len(ids) {
		t.Fatalf("cancelled job cached %d of %d segments", len(cached), len(ids))
	}
	before := map[string]int{}
	for id := range cached {
		before[id] = server.fetchCount(id)
	}

	server.delay = 0
	var progress []Progress
	result, err := Download(context.Background(), nzb, dir, cfg, func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("resumed download: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want a complete resumed file", result)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("resumed assembly differs from the posted file")
	}
	for id := range cached {
		if server.fetchCount(id) != before[id] {
			t.Fatalf("cached segment %s was fetched again on resume", id)
		}
	}
	last := progress[len(progress)-1]
	if last.CompletedSegments != len(ids) {
		t.Fatalf("resumed progress %+v, want all segments complete", last)
	}
	if last.DownloadedBytes < int64(len(cached)*100) {
		t.Fatalf("resumed bytes %d do not include the cached parts", last.DownloadedBytes)
	}
}

func TestDownloadRefetchesCorruptCache(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)

	content := pattern(200)
	ids := []string{"k1@test", "k2@test"}
	articles := map[string][]byte{}
	for i, id := range ids {
		articles[id] = yencPart(t, "cached.bin", content[i*100:(i+1)*100], i+1, 2, int64(i*100), int64(len(content)))
	}
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", articles)

	nzb := buildNZB(nzbSpec{subject: `[1/2] "cached.bin" yEnc (1/2)`, ids: ids})
	dir := t.TempDir()
	cfg := testConfig(server, 2)
	if _, err := Download(context.Background(), nzb, dir, cfg, nil); err != nil {
		t.Fatalf("first download: %v", err)
	}
	before := server.fetchCount(ids[1])

	path := cachePath(filepath.Join(dir, cacheDirName), ids[0])
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("corrupt cache: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "cached.bin")); err != nil {
		t.Fatalf("remove output: %v", err)
	}

	result, err := Download(context.Background(), nzb, dir, cfg, nil)
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if result.MissingSegments != 0 || len(result.Files) != 1 {
		t.Fatalf("got result %+v, want a complete file", result)
	}
	if got := server.fetchCount(ids[0]); got != 2 {
		t.Fatalf("corrupt cached part fetched %d times, want 2", got)
	}
	if got := server.fetchCount(ids[1]); got != before {
		t.Fatal("intact cached part was fetched again")
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("re-assembled bytes differ from the posted file")
	}
}

func TestDownloadRejectsUnsafeMetadata(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", nil)
	dir := t.TempDir()
	cfg := testConfig(server, 1)

	crlf := []byte(`<?xml version="1.0"?><nzb><file subject="x"><segments><segment number="1">ok@test&#13;&#10;injected@test</segment></segments></file></nzb>`)
	if _, err := Download(context.Background(), crlf, dir, cfg, nil); err == nil {
		t.Fatal("message id with CRLF was accepted")
	}

	duplicate := buildNZB(
		nzbSpec{subject: `[1/1] "same.bin" yEnc`, ids: []string{"one@test"}},
		nzbSpec{subject: `[1/1] "same.bin" yEnc`, ids: []string{"two@test"}},
	)
	if _, err := Download(context.Background(), duplicate, dir, cfg, nil); err == nil {
		t.Fatal("duplicate output names were accepted")
	}

	// The NZB subject owns the output name, so the hostile yEnc name is only a fallback.
	payload := pattern(100)
	article := yencPart(t, "../../evil.bin", payload, 1, 1, 0, 100)
	traversal := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"evil@test": article})
	dir = t.TempDir()
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "safe.bin" yEnc`, ids: []string{"evil@test"}}), dir, testConfig(traversal, 1), nil)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if len(result.Files) != 1 || filepath.Base(result.Files[0]) != "safe.bin" || filepath.Dir(result.Files[0]) != dir {
		t.Fatalf("subject name should be authoritative for assembly: %+v", result.Files)
	}
	got, err := os.ReadFile(result.Files[0])
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("subject-named output does not match the posted payload")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.bin")); err == nil {
		t.Fatal("output escaped the download directory")
	}
}

func TestDownloadReportsAuthenticationFailure(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)
	article := yencPart(t, "auth.bin", pattern(100), 1, 1, 0, 100)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"auth@test": article})

	cfg := testConfig(server, 1)
	cfg.Password = "wrong-password"
	dir := t.TempDir()
	result, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "auth.bin" yEnc`, ids: []string{"auth@test"}}), dir, cfg, nil)
	if err == nil {
		t.Fatal("authentication failure was reported as success")
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Fatalf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), server.host()) || !strings.Contains(strings.ToLower(err.Error()), "auth") {
		t.Fatalf("error is not actionable: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("failed job returned files: %+v", result.Files)
	}
	if entries, readErr := os.ReadDir(filepath.Join(dir, cacheDirName)); readErr == nil && len(entries) > 0 {
		t.Fatalf("failed job cached data: %d entries", len(entries))
	}
}

func TestProviderErrorsHideHostileServerText(t *testing.T) {
	tlsCfg, roots := testTLSMaterial(t)
	withTestRoots(t, roots)
	article := yencPart(t, "leak.bin", pattern(100), 1, 1, 0, 100)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "usenetuser", "s3cr3t-pw", map[string][]byte{"leak@test": article})
	server.authMessage = `login failed for user usenetuser with password s3cr3t-pw`

	cfg := testConfig(server, 1)
	cfg.Username = "usenetuser"
	cfg.Password = "s3cr3t-pw-wrong"
	hostile := []string{"s3cr3t-pw", "usenetuser", "login failed"}

	if err := Test(context.Background(), cfg); err == nil {
		t.Fatal("Test accepted the hostile rejection")
	} else if leaked(err, hostile) {
		t.Fatalf("Test error leaks server or credential text: %v", err)
	} else if !causeMentions(err, "login failed") {
		t.Fatalf("Test error dropped its cause chain: %v", err)
	} else if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("Test error lacks the fixed message: %v", err)
	}

	_, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "leak.bin" yEnc`, ids: []string{"leak@test"}}), t.TempDir(), cfg, nil)
	if err == nil {
		t.Fatal("Download reported success for the hostile rejection")
	}
	if leaked(err, hostile) {
		t.Fatalf("Download error leaks server or credential text: %v", err)
	}
	if !causeMentions(err, "login failed") {
		t.Fatalf("Download error dropped its cause chain: %v", err)
	}
	if !strings.Contains(err.Error(), "authentication failed (check the username and password)") {
		t.Fatalf("Download error lacks the fixed message: %v", err)
	}
	if !strings.Contains(err.Error(), server.host()) || !strings.Contains(strings.ToLower(err.Error()), "auth") {
		t.Fatalf("Download error is not actionable: %v", err)
	}
	if !errors.Is(sanitizeProviderError("", context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("sanitized provider errors must keep errors.Is working")
	}
}

func leaked(err error, needles []string) bool {
	text := err.Error()
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// causeMentions proves errors.Is-style traversal still reaches the upstream cause.
func causeMentions(err error, substr string) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}

func TestDownloadRejectsHostnameMismatch(t *testing.T) {
	tlsCfg, roots := testTLSMaterialFor(t, []string{"example.invalid"}, nil)
	withTestRoots(t, roots)
	article := yencPart(t, "tls.bin", pattern(100), 1, 1, 0, 100)
	server := newFakeNNTP(t, tlsCfg, "127.0.0.1:0", "user", "secret", map[string][]byte{"tls@test": article})

	_, err := Download(context.Background(), buildNZB(nzbSpec{subject: `[1/1] "tls.bin" yEnc`, ids: []string{"tls@test"}}), t.TempDir(), testConfig(server, 1), nil)
	if err == nil {
		t.Fatal("certificate for the wrong hostname was accepted")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("unexpected TLS error: %v", err)
	}
}

func testConfig(server *fakeNNTP, connections int) Config {
	return Config{Host: server.host(), Port: server.port(), Username: "user", Password: "secret", Connections: connections}
}

func testTLSMaterial(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	return testTLSMaterialFor(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")})
}

func testTLSMaterialFor(t *testing.T, dnsNames []string, ips []net.IP) (*tls.Config, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "usenet test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "usenet test server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}},
		MinVersion:   tls.VersionTLS12,
	}, roots
}

func withTestRoots(t *testing.T, roots *x509.CertPool) {
	t.Helper()
	previous := tlsRootCAs
	tlsRootCAs = roots
	t.Cleanup(func() { tlsRootCAs = previous })
}

type fakeNNTP struct {
	user        string
	pass        string
	delay       time.Duration
	delays      map[string]time.Duration
	authMessage string
	mu          sync.Mutex
	articles    map[string][]byte
	// statAvailable makes STAT answer 223 for articles this host does not actually hold.
	statAvailable map[string]bool
	fetches       map[string]int
	served        []string
	connCount     int
	ln            net.Listener
	wg            sync.WaitGroup
}

func newFakeNNTP(t *testing.T, tlsCfg *tls.Config, addr, user, pass string, articles map[string][]byte) *fakeNNTP {
	t.Helper()
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	server := newFakeNNTPBehavior(user, pass, articles)
	server.ln = ln
	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				server.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		server.wg.Wait()
	})
	return server
}

func newFakeNNTPBehavior(user, pass string, articles map[string][]byte) *fakeNNTP {
	if articles == nil {
		articles = map[string][]byte{}
	}
	return &fakeNNTP{user: user, pass: pass, authMessage: "authentication rejected", articles: articles, fetches: map[string]int{}}
}

// sniRouter runs several fake hosts behind one address, dispatching on the TLS server name.
type sniRouter struct {
	ln          net.Listener
	tlsCfg      *tls.Config
	mu          sync.Mutex
	routes      map[string]*fakeNNTP
	defaultHost *fakeNNTP
	wg          sync.WaitGroup
}

func newSNIRouter(t *testing.T, tlsCfg *tls.Config, addr string) *sniRouter {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	router := &sniRouter{ln: ln, tlsCfg: tlsCfg, routes: map[string]*fakeNNTP{}}
	router.wg.Add(1)
	go func() {
		defer router.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			router.wg.Add(1)
			go func() {
				defer router.wg.Done()
				router.route(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		router.wg.Wait()
	})
	return router
}

func (r *sniRouter) add(serverName string, host *fakeNNTP) {
	r.mu.Lock()
	if serverName == "" {
		r.defaultHost = host
	} else {
		r.routes[serverName] = host
	}
	r.mu.Unlock()
}

func (r *sniRouter) port() int {
	return r.ln.Addr().(*net.TCPAddr).Port
}

func (r *sniRouter) route(raw net.Conn) {
	defer raw.Close()
	conn := tls.Server(raw, r.tlsCfg)
	if err := conn.Handshake(); err != nil {
		return
	}
	// Clients dialing a literal address send no SNI, so those fall to the listener's default host.
	r.mu.Lock()
	host := r.routes[conn.ConnectionState().ServerName]
	if host == nil {
		host = r.defaultHost
	}
	r.mu.Unlock()
	if host == nil {
		return
	}
	host.serve(conn)
}

func (s *fakeNNTP) host() string {
	host, _, _ := net.SplitHostPort(s.ln.Addr().String())
	return host
}

func (s *fakeNNTP) port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *fakeNNTP) fetchCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches[id]
}

func (s *fakeNNTP) servedOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.served...)
}

func (s *fakeNNTP) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connCount
}

func (s *fakeNNTP) serve(conn net.Conn) {
	defer conn.Close()
	s.mu.Lock()
	s.connCount++
	s.mu.Unlock()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	send := func(format string, args ...any) {
		fmt.Fprintf(writer, format, args...)
		_ = writer.Flush()
	}
	send("200 fake NNTP ready\r\n")
	authed := s.user == ""
	userSeen := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "AUTHINFO USER"):
			userSeen = s.user != "" && strings.TrimSpace(cmd[len("AUTHINFO USER"):]) == s.user
			if userSeen {
				send("381 password required\r\n")
			} else {
				send("481 %s\r\n", s.authMessage)
			}
		case strings.HasPrefix(upper, "AUTHINFO PASS"):
			if userSeen && strings.TrimSpace(cmd[len("AUTHINFO PASS"):]) == s.pass {
				authed = true
				send("281 authentication accepted\r\n")
			} else {
				send("481 %s\r\n", s.authMessage)
			}
		case !authed:
			send("480 authentication required\r\n")
		case upper == "DATE":
			send("111 20261003000000\r\n")
		case strings.HasPrefix(upper, "STAT "):
			id := messageIDArg(cmd)
			s.mu.Lock()
			_, stored := s.articles[id]
			lie := s.statAvailable[id]
			s.mu.Unlock()
			if stored || lie {
				send("223 0 <%s>\r\n", id)
			} else {
				send("430 no such article\r\n")
			}
		case strings.HasPrefix(upper, "BODY "):
			id := messageIDArg(cmd)
			s.mu.Lock()
			s.fetches[id]++
			delay := s.delay
			if d, ok := s.delays[id]; ok {
				delay = d
			}
			s.mu.Unlock()
			if delay > 0 {
				time.Sleep(delay)
			}
			article, ok := s.article(id)
			if !ok {
				send("430 no such article\r\n")
				continue
			}
			send("222 0 <%s>\r\n", id)
			writeDotStuffed(writer, article)
			send(".\r\n")
			s.mu.Lock()
			s.served = append(s.served, id)
			s.mu.Unlock()
		case upper == "QUIT":
			send("221 bye\r\n")
			return
		default:
			send("500 command not recognized\r\n")
		}
	}
}

func (s *fakeNNTP) article(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	article, ok := s.articles[id]
	return article, ok
}

func messageIDArg(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	return strings.Trim(fields[1], "<>")
}

func writeDotStuffed(w io.Writer, article []byte) {
	for len(article) > 0 {
		line := article
		if i := bytes.Index(article, []byte("\r\n")); i >= 0 {
			line, article = article[:i], article[i+2:]
		} else {
			article = nil
		}
		if len(line) > 0 && line[0] == '.' {
			_, _ = w.Write([]byte{'.'})
		}
		_, _ = w.Write(line)
		_, _ = w.Write([]byte("\r\n"))
	}
}

type nzbSpec struct {
	subject string
	ids     []string
}

func buildNZB(specs ...nzbSpec) []byte {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	for _, spec := range specs {
		subject := spec.subject
		if subject == "" {
			subject = fmt.Sprintf(`[1/%d] "file.bin" yEnc`, len(spec.ids))
		}
		fmt.Fprintf(&b, `<file poster="tests" date="1700000000" subject="%s">`, escaped(subject))
		b.WriteString(`<groups><group>alt.test</group></groups><segments>`)
		for i, id := range spec.ids {
			fmt.Fprintf(&b, `<segment bytes="1000" number="%d">%s</segment>`, i+1, escaped(id))
		}
		b.WriteString(`</segments></file>`)
	}
	b.WriteString(`</nzb>`)
	return []byte(b.String())
}

func escaped(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func yencPart(t *testing.T, name string, payload []byte, part, total int, offset, fileSize int64) []byte {
	t.Helper()
	var body bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&body, rapidyenc.Meta{
		FileName:   name,
		FileSize:   fileSize,
		PartNumber: int64(part),
		TotalParts: int64(total),
		Offset:     offset,
		PartSize:   int64(len(payload)),
	})
	if err != nil {
		t.Fatalf("yEnc encoder: %v", err)
	}
	if _, err := enc.Write(payload); err != nil {
		t.Fatalf("yEnc write: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("yEnc close: %v", err)
	}
	return withHeaders(body.Bytes())
}

// plainYencPart writes yEnc without escaping a leading '.', forcing the transport to dot-stuff.
func plainYencPart(payload []byte, name string) []byte {
	var b bytes.Buffer
	size := len(payload)
	fmt.Fprintf(&b, "=ybegin part=1 total=1 line=128 size=%d name=%s\r\n", size, name)
	fmt.Fprintf(&b, "=ypart begin=1 end=%d\r\n", size)
	for i, c := range payload {
		if i > 0 && i%128 == 0 {
			b.WriteString("\r\n")
		}
		switch c {
		case 0, '\r', '\n', '=':
			b.WriteByte('=')
			b.WriteByte(c + 64)
		default:
			b.WriteByte(c + 42)
		}
	}
	fmt.Fprintf(&b, "\r\n=yend size=%d part=1 pcrc32=%08x\r\n", size, crc32.ChecksumIEEE(payload))
	return withHeaders(b.Bytes())
}

func withHeaders(body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("Path: fake\r\nSubject: test part\r\nMessage-ID: <fake@test>\r\n\r\n")
	b.Write(body)
	return b.Bytes()
}

// breakCRC flips a payload byte without updating the announced pcrc32.
func breakCRC(t *testing.T, article []byte) []byte {
	t.Helper()
	end := bytes.Index(article, []byte("=yend"))
	if end < 0 {
		t.Fatal("article has no =yend")
	}
	lineEnd := bytes.LastIndex(article[:end], []byte("\r\n"))
	if lineEnd < 0 {
		t.Fatal("article has no data line before =yend")
	}
	lineStart := bytes.LastIndex(article[:lineEnd], []byte("\r\n")) + 2
	if lineStart >= lineEnd {
		t.Fatal("article has no data line before =yend")
	}
	out := append([]byte(nil), article...)
	out[lineStart] = 'Z'
	return out
}

func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i%251 + 1)
	}
	return p
}

// dotLeadingPayload places a byte at every 128-char line start so encoded lines begin with '.'.
func dotLeadingPayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		if i%128 == 0 {
			p[i] = 0x04
		} else {
			p[i] = 'a'
		}
	}
	return p
}

// cachedSegments returns the valid cache entries keyed by message id.
func cachedSegments(t *testing.T, dir string) map[string]segment {
	t.Helper()
	partsDir := filepath.Join(dir, cacheDirName)
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	cached := map[string]segment{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".part") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(partsDir, entry.Name()))
		if err != nil {
			continue
		}
		s, err := decodeSegment(raw)
		if err != nil {
			continue
		}
		if got := filepath.Base(cachePath(partsDir, s.MessageID)); got != entry.Name() {
			t.Fatalf("cache file %s does not hash its message id", entry.Name())
		}
		cached[s.MessageID] = s
	}
	return cached
}
