package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func recoveryOperation(ctx *clikit.Ctx, args []string, kind string) error {
	allowed := []string{"config"}
	if kind != "upgrade" {
		allowed = append(allowed, "request", "json")
	}
	f, e := flags(args, allowed...)
	if e != nil {
		return e
	}
	if f.Get("config") == "" || (kind != "upgrade" && f.Get("request") == "") {
		return clikit.Usagef("explicit --config and --request required")
	}
	p, e := application.OpenNativeRecovery(f.Get("config"))
	if e != nil {
		return denied()
	}
	if kind == "upgrade" {
		if e = p.Upgrade(); e != nil {
			return clikit.Refusedf("explicit owner schema9 to10 recovery upgrade refused")
		}
		fmt.Fprintln(ctx.Stdout, "Recovery schema10 configured; history preserved.")
		return nil
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 1<<20)
	if e != nil {
		return denied()
	}
	var result any
	switch kind {
	case "configure":
		var r sessions.RecoveryPolicy
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		e = p.Configure(r)
		result = r
	case "preview":
		var r struct{ Kind, ProtectionID string }
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Preview(r.Kind, r.ProtectionID)
	case "apply":
		var r sessions.RecoveryRequest
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Apply(r)
	default:
		return denied()
	}
	if e != nil {
		return clikit.Refusedf("owner recovery refused: exact target, fence, immutable replay or terminal assurance/reconciliation mismatch; preserve uncertain protection")
	}
	return json.NewEncoder(ctx.Stdout).Encode(result)
}
