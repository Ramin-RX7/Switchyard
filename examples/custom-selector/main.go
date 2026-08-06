// Command custom-selector demonstrates Mode B (SDK) usage of Switchyard: import
// the library, override one pluggable stage — here the backend selector — and
// compile your own binary, all without editing any file in the switchyard
// package.
//
// It replaces the default round-robin selection on the "/api/" location with a
// sticky-by-client-IP strategy: the same client always reaches the same backend
// (useful for naive session affinity). Every other location keeps the built-in
// round-robin, and the config-driven behavior is otherwise identical to the
// turnkey binary.
//
// Run it against a JSON config just like the turnkey binary:
//
//	go run ./examples/custom-selector -config switchyard.json
package main

import (
	"flag"
	"hash/fnv"
	"log"
	"net"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// stickyByIP is a custom BackendSelector. It embeds RoundRobinSelector so it can
// fall back to plain rotation when the client address is unavailable — this is
// the "keep the default logic, override just the part you care about" pattern.
type stickyByIP struct {
	sw.RoundRobinSelector
}

// Select hashes the client IP to a stable index into the pool. When the address
// can't be parsed it defers to the embedded round-robin default.
func (s *stickyByIP) Select(pool []*sw.Backend, req sw.Request) *sw.Backend {
	if len(pool) == 0 {
		return nil
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	if host == "" {
		return s.RoundRobinSelector.Select(pool, req)
	}
	h := fnv.New32a()
	h.Write([]byte(host))
	return pool[int(h.Sum32())%len(pool)]
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

	// Override the selector on the proxy location configured for "/api/".
	// Everything else — routing, headers, logging, other locations — is
	// untouched and behaves exactly as the config specifies.
	for _, loc := range p.Locations {
		if loc.Kind == sw.KindProxy && loc.Path() == "/api/" {
			loc.Selector = &stickyByIP{}
			log.Printf("switchyard: sticky-by-IP selector installed on location %q", loc.Path())
		}
	}

	if err := p.ListenAndServe(cfg.Listen); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
