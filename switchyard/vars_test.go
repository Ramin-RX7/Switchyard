package switchyard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func testRequest() Request {
	h := http.Header{}
	h.Set("User-Agent", "curl/8")
	h.Set("Origin", "https://app.example.com")
	return Request{
		Method:     "POST",
		Path:       "/v1/users",
		Host:       "api.example.com",
		Scheme:     "https",
		RawQuery:   "id=1&sort=asc",
		RemoteAddr: "203.0.113.5:54321",
		Header:     h,
	}
}

func TestRequestVar(t *testing.T) {
	req := testRequest()
	tests := []struct {
		name   string
		want   string
		wantOK bool
	}{
		{"remote_addr", "203.0.113.5", true},
		{"remote_port", "54321", true},
		{"host", "api.example.com", true},
		{"scheme", "https", true},
		{"request_method", "POST", true},
		{"request_uri", "/v1/users?id=1&sort=asc", true},
		{"uri", "/v1/users", true},
		{"args", "id=1&sort=asc", true},
		{"query_string", "id=1&sort=asc", true},
		{"http_user_agent", "curl/8", true},
		{"http_origin", "https://app.example.com", true},
		{"http_missing", "", true}, // http_* is always "known", empty when absent
		{"not_a_var", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := requestVar(req, tt.name)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("requestVar(%q) = (%q, %v), want (%q, %v)", tt.name, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestRequestVarURIWithoutQuery(t *testing.T) {
	req := testRequest()
	req.RawQuery = ""
	if got, _ := requestVar(req, "request_uri"); got != "/v1/users" {
		t.Errorf("request_uri without query = %q, want /v1/users", got)
	}
}

func TestHostAndPortPart(t *testing.T) {
	if got := hostPart("203.0.113.5:54321"); got != "203.0.113.5" {
		t.Errorf("hostPart = %q", got)
	}
	if got := hostPart("no-port"); got != "no-port" {
		t.Errorf("hostPart(no-port) = %q, want passthrough", got)
	}
	if got := portPart("203.0.113.5:54321"); got != "54321" {
		t.Errorf("portPart = %q", got)
	}
	if got := portPart("no-port"); got != "" {
		t.Errorf("portPart(no-port) = %q, want empty", got)
	}
}

func TestCompileTemplateAndRender(t *testing.T) {
	req := testRequest()
	tests := []struct {
		tmpl string
		want string
	}{
		{"literal only", "literal only"},
		{"$remote_addr", "203.0.113.5"},
		{"ip=$remote_addr;", "ip=203.0.113.5;"},
		{"${scheme}://${host}", "https://api.example.com"},
		{"$remote_addr:$remote_port", "203.0.113.5:54321"},
		{"cost is $ 5", "cost is $ 5"}, // lone '$' is literal
		{"ua=$http_user_agent", "ua=curl/8"},
	}
	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			tpl, err := compileTemplate(tt.tmpl)
			if err != nil {
				t.Fatalf("compileTemplate(%q): %v", tt.tmpl, err)
			}
			if got := tpl.render(req); got != tt.want {
				t.Errorf("render(%q) = %q, want %q", tt.tmpl, got, tt.want)
			}
		})
	}
}

func TestCompileTemplateRejectsUnknownVar(t *testing.T) {
	for _, bad := range []string{"$nope", "${also_nope}", "prefix $bad_var suffix"} {
		if _, err := compileTemplate(bad); err == nil {
			t.Errorf("compileTemplate(%q) = nil error, want failure on unknown variable", bad)
		}
	}
}

// TemplateHeaderSetter.Apply renders values against the request and treats Host
// specially (it must update r.Host, not the header map).
func TestTemplateHeaderSetterApply(t *testing.T) {
	hs, err := newHeaderSetter(map[string]string{
		"X-Real-IP": "$remote_addr",
		"Host":      "upstream.local",
	})
	if err != nil {
		t.Fatalf("newHeaderSetter: %v", err)
	}
	r := httptest.NewRequest("GET", "http://orig.example/", nil)
	hs.Apply(testRequest(), r)

	if got := r.Header.Get("X-Real-IP"); got != "203.0.113.5" {
		t.Errorf("X-Real-IP = %q, want 203.0.113.5", got)
	}
	if r.Host != "upstream.local" {
		t.Errorf("r.Host = %q, want upstream.local (Host special-case)", r.Host)
	}
	if got := r.Header.Get("Host"); got != "" {
		t.Errorf("Host header map = %q, want empty (Host is not set via the map)", got)
	}
}
