package nativegovernance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
)

const nativeLandingUsage = "dacli native landing record --config <private-owner-file> --project <project> --request <private-file> --json"

// LandingCommands keeps owner-only batch evidence recording outside the worker
// MCP catalog. It records observed delivery, without merging or accepting it.
var LandingCommands = []clikit.Command{{
	Path:    "native landing record",
	Brief:   "Owner-record exact native task source and reviewed batch landing",
	Mutates: true,
	JSON:    true,
	Usage:   nativeLandingUsage,
	Run:     recordNativeLanding,
}}

func recordNativeLanding(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "project", "request")
	if err != nil {
		return err
	}
	for _, name := range []string{"config", "project", "request"} {
		if f.Get(name) == "" {
			return clikit.Usagef("--%s is required", name)
		}
	}
	data, err := nativeconfig.ReadPrivate(f.Get("request"), 64<<10)
	var request application.NativeLandingRequest
	if err != nil || nativeconfig.Decode(data, &request) != nil {
		return clikit.Refusedf("native landing request denied")
	}
	owner, err := application.OpenNativeLandingOwner(f.Get("config"), f.Get("project"), observeNativeLandingPR)
	if err != nil {
		return clikit.Refusedf("native landing owner authentication denied")
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	observation, err := owner.Record(callCtx, request)
	if err != nil {
		return clikit.Refusedf("native landing record refused; inspect exact source, task, merged PR, target verification and current observation")
	}
	if ctx == nil || ctx.Stdout == nil {
		return fmt.Errorf("native landing output unavailable")
	}
	if err := json.NewEncoder(ctx.Stdout).Encode(observation); err != nil {
		return fmt.Errorf("native landing output unavailable")
	}
	return nil
}
