package downloads

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/media"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

var (
	ErrNotFound      = errors.New("downloads: not found")
	ErrConflict      = errors.New("downloads: conflict")
	ErrNotConfigured = errors.New("downloads: source is not configured")
	ErrInvalid       = errors.New("downloads: invalid input")
)

const (
	statusQueued      = "queued"
	statusDownloading = "downloading"
	statusFailed      = "failed"
	statusCompleted   = "completed"

	listLimit         = 50
	maxReleaseIDBytes = 128
	maxTitleRunes     = 300
	maxErrorRunes     = 300
	maxOutputNameLen  = 1024
	progressInterval  = time.Second
	claimPoll         = 2 * time.Second
	lockPoll          = 5 * time.Second
	heartbeatInterval = 10 * time.Second
	probeTimeout      = 5 * time.Second
	reconnectDelay    = time.Second
	persistTimeout    = 3 * time.Second
)

const lockHeldSQL = `SELECT EXISTS (
	SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND pid = $1
	AND ((classid::bigint << 32) | objid::bigint) = $2)`

type OutputFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type Job struct {
	ID              string       `json:"id"`
	ReleaseID       string       `json:"releaseId"`
	Title           string       `json:"title"`
	Status          string       `json:"status"`
	BytesDone       int64        `json:"bytesDone"`
	BytesTotal      int64        `json:"bytesTotal"`
	SegmentsDone    int          `json:"segmentsDone"`
	SegmentsTotal   int          `json:"segmentsTotal"`
	MissingSegments int          `json:"missingSegments"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	Error           string       `json:"error,omitempty"`
	Files           []OutputFile `json:"files"`
}

type Manager struct {
	pool    *pgxpool.Pool
	cfg     Config
	indexer *indexer.Client
	root    string

	started  atomic.Bool
	cancelMu sync.Mutex
	cancel   context.CancelFunc
	workers  sync.WaitGroup
}

func New(ctx context.Context, pool *pgxpool.Pool, cfg Config) (*Manager, error) {
	if pool == nil {
		return nil, errors.New("downloads: a PostgreSQL pool is required")
	}
	directory := strings.TrimSpace(cfg.Directory)
	if directory == "" {
		return nil, errors.New("downloads: a download directory is required")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("downloads: the download directory is invalid")
	}
	root := filepath.Join(directory, "downloads")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("downloads: the download directory could not be created")
	}
	if err := migrate(ctx, pool); err != nil {
		return nil, err
	}
	manager := &Manager{pool: pool, cfg: cfg, root: root}
	if cfg.IndexerURL != "" && cfg.APIKey != "" {
		manager.indexer, err = indexer.New(cfg.IndexerURL, cfg.APIKey)
		if err != nil {
			return nil, fmt.Errorf("downloads: %w", err)
		}
	}
	return manager, nil
}

func (m *Manager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !m.started.CompareAndSwap(false, true) {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancelMu.Lock()
	m.cancel = cancel
	m.cancelMu.Unlock()
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		m.coordinate(runCtx)
	}()
}

func (m *Manager) Close() {
	m.cancelMu.Lock()
	cancel := m.cancel
	m.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.workers.Wait()
}

func (m *Manager) Search(ctx context.Context, query string) ([]indexer.Release, error) {
	if m.indexer == nil {
		return nil, ErrNotConfigured
	}
	releases, err := m.indexer.Search(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("downloads: search releases: %w", err)
	}
	if releases == nil {
		releases = []indexer.Release{}
	}
	return releases, nil
}

func (m *Manager) TestIndexer(ctx context.Context) error {
	if m.indexer == nil {
		return ErrNotConfigured
	}
	if err := m.indexer.Test(ctx); err != nil {
		return fmt.Errorf("downloads: test indexer: %w", err)
	}
	return nil
}

func (m *Manager) TestUsenet(ctx context.Context) error {
	if !m.usenetConfigured() {
		return ErrNotConfigured
	}
	if err := usenet.Test(ctx, m.cfg.Usenet); err != nil {
		return fmt.Errorf("downloads: test usenet: %w", err)
	}
	return nil
}

func (m *Manager) usenetConfigured() bool {
	return m.cfg.Usenet.Host != "" && m.cfg.Usenet.Username != "" && m.cfg.Usenet.Password != ""
}

func (m *Manager) Add(ctx context.Context, releaseID, title string) (Job, error) {
	releaseID = strings.TrimSpace(releaseID)
	title = strings.TrimSpace(title)
	if !validReleaseID(releaseID) || title == "" || utf8.RuneCountInString(title) > maxTitleRunes {
		return Job{}, ErrInvalid
	}
	existing, err := m.jobByRelease(ctx, m.pool, releaseID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Job{}, err
	}
	if m.indexer == nil {
		return Job{}, ErrNotConfigured
	}
	nzb, err := m.indexer.NZB(ctx, releaseID)
	if err != nil {
		return Job{}, fmt.Errorf("downloads: fetch NZB: %w", err)
	}
	job, err := m.insertJob(ctx, m.pool, rand.Text(), releaseID, title, nzb)
	if errors.Is(err, ErrConflict) {
		return m.jobByRelease(ctx, m.pool, releaseID)
	}
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func (m *Manager) List(ctx context.Context) ([]Job, error) {
	jobs, err := m.listJobs(ctx, m.pool)
	if err != nil {
		return nil, err
	}
	if jobs == nil {
		jobs = []Job{}
	}
	return jobs, nil
}

func (m *Manager) Get(ctx context.Context, id string) (Job, error) {
	return m.jobByID(ctx, m.pool, id)
}

func (m *Manager) Retry(ctx context.Context, id string) (Job, error) {
	job, err := m.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status != statusFailed {
		return Job{}, ErrConflict
	}
	return m.requeueJob(ctx, m.pool, id)
}

func (m *Manager) OpenFile(ctx context.Context, id, name string) (*os.File, error) {
	job, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status != statusCompleted {
		return nil, ErrConflict
	}
	declared := false
	for _, file := range job.Files {
		if file.Name == name {
			declared = true
			break
		}
	}
	if !declared || name == "" || len(name) > maxOutputNameLen || !filepath.IsLocal(name) {
		return nil, ErrNotFound
	}
	directory, ok := m.jobDir(job.ID, "output")
	if !ok {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	handle, err := root.Open(name)
	if err != nil {
		return nil, ErrNotFound
	}
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		handle.Close()
		return nil, ErrNotFound
	}
	return handle, nil
}

func (m *Manager) jobDir(id, part string) (string, bool) {
	if id == "" || !filepath.IsLocal(id) {
		return "", false
	}
	return filepath.Join(m.root, id, part), true
}

func (m *Manager) coordinate(ctx context.Context) {
	for {
		conn, err := m.coordinationLock(ctx)
		if err != nil {
			return
		}
		session := m.holdLock(ctx, conn)
		m.runQueue(session)
		session.close()
		if !sleep(ctx, reconnectDelay) {
			return
		}
	}
}

func (m *Manager) coordinationLock(ctx context.Context) (*pgxpool.Conn, error) {
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		acquireCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		conn, err := m.pool.Acquire(acquireCtx)
		cancel()
		if err == nil {
			probeCtx, probeCancel := context.WithTimeout(ctx, probeTimeout)
			var locked bool
			err = conn.QueryRow(probeCtx, "SELECT pg_try_advisory_lock($1)", coordinatorLock).Scan(&locked)
			probeCancel()
			if err == nil && locked {
				return conn, nil
			}
			conn.Release()
		}
		if !sleep(ctx, lockPoll) {
			return nil, ctx.Err()
		}
	}
}

func (m *Manager) holdLock(ctx context.Context, conn *pgxpool.Conn) *lockSession {
	runCtx, cancel := context.WithCancel(ctx)
	session := &lockSession{
		manager: m, conn: conn, ctx: runCtx, cancel: cancel, stop: make(chan struct{}),
		pid: int64(conn.Conn().PgConn().PID()),
	}
	go session.watch()
	return session
}

type lockSession struct {
	manager *Manager
	conn    *pgxpool.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	stop    chan struct{}
	pid     int64
	lost    atomic.Bool
}

// watch probes from a pooled connection because the lock connection is not safe for concurrent use.
func (s *lockSession) watch() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(s.ctx, probeTimeout)
			var held bool
			err := s.manager.pool.QueryRow(probeCtx, lockHeldSQL, s.pid, coordinatorLock).Scan(&held)
			cancel()
			if err != nil || !held {
				s.fail()
				return
			}
		}
	}
}

func (s *lockSession) fail() {
	if s.lost.CompareAndSwap(false, true) {
		s.cancel()
	}
}

func (s *lockSession) close() {
	close(s.stop)
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if s.lost.Load() {
		raw := s.conn.Hijack()
		_ = raw.Close(ctx)
		return
	}
	_, _ = s.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", coordinatorLock)
	s.conn.Release()
}

func (m *Manager) runQueue(session *lockSession) {
	if err := m.requeueInterrupted(session.ctx, session.conn); err != nil {
		session.fail()
		return
	}
	for {
		if session.ctx.Err() != nil {
			return
		}
		job, nzb, err := m.claimNext(session.ctx, session.conn)
		switch {
		case errors.Is(err, ErrNotFound):
			if !sleep(session.ctx, claimPoll) {
				return
			}
		case err != nil:
			session.fail()
			return
		default:
			m.process(session, job, nzb)
		}
	}
}

func (m *Manager) process(session *lockSession, job Job, nzb []byte) {
	if !m.usenetConfigured() {
		m.recordFailure(session, job.ID, "Usenet source is not configured")
		return
	}
	inputDir, ok := m.jobDir(job.ID, "input")
	if !ok {
		m.recordFailure(session, job.ID, "download directory is invalid")
		return
	}
	if err := os.MkdirAll(inputDir, 0o700); err != nil {
		m.recordFailure(session, job.ID, "download directory could not be created")
		return
	}
	progress := &progressWriter{manager: m, session: session, id: job.ID}
	result, err := usenet.Download(session.ctx, nzb, inputDir, m.cfg.Usenet, progress.report)
	progress.finish(result.MissingSegments)
	if err != nil {
		if session.ctx.Err() != nil {
			return
		}
		m.recordFailure(session, job.ID, m.safeError(err))
		return
	}
	outputDir, ok := m.jobDir(job.ID, "output")
	if !ok {
		m.recordFailure(session, job.ID, "download directory is invalid")
		return
	}
	files, err := media.Process(session.ctx, inputDir, outputDir, result.MissingSegments, func(stage string) {
		if session.ctx.Err() == nil {
			if err := m.setStatus(session.ctx, session.conn, job.ID, stage); err != nil {
				session.fail()
			}
		}
	})
	if err != nil {
		if session.ctx.Err() != nil {
			return
		}
		m.recordFailure(session, job.ID, m.safeError(err))
		return
	}
	outputs, err := outputFiles(job.ID, outputDir, files)
	if err != nil {
		m.recordFailure(session, job.ID, err.Error())
		return
	}
	if err := m.complete(session.ctx, session.conn, job.ID, outputs); err != nil {
		session.fail()
	}
}

func (m *Manager) recordFailure(session *lockSession, id, message string) {
	if err := m.failJob(session.ctx, session.conn, id, message); err != nil {
		session.fail()
	}
}

func (m *Manager) safeError(err error) string {
	message := strings.TrimSpace(err.Error())
	for _, secret := range []string{m.cfg.APIKey, m.cfg.Usenet.Username, m.cfg.Usenet.Password} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if message == "" {
		message = "download failed"
	}
	if runes := []rune(message); len(runes) > maxErrorRunes {
		message = string(runes[:maxErrorRunes])
	}
	return message
}

func outputFiles(id, directory string, files []media.File) ([]OutputFile, error) {
	if len(files) == 0 {
		return nil, errors.New("download produced no media files")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("download output is not available")
	}
	defer root.Close()
	outputs := make([]OutputFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		name := file.Name
		if name == "" || len(name) > maxOutputNameLen || !filepath.IsLocal(name) || seen[name] {
			return nil, errors.New("download output contains an unusable file name")
		}
		seen[name] = true
		handle, err := root.Open(name)
		if err != nil {
			return nil, errors.New("download output is incomplete")
		}
		info, err := handle.Stat()
		handle.Close()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil, errors.New("download output is incomplete")
		}
		outputs = append(outputs, OutputFile{
			Name: name,
			Size: info.Size(),
			URL:  "/api/v1/downloads/" + id + "/file?name=" + url.QueryEscape(name),
		})
	}
	return outputs, nil
}

type progressWriter struct {
	manager *Manager
	session *lockSession
	id      string
	last    time.Time
	latest  usenet.Progress
	seen    bool
}

func (p *progressWriter) report(update usenet.Progress) {
	p.latest = update
	p.seen = true
	if time.Since(p.last) < progressInterval {
		return
	}
	p.last = time.Now()
	if err := p.manager.saveProgress(p.session.ctx, p.session.conn, p.id, update); err != nil {
		p.session.fail()
	}
}

func (p *progressWriter) finish(missing int) {
	if !p.seen || p.session.lost.Load() {
		return
	}
	if missing > 0 {
		p.latest.MissingSegments = missing
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.session.ctx), persistTimeout)
	defer cancel()
	if err := p.manager.saveProgress(ctx, p.session.conn, p.id, p.latest); err != nil {
		p.session.fail()
	}
}

func validReleaseID(id string) bool {
	if id == "" || len(id) > maxReleaseIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
