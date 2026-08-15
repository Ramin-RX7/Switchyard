package switchyard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Built-in defaults used when a setting is left unset at every scope. They match
// the tuned transport Switchyard has always installed.
const (
	defaultMaxIdleConns        = 512
	defaultMaxIdleConnsPerHost = 256
	defaultIdleConnTimeout     = 90 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
	defaultDialTimeout         = 5 * time.Second
	defaultReadHeaderTimeout   = 10 * time.Second
	defaultServerIdleTimeout   = 60 * time.Second
	defaultOverflowStatus      = http.StatusServiceUnavailable
	defaultOverflowBody        = "switchyard: capacity reached"
	defaultRetryBackoffBaseMs  = 50
	defaultRetryBackoffMaxMs   = 2000
	defaultRetryMaxBodyBytes   = 1 << 20 // 1 MiB
	defaultRetryExhaustedBody  = "switchyard: retries exhausted"
)

// Duration is a timeout expressed in JSON as a plain integer number of seconds
// (e.g. 30). null/0 means 0 ("no limit"). Internally it is a time.Duration, so
// every consumer keeps sub-second precision when constructed from Go.
type Duration time.Duration

func (d Duration) std() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var secs int64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("duration must be an integer number of seconds: %w", err)
	}
	*d = Duration(time.Duration(secs) * time.Second)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(time.Duration(d) / time.Second))
}

// TimeoutsConfig holds upstream (backend-facing) timeouts. Request is the whole
// per-request deadline (0 = none); it is a single value today but modelled as a
// struct so it can later split into send/receive halves.
type TimeoutsConfig struct {
	Request      *Duration `json:"request"`
	TLSHandshake *Duration `json:"tls_handshake"`
}

// TransportConfig holds keep-alive connection-pool tuning.
type TransportConfig struct {
	MaxIdleConns        *int      `json:"max_idle_conns"`
	MaxIdleConnsPerHost *int      `json:"max_idle_conns_per_host"`
	IdleConnTimeout     *Duration `json:"idle_conn_timeout"`
}

// ServerConfig holds the client-facing http.Server timeouts. Project-wide only
// (there is a single server accepting client connections).
type ServerConfig struct {
	ReadHeaderTimeout *Duration `json:"read_header_timeout"`
	ReadTimeout       *Duration `json:"read_timeout"`
	WriteTimeout      *Duration `json:"write_timeout"`
	IdleTimeout       *Duration `json:"idle_timeout"`
}

// OverflowConfig controls what happens when a max_connections cap is reached.
// Strategy is "reject" (default), "queue", or "reroute"; the reject response is
// configurable (status, headers, and a body that may contain $variables).
type OverflowConfig struct {
	Strategy     string            `json:"strategy"`
	QueueTimeout *Duration         `json:"queue_timeout"`
	Status       *int              `json:"status"`
	Headers      map[string]string `json:"headers"`
	Body         *string           `json:"body"`
}

// RetryConfig controls when a failed or bad-status forward is retried on another
// backend. It exists at the top level and per-location; a location's fields merge
// over the global ones (each set field wins, unset inherits — see resolveRetry).
// All fields are pointers/nil-able so "unset" is distinguishable from a zero value.
type RetryConfig struct {
	// Attempts is the number of retries beyond the first try (0/absent disables retry).
	Attempts *int `json:"attempts"`
	// OnConnectionError retries when the backend is unreachable/resets (default true).
	// Applies to any HTTP method.
	OnConnectionError *bool `json:"on_connection_error"`
	// OnStatus lists upstream status codes that trigger a retry. Only idempotent
	// methods are retried on status unless RetryNonIdempotent is set. nil/absent = none.
	OnStatus []int `json:"on_status"`
	// RetryNonIdempotent allows status-based retry of non-idempotent methods
	// (POST/PATCH). Default false.
	RetryNonIdempotent *bool `json:"retry_non_idempotent"`
	// RetrySameBackend, when true (default), lets normal selection reselect a
	// just-tried backend; when false, already-tried backends are excluded.
	RetrySameBackend *bool `json:"retry_same_backend"`
	// SkipUnhealthy excludes backends flagged unhealthy (Backend.SetHealthy(false))
	// from selection (default true). Falls back to the full set if all are unhealthy.
	SkipUnhealthy *bool `json:"skip_unhealthy"`
	// MaxBodyBytes caps request-body buffering for replay; a larger body makes the
	// request single-attempt. Default 1 MiB.
	MaxBodyBytes *int `json:"max_body_bytes"`
	// Backoff is the inter-attempt delay policy.
	Backoff *BackoffConfig `json:"backoff"`
	// Response optionally replaces the client response when retries are exhausted.
	// When nil, an exhausted status retry passes the real upstream response through
	// and an exhausted connection retry renders backend_error (502).
	Response *ResponseConfig `json:"response"`
}

// BackoffConfig is the inter-attempt delay policy for RetryConfig. Strategy is
// "none", "constant", or "exponential" (default). BaseMs/MaxMs are milliseconds.
type BackoffConfig struct {
	Strategy *string `json:"strategy"`
	BaseMs   *int    `json:"base_ms"`
	MaxMs    *int    `json:"max_ms"`
	Jitter   *bool   `json:"jitter"`
}

// ResponseConfig describes a Switchyard-generated HTTP response: a status code,
// a set of headers, and a body. Header values and the body may contain
// $variables (see vars.go), so responses can embed request data or the current
// time. It backs the "response" location type and the built-in error responses
// (backend_error, not_found), each of which supplies its own defaults when a
// field is left unset.
type ResponseConfig struct {
	Status  *int              `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Config is Switchyard's external configuration, loaded from a JSON file.
type Config struct {
	Listen   string          `json:"listen"`
	Backends []BackendConfig `json:"backends"`
	// Logging is optional. When nil, Switchyard uses its built-in operational
	// log line; when set, each request is logged using the user-defined format.
	Logging *LogConfig `json:"logging"`
	// SetHeaders maps header names to value templates applied to requests
	// forwarded to backends, like nginx's proxy_set_header. Values may contain
	// $variables (see vars.go), e.g. {"X-Real-IP": "$remote_addr"}.
	SetHeaders map[string]string `json:"set_headers"`
	// SetResponseHeaders is the response-side mirror of SetHeaders: header names
	// to value templates set on the response returned to the client. Values may
	// contain the same $variables, e.g. {"X-Served-By": "switchyard"}.
	SetResponseHeaders map[string]string `json:"set_response_headers"`
	// Locations is an optional ordered list of nginx-style location blocks.
	Locations []LocationConfig `json:"locations"`

	// MaxConnections is the project-wide ceiling on concurrent in-flight
	// requests (0 = unlimited). It wires to Proxy.MaxInFlight.
	MaxConnections *int `json:"max_connections"`
	// Timeouts / Transport are the project-wide defaults for every backend,
	// overridable per backend.
	Timeouts  *TimeoutsConfig  `json:"timeouts"`
	Transport *TransportConfig `json:"transport"`
	// Server tunes the client-facing HTTP server (project-wide).
	Server *ServerConfig `json:"server"`
	// Overflow controls behavior when a max_connections cap is hit.
	Overflow *OverflowConfig `json:"overflow"`
	// Retry controls when a failed or bad-status forward is retried on another
	// backend. Project-wide default; overridable per location (field-merged).
	Retry *RetryConfig `json:"retry"`
	// BackendError / NotFound / MethodNotAllowed override Switchyard's built-in
	// error responses (502 when an upstream is unreachable or a pool is empty;
	// 404 when no location matched; 405 when a location matched but no backend
	// accepts the request method). When nil, sensible defaults are used. See
	// response.go.
	BackendError     *ResponseConfig `json:"backend_error"`
	NotFound         *ResponseConfig `json:"not_found"`
	MethodNotAllowed *ResponseConfig `json:"method_not_allowed"`
	// Forbidden overrides the built-in 403 response returned when a location's
	// access control denies the client. When nil, a sensible default is used.
	Forbidden *ResponseConfig `json:"forbidden"`

	// Whitelist / Blacklist restrict access to the whole proxy by client IP, the
	// project-wide tier of the per-location lists (see LocationConfig). Each entry
	// is a single IP or a CIDR range (IPv4 or IPv6). A blacklisted IP is denied;
	// when a whitelist is set, only listed IPs are allowed. Empty = no global
	// restriction. Enforced before location matching by the global
	// AccessController (Proxy.Access); it stacks with a location's own lists, so a
	// request must pass both tiers.
	Whitelist []string `json:"whitelist"`
	Blacklist []string `json:"blacklist"`
}

// LocationConfig describes one location block. Path is matched as a prefix
// (the default) or as a Go regexp when Regex is true. Type selects the
// behavior: "proxy" (default) forwards to one of the Backends (referenced by
// id), "static" serves files from Root, "response" returns the canned Response.
// Logging and SetHeaders are optional and stack with the global ones rather than
// replacing them.
type LocationConfig struct {
	Path               string            `json:"path"`
	Regex              bool              `json:"regex"`
	Type               string            `json:"type"`         // "proxy" (default), "static", or "response"
	Backends           []string          `json:"backends"`     // backend ids, for type "proxy"
	Root               string            `json:"root"`         // directory, for type "static"
	StripPrefix        *string           `json:"strip_prefix"` // nil distinguishes unset from ""
	Response           *ResponseConfig   `json:"response"`     // generated response, for type "response"
	Logging            *LogConfig        `json:"logging"`
	SetHeaders         map[string]string `json:"set_headers"`
	SetResponseHeaders map[string]string `json:"set_response_headers"`
	// Whitelist / Blacklist restrict access to this location by client IP. Each
	// entry is a single IP or a CIDR range (IPv4 or IPv6). A blacklisted IP is
	// denied; when a whitelist is set, only listed IPs are allowed. Empty = no
	// restriction. Enforced by the location's AccessController (see access.go).
	Whitelist []string `json:"whitelist"`
	Blacklist []string `json:"blacklist"`
	// MaxConnections caps concurrent in-flight requests routed through this
	// location (0 = unlimited). Independent of backend/project caps.
	MaxConnections *int `json:"max_connections"`
	// Retry overrides the global retry policy for this location. Fields set here
	// win; unset fields inherit the global retry policy (see resolveRetry).
	Retry *RetryConfig `json:"retry"`
}

// BackendConfig describes one configured upstream. URL is required; ID is
// optional and defaults to URL. IDs and URLs must each be unique across
// backends (enforced in New).
type BackendConfig struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Methods restricts which HTTP methods this backend accepts. Empty/omitted
	// means it accepts any method. When set, a location routes a request to this
	// backend only if the request method is listed (matched case-insensitively).
	Methods []string `json:"methods"`
	// MaxConnections caps concurrent in-flight requests to this backend
	// (0 = unlimited). Exposed to custom selectors via Backend.MaxConns.
	MaxConnections *int `json:"max_connections"`
	// Timeouts / Transport override the project defaults for this backend.
	Timeouts         *TimeoutsConfig  `json:"timeouts"`
	Transport        *TransportConfig `json:"transport"`
	DisableKeepAlive *bool            `json:"disable_keep_alive"`
}

// LoadConfig reads and parses the configuration file at path. It is exported so
// SDK users can reuse the same JSON config format as the turnkey binary.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// --- resolved (merged) settings ---------------------------------------------

// backendSettings is the effective per-backend configuration after merging
// backend overrides over project defaults over built-in defaults.
type backendSettings struct {
	maxConns            int           // 0 = unlimited
	requestTimeout      time.Duration // 0 = none
	tlsHandshake        time.Duration
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration
	disableKeepAlive    bool
}

func (c Config) resolveBackend(bc BackendConfig) backendSettings {
	return backendSettings{
		maxConns:            ptrInt(bc.MaxConnections, 0),
		requestTimeout:      pickDur(reqTimeout(bc.Timeouts), reqTimeout(c.Timeouts), 0),
		tlsHandshake:        pickDur(tlsTimeout(bc.Timeouts), tlsTimeout(c.Timeouts), defaultTLSHandshakeTimeout),
		maxIdleConns:        pickInt(trIdle(bc.Transport), trIdle(c.Transport), defaultMaxIdleConns),
		maxIdleConnsPerHost: pickInt(trPerHost(bc.Transport), trPerHost(c.Transport), defaultMaxIdleConnsPerHost),
		idleConnTimeout:     pickDur(trIdleTO(bc.Transport), trIdleTO(c.Transport), defaultIdleConnTimeout),
		disableKeepAlive:    ptrBool(bc.DisableKeepAlive, false),
	}
}

// serverTimeouts returns the client-facing http.Server timeouts (project-wide),
// falling back to Switchyard's long-standing defaults.
func (c Config) serverTimeouts() (readHeader, read, write, idle time.Duration) {
	readHeader, idle = defaultReadHeaderTimeout, defaultServerIdleTimeout
	if c.Server != nil {
		readHeader = pickDur(c.Server.ReadHeaderTimeout, nil, readHeader)
		read = pickDur(c.Server.ReadTimeout, nil, 0)
		write = pickDur(c.Server.WriteTimeout, nil, 0)
		idle = pickDur(c.Server.IdleTimeout, nil, idle)
	}
	return
}

// --- pointer/optional helpers -----------------------------------------------

func ptrInt(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}
func ptrBool(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}
func pickInt(b, p *int, def int) int {
	if b != nil {
		return *b
	}
	if p != nil {
		return *p
	}
	return def
}
func pickDur(b, p *Duration, def time.Duration) time.Duration {
	if b != nil {
		return b.std()
	}
	if p != nil {
		return p.std()
	}
	return def
}

func reqTimeout(t *TimeoutsConfig) *Duration {
	if t == nil {
		return nil
	}
	return t.Request
}
func tlsTimeout(t *TimeoutsConfig) *Duration {
	if t == nil {
		return nil
	}
	return t.TLSHandshake
}
func trIdle(t *TransportConfig) *int {
	if t == nil {
		return nil
	}
	return t.MaxIdleConns
}
func trPerHost(t *TransportConfig) *int {
	if t == nil {
		return nil
	}
	return t.MaxIdleConnsPerHost
}
func trIdleTO(t *TransportConfig) *Duration {
	if t == nil {
		return nil
	}
	return t.IdleConnTimeout
}

// --- validation (fail-fast at startup) --------------------------------------

func (c Config) validate() error {
	if err := checkNonNegInt("max_connections", c.MaxConnections); err != nil {
		return err
	}
	if err := checkTimeouts("timeouts", c.Timeouts); err != nil {
		return err
	}
	if err := checkTransport("transport", c.Transport); err != nil {
		return err
	}
	if err := checkServer(c.Server); err != nil {
		return err
	}
	if err := checkOverflow(c.Overflow); err != nil {
		return err
	}
	if err := checkRetry("retry", c.Retry); err != nil {
		return err
	}
	if err := checkResponse("backend_error", c.BackendError); err != nil {
		return err
	}
	if err := checkResponse("not_found", c.NotFound); err != nil {
		return err
	}
	if err := checkResponse("method_not_allowed", c.MethodNotAllowed); err != nil {
		return err
	}
	if err := checkResponse("forbidden", c.Forbidden); err != nil {
		return err
	}
	for _, bc := range c.Backends {
		who := "backend " + bc.URL
		if err := checkNonNegInt(who+" max_connections", bc.MaxConnections); err != nil {
			return err
		}
		if err := checkTimeouts(who+" timeouts", bc.Timeouts); err != nil {
			return err
		}
		if err := checkTransport(who+" transport", bc.Transport); err != nil {
			return err
		}
	}
	for _, lc := range c.Locations {
		if err := checkNonNegInt("location "+lc.Path+" max_connections", lc.MaxConnections); err != nil {
			return err
		}
		if err := checkResponse("location "+lc.Path+" response", lc.Response); err != nil {
			return err
		}
		if err := checkRetry("location "+lc.Path+" retry", lc.Retry); err != nil {
			return err
		}
	}
	return nil
}

// checkRetry validates a retry config's enum, ranges, and non-negative counts.
// Body/header templates in retry.response are compiled (and their $variables
// validated) in resolveRetry via newResponder, so only numeric ranges are here.
func checkRetry(name string, rc *RetryConfig) error {
	if rc == nil {
		return nil
	}
	if err := checkNonNegInt(name+".attempts", rc.Attempts); err != nil {
		return err
	}
	if err := checkNonNegInt(name+".max_body_bytes", rc.MaxBodyBytes); err != nil {
		return err
	}
	for _, s := range rc.OnStatus {
		if s < 100 || s > 599 {
			return fmt.Errorf("%s.on_status %d out of range (100-599)", name, s)
		}
	}
	if b := rc.Backoff; b != nil {
		if b.Strategy != nil {
			switch *b.Strategy {
			case "", "none", "constant", "exponential":
			default:
				return fmt.Errorf("%s.backoff.strategy %q is not supported (want \"none\", \"constant\", or \"exponential\")", name, *b.Strategy)
			}
		}
		if err := checkNonNegInt(name+".backoff.base_ms", b.BaseMs); err != nil {
			return err
		}
		if err := checkNonNegInt(name+".backoff.max_ms", b.MaxMs); err != nil {
			return err
		}
	}
	if err := checkResponse(name+".response", rc.Response); err != nil {
		return err
	}
	return nil
}

// checkResponse validates a generated-response config's status range. Body and
// header templates are compiled (and their $variables validated) in New via
// newResponder, so only the numeric range is checked here.
func checkResponse(name string, r *ResponseConfig) error {
	if r == nil || r.Status == nil {
		return nil
	}
	if *r.Status < 100 || *r.Status > 599 {
		return fmt.Errorf("%s.status %d out of range (100-599)", name, *r.Status)
	}
	return nil
}

func checkNonNegInt(name string, p *int) error {
	if p != nil && *p < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}
	return nil
}
func checkNonNegDur(name string, p *Duration) error {
	if p != nil && p.std() < 0 {
		return fmt.Errorf("%s must not be negative", name)
	}
	return nil
}
func checkTimeouts(name string, t *TimeoutsConfig) error {
	if t == nil {
		return nil
	}
	if err := checkNonNegDur(name+".request", t.Request); err != nil {
		return err
	}
	return checkNonNegDur(name+".tls_handshake", t.TLSHandshake)
}
func checkTransport(name string, t *TransportConfig) error {
	if t == nil {
		return nil
	}
	if err := checkNonNegInt(name+".max_idle_conns", t.MaxIdleConns); err != nil {
		return err
	}
	if err := checkNonNegInt(name+".max_idle_conns_per_host", t.MaxIdleConnsPerHost); err != nil {
		return err
	}
	return checkNonNegDur(name+".idle_conn_timeout", t.IdleConnTimeout)
}
func checkServer(s *ServerConfig) error {
	if s == nil {
		return nil
	}
	for name, d := range map[string]*Duration{
		"server.read_header_timeout": s.ReadHeaderTimeout,
		"server.read_timeout":        s.ReadTimeout,
		"server.write_timeout":       s.WriteTimeout,
		"server.idle_timeout":        s.IdleTimeout,
	} {
		if err := checkNonNegDur(name, d); err != nil {
			return err
		}
	}
	return nil
}
func checkOverflow(o *OverflowConfig) error {
	if o == nil {
		return nil
	}
	switch o.Strategy {
	case "", "reject", "queue", "reroute":
	default:
		return fmt.Errorf("overflow.strategy %q is not supported (want \"reject\", \"queue\", or \"reroute\")", o.Strategy)
	}
	if err := checkNonNegDur("overflow.queue_timeout", o.QueueTimeout); err != nil {
		return err
	}
	if o.Status != nil && (*o.Status < 100 || *o.Status > 599) {
		return fmt.Errorf("overflow.status %d out of range (100-599)", *o.Status)
	}
	return nil
}
