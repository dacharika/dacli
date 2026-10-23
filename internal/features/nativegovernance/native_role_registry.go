package nativegovernance

import (
	"fmt"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func nativeRoleRegistryUpgrade(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config")
	if err != nil {
		return err
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
	}
	c, err := config(f.Get("config"))
	if err != nil {
		return err
	}
	w, err := native.ResolveWorkspace(c.Workspace, "")
	if err != nil {
		return denied()
	}
	owner, err := native.ReadCredential(c.OwnerCredentialFile)
	if err != nil {
		return denied()
	}
	a, err := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if err != nil || a.UpgradeNativeRoleRegistry(owner) != nil {
		return denied()
	}
	fmt.Fprintln(ctx.Stdout, "Native signed role provenance schema upgraded to 20.")
	return nil
}
