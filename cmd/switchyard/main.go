// Command switchyard is the turnkey, config-only reverse proxy: point it at a
// JSON config and run, nginx-style, with no Go code required.
//
//	switchyard [-config FILE] [-pidfile FILE]   # run the server (default)
//	switchyard reload [--force] [-pidfile FILE] # tell a running server to reload
//
// It is also the reference example of using Switchyard as a library — everything
// it does (LoadConfig, New, Server) is exported, so an SDK user can start from
// this exact behavior and override any pluggable stage. See
// github.com/Ramin-RX7/Switchyard and examples/ for customization.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	sw "github.com/Ramin-RX7/Switchyard/switchyard"
)

func main() {
	// Subcommand dispatch; the default (no subcommand) runs the server.
	if len(os.Args) > 1 && os.Args[1] == "reload" {
		if err := runReload(os.Args[2:]); err != nil {
			log.Fatalf("switchyard: %v", err)
		}
		return
	}
	if err := runServe(os.Args[1:]); err != nil {
		log.Fatalf("switchyard: %v", err)
	}
}

// runServe loads the config and serves it with hot reload enabled (SIGHUP /
// SIGUSR2, or the `switchyard reload` command via the pid file).
func runServe(args []string) error {
	fs := flag.NewFlagSet("switchyard", flag.ExitOnError)
	configPath := fs.String("config", "switchyard.json", "path to configuration file")
	pidFile := fs.String("pidfile", "switchyard.pid", "path to write the server PID (empty to disable reload signalling)")
	_ = fs.Parse(args)

	// Read the config once up front for the listen address and to fail fast on a
	// bad initial config. Build re-reads it on every reload so config changes are
	// picked up without a restart.
	cfg, err := sw.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	build := func() (*sw.Proxy, error) {
		c, err := sw.LoadConfig(*configPath)
		if err != nil {
			return nil, err
		}
		return sw.New(c)
	}
	srv := &sw.Server{Addr: cfg.Listen, PidFile: *pidFile, Build: build}
	return srv.Run()
}

// runReload signals a running server (found via its pid file) to reload.
func runReload(args []string) error {
	fs := flag.NewFlagSet("switchyard reload", flag.ExitOnError)
	force := fs.Bool("force", false, "cancel in-flight requests (503) instead of draining them")
	pidFile := fs.String("pidfile", "switchyard.pid", "path to the running server's PID file")
	_ = fs.Parse(args)

	if err := sw.SignalReload(*pidFile, *force); err != nil {
		return err
	}
	mode := "graceful"
	if *force {
		mode = "force"
	}
	fmt.Printf("switchyard: sent %s reload signal (pidfile %s)\n", mode, *pidFile)
	return nil
}
