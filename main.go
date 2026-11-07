package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
    "time"
)

func main() {
	cfgPath := flag.String("config", "config.ini", "path to INI config file")
	setup := flag.Bool("setup", false, "interactively create config.ini from config.ini.example")
	example := flag.String("example", "config.ini.example", "example config used by -setup")
	flag.Parse()

	if *setup {
		if err := RunSetup(*example, *cfgPath); err != nil {
			log.Fatalf("setup: %v", err)
		}
		return
	}

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Fatalf("config: %v\nRun with -setup to create it.", err)
		}
		log.Fatalf("config: %v", err)
	}

	cl, err := Connect(cfg.Host, cfg.Port)
	if err != nil {
		log.Fatalf("connect to JS8Call API (%s:%d): %v", cfg.Host, cfg.Port, err)
	}
	defer cl.Close()
	log.Printf("connected to %s API at %s:%d", cfg.RadioName, cfg.Host, cfg.Port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lines := make(chan LogLine, 256)
	if cfg.LogDir != "" {
		go TailFile(ctx, filepath.Join(cfg.LogDir, cfg.AllFile), "ALL", lines)
		go TailFile(ctx, filepath.Join(cfg.LogDir, cfg.DirectedFile), "DIRECTED", lines)
	}

    state := JS8State{TXActive:        false,
                      QSOActive:       false,
                      QSOTarget:       "",
                      QSOFreq:         0,
                      PTT:             "off",
                      LastTX:          time.Now(),
                      LastQSOExchange: time.Now(),
                     }

	sc := NewScanner(cfg, cl, &state)
	go sc.HandleEvents(ctx)
	go sc.HandleLogs(ctx, lines)

	cl.GetFreq()
	sc.Run(ctx)
	log.Println("shutting down")
}
