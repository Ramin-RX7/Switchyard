package main

import "fmt"

// Action is what handling should do with a request. It is recorded by decide
// and acted on by act.
type Action string

const (
	ActionForward Action = "forward"
	ActionStatic  Action = "static"
	ActionReject  Action = "reject"
)

// Decision is the passive result of interpreting a request. It records how
// Switchyard understood the request, including the backend selected for it,
// but does not itself cause any forwarding to occur.
type Decision struct {
	Action  Action
	Reason  string
	Backend *backend // selected upstream, nil when none was chosen
	// Location is the matched location block, nil for global round-robin or
	// when no location matched. It carries that location's static root,
	// stacked logger and headers, read during handling.
	Location *location
	// Status is the HTTP status for an Action of "reject": 404 when no location
	// matched, 502 otherwise. Zero means "default to 502".
	Status int
}

func (d Decision) String() string {
	switch {
	case d.Backend != nil:
		return fmt.Sprintf("%s -> %s (%s)", d.Action, d.Backend.url, d.Reason)
	case d.Location != nil:
		return fmt.Sprintf("%s -> %s (%s)", d.Action, d.Location.raw, d.Reason)
	default:
		return fmt.Sprintf("%s (%s)", d.Action, d.Reason)
	}
}
