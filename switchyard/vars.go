package switchyard

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Request variables, modelled on nginx's variable set. They are computed from
// the incoming request and are used both in set_headers values ($name) and in
// the log format ({var.name}).
//
//	remote_addr     client IP (host part of RemoteAddr)
//	remote_port     client port
//	host            request Host header
//	scheme          http or https
//	request_method  HTTP method
//	request_uri     full path including query, e.g. /v1/users?id=1
//	uri             path without query, e.g. /v1/users
//	args            raw query string
//	query_string    alias for args
//	time_iso8601    request-receipt time, RFC 3339 (e.g. 2006-01-02T15:04:05Z07:00)
//	time_unix       request-receipt time, Unix seconds
//	http_<name>     a request header, e.g. http_origin -> Origin,
//	                http_user_agent -> User-Agent

// requestVar resolves a variable name against req. The bool reports whether the
// name is a known variable (http_* is always known).
func requestVar(req Request, name string) (string, bool) {
	switch name {
	case "remote_addr":
		return hostPart(req.RemoteAddr), true
	case "remote_port":
		return portPart(req.RemoteAddr), true
	case "host":
		return req.Host, true
	case "scheme":
		return req.Scheme, true
	case "request_method":
		return req.Method, true
	case "request_uri":
		if req.RawQuery != "" {
			return req.Path + "?" + req.RawQuery, true
		}
		return req.Path, true
	case "uri":
		return req.Path, true
	case "args", "query_string":
		return req.RawQuery, true
	case "time_iso8601":
		return req.ReceivedAt.Format(time.RFC3339), true
	case "time_unix":
		return strconv.FormatInt(req.ReceivedAt.Unix(), 10), true
	}
	if h, ok := headerVar(name); ok {
		return req.Header.Get(h), true
	}
	return "", false
}

// knownVar reports whether name is a valid variable, for startup validation.
func knownVar(name string) bool {
	switch name {
	case "remote_addr", "remote_port", "host", "scheme",
		"request_method", "request_uri", "uri", "args", "query_string",
		"time_iso8601", "time_unix":
		return true
	}
	_, ok := headerVar(name)
	return ok
}

// headerVar maps an http_* variable to its header name, e.g. http_origin ->
// "Origin". It reports false for non-header variables.
func headerVar(name string) (string, bool) {
	const prefix = "http_"
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return "", false
	}
	return strings.ReplaceAll(name[len(prefix):], "_", "-"), true
}

func hostPart(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func portPart(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return ""
}

// --- value templates --------------------------------------------------------

// tmplSeg is one piece of a compiled template: literal text (varName == "") or
// a variable reference.
type tmplSeg struct {
	literal string
	varName string
}

// valueTemplate is a compiled header value, e.g. "ip=$remote_addr".
type valueTemplate struct {
	segs []tmplSeg
}

// compileTemplate parses a value containing $name and ${name} variable
// references. Unknown variables are rejected so misconfiguration fails fast.
func compileTemplate(s string) (*valueTemplate, error) {
	t := &valueTemplate{}
	for i := 0; i < len(s); {
		if s[i] != '$' {
			j := strings.IndexByte(s[i:], '$')
			if j < 0 {
				t.segs = append(t.segs, tmplSeg{literal: s[i:]})
				break
			}
			t.segs = append(t.segs, tmplSeg{literal: s[i : i+j]})
			i += j
			continue
		}

		// s[i] == '$'
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated '${' in %q", s)
			}
			name := s[i+2 : i+end]
			if !knownVar(name) {
				return nil, fmt.Errorf("unknown variable %q", "$"+name)
			}
			t.segs = append(t.segs, tmplSeg{varName: name})
			i += end + 1
			continue
		}

		// $name
		j := i + 1
		for j < len(s) && isVarChar(s[j]) {
			j++
		}
		if j == i+1 { // a lone '$', treated literally
			t.segs = append(t.segs, tmplSeg{literal: "$"})
			i++
			continue
		}
		name := s[i+1 : j]
		if !knownVar(name) {
			return nil, fmt.Errorf("unknown variable %q", "$"+name)
		}
		t.segs = append(t.segs, tmplSeg{varName: name})
		i = j
	}
	return t, nil
}

func isVarChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

func (t *valueTemplate) render(req Request) string {
	var b strings.Builder
	for _, s := range t.segs {
		if s.varName == "" {
			b.WriteString(s.literal)
			continue
		}
		v, _ := requestVar(req, s.varName)
		b.WriteString(v)
	}
	return b.String()
}

// --- header setting ---------------------------------------------------------

type headerRule struct {
	name string
	tmpl *valueTemplate
}

// HeaderApplier injects/overrides headers on a request before it is forwarded.
// It is the pluggable "set_headers" stage. The default is TemplateHeaderSetter
// (config-driven, nginx-style); an SDK user may supply their own — e.g. to add
// a generated request ID or values from an external source. Embed
// TemplateHeaderSetter and override Apply to keep the config headers and add more.
type HeaderApplier interface {
	Apply(req Request, r *http.Request)
}

// TemplateHeaderSetter is the default HeaderApplier: it applies a set of
// configured headers to outgoing requests, with variable interpolation. It
// mirrors nginx's proxy_set_header.
type TemplateHeaderSetter struct {
	rules []headerRule
}

func newHeaderSetter(m map[string]string) (*TemplateHeaderSetter, error) {
	hs := &TemplateHeaderSetter{}
	for name, val := range m {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("set_headers: header name must not be empty")
		}
		t, err := compileTemplate(val)
		if err != nil {
			return nil, fmt.Errorf("set_headers: header %q: %w", name, err)
		}
		hs.rules = append(hs.rules, headerRule{name: name, tmpl: t})
	}
	return hs, nil
}

// Apply sets the configured headers on the outgoing request r, deriving values
// from the captured request req. All values are rendered first, so variables
// always resolve against the original request rather than headers set here.
// Setting "Host" updates r.Host, since Go's Header map does not control it.
func (hs *TemplateHeaderSetter) Apply(req Request, r *http.Request) {
	values := make([]string, len(hs.rules))
	for i, rule := range hs.rules {
		values[i] = rule.tmpl.render(req)
	}
	for i, rule := range hs.rules {
		if http.CanonicalHeaderKey(rule.name) == "Host" {
			r.Host = values[i]
			continue
		}
		r.Header.Set(rule.name, values[i])
	}
}
