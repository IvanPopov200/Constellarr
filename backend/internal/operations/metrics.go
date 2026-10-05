package operations

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Bounded label sets: metric labels never carry titles, paths, hosts or user input.
var downloadStates = []string{"queued", "downloading", "verifying", "repairing", "extracting", "completed", "failed"}

var torrentStates = []string{"queued", "metadata", "checking", "downloading", "seeding", "paused", "completed", "failed"}

var libraryKinds = []string{"movies", "series", "episodes", "artists", "albums", "tracks"}

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type storageStats struct {
	Free  int64
	Total int64
	Known bool
}

type metricsSnapshot struct {
	At           time.Time
	DBUp         bool
	Pool         poolStats
	Downloads    []downloadStat
	Torrents     []downloadStat
	JobsQueued   int64
	JobsActive   int64
	Failures     map[string]int64
	Library      map[string]int64
	Storage      storageStats
	AlertsFiring int
	BackupAt     time.Time
	Collecting   bool
}

type poolStats struct {
	Total, Idle, Acquired, Max, Constructing int32
	Acquires, EmptyAcquires, NewConns        uint64
	AcquireDuration                          time.Duration
}

type reqKey struct{ method, code string }

type durationStat struct {
	buckets []uint64
	sum     float64
	count   uint64
}

type metricsState struct {
	startedAt time.Time

	mu       sync.RWMutex
	snapshot metricsSnapshot

	httpMu    sync.Mutex
	requests  map[reqKey]uint64
	durations map[string]*durationStat
	inFlight  int64
}

func newMetricsState() metricsState {
	return metricsState{
		startedAt: time.Now(),
		requests:  map[reqKey]uint64{},
		durations: map[string]*durationStat{},
	}
}

func (m *metricsState) setSnapshot(snapshot metricsSnapshot) {
	m.mu.Lock()
	m.snapshot = snapshot
	m.mu.Unlock()
}

func (m *metricsState) current() metricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot
}

func (m *metricsState) observeRequest(method string, status int, duration time.Duration) {
	key := reqKey{method: boundedMethod(method), code: strconv.Itoa(status)}
	m.httpMu.Lock()
	defer m.httpMu.Unlock()
	m.requests[key]++
	stat := m.durations[key.method]
	if stat == nil {
		stat = &durationStat{buckets: make([]uint64, len(durationBuckets))}
		m.durations[key.method] = stat
	}
	seconds := duration.Seconds()
	for index, bucket := range durationBuckets {
		if seconds <= bucket {
			stat.buckets[index]++
		}
	}
	stat.sum += seconds
	stat.count++
}

func (m *metricsState) startRequest() {
	m.httpMu.Lock()
	m.inFlight++
	m.httpMu.Unlock()
}

func (m *metricsState) finishRequest() {
	m.httpMu.Lock()
	m.inFlight--
	m.httpMu.Unlock()
}

func boundedMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method
	}
	return "other"
}

func (s *Service) writeMetrics(w io.Writer) {
	memory := readMemStats()
	snapshot := s.metrics.current()
	pool := s.pool.Stat()
	m := &s.metrics

	printMetric(w, "constellarr_up", "gauge", "Whether the operations collector is running.",
		[]series{{labels: nil, value: boolValue(snapshot.Collecting)}})
	printMetric(w, "constellarr_db_up", "gauge", "Whether the PostgreSQL pool accepted the last health check.",
		[]series{{labels: nil, value: boolValue(snapshot.DBUp)}})
	printMetric(w, "constellarr_collector_last_run_timestamp_seconds", "gauge", "Unix time of the last metrics collection.",
		[]series{{labels: nil, value: float64(snapshot.At.Unix())}})

	printMetric(w, "go_goroutines", "gauge", "Number of goroutines that currently exist.",
		[]series{{labels: nil, value: float64(runtime.NumGoroutine())}})
	printMetric(w, "go_info", "gauge", "Go runtime version.",
		[]series{{labels: []label{{"version", runtime.Version()}}, value: 1}})
	printMetric(w, "go_memstats_alloc_bytes", "gauge", "Bytes of allocated heap objects.",
		[]series{{labels: nil, value: float64(memory.Alloc)}})
	printMetric(w, "go_memstats_alloc_bytes_total", "counter", "Total bytes allocated, even if freed.",
		[]series{{labels: nil, value: float64(memory.TotalAlloc)}})
	printMetric(w, "go_memstats_sys_bytes", "gauge", "Bytes obtained from the operating system.",
		[]series{{labels: nil, value: float64(memory.Sys)}})
	printMetric(w, "go_memstats_heap_alloc_bytes", "gauge", "Bytes of allocated heap objects.",
		[]series{{labels: nil, value: float64(memory.HeapAlloc)}})
	printMetric(w, "go_memstats_heap_inuse_bytes", "gauge", "Bytes in in-use heap spans.",
		[]series{{labels: nil, value: float64(memory.HeapInuse)}})
	printMetric(w, "go_memstats_heap_objects", "gauge", "Number of allocated objects.",
		[]series{{labels: nil, value: float64(memory.HeapObjects)}})
	printMetric(w, "go_memstats_stack_inuse_bytes", "gauge", "Bytes in in-use stack spans.",
		[]series{{labels: nil, value: float64(memory.StackInuse)}})
	printMetric(w, "go_memstats_mallocs_total", "counter", "Total number of heap objects allocated.",
		[]series{{labels: nil, value: float64(memory.Mallocs)}})
	printMetric(w, "go_memstats_frees_total", "counter", "Total number of heap objects freed.",
		[]series{{labels: nil, value: float64(memory.Frees)}})
	printMetric(w, "go_memstats_gc_cpu_fraction", "gauge", "Fraction of CPU time used by the garbage collector.",
		[]series{{labels: nil, value: memory.GCCPUFraction}})
	printMetric(w, "go_memstats_last_gc_time_seconds", "gauge", "Unix time of the last garbage collection.",
		[]series{{labels: nil, value: float64(memory.LastGC) / 1e9}})
	printMetric(w, "go_memstats_next_gc_bytes", "gauge", "Heap size target for the next garbage collection.",
		[]series{{labels: nil, value: float64(memory.NextGC)}})

	process := readProcessStats()
	printMetric(w, "process_start_time_seconds", "gauge", "Unix time the process started.",
		[]series{{labels: nil, value: float64(s.startedAt().Unix())}})
	printMetric(w, "process_cpu_seconds_total", "counter", "Total user and system CPU time spent in seconds.",
		[]series{{labels: nil, value: process.cpuSeconds}})
	printMetric(w, "process_resident_memory_bytes", "gauge", "Resident memory size in bytes.",
		[]series{{labels: nil, value: float64(process.residentBytes)}})
	printMetric(w, "process_open_fds", "gauge", "Number of open file descriptors.",
		[]series{{labels: nil, value: float64(process.openFDs)}})
	printMetric(w, "process_max_fds", "gauge", "Maximum number of open file descriptors.",
		[]series{{labels: nil, value: float64(process.maxFDs)}})

	printMetric(w, "constellarr_db_pool_connections", "gauge", "PostgreSQL pool connections by state.",
		[]series{
			{labels: []label{{"state", "total"}}, value: float64(pool.TotalConns())},
			{labels: []label{{"state", "idle"}}, value: float64(pool.IdleConns())},
			{labels: []label{{"state", "acquired"}}, value: float64(pool.AcquiredConns())},
			{labels: []label{{"state", "max"}}, value: float64(pool.MaxConns())},
			{labels: []label{{"state", "constructing"}}, value: float64(pool.ConstructingConns())},
		})
	printMetric(w, "constellarr_db_pool_acquires_total", "counter", "Successful pool acquisitions.",
		[]series{{labels: nil, value: float64(pool.AcquireCount())}})
	printMetric(w, "constellarr_db_pool_acquire_empty_total", "counter", "Acquisitions that waited for a connection.",
		[]series{{labels: nil, value: float64(pool.EmptyAcquireCount())}})
	printMetric(w, "constellarr_db_pool_new_connections_total", "counter", "Connections opened by the pool.",
		[]series{{labels: nil, value: float64(pool.NewConnsCount())}})
	printMetric(w, "constellarr_db_pool_acquire_duration_seconds_total", "counter", "Cumulative time spent acquiring connections.",
		[]series{{labels: nil, value: pool.AcquireDuration().Seconds()}})

	states := map[string]downloadStat{}
	for _, stat := range snapshot.Downloads {
		states[stat.State] = stat
	}
	var counts, done, total []series
	for _, state := range downloadStates {
		stat := states[state]
		label := []label{{"state", state}}
		counts = append(counts, series{labels: label, value: float64(stat.Count)})
		done = append(done, series{labels: label, value: float64(stat.BytesDone)})
		total = append(total, series{labels: label, value: float64(stat.BytesTotal)})
	}
	printMetric(w, "constellarr_downloads", "gauge", "Usenet download jobs by state.", counts)
	printMetric(w, "constellarr_download_bytes_done", "gauge", "Downloaded Usenet bytes by job state.", done)
	printMetric(w, "constellarr_download_bytes_total", "gauge", "Total Usenet bytes by job state.", total)
	printMetric(w, "constellarr_jobs_queued", "gauge", "Usenet download jobs waiting to start.",
		[]series{{labels: nil, value: float64(snapshot.JobsQueued)}})
	printMetric(w, "constellarr_jobs_active", "gauge", "Usenet download jobs that are not finished.",
		[]series{{labels: nil, value: float64(snapshot.JobsActive)}})

	torrents := map[string]downloadStat{}
	for _, stat := range snapshot.Torrents {
		torrents[stat.State] = stat
	}
	var torrentCounts, torrentDone, torrentTotal []series
	for _, state := range torrentStates {
		stat := torrents[state]
		label := []label{{"state", state}}
		torrentCounts = append(torrentCounts, series{labels: label, value: float64(stat.Count)})
		torrentDone = append(torrentDone, series{labels: label, value: float64(stat.BytesDone)})
		torrentTotal = append(torrentTotal, series{labels: label, value: float64(stat.BytesTotal)})
	}
	printMetric(w, "constellarr_torrents", "gauge", "Torrent jobs by state.", torrentCounts)
	printMetric(w, "constellarr_torrent_bytes_done", "gauge", "Downloaded torrent bytes by job state.", torrentDone)
	printMetric(w, "constellarr_torrent_bytes_total", "gauge", "Total torrent bytes by job state.", torrentTotal)
	printMetric(w, "constellarr_torrents_queued", "gauge", "Torrent jobs waiting to start.",
		[]series{{labels: nil, value: float64(torrents["queued"].Count + torrents["metadata"].Count)}})
	printMetric(w, "constellarr_torrents_active", "gauge", "Torrent jobs downloading or checking.",
		[]series{{labels: nil, value: float64(torrents["downloading"].Count + torrents["checking"].Count)}})
	printMetric(w, "constellarr_torrents_seeding", "gauge", "Torrent jobs seeding.",
		[]series{{labels: nil, value: float64(torrents["seeding"].Count)}})
	printMetric(w, "constellarr_download_failures_total", "counter", "Recorded download failures.",
		[]series{{labels: nil, value: float64(snapshot.Failures[KindDownloadFailed])}})
	printMetric(w, "constellarr_import_errors_total", "counter", "Recorded import errors.",
		[]series{{labels: nil, value: float64(snapshot.Failures[KindImportError])}})
	printMetric(w, "constellarr_provider_failures_total", "counter", "Recorded provider failures.",
		[]series{{labels: nil, value: float64(snapshot.Failures[KindProviderFailure])}})
	var library []series
	for _, kind := range libraryKinds {
		library = append(library, series{labels: []label{{"kind", kind}}, value: float64(snapshot.Library[kind])})
	}
	printMetric(w, "constellarr_library_items", "gauge", "Catalog items by kind.", library)
	printMetric(w, "constellarr_storage_free_bytes", "gauge", "Last known free space on the data volume.",
		[]series{{labels: []label{{"volume", "data"}}, value: float64(snapshot.Storage.Free)}})
	printMetric(w, "constellarr_storage_total_bytes", "gauge", "Last known size of the data volume.",
		[]series{{labels: []label{{"volume", "data"}}, value: float64(snapshot.Storage.Total)}})
	printMetric(w, "constellarr_alerts_firing", "gauge", "Alert rules that are currently firing.",
		[]series{{labels: nil, value: float64(snapshot.AlertsFiring)}})
	printMetric(w, "constellarr_backup_last_success_timestamp_seconds", "gauge", "Unix time of the last successful backup.",
		[]series{{labels: nil, value: float64(snapshot.BackupAt.Unix())}})

	m.httpMu.Lock()
	requests := make([]series, 0, len(m.requests))
	for key, count := range m.requests {
		requests = append(requests, series{labels: []label{{"method", key.method}, {"code", key.code}}, value: float64(count)})
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].labels[0].value != requests[j].labels[0].value {
			return requests[i].labels[0].value < requests[j].labels[0].value
		}
		return requests[i].labels[1].value < requests[j].labels[1].value
	})
	methods := make([]string, 0, len(m.durations))
	for method := range m.durations {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	durationSeries := make([]series, 0, len(methods)*(len(durationBuckets)+2))
	for _, method := range methods {
		stat := m.durations[method]
		for index, bucket := range durationBuckets {
			durationSeries = append(durationSeries, series{
				labels: []label{{"method", method}, {"le", formatFloat(bucket)}},
				value:  float64(stat.buckets[index]), suffix: "_bucket",
			})
		}
		durationSeries = append(durationSeries,
			series{labels: []label{{"method", method}, {"le", "+Inf"}}, value: float64(stat.count), suffix: "_bucket"},
			series{labels: []label{{"method", method}}, value: stat.sum, suffix: "_sum"},
			series{labels: []label{{"method", method}}, value: float64(stat.count), suffix: "_count"})
	}
	inFlight := m.inFlight
	m.httpMu.Unlock()
	printMetric(w, "constellarr_http_requests_total", "counter", "HTTP requests handled by the server.", requests)
	printHistogram(w, "constellarr_http_request_duration_seconds", "HTTP request duration in seconds.", durationSeries)
	printMetric(w, "constellarr_http_in_flight_requests", "gauge", "HTTP requests currently being handled.",
		[]series{{labels: nil, value: float64(inFlight)}})
}

func (s *Service) startedAt() time.Time { return s.metrics.startedAt }

type label struct{ name, value string }

type series struct {
	labels []label
	value  float64
	suffix string
}

func printMetric(w io.Writer, name, kind, help string, values []series) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	for _, entry := range values {
		fmt.Fprintf(w, "%s%s %s\n", name+entry.suffix, formatLabels(entry.labels), formatFloat(entry.value))
	}
}

func printHistogram(w io.Writer, name, help string, values []series) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
	for _, entry := range values {
		fmt.Fprintf(w, "%s%s %s\n", name+entry.suffix, formatLabels(entry.labels), formatFloat(entry.value))
	}
}

func formatLabels(labels []label) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, entry := range labels {
		parts = append(parts, entry.name+`="`+escapeLabel(entry.value)+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

type memStats struct {
	Alloc, TotalAlloc, Sys, HeapAlloc, HeapInuse, StackInuse, NextGC uint64
	HeapObjects, Mallocs, Frees                                      uint64
	GCCPUFraction                                                    float64
	LastGC                                                           uint64
}

func readMemStats() memStats {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return memStats{
		Alloc: memory.Alloc, TotalAlloc: memory.TotalAlloc, Sys: memory.Sys, HeapAlloc: memory.HeapAlloc,
		HeapInuse: memory.HeapInuse, StackInuse: memory.StackInuse, NextGC: memory.NextGC,
		HeapObjects: memory.HeapObjects, Mallocs: memory.Mallocs, Frees: memory.Frees,
		GCCPUFraction: memory.GCCPUFraction, LastGC: memory.LastGC,
	}
}

type processStats struct {
	cpuSeconds    float64
	residentBytes int64
	openFDs       int
	maxFDs        uint64
}

func readProcessStats() processStats {
	var stats processStats
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil {
		stats.cpuSeconds = float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6 +
			float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6
		stats.residentBytes = maxRSSBytes(int64(usage.Maxrss))
	}
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err == nil {
		stats.maxFDs = uint64(limits.Cur)
	}
	stats.openFDs = countFDs()
	return stats
}

// maxRSSBytes converts Maxrss to bytes; Linux reports kilobytes, BSD and macOS bytes.
func maxRSSBytes(maxRSS int64) int64 {
	if runtime.GOOS == "linux" {
		return maxRSS * 1024
	}
	return maxRSS
}

func countFDs() int {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(dir)
		if err == nil {
			return len(entries)
		}
	}
	return 0
}
