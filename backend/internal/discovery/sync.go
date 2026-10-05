package discovery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

// Start runs the recovery and library-tracking loop; Close stops it.
func (s *Service) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancelMu.Lock()
		s.cancel = cancel
		s.runCtx = runCtx
		s.cancelMu.Unlock()
		s.workers.Go(func() {
			ticker := time.NewTicker(syncInterval)
			defer ticker.Stop()
			for {
				if _, err := s.Sync(runCtx); err != nil && runCtx.Err() == nil && !errors.Is(err, ErrConflict) {
					slog.Warn("discovery synchronization needs attention", "error", err)
				}
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	})
}

func (s *Service) Close() {
	s.cancelMu.Lock()
	cancel := s.cancel
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.workers.Wait()
}

func (s *Service) baseContext() context.Context {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// Sync resumes interrupted approvals and refreshes delivery state for approved requests.
func (s *Service) Sync(ctx context.Context) (SyncResult, error) {
	if !s.syncMu.TryLock() {
		return SyncResult{}, ErrConflict
	}
	defer s.syncMu.Unlock()
	result := SyncResult{}
	resumes, err := s.approvalsToResume(ctx, approvalRetryDelay, approvalResumeSize)
	if err != nil {
		return result, err
	}
	for _, request := range resumes {
		if ctx.Err() != nil {
			break
		}
		if request.Delivery.Attempts >= approvalAttemptCap {
			if err := s.revertApproval(ctx, request); err != nil {
				return result, err
			}
			result.Reverted++
			continue
		}
		if _, err := s.finishApproval(ctx, request.ID); err != nil {
			result.Failed++
			continue
		}
		result.Approved++
	}
	tracked, err := s.trackedRequests(ctx, trackedBatchSize)
	if err != nil {
		return result, err
	}
	for _, request := range tracked {
		if ctx.Err() != nil {
			break
		}
		updated, err := s.refreshDelivery(ctx, request.ID)
		if err != nil {
			continue
		}
		if updated.UpdatedAt.After(request.UpdatedAt) {
			result.Updated++
		}
		if updated.Status == StatusAvailable && request.Status != StatusAvailable {
			result.Available++
		}
	}
	if err := s.markTask(ctx, "sync"); err != nil {
		return result, err
	}
	return result, nil
}

// refreshDelivery maps the owning module's state onto the request; it reports whether the request moved to available.
func (s *Service) refreshDelivery(ctx context.Context, id string) (Request, error) {
	request, err := s.requestByID(ctx, s.pool, id)
	if err != nil {
		return Request{}, err
	}
	if request.Status != StatusApproved && request.Status != StatusAvailable {
		return request, nil
	}
	previousPhase := request.Delivery.Phase
	delivery, available, err := s.deliveryState(ctx, request)
	if err != nil {
		return Request{}, err
	}
	if available && request.Status != StatusAvailable {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return Request{}, errors.New("discovery: request state could not be saved")
		}
		defer tx.Rollback(ctx)
		current, err := s.lockRequest(ctx, tx, id)
		if err != nil {
			return Request{}, err
		}
		if current.Status != StatusApproved {
			if err := tx.Commit(ctx); err != nil {
				return Request{}, errors.New("discovery: request state could not be saved")
			}
			return current, nil
		}
		delivery.Phase = PhaseAvailable
		delivery.Available = true
		delivery.Message = "In the library"
		if err := s.setDecision(ctx, tx, current.ID, StatusAvailable, current.DecidedBy, current.DecisionNote, current.LibraryID, delivery); err != nil {
			return Request{}, err
		}
		if err := s.addEvent(ctx, tx, current.ID, "system", "available", StatusApproved, StatusAvailable, "The media is available in the library"); err != nil {
			return Request{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Request{}, errors.New("discovery: request state could not be saved")
		}
		updated, err := s.refresh(ctx, id)
		if err != nil {
			return Request{}, err
		}
		s.notify(ctx, updated)
		return updated, nil
	}
	if !equalDelivery(delivery, request.Delivery) {
		if err := s.setDelivery(ctx, s.pool, id, delivery); err != nil {
			return Request{}, err
		}
		request.Delivery = delivery
		// Report one failure per transition so a failing download is not reported on every pass.
		if delivery.Phase == PhaseFailed && previousPhase != PhaseFailed {
			updated, err := s.refresh(ctx, id)
			if err == nil {
				s.notify(ctx, updated)
			}
		}
	}
	return request, nil
}

func equalDelivery(a, b Delivery) bool {
	left, errA := encodeJSON(a)
	right, errB := encodeJSON(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func (s *Service) deliveryState(ctx context.Context, request Request) (Delivery, bool, error) {
	switch request.MediaType {
	case MediaMovie:
		return s.movieDelivery(ctx, request)
	case MediaTV:
		return s.tvDelivery(ctx, request)
	case MediaMusic:
		return s.musicDelivery(ctx, request)
	default:
		return Delivery{Phase: PhaseUnknown}, false, nil
	}
}

func (s *Service) movieDelivery(ctx context.Context, request Request) (Delivery, bool, error) {
	movie, err := s.movies.Get(ctx, request.LibraryID)
	if err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			return Delivery{Phase: PhaseRemoved, Message: "The library item was removed"}, false, nil
		}
		return Delivery{}, false, errors.New("discovery: the movie library could not be read")
	}
	present := 0
	for _, file := range movie.Files {
		if !file.Missing {
			present++
		}
	}
	if present > 0 {
		return Delivery{Phase: PhaseAvailable, Available: true, Message: "In the library"}, true, nil
	}
	delivery := Delivery{Phase: PhaseSearching}
	switch movie.Status {
	case "downloading":
		delivery.Phase = PhaseDownloading
		delivery.JobID, delivery.Progress = s.movieProgress(ctx, movie.ID)
	case "importing":
		delivery.Phase = PhaseImporting
	case "failed", "import-failed":
		delivery.Phase = PhaseFailed
		delivery.Message = sanitize(movie.Error)
	case "unmonitored":
		delivery.Phase = PhaseUnmonitored
	}
	return delivery, false, nil
}

// movieProgress reports the newest job for a movie without failing when the download state is missing.
func (s *Service) movieProgress(ctx context.Context, movieID string) (string, *float64) {
	if s.movies.Downloads == nil {
		return "", nil
	}
	acquisitions, err := s.movies.Store.Acquisitions(ctx)
	if err != nil {
		return "", nil
	}
	jobID := ""
	for _, acquisition := range acquisitions {
		if acquisition.MovieID != movieID {
			continue
		}
		switch acquisition.Status {
		case "queued", "downloading", "":
			jobID = acquisition.JobID
		}
	}
	if jobID == "" {
		return "", nil
	}
	job, err := s.movies.Downloads.Get(ctx, jobID)
	if err != nil || job.BytesTotal <= 0 {
		return jobID, nil
	}
	progress := float64(job.BytesDone) / float64(job.BytesTotal)
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	return jobID, &progress
}

func (s *Service) tvDelivery(ctx context.Context, request Request) (Delivery, bool, error) {
	series, err := s.tv.Get(ctx, request.LibraryID)
	if err != nil {
		if errors.Is(err, tv.ErrNotFound) {
			return Delivery{Phase: PhaseRemoved, Message: "The library item was removed"}, false, nil
		}
		return Delivery{}, false, errors.New("discovery: the TV library could not be read")
	}
	delivery := Delivery{Phase: PhaseSearching, Done: series.Downloaded, Total: series.Total}
	if series.Total > 0 {
		progress := float64(series.Downloaded) / float64(series.Total)
		delivery.Progress = &progress
	}
	if series.Downloaded > 0 && series.Wanted == 0 {
		return Delivery{Phase: PhaseAvailable, Available: true, Message: "In the library",
			Done: series.Downloaded, Total: series.Total, Progress: delivery.Progress}, true, nil
	}
	switch series.Status {
	case "downloading":
		delivery.Phase = PhaseDownloading
	case "importing":
		delivery.Phase = PhaseImporting
	case "failed", "import-failed":
		delivery.Phase = PhaseFailed
		delivery.Message = sanitize(series.Error)
	case "unmonitored":
		delivery.Phase = PhaseUnmonitored
	}
	return delivery, false, nil
}

func (s *Service) musicDelivery(ctx context.Context, request Request) (Delivery, bool, error) {
	if s.music == nil {
		return Delivery{Phase: PhaseUnknown, Message: "The music library is not configured"}, false, nil
	}
	item, err := s.music.Status(ctx, request.LibraryID)
	if err != nil {
		return Delivery{Phase: PhaseUnknown, Message: "The music library could not be read"}, false, nil
	}
	if item.Available {
		return Delivery{Phase: PhaseAvailable, Available: true, Message: "In the library"}, true, nil
	}
	delivery := Delivery{Phase: PhaseSearching}
	switch strings.ToLower(strings.TrimSpace(item.Status)) {
	case "downloading":
		delivery.Phase = PhaseDownloading
	case "importing":
		delivery.Phase = PhaseImporting
	case "failed":
		delivery.Phase = PhaseFailed
		delivery.Message = sanitize(item.Error)
	}
	return delivery, false, nil
}

// sanitize keeps stored and returned messages bounded and free of control characters.
func sanitize(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > 300 {
		value = strings.TrimSpace(value[:300])
	}
	return value
}

func (s *Service) notify(ctx context.Context, request Request) {
	if s.notifyFn == nil {
		return
	}
	s.notifyFn(ctx, request)
}

// refresh loads one request with display names, for single-request responses and notifications.
func (s *Service) refresh(ctx context.Context, id string) (Request, error) {
	request, err := s.requestByID(ctx, s.pool, id)
	if err != nil {
		return Request{}, err
	}
	s.namesFor(ctx, newNameResolver(s.userNameFn), &request)
	return request, nil
}
