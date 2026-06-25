package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// Logger renders a logRecord using a user-defined format and writes the result
// to the configured outputs. It is safe for concurrent use.
type Logger struct {
	format *logFormat
	mu     sync.Mutex
	w      io.Writer
}

// newLogger validates the configuration, compiles the format, and opens the
// configured outputs.
func newLogger(cfg LogConfig) (*Logger, error) {
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

	return &Logger{format: f, w: io.MultiWriter(ws...)}, nil
}

// needsRequestBody reports whether the configured format references the request
// body, so the proxy can avoid buffering bodies that are never logged.
func (l *Logger) needsRequestBody() bool { return l.format.refs["request_body"] }

// needsResponseBody reports whether the configured format references the
// response body, so the proxy only tees responses that are actually logged.
func (l *Logger) needsResponseBody() bool { return l.format.refs["response_body"] }

// log renders rec and writes a single line to all outputs. The write is
// serialized so concurrent requests do not interleave within a line.
func (l *Logger) log(rec *logRecord) {
	line := l.format.render(rec)
	l.mu.Lock()
	defer l.mu.Unlock()
	io.WriteString(l.w, line+"\n")
}

// logRecord holds everything observed about a single request/response exchange.
// Fields that were never reached (e.g. backend timing for a rejected request)
// stay at their zero value and render as "-".
type logRecord struct {
	req          Request
	backend      *backend  // selected upstream, nil when none was chosen
	forwardTime  time.Time // sent to backend
	appRespTime  time.Time // response received from backend
	endTime      time.Time // response fully handled
	requestBody  []byte
	responseBody []byte
	appStatus    int // status returned by the backend, 0 if none
	status       int // status returned to the client
	respHeader   http.Header
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
	case "req_header", "resp_header", "query":
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

func (f *logFormat) render(rec *logRecord) string {
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

func renderField(rec *logRecord, field, param string) string {
	switch field {
	case "method":
		return rec.req.Method
	case "url":
		return rec.req.URL
	case "path":
		return rec.req.Path
	case "request_body":
		if rec.requestBody == nil {
			return absent
		}
		return string(rec.requestBody)
	case "response_body":
		if rec.responseBody == nil {
			return absent
		}
		return string(rec.responseBody)
	case "backend_id":
		if rec.backend == nil {
			return absent
		}
		return rec.backend.id
	case "status":
		return statusString(rec.status)
	case "app_status":
		return statusString(rec.appStatus)
	case "receive_time":
		return timeString(rec.req.ReceivedAt)
	case "forward_time":
		return timeString(rec.forwardTime)
	case "app_response_time":
		return timeString(rec.appRespTime)
	case "end_time":
		return timeString(rec.endTime)
	case "request_duration":
		return durationString(rec.req.ReceivedAt, rec.endTime)
	case "app_duration":
		return durationString(rec.forwardTime, rec.appRespTime)
	case "req_header":
		return valueOrAbsent(rec.req.Header.Get(param))
	case "resp_header":
		if rec.respHeader == nil {
			return absent
		}
		return valueOrAbsent(rec.respHeader.Get(param))
	case "query":
		return valueOrAbsent(rec.req.Query.Get(param))
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
