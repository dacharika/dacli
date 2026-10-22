package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func budgetOwner(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "config", "request", "json")
	if e != nil {
		return e
	}
	if f.Get("config") == "" || kind != "upgrade" && f.Get("request") == "" {
		return clikit.Usagef("explicit private --config and --request required")
	}
	p, e := application.OpenNativeBudgetOwner(f.Get("config"))
	if e != nil {
		return denied()
	}
	if kind == "upgrade" {
		if p.Upgrade() != nil {
			return clikit.Refusedf("owner schema12 to13 budget upgrade refused")
		}
		fmt.Fprintln(ctx.Stdout, "Budget schema13 configured; prior history preserved; provider usage remains unknown.")
		return nil
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 1<<20)
	var r sessions.BudgetPolicy
	if e != nil || nativeconfig.Decode(data, &r) != nil || p.Configure(r) != nil {
		return denied()
	}
	return json.NewEncoder(ctx.Stdout).Encode(r)
}
func budgetCommand(ctx *clikit.Ctx, args []string, kind string) error {
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
	var out any
	if kind == "settle" {
		var r sessions.BudgetSettleRequest
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		out, e = p.SettleBudget(r)
	} else {
		var r struct{ ProjectID, TaskID string }
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		if kind == "status" {
			out, e = p.BudgetStatus(r.ProjectID)
		} else {
			q, err := p.QuoteBudget(r.ProjectID, r.TaskID)
			if err != nil {
				return denied()
			}
			if e = json.NewEncoder(ctx.Stdout).Encode(q); e != nil {
				return e
			}
			if !q.Allowed {
				return clikit.Refusedf("estimated budget admission refused: %s", q.Reason)
			}
			return nil
		}
	}
	if e != nil {
		return denied()
	}
	return json.NewEncoder(ctx.Stdout).Encode(out)
}
