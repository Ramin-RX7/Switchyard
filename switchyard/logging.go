package switchyard

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Logger renders the observations about one request/response exchange and emits
// them. It is the pluggable logging stage: the built-in FormatLogger renders a
// user-defined format string, but SDK users may supply their own implementation
// (e.g. structured JSON, metrics, a tracing span).
//
// NeedsRequestBody / NeedsResponseBody let the proxy avoid buffering bodies that
// no active logger will read; a logger that never uses them should return false.
type Logger interface {
	Log(rec *LogRecord)
	NeedsRequestBody() bool
	NeedsResponseBody() bool
}

// LogConfig is the user-controlled logging configuration. It is optional: when
// absent from the config file Switchyard falls back to its built-in operational
// log line.
type LogConfig struct {
	// Outputs lists where each log line is written. Valid values are "console"
	// and "file". An empty list defaults to ["console"].
	Outputs []string `json:"outputs"`
	// File is the path written to when "file" is among the outputs.
	File string `json:"file"`
	// Format is the line template. See compileFormat for the available fields.
	Format string `json:"format"`
}

// FormatLogger is the default Logger. It renders a LogRecord using a
// user-defined format and writes the result to the configured outputs. It is
// safe for concurrent use.
type FormatLogger struct {
	format *logFormat
	mu     sync.Mutex
	w      io.Writer
}

// newLogger validates the configuration, compiles the format, and opens the
// configured outputs, returning the default FormatLogger.
func newLogger(cfg LogConfig) (*FormatLogger, error) {
	if strings.TrimSpace(cfg.Format) == "" {
		return nil, fmt.Errorf("logging: format must not be empty")
	}
	f, err := compileFormat(cfg.Format)
	if err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}

	outputs := cfg.Outputs
	if len(outputs) == 0 {
		outputs = []string{"console"}
	}

	var ws []io.Writer
	for _, o := range outputs {
		switch o {
		case "console", "stdout":
			ws = append(ws, os.Stdout)
		case "file":
			if cfg.File == "" {
				return nil, fmt.Errorf(`logging: "file" output requires a "file" path`)
			}
			file, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return nil, fmt.Errorf("logging: open log file: %w", err)
			}
			ws = append(ws, file)
		default:
			return nil, fmt.Errorf("logging: unknown output %q (want \"console\" or \"file\")", o)
		}
	}

	return &FormatLogger{format: f, w: io.MultiWriter(ws...)}, nil
}

// NeedsRequestBody reports whether the configured format references the request
// body, so the proxy can avoid buffering bodies that are never logged.
func (l *FormatLogger) NeedsRequestBody() bool { return l.format.refs["request_body"] }

// NeedsResponseBody reports whether the configured format references the
// response body, so the proxy only tees responses that are actually logged.
func (l *FormatLogger) NeedsResponseBody() bool { return l.format.refs["response_body"] }

// Log renders rec and writes a single line to all outputs. The write is
// serialized so concurrent requests do not interleave within a line.
func (l *FormatLogger) Log(rec *LogRecord) {
	line := l.format.render(rec)
	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(l.w, line+"\n")
}

// LogRecord holds everything observed about a single request/response exchange.
// Fields that were never reached (e.g. backend timing for a rejected request)
// stay at their zero value and render as "-". Its fields are exported so custom
// Logger implementations can read them.
type LogRecord struct {
	Req          Request
	Backend      *Backend  // selected upstream, nil when none was chosen
	ForwardTime  time.Time // sent to backend
	AppRespTime  time.Time // response received from backend
	EndTime      time.Time // response fully handled
	RequestBody  []byte
	ResponseBody []byte
	AppStatus    int // status returned by the backend, 0 if none
	Status       int // status returned to the client
	RespHeader   http.Header
}

// --- format compilation -----------------------------------------------------

// segment is one piece of a compiled format: either literal text (field == "")
// or a field reference with an optional parameter.
type segment struct {
	literal string
	field   string
	param   string
}

type logFormat struct {
	segments []segment
	refs     map[string]bool
}

// compileFormat parses a format string into segments. Field references are
// written as {field} or, for the parameterized fields, {group.param} — e.g.
// {req_header.User-Agent}, {resp_header.Content-Type}, {query.id}.
//
// Available fields:
//
//	method, url, path, request_body, response_body
//	backend_id        id of the selected backend
//	status            response status sent to the client
//	app_status        status returned by the backend
//	receive_time      request received from the client
//	forward_time      request sent to the backend
//	app_response_time response received from the backend
//	end_time          response fully handled
//	request_duration  end_time - receive_time
//	app_duration      app_response_time - forward_time
//	req_header.NAME   a single request header
//	resp_header.NAME  a single response header
//	query.NAME        a single query parameter
//	var.NAME          a request variable (see vars.go), e.g. var.remote_addr
func compileFormat(format string) (*logFormat, error) {
	f := &logFormat{refs: map[string]bool{}}
	for i := 0; i < len(format); {
		if format[i] != '{' {
			j := strings.IndexByte(format[i:], '{')
			if j < 0 {
				f.segments = append(f.segments, segment{literal: format[i:]})
				break
			}
			f.segments = append(f.segments, segment{literal: format[i : i+j]})
			i += j
			continue
		}

		end := strings.IndexByte(format[i:], '}')
		if end < 0 {
			return nil, fmt.Errorf("unterminated '{' in format")
		}
		name := format[i+1 : i+end]
		field, param := name, ""
		if dot := strings.IndexByte(name, '.'); dot >= 0 {
			field, param = name[:dot], name[dot+1:]
		}
		if err := validateField(field, param); err != nil {
			return nil, err
		}
		f.segments = append(f.segments, segment{field: field, param: param})
		f.refs[field] = true
		i += end + 1
	}
	return f, nil
}

func validateField(field, param string) error {
	switch field {
	case "method", "url", "path", "request_body", "response_body", "backend_id",
		"status", "app_status",
		"receive_time", "forward_time", "app_response_time", "end_time",
		"request_duration", "app_duration":
		if param != "" {
			return fmt.Errorf("field %q does not take a parameter", field)
		}
		return nil
	case "req_header", "resp_header", "query", "var":
		if param == "" {
			return fmt.Errorf("field %q requires a parameter, e.g. {%s.NAME}", field, field)
		}
		return nil
	default:
		return fmt.Errorf("unknown log field %q", field)
	}
}

const (
	logTimeFormat = "2006-01-02T15:04:05.000Z07:00"
	absent        = "-"
)

func (f *logFormat) render(rec *LogRecord) string {
	var b strings.Builder
	for _, s := range f.segments {
		if s.field == "" {
			b.WriteString(s.literal)
			continue
		}
		b.WriteString(renderField(rec, s.field, s.param))
	}
	return b.String()
}

func renderField(rec *LogRecord, field, param string) string {
	switch field {
	case "method":
		return rec.Req.Method
	case "url":
		return rec.Req.URL
	case "path":
		return rec.Req.Path
	case "request_body":
		if rec.RequestBody == nil {
			return absent
		}
		return string(rec.RequestBody)
	case "response_body":
		if rec.ResponseBody == nil {
			return absent
		}
		return string(rec.ResponseBody)
	case "backend_id":
		if rec.Backend == nil {
			return absent
		}
		return rec.Backend.ID
	case "status":
		return statusString(rec.Status)
	case "app_status":
		return statusString(rec.AppStatus)
	case "receive_time":
		return timeString(rec.Req.ReceivedAt)
	case "forward_time":
		return timeString(rec.ForwardTime)
	case "app_response_time":
		return timeString(rec.AppRespTime)
	case "end_time":
		return timeString(rec.EndTime)
	case "request_duration":
		return durationString(rec.Req.ReceivedAt, rec.EndTime)
	case "app_duration":
		return durationString(rec.ForwardTime, rec.AppRespTime)
	case "req_header":
		return valueOrAbsent(rec.Req.Header.Get(param))
	case "resp_header":
		if rec.RespHeader == nil {
			return absent
		}
		return valueOrAbsent(rec.RespHeader.Get(param))
	case "query":
		return valueOrAbsent(rec.Req.Query.Get(param))
	case "var":
		v, _ := requestVar(rec.Req, param)
		return valueOrAbsent(v)
	}
	return absent
}

func statusString(code int) string {
	if code == 0 {
		return absent
	}
	return strconv.Itoa(code)
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return absent
	}
	return t.Format(logTimeFormat)
}

func durationString(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return absent
	}
	return end.Sub(start).String()
}

func valueOrAbsent(s string) string {
	if s == "" {
		return absent
	}
	return s
}

// --- request/response capture plumbing --------------------------------------

// anyNeedsRequestBody reports whether any of the loggers reference the request
// body, so the proxy can skip buffering it when none do.
func anyNeedsRequestBody(ls []Logger) bool {
	for _, l := range ls {
		if l.NeedsRequestBody() {
			return true
		}
	}
	return false
}

// anyNeedsResponseBody reports whether any of the loggers reference the response
// body, so the proxy only tees responses that are actually logged.
func anyNeedsResponseBody(ls []Logger) bool {
	for _, l := range ls {
		if l.NeedsResponseBody() {
			return true
		}
	}
	return false
}

// captureBody reads the request body into rec and restores it so the backend
// still receives the full body. Used only when the log format references it.
func captureBody(r *http.Request, rec *LogRecord) {
	if r.Body == nil {
		return
	}
	data, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return
	}
	rec.RequestBody = data
	r.Body = io.NopCloser(bytes.NewReader(data))
}

// contextKey is a private type for context values to avoid collisions.
type contextKey int

const recordKey contextKey = iota

// loggingTransport records when a request is sent to the backend, when the
// response returns, and the backend's status code, into the LogRecord carried
// on the request context.
type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec, _ := r.Context().Value(recordKey).(*LogRecord)
	if rec != nil {
		rec.ForwardTime = time.Now()
	}
	resp, err := t.base.RoundTrip(r)
	if rec != nil {
		rec.AppRespTime = time.Now()
		if resp != nil {
			rec.AppStatus = resp.StatusCode
		}
	}
	return resp, err
}

// statusWriter wraps an http.ResponseWriter to capture the status code sent to
// the client. It forwards Flush and Hijack so streaming and connection upgrades
// (e.g. WebSockets) through the reverse proxy keep working.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	body   *bytes.Buffer // non-nil when the response body should be captured
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	if w.body != nil {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("switchyard: response writer does not support hijacking")
}
