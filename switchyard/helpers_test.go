package switchyard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// echoBackend starts an httptest server that records the headers of the most
// recent request it received and echoes its identity in the response body. It
// is the standard fake upstream for black-box tests. The server is closed via
// t.Cleanup.
type echoBackend struct {
	*httptest.Server
	id string

	mu     sync.Mutex
	hits   int
	lastRH http.Header // headers seen on the last request
}

func newEchoBackend(t *testing.T, id string) *echoBackend {
	t.Helper()
	b := &echoBackend{id: id}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.hits++
		b.lastRH = r.Header.Clone()
		// Host is not part of Header; stash it so tests can assert Host rewrites.
		b.lastRH.Set("Host", r.Host)
		b.mu.Unlock()
		io.WriteString(w, id)
	}))
	t.Cleanup(b.Server.Close)
	return b
}

func (b *echoBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hits
}

// header returns the value the backend saw for key on its last request.
func (b *echoBackend) header(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastRH.Get(key)
}

// serve drives one request through the proxy handler in-process and returns the
// recorder. Backends are real httptest servers; only the proxy runs against a
// recorder, which keeps tests fast and deterministic.
func serve(p *sw.Proxy, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	p.Handler().ServeHTTP(rec, req)
	return rec
}

// mustNew builds a Proxy from cfg, failing the test on error.
func mustNew(t *testing.T, cfg sw.Config) *sw.Proxy {
	t.Helper()
	p, err := sw.New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return p
}

// --- fakes for custom-behavior (axis 3) tests --------------------------------

// recordingLogger captures every LogRecord it is given.
type recordingLogger struct {
	mu   sync.Mutex
	recs []*sw.LogRecord
	// reqBody/respBody control what the proxy buffers for this logger.
	reqBody, respBody bool
}

func (l *recordingLogger) Log(rec *sw.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, rec)
}
func (l *recordingLogger) NeedsRequestBody() bool  { return l.reqBody }
func (l *recordingLogger) NeedsResponseBody() bool { return l.respBody }

func (l *recordingLogger) records() []*sw.LogRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*sw.LogRecord(nil), l.recs...)
}

// fixedSelector always returns the backend at index idx (or nil for empty pool).
type fixedSelector struct{ idx int }

func (s fixedSelector) Select(pool []*sw.Backend, _ sw.Request) *sw.Backend {
	if len(pool) == 0 {
		return nil
	}
	return pool[s.idx%len(pool)]
}
