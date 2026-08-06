package switchyard

import (
	"net/http"
	"strings"
)

// StaticServer serves a request from local storage. It is the pluggable "media
// serving" stage, used by static locations. The default is FileServer (Go's
// http.FileServer over a directory); an SDK user may supply their own — e.g.
// serving from an embedded FS, object storage, or with custom cache headers.
type StaticServer interface {
	Serve(w http.ResponseWriter, r *http.Request, req Request)
}

// FileServer is the default StaticServer: it serves files from a directory,
// stripping a path prefix first. http.FileServer/http.Dir handle content types,
// range requests, and path-traversal protection.
type FileServer struct {
	root        string
	stripPrefix string
	fs          http.Handler
}

// newFileServer builds a FileServer rooted at root, stripping stripPrefix from
// the request path before lookup.
func newFileServer(root, stripPrefix string) *FileServer {
	return &FileServer{root: root, stripPrefix: stripPrefix, fs: http.FileServer(http.Dir(root))}
}

// Serve strips the configured prefix from the request path and serves the file.
func (s *FileServer) Serve(w http.ResponseWriter, r *http.Request, req Request) {
	upath := req.Path
	if s.stripPrefix != "" {
		upath = strings.TrimPrefix(upath, s.stripPrefix)
	}
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = upath
	s.fs.ServeHTTP(w, r2)
}
