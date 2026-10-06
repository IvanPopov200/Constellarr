package torrents

import (
	"reflect"
	"strings"

	"golang.org/x/time/rate"

	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
)

// Policy is the shared transfer-policy surface the torrent engine reacts to; *transferpolicy.Controller satisfies it.
type Policy interface {
	Allowed() bool
	Limiter() *rate.Limiter
}

// policySnapshot is the optional richer surface used to explain a hold to users.
type policySnapshot interface {
	Snapshot() transferpolicy.Snapshot
}

// policyHoldPrefix marks queued jobs the transfer policy is holding.
const policyHoldPrefix = "paused by the transfer policy"

// usablePolicy rejects nil and typed-nil controllers so a missing policy keeps the standalone engine.
func usablePolicy(policy Policy) bool {
	if policy == nil {
		return false
	}
	value := reflect.ValueOf(policy)
	return value.Kind() != reflect.Pointer || !value.IsNil()
}

func (s *Service) policyAllowed() bool {
	return s.policy == nil || s.policy.Allowed()
}

// holdReason returns the policy's own explanation behind a stable marker.
func (s *Service) holdReason() string {
	if snapshotter, ok := s.policy.(policySnapshot); ok {
		if reason := strings.TrimSpace(snapshotter.Snapshot().Effective.Reason); reason != "" {
			return policyHoldPrefix + ": " + reason
		}
	}
	return policyHoldPrefix
}

// runExemptFromHold reports transfers the policy keeps running: finished seeding and already complete payloads.
func runExemptFromHold(run *jobRun) bool {
	run.mu.Lock()
	completed, status := run.completed, run.job.Status
	run.mu.Unlock()
	if completed || status == statusSeeding {
		return true
	}
	tor := run.torrent()
	return tor != nil && tor.Info() != nil && tor.Complete().Bool()
}
