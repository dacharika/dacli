package nativegovernance

import (
	"fmt"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
)

// HostSlotCommands is proposed separately so the owner can register the
// schema-22 local-only mutator with its shared authorization invariants.
var HostSlotCommands = []clikit.Command{
	{Path: "native capacity slots upgrade", Brief: "Owner-upgrade native authority schema 21 to 22 for single-authority host-slot admission", Mutates: true, JSON: true, Usage: "dacli native capacity slots upgrade --config <private-owner-file> --json", Run: hostSlotsUpgrade},
}

func hostSlotsUpgrade(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config")
	if err != nil {
		return err
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
	}
	owner, err := application.OpenNativeCapacityOwner(f.Get("config"))
	if err != nil {
		return denied()
	}
	if err = owner.UpgradeSlots(); err != nil {
		return clikit.Refusedf("native capacity slots upgrade requires schema 21 and a valid private owner authority")
	}
	fmt.Fprintln(ctx.Stdout, `{"schema_version":1,"authority_schema":22,"upgraded":true}`)
	return nil
}
