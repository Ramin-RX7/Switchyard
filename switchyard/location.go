package switchyard

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// LocationKind selects a location's behavior: forward to a backend pool
// ("proxy"), serve files from a directory ("static"), or return a
// Switchyard-generated response ("response").
type LocationKind string

const (
	KindProxy   LocationKind = "proxy"
	KindStatic  LocationKind = "static"
	KindRespond LocationKind = "response"
)

// Location is one compiled entry in the ordered locations list. Matching is
// first-match-wins in slice order. A "proxy" location selects from its own pool
// of backends via its own Selector; a "static" location serves files from a
// directory; a "response" location returns a Switchyard-generated response.
// Logging and headers, when set, stack on top of the global ones.
//
// The exported fields (Kind, Pool, Selector, Static, Responder, Logger, Headers)
// are overridable by SDK users after New — e.g. assign a custom Selector to change
// this location's load balancing without touching the rest of its configuration.
//
// A Location must not be copied after construction: its Selector may hold an
// atomic counter. Always pass it around as *Location.
type Location struct {
	// matching
	prefix string         // used when re == nil
	re     *regexp.Regexp // used when set (regex: true)
	raw    string         // original path, for messages

	Kind LocationKind // KindProxy or KindStatic

	// proxy
	Pool     BackendPool     // this location's backend pool
	Selector BackendSelector // this location's backend-selection strategy

	// static
	Static StaticServer // serves files; nil unless Kind == KindStatic

	// response
	Responder ResponseGenerator // generates a response; nil unless Kind == KindRespond

	// stacking features (nil = none for this location)
	Logger  Logger
	Headers HeaderApplier

	lim *limiter // concurrency cap for this location (nil = unlimited)
}

// Path returns the location's configured path (the prefix or regex source), for
// SDK users identifying a specific location to customize after New.
func (l *Location) Path() string { return l.raw }

// Matches reports whether path is handled by this location. It is exported so a
// custom Decider can reuse the built-in matching rule.
func (l *Location) Matches(path string) bool {
	if l.re != nil {
		return l.re.MatchString(path)
	}
	return strings.HasPrefix(path, l.prefix)
}

// compileLocations validates and compiles the configured locations against the
// backend registry. It fails fast on any misconfiguration so problems surface
// at startup rather than per request.
func compileLocations(cfgs []LocationConfig, byID map[string]*Backend) ([]*Location, error) {
	locs := make([]*Location, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Path == "" {
			return nil, fmt.Errorf("location: path must not be empty")
		}

		loc := &Location{raw: c.Path}
		if c.Regex {
			re, err := regexp.Compile(c.Path)
			if err != nil {
				return nil, fmt.Errorf("location %q: invalid regex: %w", c.Path, err)
			}
			loc.re = re
		} else {
			loc.prefix = c.Path
		}

		kind := LocationKind(c.Type)
		if kind == "" {
			kind = KindProxy
		}
		switch kind {
		case KindProxy:
			if c.Root != "" || c.StripPrefix != nil {
				return nil, fmt.Errorf("location %q: root/strip_prefix are only valid for type \"static\"", c.Path)
			}
			if len(c.Backends) == 0 {
				return nil, fmt.Errorf("location %q: proxy location requires at least one backend", c.Path)
			}
			var pool []*Backend
			for _, id := range c.Backends {
				b, ok := byID[id]
				if !ok {
					return nil, fmt.Errorf("location %q: unknown backend id %q", c.Path, id)
				}
				pool = append(pool, b)
			}
			loc.Pool = NewStaticPool(pool)
			// Each location gets its own selector so locations sharing a backend
			// rotate independently. Users may replace this after New.
			loc.Selector = &RoundRobinSelector{}
		case KindStatic:
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
			var stripPrefix string
			switch {
			case c.StripPrefix != nil:
				stripPrefix = *c.StripPrefix
			case loc.re == nil:
				stripPrefix = c.Path
			}
			loc.Static = newFileServer(c.Root, stripPrefix)
		case KindRespond:
			if len(c.Backends) > 0 || c.Root != "" || c.StripPrefix != nil {
				return nil, fmt.Errorf("location %q: backends/root/strip_prefix are not valid for type \"response\"", c.Path)
			}
			if c.Response == nil {
				return nil, fmt.Errorf("location %q: response location requires a \"response\" block", c.Path)
			}
			resp, err := newResponder(*c.Response, http.StatusOK, "")
			if err != nil {
				return nil, fmt.Errorf("location %q: %w", c.Path, err)
			}
			loc.Responder = resp
		default:
			return nil, fmt.Errorf("location %q: unknown type %q (want \"proxy\", \"static\", or \"response\")", c.Path, kind)
		}
		loc.Kind = kind
		loc.lim = newLimiter(ptrInt(c.MaxConnections, 0))

		if c.Logging != nil {
			l, err := newLogger(*c.Logging)
			if err != nil {
				return nil, fmt.Errorf("location %q: %w", c.Path, err)
			}
			loc.Logger = l
		}
		if len(c.SetHeaders) > 0 {
			hs, err := newHeaderSetter(c.SetHeaders)
			if err != nil {
				return nil, fmt.Errorf("location %q: %w", c.Path, err)
			}
			loc.Headers = hs
		}

		locs = append(locs, loc)
	}
	return locs, nil
}
