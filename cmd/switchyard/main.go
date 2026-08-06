// Command switchyard is the turnkey, config-only reverse proxy: point it at a
// JSON config and run, nginx-style, with no Go code required.
//
// It is also the reference example of using Switchyard as a library — everything
// it does (LoadConfig, New, ListenAndServe) is exported, so an SDK user can
// start from this exact behavior and override any pluggable stage. See
// github.com/Ramin-RX7/Switchyard and examples/ for customization.
package main

import (
	"flag"
	"log"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

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

	if err := p.ListenAndServe(cfg.Listen); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
