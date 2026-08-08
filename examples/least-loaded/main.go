// Command least-loaded demonstrates capacity-aware backend selection via the
// SDK. Instead of round-robin, it sends each request to the backend with the
// most spare capacity (max_connections − in-flight), so backends configured
// with different limits (e.g. one allowing 2, another 10) receive traffic in
// proportion to their headroom.
//
// It relies only on the public capacity API — Backend.MaxConns / InFlight — and
// is assigned per proxy location, without touching Switchyard's source.
//
//	go run ./examples/least-loaded -config switchyard.json
package main

import (
	"flag"
	"log"
	"math"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// leastLoaded picks the backend with the greatest spare capacity. It embeds the
// default RoundRobinSelector purely as a tie-break/fallback for the (rare) case
// where every backend is unlimited or equally loaded.
type leastLoaded struct {
	sw.RoundRobinSelector
}

func (s *leastLoaded) Select(pool []*sw.Backend, req sw.Request) *sw.Backend {
	if len(pool) == 0 {
		return nil
	}
	best, bestSpare := pool[0], spare(pool[0])
	tie := true
	for _, b := range pool[1:] {
		sp := spare(b)
		if sp != bestSpare {
			tie = false
		}
		if sp > bestSpare {
			best, bestSpare = b, sp
		}
	}
	if tie {
		return s.RoundRobinSelector.Select(pool, req) // all equal → fall back to rotation
	}
	return best
}

// spare is the backend's remaining capacity; an unlimited backend (MaxConns 0)
// is treated as effectively infinite.
func spare(b *sw.Backend) int {
	if b.MaxConns() == 0 {
		return math.MaxInt
	}
	return b.MaxConns() - b.InFlight()
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

	// Use capacity-aware selection on every proxy location.
	for _, loc := range p.Locations {
		if loc.Kind == sw.KindProxy {
			loc.Selector = &leastLoaded{}
		}
	}
	log.Printf("switchyard: least-loaded backend selection enabled")

	if err := p.ListenAndServe(cfg.Listen); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
