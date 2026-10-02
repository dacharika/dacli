// Package teamops is the org slice: agent identities and lineage, roles as
// mechanical capability bundles, and escalation routing.
package teamops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/gates"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "agent spawn", Brief: "Mint a child agent identity and print its token once", Mutates: true, Usage: "dacli agent spawn [--role r] [--grant ro|rw]", Run: cmdAgentSpawn},
	{Path: "agent tree", Brief: "Show agent lineage, roles, current task and write attribution", Usage: "dacli agent tree", Run: cmdAgentTree},
	{Path: "agent show", Brief: "Resolve one agent id: role, lineage, runs, tasks, events", Usage: "dacli agent show <agent-id>", Run: cmdAgentShow},
	{Path: "agent retire", Brief: "Mark an agent retired, freeing its WIP slot", Mutates: true, Usage: "dacli agent retire <agent-id>", Run: cmdAgentRetire},
	{Path: "role add", Brief: "Define a role: skills, scope, shortcuts, escalation, and a provider-neutral model profile", Mutates: true, Usage: "dacli role add <name> [--runtime rt] [--model-id id] [--cost-tier 1..98] [--max-task-points N] [--context-limit N] [--capability-tag tag]...", Run: cmdRoleAdd},
	{Path: "role rm", Brief: "Remove a role (refuses while a live agent holds it)", Mutates: true, Usage: "dacli role rm <name>", Run: cmdRoleRm},
	{Path: "role list", Brief: "List roles", Usage: "dacli role list", Run: cmdRoleList},
	{Path: "role show", Brief: "One role: version, changelog, capabilities", Usage: "dacli role show <name>", Run: cmdRoleShow},
	{Path: "role bump", Brief: "Increment a role's version (v1→v2) after a change", Mutates: true, Usage: "dacli role bump <name>", Run: cmdRoleBump},
	{Path: "team", Brief: "Roster: roles, live WIP occupancy, and headroom (identity history is separate)", Usage: "dacli team", Run: cmdTeam},
	{Path: "team route", Brief: "Who owns this path, and the chain to reach them", Usage: "dacli team route <path> [--from role]", Run: cmdTeamRoute},
	{Path: "route", Brief: "Alias for team route: who owns this path and the escalation chain", Usage: "dacli route <path> [--from role]", Run: cmdRoute},
	{Path: "team assign", Brief: "Which role should take this task: the cheapest model whose capacity covers its Te, for the phase's allowed kind", Usage: "dacli team assign <task-ref> [--kind implementer|reviewer|researcher|planner|designer]", Run: cmdTeamAssign},
}

func cmdAgentSpawn(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("grant", "role"); err != nil {
		return err
	}
	grant := model.Grant(f.Get("grant"))
	roleName := f.Get("role")

	// A role file supplies defaults and limits. Its grant is a ceiling
	// REQUEST — attenuation against the parent still wins in Spawn.
	var roleSkills, roleShortcuts []string
	if role, ok := store.LoadRole(w, roleName); ok {
		if grant == "" && role.Grant != "" {
			grant = model.Grant(role.Grant)
		}
		roleSkills, roleShortcuts = role.Skills, role.Shortcuts
		if role.WIP > 0 {
			// The SIBLING gate to gateRoleWIP, and it was left discarding the
			// error when 341 widened the signature — the "rule applied in four
			// places and missed in a fifth" pattern this codebase keeps
			// hitting. It refuses a spawn, so it must fail closed for the same
			// reason: a WIP cap that cannot be read is not a WIP cap of zero.
			active, aerr := store.ActiveInRole(w, roleName)
			if aerr != nil {
				return clikit.Refusedf("cannot check role %s's WIP limit: %v — a cap that cannot be read must not wave a spawn through", roleName, aerr)
			}
			if active >= role.WIP {
				// Burning Across made preventable rather than detectable:
				// the refusal happens BEFORE the thirty-first child exists.
				return clikit.Refusedf("role %s is at its WIP limit (%d/%d) — `dacli agent retire` one, or raise wip in the role file",
					roleName, active, role.WIP)
			}
		}
	}

	childID, token, err := agentid.Spawn(w, id, roleName, grant)
	if err != nil {
		if errors.Is(err, agentid.ErrAttenuation) {
			return clikit.Refusedf("%v: your grant is %s", err, id.Grant)
		}
		return err
	}
	if len(roleSkills) > 0 {
		fmt.Fprintf(ctx.Stderr, "role skills to load for the child: %s\n", strings.Join(roleSkills, ", "))
	}
	if len(roleShortcuts) > 0 {
		fmt.Fprintf(ctx.Stderr, "role toolkit: %s\n", strings.Join(roleShortcuts, ", "))
	}
	// The token goes to stdout ALONE so `TOKEN=$(dacli agent spawn ...)`
	// captures exactly it; everything human-facing goes to stderr. It is
	// shown once and never stored — a lost token means a new agent.
	fmt.Fprintln(ctx.Stdout, token)
	fmt.Fprintf(ctx.Stderr, "spawned %s (role: %s, grant: %s)\ntoken shown once above — pass it to the child as %s\n",
		childID, clikit.OrDash(f.Get("role")), clikit.OrDash(string(grant), "ro"), agentid.EnvVar)
	return nil
}

func cmdAgentTree(ctx *clikit.Ctx, args []string) error {
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
	entries, err := os.ReadDir(w.AgentsDir())
	if err != nil {
		return err
	}

	type agent struct {
		id, parent, grant, role string
		retired                 bool
	}
	byParent := map[string][]agent{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		d, err := mdstore.ReadFile(w.AgentPath(strings.TrimSuffix(e.Name(), ".md")))
		if err != nil {
			continue
		}
		a := agent{}
		a.id, _ = d.Front.Get("id")
		a.grant, _ = d.Front.Get("grant")
		a.role, _ = d.Front.Get("role")
		if p, ok := d.Front.Get("parent"); ok {
			a.parent = strings.TrimSuffix(strings.TrimPrefix(p, "[["), "]]")
		}
		if r, _ := d.Front.Get("retired"); r == "true" {
			a.retired = true
		}
		byParent[a.parent] = append(byParent[a.parent], a)
	}

	// Write attribution: how many events each agent has actually produced.
	writes := map[string]int{}
	events, _ := eventlog.List(w, eventlog.Query{})
	for _, e := range events {
		writes[e.Actor]++
	}
	// Traceability (dacli 225): the run records already say which task each
	// agent was spawned against and which run produced its work. Joining them
	// here is what turns the tree from a list of names into something an
	// operator can act on without opening a single file.
	runs := runsByAgent(w)

	var render func(a agent, depth int)
	render = func(a agent, depth int) {
		role := a.role
		if role == "" {
			// An agent file with no role: the id itself may still carry one,
			// which is the whole point of a readable id.
			role = agentid.RoleOf(a.id)
		}
		if role != "" {
			role = " · " + role
		}
		fmt.Fprintf(ctx.Stdout, "%s%s (%s%s) — %d events%s\n",
			strings.Repeat("  ", depth), a.id, a.grant, role, writes[a.id], traceSuffix(w, runs[a.id], a.retired))
		kids := byParent[a.id]
		sort.Slice(kids, func(i, j int) bool { return kids[i].id < kids[j].id })
		for _, k := range kids {
			render(k, depth+1)
		}
	}
	for _, root := range byParent[""] {
		render(root, 0)
	}
	return nil
}

// runsByAgent indexes the run records by the agent they spawned, newest run
// first (dacli 225). This is the join that makes an id traceable: a run record
// already carries the task, the runtime, the claimed paths and the run id under
// which the brief, invocation and outcome were filed. No new file format — just
// reading the one that has been written at every spawn since E2.
func runsByAgent(w *workspace.Workspace) map[string][]procmon.Record {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // ULID dir names: newest first
	out := map[string][]procmon.Record{}
	for _, n := range names {
		rec, err := procmon.ReadRecord(filepath.Join(w.RunDir(n), "proc.txt"))
		if err != nil || rec.Child == "" {
			continue
		}
		if rec.RunID == "" {
			rec.RunID = n
		}
		out[rec.Child] = append(out[rec.Child], rec)
	}
	return out
}

// taskLabel renders a run record's task ref the way humans refer to tasks
// (042-fix-the-thing), falling back to the raw ref when the task has since been
// archived — a dangling ref is still better traceability than nothing.
func taskLabel(w *workspace.Workspace, ref string) string {
	if ref == "" {
		return ""
	}
	if t, err := store.FindTask(w, ref); err == nil {
		return fmt.Sprintf("%03d-%s", t.Seq, t.Slug)
	}
	return ref
}

// traceSuffix appends the newest run's task and run id to a tree line, so the
// tree answers "what is this agent doing, and where is its work recorded".
func traceSuffix(w *workspace.Workspace, recs []procmon.Record, retired bool) string {
	var parts []string
	if len(recs) > 0 {
		if lbl := taskLabel(w, recs[0].Task); lbl != "" {
			parts = append(parts, "task "+lbl)
		}
		parts = append(parts, "run "+recs[0].RunID)
	}
	if retired {
		parts = append(parts, "retired")
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// cmdAgentShow resolves ONE id all the way: role, lineage, grant, the runs it
// was spawned for and the work it recorded (dacli 225). This is the command an
// operator reaches for after reading an unfamiliar id in `git log` or a task's
// Log — the alternative it replaces is opening .dacli/agents/<id>.md by hand and
// then grepping the runs dir.
func cmdAgentShow(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
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
		return clikit.Usagef("usage: dacli agent show <agent-id>")
	}
	want := f.Pos[0]

	d, derr := mdstore.ReadFile(w.AgentPath(want))
	if derr != nil {
		// An id with no file is not necessarily a typo: an agent can be named in
		// a commit trailer of a checkout whose .dacli/agents/ predates it. Say
		// what the id itself still reveals rather than only "not found".
		if role := agentid.RoleOf(want); role != "" {
			return store.ErrNotFound{Ref: "agent " + want + " (id reads as role " + role + "; the agent file is not in this checkout)"}
		}
		return store.ErrNotFound{Ref: "agent " + want}
	}
	get := func(k string) string { v, _ := d.Front.Get(k); return v }

	role := get("role")
	if role == "" {
		role = agentid.RoleOf(want)
	}
	fmt.Fprintf(ctx.Stdout, "%s\n", want)
	fmt.Fprintf(ctx.Stdout, "%-9s %s\n", "role:", clikit.OrDash(role))
	fmt.Fprintf(ctx.Stdout, "%-9s %s\n", "grant:", clikit.OrDash(get("grant")))
	fmt.Fprintf(ctx.Stdout, "%-9s %s\n", "created:", clikit.OrDash(get("created")))
	if get("retired") == "true" {
		fmt.Fprintf(ctx.Stdout, "%-9s yes (lineage and attribution kept)\n", "retired:")
	}

	// Lineage, walked to the root: "who delegated this" is the question an
	// unfamiliar id raises first, and one hop rarely answers it.
	chain := []string{want}
	seen := map[string]bool{want: true}
	for cur := want; ; {
		cd, err := mdstore.ReadFile(w.AgentPath(cur))
		if err != nil {
			break
		}
		p, ok := cd.Front.Get("parent")
		if !ok {
			break
		}
		p = strings.TrimSuffix(strings.TrimPrefix(p, "[["), "]]")
		// A cycle would be corruption, but a corrupt file must not hang a
		// read-only command.
		if p == "" || seen[p] {
			break
		}
		seen[p] = true
		chain = append([]string{p}, chain...)
		cur = p
	}
	fmt.Fprintf(ctx.Stdout, "%-9s %s\n", "lineage:", strings.Join(chain, " → "))

	recs := runsByAgent(w)[want]
	if len(recs) == 0 {
		fmt.Fprintf(ctx.Stdout, "%-9s none recorded (native host or historical record unavailable)\n", "runs:")
	} else {
		fmt.Fprintf(ctx.Stdout, "runs (newest first):\n")
		for _, r := range recs {
			line := "  " + r.RunID
			if lbl := taskLabel(w, r.Task); lbl != "" {
				line += "  task " + lbl
			}
			if r.Runtime != "" {
				line += "  runtime " + r.Runtime
			}
			if !r.Started.IsZero() {
				line += "  started " + r.Started.UTC().Format(time.RFC3339)
			}
			if len(r.Claims) > 0 {
				line += "  claims " + strings.Join(r.Claims, ",")
			}
			fmt.Fprintln(ctx.Stdout, line)
		}
	}

	events, _ := eventlog.List(w, eventlog.Query{Actor: want})
	fmt.Fprintf(ctx.Stdout, "events:   %d\n", len(events))
	for i, e := range events {
		if i == 5 {
			fmt.Fprintf(ctx.Stdout, "  … %d more\n", len(events)-i)
			break
		}
		fmt.Fprintf(ctx.Stdout, "  %s %s %s\n", e.ID, e.Kind, clikit.OrDash(clikit.FirstLine(e.Body)))
	}
	return nil
}

func cmdAgentRetire(ctx *clikit.Ctx, args []string) error {
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
		return clikit.Usagef("usage: dacli agent retire <agent-id>")
	}
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("retiring an agent rewrites its file, which needs an rw grant")
	}
	if err := store.RetireAgent(w, f.Pos[0]); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "retired %s (lineage and attribution kept; WIP slot freed)\n", f.Pos[0])
	return nil
}

func cmdRoleAdd(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("summary", "kind", "skill", "scope", "out-of-scope", "shortcut", "escalate-to", "grant", "wip", "runtime", "model", "max-points", "model-id", "cost-tier", "max-task-points", "context-limit", "capability-tag"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli role add <name> [--summary s] [--kind researcher|planner|designer|implementer|reviewer] [--skill s]... [--scope glob]... [--shortcut n]... [--escalate-to role]... [--grant ro|rw] [--wip N] [--runtime rt] [--model m] [--max-points N]")
	}
	r := team.Role{
		Name:       f.Pos[0],
		Summary:    f.Get("summary"),
		Skills:     f.All("skill"),
		Scope:      f.All("scope"),
		OutOfScope: f.All("out-of-scope"),
		Shortcuts:  f.All("shortcut"),
		EscalateTo: f.All("escalate-to"),
		Grant:      f.Get("grant"),
		Kind:       f.Get("kind"),
		Runtime:    f.Get("runtime"),
		Model:      f.Get("model"),
		Profile: team.ModelProfile{
			ID:             f.Get("model-id"),
			CapabilityTags: f.All("capability-tag"),
		},
	}
	if r.WIP, err = f.Int("wip", 0); err != nil {
		return err
	}
	_, _ = fmt.Sscanf(f.Get("max-points"), "%g", &r.MaxPoints)
	if v := f.Get("cost-tier"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &r.Profile.CostTier); err != nil || team.ModelTier(r.Profile.CostTier) == 99 {
			return clikit.Usagef("--cost-tier must be an integer from 1 through 98")
		}
	}
	if v := f.Get("max-task-points"); v != "" {
		if _, err := fmt.Sscanf(v, "%g", &r.Profile.MaxTaskPoints); err != nil || r.Profile.MaxTaskPoints <= 0 {
			return clikit.Usagef("--max-task-points must be positive")
		}
	}
	if v := f.Get("context-limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &r.Profile.ContextLimit); err != nil || r.Profile.ContextLimit <= 0 {
			return clikit.Usagef("--context-limit must be a positive integer")
		}
	}

	// A role must change what an agent can do, not just what it calls
	// itself. A name-only role is cosplay; warn, don't refuse — the fields
	// can be added later, but the warning should sting now.
	if len(r.Skills)+len(r.Scope)+len(r.Shortcuts)+len(r.EscalateTo)+len(r.Profile.CapabilityTags) == 0 && r.Grant == "" && r.WIP == 0 && r.ModelID() == "" && r.Runtime == "" && r.TaskCapacity() == 0 && r.Kind == "" {
		fmt.Fprintln(ctx.Stderr, "warning: this role changes nothing mechanical (no skills, scope, shortcuts, escalation, grant, or wip) — it is a costume, not a role")
	}
	if err := store.CreateRole(w, id.ID, r); err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "role %s defined\n", r.Name)
	return nil
}

func cmdRoleList(ctx *clikit.Ctx, args []string) error {
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
	roles, _ := store.LoadRoles(w)
	for _, r := range roles {
		extras := []string{}
		if len(r.Skills) > 0 {
			extras = append(extras, fmt.Sprintf("skills:%d", len(r.Skills)))
		}
		if len(r.Scope) > 0 {
			extras = append(extras, fmt.Sprintf("scope:%d", len(r.Scope)))
		}
		if r.WIP > 0 {
			extras = append(extras, fmt.Sprintf("wip:%d", r.WIP))
		}
		if r.Kind != "" {
			extras = append(extras, "kind:"+r.Kind)
		}
		if r.ModelID() != "" {
			extras = append(extras, "model:"+r.ModelID())
		}
		if r.Runtime != "" {
			extras = append(extras, "rt:"+r.Runtime)
		}
		if r.TaskCapacity() > 0 {
			extras = append(extras, fmt.Sprintf("≤%gpt", r.TaskCapacity()))
		}
		fmt.Fprintf(ctx.Stdout, "%-14s %-6s %-32s %s\n", r.Name, clikit.OrDash(r.Grant), strings.Join(extras, " "), r.Summary)
	}
	return nil
}

func cmdRoleShow(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
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
		return clikit.Usagef("usage: dacli role show <name>")
	}
	name := f.Pos[0]
	r, ok := store.LoadRole(w, name)
	if !ok {
		return store.ErrNotFound{Ref: "role " + name}
	}
	path := w.RolePath(name)
	version := store.FileVersion(path)

	fmt.Fprintf(ctx.Stdout, "%s — %s\nversion: %s · grant: %s\n", r.Name, clikit.OrDash(r.Summary), version, clikit.OrDash(r.Grant))
	field := func(label string, vals []string) {
		if len(vals) > 0 {
			fmt.Fprintf(ctx.Stdout, "%-12s %s\n", label+":", strings.Join(vals, ", "))
		}
	}
	field("skills", r.Skills)
	field("scope", r.Scope)
	field("out-of-scope", r.OutOfScope)
	field("shortcuts", r.Shortcuts)
	field("escalate-to", r.EscalateTo)
	if r.Kind != "" {
		fmt.Fprintf(ctx.Stdout, "%-12s %s\n", "kind:", r.Kind)
	}
	if r.WIP > 0 {
		fmt.Fprintf(ctx.Stdout, "%-12s %d\n", "wip:", r.WIP)
	}

	if stale, since := store.VersionIsStale(path, version); stale {
		if since > 0 {
			fmt.Fprintf(ctx.Stdout, "⚠ changed in %d commit(s) since %s was set — bump with `dacli role bump %s`\n", since, version, name)
		} else {
			fmt.Fprintf(ctx.Stdout, "⚠ uncommitted edits — bump with `dacli role bump %s` before committing\n", name)
		}
	}
	changes, seen := store.FileChangelog(path, 10)
	fmt.Fprintf(ctx.Stdout, "\nchangelog:\n%s\n", store.FormatChangelog(changes, seen))
	return nil
}

func cmdRoleBump(ctx *clikit.Ctx, args []string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("bumping a role version rewrites its file, which needs an rw grant")
	}
	f, _ := clikit.ParseFlags(args)
	// Reject unknown flags: a typo used to be dropped silently and the
	// command ran as if the caller had meant the default.
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli role bump <name>")
	}
	name := f.Pos[0]
	if _, ok := store.LoadRole(w, name); !ok {
		return store.ErrNotFound{Ref: "role " + name}
	}
	old, next, err := store.BumpFileVersion(w.RolePath(name))
	if err != nil {
		return err
	}
	fmt.Fprintf(ctx.Stdout, "role %s: %s → %s — commit it with `dacli commit`\n", name, old, next)
	return nil
}

func cmdTeam(ctx *clikit.Ctx, args []string) error {
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
	roles, _ := store.LoadRoles(w)
	occupancy, occupancyErr := store.LiveOccupancyByRole(w)
	for _, r := range roles {
		// A display, not a gate: report what could be read, and say plainly
		// when it could not be, rather than printing 0 as if it were a count.
		active := occupancy[r.Name]
		if occupancyErr != nil {
			fmt.Fprintf(ctx.Stderr, "warning: cannot count live occupancy in role %s: %v — its WIP headroom below is not a real number\n", r.Name, occupancyErr)
		}
		head := "∞"
		if r.WIP > 0 {
			head = fmt.Sprint(r.WIP - active)
		}
		fmt.Fprintf(ctx.Stdout, "%-14s occupancy:%d headroom:%s  %s\n", r.Name, active, head, r.Summary)
	}
	agents, _ := store.ListAgents(w)
	unroled := 0
	for _, a := range agents {
		if a.Role == "" && !a.Retired {
			unroled++
		}
	}
	if unroled > 0 {
		fmt.Fprintf(ctx.Stdout, "(plus %d agents with no role)\n", unroled)
	}
	return nil
}

// roleKinds are the function labels a role can carry (team.Role.Kind) and the
// only values kind-inference will emit. An explicit --kind is NOT checked
// against this set — a typo there surfaces honestly as "no <kind> role fits" —
// but a guess from the task must land on a real kind or it is worthless.
var roleKinds = map[string]bool{
	"implementer": true,
	"reviewer":    true,
	"researcher":  true,
	"planner":     true,
	"designer":    true,
}

// kindVerbs maps a word that can appear in a task title to the role kind it
// implies. A task titled "review the burn alert" or "research runtime drift"
// names its own function more precisely than the phase does — the phase only
// says which kinds are ALLOWED to act now, not what THIS task is — so a title
// verb outranks the phase. The map is deliberately small and high-signal;
// anything unmatched falls through to the phase, then to implementer.
// titleVerbModifiers are the only words that defer title intent to the word
// after them. An unknown leading word is treated as the task's verb instead of
// being skipped: "Test audit logging" is implementation work, while "Full
// audit of logging" is review work.
var titleVerbModifiers = map[string]bool{
	"full": true,
}

// Each verb below is one a task title in this workspace actually used, or the
// direct synonym an agent reaches for when writing the same kind of task. The
// additions came from live misroutes: "Falsify the safety-property suites…"
// and "Trace one user-invoked verb end to end…" (tasks 324, 325) are both pure
// audit work, matched nothing, and fell through to implementer — so the loop
// would have spawned a `fixer` onto work whose entire output is a filed
// finding (task 326).
//
// The bar for adding a verb is that it CANNOT plausibly lead an implementation
// task. "check", "test", "improve" and "cover" are all deliberately absent:
// "Test the retry path" and "Check the token is refreshed" are things an
// implementer writes code for, and routing them to a role charted "never
// implements" is the 318 bug in the other direction. "Fix" is explicit here
// because a later reviewer keyword must not override that leading intent.
var kindVerbs = map[string]string{
	"fix":         "implementer",
	"review":      "reviewer",
	"audit":       "reviewer",
	"verify":      "reviewer",
	"falsify":     "reviewer",
	"trace":       "reviewer",
	"inspect":     "reviewer",
	"assess":      "reviewer",
	"evaluate":    "reviewer",
	"critique":    "reviewer",
	"research":    "researcher",
	"investigate": "researcher",
	"explore":     "researcher",
	"spike":       "researcher",
	"survey":      "researcher",
	"plan":        "planner",
	"decompose":   "planner",
	"estimate":    "planner",
	"schedule":    "planner",
	"design":      "designer",
	"prototype":   "designer",
	"wireframe":   "designer",
}

// inferKind guesses a routing kind for a task that was assigned without an
// explicit --kind, and returns a human-readable source for the guess so the
// caller can print it. The order is most-authoritative first: a kind the task
// author declared, then a verb in the title that names the work, then the
// project phase, and finally implementer when nothing else speaks up.
func inferKind(w *workspace.Workspace, t *store.Task) (kind, source string) {
	if t.Doc != nil {
		if k, ok := t.Doc.Front.Get("role_kind"); ok && roleKinds[k] {
			return k, "task role_kind"
		}
	}
	// The LEADING verb, not the first keyword found anywhere. Scanning the
	// whole title let an incidental noun override the verb that states the
	// actual intent: "Write the tests the suite audit calls for" matched
	// "audit" and routed pure code-writing to a role whose charter is "never
	// implements" (task 318, observed live on task 315).
	//
	// Task titles are imperative by convention — `lint` pushes them that way —
	// so only an explicit modifier may defer intent to the second word.
	words := strings.Fields(strings.ToLower(t.Title))
	if len(words) > 0 {
		word := strings.Trim(words[0], ".,:;!?\"'()-")
		if titleVerbModifiers[word] && len(words) > 1 {
			word = strings.Trim(words[1], ".,:;!?\"'()-")
		}
		if k, ok := kindVerbs[word]; ok {
			return k, fmt.Sprintf("title verb %q", word)
		}
	}
	if ph, err := gates.PhaseFor(w, t.Project); err == nil && ph.Gated && len(ph.Allows) > 0 {
		return ph.Allows[0], fmt.Sprintf("phase %q", ph.Name)
	}
	return "implementer", "default"
}

// cmdTeamAssign answers "who should take this task, and on which model" from
// the task's own size rather than an operator's habit.
//
// dacli already had every piece and used none of them together: roles carry a
// Model tier and a MaxPoints capacity, and the seniority gate REFUSES a role
// whose capacity is below the task's Te. But nothing ever CHOSE a role, and no
// role in a shipped roster declared a capacity — so the gate was inert and
// model tiering was a per-spawn decision made by hand. With capacities
// declared, the choice is mechanical: the cheapest model that can hold the work
// takes it, and the expensive models stay free for the work that needs them
// (dacli 230, 231).
func cmdTeamAssign(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("kind"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: dacli team assign <task-ref> [--kind implementer|reviewer|researcher|planner|designer]")
	}
	t, err := store.FindTask(w, f.Pos[0])
	if err != nil {
		return err
	}
	tp, sized := t.Estimate()
	if !sized {
		return clikit.Refusedf("task %03d-%s has no estimate — capacity routing needs one; `dacli task estimate %03d --estimate o,m,p`", t.Seq, t.Slug, t.Seq)
	}
	te := tp.Expected()

	// The kind decides WHICH roles compete; the estimate decides which of them
	// is big enough. An explicit --kind overrides everything, for asking "who
	// would review this" while the project is still implementing. Absent one, we
	// infer the kind from the task itself and PRINT what we inferred (below), so
	// a wrong guess is visible and correctable rather than silently defaulted to
	// implementer (dacli 265).
	kind := f.Get("kind")
	source := "explicit --kind"
	if kind == "" {
		kind, source = inferKind(w, t)
	}

	roles, _ := store.LoadRoles(w)
	candidates := make([]team.RouteCandidate, 0, len(roles))
	for _, role := range roles {
		// Undeclared grants retain the legacy read-write role behavior, matching
		// spawn and the loop candidate builder.
		if role.Grant == "" {
			role.Grant = "rw"
		}
		capacity := 1
		if role.WIP > 0 {
			active, activeErr := store.ActiveInRole(w, role.Name)
			if activeErr != nil {
				capacity = 0
			} else {
				capacity = role.WIP - active
			}
		}
		candidates = append(candidates, team.RouteCandidate{Role: role, GrantEnforced: role.Grant != "ro" || kind == "reviewer", ContextLimit: role.Profile.ContextLimit, CapacityRemaining: capacity})
	}
	decision := (team.Strategy{}).Select(team.RouteRequirements{Kind: kind, Grant: map[bool]string{true: "ro", false: "rw"}[kind == "reviewer"], Title: t.Title, Body: taskBody(t), Paths: store.ClaimHints(w.Root, t), TaskPoints: te}, candidates)
	pick, ok := store.LoadRole(w, decision.Selected.Role)
	if !ok {
		return clikit.Refusedf("no %s role can hold Te %.1f — every capped role is too small and none is uncapped; decompose %03d-%s or add a heavier role",
			kind, te, t.Seq, t.Slug)
	}

	cap := "uncapped"
	if pick.TaskCapacity() > 0 {
		cap = fmt.Sprintf("%g points", pick.TaskCapacity())
	}
	fmt.Fprintf(ctx.Stdout, "%03d-%s (Te %.1f) → %s\n", t.Seq, t.Slug, te, pick.Name)
	fmt.Fprintf(ctx.Stdout, "  selected runtime %s · model %s\n", clikit.OrDash(pick.Runtime), clikit.OrDash(pick.ModelID()))
	fmt.Fprintf(ctx.Stdout, "  kind %s (%s)\n", kind, source)
	fmt.Fprintf(ctx.Stdout, "  decision: cost tier %d · task capacity %s covers Te %.1f\n", team.ModelTier(pick.Profile.CostTier), cap, te)
	fmt.Fprintf(ctx.Stdout, "  source: %s\n", decision.Source)
	if decision.Uplift != "" {
		fmt.Fprintf(ctx.Stdout, "  consequence uplift: %s\n", decision.Uplift)
	}
	context := "undeclared"
	if pick.Profile.ContextLimit > 0 {
		context = fmt.Sprintf("%d", pick.Profile.ContextLimit)
	}
	fmt.Fprintf(ctx.Stdout, "  profile: context limit %s · capabilities %s\n", context, clikit.OrDash(strings.Join(pick.Profile.CapabilityTags, ",")))
	fmt.Fprintf(ctx.Stdout, "  plan for task %03d with role %s; enroll a scoped native session and let the host own execution\n", t.Seq, pick.Name)
	return nil
}

func cmdTeamRoute(ctx *clikit.Ctx, args []string) error {
	return routePath(ctx, args, "dacli team route")
}

func cmdRoute(ctx *clikit.Ctx, args []string) error {
	return routePath(ctx, args, "dacli route")
}

func routePath(ctx *clikit.Ctx, args []string, usage string) error {
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject("from"); err != nil {
		return err
	}
	if len(f.Pos) == 0 {
		return clikit.Usagef("usage: %s <path> [--from role]", usage)
	}
	roles, _ := store.LoadRoles(w)
	if len(roles) == 0 {
		return fmt.Errorf("no roles defined; `dacli role add` first")
	}
	tm, err := team.New(roles)
	if err != nil {
		return err
	}
	path := f.Pos[0]

	owners := tm.Owners(path)
	if len(owners) == 0 {
		fmt.Fprintf(ctx.Stdout, "no role covers %s — escalate to a human\n", path)
		return nil
	}
	fmt.Fprintf(ctx.Stdout, "owners (most specific first): %s\n", strings.Join(owners, ", "))

	from := f.Get("from")
	if from == "" {
		from = id.Role
	}
	if from != "" && from != "root" {
		chain, err := tm.Route(from, path)
		if err != nil {
			// The G8 rule: an owner that exists but is unreachable is a
			// missing edge, not a dead end — the message must say which.
			return fmt.Errorf("%s owns this but is not reachable from %q's escalation chain — add it to escalate_to, or route via a shared ancestor (%w)", owners[0], from, err)
		}
		fmt.Fprintf(ctx.Stdout, "chain from %s: %s\n", from, strings.Join(chain, " → "))
	}
	return nil
}

// cmdRoleRm is the removal inverse of the corresponding add. Every creatable object
// used to need a text editor to undo, which made a mistake a command made into
// a mistake only a human could fix (task 293).
func cmdRoleRm(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, _ := clikit.ParseFlags(args)
	if err := f.Reject(); err != nil {
		return err
	}
	if len(f.Pos) < 1 {
		return clikit.Usagef("usage: dacli role rm <name>")
	}
	name := f.Pos[0]
	if err := store.RemoveRole(w, name); err != nil {
		var ref store.ErrReferenced
		if errors.As(err, &ref) {
			// A dangling reference is worse than the mistake being removed, so
			// name what still points here rather than deleting and letting it
			// fail later at spawn time.
			return clikit.Refusedf("%v — retire or repoint them first", ref)
		}
		return err
	}
	fmt.Fprintf(ctx.Stdout, "removed role %s\n", name)
	return nil
}

// taskBody is everything the task says about itself EXCEPT its title — the
// acceptance criteria and context. Kept separate from the title because
// routing weights the two differently: the title names the domain, the body
// describes verification in vocabulary every candidate shares (see
// team.CheapestCapableForTitled).
func taskBody(t *store.Task) string {
	var b strings.Builder
	for _, s := range t.Doc.Sections {
		if strings.EqualFold(s.Title, "Log") {
			continue // the log records what happened, not what the task is
		}
		b.WriteString(" ")
		b.WriteString(s.Content)
	}
	return strings.TrimSpace(b.String())
}
