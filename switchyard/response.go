package switchyard

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ResponseGenerator produces a complete HTTP response from Switchyard itself,
// rather than proxying to a backend or serving a file. It is the pluggable
// stage behind the "response" location type and the built-in generated
// responses (backend-down 502, no-match 404, and overflow). The default is
// TemplateResponder; an SDK user may supply their own and assign it to
// loc.Responder (per response location) or p.NotFound / p.BadGateway (the
// global error responses).
type ResponseGenerator interface {
	Generate(w http.ResponseWriter, r *http.Request, req Request)
}

// TemplateResponder is the default ResponseGenerator: a fixed status code plus
// headers and a body whose values may contain $variables (see vars.go),
// rendered against the immutable request snapshot. Header values and the body
// are compiled once — an unknown variable fails fast at New — and rendered per
// request.
type TemplateResponder struct {
	status  int
	headers []headerRule
	body    *valueTemplate
}

// newResponder compiles cfg into a TemplateResponder, applying defStatus/defBody
// when the corresponding field is left unset. Compilation validates every
// $variable in the body and header values, so misconfiguration fails fast.
func newResponder(cfg ResponseConfig, defStatus int, defBody string) (*TemplateResponder, error) {
	status := defStatus
	if cfg.Status != nil {
		status = *cfg.Status
	}
	body := defBody
	if cfg.Body != "" {
		body = cfg.Body
	}
	bt, err := compileTemplate(body)
	if err != nil {
		return nil, fmt.Errorf("response body: %w", err)
	}
	r := &TemplateResponder{status: status, body: bt}

	// Compile headers in a stable (sorted) order so output is deterministic.
	names := make([]string, 0, len(cfg.Headers))
	for name := range cfg.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("response: header name must not be empty")
		}
		t, err := compileTemplate(cfg.Headers[name])
		if err != nil {
			return nil, fmt.Errorf("response: header %q: %w", name, err)
		}
		r.headers = append(r.headers, headerRule{name: name, tmpl: t})
	}
	return r, nil
}

// responderOf compiles an optional *ResponseConfig (nil = use only the
// defaults) into a TemplateResponder. It is the entry point New uses for the
// built-in error responders.
func responderOf(rc *ResponseConfig, defStatus int, defBody string) (*TemplateResponder, error) {
	var c ResponseConfig
	if rc != nil {
		c = *rc
	}
	return newResponder(c, defStatus, defBody)
}

// Generate writes the compiled response: headers first (defaulting Content-Type
// to text/plain, matching http.Error, when the config did not set one), then
// the status, then the rendered body.
func (r *TemplateResponder) Generate(w http.ResponseWriter, _ *http.Request, req Request) {
	h := w.Header()
	hasContentType := false
	for _, rule := range r.headers {
		if http.CanonicalHeaderKey(rule.name) == "Content-Type" {
			hasContentType = true
		}
		h.Set(rule.name, rule.tmpl.render(req))
	}
	if !hasContentType {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
	}
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body.render(req)))
}
