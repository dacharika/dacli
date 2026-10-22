package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func lifecycleOwner(ctx *clikit.Ctx, args []string, upgrade bool) error {
	allowed := []string{"config"}
	if !upgrade {
		allowed = append(allowed, "policy-file")
	}
	f, e := flags(args, allowed...)
	if e != nil {
		return e
	}
	if f.Get("config") == "" || (!upgrade && f.Get("policy-file") == "") {
		return clikit.Usagef("explicit --config and configured policy file required")
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	owner, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil || a.AuthenticateOwner(owner) != nil {
		return denied()
	}
	if upgrade {
		e = a.UpgradeLifecycle(owner)
	} else {
		data, readErr := native.ReadPrivate(f.Get("policy-file"), 65536)
		var p sessions.LifecyclePolicy
		if readErr != nil || native.Decode(data, &p) != nil {
			return denied()
		}
		e = a.ConfigureLifecycle(owner, p)
	}
	if e != nil {
		return clikit.Refusedf("native lifecycle owner policy or explicit schema 8 to 9 upgrade refused; preserve authority for reconciliation")
	}
	fmt.Fprintln(ctx.Stdout, "Native lifecycle authority configured; history preserved and host reports remain cooperative.")
	return nil
}
func lifecycleOperation(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "connection", "request", "json")
	if e != nil {
		return e
	}
	if f.Get("connection") == "" || f.Get("request") == "" {
		return clikit.Usagef("--connection and --request required")
	}
	p, e := application.OpenNativeLifecycle(f.Get("connection"))
	if e != nil {
		return denied()
	}
	data, e := native.ReadPrivate(f.Get("request"), 1<<20)
	if e != nil {
		return denied()
	}
	var result any
	switch kind {
	case "observe":
		var r sessions.ObserveLifecycleRequest
		if native.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Observe(r)
	case "cancel":
		var r sessions.CancelLifecycleRequest
		if native.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Cancel(r)
	case "cancel-ack":
		var r sessions.AcknowledgeCancellationRequest
		if native.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Acknowledge(r)
	case "status":
		var r struct{ DispatchID string }
		if native.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Status(r.DispatchID)
	default:
		return denied()
	}
	if e != nil {
		return clikit.Refusedf("native lifecycle refused: authenticated assigned actor, exact binding, immutable replay, sequence or owner cancellation policy mismatch")
	}
	return json.NewEncoder(ctx.Stdout).Encode(result)
}
