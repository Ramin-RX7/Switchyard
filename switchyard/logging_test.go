package switchyard_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// --- axis 1: default FormatLogger writes to a file -------------------------

func TestFileLoggerWritesLine(t *testing.T) {
	a := newEchoBackend(t, "api1")
	logPath := filepath.Join(t.TempDir(), "access.log")
	cfg := sw.Config{
		Backends: []sw.BackendConfig{{ID: "api1", URL: a.URL}},
		Logging:  &sw.LogConfig{Outputs: []string{"file"}, File: logPath, Format: "{method} {path} {status} {backend_id}"},
	}
	p := mustNew(t, cfg)

	serve(p, "GET", "http://x/users")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if want := "GET /users 200 api1"; !strings.Contains(string(data), want) {
		t.Errorf("log = %q, want it to contain %q", string(data), want)
	}
}

// --- axis 2/3: global + location loggers both fire -------------------------

func TestGlobalAndLocationLoggersBothFire(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	global := &recordingLogger{}
	loc := &recordingLogger{}
	p.Logger = global
	p.Locations[0].Logger = loc

	serve(p, "GET", "http://x/api/users")

	if len(global.records()) != 1 {
		t.Errorf("global logger fired %d times, want 1", len(global.records()))
	}
	if len(loc.records()) != 1 {
		t.Errorf("location logger fired %d times, want 1", len(loc.records()))
	}
}

// --- axis 3: a custom Logger captures the record fields --------------------

func TestCustomLoggerCapturesRecord(t *testing.T) {
	p, _, _ := twoBackendProxy(t)
	rl := &recordingLogger{}
	p.Logger = rl

	serve(p, "GET", "http://x/api/users")

	recs := rl.records()
	if len(recs) != 1 {
		t.Fatalf("captured %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Req.Path != "/api/users" {
		t.Errorf("Req.Path = %q, want /api/users", rec.Req.Path)
	}
	if rec.Backend == nil || rec.Backend.ID == "" {
		t.Errorf("Backend = %+v, want a selected backend", rec.Backend)
	}
	if rec.Status != 200 {
		t.Errorf("Status = %d, want 200", rec.Status)
	}
}

// Bodies are captured only when the active logger asks for them.
func TestBodyCaptureIsOnDemand(t *testing.T) {
	run := func(reqBody, respBody bool) *sw.LogRecord {
		p, _, _ := twoBackendProxy(t)
		rl := &recordingLogger{reqBody: reqBody, respBody: respBody}
		p.Logger = rl
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "http://x/api/echo", strings.NewReader("ping"))
		p.Handler().ServeHTTP(rec, req)
		recs := rl.records()
		if len(recs) != 1 {
			t.Fatalf("want 1 record, got %d", len(recs))
		}
		return recs[0]
	}

	on := run(true, true)
	if string(on.RequestBody) != "ping" {
		t.Errorf("RequestBody = %q, want ping", on.RequestBody)
	}
	if string(on.ResponseBody) != "api1" { // echo backend writes its id
		t.Errorf("ResponseBody = %q, want api1", on.ResponseBody)
	}

	off := run(false, false)
	if off.RequestBody != nil || off.ResponseBody != nil {
		t.Errorf("bodies buffered without being referenced: req=%v resp=%v", off.RequestBody, off.ResponseBody)
	}
}
