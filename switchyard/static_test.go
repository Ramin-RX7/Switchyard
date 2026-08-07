package switchyard_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func staticProxy(t *testing.T) (*sw.Proxy, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello media\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := sw.Config{Locations: []sw.LocationConfig{{Path: "/media/", Type: "static", Root: dir}}}
	return mustNew(t, cfg), dir
}

// --- axis 1 & 2: default FileServer ----------------------------------------

func TestStaticServesFileWithPrefixStripped(t *testing.T) {
	p, _ := staticProxy(t)
	rec := serve(p, "GET", "http://x/media/hello.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "hello media\n" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello media\n")
	}
}

func TestStaticMissingFileIs404(t *testing.T) {
	p, _ := staticProxy(t)
	if rec := serve(p, "GET", "http://x/media/missing.txt"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- axis 3: a user-supplied StaticServer is honored -----------------------

type sentinelStatic struct{}

func (sentinelStatic) Serve(w http.ResponseWriter, _ *http.Request, _ sw.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("static-sentinel"))
}

func TestCustomStaticServerHonored(t *testing.T) {
	p, _ := staticProxy(t)
	p.Locations[0].Static = sentinelStatic{}
	rec := serve(p, "GET", "http://x/media/whatever")
	if rec.Body.String() != "static-sentinel" {
		t.Errorf("body = %q, want static-sentinel", rec.Body.String())
	}
}
