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
	"github.com/mlnomadpy/dacli/cloud/internal/domain"
	"github.com/mlnomadpy/dacli/cloud/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "control-plane worker stopped:", err)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], config.LoadFile)
}

type configLoader func(string, config.LookupEnv) (config.Config, error)

func runArgs(args []string, load configLoader) error {
	flags := flag.NewFlagSet("dacli-cloud-worker", flag.ContinueOnError)
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
	logger.Info("starting control-plane worker", "component", domain.ComponentWorker, "contract_version", domain.ContractVersion, "config", cfg.SafeSummary())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := composition.Open(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = runtime.Close() }()
	worker.Run(ctx, cfg.WorkerInterval, cfg.RequestTimeout, worker.JobFunc(runtime.Cycle), func(err error) {
		logger.Error("control-plane worker cycle failed", "code", "worker_cycle_failed")
	})
	return nil
}
