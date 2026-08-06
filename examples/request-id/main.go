// Command request-id demonstrates overriding the "set_headers" stage via the SDK.
//
// It replaces the global HeaderApplier with one that *embeds* the config-built
// default (TemplateHeaderSetter), calls it so all configured set_headers still
// apply, and then adds an incrementing X-Request-Id header to every forwarded
// request. This is the "keep the config, extend the logic" pattern applied to a
// Phase-2 stage, and maps to the "request ID injection" roadmap item.
//
// Run it against a JSON config just like the turnkey binary:
//
//	go run ./examples/request-id -config switchyard.json
package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// withRequestID embeds the default header setter and adds X-Request-Id. The
// embedded *TemplateHeaderSetter may be nil (when the config has no set_headers);
// Apply guards for that.
type withRequestID struct {
	*sw.TemplateHeaderSetter
	n atomic.Uint64
}

func (h *withRequestID) Apply(req sw.Request, r *http.Request) {
	if h.TemplateHeaderSetter != nil {
		h.TemplateHeaderSetter.Apply(req, r) // keep all configured set_headers
	}
	r.Header.Set("X-Request-Id", strconv.FormatUint(h.n.Add(1), 10))
}

func main() {
	configPath := flag.String("config", "switchyard.json", "path to configuration file")
	flag.Parse()

	cfg, err := sw.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}

	p, err := sw.New(cfg)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}

	// Wrap the config-built default (if any) so set_headers still apply.
	base, _ := p.Headers.(*sw.TemplateHeaderSetter)
	p.Headers = &withRequestID{TemplateHeaderSetter: base}
	log.Printf("switchyard: X-Request-Id injection enabled")

	if err := p.ListenAndServe(cfg.Listen); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
