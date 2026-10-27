package nativegovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/operatorread"
)

const queueDiagnosticsUsage = "dacli native queue diagnostics --connection <private-file> --project <exact-slug> [--task <canonical-id>] [--limit 1..100] --json"

// QueueDiagnosticsCommands is registered by the app after this feature has
// supplied the complete read-only command contract.
var QueueDiagnosticsCommands = []clikit.Command{{
	Path:  "native queue diagnostics",
	Brief: "Read bounded native queue diagnostics for one exact project",
	JSON:  true,
	Usage: queueDiagnosticsUsage,
	Run:   nativeQueueDiagnostics,
}}

func nativeQueueDiagnostics(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "connection", "project", "task", "limit")
	if err != nil {
		return err
	}
	for _, name := range []string{"connection", "project"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	limit := 50
	if f.Get("limit") != "" {
		limit, err = strconv.Atoi(f.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return clikit.Usagef("--limit must be between 1 and 100")
		}
	}
	reader, err := application.OpenOperatorRead(f.Get("connection"))
	if err != nil {
		return clikit.Refusedf("native queue diagnostics denied")
	}
	snapshot, err := reader.ReadQueueDiagnostics(context.Background(), f.Get("project"), f.Get("task"), limit)
	if err != nil {
		if errors.Is(err, operatorread.ErrDenied) {
			return clikit.Refusedf("native queue diagnostics denied")
		}
		return fmt.Errorf("native queue diagnostics unavailable")
	}
	if ctx == nil || ctx.Stdout == nil {
		return fmt.Errorf("native queue diagnostics output unavailable")
	}
	if err := json.NewEncoder(ctx.Stdout).Encode(snapshot); err != nil {
		return fmt.Errorf("native queue diagnostics output unavailable")
	}
	return nil
}
