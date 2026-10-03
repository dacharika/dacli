package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// ObserveSwarm resolves the complete canonical graph before project filtering.
// Every file contributes to the snapshot, including cross-project dependencies.
// Markdown writers do not take the authority lock: fresh repeated observations
// are cooperative admission, not serializable canonical-edit transactions.
func (a *LeaseAdmission) ObserveSwarm(workspaceID, project string) (sessions.SwarmSnapshot, error) {
	if a == nil || workspaceID != a.workspace.ID || project == "" {
		return sessions.SwarmSnapshot{}, sessions.ErrDenied
	}
	tasks, e := store.ListTasks(&a.workspace, "", "")
	if e != nil {
		return sessions.SwarmSnapshot{}, e
	}
	duplicates, e := store.DuplicateTaskFiles(&a.workspace)
	if e != nil || len(duplicates) != 0 {
		return sessions.SwarmSnapshot{}, sessions.ErrDenied
	}
	for _, broken := range store.BrokenTaskFiles() {
		if strings.HasPrefix(broken.Path, a.workspace.Root+string(filepath.Separator)) {
			return sessions.SwarmSnapshot{}, sessions.ErrDenied
		}
	}
	ready := map[string]bool{}
	problems := map[string]bool{}
	fr := store.ReadyFrontierForProject(tasks, project)
	for _, t := range fr.Ready {
		ready[t.ID] = true
	}
	for _, p := range fr.Problems {
		problems[p.Task.ID] = true
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	type fact struct{ ID, Project, Status, Path, Digest string }
	facts := []fact{}
	candidates := []sessions.SwarmCandidate{}
	seen := map[string]bool{}
	for _, t := range tasks {
		if t.ID == "" || seen[t.ID] {
			return sessions.SwarmSnapshot{}, sessions.ErrDenied
		}
		seen[t.ID] = true
		data, e := os.ReadFile(t.Path)
		if e != nil {
			return sessions.SwarmSnapshot{}, e
		}
		digest := sha256.Sum256(data)
		facts = append(facts, fact{t.ID, t.Project, string(t.Status), t.Path, hex.EncodeToString(digest[:])})
		if t.Project != project {
			continue
		}
		reason := "dependencies_unsatisfied"
		switch {
		case ready[t.ID]:
			reason = "ready"
		case problems[t.ID]:
			reason = "dependency_invalid"
		case t.Status != model.StatusOpen:
			reason = "status_" + string(t.Status)
		case t.IsLoopAnchor() || t.IsAggregate():
			reason = "nonimplementation_task"
		case !model.Priority(t.Priority()).Schedulable():
			reason = "priority_wont"
		}
		resources := []sessions.Resource{}
		for _, path := range t.Claims() {
			resources = append(resources, sessions.Resource{Kind: "path", Path: path})
		}
		candidates = append(candidates, sessions.SwarmCandidate{TaskID: t.ID, ProjectID: t.Project, Status: string(t.Status), Reason: reason, Priority: model.Priority(t.Priority()).Rank(), Resources: resources})
	}
	data, e := json.Marshal(facts)
	if e != nil {
		return sessions.SwarmSnapshot{}, e
	}
	digest := sha256.Sum256(data)
	return sessions.SwarmSnapshot{Digest: hex.EncodeToString(digest[:]), Candidates: candidates}, nil
}

// NewSwarmAdmission adds an explicitly trusted host capability observation
// adapter. Ordinary worker admission has no host dispatch capability authority.
func NewSwarmAdmission(w *workspace.Workspace, capability sessions.SwarmCapabilityAdmission) (*LeaseAdmission, error) {
	a, e := NewLeaseAdmission(w)
	if e != nil || capability == nil {
		return nil, sessions.ErrDenied
	}
	a.swarmCapability = capability
	return a, nil
}
func (a *LeaseAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	if a == nil || a.swarmCapability == nil || w != a.workspace.ID {
		return sessions.ErrDenied
	}
	return a.swarmCapability.AdmitSwarmCapability(w, p, h, d)
}

func (a *LeaseAdmission) AdmitDispatchCapability(w, p, h, d string) error {
	observer, ok := a.swarmCapability.(sessions.DispatchCapabilityAdmission)
	if !ok || w != a.workspace.ID {
		return sessions.ErrDenied
	}
	return observer.AdmitDispatchCapability(w, p, h, d)
}
