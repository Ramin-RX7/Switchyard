package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

const defaultListen = ":8091"

func main() {
	configPath := flag.String("config", "switchyard.json", "path to configuration file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}

	p, err := newProxy(cfg)
	if err != nil {
		log.Fatalf("switchyard: %v", err)
	}

	listen := cfg.Listen
	if listen == "" {
		listen = defaultListen
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)

	srv := &http.Server{
		Addr:    listen,
		Handler: mux,
		// Defensive timeouts against slow clients. Read/write body timeouts are
		// left unset so legitimate slow or large proxied responses are not cut off.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("switchyard: listening on %s, %d backend(s)", listen, len(p.backends))
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("switchyard: server stopped: %v", err)
	}
}
