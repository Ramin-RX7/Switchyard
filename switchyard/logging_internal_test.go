package switchyard

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestCompileFormatValidation(t *testing.T) {
	valid := []string{
		"{method} {path} {status}",
		"{backend_id} {app_status} {request_duration} {app_duration}",
		"{req_header.User-Agent} {resp_header.Content-Type} {query.id} {var.remote_addr}",
		"plain text no fields",
	}
	for _, f := range valid {
		if _, err := compileFormat(f); err != nil {
			t.Errorf("compileFormat(%q) unexpected error: %v", f, err)
		}
	}

	invalid := []string{
		"{not_a_field}", // unknown field
		"{req_header}",  // parameterized field missing its param
		"{method.oops}", // simple field given a param
		"{unterminated", // no closing brace
	}
	for _, f := range invalid {
		if _, err := compileFormat(f); err == nil {
			t.Errorf("compileFormat(%q) = nil error, want failure", f)
		}
	}
}

func TestLogFormatRender(t *testing.T) {
	h := http.Header{}
	h.Set("X-Test", "hi")
	rec := &LogRecord{
		Req: Request{
			Method:     "GET",
			Path:       "/v1/users",
			RemoteAddr: "203.0.113.5:5",
			Header:     h,
			Query:      url.Values{"id": {"42"}},
		},
		Backend: &Backend{ID: "api1"},
		Status:  200,
	}
	f, err := compileFormat("{method} {path} {status} {backend_id} {var.remote_addr} {req_header.X-Test} {query.id}")
	if err != nil {
		t.Fatal(err)
	}
	want := "GET /v1/users 200 api1 203.0.113.5 hi 42"
	if got := f.render(rec); got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
}

func TestLogFormatRenderAbsentValues(t *testing.T) {
	rec := &LogRecord{Req: Request{Method: "GET", Path: "/"}} // no backend, no status, zero times
	f, _ := compileFormat("{backend_id} {status} {forward_time} {app_duration}")
	if got := f.render(rec); got != "- - - -" {
		t.Errorf("render of absent values = %q, want '- - - -'", got)
	}
}

func TestFieldFormatters(t *testing.T) {
	if statusString(0) != "-" || statusString(200) != "200" {
		t.Error("statusString")
	}
	if timeString(time.Time{}) != "-" {
		t.Error("timeString(zero) should be -")
	}
	if durationString(time.Time{}, time.Now()) != "-" {
		t.Error("durationString with a zero endpoint should be -")
	}
	if valueOrAbsent("") != "-" || valueOrAbsent("x") != "x" {
		t.Error("valueOrAbsent")
	}
}

func TestFormatLoggerNeedsBody(t *testing.T) {
	withReq, _ := newLogger(LogConfig{Format: "{request_body}"})
	if !withReq.NeedsRequestBody() || withReq.NeedsResponseBody() {
		t.Error("format referencing request_body: NeedsRequestBody=true, NeedsResponseBody=false")
	}
	withResp, _ := newLogger(LogConfig{Format: "{response_body}"})
	if withResp.NeedsRequestBody() || !withResp.NeedsResponseBody() {
		t.Error("format referencing response_body: NeedsResponseBody=true, NeedsRequestBody=false")
	}
	neither, _ := newLogger(LogConfig{Format: "{method}"})
	if neither.NeedsRequestBody() || neither.NeedsResponseBody() {
		t.Error("format referencing neither body should need no buffering")
	}
}
