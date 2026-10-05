package nativegovernance

import (
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// RegisterModelRoutingCommands contributes owner-only policy commands. The
// catalog owner wires this contribution; no public bearer configuration exists.
func ModelRoutingCommands() []clikit.Command {
	return []clikit.Command{
		{Path: "native model-routing upgrade", Brief: "explicit owner schema17 model routing upgrade", Mutates: true, Usage: "dacli native model-routing upgrade --config <private-owner>", Run: func(ctx *clikit.Ctx, args []string) error { return modelRoutingOwner(ctx, args, true) }},
		{Path: "native model-routing configure", Brief: "configure owner-approved native difficulty/model routes", Mutates: true, JSON: true, Usage: "dacli native model-routing configure --config <private-owner> --request <policy-json> [--json]", Run: func(ctx *clikit.Ctx, args []string) error { return modelRoutingOwner(ctx, args, false) }},
	}
}
func ModelRoutingAuthenticatesExplicitly(path string) bool {
	return path == "native model-routing upgrade" || path == "native model-routing configure"
}
func modelRoutingOwner(ctx *clikit.Ctx, args []string, upgrade bool) error {
	f, e := flags(args, "config", "request", "json")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
	}
	owner, e := application.OpenNativeModelRoutingOwner(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	if upgrade {
		if f.Get("request") != "" {
			return clikit.Usagef("upgrade does not accept --request")
		}
		if e = owner.Upgrade(); e != nil {
			return nativePolicyError(e)
		}
		fmt.Fprintln(ctx.Stdout, "Native model routing authority explicitly upgraded to schema17.")
		return nil
	}
	var policy sessions.NativeModelRoutingPolicy
	if e = nativePolicyRequest(f, &policy); e != nil {
		return e
	}
	if e = owner.Configure(policy); e != nil {
		return nativePolicyError(e)
	}
	return controllerEmit(ctx, struct{ ProjectID, Revision, Harness string }{policy.ProjectID, policy.Revision, policy.Harness})
}
