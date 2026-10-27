// Package store models a delivery slice as a typed child task, not a second
// lifecycle. The child keeps the existing status, acceptance, claim and event
// machinery; these helpers only add generation-scoped delivery identity
// (issue #872).
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type DeliverySliceOpts struct {
	Generation       int
	ParentGeneration int
	Required         bool
	Terminal         bool
}

type DeliverySliceState struct {
	ID               string                 `json:"id"`
	ParentTask       string                 `json:"parent_task"`
	Generation       int                    `json:"generation"`
	ParentGeneration int                    `json:"parent_generation"`
	Required         bool                   `json:"required"`
	Terminal         bool                   `json:"terminal"`
	Status           string                 `json:"status"`
	CompletionState  string                 `json:"completion_state,omitempty"`
	Branch           string                 `json:"branch"`
	HeadSHA          string                 `json:"head_sha,omitempty"`
	TreeSHA          string                 `json:"tree_sha,omitempty"`
	AcceptanceDone   int                    `json:"acceptance_done"`
	AcceptanceTotal  int                    `json:"acceptance_total"`
	Acceptance       []DeliveryCriterion    `json:"acceptance"`
	Verification     []VerificationEvidence `json:"verification_evidence"`
	PRURL            string                 `json:"pr_url,omitempty"`
	PRNumber         int                    `json:"pr_number,omitempty"`
	MergeSHA         string                 `json:"merge_sha,omitempty"`
	ObservedAt       string                 `json:"observed_at,omitempty"`
	Claims           []string               `json:"claims"`
	CleanupState     string                 `json:"cleanup_state"`
	Landed           bool                   `json:"landed"`
	LandingState     string                 `json:"landing_state,omitempty"`
	LandingReason    string                 `json:"landing_reason,omitempty"`
	LandingReceipt   string                 `json:"landing_receipt_id,omitempty"`
}

type DeliveryCriterion struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type DeliveryProgress struct {
	Schema           string               `json:"schema"`
	Version          int                  `json:"version"`
	ParentTask       string               `json:"parent_task"`
	CompletionState  string               `json:"completion_state,omitempty"`
	ParentGeneration int                  `json:"parent_generation"`
	RequiredDone     int                  `json:"required_done"`
	RequiredTotal    int                  `json:"required_total"`
	ReadyToClose     bool                 `json:"ready_to_close"`
	Slices           []DeliverySliceState `json:"slices"`
}

func FindDeliverySlice(w *workspace.Workspace, ref string) (*Task, error) {
	if t, err := FindTask(w, ref); err == nil {
		if !t.IsDeliverySlice() {
			return nil, fmt.Errorf("task %s is not a delivery slice", t.ID)
		}
		return t, nil
	}
	parentRef, sliceRef, ok := strings.Cut(ref, "/")
	if !ok {
		return nil, ErrNotFound{Ref: "delivery-slice/" + ref}
	}
	parent, err := FindTask(w, parentRef)
	if err != nil {
		return nil, err
	}
	slices, err := DeliverySlices(w, parent)
	if err != nil {
		return nil, err
	}
	sliceRef = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(sliceRef)), "g")
	for _, slice := range slices {
		if strconv.Itoa(slice.DeliveryGeneration()) == sliceRef || slice.ID == sliceRef || slice.Slug == sliceRef || fmt.Sprintf("%03d", slice.Seq) == sliceRef {
			return slice, nil
		}
	}
	return nil, ErrNotFound{Ref: "delivery-slice/" + ref}
}

func (t *Task) IsDeliverySlice() bool {
	if t == nil || t.Doc == nil {
		return false
	}
	v, _ := t.Doc.Front.Get("delivery_slice")
	return strings.EqualFold(v, "true")
}

func (t *Task) DeliveryGeneration() int       { return nonnegativeFrontInt(t, "delivery_generation") }
func (t *Task) DeliveryParentGeneration() int { return nonnegativeFrontInt(t, "parent_generation") }
func (t *Task) DeliveryRequired() bool {
	v, _ := t.Doc.Front.Get("delivery_required")
	return strings.EqualFold(v, "true")
}
func (t *Task) DeliveryTerminal() bool {
	v, _ := t.Doc.Front.Get("delivery_terminal")
	return strings.EqualFold(v, "true")
}

func (t *Task) ParentID() string {
	v, _ := t.Doc.Front.Get("parent")
	return strings.TrimSuffix(strings.TrimPrefix(v, "[["), "]]")
}

func nonnegativeFrontInt(t *Task, key string) int {
	v, _ := t.Doc.Front.Get(key)
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// TaskBranch gives each slice generation a branch identity that cannot alias
// either its parent or a historical slice generation.
func deliverySliceBranch(t *Task) string {
	return fmt.Sprintf("dacli/%03d-%s/slice-g%d-r%d", t.Seq, t.Slug, t.DeliveryGeneration(), t.Generation())
}

func CreateDeliverySlice(w *workspace.Workspace, actor, parentRef, title string, accept []string, required, terminal bool) (*Task, bool, error) {
	parent, err := FindTask(w, parentRef)
	if err != nil {
		return nil, false, err
	}
	if parent.IsDeliverySlice() {
		return nil, false, fmt.Errorf("delivery slice parent must be a product task, not slice %s", parent.ID)
	}
	lock := w.ProjectDir(parent.Project) + "/.delivery-slices.lock"
	var result *Task
	created := false
	err = WithFileLock(lock, func() error {
		tasks, listErr := ListTasks(w, parent.Project, "")
		if listErr != nil {
			return listErr
		}
		next := 1
		for _, candidate := range tasks {
			if !candidate.IsDeliverySlice() || candidate.ParentID() != parent.ID || candidate.DeliveryParentGeneration() != parent.Generation() {
				continue
			}
			if candidate.Title == title {
				result = candidate
				return nil
			}
			if terminal && candidate.DeliveryTerminal() {
				return fmt.Errorf("parent generation already has terminal delivery slice %s", candidate.ID)
			}
			if candidate.DeliveryGeneration() >= next {
				next = candidate.DeliveryGeneration() + 1
			}
		}
		if parent.Status == model.StatusDone {
			return fmt.Errorf("parent task %s is done; reopen it before adding corrective delivery", parent.ID)
		}
		result, listErr = CreateTask(w, actor, parent.Project, title, TaskOpts{
			Accept: accept, Parent: parent.ID,
			Delivery: &DeliverySliceOpts{Generation: next, ParentGeneration: parent.Generation(), Required: required, Terminal: terminal},
		})
		created = listErr == nil
		return listErr
	})
	if err == nil && required && result != nil {
		// Make the typed child edge visible to the existing dependency/CPM
		// machinery. Replays also repair an interruption after the child write
		// but before this parent rewrite, without creating a duplicate slice.
		err = WithTask(w, parent, func(fresh *Task) error {
			return ApplyDependencyChange(w, fresh, DependencyChange{Add: []string{result.ID + ":FS"}})
		})
	}
	return result, created, err
}

func DeliverySlices(w *workspace.Workspace, parent *Task) ([]*Task, error) {
	tasks, err := ListTasks(w, parent.Project, "")
	if err != nil {
		return nil, err
	}
	out := []*Task{}
	for _, t := range tasks {
		if t.IsDeliverySlice() && t.ParentID() == parent.ID && t.DeliveryParentGeneration() == parent.Generation() {
			out = append(out, t)
		}
	}
	return out, nil
}

func DeliveryProgressFor(w *workspace.Workspace, parent *Task) (DeliveryProgress, error) {
	slices, err := DeliverySlices(w, parent)
	if err != nil {
		return DeliveryProgress{}, err
	}
	p := DeliveryProgress{Schema: "delivery-progress/v1", Version: 1, ParentTask: parent.ID, CompletionState: parent.CompletionState(), ParentGeneration: parent.Generation(), ReadyToClose: true, Slices: []DeliverySliceState{}}
	for _, slice := range slices {
		state := deliveryState(w, slice)
		p.Slices = append(p.Slices, state)
		if state.Required {
			p.RequiredTotal++
			if state.Status == string(model.StatusDone) && state.Landed && state.AcceptanceDone == state.AcceptanceTotal {
				p.RequiredDone++
			} else {
				p.ReadyToClose = false
			}
		}
	}
	return p, nil
}

func deliveryState(w *workspace.Workspace, t *Task) DeliverySliceState {
	boxes, done := t.Acceptance(), 0
	for _, box := range boxes {
		if box.Done {
			done++
		}
	}
	branch := TaskBranch(t)
	state := DeliverySliceState{ID: t.ID, ParentTask: t.ParentID(), Generation: t.DeliveryGeneration(), ParentGeneration: t.DeliveryParentGeneration(), Required: t.DeliveryRequired(), Terminal: t.DeliveryTerminal(), Status: string(t.Status), CompletionState: t.CompletionState(), Branch: branch, AcceptanceDone: done, AcceptanceTotal: len(boxes), Acceptance: []DeliveryCriterion{}, Verification: VerificationEvidenceRecords(t), PRURL: RecordedPRURL(t), Claims: []string{}, CleanupState: "not_started"}
	for _, box := range boxes {
		state.Acceptance = append(state.Acceptance, DeliveryCriterion{Text: box.Text, Done: box.Done})
	}
	if state.PRURL == "" {
		state.PRURL, _ = t.Doc.Front.Get("delivery_pr_url")
	}
	state.TreeSHA, _ = t.Doc.Front.Get("delivery_tree_sha")
	state.ObservedAt, _ = t.Doc.Front.Get("delivery_observed_at")
	if head, err := gitx.Run(w.Root, "rev-parse", "--verify", branch); err == nil {
		state.HeadSHA = strings.TrimSpace(head)
		if tree, treeErr := gitx.Run(w.Root, "rev-parse", state.HeadSHA+"^{tree}"); treeErr == nil {
			state.TreeSHA = strings.TrimSpace(tree)
		}
		state.CleanupState = "retained"
	}
	if v, _ := t.Doc.Front.Get("delivery_pr_number"); v != "" {
		state.PRNumber, _ = strconv.Atoi(v)
	}
	state.MergeSHA, _ = t.Doc.Front.Get("delivery_merge_sha")
	if runs, err := os.ReadDir(w.RunsDir()); err == nil {
		for _, run := range runs {
			record, readErr := procmon.ReadRecord(filepath.Join(w.RunDir(run.Name()), "proc.txt"))
			if readErr == nil && record.Task == t.ID {
				state.Claims = append(state.Claims, record.Claims...)
			}
		}
	}
	observedHead, _ := t.Doc.Front.Get("delivery_head_sha")
	base := "main"
	if project, err := LoadProject(w, t.Project); err == nil && project.Landing.Base != "" {
		base = project.Landing.Base
	}
	if observation, present := ReadTaskLanding(context.Background(), w, t, base); present {
		// Legacy delivery_* metadata cannot override an invalid selected native
		// source, nor certify another task's batch as delivered (#1209).
		state.Landed = observation.State == LandingLanded
		state.LandingState = taskLandingStateName(observation.State)
		state.LandingReason, state.LandingReceipt = observation.Reason, observation.ReceiptID
	} else if state.MergeSHA != "" && state.TreeSHA != "" && state.ObservedAt != "" && ((observedHead != "" && observedHead == state.HeadSHA) || state.HeadSHA == "") {
		state.Landed = true
	}
	if t.Status == model.StatusDone && state.HeadSHA == "" {
		state.CleanupState = "cleaned"
	}
	return state
}

func RecordDeliveryObservation(w *workspace.Workspace, slice *Task, pr DeliveryPR) error {
	if !slice.IsDeliverySlice() {
		return fmt.Errorf("task %s is not a delivery slice", slice.ID)
	}
	if pr.Number <= 0 || strings.TrimSpace(pr.URL) == "" || strings.TrimSpace(pr.HeadRefOid) == "" {
		return fmt.Errorf("delivery PR observation requires exact number, URL, and head SHA")
	}
	return WithTask(w, slice, func(fresh *Task) error {
		if pr.HeadRefName != TaskBranch(fresh) {
			return fmt.Errorf("PR head %s does not match slice branch %s", pr.HeadRefName, TaskBranch(fresh))
		}
		fresh.Doc.Front.Set("delivery_pr_number", strconv.Itoa(pr.Number))
		fresh.Doc.Front.Set("delivery_pr_url", pr.URL)
		fresh.Doc.Front.Set("delivery_head_sha", pr.HeadRefOid)
		if tree, err := gitx.Run(w.Root, "rev-parse", pr.HeadRefOid+"^{tree}"); err == nil {
			fresh.Doc.Front.Set("delivery_tree_sha", strings.TrimSpace(tree))
		}
		fresh.Doc.Front.Set("delivery_observed_at", time.Now().UTC().Format(time.RFC3339Nano))
		if pr.MergeCommit != nil && strings.EqualFold(pr.DeliveryConfidence, "MERGED") {
			fresh.Doc.Front.Set("delivery_merge_sha", pr.MergeCommit.OID)
		} else {
			// A newer open/closed PR generation must invalidate the historical
			// merge fact. Keeping it lets the old merge satisfy the new head.
			fresh.Doc.Front.Delete("delivery_merge_sha")
		}
		return SaveTask(fresh)
	})
}

func RefuseIncompleteDelivery(w *workspace.Workspace, parent *Task) error {
	progress, err := DeliveryProgressFor(w, parent)
	if err != nil {
		return err
	}
	if len(progress.Slices) == 0 {
		return nil
	} // explicit one-task/one-PR compatibility
	if !progress.ReadyToClose {
		return fmt.Errorf("required delivery slices are incomplete or not freshly observed landed (%d/%d ready)", progress.RequiredDone, progress.RequiredTotal)
	}
	return nil
}
