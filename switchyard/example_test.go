package switchyard_test

import (
	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

// ExampleNew shows the config-only (turnkey-equivalent) usage: load a config,
// build the proxy, and serve. These examples have no "// Output:" line, so
// `go test` compile-checks them without running the blocking server.
func ExampleNew() {
	cfg, err := sw.LoadConfig("switchyard.json")
	if err != nil {
		panic(err)
	}
	p, err := sw.New(cfg)
	if err != nil {
		panic(err)
	}
	_ = p.ListenAndServe(cfg.Listen)
}

// stickySelector always routes a given client IP to the same backend.
type stickySelector struct{ sw.RoundRobinSelector }

func (s *stickySelector) Select(pool []*sw.Backend, req sw.Request) *sw.Backend {
	if len(pool) == 0 {
		return nil
	}
	var sum int
	for i := 0; i < len(req.RemoteAddr); i++ {
		sum += int(req.RemoteAddr[i])
	}
	return pool[sum%len(pool)]
}

// Example_customSelector shows overriding a single stage (backend selection)
// on a specific location without touching Switchyard's source.
func Example_customSelector() {
	cfg, _ := sw.LoadConfig("switchyard.json")
	p, _ := sw.New(cfg)
	for _, loc := range p.Locations {
		if loc.Kind == sw.KindProxy && loc.Path() == "/api/" {
			loc.Selector = &stickySelector{}
		}
	}
	_ = p.ListenAndServe(cfg.Listen)
}
