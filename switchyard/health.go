package switchyard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// healthPolicy is the resolved (merged) health-check configuration for one
// backend. The zero value disables both detectors.
type healthPolicy struct {
	passive passivePolicy
	active  activePolicy
}

// passivePolicy ejects a backend after too many failures from real traffic.
type passivePolicy struct {
	enabled  bool
	statuses map[int]bool
	count    int
	window   time.Duration
	cooldown time.Duration
}

// activePolicy probes a backend's health endpoint on an interval.
type activePolicy struct {
	enabled        bool
	path           string
	method         string
	interval       time.Duration
	timeout        time.Duration
	expectedStatus int
	retries        int
	unhealthyThr   int
	healthyThr     int
	host           string
}

// healthState is the per-backend mutable bookkeeping for the passive detector:
// a ring of the last `count` failure timestamps plus a pending-recovery guard.
// Guarded by mu (compound state, so a mutex rather than atomics).
type healthState struct {
	mu         sync.Mutex
	fails      []time.Time // ring sized to passive.count (nil when passive disabled)
	idx        int
	n          int // entries recorded, capped at len(fails)
	recovering bool
}

// resolveHealth merges a backend's HealthConfig (backend) over the top-level one
// (global) field by field — each set field wins, unset inherits, else the
// built-in default — mirroring resolveRetry.
func resolveHealth(global, backend *HealthConfig) healthPolicy {
	p := healthPolicy{
		passive: passivePolicy{
			count:    defaultHealthPassiveCount,
			window:   defaultHealthPassiveWindow,
			cooldown: defaultHealthPassiveCooldown,
		},
		active: activePolicy{
			method:         defaultHealthActiveMethod,
			interval:       defaultHealthActiveInterval,
			timeout:        defaultHealthActiveTimeout,
			expectedStatus: defaultHealthActiveStatus,
			retries:        defaultHealthActiveRetries,
			unhealthyThr:   defaultHealthUnhealthyThresh,
			healthyThr:     defaultHealthHealthyThresh,
		},
	}
	var statuses []int
	var seenPassive, seenActive bool
	apply := func(hc *HealthConfig) {
		if hc == nil {
			return
		}
		if pc := hc.Passive; pc != nil {
			seenPassive = true
			if pc.Statuses != nil {
				statuses = pc.Statuses
			}
			if pc.Count != nil {
				p.passive.count = *pc.Count
			}
			if pc.Window != nil {
				p.passive.window = pc.Window.std()
			}
			if pc.Cooldown != nil {
				p.passive.cooldown = pc.Cooldown.std()
			}
		}
		if ac := hc.Active; ac != nil {
			seenActive = true
			if ac.Path != "" {
				p.active.path = ac.Path
			}
			if ac.Method != "" {
				p.active.method = ac.Method
			}
			if ac.Interval != nil {
				p.active.interval = ac.Interval.std()
			}
			if ac.Timeout != nil {
				p.active.timeout = ac.Timeout.std()
			}
			if ac.ExpectedStatus != nil {
				p.active.expectedStatus = *ac.ExpectedStatus
			}
			if ac.Retries != nil {
				p.active.retries = *ac.Retries
			}
			if ac.UnhealthyThreshold != nil {
				p.active.unhealthyThr = *ac.UnhealthyThreshold
			}
			if ac.HealthyThreshold != nil {
				p.active.healthyThr = *ac.HealthyThreshold
			}
			if ac.Host != "" {
				p.active.host = ac.Host
			}
		}
	}
	apply(global)
	apply(backend)

	if seenPassive && p.passive.count > 0 && p.passive.window > 0 {
		p.passive.enabled = true
		if statuses == nil {
			statuses = defaultHealthPassiveStatuses
		}
		p.passive.statuses = statusSet(statuses)
	}
	if seenActive && p.active.path != "" {
		p.active.enabled = true
		if p.active.unhealthyThr < 1 {
			p.active.unhealthyThr = 1
		}
		if p.active.healthyThr < 1 {
			p.active.healthyThr = 1
		}
		if p.active.interval <= 0 {
			p.active.interval = defaultHealthActiveInterval
		}
	}
	return p
}

// markHealth flips the backend's health flag, logging only on a real transition
// (so repeated same-state marks don't spam). On recovery it resets the passive
// window so detection starts fresh.
func (b *Backend) markHealth(healthy bool, reason string) {
	if b.healthy.Swap(healthy) == healthy {
		return // no transition
	}
	if healthy {
		b.hs.mu.Lock()
		b.hs.idx, b.hs.n = 0, 0
		b.hs.mu.Unlock()
		log.Printf("switchyard: backend %s marked healthy (%s)", b.URL, reason)
		return
	}
	log.Printf("switchyard: backend %s marked unhealthy (%s)", b.URL, reason)
}

// observeOutcome is the passive detector, called from proxyTransport.RoundTrip
// for every real backend round-trip. A failure (a status in the configured set,
// or a non-cancellation error) is recorded in the window; crossing the threshold
// ejects the backend. Client/reload cancellations are not backend faults and are
// ignored.
func (b *Backend) observeOutcome(status int, err error) {
	p := b.health.passive
	if !p.enabled {
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return // client disconnect / force reload — not a backend fault
		}
	} else if !p.statuses[status] {
		return // a response with an unlisted status is not a failure
	}

	now := time.Now()
	tripped := false
	b.hs.mu.Lock()
	if len(b.hs.fails) > 0 {
		b.hs.fails[b.hs.idx] = now
		b.hs.idx = (b.hs.idx + 1) % len(b.hs.fails)
		if b.hs.n < len(b.hs.fails) {
			b.hs.n++
		}
		if b.hs.n == len(b.hs.fails) {
			// idx now points at the oldest of the last `count` failures.
			if now.Sub(b.hs.fails[b.hs.idx]) <= p.window {
				tripped = true
			}
		}
	}
	b.hs.mu.Unlock()

	if tripped {
		b.markHealth(false, fmt.Sprintf("passive: %d failures within %s", p.count, p.window))
		if !b.health.active.enabled {
			b.scheduleRecover(p.cooldown)
		}
	}
}

// scheduleRecover restores the backend after the passive cooldown, unless a
// recovery is already pending. Used only when no active check is configured (the
// prober is otherwise the authority on recovery).
func (b *Backend) scheduleRecover(d time.Duration) {
	b.hs.mu.Lock()
	if b.hs.recovering {
		b.hs.mu.Unlock()
		return
	}
	b.hs.recovering = true
	b.hs.mu.Unlock()

	time.AfterFunc(d, func() {
		b.hs.mu.Lock()
		b.hs.recovering = false
		b.hs.mu.Unlock()
		b.markHealth(true, "passive cooldown elapsed")
	})
}

// StartHealthChecks launches an active-probe goroutine for every backend whose
// active health check is enabled; each stops when ctx is cancelled. The Server
// calls this per generation (bound to the generation's context), so probers live
// exactly as long as their configuration. Raw Handler() users (no Server) call it
// themselves to enable active checks; passive checks need no start-up.
func (p *Proxy) StartHealthChecks(ctx context.Context) {
	for _, b := range p.Pool.Backends() {
		if b.health.active.enabled {
			go b.runActiveChecks(ctx)
		}
	}
}

// runActiveChecks probes the backend on its interval until ctx is done, flipping
// health on consecutive-cycle thresholds.
func (b *Backend) runActiveChecks(ctx context.Context) {
	a := b.health.active
	client := &http.Client{Timeout: a.timeout, Transport: b.transport}
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	var consecPass, consecFail int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if b.probeCycle(ctx, client, a) {
				consecFail = 0
				if consecPass++; consecPass >= a.healthyThr {
					b.markHealth(true, fmt.Sprintf("active: %d consecutive probe successes", a.healthyThr))
				}
			} else {
				consecPass = 0
				if consecFail++; consecFail >= a.unhealthyThr {
					b.markHealth(false, fmt.Sprintf("active: %d consecutive probe failures", a.unhealthyThr))
				}
			}
		}
	}
}

// probeCycle runs one probe cycle: the request plus up to a.retries immediate
// retries. The cycle passes as soon as one attempt returns the expected status.
func (b *Backend) probeCycle(ctx context.Context, client *http.Client, a activePolicy) bool {
	url := strings.TrimRight(b.URL, "/") + "/" + strings.TrimLeft(a.path, "/")
	for attempt := 0; attempt <= a.retries; attempt++ {
		if b.probeAttempt(ctx, client, a, url) {
			return true
		}
	}
	return false
}

// probeAttempt sends a single probe and reports whether it returned the expected
// status. The body is drained and closed so the connection can be reused.
func (b *Backend) probeAttempt(ctx context.Context, client *http.Client, a activePolicy, url string) bool {
	req, err := http.NewRequestWithContext(ctx, a.method, url, nil)
	if err != nil {
		return false
	}
	if a.host != "" {
		req.Host = a.host
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == a.expectedStatus
}
