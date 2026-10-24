package nativegovernance

import (
	"fmt"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func dispatchUpgrade(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
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
	a, e := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	if e = a.UpgradeDispatchIntents(owner); e != nil {
		return clikit.Refusedf("native dispatch upgrade requires schema 7 and valid private owner authority; preserve storage for owner reconciliation")
	}
	fmt.Fprintln(ctx.Stdout, "Native authority upgraded from schema 7 to 8; existing authority and receipts preserved.")
	return nil
}
