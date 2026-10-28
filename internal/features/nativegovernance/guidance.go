package nativegovernance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/guidance"
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
	f, err := flags(args, "config", "skill-root", "plan-out")
	if err != nil {
		return err
	}
	configPath, root, output := f.Get("config"), f.Get("skill-root"), f.Get("plan-out")
	if configPath == "" || root == "" || output == "" {
		return clikit.Usagef("--config <owner>, --skill-root and --plan-out are required")
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(output) {
		return clikit.Usagef("--skill-root and --plan-out must be absolute paths")
	}
	owner, err := guidancePlanOwner(configPath)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	diagnosis := guidance.Diagnose(guidance.DefaultRoots(home, root), "unknown", "unknown", "not_selected", "")
	var selected *guidance.Copy
	for i := range diagnosis.Copies {
		copy := &diagnosis.Copies[i]
		if filepath.Clean(copy.Root) == filepath.Clean(root) {
			selected = copy
		}
		if copy.Status != guidance.StatusMissing && copy.Status != guidance.StatusLegacy && copy.Status != guidance.StatusPreviousCurrent {
			return clikit.Refusedf("guidance refresh preview refused: another discoverable copy is %s; preserve all copies and resolve the conflict first", copy.Status)
		}
	}
	if selected == nil || (selected.Status != guidance.StatusLegacy && selected.Status != guidance.StatusPreviousCurrent) {
		return clikit.Refusedf("guidance refresh preview refused: selected copy is missing or has no supported before bundle")
	}
	for _, copy := range diagnosis.Copies {
		if copy.Status != guidance.StatusMissing && (copy.BundleIdentity != selected.BundleIdentity || copy.BundleDigest != selected.BundleDigest) {
			return clikit.Refusedf("guidance refresh preview refused: discoverable copies have different before bundles; resolve the conflict first")
		}
	}
	p, err := guidance.PreviewOwned(root, guidance.ReviewedSourceID, owner)
	if err != nil {
		return clikit.Refusedf("guidance refresh preview refused: %v", err)
	}
	if err := owner.Authorize(); err != nil {
		return err
	}
	if err := guidance.WritePlanOwned(output, p, owner); err != nil {
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
	owner, err := guidancePlanOwner(f.Get("config"))
	if err != nil {
		return err
	}
	receipt, err := guidance.ApplyOwned(plan, owner)
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
