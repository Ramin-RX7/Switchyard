package switchyard

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ResponseHeaderApplier sets headers on the response returned to the client. It
// is the pluggable "set_response_headers" stage — the response-side mirror of
// HeaderApplier. The default is TemplateResponseHeaderSetter (config-driven,
// with $variable interpolation); an SDK user may supply their own, globally via
// Proxy.ResponseHeaders or per-location via Location.ResponseHeaders.
type ResponseHeaderApplier interface {
	// Apply sets headers on h (the response header map), deriving values from the
	// captured request snapshot. It runs once, just before the status line is
	// written, so values overriding a header the handler already set take effect.
	Apply(req Request, h http.Header)
}

// TemplateResponseHeaderSetter is the default ResponseHeaderApplier: it sets a
// fixed set of headers on every response, with variable interpolation against
// the request snapshot. It reuses the same compiled-template machinery as the
// request-side TemplateHeaderSetter.
type TemplateResponseHeaderSetter struct {
	rules []headerRule
}

func newResponseHeaderSetter(m map[string]string) (*TemplateResponseHeaderSetter, error) {
	hs := &TemplateResponseHeaderSetter{}
	for name, val := range m {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("set_response_headers: header name must not be empty")
		}
		t, err := compileTemplate(val)
		if err != nil {
			return nil, fmt.Errorf("set_response_headers: header %q: %w", name, err)
		}
		hs.rules = append(hs.rules, headerRule{name: name, tmpl: t})
	}
	return hs, nil
}

// Apply renders each configured value against req and sets it on the response
// header map, overriding any existing value for that name.
func (hs *TemplateResponseHeaderSetter) Apply(req Request, h http.Header) {
	for _, rule := range hs.rules {
		h.Set(rule.name, rule.tmpl.render(req))
	}
}

// responseHeaderWriter wraps an http.ResponseWriter to inject the configured
// response headers exactly once, just before the status line is written (on the
// first WriteHeader or Write). It forwards Flush and Hijack so streaming and
// connection upgrades keep working, mirroring statusWriter.
type responseHeaderWriter struct {
	http.ResponseWriter
	apply func(http.Header)
	done  bool
}

func (w *responseHeaderWriter) inject() {
	if !w.done {
		w.done = true
		w.apply(w.ResponseWriter.Header())
	}
}

func (w *responseHeaderWriter) WriteHeader(code int) {
	w.inject()
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseHeaderWriter) Write(b []byte) (int, error) {
	w.inject()
	return w.ResponseWriter.Write(b)
}

func (w *responseHeaderWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *responseHeaderWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("switchyard: response writer does not support hijacking")
}
