package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlnomadpy/dacli/cloud/internal/composition"
	"github.com/mlnomadpy/dacli/cloud/internal/config"
	"github.com/mlnomadpy/dacli/cloud/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "control-plane API stopped:", err)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], config.LoadFile)
}

type configLoader func(string, config.LookupEnv) (config.Config, error)

func runArgs(args []string, load configLoader) error {
	flags := flag.NewFlagSet("dacli-cloud-api", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "cloud/config.development.json", "path to strict JSON configuration")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	cfg, err := load(*configPath, nil)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger.Info("starting control-plane API", "config", cfg.SafeSummary())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := composition.Open(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = runtime.Close() }()
	return service.RunAPI(ctx, cfg, runtime.API)
}
