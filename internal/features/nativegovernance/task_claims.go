package nativegovernance

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

// taskClaimsPreview and taskClaimsApply are wired by the owner in the native
// command registry. Both deliberately use --config, never ambient DACLI_AGENT.
func taskClaimsPreview(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "task", "claims-file")
	if err != nil {
		return err
	}
	for _, key := range []string{"config", "task", "claims-file"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
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
	if err != nil || a.AuthenticateOwner(owner) != nil {
		return denied()
	}
	task, err := store.FindTask(w, f.Get("task"))
	if err != nil {
		return clikit.Refusedf("task path claims target unavailable")
	}
	data, err := native.ReadPrivate(f.Get("claims-file"), 65536)
	var request struct {
		Claims []string `json:"claims"`
	}
	if err != nil || native.Decode(data, &request) != nil {
		return denied()
	}
	plan, err := store.PlanTaskPathClaims(w, task, request.Claims, time.Now().UTC())
	if err != nil {
		return clikit.Refusedf("task path claims preview refused: %v", err)
	}
	return json.NewEncoder(ctx.Stdout).Encode(plan)
}

func taskClaimsApply(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "plan")
	if err != nil {
		return err
	}
	for _, key := range []string{"config", "plan"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
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
	if err != nil {
		return denied()
	}
	data, err := native.ReadPrivate(f.Get("plan"), 1<<20)
	var plan store.TaskPathClaimsPlan
	if err != nil || native.Decode(data, &plan) != nil {
		return denied()
	}
	task, err := store.FindTask(w, plan.TaskID)
	if err != nil {
		return clikit.Refusedf("task path claims target unavailable")
	}
	var applied store.TaskPathClaimsPlan
	err = a.WithOwnerTaskClaimsProtection(owner, task.ID, func(protection sessions.TaskClaimsProtection) error {
		if len(protection.Holds) > 0 {
			return fmt.Errorf("native task protections remain: %v", protection.Holds)
		}
		var applyErr error
		applied, applyErr = store.ApplyTaskPathClaims(w, task, plan, time.Now().UTC())
		return applyErr
	})
	if err != nil {
		return clikit.Refusedf("task path claims apply refused: %v", err)
	}
	return json.NewEncoder(ctx.Stdout).Encode(applied)
}
