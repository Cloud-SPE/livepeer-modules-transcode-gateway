package service

import (
	"sync"
	"time"
)

// Health tracks per-candidate (capability+offering+worker) failure state
// and a cooldown window before the candidate is retried.
type Health struct {
	mu                  sync.Mutex
	state               map[string]*entry
	failureThreshold    int
	cooldown            time.Duration
}

type entry struct {
	consecFailures int
	cooldownUntil  time.Time
}

func NewHealth(threshold int, cooldown time.Duration) *Health {
	if threshold <= 0 {
		threshold = 2
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Health{
		state:            map[string]*entry{},
		failureThreshold: threshold,
		cooldown:         cooldown,
	}
}

// CoolingDown reports whether the candidate is currently being skipped.
func (h *Health) CoolingDown(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.state[key]
	return ok && now.Before(e.cooldownUntil)
}

// RecordFailure increments the failure counter and opens a cooldown
// window if the threshold is crossed. Returns true if a fresh cooldown
// was opened.
func (h *Health) RecordFailure(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.state[key]
	if e == nil {
		e = &entry{}
		h.state[key] = e
	}
	e.consecFailures++
	if e.consecFailures >= h.failureThreshold {
		e.cooldownUntil = now.Add(h.cooldown)
		e.consecFailures = 0
		return true
	}
	return false
}

// RecordSuccess clears the failure counter.
func (h *Health) RecordSuccess(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.state, key)
}

// HealthSnapshotEntry is one row of the operator-facing health view.
type HealthSnapshotEntry struct {
	Key             string
	ConsecFailures  int
	CoolingDown     bool
	CooldownUntil   time.Time
}

// Snapshot returns the current per-candidate health state. Cheap; used
// by the admin /admin/registry/health view.
func (h *Health) Snapshot(now time.Time) []HealthSnapshotEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HealthSnapshotEntry, 0, len(h.state))
	for k, e := range h.state {
		out = append(out, HealthSnapshotEntry{
			Key:            k,
			ConsecFailures: e.consecFailures,
			CoolingDown:    !e.cooldownUntil.IsZero() && now.Before(e.cooldownUntil),
			CooldownUntil:  e.cooldownUntil,
		})
	}
	return out
}

// Thresholds exposes the configured failure threshold + cooldown window
// for operator visibility ("we cool off after N consecutive failures for
// M seconds").
func (h *Health) Thresholds() (failuresToCooldown int, cooldown time.Duration) {
	return h.failureThreshold, h.cooldown
}
