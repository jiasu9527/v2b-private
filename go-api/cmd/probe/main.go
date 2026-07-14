package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"forest/go-api/internal/probeagent"
)

func main() {
	configPath := flag.String("config", "/etc/forest-probe/config.json", "path to 0600 probe configuration")
	apiURL := flag.String("api_url", "", "probe API URL")
	token := flag.String("token", "", "probe token")
	interval := flag.Duration("interval", 0, "heartbeat interval")
	version := flag.String("version", "", "probe version")
	allowLocalHTTP := flag.Bool("allow-local-http", false, "allow HTTP only for localhost testing")
	flag.Parse()
	cfg, err := probeagent.LoadConfig(*configPath)
	if err != nil && (*apiURL == "" || *token == "") {
		log.Fatalf("load probe configuration: %v", err)
	}
	if *apiURL != "" {
		cfg.APIURL = *apiURL
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *interval > 0 {
		cfg.Interval = *interval
	}
	if *version != "" {
		cfg.Version = *version
	}
	if cfg.Version == "" {
		cfg.Version = "forest-probe"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	options := []probeagent.Option{}
	if *allowLocalHTTP {
		options = append(options, probeagent.WithInsecureLocalHTTP())
	}
	agent, err := probeagent.New(cfg, options...)
	if err != nil {
		log.Fatalf("invalid probe configuration: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := agent.Run(ctx); err != nil {
		log.Fatalf("probe stopped: %v", err)
	}
}
