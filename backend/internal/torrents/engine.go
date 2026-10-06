package torrents

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

var discardLogger = slog.New(slog.DiscardHandler)

func count(value torrent.Count) int64 { return value.Int64() }

const (
	tickInterval     = time.Second
	progressInterval = time.Second
	metadataTimeout  = 30 * time.Minute
	flushFailureMax  = 5
	persistTimeout   = 3 * time.Second
)

// jobRun is one torrent attached to an engine client.
type jobRun struct {
	mu        sync.Mutex
	job       storedJob
	dir       string
	store     *pieceStore
	tor       *torrent.Torrent
	client    *torrent.Client
	private   bool
	paused    bool
	cancelled bool
	completed bool
	saved     bool
	seedingAt time.Time
	seedBase  int64

	metadataDeadline time.Time

	lastProgress   time.Time
	lastHashed     int64
	lastHashChange time.Time
	flushFailures  int

	rateAt                  time.Time
	rateBytes, rateUploaded int64
	downRate, upRate        float64
}

func (r *jobRun) snapshot() storedJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}

// torrent reads the attached torrent; moveRun swaps it for private torrents.
func (r *jobRun) torrent() *torrent.Torrent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tor
}

// refreshMetadataDeadline restarts the metadata wait so a paused run never ages out.
func (r *jobRun) refreshMetadataDeadline(now time.Time) {
	r.mu.Lock()
	r.metadataDeadline = now.Add(metadataTimeout)
	r.mu.Unlock()
}

func (r *jobRun) metadataExpired(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return now.After(r.metadataDeadline)
}

func (s *Service) newClientConfig(private bool, port int) *torrent.ClientConfig {
	settings := s.Settings()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = s.root
	cfg.ListenPort = port
	cfg.NoDefaultPortForwarding = true
	cfg.Seed = true
	cfg.NoDHT = s.testing || private || !settings.DHTEnabled
	cfg.DisablePEX = s.testing || private || !settings.PEXEnabled
	cfg.DisableTrackers = s.testing
	cfg.PeriodicallyAnnounceTorrentsToDht = !cfg.NoDHT
	cfg.DownloadRateLimiter = s.downloadLimiter
	cfg.UploadRateLimiter = s.uploadLimiter
	cfg.Slogger = discardLogger
	if s.testing {
		cfg.ListenHost = torrent.LoopbackListenHost
		cfg.DisableIPv6 = true
		cfg.ListenPort = 0
	}
	return cfg
}

// clientFor returns the client that respects the DHT and PEX rules of the torrent.
func (s *Service) clientFor(private bool) (*torrent.Client, error) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	target := &s.publicClient
	if private {
		target = &s.privateClient
	}
	if *target != nil {
		return *target, nil
	}
	port := s.portFor(private)
	client, err := torrent.NewClient(s.newClientConfig(private, port))
	if err != nil && port != 0 {
		// The neighbouring port was taken; fall back to an ephemeral one.
		client, err = torrent.NewClient(s.newClientConfig(private, 0))
	}
	if err != nil {
		return nil, errors.New("torrents: the BitTorrent client could not start")
	}
	*target = client
	if s.clientErr {
		s.engineErr, s.clientErr = "", false
	}
	return client, nil
}

// private torrents use a dedicated client without DHT or PEX; it takes the next free port.
func (s *Service) portFor(private bool) int {
	base := s.Settings().ListenPort
	if !private || base <= 0 || s.testing {
		return base
	}
	if base < 65535 {
		return base + 1
	}
	return 0
}

func (s *Service) ensureClients() error {
	for _, private := range []bool{false, true} {
		if _, err := s.clientFor(private); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) closeClients() {
	s.engineMu.Lock()
	public, private := s.publicClient, s.privateClient
	s.publicClient, s.privateClient = nil, nil
	s.engineMu.Unlock()
	if public != nil {
		public.Close()
	}
	if private != nil {
		private.Close()
	}
}

func (s *Service) setEngineError(message string) {
	s.engineMu.Lock()
	s.engineErr, s.clientErr = message, false
	s.engineMu.Unlock()
}

func (s *Service) setClientError(message string) {
	s.engineMu.Lock()
	s.engineErr, s.clientErr = message, true
	s.engineMu.Unlock()
}

func (s *Service) engineError() string {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	return s.engineErr
}

func (s *Service) livePort() int {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	if s.publicClient == nil {
		return 0
	}
	return s.publicClient.LocalPort()
}

// applyRuntimeSettings applies transfer limits immediately; port and discovery changes need a restart.
func (s *Service) applyRuntimeSettings(next Settings) {
	// The shared policy owns the download limiter.
	if !s.sharedLimiter {
		s.downloadLimiter.SetLimit(limitFor(next.DownloadLimitKBps))
	}
	s.uploadLimiter.SetLimit(limitFor(next.UploadLimitKBps))
}

func limitFor(kbps int) rate.Limit {
	if kbps <= 0 {
		return rate.Inf
	}
	return rate.Limit(kbps) * 1024
}

func (s *Service) startRun(ctx context.Context, job storedJob) {
	dir, err := s.dataDir(job.InfoHash)
	if err != nil {
		s.failRun(ctx, job.ID, "the torrent info hash is invalid")
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.failRun(ctx, job.ID, "the download directory could not be created")
		return
	}
	if err := checkNoSymlinks(dir); err != nil {
		s.failRun(ctx, job.ID, "the download directory contains a symbolic link")
		return
	}
	// Jobs stored before the private flag was recorded are repaired from their metainfo here.
	if !job.Private && len(job.metainfo) != 0 {
		if stored, err := metainfo.Load(bytes.NewReader(job.metainfo)); err == nil {
			if info, err := stored.UnmarshalInfo(); err == nil && privateFromMetainfo(&info) {
				job.Private = true
				if err := s.setPrivate(ctx, s.pool, job.ID); err != nil {
					s.setEngineError(err.Error())
				}
			}
		}
	}
	// A magnet with trackers resolves metadata without DHT or PEX, so private swarms never leak early.
	initialPrivate := job.Private
	if len(job.metainfo) == 0 {
		initialPrivate = initialPrivate || magnetHasTrackers(job.magnet)
	}
	started := time.Now()
	run := &jobRun{
		job: job, dir: dir, private: initialPrivate, store: newPieceStore(s.pool, job.ID, job.PiecesTotal, job.bits),
		seedBase: job.SeedingElapsed, lastHashChange: started, metadataDeadline: started.Add(metadataTimeout),
	}
	spec, err := s.torrentSpec(run)
	if err != nil {
		s.failRun(ctx, job.ID, err.Error())
		return
	}
	client, err := s.clientFor(initialPrivate)
	if err != nil {
		s.failRun(ctx, job.ID, "the BitTorrent client could not start")
		return
	}
	tor, _, err := client.AddTorrentSpec(spec)
	if err != nil {
		s.failRun(ctx, job.ID, "the torrent could not be added")
		return
	}
	run.tor, run.client = tor, client
	infoKnown := tor.Info() != nil
	if infoKnown {
		run.tor.DownloadAll()
		run.tor.AllowDataUpload()
	}
	s.runsMu.Lock()
	s.runs[job.ID] = run
	s.runsMu.Unlock()
	// A pause, cancel or policy hold may have arrived while the torrent was being attached.
	run.paused = s.pausedByIntent(job.ID)
	run.cancelled = s.cancelledByIntent(job.ID)
	if fresh, err := s.jobByID(ctx, s.pool, job.ID); err == nil {
		switch fresh.Status {
		case statusPaused:
			run.paused = true
		case statusCancelled:
			run.cancelled = true
		}
	}
	if run.paused {
		run.tor.DisallowDataDownload()
		run.tor.DisallowDataUpload()
	}
	if run.cancelled {
		s.dropRun(run)
		if s.cancelledByIntent(job.ID) {
			s.persistCancelled(ctx, job.ID)
		}
		return
	}
	// A hold that started during the attach must not fetch metadata or data.
	if !run.paused && !s.policyAllowed() && !(infoKnown && tor.Complete().Bool()) {
		s.dropRun(run)
		if err := s.setQueuedState(ctx, s.pool, job.ID, s.holdReason()); err != nil {
			s.setEngineError(err.Error())
		}
		return
	}
	status := statusDownloading
	switch {
	case run.paused:
		status = statusPaused
	case !infoKnown:
		status = statusMetadata
	case directoryHasData(dir):
		status = statusChecking
	}
	if run.paused {
		s.setRunStatus(ctx, run, statusPaused, "")
	} else {
		s.setEngineStatus(ctx, run, status, "")
	}
}

func (s *Service) torrentSpec(run *jobRun) (*torrent.TorrentSpec, error) {
	storageImpl := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   run.dir,
		FilePathMaker:   func(opts storage.FilePathMakerOpts) string { return safeFilePath(opts.Info, *opts.File) },
		PieceCompletion: run.store,
		UsePartFiles:    generics.Some(false),
		Logger:          discardLogger,
	})
	var spec *torrent.TorrentSpec
	if len(run.job.metainfo) != 0 {
		meta, err := metainfo.Load(bytes.NewReader(run.job.metainfo))
		if err != nil {
			return nil, errors.New("torrents: the stored torrent metadata is invalid")
		}
		info, err := meta.UnmarshalInfo()
		if err != nil {
			return nil, errors.New("torrents: the stored torrent metadata is invalid")
		}
		if _, err := torrentFiles(&info); err != nil {
			return nil, err
		}
		spec = torrent.TorrentSpecFromMetaInfo(meta)
		spec.DisplayName = firstNonEmpty(run.job.Name, info.BestName())
	} else {
		parsed, err := torrent.TorrentSpecFromMagnetUri(run.job.magnet)
		if err != nil {
			return nil, invalidMagnet()
		}
		spec = parsed
		spec.DisplayName = firstNonEmpty(run.job.Title, run.job.Name)
	}
	spec.AddTorrentOpts.Storage = storageImpl
	return spec, nil
}

func directoryHasData(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func (s *Service) failRun(ctx context.Context, id, message string) {
	if err := s.markFailed(ctx, s.pool, id, message); err != nil {
		s.setEngineError(err.Error())
	}
}

// persistCancelled records a cancel that arrived while the torrent was attaching.
func (s *Service) persistCancelled(ctx context.Context, id string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.setStatus(writeCtx, s.pool, id, statusCancelled, ""); err != nil {
		s.setEngineError(err.Error())
	}
}

// dropRun detaches a job from the client and persists its resume state.
func (s *Service) dropRun(run *jobRun) {
	s.runsMu.Lock()
	if current, ok := s.runs[run.job.ID]; ok && current == run {
		delete(s.runs, run.job.ID)
	}
	s.runsMu.Unlock()
	if err := run.store.Close(); err != nil {
		s.setEngineError(safeRunError(err))
	}
	if tor := run.torrent(); tor != nil {
		tor.Drop()
	}
}

func (s *Service) run(id string) *jobRun {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	return s.runs[id]
}

func (s *Service) runList() []*jobRun {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	runs := make([]*jobRun, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, run)
	}
	return runs
}

// resumeMetadata validates and persists metadata once a magnet has resolved it.
func (s *Service) resumeMetadata(ctx context.Context, run *jobRun) error {
	info := run.tor.Info()
	if info == nil {
		return nil
	}
	files, err := torrentFiles(info)
	if err != nil {
		return err
	}
	hash := run.tor.InfoHash().HexString()
	if hash != run.job.InfoHash {
		return errors.New("torrents: the torrent metadata does not match the requested info hash")
	}
	var buf bytes.Buffer
	meta := run.tor.Metainfo()
	if err := meta.Write(&buf); err != nil {
		return errors.New("torrents: the torrent metadata could not be stored")
	}
	run.store.setTotal(info.NumPieces())
	// An explicit feed or torrent-file privacy signal outlives metadata without a private flag.
	run.mu.Lock()
	private := run.job.Private || privateFromMetainfo(info)
	run.mu.Unlock()
	if err := s.saveMetadata(ctx, s.pool, run.job.ID, info.BestName(), info.TotalLength(),
		info.NumPieces(), files, buf.Bytes(), private); err != nil {
		return err
	}
	run.mu.Lock()
	run.job.Name = info.BestName()
	run.job.BytesTotal = info.TotalLength()
	run.job.PiecesTotal = info.NumPieces()
	run.job.Files = files
	run.job.metainfo = buf.Bytes()
	run.job.Private = private
	run.saved = true
	run.mu.Unlock()
	run.tor.DownloadAll()
	run.tor.AllowDataUpload()
	return nil
}

// moveRun re-attaches a torrent to the client that matches its private flag.
func (s *Service) moveRun(run *jobRun) error {
	if run.private == run.job.Private {
		return nil
	}
	client, err := s.clientFor(run.job.Private)
	if err != nil {
		return err
	}
	run.tor.Drop()
	spec, err := s.torrentSpec(run)
	if err != nil {
		return err
	}
	tor, _, err := client.AddTorrentSpec(spec)
	if err != nil {
		return errors.New("torrents: the torrent could not be moved to its own client")
	}
	run.tor, run.client, run.private = tor, client, run.job.Private
	run.tor.DownloadAll()
	run.tor.AllowDataUpload()
	return nil
}

func (s *Service) tick(ctx context.Context) {
	if err := s.ensureClients(); err != nil {
		s.setClientError(err.Error())
		return
	}
	held := !s.policyAllowed()
	s.syncQueuedHoldReason(ctx, held)
	if !held {
		if err := s.startQueued(ctx); err != nil {
			s.setEngineError(err.Error())
			return
		}
	}
	for _, run := range s.runList() {
		s.advance(ctx, run)
	}
	s.scheduleProcessing(ctx)
}

// syncQueuedHoldReason keeps policy holds visible on waiting jobs; the statements only touch changed rows.
func (s *Service) syncQueuedHoldReason(ctx context.Context, held bool) {
	if s.policy == nil {
		return
	}
	var err error
	if held {
		_, err = s.pool.Exec(ctx, `UPDATE torrent_jobs SET error = $1, updated_at = now()
			WHERE status = $2 AND error NOT LIKE $3`, s.holdReason(), statusQueued, policyHoldPrefix+"%")
	} else {
		_, err = s.pool.Exec(ctx, `UPDATE torrent_jobs SET error = '', updated_at = now()
			WHERE status = $1 AND error LIKE $2`, statusQueued, policyHoldPrefix+"%")
	}
	if err != nil {
		s.setEngineError(storeError("update queued jobs", err).Error())
	}
}

func (s *Service) startQueued(ctx context.Context) error {
	settings := s.Settings()
	active := s.activeRunCount()
	if active >= settings.MaxActiveJobs {
		return nil
	}
	jobs, err := s.listJobs(ctx, s.pool, statusQueued)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if active >= settings.MaxActiveJobs {
			return nil
		}
		if s.run(job.ID) != nil {
			continue
		}
		stored, err := s.storedByID(ctx, s.pool, job.ID)
		if err != nil {
			continue
		}
		s.startRun(ctx, stored)
		active++
	}
	return nil
}

// activeRunCount counts transferring runs so paused and cancelled jobs cannot block queue capacity.
func (s *Service) activeRunCount() int {
	active := 0
	for _, run := range s.runList() {
		run.mu.Lock()
		busy := !run.paused && !run.cancelled
		run.mu.Unlock()
		if busy {
			active++
		}
	}
	return active
}

func (s *Service) advance(ctx context.Context, run *jobRun) {
	stats := run.tor.Stats()
	bytesDone := run.tor.BytesCompleted()
	uploaded := count(stats.BytesWrittenData)
	updateRates(run, bytesDone, uploaded)
	run.mu.Lock()
	paused, completed, saved, cancelled := run.paused, run.completed, run.saved, run.cancelled
	run.mu.Unlock()
	if cancelled {
		return
	}
	if paused {
		// A manual pause still waits for metadata but never transfers data.
		run.tor.DisallowDataDownload()
		run.tor.DisallowDataUpload()
		run.refreshMetadataDeadline(time.Now())
		return
	}
	held := !s.policyAllowed()
	if held && !runExemptFromHold(run) {
		s.holdRun(run)
		return
	}
	if run.tor.Info() == nil {
		if run.metadataExpired(time.Now()) {
			s.failRun(ctx, run.job.ID, "no peers supplied the torrent metadata")
			s.dropRun(run)
		}
		return
	}
	if !saved {
		if err := s.resumeMetadata(ctx, run); err != nil {
			s.failRun(ctx, run.job.ID, safeRunError(err))
			s.dropRun(run)
			return
		}
		if err := s.moveRun(run); err != nil {
			s.failRun(ctx, run.job.ID, safeRunError(err))
			s.dropRun(run)
			return
		}
		s.setEngineStatus(ctx, run, statusChecking, "")
		return
	}
	switch {
	case completed:
		return
	case run.tor.Complete().Bool():
		run.mu.Lock()
		ratioLimit, timeLimit := run.job.SeedRatioLimit, run.job.SeedTimeLimitMinutes
		run.mu.Unlock()
		if err := s.finishDownload(ctx, run, time.Now(), uploaded, ratioLimit, timeLimit); err != nil {
			s.setEngineError(err.Error())
		}
	case held:
		// Complete seeding keeps uploading under the existing cap while the policy holds downloads.
		run.tor.DisallowDataDownload()
	default:
		run.tor.AllowDataDownload()
	}
	if err := s.persistProgress(ctx, run, bytesDone, uploaded); err != nil {
		run.flushFailures++
		if run.flushFailures >= flushFailureMax {
			s.failRun(ctx, run.job.ID, "the transfer progress could not be saved")
			s.dropRun(run)
		}
		return
	}
	run.flushFailures = 0
	s.settleStatus(ctx, run)
}

// holdRun detaches a transfer stopped by the shared policy and keeps it queued for an automatic restart.
func (s *Service) holdRun(run *jobRun) {
	s.persistRunNow(run)
	s.dropRun(run)
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	if err := s.setQueuedState(ctx, s.pool, run.job.ID, s.holdReason()); err != nil {
		s.setEngineError(err.Error())
	}
}

func (s *Service) persistRunNow(run *jobRun) {
	tor := run.torrent()
	if tor == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	piecesDone, bits := run.store.completed()
	uploaded := count(tor.Stats().BytesWrittenData)
	if err := s.saveProgress(ctx, s.pool, run.job.ID, tor.BytesCompleted(), uploaded, piecesDone, bits); err != nil {
		s.setEngineError(safeRunError(err))
	}
}

// finishDownload moves a completed transfer into seeding or completes it under the seed policy.
func (s *Service) finishDownload(ctx context.Context, run *jobRun, now time.Time, uploaded int64, seedRatio float64, seedMinutes int) error {
	ratioLimit, timeLimit := seedPolicy(seedRatio, seedMinutes)
	if ratioLimit <= 0 && timeLimit <= 0 {
		run.tor.DisallowDataUpload()
		run.mu.Lock()
		run.completed = true
		run.mu.Unlock()
		if err := s.markCompleted(ctx, s.pool, run.job.ID, run.seedBase); err != nil {
			return err
		}
		s.dropRun(run)
		return nil
	}
	seeding := !run.seedingAt.IsZero()
	if !seeding {
		run.seedingAt = now
		run.mu.Lock()
		run.job.Status = statusSeeding
		run.mu.Unlock()
		return s.markSeeding(ctx, s.pool, run.job.ID, now)
	}
	elapsed := run.seedBase + int64(now.Sub(run.seedingAt).Seconds())
	ratio := 0.0
	if run.tor.Length() > 0 {
		ratio = float64(uploaded) / float64(run.tor.Length())
	}
	reached := (ratioLimit > 0 && ratio >= ratioLimit) || (timeLimit > 0 && elapsed >= int64(timeLimit)*60)
	if !reached {
		run.tor.AllowDataUpload()
		return nil
	}
	run.tor.DisallowDataUpload()
	run.mu.Lock()
	run.completed = true
	run.mu.Unlock()
	if err := s.markCompleted(ctx, s.pool, run.job.ID, elapsed); err != nil {
		return err
	}
	s.dropRun(run)
	return nil
}

// settleStatus reports downloading once an initial recheck has finished hashing existing data.
func (s *Service) settleStatus(ctx context.Context, run *jobRun) {
	run.mu.Lock()
	status := run.job.Status
	run.mu.Unlock()
	if status != statusChecking {
		return
	}
	hashed := count(run.tor.Stats().BytesHashed)
	if hashed != run.lastHashed {
		run.lastHashed, run.lastHashChange = hashed, time.Now()
		return
	}
	if time.Since(run.lastHashChange) < 2*tickInterval {
		return
	}
	s.setEngineStatus(ctx, run, statusDownloading, "")
}

func (s *Service) persistProgress(ctx context.Context, run *jobRun, bytesDone, uploaded int64) error {
	if time.Since(run.lastProgress) < progressInterval {
		return nil
	}
	run.lastProgress = time.Now()
	piecesDone, bits := run.store.completed()
	run.mu.Lock()
	run.job.BytesDone, run.job.Uploaded, run.job.PiecesDone = bytesDone, uploaded, piecesDone
	run.job.UpdatedAt = time.Now()
	run.mu.Unlock()
	return s.saveProgress(ctx, s.pool, run.job.ID, bytesDone, uploaded, piecesDone, bits)
}

// setEngineStatus records an engine-driven transition; a concurrent pause or cancel always wins.
func (s *Service) setEngineStatus(ctx context.Context, run *jobRun, status, message string) {
	run.mu.Lock()
	if run.cancelled || (run.paused && status != statusPaused) {
		run.mu.Unlock()
		return
	}
	run.job.Status, run.job.Error, run.job.UpdatedAt = status, message, time.Now()
	run.mu.Unlock()
	if err := s.setStatusWhen(ctx, s.pool, run.job.ID, status, message, statusPaused, statusCancelled); err != nil {
		s.setEngineError(err.Error())
	}
}

// setRunStatus records a user-driven transition; a cancellation always wins.
func (s *Service) setRunStatus(ctx context.Context, run *jobRun, status, message string) {
	run.mu.Lock()
	if run.cancelled {
		run.mu.Unlock()
		return
	}
	run.job.Status, run.job.Error = status, message
	run.job.UpdatedAt = time.Now()
	run.mu.Unlock()
	if err := s.setStatusWhen(ctx, s.pool, run.job.ID, status, message, statusCancelled); err != nil {
		s.setEngineError(err.Error())
	}
}

func updateRates(run *jobRun, bytesDone, uploaded int64) {
	now := time.Now()
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.rateAt.IsZero() {
		run.rateAt, run.rateBytes, run.rateUploaded = now, bytesDone, uploaded
		return
	}
	elapsed := now.Sub(run.rateAt).Seconds()
	if elapsed < 0.5 {
		return
	}
	run.downRate = float64(max(bytesDone-run.rateBytes, 0)) / elapsed
	run.upRate = float64(max(uploaded-run.rateUploaded, 0)) / elapsed
	run.rateAt, run.rateBytes, run.rateUploaded = now, bytesDone, uploaded
}

func safeRunError(err error) string {
	message := err.Error()
	if message == "" {
		message = "torrent failed"
	}
	runes := []rune(message)
	if len(runes) > maxNameRunes {
		message = string(runes[:maxNameRunes])
	}
	return message
}

func (s *Service) addPeers(ctx context.Context, id string, addrs []string) error {
	run := s.run(id)
	if run == nil {
		return ErrConflict
	}
	tor := run.torrent()
	if tor == nil {
		return ErrConflict
	}
	peers := make([]torrent.PeerInfo, 0, len(addrs))
	for _, raw := range addrs {
		addr, err := netip.ParseAddrPort(raw)
		if err != nil {
			return ErrInvalid
		}
		peers = append(peers, torrent.PeerInfo{Addr: addr})
	}
	if len(peers) == 0 {
		return ErrInvalid
	}
	tor.AddPeers(peers)
	return nil
}
