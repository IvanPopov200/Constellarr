package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	loginWindow      = 15 * time.Minute
	loginMaxFailures = 8
	limiterMaxIPs    = 1024
	limiterMaxNames  = 4096
	maxKeyRunes      = 80
)

type limiterEntry struct {
	failures  int
	windowEnd time.Time
}

// loginLimiter throttles credential guessing per account and per client address
// with bounded maps, so neither key type can grow without limit.
type loginLimiter struct {
	mu    sync.Mutex
	ips   map[string]limiterEntry
	names map[string]limiterEntry
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{ips: make(map[string]limiterEntry), names: make(map[string]limiterEntry)}
}

func (l *loginLimiter) blocked(name, ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	wait := blockWait(l.ips[ip])
	if accountWait := blockWait(l.names[nameKey(name)]); accountWait > wait {
		wait = accountWait
	}
	return wait
}

func (l *loginLimiter) fail(name, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.record(l.ips, ip, now, limiterMaxIPs)
	l.record(l.names, nameKey(name), now, limiterMaxNames)
}

func (l *loginLimiter) reset(name, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
	delete(l.names, nameKey(name))
}

func (l *loginLimiter) purge() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	dropExpired(l.ips, now)
	dropExpired(l.names, now)
}

func (l *loginLimiter) record(entries map[string]limiterEntry, key string, now time.Time, limit int) {
	if key == "" {
		return
	}
	entry, known := entries[key]
	if !known || now.After(entry.windowEnd) {
		entry = limiterEntry{windowEnd: now.Add(loginWindow)}
	}
	entry.failures++
	if !known {
		makeRoom(entries, limit, now)
	}
	entries[key] = entry
}

// makeRoom keeps existing blocks: expired entries are dropped first, then the
// newest one, so rotating usernames or addresses cannot erase active limits.
func makeRoom(entries map[string]limiterEntry, limit int, now time.Time) {
	if len(entries) < limit {
		return
	}
	dropExpired(entries, now)
	if len(entries) < limit {
		return
	}
	newestKey := ""
	var newestEnd time.Time
	for key, entry := range entries {
		if newestKey == "" || entry.windowEnd.After(newestEnd) {
			newestKey, newestEnd = key, entry.windowEnd
		}
	}
	delete(entries, newestKey)
}

func dropExpired(entries map[string]limiterEntry, now time.Time) {
	for key, entry := range entries {
		if now.After(entry.windowEnd) {
			delete(entries, key)
		}
	}
}

func blockWait(entry limiterEntry) time.Duration {
	if entry.failures < loginMaxFailures {
		return 0
	}
	if remaining := time.Until(entry.windowEnd); remaining > 0 {
		return remaining
	}
	return 0
}

func nameKey(name string) string {
	return strings.ToLower(truncateRunes(strings.TrimSpace(name), maxKeyRunes))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return truncateRunes(r.RemoteAddr, maxKeyRunes)
	}
	return host
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
