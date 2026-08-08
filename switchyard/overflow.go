package switchyard

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// overflowPolicy decides what happens when a max_connections cap is reached:
//   - "reject": fail fast.
//   - "queue":  wait up to queueWait for a slot, then fail.
//   - "reroute": at the backend step, try other backends in the pool; if all
//     are full, fall back to the queue/reject behavior (waiting queueWait when set).
//
// The reject response is produced by a ResponseGenerator (resp), so it carries a
// configurable status, headers, and a body that may contain $variables.
type overflowPolicy struct {
	strategy  string
	queueWait time.Duration
	resp      *TemplateResponder
}

// fallbackWait is how long to block for a slot when none is immediately free
// (0 = don't block). Applies to "queue" and to "reroute"'s all-full fallback.
func (o overflowPolicy) fallbackWait() time.Duration {
	if o.strategy == "queue" || o.strategy == "reroute" {
		return o.queueWait
	}
	return 0
}

// reroutes reports whether a full backend should try other pool members first.
func (o overflowPolicy) reroutes() bool { return o.strategy == "reroute" }

func (c Config) overflowPolicy() (overflowPolicy, error) {
	o := overflowPolicy{strategy: "reject"}
	var rc ResponseConfig
	if c.Overflow != nil {
		if c.Overflow.Strategy != "" {
			o.strategy = c.Overflow.Strategy
		}
		if c.Overflow.QueueTimeout != nil {
			o.queueWait = c.Overflow.QueueTimeout.std()
		}
		rc.Status = c.Overflow.Status
		rc.Headers = c.Overflow.Headers
		if c.Overflow.Body != nil {
			rc.Body = *c.Overflow.Body
		}
	}
	resp, err := newResponder(rc, defaultOverflowStatus, defaultOverflowBody)
	if err != nil {
		return overflowPolicy{}, fmt.Errorf("overflow: %w", err)
	}
	o.resp = resp
	return o, nil
}

// acquire takes a slot on l, blocking up to the policy's fallback wait (0 for
// "reject", queueWait for "queue"/"reroute"). Returns true if held. Used for
// scopes without alternates (the global and per-location caps).
func (o overflowPolicy) acquire(ctx context.Context, l *limiter) bool {
	return l.acquire(ctx, o.fallbackWait())
}

// reject writes the configured over-capacity response to the client.
func (o overflowPolicy) reject(w http.ResponseWriter, r *http.Request, req Request) {
	o.resp.Generate(w, r, req)
}
