package nativegovernance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

const controllerDriverUsage = "dacli native controller drive --profile <private-file> --request <private-file> [--json]"

// controllerDrive uses only the explicitly selected native actors. The CLI
// supplies no host backend or independent review decision: unsupported effects
// stop before new reservations, while existing authorized producers can reconcile.
func controllerDrive(c *clikit.Ctx, args []string) error {
	f, err := flags(args, "profile", "request", "json")
	if err != nil {
		return err
	}
	if f.Get("profile") == "" || f.Get("request") == "" {
		return clikit.Usagef("%s", controllerDriverUsage)
	}
	var request driver.Request
	if err = nativePolicyRequest(f, &request); err != nil {
		return err
	}
	selected, err := application.OpenNativeControllerDriver(f.Get("profile"), nil, nil)
	if err != nil {
		return clikit.Refusedf("native controller driver selection refused; inspect the explicit private profile")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, runErr := selected.Run(ctx, request)
	if result.Schema != driver.ResultSchema && runErr == nil {
		return fmt.Errorf("native controller driver returned no verified result")
	}
	if result.Schema == driver.ResultSchema {
		c.Result = result
		if c.JSON {
			if err = clikit.EmitJSON(c, result); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(c.Stdout, "Native controller driver: %s (%s). %s\n", result.Outcome, result.Reason, result.NextAction)
		}
	}
	if runErr == nil {
		if result.Outcome == "refused" {
			return clikit.Refusedf("native controller driver refused; inspect the original operation and current scope, never retry unchanged")
		}
		return nil
	}
	if errors.Is(runErr, driver.ErrInvalidRequest) || errors.Is(runErr, driver.ErrInvalidLimits) {
		return clikit.Usagef("native controller driver request or bounds are invalid")
	}
	if errors.Is(runErr, sessions.ErrDenied) || errors.Is(runErr, executor.ErrDenied) {
		return clikit.Refusedf("native controller driver refused; inspect the original operation and current scope, never retry unchanged")
	}
	// Producer errors may contain private profile or command details. Preserve
	// the typed public result, but do not print an untrusted producer error.
	return fmt.Errorf("native controller driver interrupted; reconcile original operations before resuming")
}
