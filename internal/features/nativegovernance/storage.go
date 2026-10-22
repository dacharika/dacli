package nativegovernance

import (
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
)

func storageCommand(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "config", "destination", "mode", "project", "json")
	if e != nil {
		return e
	}
	if len(f.Pos) != 0 {
		return clikit.Usagef("storage operations accept named flags only")
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
	}
	owner, e := application.OpenNativeStorageOwner(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	var out any
	switch kind {
	case "observe":
		if f.Get("destination") != "" || f.Get("mode") != "" || f.Get("project") != "" {
			return clikit.Usagef("observe accepts only --config")
		}
		out, e = owner.Observe()
	case "experiment":
		if f.Get("destination") == "" || f.Get("mode") == "" || f.Get("project") != "" {
			return clikit.Usagef("experiment requires --destination and --mode projection-only")
		}
		out, e = owner.Experiment(f.Get("destination"), f.Get("mode"))
	case "retention":
		if f.Get("project") == "" || f.Get("destination") != "" || f.Get("mode") != "" {
			return clikit.Usagef("retention requires --project")
		}
		out, e = owner.Retention(f.Get("project"))
	default:
		return clikit.Usagef("unknown storage operation")
	}
	if e != nil {
		return nativePolicyError(e)
	}
	result := struct {
		Schema string `json:"schema"`
		Result any    `json:"result"`
	}{"native-storage/v1", out}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintln(ctx.Stdout, "Owner storage observation completed; disposable projections never activate authority and retention is dry-run only.")
	return nil
}
