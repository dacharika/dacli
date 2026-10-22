// Package planning is the work-definition slice: projects, tasks, risks,
// and the glossary — the objects every other slice reads.
package planning

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gates"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/spm"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "project connect", Brief: "Bind a local project to verified remote metadata sync", JSON: true, Mutates: true, Usage: "dacli project connect <slug> --provider <url> --tenant <id> --device <id> --remote-project <id> --environment <id> --repository <id>", Run: cmdProjectConnect},
	{Path: "project disconnect", Brief: "Preserve a local connection tombstone without disabling local work", JSON: true, Mutates: true, Usage: "dacli project disconnect <slug>", Run: cmdProjectDisconnect},
	{Path: "project sync", Brief: "Queue allowlisted signed metadata and receive proposal-only records", JSON: true, Mutates: true, Usage: "dacli project sync <slug>", Run: cmdProjectSync},
	{Path: "project status", Brief: "Inspect cloud connection health, cursor and pending backlog", JSON: true, Usage: "dacli project status <slug>", Run: cmdProjectStatus},
	{Path: "project add", Brief: "Create a project", Mutates: true, Usage: "dacli project add <title> [--slug s] [--goal g] [--stage definition|elicitation|approach|design] [--landing-mode local|pr] [--landing-base BRANCH]", Run: cmdProjectAdd},
	{Path: "project list", Brief: "List projects", Usage: "dacli project list", Run: cmdProjectList},
	{Path: "project show", Brief: "Show a project and configure its effective landing policy when landing flags are supplied", JSON: true, Mutates: true, Usage: "dacli project show <slug> [--landing-mode local|pr] [--landing-base BRANCH]", Run: cmdProjectShow},
	{Path: "project rm", Brief: "Delete a project and everything filed under it (irreversible; requires --force)", Mutates: true, Usage: "dacli project rm <slug> --force", Run: cmdProjectRm},
	{Path: "task add", Brief: "Create a task", Usage: "dacli task add <title> --project <slug> [--priority must|should|could|wont] [--estimate o,m,p] [--accept criterion]... [--so-that why] [--parent ref] [--depends-on ref[:TYPE]]... [--force]", Run: cmdTaskAdd},
	{Path: "task list", Brief: "List tasks, optionally by status", JSON: true, Usage: "dacli task list [--project slug] [--status open|active|blocked|done]", Run: cmdTaskList},
	{Path: "task show", Brief: "Show a task", JSON: true, Usage: "dacli task show <ref>", Run: cmdTaskShow},
	{Path: "task claim", Brief: "Take ownership of a task", Usage: "dacli task claim <ref>", Run: cmdTaskClaim},
	{Path: "task takeover", Brief: "Root recovers an orphaned unfinished task with an audited reason", Mutates: true, Usage: "dacli task takeover <ref> --force --reason \"why recovery is safe\"", Run: cmdTaskTakeover},
	{Path: "task check", Brief: "Check acceptance boxes (--n N or --all)", Usage: "dacli task check <ref> [--n N | --all] [--verify command] [--full-output]", Run: cmdTaskCheck},
	{Path: "task done", Brief: "Task owner closes self-performed work only after every acceptance box and required verification pass; spawned workers propose, they do not close", Usage: "dacli task done <ref> [--allow-unverified]", Run: cmdTaskDone},
	{Path: "task block", Brief: "Mark a task blocked", Usage: "dacli task block <ref> [--by ref] [--why text]", Run: cmdTaskBlock},
	{Path: "task reopen", Brief: "Reopen a wrongly-closed task, clearing its acceptance boxes (--reason required)", Mutates: true, Usage: "dacli task reopen <ref> --reason \"<what makes the close wrong>\"", Run: cmdTaskReopen},
	{Path: "task rm", Brief: "Remove a task that should never have existed; owners may remove their own, while rw root may recover a child-owned task only after its owner is no longer live; history, active, and done tasks require --force", Mutates: true, Usage: "dacli task rm <ref> [--force]", Run: cmdTaskRm},
	{Path: "task estimate", Brief: "Size an existing task: --estimate o,m,p (three-point; a scalar hides the risk). Sizing the backlog is what makes critical-path and `next --parallel` work", Usage: "dacli task estimate <ref> --estimate o,m,p [--force]   (optimistic,probable,pessimistic)", Run: cmdTaskEstimate},
	{Path: "task depend", Brief: "Add or remove validated typed dependency edges on an existing task; non-owners propose for sync", Usage: "dacli task depend <ref> [--add dep[:FS|SS|FF|SF]]... [--remove dep[:FS|SS|FF|SF]]...", Run: cmdTaskDepend},
	{Path: "task aggregate", Brief: "Preview or apply an immutable repair that makes a descriptive parent a derived aggregate", JSON: true, Mutates: true, Usage: "dacli task aggregate <ref> (--dry-run | --apply plan-id)", Run: cmdTaskAggregate},
	{Path: "task decompose", Brief: "Preview or explicitly apply a stable WBS proposal for an oversized leaf", JSON: true, Mutates: true, Usage: "dacli task decompose <ref> (--dry-run | --apply plan-id)", Run: cmdTaskDecompose},
	{Path: "risk add", Brief: "Record a risk in the impact x likelihood matrix", Usage: "dacli risk add <title> --project <slug> --impact high|medium|low --likelihood high|medium|low [--indicator text]... [--action text]", Run: cmdRiskAdd},
	{Path: "risk list", Brief: "List risks by rank; rank 1 and 2 require an action plan", Usage: "dacli risk list <project>", Run: cmdRiskList},
	{Path: "glossary", Brief: "Show or edit the project term list", Usage: "dacli glossary <project> [--term t --def text]", Run: cmdGlossary},
}

func cmdProjectAdd(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("goal", "slug", "stage", "template", "landing-mode", "landing-base"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli project add <title> [--slug s] [--goal g] [--stage definition|elicitation|approach|design]")
	}
	if err := clikit.RequireRW(id, "creating a project"); err != nil {
		return err
	}
	title := strings.Join(f.Pos, " ")
	policy, _, err := landingPolicyFromFlags(model.LandingPolicy{}, f)
	if err != nil {
		return clikit.Usagef("%v", err)
	}
	p, err := store.CreateProject(w, id.ID, title, f.Get("slug"), f.Get("goal"), f.Get("stage"), policy)
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "project %s created (stage: %s)\n", p.Slug, p.Stage)

	// --template attaches controlled steps at birth. Solo is the default by
	// absence: no template means no gates, per TEMPLATES.md § 2. When the flag
	// is omitted, fall back to the workspace default `dacli init --template`
	// recorded, so init's seeding actually reaches the first project.
	tmpl := f.Get("template")
	if tmpl == "" {
		tmpl = w.DefaultTemplate
	}
	if tmpl != "" && tmpl != "solo" {
		first, err := gates.Attach(w, p.Slug, tmpl)
		if err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "template %s attached (stage: %s)\n", tmpl, first.Name)
	}
	return nil
}

func cmdProjectList(ctx *clikit.Ctx, args []string) error {
	// This command takes no flags, so ANY flag is a typo. An empty allowlist
	// rejects every one — without it a mistyped flag was dropped and the
	// command ran as if nothing were wrong.
	if f, ferr := clikit.ParseFlags(args); ferr != nil {
		return ferr
	} else if err := f.Reject(); err != nil {
		return err
	}
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	ps, err := store.ListProjects(w)
	if err != nil {
		return err
	}
	for _, p := range ps {
		fmt.Fprintf(ctx.Stdout, "%-16s %-12s %s\n", p.Slug, p.Stage, p.Title)
	}
	return nil
}

func cmdProjectShow(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("landing-mode", "landing-base"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli project show <slug>")
	}
	p, err := store.LoadProject(w, f.Pos[0])
	if err != nil {
		return err
	}
	override, explicit, err := landingOverrideFromFlags(f)
	if err != nil {
		return clikit.Usagef("%v", err)
	}
	effective, _, err := model.ResolveLanding(p.Landing, override)
	if err != nil {
		return clikit.Usagef("%v", err)
	}
	if explicit {
		if err := clikit.RequireRW(id, "configuring project landing policy"); err != nil {
			return err
		}
		// These flags are documented as durable project configuration, not a
		// one-command display override. Save and reload before rendering so the
		// reported effective policy is exactly what later ship/integrate calls see.
		p, err = store.UpdateProjectLanding(w, f.Pos[0], override)
		if err != nil {
			return err
		}
		effective, explicit = p.Landing, false
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, struct {
			Slug       string              `json:"slug"`
			Title      string              `json:"title"`
			Configured model.LandingPolicy `json:"landing_configured"`
			Effective  model.LandingPolicy `json:"landing_effective"`
			Override   bool                `json:"landing_override"`
		}{p.Slug, p.Title, p.Landing, effective, explicit})
	}
	fmt.Fprint(ctx.Stdout, mdstore.Render(p.Doc))
	fmt.Fprintf(ctx.Stdout, "\nLanding configured: mode=%s base=%s\nLanding effective: mode=%s base=%s (override: %t)\n", p.Landing.Mode, clikit.OrDash(p.Landing.Base, "<repository default>"), effective.Mode, clikit.OrDash(effective.Base, "<repository default>"), explicit)
	return nil
}

func landingPolicyFromFlags(config model.LandingPolicy, f *clikit.Flags) (model.LandingPolicy, bool, error) {
	override, explicit, err := landingOverrideFromFlags(f)
	if err != nil {
		return model.LandingPolicy{}, false, err
	}
	policy, _, err := model.ResolveLanding(config, override)
	return policy, explicit, err
}

func landingOverrideFromFlags(f *clikit.Flags) (model.LandingOverride, bool, error) {
	var override model.LandingOverride
	if len(f.All("landing-mode")) > 0 {
		modeValue, err := oneLandingFlagValue("landing-mode", f.All("landing-mode"))
		if err != nil {
			return model.LandingOverride{}, false, err
		}
		mode := model.LandingMode(modeValue)
		override.Mode = &mode
	}
	if len(f.All("landing-base")) > 0 {
		base, err := oneLandingFlagValue("landing-base", f.All("landing-base"))
		if err != nil {
			return model.LandingOverride{}, false, err
		}
		override.Base = &base
	}
	return override, override.Mode != nil || override.Base != nil, nil
}

// oneLandingFlagValue rejects ambiguous repeated configuration. Taking the
// last value would make a generated invocation silently select a policy its
// earlier flag contradicted.
func oneLandingFlagValue(name string, values []string) (string, error) {
	for _, value := range values[1:] {
		if value != values[0] {
			return "", fmt.Errorf("conflicting --%s values", name)
		}
	}
	return values[0], nil
}

// cmdProjectRm is the recovery path for a project created by mistake (e.g. an
// `adopt` that guessed the wrong slug, see task 118): it deletes the project
// directory and everything filed under it — tasks, notes, risks, glossary.
// That is irreversible, so it refuses without --force and reports the size of
// the blast radius (task count) so the caller isn't guessing at it.
func cmdProjectRm(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("force"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli project rm <slug> --force")
	}
	slug := f.Pos[0]
	if !id.CanMutate("") {
		return clikit.Refusedf("%s cannot delete a project", id.MutateRefusal())
	}
	p, err := store.LoadProject(w, slug)
	if err != nil {
		return err
	}
	tasks, err := store.ListTasks(w, slug, "")
	if err != nil {
		return err
	}
	if !f.Bool("force") {
		return clikit.Refusedf("project %s (%q) has %d task(s) filed under it — rm deletes the whole project directory (tasks, notes, risks, glossary), irreversibly; re-run with --force to confirm", p.Slug, p.Title, len(tasks))
	}
	if err := store.DeleteProject(w, slug); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "project %s deleted (%d task(s) removed with it)\n", slug, len(tasks))
	return nil
}

func cmdTaskAdd(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("project", "force", "priority", "estimate", "accept", "so-that", "context", "depends-on", "parent"); err != nil {
		return err
	}
	if len(f.Pos) == 0 || f.Get("project") == "" {
		return clikit.Usagef("usage: dacli task add <title> --project <slug> [--priority must|should|could|wont] [--estimate o,m,p] [--accept criterion]... [--so-that why] [--parent ref] [--depends-on ref[:TYPE]]... [--force]")
	}
	title := strings.Join(f.Pos, " ")
	if est := f.Get("estimate"); est != "" {
		if err := store.ValidateEstimate(est); err != nil {
			return clikit.Usagef("%v", err)
		}
	}

	// Ambiguity lint on the title, before the task exists. Titles get the
	// strict pass because a vague title becomes three different deliverables.
	if finds := spm.Scan(title, spm.Options{}); len(finds) > 0 {
		fmt.Fprintf(ctx.Stderr, "warning: ambiguous title —\n")
		for _, fd := range finds {
			fmt.Fprintf(ctx.Stderr, "  %s\n", fd)
		}
	}

	// Near-duplicate dedup against the open backlog, before the task exists.
	// A review auditor that re-discovers an issue a prior cycle already
	// queued must be told "already filed", not left to manufacture a second
	// task for the same work (dacli task 116).
	if !f.Bool("force") {
		problem := strings.TrimSpace(strings.Join([]string{f.Get("so-that"), f.Get("context")}, "\n"))
		if dup, score, err := store.FindNearDuplicateTaskContent(w, f.Get("project"), store.TaskSimilarityInput{
			Title: title, Problem: problem, Acceptance: f.All("accept"),
		}); err != nil {
			return err
		} else if dup != nil {
			return clikit.Refusedf("candidate is a %.0f%% near-duplicate of %s task %03d-%s (%q) — check it before filing, or re-run with --force to file anyway", score*100, dup.Status, dup.Seq, dup.Slug, dup.Title)
		}
	}

	for _, dep := range f.All("depends-on") {
		if _, err := store.NormalizeDependency(dep); err != nil {
			return clikit.Usagef("%v", err)
		}
	}
	t, err := store.CreateTask(w, id.ID, f.Get("project"), title, store.TaskOpts{
		Priority:  f.Get("priority"),
		Estimate:  f.Get("estimate"),
		Accept:    f.All("accept"),
		SoThat:    f.Get("so-that"),
		Context:   f.Get("context"),
		DependsOn: f.All("depends-on"),
		Parent:    f.Get("parent"),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "task %03d-%s created (%s)\n", t.Seq, t.Slug, t.ID)
	if len(t.Acceptance()) == 0 {
		fmt.Fprintf(ctx.Stderr, "warning: no acceptance criteria — an agent given this task cannot know when to stop (add --accept)\n")
	}
	return nil
}

func cmdTaskList(ctx *clikit.Ctx, args []string) error {
	if ctx.JSON {
		return cmdTaskListJSON(ctx, args)
	}
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	r, err := application.NewCooperativeContext(w, id)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Both the text and JSON list paths need this: a dropped --status filter
	// silently lists EVERY task, which reads as a correct answer to a question
	// nobody asked. The 175 sweep missed both, and an invariant test over the
	// exit-code contract is what found them (dacli 202).
	if err := f.Reject("project", "status"); err != nil {
		return err
	}
	status, serr := model.ParseStatus(f.Get("status"))
	if serr != nil {
		return clikit.Usagef("%v", serr)
	}
	ts, err := application.ListTasks(r, f.Get("project"), status)
	if err != nil {
		return err
	}
	for _, t := range ts {
		boxes := t.Acceptance()
		done := 0
		for _, b := range boxes {
			if b.Done {
				done++
			}
		}
		progress := fmt.Sprintf("[%d/%d]", done, len(boxes))
		if t.IsAggregate() {
			p, progressErr := store.AggregateProgressFor(w, t)
			if progressErr != nil {
				return progressErr
			}
			progress = fmt.Sprintf("[aggregate %d/%d]", p.RequiredDone, p.Required)
		}
		if completion := t.CompletionState(); completion != "" {
			progress += " [" + completion + "]"
		}
		fmt.Fprintf(ctx.Stdout, "%-10s %03d-%-28s %-8s %-7s %-18s %s\n",
			t.Project, t.Seq, t.Slug, t.Status, t.Priority(), progress, t.Title)
	}
	return nil
}

type taskJSON struct {
	ID              string                   `json:"id"`
	Seq             int                      `json:"seq"`
	Slug            string                   `json:"slug"`
	Project         string                   `json:"project"`
	Status          string                   `json:"status"`
	CompletionState string                   `json:"completion_state,omitempty"`
	Priority        string                   `json:"priority,omitempty"`
	Title           string                   `json:"title"`
	Done            int                      `json:"acceptance_done"`
	Total           int                      `json:"acceptance_total"`
	Kind            string                   `json:"task_kind"`
	Aggregate       *store.AggregateProgress `json:"aggregate,omitempty"`
}

func cmdTaskListJSON(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	r, err := application.NewCooperativeContext(w, id)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Both the text and JSON list paths need this: a dropped --status filter
	// silently lists EVERY task, which reads as a correct answer to a question
	// nobody asked. The 175 sweep missed both, and an invariant test over the
	// exit-code contract is what found them (dacli 202).
	if err := f.Reject("project", "status"); err != nil {
		return err
	}
	status, serr := model.ParseStatus(f.Get("status"))
	if serr != nil {
		return clikit.Usagef("%v", serr)
	}
	ts, err := application.ListTasks(r, f.Get("project"), status)
	if err != nil {
		return err
	}
	out := []taskJSON{}
	for _, t := range ts {
		boxes := t.Acceptance()
		done := 0
		for _, b := range boxes {
			if b.Done {
				done++
			}
		}
		var aggregate *store.AggregateProgress
		if t.IsAggregate() {
			p, progressErr := store.AggregateProgressFor(w, t)
			if progressErr != nil {
				return progressErr
			}
			aggregate = &p
		}
		out = append(out, taskJSON{ID: t.ID, Seq: t.Seq, Slug: t.Slug, Project: t.Project,
			Status: string(t.Status), CompletionState: t.CompletionState(), Priority: t.Priority(), Title: t.Title,
			Done: done, Total: len(boxes), Kind: t.TaskKind(), Aggregate: aggregate})
	}
	return clikit.EmitJSON(ctx, out)
}

func cmdTaskShow(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	r, err := application.NewCooperativeContext(w, id)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task show <ref>")
	}
	t, err := application.GetTask(r, f.Pos[0])
	if err != nil {
		return err
	}
	proposals, err := store.PendingAcceptanceProposals(w, t)
	if err != nil {
		return fmt.Errorf("read pending acceptance proposals: %w", err)
	}
	if ctx.JSON {
		type criterion struct {
			Number  int    `json:"number"`
			Text    string `json:"text"`
			Checked bool   `json:"checked"`
		}
		criteria := make([]criterion, 0, len(t.Acceptance()))
		for i, box := range t.Acceptance() {
			criteria = append(criteria, criterion{Number: i + 1, Text: box.Text, Checked: box.Done})
		}
		return clikit.EmitJSON(ctx, struct {
			Schema            string                     `json:"schema"`
			ID                string                     `json:"id"`
			Seq               int                        `json:"seq"`
			Slug              string                     `json:"slug"`
			Project           string                     `json:"project"`
			Status            string                     `json:"status"`
			CompletionState   string                     `json:"completion_state"`
			Kind              string                     `json:"kind"`
			Title             string                     `json:"title"`
			Owner             string                     `json:"owner"`
			Priority          string                     `json:"priority,omitempty"`
			Dependencies      []store.Dep                `json:"dependencies"`
			Claims            []string                   `json:"claims"`
			Acceptance        []criterion                `json:"acceptance"`
			PendingAcceptance []store.AcceptanceProposal `json:"pending_acceptance_proposals"`
			Markdown          string                     `json:"markdown"`
		}{"task/v1", t.ID, t.Seq, t.Slug, t.Project, string(t.Status), t.CompletionState(), t.TaskKind(), t.Title, t.Owner(), t.Priority(), t.Deps(), t.Claims(), criteria, proposals, mdstore.Render(t.Doc)})
	}
	fmt.Fprint(ctx.Stdout, mdstore.Render(t.Doc))
	for _, proposal := range proposals {
		fmt.Fprintf(ctx.Stdout, "\nPending acceptance proposal %s by %s (%s/%s)\n  evidence: %s · commit %s · tree %s\n  next: dacli accept apply %s --proposal %s\n", proposal.ID, proposal.Proposer, proposal.Runtime, proposal.Grant, proposal.EvidenceDigest, proposal.CommitSHA, proposal.TreeSHA, t.ID, proposal.ID)
	}
	return nil
}

func cmdTaskClaim(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task claim <ref>")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}

	if owner := t.Owner(); owner != "" && owner != id.ID && t.Status == model.StatusActive {
		return clikit.Refusedf("task is owned by %s and active; ask them or wait for release", owner)
	}
	if !id.CanMutate(t.Owner()) {
		// A read-only agent claims via an event, not a rewrite.
		if _, err := eventlog.Append(w, id.ID, model.EventClaim, t.ID, "", ""); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "claim recorded as event (%s); the owner applies it on sync\n", id.MutateRefusal())
		return nil
	}

	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		fresh.Doc.Front.Set("owner", id.ID)
		store.AppendLog(fresh, "claimed by "+id.ID)
		if err := store.SaveTask(fresh); err != nil {
			return err
		}
		if fresh.Status == model.StatusOpen {
			return store.MoveTask(w, fresh, model.StatusActive)
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "claimed %03d-%s\n", t.Seq, t.Slug)
	return nil
}

func cmdTaskCheck(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("n", "all", "verify", "full-output"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task check <ref> [--n N | --all] [--verify command] [--full-output]")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if !id.CanMutate(t.Owner()) {
		return clikit.Refusedf("only the owner (%s) checks acceptance boxes; report a finding instead", t.Owner())
	}
	if err := taskCheckTestRendezvous(); err != nil {
		return err
	}
	return store.WithTask(w, t, func(fresh *store.Task) error {
		sec, ok := fresh.Doc.Section("Acceptance")
		if !ok {
			return fmt.Errorf("task has no acceptance section")
		}
		boxes := mdstore.Checkboxes(sec.Content)
		var selected []int
		if f.Bool("all") {
			for i := range boxes {
				if !boxes[i].Done {
					selected = append(selected, i+1)
				}
			}
		} else {
			n, err := strconv.Atoi(f.Get("n"))
			if err != nil || n < 1 || n > len(boxes) {
				return clikit.Usagef("--n must be 1..%d", len(boxes))
			}
			selected = append(selected, n)
		}
		verify := f.Get("verify")
		needsCommand := false
		for _, n := range selected {
			needsCommand = needsCommand || store.AcceptanceRequiresCommandVerification(fresh, n)
		}
		if needsCommand && verify == "" {
			return clikit.Refusedf("criterion requires command verification; pass --verify with the command so artifact hash and verifier identity can be recorded")
		}
		if verify != "" {
			ev, out, err := store.RunVerification(ctx.Cwd, id.ID, verify)
			if persistErr := store.PersistVerificationArtifact(w, ev, out); persistErr != nil {
				return fmt.Errorf("retain verification artifact: %w", persistErr)
			}
			if err != nil {
				detail := strings.TrimSpace(store.VerificationOutput(out, f.Bool("full-output")))
				if detail != "" {
					return fmt.Errorf("%s\n%s: %w", store.VerificationSummary(ev), detail, err)
				}
				return fmt.Errorf("%s (rerun with --full-output to inspect bounded command output)", store.VerificationSummary(ev))
			}
			fmt.Fprintln(ctx.Stderr, store.VerificationSummary(ev))
			if err := store.AppendVerificationEvidence(fresh, ev); err != nil {
				return clikit.Refusedf("command verification evidence is incomplete: %v", err)
			}
		}
		for _, n := range selected {
			boxes[n-1].Done = true
		}
		fresh.Doc.SetSection("Acceptance", mdstore.RenderCheckboxes(boxes))
		return store.SaveTask(fresh)
	})
}

// taskCheckTestRendezvous makes the cross-process lost-update regression
// deterministic. It is inert outside tests; two real CLI processes announce
// that they both loaded the same pre-mutation task before either takes its
// per-task lock. Keeping the rendezvous before WithTask is load-bearing: the
// second process must then re-read the first process's persisted check.
func taskCheckTestRendezvous() error {
	dir := os.Getenv("DACLI_TEST_TASK_CHECK_RENDEZVOUS")
	if dir == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())), nil, 0o600); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) >= 2 {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("task-check test rendezvous timed out")
}

func cmdTaskDone(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("allow-unverified"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task done <ref> [--allow-unverified]")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	allowUnverified := f.Bool("allow-unverified")

	// A task with no acceptance criteria has nothing to verify: the unmet-box
	// scan below finds an empty list and would pass, so zero boxes read as all
	// boxes and `done` — which "VERIFIES, not just records" — would certify
	// nothing (dacli 289). Refuse before the propose branch so a read-only agent
	// cannot even propose closing an unverifiable task; the owner opts into an
	// explicitly unverified close with --allow-unverified, the same rule the
	// accept and propose→sync paths enforce.
	if !store.HasAcceptanceCriteria(t) && !allowUnverified {
		return clikit.Refusedf("task %03d has no acceptance criteria: closing it would verify nothing. Add at least one criterion (edit the task, or refile with `task add --accept`), or pass --allow-unverified to close it as explicitly UNVERIFIED — do not retry", t.Seq)
	}

	if !id.CanMutate(t.Owner()) {
		if _, err := eventlog.Append(w, id.ID, model.EventProposeStatus, t.ID, "", "propose: done"); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "done proposed as event (%s)\n", id.MutateRefusal())
		return nil
	}

	// done VERIFIES, not just records: unchecked acceptance is a refusal
	// naming the criterion — "no" is an answer, not a failure.
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		var unmet []string
		for _, box := range fresh.Acceptance() {
			if !box.Done {
				unmet = append(unmet, box.Text)
			}
		}
		if len(unmet) > 0 {
			return clikit.Refusedf("acceptance unmet:\n  - %s\nfix, `task check`, or `ask` if the criterion is wrong — do not retry", strings.Join(unmet, "\n  - "))
		}
		if !store.HasAcceptanceCriteria(fresh) {
			if !allowUnverified {
				return clikit.Refusedf("task %03d has no acceptance criteria: closing it would verify nothing — do not retry", fresh.Seq)
			}
			store.AppendLog(fresh, "closed with NO acceptance criteria — UNVERIFIED (--allow-unverified)")
		}
		return store.CloseTask(w, fresh, id.ID)
	}); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "done: %03d-%s\n", t.Seq, t.Slug)
	return nil
}

// cmdTaskTakeover is the explicit root-only recovery path for a task whose
// owner cannot return to apply its pending events. It deliberately does not
// consume those events: changing ownership is recovery, not an assertion that
// any proposal is correct.
func cmdTaskTakeover(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("force", "reason"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || !f.Bool("force") || strings.TrimSpace(f.Get("reason")) == "" {
		return clikit.Usagef("usage: dacli task takeover <ref> --force --reason \"why recovery is safe\"")
	}
	if id.ID != agentid.RootID || id.Grant != model.GrantRW {
		return clikit.Refusedf("task takeover requires read-write root identity")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if t.Owner() == id.ID {
		return clikit.Refusedf("%03d-%s is already owned by %s; takeover is only for a non-root orphan", t.Seq, t.Slug, id.ID)
	}
	if t.Owner() == "" || (t.Status != model.StatusOpen && t.Status != model.StatusActive) {
		return clikit.Refusedf("%03d-%s is not an unfinished task owned by a non-root agent", t.Seq, t.Slug)
	}
	leased, err := store.OwnerTaskHasRecoveryLease(w, t.Owner(), t.ID)
	if err != nil {
		return clikit.Refusedf("%03d-%s recovery evidence is unreadable; refusing takeover: %v", t.Seq, t.Slug, err)
	}
	if leased {
		return clikit.Refusedf("%03d-%s remains owned by %s: a live process or transcript-active run still holds recovery authority", t.Seq, t.Slug, t.Owner())
	}
	previous := t.Owner()
	reason := strings.TrimSpace(f.Get("reason"))
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		if fresh.Owner() != previous {
			return clikit.Refusedf("%03d-%s owner changed during recovery; reload before retrying", fresh.Seq, fresh.Slug)
		}
		leased, err := store.OwnerTaskHasRecoveryLease(w, fresh.Owner(), fresh.ID)
		if err != nil {
			return clikit.Refusedf("%03d-%s recovery evidence became unreadable; refusing takeover: %v", fresh.Seq, fresh.Slug, err)
		}
		if leased {
			return clikit.Refusedf("%03d-%s recovery refused: a live process or transcript-active run appeared", fresh.Seq, fresh.Slug)
		}
		fresh.Doc.Front.Set("owner", id.ID)
		store.AppendLog(fresh, fmt.Sprintf("takeover by %s from %s (recovery: task takeover --force; reason: %s)", id.ID, previous, reason))
		return store.SaveTask(fresh)
	}); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "took over %03d-%s from %s; pending proposals preserved\n", t.Seq, t.Slug, previous)
	return nil
}

// cmdTaskEstimate sizes an existing task. Estimates could only be set at
// creation, so any backlog filed without them was permanently unsizable — and
// every scheduling command that depends on estimates (critical-path, next's
// slack ordering, `next --parallel`) silently fell back to MoSCoW-then-sequence,
// the one ordering that cannot tell you what runs concurrently. Sizing after
// the fact is the normal case: you learn the shape of work by looking at it
// (dacli 228).
func cmdTaskEstimate(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("estimate", "force"); err != nil {
		return err
	}
	est := f.Get("estimate")
	if len(f.Pos) == 0 || est == "" {
		return clikit.Usagef("usage: dacli task estimate <ref> --estimate o,m,p [--force]   (optimistic,probable,pessimistic)")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	// Sizing rewrites the task, so it obeys the same ownership rule every other
	// mutation does rather than inventing a looser one. --force mirrors
	// `accept --force`: root adopts a task orphaned by a finished agent, which
	// is the only way a backlog left behind by a dead loop can be sized at all.
	adopt := !id.CanMutate(t.Owner())
	if adopt {
		if id.ID != agentid.RootID || !f.Bool("force") {
			return clikit.Refusedf("cannot size %03d-%s: %s (root can adopt it with --force)", t.Seq, t.Slug, id.MutateRefusal())
		}
	}
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		if adopt {
			prev := fresh.Owner()
			fresh.Doc.Front.Set("owner", id.ID)
			store.AppendLog(fresh, fmt.Sprintf("adopted by %s (owner %s orphaned)", id.ID, clikit.OrDash(prev)))
		}
		return store.SetEstimate(w, fresh, est)
	}); err != nil {
		return clikit.Usagef("%v", err)
	}
	tp, ok := t.Estimate()
	if !ok {
		return fmt.Errorf("estimate written but did not parse back — refusing to report a size that will not survive a reload")
	}
	fmt.Fprintf(ctx.Stdout, "sized %03d-%s: Te %.1f (o %g, m %g, p %g)\n", t.Seq, t.Slug, tp.Expected(), tp.Optimistic, tp.Probable, tp.Pessimistic)
	return nil
}

func cmdTaskDepend(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("add", "remove"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || len(f.All("add"))+len(f.All("remove")) == 0 {
		return clikit.Usagef("usage: dacli task depend <ref> [--add dep[:FS|SS|FF|SF]]... [--remove dep[:FS|SS|FF|SF]]...")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	change := store.DependencyChange{Add: f.All("add"), Remove: f.All("remove")}
	// Validate against a disposable copy before recording a proposal. A bad
	// proposal that can never sync is not useful audit history.
	if err := store.ValidateDependencyChange(w, t, change); err != nil {
		return err
	}
	body, err := store.EncodeDependencyChange(change)
	if err != nil {
		return err
	}
	event, err := eventlog.Append(w, id.ID, model.EventDependency, t.ID, "", body)
	if err != nil {
		return err
	}
	if !id.CanMutate(t.Owner()) {
		fmt.Fprintf(ctx.Stdout, "dependency edit proposed as event %s (%s); the owner applies it on sync\n", event.ID, id.MutateRefusal())
		return nil
	}
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		if err := store.ApplyDependencyChange(w, fresh, change); err != nil {
			return err
		}
		store.AppendLog(fresh, fmt.Sprintf("dependency edit by %s (event %s)", id.ID, event.ID))
		return store.SaveTask(fresh)
	}); err != nil {
		return err
	}
	if err := eventlog.MarkApplied(event.Path); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "dependencies updated: %03d-%s (event %s)\n", t.Seq, t.Slug, event.ID)
	return nil
}

func cmdTaskAggregate(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("dry-run", "apply"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || (f.Bool("dry-run") == (f.Get("apply") != "")) {
		return clikit.Usagef("usage: dacli task aggregate <ref> (--dry-run | --apply plan-id)")
	}
	parent, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	plan, err := store.BuildAggregateRepairPlan(w, parent)
	if err != nil {
		return err
	}
	if f.Bool("dry-run") {
		if ctx.JSON {
			return clikit.EmitJSON(ctx, plan)
		}
		fmt.Fprintf(ctx.Stdout, "aggregate repair plan %s\nparent: %s\nrequired children: %s\ndry-run: nothing was written; apply with --apply %s\n", plan.ID, plan.TaskID, strings.Join(plan.ChildIDs, ", "), plan.ID)
		return nil
	}
	if !id.CanMutate(parent.Owner()) {
		return clikit.Refusedf("only %s may apply aggregate repair plan %s", clikit.OrDash(parent.Owner()), plan.ID)
	}
	plan, err = store.ApplyAggregateRepairPlan(w, parent, f.Get("apply"))
	if err != nil {
		return clikit.Refusedf("%v", err)
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, plan)
	}
	fmt.Fprintf(ctx.Stdout, "applied aggregate repair plan %s: %d stable children now derive parent progress\n", plan.ID, len(plan.ChildIDs))
	return nil
}

func cmdTaskDecompose(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("dry-run", "apply"); err != nil {
		return err
	}
	if len(f.Pos) != 1 || (f.Bool("dry-run") == (f.Get("apply") != "")) {
		return clikit.Usagef("usage: dacli task decompose <ref> (--dry-run | --apply plan-id)")
	}
	parent, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	plan, err := store.BuildDecompositionPlan(w, parent)
	if err != nil {
		return clikit.Refusedf("%v", err)
	}
	if f.Bool("dry-run") {
		if ctx.JSON {
			return clikit.EmitJSON(ctx, plan)
		}
		fmt.Fprintf(ctx.Stdout, "decomposition plan %s for %s\n", plan.ID, plan.TaskID)
		for _, child := range plan.Children {
			fmt.Fprintf(ctx.Stdout, "  %s %q estimate=%s claims=%s depends_on=%s\n", child.ID, child.Title, child.Estimate, strings.Join(child.Claims, ","), strings.Join(child.DependsOn, ","))
		}
		fmt.Fprintf(ctx.Stdout, "dry-run: no task was created; apply this exact plan with --apply %s\n", plan.ID)
		return nil
	}
	if !id.CanMutate(parent.Owner()) {
		return clikit.Refusedf("only %s may apply decomposition plan %s", clikit.OrDash(parent.Owner()), plan.ID)
	}
	plan, err = store.ApplyDecompositionPlan(w, parent, f.Get("apply"), id.ID)
	if err != nil {
		return clikit.Refusedf("%v", err)
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, plan)
	}
	fmt.Fprintf(ctx.Stdout, "applied decomposition plan %s: created %d stable children; parent is now aggregate\n", plan.ID, len(plan.Children))
	return nil
}

func cmdTaskBlock(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("by", "why"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task block <ref> [--by ref] [--why text]")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	why := f.Get("why")
	if by := f.Get("by"); by != "" {
		why = "blocked_by [[" + by + "]] " + why
	}
	if !id.CanMutate(t.Owner()) {
		if _, err := eventlog.Append(w, id.ID, model.EventBlock, t.ID, "", why); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "block recorded as event (%s)\n", id.MutateRefusal())
		return nil
	}
	if by := f.Get("by"); by != "" {
		t.Doc.Front.Set("blocked_by", "[["+by+"]]")
	}
	if err := store.WithTask(w, t, func(fresh *store.Task) error {
		if by := f.Get("by"); by != "" {
			fresh.Doc.Front.Set("blocked_by", "[["+by+"]]")
		}
		store.AppendLog(fresh, "blocked: "+why)
		if err := store.SaveTask(fresh); err != nil {
			return err
		}
		return store.MoveTask(w, fresh, model.StatusBlocked)
	}); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "blocked: %03d-%s\n", t.Seq, t.Slug)
	return nil
}

func cmdRiskAdd(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("action", "impact", "indicator", "likelihood", "project"); err != nil {
		return err
	}
	if len(f.Pos) == 0 || f.Get("project") == "" || f.Get("impact") == "" || f.Get("likelihood") == "" {
		return clikit.Usagef("usage: dacli risk add <title> --project <slug> --impact high|medium|low --likelihood high|medium|low [--indicator text]... [--action text]")
	}
	r, err := store.CreateRisk(w, id.ID, f.Get("project"), strings.Join(f.Pos, " "),
		model.Level(f.Get("impact")), model.Level(f.Get("likelihood")),
		f.All("indicator"), f.Get("action"))
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "risk %s recorded (rank %d)\n", r.Slug, r.Rank())
	if r.Rank() <= 2 && strings.TrimSpace(r.Action) == "" {
		fmt.Fprintf(ctx.Stderr, "warning: rank-%d risk with no action plan — ranks 1 and 2 require one\n", r.Rank())
	}
	return nil
}

func cmdRiskList(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("project"); err != nil {
		return err
	}
	project := f.Get("project")
	if project == "" && len(f.Pos) > 0 {
		project = f.Pos[0]
	}
	if project == "" {
		return clikit.Usagef("usage: dacli risk list <project>")
	}
	risks, err := store.ListRisks(w, project)
	if err != nil {
		return err
	}
	for _, r := range risks {
		flag := ""
		if r.Rank() <= 2 && strings.TrimSpace(r.Action) == "" {
			flag = "  ⚠ no action plan"
		}
		fmt.Fprintf(ctx.Stdout, "rank %d  %-8s×%-8s %s%s\n", r.Rank(), r.Impact, r.Likelihood, r.Title, flag)
	}
	return nil
}

func cmdGlossary(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject("def", "term"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli glossary <project> [--term t --def text]")
	}
	project := f.Pos[0]
	if term := f.Get("term"); term != "" {
		if f.Get("def") == "" {
			return clikit.Usagef("--term requires --def")
		}
		if err := store.GlossaryAdd(w, id.ID, project, term, f.Get("def")); err != nil {
			return err
		}
		fmt.Fprintf(ctx.Stdout, "defined %q\n", term)
		return nil
	}
	fmt.Fprint(ctx.Stdout, store.GlossaryRead(w, project))
	return nil
}

// cmdTaskReopen moves a wrongly-closed task back to open.
//
// Closing was a one-way door: a task force-accepted by mistake could only be
// corrected by editing the markdown store by hand, which is what happened to
// tasks 336 and 339 when `accept --force` was run over a batch nobody read
// (dacli 340). The tool's product is a record, and it gave no command to fix
// the one thing it exists to keep.
func cmdTaskReopen(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("reason"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task reopen <ref> --reason \"<what makes the close wrong>\"")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if !id.CanMutate(t.Owner()) {
		return clikit.Refusedf("%03d-%s is owned by %s — only its owner or root can reopen it", t.Seq, t.Slug, clikit.OrDash(t.Owner()))
	}
	reason := f.Get("reason")
	if strings.TrimSpace(reason) == "" {
		return clikit.Usagef("dacli task reopen needs --reason: a reopen with no reason is a mystery to the next reader")
	}
	var cleared int
	err = store.WithTask(w, t, func(fresh *store.Task) error {
		var reopenErr error
		cleared, reopenErr = store.ReopenTask(w, fresh, id.ID, reason)
		return reopenErr
	})
	if err != nil {
		return clikit.Usagef("%v", err)
	}
	// Say what was cleared. Silently unchecking boxes would replace one false
	// record with a different one.
	fmt.Fprintf(ctx.Stdout, "reopened %03d-%s — cleared %d acceptance box(es); the close claimed work that was not verified\n", t.Seq, t.Slug, cleared)
	return nil
}

// cmdTaskRm deletes a task that should never have existed.
//
// For a probe, a duplicate or a mis-filed entry — NOT for retracting real work,
// which is corrected by reopening so the record stays visible. It refuses while
// anything still points at the task, because a dangling reference fails far
// from the deletion that caused it and names neither.
func cmdTaskRm(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("force"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli task rm <ref> [--force]")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	if err := authorizeTaskRemoval(w, id, t); err != nil {
		return err
	}
	// A task with history or an active/done status has evidence beyond its
	// creation. --force is the explicit acknowledgement that the record itself
	// is the mistake; it never bypasses live-agent or reference safety.
	if !f.Bool("force") && (taskHasHistory(t) || t.Status == model.StatusActive || t.Status == model.StatusDone) {
		return clikit.Refusedf("%03d-%s is %s or has history — removing it erases an existing record. Use `dacli task reopen` to correct a wrong close, or pass --force if this task should never have existed", t.Seq, t.Slug, strings.ToUpper(string(t.Status)))
	}
	if err := store.RemoveTask(w, t); err != nil {
		var ref store.ErrReferenced
		if errors.As(err, &ref) {
			return clikit.Refusedf("%v — remove the reference first, or reopen the task instead of deleting it", ref)
		}
		return err
	}
	fmt.Fprintf(ctx.Stdout, "removed %03d-%s\n", t.Seq, t.Slug)
	return nil
}

// authorizeTaskRemoval keeps root's orphan recovery local to task rm. Making
// agentid.CanMutate root-aware would silently widen every unrelated command's
// authority. Root may cross ownership only for a real child identity whose
// process is no longer live; store.RemoveTask independently retains the
// issue-433 guard for any live run working this specific task.
func authorizeTaskRemoval(w *workspace.Workspace, id *agentid.Identity, t *store.Task) error {
	if id.CanMutate(t.Owner()) {
		return nil
	}
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("%03d-%s cannot be removed: %s; task rm requires a read-write owner or read-write root recovering a non-live child-owned task", t.Seq, t.Slug, id.MutateRefusal())
	}
	if id.ID != agentid.RootID {
		return clikit.Refusedf("%03d-%s is owned by %s — only that owner, or read-write root after the child owner is no longer live, can remove it", t.Seq, t.Slug, clikit.OrDash(t.Owner()))
	}

	agents, err := store.ListAgents(w)
	if err != nil {
		return err
	}
	knownChild := false
	for _, agent := range agents {
		if agent.ID == t.Owner() {
			knownChild = true
			break
		}
	}
	if !knownChild {
		return clikit.Refusedf("%03d-%s is owned by %s, whose agent lifecycle cannot be resolved — root orphan recovery applies only to known child agents", t.Seq, t.Slug, clikit.OrDash(t.Owner()))
	}
	leased, err := store.OwnerTaskHasRecoveryLease(w, t.Owner(), t.ID)
	if err != nil {
		return clikit.Refusedf("%03d-%s recovery evidence is unreadable; refusing removal: %v", t.Seq, t.Slug, err)
	}
	if leased {
		return clikit.Refusedf("%03d-%s is owned by live agent %s — root cannot remove it while that owner has a live run or process; stop it or let it finish first", t.Seq, t.Slug, t.Owner())
	}
	return nil
}

func taskHasHistory(t *store.Task) bool {
	log, ok := t.Doc.Section("Log")
	return ok && strings.TrimSpace(log.Content) != ""
}
