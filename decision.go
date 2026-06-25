package main

import "fmt"

// Decision is the passive result of interpreting a request. It records how
// Switchyard understood the request, including the backend selected for it,
// but does not itself cause any forwarding to occur.
type Decision struct {
	Action  string
	Reason  string
	Backend *backend // selected upstream, nil when none was chosen
}

func (d Decision) String() string {
	if d.Backend == nil {
		return fmt.Sprintf("%s (%s)", d.Action, d.Reason)
	}
	return fmt.Sprintf("%s -> %s (%s)", d.Action, d.Backend.url, d.Reason)
}
