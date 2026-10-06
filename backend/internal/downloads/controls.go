package downloads

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
)

const (
	pauseReasonManual = "manual"
	policyPoll        = time.Second
)

// InvalidPolicyError matches ErrInvalid while carrying the policy package's user-facing reason.
type InvalidPolicyError string

func (e InvalidPolicyError) Error() string        { return string(e) }
func (e InvalidPolicyError) Is(target error) bool { return target == ErrInvalid }

// jobControl tracks one job's live cancellation and the user intent recorded for it.
type jobControl struct {
	id string

	mu     sync.Mutex
	cancel context.CancelFunc
	intent string
}

func (c *jobControl) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *jobControl) intentValue() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.intent
}

func (m *Manager) control(id string) *jobControl {
	m.controlsMu.Lock()
	defer m.controlsMu.Unlock()
	if control := m.controls[id]; control != nil {
		return control
	}
	control := &jobControl{id: id}
	m.controls[id] = control
	return control
}

// noteIntent records a user action so a job claimed later still honors it.
func (m *Manager) noteIntent(id, intent string) *jobControl {
	control := m.control(id)
	control.mu.Lock()
	control.intent = intent
	control.mu.Unlock()
	return control
}

// clearIntent drops a recorded action that no longer applies.
func (m *Manager) clearIntent(control *jobControl) {
	control.mu.Lock()
	control.intent = ""
	idle := control.cancel == nil
	control.mu.Unlock()
	if idle {
		m.forgetControl(control)
	}
}

// dropIntent clears any recorded action for a job without creating a control entry.
func (m *Manager) dropIntent(id string) {
	m.controlsMu.Lock()
	control := m.controls[id]
	m.controlsMu.Unlock()
	if control != nil {
		m.clearIntent(control)
	}
}

func (m *Manager) forgetControl(control *jobControl) {
	m.controlsMu.Lock()
	defer m.controlsMu.Unlock()
	if m.controls[control.id] != control {
		return
	}
	control.mu.Lock()
	idle := control.cancel == nil && control.intent == ""
	control.mu.Unlock()
	if idle {
		delete(m.controls, control.id)
	}
}

// beginJob registers a fresh job context, stopping it immediately when the user already acted.
func (m *Manager) beginJob(ctx context.Context, id string) (context.Context, *jobControl) {
	jobCtx, cancel := context.WithCancel(ctx)
	control := m.control(id)
	control.mu.Lock()
	control.cancel = cancel
	intent := control.intent
	control.mu.Unlock()
	if intent != "" {
		cancel()
	}
	return jobCtx, control
}

func (m *Manager) endJob(control *jobControl) {
	control.mu.Lock()
	control.cancel = nil
	idle := control.intent == ""
	control.mu.Unlock()
	if idle {
		m.forgetControl(control)
	}
}

// settleStop decides a job's state after its worker stopped without failing.
func (m *Manager) settleStop(ctx context.Context, q querier, id string, control *jobControl) error {
	switch control.intentValue() {
	case statusPaused, statusCancelled:
		return nil
	}
	// A policy hold keeps the job queued with its progress intact.
	if err := m.requeueHeld(ctx, q, id); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	return nil
}

// holdActive stops running jobs so a newly paused policy cannot keep transferring.
func (m *Manager) holdActive() {
	m.controlsMu.Lock()
	controls := make([]*jobControl, 0, len(m.controls))
	for _, control := range m.controls {
		controls = append(controls, control)
	}
	m.controlsMu.Unlock()
	for _, control := range controls {
		control.stop()
	}
}

// watchPolicy stops running jobs as soon as the resolved policy pauses them.
func (m *Manager) watchPolicy(ctx context.Context) {
	ticker := time.NewTicker(policyPoll)
	defer ticker.Stop()
	allowed := m.policy.Allowed()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := m.policy.Allowed()
			if allowed && !current {
				m.holdActive()
			}
			allowed = current
		}
	}
}

func (m *Manager) Policy() *transferpolicy.Controller { return m.policy }

func (m *Manager) PolicySnapshot() transferpolicy.Snapshot { return m.policy.Snapshot() }

func policyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, transferpolicy.ErrInvalid) {
		return InvalidPolicyError(strings.TrimPrefix(err.Error(), "transferpolicy: "))
	}
	return err
}

func (m *Manager) UpdatePolicy(ctx context.Context, cfg transferpolicy.Config) (transferpolicy.Snapshot, error) {
	snapshot, err := m.policy.Update(ctx, cfg)
	if err != nil {
		return snapshot, policyError(err)
	}
	if snapshot.Effective.Paused {
		m.holdActive()
	}
	return snapshot, nil
}

// SetPaused switches the manual global pause and stops running jobs when it engages.
func (m *Manager) SetPaused(ctx context.Context, paused bool) (transferpolicy.Snapshot, error) {
	snapshot, err := m.policy.SetPaused(ctx, paused)
	if err != nil {
		return snapshot, policyError(err)
	}
	if snapshot.Effective.Paused {
		m.holdActive()
	}
	return snapshot, nil
}

// Pause stops one job; pausing an already paused job is a no-op.
func (m *Manager) Pause(ctx context.Context, id string) (Job, error) {
	job, err := m.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Protocol == "torrent" {
		source := m.torrentSource()
		if source == nil {
			return Job{}, ErrNotConfigured
		}
		live, err := source.Pause(ctx, id)
		if err != nil {
			return Job{}, err
		}
		return m.saveTorrent(ctx, live, job.ReleaseID)
	}
	control := m.noteIntent(id, statusPaused)
	paused, err := m.pauseJob(ctx, m.pool, id)
	if err != nil {
		m.clearIntent(control)
		return Job{}, err
	}
	control.stop()
	return paused, nil
}

// Resume requeues one paused job; resuming work that is not paused is a no-op.
func (m *Manager) Resume(ctx context.Context, id string) (Job, error) {
	job, err := m.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Protocol == "torrent" {
		source := m.torrentSource()
		if source == nil {
			return Job{}, ErrNotConfigured
		}
		live, err := source.Resume(ctx, id)
		if err != nil {
			return Job{}, err
		}
		return m.saveTorrent(ctx, live, job.ReleaseID)
	}
	control := m.control(id)
	resumed, err := m.resumeJob(ctx, m.pool, id)
	if err != nil {
		return Job{}, err
	}
	m.clearIntent(control)
	return resumed, nil
}

// Cancel stops one job and keeps its files, cache, and history; completed jobs cannot be cancelled.
func (m *Manager) Cancel(ctx context.Context, id string) (Job, error) {
	job, err := m.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Protocol == "torrent" {
		source := m.torrentSource()
		if source == nil {
			return Job{}, ErrNotConfigured
		}
		live, err := source.Cancel(ctx, id)
		if err != nil {
			return Job{}, err
		}
		return m.saveTorrent(ctx, live, job.ReleaseID)
	}
	control := m.noteIntent(id, statusCancelled)
	cancelled, err := m.cancelJob(ctx, m.pool, id)
	if err != nil {
		m.clearIntent(control)
		return Job{}, err
	}
	control.stop()
	return cancelled, nil
}
