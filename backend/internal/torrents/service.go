package torrents

import (
	"context"
	"crypto/rand"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/anacrolix/torrent"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"
)

const defaultListenPort = 51413

type Options struct {
	Directory  string
	Testing    bool
	ListenPort int
	Policy     Policy
}

type Service struct {
	pool      *pgxpool.Pool
	directory string
	root      string
	testing   bool

	configMu sync.RWMutex
	cfg      Settings

	engineMu      sync.Mutex
	publicClient  *torrent.Client
	privateClient *torrent.Client
	engineErr     string
	clientErr     bool

	policy        Policy
	sharedLimiter bool

	downloadLimiter *rate.Limiter
	uploadLimiter   *rate.Limiter
	httpClient      *http.Client
	results         *resultCache

	runsMu sync.Mutex
	runs   map[string]*jobRun

	intentsMu sync.Mutex
	intents   map[string]jobIntent

	processMu      sync.Mutex
	processCancels map[string]context.CancelFunc

	started  atomic.Bool
	cancelMu sync.Mutex
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	wake     chan struct{}

	processingActive atomic.Int64
}

// jobIntent records pause and cancel requests that must survive a concurrent torrent attach.
type jobIntent struct {
	paused    bool
	cancelled bool
}

// New creates the torrent service; downloads migrations must have created the torrent tables.
func New(ctx context.Context, pool *pgxpool.Pool, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("torrents: a PostgreSQL pool is required")
	}
	directory := strings.TrimSpace(options.Directory)
	if directory == "" {
		return nil, errors.New("torrents: a download directory is required")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("torrents: the download directory is invalid")
	}
	root := filepath.Join(directory, "torrents")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("torrents: the download directory could not be created")
	}
	service := &Service{
		pool: pool, directory: directory, root: root, testing: options.Testing,
		runs: make(map[string]*jobRun), intents: make(map[string]jobIntent), wake: make(chan struct{}, 1),
		processCancels:  make(map[string]context.CancelFunc),
		downloadLimiter: rate.NewLimiter(rate.Inf, 1<<20), uploadLimiter: rate.NewLimiter(rate.Inf, 1<<20),
		httpClient: boundedHTTPClient(), results: newResultCache(),
	}
	if usablePolicy(options.Policy) {
		service.policy = options.Policy
		// The policy owns the shared download limiter; torrent settings must not touch it.
		if limiter := options.Policy.Limiter(); limiter != nil {
			service.downloadLimiter, service.sharedLimiter = limiter, true
		}
	}
	if err := service.checkSchema(ctx, pool); err != nil {
		return nil, err
	}
	cfg, found, err := loadSettings(ctx, pool)
	if err != nil {
		return nil, err
	}
	if !found {
		cfg = Settings{
			ListenPort: defaultListenPort, DHTEnabled: true, PEXEnabled: true,
			MaxActiveJobs: 3, SeedRatioLimit: 1,
		}
		if options.ListenPort > 0 && options.ListenPort <= 65535 {
			cfg.ListenPort = options.ListenPort
		}
	}
	service.cfg = cfg
	service.applyRuntimeSettings(cfg)
	return service, nil
}

func (s *Service) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancelMu.Lock()
	s.cancel = cancel
	s.cancelMu.Unlock()
	if err := s.resumeProcessing(runCtx, s.pool); err != nil {
		s.setEngineError(err.Error())
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.coordinate(runCtx)
	}()
}

func (s *Service) Close() {
	s.cancelMu.Lock()
	cancel := s.cancel
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.workers.Wait()
	ctx, cancelTimeout := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelTimeout()
	for _, run := range s.runList() {
		stats := run.tor.Stats()
		if err := run.store.flush(ctx); err != nil {
			s.setEngineError(safeRunError(err))
		}
		piecesDone, bits := run.store.completed()
		_ = s.saveProgress(ctx, s.pool, run.job.ID, run.tor.BytesCompleted(),
			count(stats.BytesWrittenData), piecesDone, bits)
		run.tor.Drop()
	}
	s.closeClients()
}

func (s *Service) coordinate(ctx context.Context) {
	s.tick(ctx)
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		case <-s.wake:
			s.tick(ctx)
		}
	}
}

func (s *Service) wakeEngine() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

type AddInput struct {
	Magnet    string
	Torrent   []byte
	Filename  string
	Source    string
	ReleaseID string
	Title     string
	// Private is a tracker-feed hint that pins the job away from DHT and PEX from the first attach.
	Private bool
}

func (s *Service) Add(ctx context.Context, input AddInput) (Job, error) {
	magnet := strings.TrimSpace(input.Magnet)
	hasTorrent := len(input.Torrent) != 0
	switch {
	case magnet != "" && hasTorrent:
		return Job{}, ErrInvalid
	case magnet == "" && !hasTorrent:
		return Job{}, ErrInvalid
	}
	source := input.Source
	if source != sourceMagnet && source != sourceFile && source != sourceTorznab {
		if hasTorrent {
			source = sourceFile
		} else {
			source = sourceMagnet
		}
	}
	job := storedJob{}
	job.Source = source
	job.ReleaseID = strings.TrimSpace(input.ReleaseID)
	job.Title = strings.TrimSpace(input.Title)
	if utf8.RuneCountInString(job.Title) > maxNameRunes || len(job.ReleaseID) > 200 {
		return Job{}, ErrInvalid
	}
	if hasTorrent {
		meta, hash, err := loadMetainfo(input.Torrent)
		if err != nil {
			return Job{}, err
		}
		info, err := meta.UnmarshalInfo()
		if err != nil {
			return Job{}, invalid("the torrent file has no readable metadata")
		}
		job.InfoHash = hash
		job.Name = sanitizeDisplayName(info.BestName())
		job.metainfo = input.Torrent
		job.Private = input.Private || privateFromMetainfo(&info)
		if job.Title == "" {
			job.Title = job.Name
		}
	} else {
		hash, err := parseMagnet(magnet)
		if err != nil {
			return Job{}, err
		}
		job.InfoHash = hash
		job.magnet = magnet
		job.Private = input.Private
		if job.Private && !magnetHasTrackers(magnet) {
			return Job{}, invalid("a private magnet needs its tracker; supply the torrent file instead")
		}
		if job.Title == "" {
			job.Title = magnetDisplayName(magnet)
		}
		if job.Title == "" {
			job.Title = hash
		}
	}
	if existing, err := s.jobByInfoHash(ctx, s.pool, job.InfoHash); err == nil {
		return s.liveJob(existing), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Job{}, err
	}
	settings := s.Settings()
	job.SeedRatioLimit, job.SeedTimeLimitMinutes = seedPolicy(settings.SeedRatioLimit, settings.SeedTimeLimitMinutes)
	job.ID = rand.Text()
	if filename := strings.TrimSpace(input.Filename); filename != "" && job.Name == "" {
		if name := sanitizeDisplayName(filename); name != "" {
			job.Name = name
		}
	}
	created, err := s.insertJob(ctx, s.pool, job)
	if errors.Is(err, ErrConflict) {
		existing, err := s.jobByInfoHash(ctx, s.pool, job.InfoHash)
		if err != nil {
			return Job{}, err
		}
		return s.liveJob(existing), nil
	}
	if err != nil {
		return Job{}, err
	}
	if !s.policyAllowed() {
		reason := s.holdReason()
		if err := s.setQueuedReason(ctx, s.pool, created.ID, reason); err != nil {
			s.setEngineError(err.Error())
		} else {
			created.Error = reason
		}
	}
	s.wakeEngine()
	return s.liveJob(created), nil
}

func magnetDisplayName(magnet string) string {
	values, err := url.ParseQuery(strings.TrimPrefix(magnet, "magnet:?"))
	if err != nil {
		return ""
	}
	return sanitizeDisplayName(values.Get("dn"))
}

func sanitizeDisplayName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
	}
	return name
}

func (s *Service) List(ctx context.Context) ([]Job, error) {
	jobs, err := s.listJobs(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	states, err := s.processingByJob(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	for index := range jobs {
		jobs[index] = s.liveJob(jobs[index])
		if row, ok := states[jobs[index].ID]; ok {
			jobs[index].Processing = row.public()
		}
	}
	return jobs, nil
}

func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return Detail{}, err
	}
	job = s.liveJob(job)
	if row, err := s.loadProcessing(ctx, s.pool, id); err == nil {
		job.Processing = row.public()
	}
	detail := Detail{Job: job, Peers: []Peer{}}
	if run := s.run(id); run != nil {
		detail.Peers = peersOf(run)
	}
	return detail, nil
}

func (s *Service) liveJob(job Job) Job {
	if job.Files == nil {
		job.Files = []File{}
	}
	run := s.run(job.ID)
	if run == nil {
		return job
	}
	tor := run.torrent()
	if tor == nil {
		return job
	}
	stats := tor.Stats()
	job.BytesDone = tor.BytesCompleted()
	job.Uploaded = count(stats.BytesWrittenData)
	// A magnet has no piece information until its metadata arrives.
	if tor.Info() != nil {
		if length := tor.Length(); length > 0 {
			job.BytesTotal = length
			job.Ratio = ratioOf(job.Uploaded, length)
		}
		if pieces := tor.NumPieces(); pieces > 0 {
			job.PiecesTotal = pieces
			job.PiecesDone = stats.PiecesComplete
		}
	}
	run.mu.Lock()
	job.DownloadRate, job.UploadRate = int64(run.downRate), int64(run.upRate)
	elapsed := run.seedBase
	if !run.seedingAt.IsZero() {
		elapsed += int64(time.Since(run.seedingAt).Seconds())
	}
	run.mu.Unlock()
	job.SeedingElapsed = elapsed
	if job.DownloadRate > 0 && job.BytesTotal > job.BytesDone {
		job.ETASeconds = (job.BytesTotal - job.BytesDone) / job.DownloadRate
	}
	job.Peers = stats.ActivePeers + stats.PendingPeers
	job.Seeds = stats.ConnectedSeeders
	job.Files = liveFiles(run, job.Files, job.Status)
	return job
}

func liveFiles(run *jobRun, files []File, status string) []File {
	byName := make(map[string]*torrent.File, len(files))
	info := run.tor.Info()
	if info == nil {
		return files
	}
	for _, file := range run.tor.Files() {
		byName[safeFilePath(info, file.FileInfo())] = file
	}
	for index := range files {
		if file := byName[files[index].Name]; file != nil {
			files[index].Done = file.BytesCompleted()
		}
		if status == statusCompleted || status == statusSeeding {
			files[index].URL = torrentFileURL(run.job.ID, files[index].Name)
		}
	}
	return files
}

func torrentFileURL(id, name string) string {
	return "/api/v1/torrents/" + url.PathEscape(id) + "/file?name=" + url.QueryEscape(name)
}

func peersOf(run *jobRun) []Peer {
	tor := run.torrent()
	if tor == nil {
		return []Peer{}
	}
	conns := tor.PeerConns()
	peers := make([]Peer, 0, len(conns))
	pieces := 0
	if tor.Info() != nil {
		pieces = tor.NumPieces()
	}
	for _, conn := range conns {
		peer := Peer{Address: conn.RemoteAddr.String(), Direction: "outgoing", Client: clientName(conn)}
		if conn.Discovery == torrent.PeerSourceIncoming {
			peer.Direction = "incoming"
		}
		if pieces > 0 {
			peer.Progress = float64(conn.PeerPieces().GetCardinality()) / float64(pieces)
		}
		peers = append(peers, peer)
	}
	return peers
}

func clientName(conn *torrent.PeerConn) string {
	if value, ok := conn.PeerClientName.Load().(string); ok {
		return sanitizeDisplayName(value)
	}
	return ""
}

func ratioOf(uploaded, downloaded int64) float64 {
	if downloaded <= 0 {
		return 0
	}
	value := float64(uploaded) / float64(downloaded)
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0
	}
	return value
}

// markPauseIntent records a pause before any status write so a concurrently attached torrent cannot miss it.
func (s *Service) markPauseIntent(id string, paused bool) {
	s.intentsMu.Lock()
	defer s.intentsMu.Unlock()
	intent := s.intents[id]
	intent.paused = paused
	s.storeIntentLocked(id, intent)
}

func (s *Service) pausedByIntent(id string) bool {
	s.intentsMu.Lock()
	defer s.intentsMu.Unlock()
	return s.intents[id].paused
}

// markCancelIntent records a cancel before any status write so a concurrent attach cannot revive the job.
func (s *Service) markCancelIntent(id string, cancelled bool) {
	s.intentsMu.Lock()
	defer s.intentsMu.Unlock()
	intent := s.intents[id]
	intent.cancelled = cancelled
	s.storeIntentLocked(id, intent)
}

func (s *Service) cancelledByIntent(id string) bool {
	s.intentsMu.Lock()
	defer s.intentsMu.Unlock()
	return s.intents[id].cancelled
}

func (s *Service) storeIntentLocked(id string, intent jobIntent) {
	if intent == (jobIntent{}) {
		delete(s.intents, id)
		return
	}
	s.intents[id] = intent
}

func (s *Service) Pause(ctx context.Context, id string) (Job, error) {
	if s.cancelledByIntent(id) {
		return Job{}, ErrConflict
	}
	if run := s.run(id); run != nil {
		if !s.pauseRun(ctx, run) {
			return Job{}, ErrConflict
		}
		return s.liveJob(run.snapshot().Job), nil
	}
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return Job{}, err
	}
	switch job.Status {
	case statusPaused:
		return job, nil
	case statusQueued, statusMetadata, statusChecking, statusDownloading, statusSeeding:
	default:
		return Job{}, ErrConflict
	}
	s.markPauseIntent(id, true)
	if err := s.setStatusWhen(ctx, s.pool, id, statusPaused, "", statusCancelled); err != nil {
		s.markPauseIntent(id, false)
		return Job{}, err
	}
	job.Status, job.Error = statusPaused, ""
	// The engine may have attached the torrent while the pause was saved.
	if run := s.run(id); run != nil {
		s.pauseRun(ctx, run)
	}
	return job, nil
}

// pauseRun pauses an attached run; a cancellation that won the race is never overwritten.
func (s *Service) pauseRun(ctx context.Context, run *jobRun) bool {
	run.mu.Lock()
	if run.cancelled {
		run.mu.Unlock()
		return false
	}
	run.paused = true
	run.job.Status, run.job.Error, run.job.UpdatedAt = statusPaused, "", time.Now()
	run.mu.Unlock()
	if tor := run.torrent(); tor != nil {
		tor.DisallowDataDownload()
		tor.DisallowDataUpload()
	}
	if err := s.setStatusWhen(ctx, s.pool, run.job.ID, statusPaused, "", statusCancelled); err != nil {
		s.setEngineError(err.Error())
	}
	return true
}

func (s *Service) Resume(ctx context.Context, id string) (Job, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == statusCancelled || s.cancelledByIntent(id) {
		return s.resumeCancelled(ctx, job)
	}
	retried := s.retryProcessing(ctx, id)
	if run := s.run(id); run != nil {
		run.mu.Lock()
		paused := run.paused
		run.mu.Unlock()
		if !paused && job.Status != statusPaused {
			if retried {
				// The extraction retry was accepted even though the transfer is still seeding.
				current := s.liveJob(run.snapshot().Job)
				current.Processing = &Processing{State: processPending}
				return current, nil
			}
			return Job{}, ErrConflict
		}
		s.markPauseIntent(id, false)
		run.mu.Lock()
		run.paused = false
		run.mu.Unlock()
		if tor := run.torrent(); tor != nil {
			tor.AllowDataDownload()
			tor.AllowDataUpload()
		}
		s.setRunStatus(ctx, run, statusDownloading, "")
		return s.liveJob(run.snapshot().Job), nil
	}
	switch job.Status {
	case statusPaused, statusCompleted, statusFailed:
	case statusQueued:
		return job, nil
	default:
		return Job{}, ErrConflict
	}
	s.markPauseIntent(id, false)
	if job.Status == statusFailed {
		if _, err := s.pool.Exec(ctx, `UPDATE torrent_jobs SET error = '' WHERE id = $1`, id); err != nil {
			return Job{}, storeError("resume job", err)
		}
		job.Error = ""
	}
	if err := s.queueUnlessCancelled(ctx, s.pool, id); err != nil {
		return Job{}, err
	}
	job.Status = statusQueued
	s.wakeEngine()
	return job, nil
}

// resumeCancelled requeues a cancelled job; cancelled jobs never restart without this explicit request.
func (s *Service) resumeCancelled(ctx context.Context, job Job) (Job, error) {
	s.markCancelIntent(job.ID, false)
	s.markPauseIntent(job.ID, false)
	s.retryProcessing(ctx, job.ID)
	// The guard in startRun keeps a cancel that races this requeue from being lost.
	if err := s.setStatus(ctx, s.pool, job.ID, statusQueued, ""); err != nil {
		return Job{}, err
	}
	job.Status, job.Error = statusQueued, ""
	s.wakeEngine()
	return job, nil
}

// Cancel stops a job's transfer and extraction without deleting its files; resume is the only way back.
func (s *Service) Cancel(ctx context.Context, id string) (Job, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == statusCancelled || s.cancelledByIntent(id) {
		return job, nil
	}
	s.markCancelIntent(id, true)
	s.markPauseIntent(id, false)
	s.cancelProcessing(id)
	if run := s.run(id); run != nil {
		run.mu.Lock()
		run.cancelled = true
		run.mu.Unlock()
		if tor := run.torrent(); tor != nil {
			tor.DisallowDataDownload()
			tor.DisallowDataUpload()
		}
		s.persistRunNow(run)
		s.dropRun(run)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.setStatus(writeCtx, s.pool, id, statusCancelled, ""); err != nil {
		return Job{}, err
	}
	job.Status, job.Error = statusCancelled, ""
	return job, nil
}

// Recheck clears verified pieces and requeues the job so the engine hashes the data again.
func (s *Service) Recheck(ctx context.Context, id string) (Job, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == statusCancelled {
		return Job{}, ErrConflict
	}
	if run := s.run(id); run != nil {
		s.dropRun(run)
	}
	// A cancel that lands mid-recheck must win over the requeue below.
	tag, err := s.pool.Exec(ctx, `UPDATE torrent_jobs SET pieces_done = 0, piece_bits = '', status = $2,
		error = '', seeding_started_at = NULL, updated_at = now() WHERE id = $1 AND status <> $3`,
		id, statusQueued, statusCancelled)
	if err != nil {
		return Job{}, storeError("recheck job", err)
	}
	if tag.RowsAffected() == 0 {
		return Job{}, ErrConflict
	}
	job.Status, job.PiecesDone = statusQueued, 0
	s.markPauseIntent(id, false)
	s.wakeEngine()
	return job, nil
}

func (s *Service) UpdateLimits(ctx context.Context, id string, ratio float64, minutes int) (Job, error) {
	if ratio < 0 || ratio > 1000 || math.IsNaN(ratio) || minutes < 0 || minutes > 525_600 {
		return Job{}, ErrInvalid
	}
	job, err := s.saveSeedLimits(ctx, s.pool, id, ratio, minutes)
	if err != nil {
		return Job{}, err
	}
	if run := s.run(id); run != nil {
		run.mu.Lock()
		run.job.SeedRatioLimit, run.job.SeedTimeLimitMinutes = ratio, minutes
		run.mu.Unlock()
	}
	return s.liveJob(job), nil
}

// Delete removes a job; downloaded files are kept unless removeFiles is set.
func (s *Service) Delete(ctx context.Context, id string, removeFiles bool) (bool, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return false, err
	}
	if run := s.run(id); run != nil {
		s.dropRun(run)
	}
	s.markPauseIntent(id, false)
	s.markCancelIntent(id, false)
	removed := false
	if removeFiles {
		dir, err := s.dataDir(job.InfoHash)
		if err != nil {
			return false, err
		}
		// Keep the queue honest if deleting the payload fails and the row stays.
		_ = s.setStatus(ctx, s.pool, id, statusPaused, "")
		if err := s.removeDataDir(dir); err != nil {
			return false, err
		}
		if err := s.removeProcessedDir(job.InfoHash); err != nil {
			return false, err
		}
		removed = true
	}
	if err := s.deleteJob(ctx, s.pool, id); err != nil {
		return removed, err
	}
	return removed, nil
}

// AddPeers attaches explicit peer addresses to a running job; used by tests and advanced setups.
func (s *Service) AddPeers(ctx context.Context, id string, addrs []string) error {
	return s.addPeers(ctx, id, addrs)
}

func (s *Service) Health(ctx context.Context) Health {
	health := Health{OK: true, Started: s.started.Load()}
	settings := s.Settings()
	health.DHTEnabled, health.PEXEnabled = settings.DHTEnabled, settings.PEXEnabled
	health.ListenPort = s.livePort()
	health.ActiveJobs = len(s.runList())
	if queued, err := s.countJobs(ctx, s.pool, statusQueued); err == nil {
		health.QueuedJobs = queued
	} else {
		health.OK = false
	}
	if failed, err := s.countJobs(ctx, s.pool, statusFailed); err == nil {
		health.FailedJobs = failed
	} else {
		health.OK = false
	}
	if extracting, err := s.countProcessing(ctx); err == nil {
		health.ProcessingJobs = extracting
	} else {
		health.OK = false
	}
	if message := s.engineError(); message != "" {
		health.OK, health.Error = false, message
	}
	return health
}
