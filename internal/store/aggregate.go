package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const (
	TaskKindWork      = "work"
	TaskKindAggregate = "aggregate"
)

func (t *Task) TaskKind() string {
	kind, _ := t.Doc.Front.Get("task_kind")
	if kind == "" {
		return TaskKindWork
	}
	return kind
}

func (t *Task) IsAggregate() bool { return t != nil && t.TaskKind() == TaskKindAggregate }

func (t *Task) AggregateChildren() []string {
	if !t.IsAggregate() {
		return nil
	}
	return t.Doc.Front.GetList("aggregate_children")
}

type AggregateChildState struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	CompletionState string `json:"completion_state,omitempty"`
	AcceptanceDone  int    `json:"acceptance_done"`
	AcceptanceTotal int    `json:"acceptance_total"`
	Verified        bool   `json:"verified"`
	Landed          bool   `json:"landed"`
	LandingState    string `json:"landing_state,omitempty"`
	LandingReason   string `json:"landing_reason,omitempty"`
	LandingReceipt  string `json:"landing_receipt_id,omitempty"`
	Blocker         string `json:"blocker,omitempty"`
}

type AggregateProgress struct {
	Schema          string                `json:"schema"`
	Version         int                   `json:"version"`
	TaskID          string                `json:"task_id"`
	CompletionState string                `json:"completion_state,omitempty"`
	Kind            string                `json:"kind"`
	RequiredDone    int                   `json:"required_done"`
	Required        int                   `json:"required"`
	ReadyToClose    bool                  `json:"ready_to_close"`
	Blockers        []string              `json:"blockers"`
	Children        []AggregateChildState `json:"children"`
}

// AggregateProgressFor derives milestone state exclusively from the stable
// child IDs frozen by the repair/decomposition plan. Descriptive hierarchy is
// deliberately ignored unless the parent explicitly opted into aggregate
// semantics (issue #866).
func AggregateProgressFor(w *workspace.Workspace, parent *Task) (AggregateProgress, error) {
	p := AggregateProgress{Schema: "aggregate-progress/v1", Version: 1, TaskID: parent.ID, CompletionState: parent.CompletionState(), Kind: parent.TaskKind(), ReadyToClose: true, Blockers: []string{}, Children: []AggregateChildState{}}
	if !parent.IsAggregate() {
		return p, nil
	}
	tasks, err := ListTasks(w, parent.Project, "")
	if err != nil {
		return p, err
	}
	idx := NewTaskIndex(tasks)
	project, err := LoadProject(w, parent.Project)
	if err != nil {
		return p, err
	}
	landing, _, err := model.ResolveLanding(project.Landing, model.LandingOverride{})
	if err != nil {
		return p, err
	}
	for _, id := range parent.AggregateChildren() {
		p.Required++
		state := AggregateChildState{ID: id}
		child, findErr := idx.Find(id)
		if findErr != nil {
			state.Blocker = "required child is missing"
			p.ReadyToClose = false
			p.Blockers = append(p.Blockers, id+": "+state.Blocker)
			p.Children = append(p.Children, state)
			continue
		}
		state.Status = string(child.Status)
		state.CompletionState = child.CompletionState()
		for _, box := range child.Acceptance() {
			state.AcceptanceTotal++
			if box.Done {
				state.AcceptanceDone++
			}
		}
		state.Verified = state.AcceptanceTotal > 0 && state.AcceptanceDone == state.AcceptanceTotal
		state.Landed = child.Status == model.StatusDone
		base := landing.Base
		if base == "" {
			base = "main"
		}
		if observation, present := ReadTaskLanding(context.Background(), w, child, base); present {
			state.Landed = observation.State == LandingLanded
			state.LandingState = taskLandingStateName(observation.State)
			state.LandingReason, state.LandingReceipt = observation.Reason, observation.ReceiptID
		} else if landing.Mode == model.LandingPR {
			landed, _ := CheckLanded(w, child, base)
			state.Landed = landed == LandingLanded
		}
		switch {
		case child.Status == model.StatusBlocked:
			state.Blocker = "required child is blocked"
		case child.Status != model.StatusDone:
			state.Blocker = "required child is open"
		case !state.Verified:
			state.Blocker = "required child is unverified"
		case !state.Landed && state.LandingState == "unknown":
			state.Blocker = "required child landing is unknown; record or reconcile native landing evidence"
		case !state.Landed:
			state.Blocker = "required child is unlanded under project policy"
		default:
			p.RequiredDone++
		}
		if state.Blocker != "" {
			p.ReadyToClose = false
			p.Blockers = append(p.Blockers, id+": "+state.Blocker)
		}
		p.Children = append(p.Children, state)
	}
	if p.Required == 0 {
		p.ReadyToClose = false
		p.Blockers = append(p.Blockers, "aggregate has no required children")
	}
	return p, nil
}

func taskLandingStateName(state LandingState) string {
	switch state {
	case LandingLanded:
		return "landed"
	case LandingUnlanded:
		return "unlanded"
	default:
		return "unknown"
	}
}

func RefuseIncompleteAggregate(w *workspace.Workspace, parent *Task) error {
	if !parent.IsAggregate() {
		return nil
	}
	p, err := AggregateProgressFor(w, parent)
	if err != nil {
		return err
	}
	if !p.ReadyToClose {
		return fmt.Errorf("aggregate task cannot close (%d/%d required children complete): %s", p.RequiredDone, p.Required, strings.Join(p.Blockers, "; "))
	}
	return nil
}

type AggregateRepairPlan struct {
	Schema      string   `json:"schema"`
	Version     int      `json:"version"`
	ID          string   `json:"id"`
	TaskID      string   `json:"task_id"`
	Project     string   `json:"project"`
	GraphDigest string   `json:"graph_digest"`
	ChildIDs    []string `json:"child_ids"`
	AddDeps     []string `json:"add_dependencies"`
}

type AggregateRepairCandidate struct {
	Parent   *Task
	Children []*Task
}

// AggregateRepairCandidates finds the dangerous shape, but does not infer
// intent: a still-ordinary parent is independently ready while two or more
// direct implementation children are open. Doctor presents this as an
// operator-reviewed repair proposal; only task aggregate --apply changes it.
func AggregateRepairCandidates(tasks []*Task) []AggregateRepairCandidate {
	ready := map[string]bool{}
	for _, task := range ReadyFrontier(tasks).Ready {
		ready[task.ID] = true
	}
	children := map[string][]*Task{}
	for _, task := range tasks {
		parent, _ := task.Doc.Front.Get("parent")
		parent = strings.TrimSuffix(strings.TrimPrefix(parent, "[["), "]]")
		if parent != "" && task.Status != model.StatusDone {
			children[parent] = append(children[parent], task)
		}
	}
	out := []AggregateRepairCandidate{}
	for _, parent := range tasks {
		if parent.IsAggregate() || !ready[parent.ID] || len(children[parent.ID]) < 2 {
			continue
		}
		out = append(out, AggregateRepairCandidate{Parent: parent, Children: children[parent.ID]})
	}
	return out
}

type graphFact struct {
	ID, Parent, Kind, Status string
	Deps                     []Dep
}

func taskGraphDigest(tasks []*Task) (string, error) {
	facts := make([]graphFact, 0, len(tasks))
	for _, task := range tasks {
		parent, _ := task.Doc.Front.Get("parent")
		facts = append(facts, graphFact{ID: task.ID, Parent: parent, Kind: task.TaskKind(), Status: string(task.Status), Deps: task.Deps()})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	raw, err := json.Marshal(facts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func BuildAggregateRepairPlan(w *workspace.Workspace, parent *Task) (AggregateRepairPlan, error) {
	tasks, err := ListTasks(w, parent.Project, "")
	if err != nil {
		return AggregateRepairPlan{}, err
	}
	children := []string{}
	for _, task := range tasks {
		ref, _ := task.Doc.Front.Get("parent")
		if strings.TrimSuffix(strings.TrimPrefix(ref, "[["), "]]") == parent.ID {
			children = append(children, task.ID)
		}
	}
	if len(children) == 0 {
		return AggregateRepairPlan{}, fmt.Errorf("task %03d-%s has no direct children to aggregate", parent.Seq, parent.Slug)
	}
	sort.Strings(children)
	digest, err := taskGraphDigest(tasks)
	if err != nil {
		return AggregateRepairPlan{}, err
	}
	plan := AggregateRepairPlan{Schema: "aggregate-repair/v1", Version: 1, TaskID: parent.ID, Project: parent.Project, GraphDigest: digest, ChildIDs: children}
	for _, id := range children {
		plan.AddDeps = append(plan.AddDeps, id+":FS")
	}
	return sealAggregatePlan(plan)
}

func sealAggregatePlan(plan AggregateRepairPlan) (AggregateRepairPlan, error) {
	plan.ID = ""
	raw, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	plan.ID = fmt.Sprintf("%x", sha256.Sum256(raw))
	return plan, nil
}

func ApplyAggregateRepairPlan(w *workspace.Workspace, parent *Task, requested string) (AggregateRepairPlan, error) {
	current, err := BuildAggregateRepairPlan(w, parent)
	if err != nil {
		return AggregateRepairPlan{}, err
	}
	if current.ID != requested {
		return AggregateRepairPlan{}, fmt.Errorf("aggregate repair plan changed (requested %s, current %s); preview the new graph before applying", requested, current.ID)
	}
	change := DependencyChange{Add: current.AddDeps}
	if err := ValidateDependencyChange(w, parent, change); err != nil {
		return AggregateRepairPlan{}, err
	}
	if err := persistImmutablePlan(w, "aggregate", current.ID, current); err != nil {
		return AggregateRepairPlan{}, err
	}
	if err := WithTask(w, parent, func(fresh *Task) error {
		fresh.Doc.Front.Set("task_kind", TaskKindAggregate)
		fresh.Doc.Front.SetList("aggregate_children", current.ChildIDs)
		AppendLog(fresh, "aggregate repair plan "+current.ID+" applied")
		return ApplyDependencyChange(w, fresh, change)
	}); err != nil {
		return AggregateRepairPlan{}, err
	}
	return current, nil
}

type DecompositionChild struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Acceptance []string `json:"acceptance"`
	Estimate   string   `json:"estimate"`
	Claims     []string `json:"claims"`
	DependsOn  []string `json:"depends_on"`
}

type DecompositionPlan struct {
	Schema      string               `json:"schema"`
	Version     int                  `json:"version"`
	ID          string               `json:"id"`
	TaskID      string               `json:"task_id"`
	Project     string               `json:"project"`
	GraphDigest string               `json:"graph_digest"`
	Children    []DecompositionChild `json:"children"`
}

func BuildDecompositionPlan(w *workspace.Workspace, parent *Task) (DecompositionPlan, error) {
	tasks, err := ListTasks(w, parent.Project, "")
	if err != nil {
		return DecompositionPlan{}, err
	}
	for _, task := range tasks {
		ref, _ := task.Doc.Front.Get("parent")
		if strings.TrimSuffix(strings.TrimPrefix(ref, "[["), "]]") == parent.ID {
			return DecompositionPlan{}, fmt.Errorf("task %03d-%s is not a leaf; repair its existing child graph instead", parent.Seq, parent.Slug)
		}
	}
	est, ok := parent.Estimate()
	if !ok || est.Expected() <= 8 {
		return DecompositionPlan{}, fmt.Errorf("task %03d-%s is not demonstrably oversized (expected estimate must exceed 8)", parent.Seq, parent.Slug)
	}
	boxes := parent.Acceptance()
	if len(boxes) < 2 {
		return DecompositionPlan{}, fmt.Errorf("oversized leaf needs at least two acceptance criteria before a WBS can be proposed")
	}
	digest, err := taskGraphDigest(tasks)
	if err != nil {
		return DecompositionPlan{}, err
	}
	plan := DecompositionPlan{Schema: "task-decomposition/v1", Version: 1, TaskID: parent.ID, Project: parent.Project, GraphDigest: digest, Children: []DecompositionChild{}}
	for i, box := range boxes {
		claims := PathTokens(box.Text)
		if len(claims) == 0 {
			claims = parent.PathHints()
		}
		if len(claims) == 0 {
			return DecompositionPlan{}, fmt.Errorf("criterion %d has no path claim; name the minimal file or directory before proposing decomposition", i+1)
		}
		seed := fmt.Sprintf("%s\x00%s\x00%d\x00%s", parent.ID, digest, i, box.Text)
		id := fmt.Sprintf("t-%x", sha256.Sum256([]byte(seed)))[:28]
		child := DecompositionChild{ID: id, Title: box.Text, Acceptance: []string{box.Text}, Estimate: "1,2,3", Claims: []string{claims[0]}}
		if i > 0 {
			child.DependsOn = []string{plan.Children[0].ID + ":SS"}
		}
		plan.Children = append(plan.Children, child)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return DecompositionPlan{}, err
	}
	plan.ID = fmt.Sprintf("%x", sha256.Sum256(raw))
	return plan, nil
}

func ApplyDecompositionPlan(w *workspace.Workspace, parent *Task, requested, actor string) (DecompositionPlan, error) {
	plan, err := BuildDecompositionPlan(w, parent)
	if err != nil {
		return DecompositionPlan{}, err
	}
	if plan.ID != requested {
		return DecompositionPlan{}, fmt.Errorf("decomposition plan changed (requested %s, current %s); preview the new graph before applying", requested, plan.ID)
	}
	if err := persistImmutablePlan(w, "decomposition", plan.ID, plan); err != nil {
		return DecompositionPlan{}, err
	}
	created := []*Task{}
	rollback := func() {
		for _, task := range created {
			_ = os.Remove(task.Path)
		}
	}
	for _, child := range plan.Children {
		task, createErr := CreateTask(w, actor, parent.Project, child.Title, TaskOpts{Accept: child.Acceptance, Estimate: child.Estimate, Parent: parent.ID, Claims: child.Claims, DependsOn: child.DependsOn, StableID: child.ID})
		if createErr != nil {
			rollback()
			return DecompositionPlan{}, createErr
		}
		created = append(created, task)
	}
	children := make([]string, 0, len(plan.Children))
	deps := make([]string, 0, len(plan.Children))
	for _, child := range plan.Children {
		children = append(children, child.ID)
		deps = append(deps, child.ID+":FS")
	}
	if err := WithTask(w, parent, func(fresh *Task) error {
		fresh.Doc.Front.Set("task_kind", TaskKindAggregate)
		fresh.Doc.Front.SetList("aggregate_children", children)
		AppendLog(fresh, "decomposition plan "+plan.ID+" applied")
		return ApplyDependencyChange(w, fresh, DependencyChange{Add: deps})
	}); err != nil {
		rollback()
		return DecompositionPlan{}, err
	}
	return plan, nil
}

func persistImmutablePlan(w *workspace.Workspace, kind, id string, plan any) error {
	dir := filepath.Join(w.Root, workspace.Dir, "plans", kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join(dir, id+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		if _, err = f.Write(raw); err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	if !os.IsExist(err) {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(existing, raw) {
		return fmt.Errorf("persisted plan %s does not match its content address", id)
	}
	return nil
}
