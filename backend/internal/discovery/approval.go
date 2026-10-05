package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

const (
	approvalTimeout    = 3 * time.Minute
	domainSyncTimeout  = 2 * time.Minute
	approvalRetryDelay = 90
	approvalAttemptCap = 5
	approvalResumeSize = 20
	trackedBatchSize   = 100
	syncInterval       = 30 * time.Second
)

// Approve keeps the request in "approving" until the owning module confirms the add; a failed add is never reported as approved.
func (s *Service) Approve(ctx context.Context, actor, id string, input ApproveInput) (Request, error) {
	note, err := plainText(input.Note, maxNoteRunes, false)
	if err != nil {
		return Request{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, errors.New("discovery: the approval could not be recorded")
	}
	defer tx.Rollback(ctx)
	request, err := s.lockRequest(ctx, tx, id)
	if err != nil {
		return Request{}, err
	}
	switch request.Status {
	case StatusApproved, StatusAvailable:
		// Approving twice is a no-op that keeps the existing library association.
		if err := tx.Commit(ctx); err != nil {
			return Request{}, errors.New("discovery: the approval could not be recorded")
		}
		return request, nil
	case StatusPending, StatusApproving:
	default:
		return Request{}, fmt.Errorf("%w: only pending requests can be approved", ErrConflict)
	}
	state, err := s.approvalState(input, request.Delivery.Approval)
	if err != nil {
		return Request{}, err
	}
	if err := s.validateEdits(ctx, request.MediaType, state); err != nil {
		return Request{}, err
	}
	delivery := request.Delivery
	if request.Status == StatusPending {
		delivery.Attempts = 0
	}
	delivery.Phase = StatusApproving
	delivery.Message = ""
	delivery.Approval = &state
	if err := s.setApproving(ctx, tx, request.ID, actor, note, delivery); err != nil {
		return Request{}, err
	}
	if err := s.addEvent(ctx, tx, request.ID, actor, "approving", request.Status, StatusApproving, note); err != nil {
		return Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, errors.New("discovery: the approval could not be recorded")
	}
	return s.finishApproval(ctx, request.ID)
}

// approvalState keeps earlier edits when a resume only repeats the decision.
func (s *Service) approvalState(input ApproveInput, previous *ApprovalState) (ApprovalState, error) {
	state := ApprovalState{Monitored: true}
	if previous != nil {
		state = *previous
	}
	if input.Monitored != nil {
		state.Monitored = *input.Monitored
	}
	if value := strings.TrimSpace(input.ProfileID); value != "" {
		state.ProfileID = value
	}
	if value := strings.TrimSpace(input.RootID); value != "" {
		state.RootID = value
	}
	if value := strings.ToLower(strings.TrimSpace(input.MonitorMode)); value != "" {
		if !validMonitorMode(value) {
			return ApprovalState{}, fmt.Errorf("%w: monitor mode must be all, future, missing, existing, first, latest, or none", ErrInvalid)
		}
		state.MonitorMode = value
	}
	return state, nil
}

func validMonitorMode(mode string) bool {
	switch mode {
	case "all", "future", "missing", "existing", "first", "latest", "none":
		return true
	default:
		return false
	}
}

// validateEdits checks approver choices against the domain configuration before any state changes.
func (s *Service) validateEdits(ctx context.Context, mediaType string, state ApprovalState) error {
	switch mediaType {
	case MediaMovie, MediaTV:
		if state.ProfileID != "" {
			if err := s.validateProfile(ctx, mediaType, state.ProfileID); err != nil {
				return err
			}
		}
		if state.RootID != "" {
			if err := s.validateRoot(ctx, mediaType, state.RootID); err != nil {
				return err
			}
		}
		if state.MonitorMode != "" && mediaType != MediaTV {
			return fmt.Errorf("%w: a monitor mode applies to TV requests only", ErrInvalid)
		}
	case MediaMusic:
		if state.ProfileID != "" || state.RootID != "" || state.MonitorMode != "" {
			return fmt.Errorf("%w: music requests cannot set a quality profile, root folder, or monitor mode", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) validateProfile(ctx context.Context, mediaType, profileID string) error {
	if mediaType == MediaTV {
		if _, err := s.tv.Shared.Profile(ctx, profileID); err != nil {
			if errors.Is(err, movies.ErrNotFound) {
				return fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
			}
			return err
		}
		return nil
	}
	if _, err := s.movies.Store.Profile(ctx, profileID); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			return fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
		}
		return err
	}
	return nil
}

func (s *Service) validateRoot(ctx context.Context, mediaType, rootID string) error {
	roots := []movies.RootFolder{}
	if mediaType == MediaTV {
		cfg, err := s.tv.Store.Config(ctx)
		if err != nil {
			return err
		}
		roots = cfg.RootFolders
	} else {
		cfg, err := s.movies.Store.Config(ctx)
		if err != nil {
			return err
		}
		roots = cfg.RootFolders
	}
	for _, root := range roots {
		if root.ID == rootID {
			return nil
		}
	}
	return fmt.Errorf("%w: root folder does not exist", ErrInvalid)
}

// finishApproval adds the media through the owning module and records the exact library association.
func (s *Service) finishApproval(ctx context.Context, id string) (Request, error) {
	request, err := s.requestByID(ctx, s.pool, id)
	if err != nil {
		return Request{}, err
	}
	if request.Status != StatusApproving || request.Delivery.Approval == nil {
		return request, nil
	}
	state := *request.Delivery.Approval
	libraryID, err := s.addToLibrary(ctx, request, state)
	if err != nil {
		detail := sanitize(err.Error())
		s.recordApprovalFailure(ctx, request, detail)
		return Request{}, fmt.Errorf("%w: %s", ErrUpstream, detail)
	}
	// The media exists now, so the decision must be recorded even if the caller disconnected.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalTimeout)
	defer cancel()
	tx, err := s.pool.Begin(writeCtx)
	if err != nil {
		return Request{}, errors.New("discovery: the approval could not be recorded")
	}
	defer tx.Rollback(writeCtx)
	current, err := s.lockRequest(writeCtx, tx, id)
	if err != nil {
		return Request{}, err
	}
	if current.Status != StatusApproving {
		// A decision landed while the module was adding the media; keep that decision and note the outcome.
		if err := s.addEvent(writeCtx, tx, current.ID, current.DecidedBy, "library-added", current.Status, current.Status,
			"The media was added to the library"); err != nil {
			return Request{}, err
		}
		if err := tx.Commit(writeCtx); err != nil {
			return Request{}, errors.New("discovery: the approval could not be recorded")
		}
		return s.refresh(ctx, current.ID)
	}
	delivery := current.Delivery
	delivery.Phase = PhaseSearching
	delivery.Message = ""
	delivery.Available = false
	delivery.Approval = &state
	if err := s.setDecision(writeCtx, tx, current.ID, StatusApproved, current.DecidedBy, current.DecisionNote, libraryID, delivery); err != nil {
		return Request{}, err
	}
	if err := s.addEvent(writeCtx, tx, current.ID, current.DecidedBy, "approved", StatusApproving, StatusApproved,
		"The media was added to the library"); err != nil {
		return Request{}, err
	}
	if err := tx.Commit(writeCtx); err != nil {
		return Request{}, errors.New("discovery: the approval could not be recorded")
	}
	updated, err := s.refresh(ctx, id)
	if err != nil {
		return Request{}, err
	}
	s.notify(ctx, updated)
	// Queue the owning module's monitor/search pass; automation already polls, this shortens the wait.
	s.queueDomainSync(current.MediaType)
	if refreshed, err := s.refreshDelivery(ctx, id); err == nil {
		return refreshed, nil
	}
	return updated, nil
}

func (s *Service) addToLibrary(ctx context.Context, request Request, state ApprovalState) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, approvalTimeout)
	defer cancel()
	switch request.MediaType {
	case MediaMovie:
		movie, err := s.movies.Add(ctx, movies.AddInput{
			IMDbID: request.ProviderID,
			Metadata: metadata.Title{
				IMDbID: request.ProviderID, Title: request.Title, Year: request.Year,
				Type: MediaMovie, Poster: request.Poster,
			},
			Monitored: state.Monitored,
			ProfileID: state.ProfileID,
			RootID:    state.RootID,
		})
		if err != nil {
			return "", err
		}
		return movie.ID, nil
	case MediaTV:
		mode := state.MonitorMode
		if mode == "" {
			mode = "all"
		}
		series, err := s.tv.Add(ctx, tv.AddInput{
			IMDbID: request.ProviderID,
			Metadata: metadata.Title{
				IMDbID: request.ProviderID, Title: request.Title, Year: request.Year,
				Type: "series", Poster: request.Poster,
			},
			Monitored: state.Monitored, MonitorMode: mode,
			ProfileID: state.ProfileID, RootID: state.RootID,
		})
		if err != nil {
			return "", err
		}
		return series.ID, nil
	case MediaMusic:
		if s.music == nil {
			return "", fmt.Errorf("%w: music is not configured on this server", ErrNotConfigured)
		}
		item, err := s.music.Add(ctx, MusicAddInput{ID: request.ProviderID, Title: request.Title, Year: request.Year})
		if err != nil {
			if errors.Is(err, ErrNotConfigured) {
				return "", err
			}
			if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
				return "", err
			}
			return "", fmt.Errorf("%w: the music library rejected the add: %s", ErrUpstream, sanitize(err.Error()))
		}
		if id := strings.TrimSpace(item.ID); id != "" {
			return id, nil
		}
		return request.ProviderID, nil
	}
	return "", fmt.Errorf("%w: unknown media type", ErrInvalid)
}

func (s *Service) recordApprovalFailure(ctx context.Context, request Request, detail string) {
	// The caller context may already be expired; the failure still has to be durable.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	delivery := request.Delivery
	delivery.Attempts++
	delivery.Phase = StatusApproving
	delivery.Message = detail
	if err := s.setDelivery(writeCtx, s.pool, request.ID, delivery); err != nil {
		slog.Warn("discovery approval state could not be saved", "request", request.ID, "error", err)
	}
	if err := s.addEvent(writeCtx, s.pool, request.ID, request.DecidedBy, "approve-failed", StatusApproving, StatusApproving, detail); err != nil {
		slog.Warn("discovery approval audit could not be saved", "request", request.ID, "error", err)
	}
}

// revertApproval returns a repeatedly failing approval to the queue so no request stays stuck.
func (s *Service) revertApproval(ctx context.Context, request Request) error {
	message := request.Delivery.Message
	if message == "" {
		message = "the library module could not complete this approval"
	}
	delivery := request.Delivery
	delivery.Phase = PhaseFailed
	delivery.Message = message
	if err := s.setStatus(ctx, s.pool, request.ID, StatusPending, delivery); err != nil {
		return err
	}
	if err := s.addEvent(ctx, s.pool, request.ID, request.DecidedBy, "approve-reverted", StatusApproving, StatusPending, message); err != nil {
		return err
	}
	updated, err := s.refresh(ctx, request.ID)
	if err != nil {
		return err
	}
	s.notify(ctx, updated)
	return nil
}

// queueDomainSync triggers one monitor/search pass in the owning module; overlapping calls collapse.
func (s *Service) queueDomainSync(mediaType string) {
	if !s.queueMu.TryLock() {
		return
	}
	s.workers.Go(func() {
		defer s.queueMu.Unlock()
		ctx, cancel := context.WithTimeout(s.baseContext(), domainSyncTimeout)
		defer cancel()
		switch mediaType {
		case MediaMovie:
			if _, err := s.movies.Sync(ctx, true); err != nil && !errors.Is(err, movies.ErrConflict) {
				slog.Debug("discovery movie search queue skipped", "error", err)
			}
		case MediaTV:
			if _, err := s.tv.Sync(ctx, true); err != nil && !errors.Is(err, tv.ErrConflict) {
				slog.Debug("discovery TV search queue skipped", "error", err)
			}
		case MediaMusic:
			// The music hook opts in through Sync; throttled so a queued pass respects the module's schedule.
			if syncer, ok := s.music.(musicSyncer); ok {
				if err := syncer.Sync(ctx, false); err != nil {
					slog.Debug("discovery music search queue skipped", "error", err)
				}
			}
		}
	})
}
