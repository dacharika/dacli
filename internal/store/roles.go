package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// CreateRole writes .dacli/roles/<name>.md. A role must change what an agent
// can do — skills, scope, shortcuts, escalation — not just what it calls
// itself; a role that sets none of these is cosplay and gets flagged.
func CreateRole(w *workspace.Workspace, actor string, r team.Role) error {
	if r.Name == "" {
		return fmt.Errorf("role needs a name")
	}
	// A role name becomes a filename; reject traversal (dacli 200).
	if !workspace.SafeSegment(r.Name) {
		return fmt.Errorf("invalid role name %q: must be a single path segment without '/' or '..'", r.Name)
	}
	path := w.RolePath(r.Name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("role %q already exists", r.Name)
	}

	d := &mdstore.Doc{}
	d.Front.Set("id", "role-"+r.Name)
	d.Front.Set("kind", string(model.KindRole))
	d.Front.Set("created", now())
	d.Front.Set("created_by", actor)
	d.Front.Set("name", r.Name)
	// A role is versioned from birth so `role show` always has something
	// legible to print and every later edit has a baseline to bump past.
	d.Front.Set("version", DefaultVersion)
	if r.Summary != "" {
		d.Front.Set("summary", r.Summary)
	}
	setList := func(k string, v []string) {
		if len(v) > 0 {
			d.Front.SetList(k, v)
		}
	}
	setList("skills", r.Skills)
	setList("scope", r.Scope)
	setList("out_of_scope", r.OutOfScope)
	setList("shortcuts", r.Shortcuts)
	setList("escalate_to", r.EscalateTo)
	setList("fallback_to", r.FallbackTo)
	if r.Grant != "" {
		d.Front.Set("grant", r.Grant)
	}
	if r.Kind != "" {
		// role_kind, NOT kind: `kind` is the object-type frontmatter ("role")
		// every file carries. Reusing it made every role read back kind="role"
		// and get gated as an unknown kind. Found by the phase test.
		d.Front.Set("role_kind", r.Kind)
	}
	if r.WIP > 0 {
		d.Front.Set("wip", fmt.Sprint(r.WIP))
	}
	if r.Runtime != "" {
		d.Front.Set("runtime", r.Runtime)
	}
	if r.Model != "" {
		d.Front.Set("model", r.Model)
	}
	if r.Profile.ID != "" {
		d.Front.Set("model_id", r.Profile.ID)
	}
	if r.Profile.CostTier >= 1 && r.Profile.CostTier <= 98 {
		d.Front.Set("cost_tier", fmt.Sprint(r.Profile.CostTier))
	}
	if r.Profile.MaxTaskPoints > 0 {
		d.Front.Set("max_task_points", fmt.Sprint(r.Profile.MaxTaskPoints))
	}
	if r.Profile.ContextLimit > 0 {
		d.Front.Set("context_limit", fmt.Sprint(r.Profile.ContextLimit))
	}
	setList("capability_tags", r.Profile.CapabilityTags)
	if r.MaxPoints > 0 {
		d.Front.Set("max_points", fmt.Sprint(r.MaxPoints))
	}
	d.Sections = []mdstore.Section{{Level: 1, Title: r.Name, Content: r.Summary + "\n"}}
	return mdstore.WriteFile(path, d)
}

// parseRole builds the pure engine's type from a parsed role doc. fallbackName
// is used when the file omits an explicit name (defaults to the filename).
func parseRole(d *mdstore.Doc, fallbackName string) team.Role {
	r := team.Role{}
	r.Name, _ = d.Front.Get("name")
	if r.Name == "" {
		r.Name = fallbackName
	}
	r.Summary, _ = d.Front.Get("summary")
	r.Skills = d.Front.GetList("skills")
	r.Scope = d.Front.GetList("scope")
	r.OutOfScope = d.Front.GetList("out_of_scope")
	r.Shortcuts = d.Front.GetList("shortcuts")
	r.EscalateTo = d.Front.GetList("escalate_to")
	r.FallbackTo = d.Front.GetList("fallback_to")
	r.Grant, _ = d.Front.Get("grant")
	if wip, ok := d.Front.Get("wip"); ok {
		_, _ = fmt.Sscanf(wip, "%d", &r.WIP)
	}
	r.Kind, _ = d.Front.Get("role_kind")
	r.Runtime, _ = d.Front.Get("runtime")
	r.Model, _ = d.Front.Get("model")
	r.Profile.ID, _ = d.Front.Get("model_id")
	if r.Profile.ID == "" {
		r.Profile.ID = r.Model
	} else if r.Model == "" {
		// Keep pre-profile consumers working during migration. The authoritative
		// declaration is model_id; Model is only the compatibility projection.
		r.Model = r.Profile.ID
	}
	if v, ok := d.Front.Get("cost_tier"); ok {
		_, _ = fmt.Sscanf(v, "%d", &r.Profile.CostTier)
	} else {
		// Roles written before model profiles existed encoded the original
		// three-tier catalog in model names. Keep that compatibility heuristic
		// at the persistence boundary so the routing domain remains entirely
		// provider-neutral and old rosters do not silently become unpriced.
		r.Profile.CostTier = legacyModelCostTier(r.Model)
	}
	if v, ok := d.Front.Get("max_task_points"); ok {
		_, _ = fmt.Sscanf(v, "%g", &r.Profile.MaxTaskPoints)
	}
	if v, ok := d.Front.Get("context_limit"); ok {
		_, _ = fmt.Sscanf(v, "%d", &r.Profile.ContextLimit)
	}
	r.Profile.CapabilityTags = d.Front.GetList("capability_tags")
	if mp, ok := d.Front.Get("max_points"); ok {
		_, _ = fmt.Sscanf(mp, "%g", &r.MaxPoints)
	}
	if r.Profile.MaxTaskPoints == 0 {
		r.Profile.MaxTaskPoints = r.MaxPoints
	} else if r.MaxPoints == 0 {
		// As above, old spawn/capacity gates see the new declaration without
		// requiring every feature slice to migrate in the same commit.
		r.MaxPoints = r.Profile.MaxTaskPoints
	}
	r.Prompt = roleBody(d, r.Name, r.Summary)
	return r
}

// legacyModelCostTier is migration-only: new role files declare cost_tier.
// Unknown legacy models stay undeclared (zero), which the routing policy ranks
// as tier 99. Once persisted profiles are ubiquitous this boundary can go away
// without changing the provider-neutral team package.
func legacyModelCostTier(modelID string) int {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	for i, marker := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(modelID, marker) {
			return i + 1
		}
	}
	return 0
}

// roleBody extracts the role file's standing instructions: everything under the
// frontmatter, minus the conventional `# <name>` title and a first line that
// merely repeats the summary. What remains is the role's method — the part a
// spawned agent actually needs and, until dacli 202, the part that was parsed
// and thrown away. Returns "" when the file carries no instructions beyond its
// metadata, so callers can tell a described role from a defined one.
func roleBody(d *mdstore.Doc, name, summary string) string {
	var b strings.Builder
	for _, s := range d.Sections {
		// Skip the conventional H1 title (`# fixer`), keep any real section.
		if s.Level == 1 && strings.EqualFold(strings.TrimSpace(s.Title), name) {
			b.WriteString(s.Content)
			continue
		}
		if s.Title != "" {
			b.WriteString("\n" + strings.Repeat("#", s.Level) + " " + s.Title + "\n")
		}
		b.WriteString(s.Content)
	}
	body := strings.TrimSpace(b.String())
	// A body that only restates the summary adds nothing to a brief.
	if body == "" || strings.EqualFold(body, strings.TrimSpace(summary)) {
		return ""
	}
	return body
}

// LoadRoles parses every role file into the pure engine's type.
func LoadRoles(w *workspace.Workspace) ([]team.Role, error) {
	entries, err := os.ReadDir(w.RolesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no roles dir yet is not an error
		}
		return nil, err // a real I/O/permission failure must not read as "empty"
	}
	var out []team.Role
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		d, err := mdstore.ReadFile(w.RolePath(name))
		if err != nil {
			continue
		}
		out = append(out, parseRole(d, name))
	}
	return out, nil
}

// LoadRole reads one role by name from its exact file, rather than scanning the
// whole directory through LoadRoles.
func LoadRole(w *workspace.Workspace, name string) (team.Role, bool) {
	d, err := mdstore.ReadFile(w.RolePath(name))
	if err != nil {
		return team.Role{}, false
	}
	return parseRole(d, name), true
}

// AgentInfo is the file-level view of an agent, for rosters and standups.
type AgentInfo struct {
	ID      string
	Role    string
	Grant   string
	Parent  string
	Retired bool
}

// ListAgents reads every agent file.
func ListAgents(w *workspace.Workspace) ([]AgentInfo, error) {
	entries, err := os.ReadDir(w.AgentsDir())
	if err != nil {
		return nil, err
	}
	var out []AgentInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if e.IsDir() {
			return nil, fmt.Errorf("read agent %s: expected a file, found a directory", e.Name())
		}
		d, err := mdstore.ReadFile(w.AgentPath(strings.TrimSuffix(e.Name(), ".md")))
		if err != nil {
			return nil, fmt.Errorf("read agent %s: %w", e.Name(), err)
		}
		a := AgentInfo{}
		a.ID, _ = d.Front.Get("id")
		a.Role, _ = d.Front.Get("role")
		a.Grant, _ = d.Front.Get("grant")
		if p, ok := d.Front.Get("parent"); ok {
			a.Parent = strings.TrimSuffix(strings.TrimPrefix(p, "[["), "]]")
		}
		if r, _ := d.Front.Get("retired"); r == "true" {
			a.Retired = true
		}
		out = append(out, a)
	}
	return out, nil
}

// LiveOccupancyByRole counts liveness-probed runs by the recorded agent's role.
// It is the shared WIP census for spawn gates and operator read models: durable
// agent identities preserve attribution, but only a process that is alive now
// consumes execution capacity (issue #697).
func LiveOccupancyByRole(w *workspace.Workspace) (map[string]int, error) {
	if _, err := ListAgents(w); err != nil {
		return nil, err
	}
	runs, err := loadAgentRunState(w)
	if err != nil {
		return nil, err
	}
	occupancy := map[string]int{}
	for _, role := range runs.liveRole {
		occupancy[role]++
	}
	return occupancy, nil
}

// ActiveInRole returns one role's live WIP occupancy. The name remains for API
// compatibility; callers presenting the value should call it occupancy, not
// identity activity.
//
// An unreadable agents dir is a real fault, not "zero agents" — ListAgents'
// error is returned rather than swallowed, so a caller enforcing a WIP cap
// (gateRoleWIP) can fail closed instead of certifying a count it never
// actually read (dacli 341, the same "a gate must never certify what it
// could not read" rule dacli 337 already applies to the runs dir).
func ActiveInRole(w *workspace.Workspace, role string) (int, error) {
	occupancy, err := LiveOccupancyByRole(w)
	if err != nil {
		return 0, err
	}
	return occupancy[role], nil
}

// holdsWIPSlot decides whether one non-retired agent still occupies capacity.
//
// The discriminator is NOT "does it have a live process" alone: `agent spawn`
// mints an identity BEFORE any process exists (the token is handed to a child
// that runs afterwards, possibly outside dacli), so a just-minted agent is
// about to work and must keep its slot. What must not keep a slot is an agent
// that RAN and FINISHED, which is exactly task 282's "finished but never
// retired" — nothing in the run lifecycle calls RetireAgent, so those piled up
// until a role was permanently full while `dacli agents` showed nobody live.
//
// So: live counts; ran-and-finished does not; minted-but-never-run counts,
// because its work has not happened yet.
func holdsWIPSlot(id string, runs agentRunState) bool {
	if runs.live[id] != "" {
		return true
	}
	return !runs.finished[id]
}

// agentRunState is durable lifecycle evidence shared by the deliberately
// different predicates: live process occupancy and conservative role-removal
// provenance. Sharing the scan must not collapse those policies (issues #690,
// #697).
type agentRunState struct {
	finished map[string]bool
	live     map[string]string // child id -> run id
	liveRole map[string]string // child id -> role recorded by the live run
}

// loadAgentRunState scans proc records once and fails closed when recorded run
// state exists but cannot be read. Run directories without proc.txt are valid:
// verification and other non-agent runs use the same runs tree.
func loadAgentRunState(w *workspace.Workspace) (agentRunState, error) {
	state := agentRunState{finished: map[string]bool{}, live: map[string]string{}, liveRole: map[string]string{}}
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("read runs: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(w.RunDir(e.Name()), "proc.txt")
		rec, err := procmon.ReadRecord(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return state, fmt.Errorf("read run %s: %w", e.Name(), err)
		}
		if rec.Child == "" {
			return state, fmt.Errorf("read run %s: proc.txt has no child", e.Name())
		}
		if procmon.AliveRecord(rec) {
			state.live[rec.Child] = e.Name()
			state.liveRole[rec.Child] = rec.Role
		} else {
			state.finished[rec.Child] = true
		}
	}
	return state, nil
}

// RetireAgent marks an agent retired, freeing its WIP slot. The file stays —
// lineage and attribution outlive the agent.
func RetireAgent(w *workspace.Workspace, id string) error {
	d, err := mdstore.ReadFile(w.AgentPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound{Ref: "agent/" + id}
		}
		return err
	}
	d.Front.Set("retired", "true")
	return mdstore.WriteFile(w.AgentPath(id), d)
}
