// Package cleanup exposes content-addressed repository cleanup plans. The
// classifier and mutations live in store so worktree prune and cleanup share
// entity-level policy rather than feature slices importing one another.
package cleanup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "cleanup", Brief: "Plan or apply content-addressed, recoverable repository cleanup", JSON: true, Mutates: true, Usage: "dacli cleanup --project <slug> (--dry-run | --apply-safe <plan-id> | --restore <plan-id> --artifact <identity>)", Run: cmdCleanup},
	{Path: "branches audit", Brief: "Branch/worktree cleanup audit delegated to the canonical cleanup planner", JSON: true, Usage: "dacli branches audit --project <slug>", Run: cmdBranchesAudit},
	{Path: "branches prune", Brief: "Apply an exact cleanup plan; never deletes outside the canonical planner", JSON: true, Mutates: true, Usage: "dacli branches prune --project <slug> --apply-safe <plan-id>", Run: cmdBranchesPrune},
}

func cmdBranchesAudit(ctx *clikit.Ctx, args []string) error {
	return cmdCleanup(ctx, append(append([]string(nil), args...), "--dry-run"))
}

func cmdBranchesPrune(ctx *clikit.Ctx, args []string) error {
	return cmdCleanup(ctx, args)
}

func cmdCleanup(ctx *clikit.Ctx, args []string) error {
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("project", "dry-run", "apply-safe", "restore", "artifact"); err != nil {
		return err
	}
	project := f.Get("project")
	applyID := f.Get("apply-safe")
	restoreID := f.Get("restore")
	artifactID := f.Get("artifact")
	if project == "" || !workspace.SafeSegment(project) {
		return clikit.Usagef("--project requires a valid project slug")
	}
	modes := 0
	if f.Bool("dry-run") {
		modes++
	}
	if applyID != "" {
		modes++
	}
	if restoreID != "" {
		modes++
	}
	if modes != 1 || (restoreID != "") != (artifactID != "") {
		return clikit.Usagef("choose exactly one of --dry-run, --apply-safe <plan-id>, or --restore <plan-id> --artifact <identity>")
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if restoreID != "" {
		audit, err := store.RestoreRepositoryCleanupArtifact(w, project, restoreID, artifactID, time.Now())
		if err != nil {
			return clikit.Refusedf("artifact restore refused: %v", err)
		}
		if ctx.JSON {
			return json.NewEncoder(ctx.Stdout).Encode(audit)
		}
		fmt.Fprintf(ctx.Stdout, "restored artifact %s from cleanup plan %s\n", artifactID, restoreID)
		return nil
	}
	plan, err := store.PlanRepositoryCleanup(w, project, time.Now(), ctx.Cwd)
	if err != nil {
		return err
	}
	if f.Bool("dry-run") {
		return renderPlan(ctx, plan)
	}
	if applyID != plan.ID {
		return clikit.Refusedf("cleanup plan changed or is unknown (requested %s, current %s); review a new --dry-run", applyID, plan.ID)
	}
	audit, err := store.ApplyRepositoryCleanup(w, project, applyID, time.Now(), ctx.Cwd)
	if err != nil {
		return clikit.Refusedf("safe cleanup refused: %v", err)
	}
	if ctx.JSON {
		return json.NewEncoder(ctx.Stdout).Encode(audit)
	}
	fmt.Fprintf(ctx.Stdout, "applied cleanup plan %s; removed %d item(s), quarantined %d artifact(s), recorded %d sandbox recovery record(s)\n", plan.ID, len(audit.Removed), len(audit.Quarantined), len(audit.Sandboxes))
	for _, sandbox := range audit.Sandboxes {
		fmt.Fprintf(ctx.Stdout, "sandbox %s state=%s recovery=%s archive_bytes=%d\n  recover: dacli cleanup --project %s --restore %s --artifact %s\n", sandbox.Candidate.Path, sandbox.State, sandbox.Archive, sandbox.Bytes, project, plan.ID, sandbox.Candidate.Identity)
	}
	for _, artifact := range audit.Quarantined {
		fmt.Fprintf(ctx.Stdout, "quarantined artifact %s at %s\n  recover: %s\n", artifact.Path, artifact.Quarantine, artifact.Recovery)
	}
	for _, item := range audit.Removed {
		fmt.Fprintf(ctx.Stdout, "removed worktree %s and branch %s at %s\n", item.Worktree, item.Branch, item.Commit)
		for _, recovery := range item.Recovery {
			fmt.Fprintf(ctx.Stdout, "  recover: %s\n", recovery)
		}
	}
	return nil
}

func renderPlan(ctx *clikit.Ctx, plan store.CleanupPlan) error {
	if ctx.JSON {
		enc := json.NewEncoder(ctx.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}
	fmt.Fprintf(ctx.Stdout, "repository cleanup plan %s · schema %s · project %s · base %s@%s · current %s\n", plan.ID, plan.Schema, plan.Project, plan.Base, plan.BaseCommit, plan.CurrentBranch)
	for _, item := range plan.Items {
		verdict := "preserve"
		if item.Eligible {
			verdict = "eligible"
		}
		fmt.Fprintf(ctx.Stdout, "%s %s branch=%s task=%s status=%s owner=%s pr=%s commit=%s\n", verdict, item.Worktree, item.Branch, item.Task, item.TaskStatus, item.Owner, item.PRState, item.Commit)
		for _, pr := range item.PRHistory {
			fmt.Fprintf(ctx.Stdout, "  pr: #%d %s\n", pr.Number, pr.State)
		}
		for _, run := range item.Runs {
			fmt.Fprintf(ctx.Stdout, "  run: %s agent=%s state=%s claims=%v\n", run.ID, run.Agent, run.State, run.Claims)
		}
		for _, reason := range item.Reasons {
			fmt.Fprintf(ctx.Stdout, "  reason: %s\n", reason)
		}
		for _, operation := range item.Operations {
			fmt.Fprintf(ctx.Stdout, "  operation: %s\n", operation)
		}
		for _, recovery := range item.Recovery {
			fmt.Fprintf(ctx.Stdout, "  recovery: %s\n", recovery)
		}
	}
	for _, sandbox := range plan.Sandboxes {
		fmt.Fprintf(ctx.Stdout, "sandbox %s run=%s task=%s eligible=%t identity=%s bytes=%d\n  reason: %s\n", sandbox.Path, sandbox.RunID, sandbox.Task, sandbox.Eligible, sandbox.Identity, sandbox.Bytes, sandbox.Reason)
	}
	for _, artifact := range plan.Artifacts {
		fmt.Fprintf(ctx.Stdout, "artifact %s run=%s task=%s class=%s pruneable=%t identity=%s digest=%s\n  reason: %s\n", artifact.Path, artifact.RunID, artifact.Task, artifact.Classification, artifact.Pruneable, artifact.Identity, artifact.Digest, artifact.Reason)
		if artifact.Pruneable {
			fmt.Fprintf(ctx.Stdout, "  quarantine: %s\n  operation: %s\n  recovery: %s\n", artifact.Quarantine, artifact.Operation, artifact.Recovery)
		}
	}
	fmt.Fprintf(ctx.Stdout, "dry-run: nothing was written; apply this immutable plan with --apply-safe %s\n", plan.ID)
	return nil
}
