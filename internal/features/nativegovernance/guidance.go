package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/guidance"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func nativeGuidanceDiagnose(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "skill-root", "runtime-capabilities")
	if err != nil {
		return err
	}
	if f.Get("skill-root") != "" && !filepath.IsAbs(f.Get("skill-root")) {
		return clikit.Usagef("--skill-root must be an absolute selected installation directory")
	}
	if f.Get("runtime-capabilities") != "" && !filepath.IsAbs(f.Get("runtime-capabilities")) {
		return clikit.Usagef("--runtime-capabilities must be an absolute selected manifest path")
	}
	home, _ := os.UserHomeDir()
	result := inspectInstalledGuidance(f.Get("skill-root"), f.Get("runtime-capabilities"))
	if home == "" {
		result.CombinedStatus = guidance.StatusUnsafe
	}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "Installed dacli guidance: %s; fresh process %s; cached connection %s.\n", result.CombinedStatus, result.FreshProcessState, result.CachedConnectionState)
	for _, copy := range result.Copies {
		fmt.Fprintf(ctx.Stdout, "%s: %s — %s\n", copy.Root, copy.Status, copy.NextAction)
	}
	return nil
}

func nativeGuidanceRefreshPreview(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "skill-root", "plan-out")
	if err != nil {
		return err
	}
	root, output := f.Get("skill-root"), f.Get("plan-out")
	if root == "" || output == "" {
		return clikit.Usagef("--skill-root and --plan-out are required")
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(output) {
		return clikit.Usagef("--skill-root and --plan-out must be absolute paths")
	}
	p, err := guidance.Preview(root, guidance.ReviewedSourceID)
	if err != nil {
		return clikit.Refusedf("guidance refresh preview refused: %v", err)
	}
	if err := guidance.WritePlan(output, p); err != nil {
		return clikit.Refusedf("could not create exclusive private refresh plan: %v", err)
	}
	ctx.Result = p
	if ctx.JSON {
		return clikit.EmitJSON(ctx, p)
	}
	fmt.Fprintf(ctx.Stdout, "Reviewed refresh plan %s targets %d allowlisted files; inspect it before explicit apply.\n", p.PlanDigest, len(p.Files))
	return nil
}

func nativeGuidanceRefreshApply(ctx *clikit.Ctx, args []string) error {
	f, err := flags(args, "config", "plan")
	if err != nil {
		return err
	}
	plan := f.Get("plan")
	if f.Get("config") == "" || plan == "" || !filepath.IsAbs(plan) {
		return clikit.Usagef("--config <owner> and --plan <absolute-private-reviewed-plan> are required")
	}
	ownerConfig, err := config(f.Get("config"))
	if err != nil {
		return denied()
	}
	credential, err := native.ReadCredential(ownerConfig.OwnerCredentialFile)
	if err != nil {
		return denied()
	}
	authorize := func() error {
		currentConfig, err := config(f.Get("config"))
		if err != nil || currentConfig != ownerConfig {
			return denied()
		}
		currentCredential, err := native.ReadCredential(currentConfig.OwnerCredentialFile)
		if err != nil || currentCredential != credential {
			return denied()
		}
		workspace, err := native.ResolveWorkspace(currentConfig.Workspace, "")
		if err != nil {
			return denied()
		}
		authority, err := sessions.OpenCurrent(currentConfig.Authority, sessions.Binding{Workspace: workspace.ID, Installation: currentConfig.InstallationID})
		if err != nil || authority.AuthenticateOwner(currentCredential) != nil {
			return denied()
		}
		return nil
	}
	if err := authorize(); err != nil {
		return err
	}
	receipt, err := guidance.ApplyAuthorized(plan, authorize)
	ctx.Result = receipt
	if ctx.JSON {
		if emitErr := clikit.EmitJSON(ctx, receipt); emitErr != nil {
			return emitErr
		}
	}
	if err != nil {
		if receipt.State == "recovery_required" {
			return fmt.Errorf("guidance refresh %s: %w", receipt.State, err)
		}
		return clikit.Refusedf("guidance refresh refused: %v", err)
	}
	if !ctx.JSON {
		return json.NewEncoder(ctx.Stdout).Encode(receipt)
	}
	return nil
}
