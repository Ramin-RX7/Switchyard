package switchyard

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Request is Switchyard's internal representation of an incoming HTTP request.
// It is captured once per request and is the basis for any further handling.
type Request struct {
	Method     string
	Path       string // relative route, e.g. /v1/users
	Host       string
	Scheme     string // http or https
	URL        string // complete request URL, e.g. http://host/v1/users?id=1
	RawQuery   string
	RemoteAddr string
	Query      url.Values
	Header     http.Header
	ReceivedAt time.Time // when the request was received from the client
}

// captureRequest builds the internal representation from an incoming request.
// Header is cloned so the snapshot stays stable even when set_headers later
// modifies the headers forwarded to the backend.
func captureRequest(r *http.Request) Request {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return Request{
		Method:     r.Method,
		Path:       r.URL.Path,
		Host:       r.Host,
		Scheme:     scheme,
		URL:        scheme + "://" + r.Host + r.URL.RequestURI(),
		RawQuery:   r.URL.RawQuery,
		RemoteAddr: r.RemoteAddr,
		Query:      r.URL.Query(),
		Header:     r.Header.Clone(),
		ReceivedAt: time.Now(),
	}
}

func (req Request) String() string {
	return fmt.Sprintf("%s %s host=%s from=%s", req.Method, req.Path, req.Host, req.RemoteAddr)
}
