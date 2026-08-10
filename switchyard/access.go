package switchyard

import (
	"fmt"
	"net/netip"
	"strings"
)

// AccessController decides whether a request may proceed through a location. It
// is the pluggable access-control stage, assigned per location via
// Location.Access. The default is IPAccessControl (config-driven
// whitelist/blacklist); an SDK user may replace it with any logic — a token
// check, geo lookup, allow-list from service discovery, etc. A nil Access means
// the location is unrestricted.
type AccessController interface {
	// Allow reports whether the request may proceed. Returning false makes the
	// Decider reject with 403 (rendered by the configurable Forbidden responder).
	// It must stay pure: no I/O, no mutation of the request.
	Allow(req Request) bool
}

// IPAccessControl is the default AccessController: it allows or denies by client
// IP using a blacklist and an optional whitelist. Each list holds single IPs
// and/or CIDR ranges (IPv4 or IPv6). Order of evaluation:
//
//  1. If the client IP is blacklisted, deny.
//  2. Else if a whitelist is configured, allow only when the IP is in it.
//  3. Else allow.
//
// The client IP is the connecting peer (Request.RemoteAddr); an SDK user who
// terminates behind a trusted proxy can supply a custom AccessController that
// consults X-Forwarded-For instead.
type IPAccessControl struct {
	whitelist []netip.Prefix
	blacklist []netip.Prefix
}

// newIPAccessControl compiles whitelist/blacklist entries (single IPs or CIDRs)
// into prefixes, failing fast on any malformed entry.
func newIPAccessControl(whitelist, blacklist []string) (*IPAccessControl, error) {
	wl, err := parsePrefixes("whitelist", whitelist)
	if err != nil {
		return nil, err
	}
	bl, err := parsePrefixes("blacklist", blacklist)
	if err != nil {
		return nil, err
	}
	return &IPAccessControl{whitelist: wl, blacklist: bl}, nil
}

// Allow implements AccessController with blacklist-then-whitelist semantics.
func (a *IPAccessControl) Allow(req Request) bool {
	addr, ok := clientAddr(req)
	if ok && containsAddr(a.blacklist, addr) {
		return false // explicitly denied
	}
	if len(a.whitelist) > 0 {
		// A whitelist is configured: only listed IPs pass, and an unparseable
		// client address fails closed.
		return ok && containsAddr(a.whitelist, addr)
	}
	return true
}

// clientAddr parses the connecting peer's IP from the request snapshot.
func clientAddr(req Request) (netip.Addr, bool) {
	a, err := netip.ParseAddr(hostPart(req.RemoteAddr))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func parsePrefixes(name string, entries []string) ([]netip.Prefix, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			return nil, fmt.Errorf("%s: entries must not be empty", name)
		}
		p, err := parsePrefix(e)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid entry %q: %w", name, e, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// parsePrefix parses a single IP (treated as a host route) or a CIDR range.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}
