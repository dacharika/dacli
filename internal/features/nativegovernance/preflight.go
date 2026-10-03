package nativegovernance

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

func preflightOwner(ctx *clikit.Ctx, args []string, kind string) error {
	allowed := []string{"config"}
	if kind != "upgrade" {
		allowed = append(allowed, "request", "json")
	}
	f, e := flags(args, allowed...)
	if e != nil {
		return e
	}
	if f.Get("config") == "" || kind != "upgrade" && f.Get("request") == "" {
		return clikit.Usagef("explicit --config and private --request required")
	}
	p, e := application.OpenNativePreflightOwner(f.Get("config"))
	if e != nil {
		return denied()
	}
	if kind == "upgrade" {
		if p.Upgrade() != nil {
			return clikit.Refusedf("explicit owner schema10 to11 preflight upgrade refused")
		}
		fmt.Fprintln(ctx.Stdout, "Preflight schema11 configured; existing history preserved.")
		return nil
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 1<<20)
	if e != nil {
		return denied()
	}
	var result any
	switch kind {
	case "configure":
		var r sessions.PreflightPolicy
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		e = p.Configure(r)
		result = r
	case "override":
		var r sessions.CapacityOverrideRequest
		if nativeconfig.Decode(data, &r) != nil {
			return denied()
		}
		result, e = p.Override(r)
	default:
		return denied()
	}
	if e != nil {
		return clikit.Refusedf("owner preflight configuration or exact expiring task-size override refused")
	}
	return json.NewEncoder(ctx.Stdout).Encode(result)
}
func preflightCheck(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection", "capability-file", "request", "json")
	if e != nil {
		return e
	}
	for _, k := range []string{"connection", "capability-file", "request"} {
		if f.Get(k) == "" {
			return clikit.Usagef("explicit private --connection, --capability-file and --request required")
		}
	}
	data, e := nativeconfig.ReadPrivate(f.Get("request"), 16384)
	var r struct{ ProjectID, TaskID, OperationKey string }
	if e != nil || nativeconfig.Decode(data, &r) != nil {
		return denied()
	}
	p, e := application.OpenNativeSwarm(f.Get("connection"), f.Get("capability-file"))
	if e != nil {
		return denied()
	}
	report, e := p.Preflight(r.ProjectID, r.TaskID, r.OperationKey)
	if e != nil {
		return denied()
	}
	if e = json.NewEncoder(ctx.Stdout).Encode(report); e != nil {
		return e
	}
	if !report.Report.Allowed {
		if report.Report.FailureClass == "transient" {
			return fmt.Errorf("%w: retry only through the bounded preflight operation budget", sessions.ErrPreflightTransient)
		}
		return clikit.Refusedf("complete-loop preflight blocked: inspect versioned phase remedies; permanent refusals are final")
	}
	return nil
}

func nativePreflightFailure(err error, message string) error {
	if errors.Is(err, sessions.ErrPreflightTransient) {
		return fmt.Errorf("%w: reconcile or consume only a bounded preflight retry; do not redispatch uncertain execution", sessions.ErrPreflightTransient)
	}
	return clikit.Refusedf("%s", message)
}
