package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func checkpointOwner(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "config", "request", "json")
	if e != nil {
		return e
	}
	if f.Get("config") == "" || kind != "upgrade" && f.Get("request") == "" {
		return clikit.Usagef("explicit private --config and --request required")
	}
	p, e := application.OpenNativeCheckpointOwner(f.Get("config"))
	if e != nil {
		return denied()
	}
	if kind == "upgrade" {
		if p.Upgrade() != nil {
			return clikit.Refusedf("owner schema11 to12 checkpoint upgrade refused")
		}
		fmt.Fprintln(ctx.Stdout, "Checkpoint schema12 configured; all prior history preserved.")
		return nil
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 1<<20)
	var r sessions.CheckpointPolicy
	if e != nil || nativeconfig.Decode(data, &r) != nil || p.Configure(r) != nil {
		return denied()
	}
	return json.NewEncoder(ctx.Stdout).Encode(r)
}
func checkpointCommand(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "connection", "capability-file", "request", "json")
	if e != nil {
		return e
	}
	for _, k := range []string{"connection", "capability-file", "request"} {
		if f.Get(k) == "" {
			return clikit.Usagef("explicit --%s required", k)
		}
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 1<<20)
	if e != nil {
		return denied()
	}
	p, e := application.OpenNativeSwarm(f.Get("connection"), f.Get("capability-file"))
	if e != nil {
		return denied()
	}
	var out sessions.CheckpointStatus
	if kind == "status" {
		var r struct{ ProjectID, CheckpointID string }
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		out, e = p.CheckpointStatus(r.ProjectID, r.CheckpointID)
	} else {
		var r sessions.CheckpointRequest
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		if kind == "record" {
			out, e = p.RecordCheckpoint(r)
		} else {
			out, e = p.ReconcileCheckpoint(r)
		}
	}
	if e != nil {
		return denied()
	}
	if e = json.NewEncoder(ctx.Stdout).Encode(out); e != nil {
		return e
	}
	if kind != "record" && out.HaltClass != "ready" {
		if out.Retryable {
			return fmt.Errorf("checkpoint halted: %s: %s", out.HaltClass, out.NextAction)
		}
		return clikit.Refusedf("checkpoint halted: %s: %s", out.HaltClass, out.NextAction)
	}
	return nil
}
