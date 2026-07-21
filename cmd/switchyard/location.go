package main

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
)

// locationKind selects a location's behavior: forward to a backend pool
// ("proxy") or serve files from a directory ("static").
type locationKind string

const (
	kindProxy  locationKind = "proxy"
	kindStatic locationKind = "static"
)

// location is one compiled entry in the ordered locations list. Matching is
// first-match-wins in slice order. A "proxy" location selects from its own pool
// of backends (round-robin via its own counter); a "static" location serves
// files from a directory. Logging and headers, when set, stack on top of the
// global ones.
//
// A location must not be copied after construction: it holds an atomic counter.
// Always pass it around as *location.
type location struct {
	// matching
	prefix string         // used when re == nil
	re     *regexp.Regexp // used when set (regex: true)
	raw    string         // original path, for messages

	kind locationKind // kindProxy or kindStatic

	// proxy
	backends []*backend    // shared pointers from the global registry
	next     atomic.Uint64 // this location's own round-robin counter

	// static
	root        string
	stripPrefix string
	fileServer  http.Handler // built once at compile time

	// stacking features (nil = none for this location)
	logger  *Logger
	headers *headerSetter
}

// matches reports whether path is handled by this location.
func (l *location) matches(path string) bool {
	if l.re != nil {
		return l.re.MatchString(path)
	}
	return strings.HasPrefix(path, l.prefix)
}

// selectBackend picks the next backend from the pool using round-robin. It is
// passive: an atomic increment, no I/O. Returns nil when the pool is empty (the
// caller turns this into a reject), though compileLocations rejects empty pools
// at startup so this should be unreachable.
func (l *location) selectBackend() *backend {
	if len(l.backends) == 0 {
		return nil
	}
	i := l.next.Add(1) - 1
	return l.backends[int(i%uint64(len(l.backends)))]
}

// compileLocations validates and compiles the configured locations against the
// backend registry. It fails fast on any misconfiguration so problems surface
// at startup rather than per request.
func compileLocations(cfgs []LocationConfig, byID map[string]*backend) ([]*location, error) {
	locs := make([]*location, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Path == "" {
			return nil, fmt.Errorf("location: path must not be empty")
		}

		loc := &location{raw: c.Path}
		if c.Regex {
			re, err := regexp.Compile(c.Path)
			if err != nil {
				return nil, fmt.Errorf("location %q: invalid regex: %w", c.Path, err)
			}
			loc.re = re
		} else {
			loc.prefix = c.Path
		}

		kind := locationKind(c.Type)
		if kind == "" {
			kind = kindProxy
		}
		switch kind {
		case kindProxy:
			if c.Root != "" || c.StripPrefix != nil {
				return nil, fmt.Errorf("location %q: root/strip_prefix are only valid for type \"static\"", c.Path)
			}
			if len(c.Backends) == 0 {
				return nil, fmt.Errorf("location %q: proxy location requires at least one backend", c.Path)
			}
			for _, id := range c.Backends {
				b, ok := byID[id]
				if !ok {
					return nil, fmt.Errorf("location %q: unknown backend id %q", c.Path, id)
				}
				loc.backends = append(loc.backends, b)
			}
		case kindStatic:
			if len(c.Backends) > 0 {
				return nil, fmt.Errorf("location %q: backends are only valid for type \"proxy\"", c.Path)
			}
			if c.Root == "" {
				return nil, fmt.Errorf("location %q: static location requires a root directory", c.Path)
			}
			info, err := os.Stat(c.Root)
			if err != nil {
				return nil, fmt.Errorf("location %q: root %q: %w", c.Path, c.Root, err)
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("location %q: root %q is not a directory", c.Path, c.Root)
			}
			loc.root = c.Root
			loc.fileServer = http.FileServer(http.Dir(c.Root))
			switch {
			case c.StripPrefix != nil:
				loc.stripPrefix = *c.StripPrefix
			case loc.re == nil:
				loc.stripPrefix = c.Path
			}
		default:
			return nil, fmt.Errorf("location %q: unknown type %q (want \"proxy\" or \"static\")", c.Path, kind)
		}
		loc.kind = kind

		if c.Logging != nil {
			l, err := newLogger(*c.Logging)
			if err != nil {
				return nil, fmt.Errorf("location %q: %w", c.Path, err)
			}
			loc.logger = l
		}
		if len(c.SetHeaders) > 0 {
			hs, err := newHeaderSetter(c.SetHeaders)
			if err != nil {
				return nil, fmt.Errorf("location %q: %w", c.Path, err)
			}
			loc.headers = hs
		}

		locs = append(locs, loc)
	}
	return locs, nil
}
