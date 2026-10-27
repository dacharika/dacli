package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/model"
)

// These consumer fixtures use synthetic owner observations over real Git
// history. Producer tests separately establish authenticated publication joins.
func TestNativeBatchLandingProgressAcceptanceCleanupAgreement(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.sourceCommit()
	f.mergeSource()
	parent, err := CreateTask(f.w, "a-root", "core", "Deliver the native batch", TaskOpts{Accept: []string{"all required native deliveries landed"}})
	if err != nil {
		t.Fatal(err)
	}
	children := []*Task{f.task}
	for i := 2; i <= 3; i++ {
		child, err := CreateTask(f.w, "a-root", "core", fmt.Sprintf("Native child %d", i), TaskOpts{Parent: parent.ID, Accept: []string{"land the exact source"}})
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
	}
	var ids []string
	for _, child := range children {
		child.Doc.Front.Set("parent", "[["+parent.ID+"]]")
		CheckAllAcceptance(child)
		if err := SaveTask(child); err != nil {
			t.Fatal(err)
		}
		if err := MoveTask(f.w, child, model.StatusDone); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, child.ID)
	}
	parent.Doc.Front.Set("task_kind", TaskKindAggregate)
	parent.Doc.Front.SetList("aggregate_children", ids)
	if err := SaveTask(parent); err != nil {
		t.Fatal(err)
	}
	before, err := AggregateProgressFor(f.w, parent)
	if err != nil || before.RequiredDone != 0 || before.Required != 3 || before.ReadyToClose {
		t.Fatalf("before native observations: %#v err=%v", before, err)
	}
	if err := RefuseIncompleteAggregate(f.w, parent); err == nil {
		t.Fatal("parent acceptance ignored missing task-bound landing")
	}
	receipts := map[string]string{}
	for _, child := range children {
		childFixture := *f
		childFixture.task = child
		observation := childFixture.withID(childFixture.observation(f.base, f.source, f.source, f.merge, f.target))
		if err := AppendTaskLanding(child, observation); err != nil {
			t.Fatal(err)
		}
		if err := SaveTask(child); err != nil {
			t.Fatal(err)
		}
		receipts[child.ID] = observation.ID
	}
	after, err := AggregateProgressFor(f.w, parent)
	if err != nil || after.RequiredDone != 3 || !after.ReadyToClose {
		t.Fatalf("after native observations: %#v err=%v", after, err)
	}
	for _, child := range after.Children {
		if !child.Landed || child.LandingState != "landed" || child.LandingReceipt != receipts[child.ID] {
			t.Fatalf("aggregate lost exact landing source: %#v", child)
		}
	}
	if err := RefuseIncompleteAggregate(f.w, parent); err != nil {
		t.Fatalf("parent acceptance disagrees with exact landing: %v", err)
	}
	projection, err := LocalDeliveryProjection(f.w, "core", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := BuildProgressExplain(f.w, "core", projection, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range explanation.Tasks {
		if _, selected := receipts[task.ID.Value]; selected && task.Landing.Value.Classification != "native-task-landed" {
			t.Fatalf("explanation changed selected landing truth: %#v", task.Landing)
		}
	}
	// Even an exact landed task does not authorize removal of a dirty managed
	// checkout. External worktree discovery/removal is unchanged.
	child := children[0]
	branch := TaskBranch(child)
	path := f.w.WorktreePath(child.Project, child.Seq, child.Slug)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	taskLandingGit(t, f.root, "worktree", "add", "-b", branch, path, f.source)
	if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("preserve this"), 0600); err != nil {
		t.Fatal(err)
	}
	old := ObserveDeliveryPRs
	ObserveDeliveryPRs = func(string) ([]DeliveryPR, error) {
		return []DeliveryPR{{Number: 1209, DeliveryConfidence: "MERGED", HeadRefName: branch, HeadRefOid: f.source, BaseRefName: "main"}}, nil
	}
	t.Cleanup(func() { ObserveDeliveryPRs = old })
	plan, err := PlanRepositoryCleanup(f.w, "core", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].LandingState != "landed" || plan.Items[0].LandingReceipt != receipts[child.ID] || !plan.Items[0].Dirty || plan.Items[0].Eligible {
		t.Fatalf("cleanup lost receipt truth or independent dirty guard: %#v", plan.Items)
	}
}

func TestNativeBatchLandingInvalidObservationBlocksLooseMetadata(t *testing.T) {
	f := newTaskLandingFixture(t)
	f.task.Doc.Front.Set("native_landing.current", "malformed")
	f.task.Doc.Front.Set("delivery_merge_sha", "loose-merge")
	f.task.Doc.Front.Set("delivery_tree_sha", "loose-tree")
	f.task.Doc.Front.Set("delivery_observed_at", time.Now().UTC().Format(time.RFC3339))
	if result, present := ReadTaskLanding(context.Background(), f.w, f.task, "main"); !present || result.State != LandingUnknown {
		t.Fatalf("invalid native marker was not explicit unknown: %#v present=%t", result, present)
	}
	if state, _ := CheckLanded(f.w, f.task, "main"); state != LandingUnknown {
		t.Fatalf("shared landing check fell back from invalid native record: %v", state)
	}
	if state := deliveryState(f.w, f.task); state.Landed || state.LandingState != "unknown" {
		t.Fatalf("loose delivery metadata overrode invalid selected evidence: %#v", state)
	}
	CheckAllAcceptance(f.task)
	if err := CloseTaskAfterLandingDecision(f.w, f.task, "a-root"); err == nil {
		t.Fatal("prior landing decision bypassed invalid selected native evidence")
	}
	if err := SaveTask(f.task); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNativeAcceptanceClose(f.w, f.task, "repository", f.base, "tree", "main"); err == nil || !strings.Contains(err.Error(), "selected native landing") {
		t.Fatalf("native acceptance ignored the selected evidence before its other gates: %v", err)
	}
}

func TestNativeBatchLandingUnknownPreservesMergedManagedWorktree(t *testing.T) {
	for _, status := range []model.Status{model.StatusOpen, model.StatusActive, model.StatusDone} {
		t.Run(string(status), func(t *testing.T) {
			f := newTaskLandingFixture(t)
			f.sourceCommit()
			f.mergeSource()
			CheckAllAcceptance(f.task)
			if err := SaveTask(f.task); err != nil {
				t.Fatal(err)
			}
			if status != model.StatusOpen {
				if err := MoveTask(f.w, f.task, status); err != nil {
					t.Fatal(err)
				}
			}
			branch := TaskBranch(f.task)
			path := f.w.WorktreePath(f.task.Project, f.task.Seq, f.task.Slug)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			taskLandingGit(t, f.root, "worktree", "add", "-b", branch, path, f.source)
			before, err := ReclaimableWorktrees(f.w, "main")
			if err != nil || len(before) != 1 || !before[0].Merged {
				t.Fatalf("legacy fixture was not otherwise reclaimable: %#v %v", before, err)
			}
			observation := f.withID(f.observation(f.base, f.source, f.source, f.merge, f.target))
			if err := AppendTaskLanding(f.task, observation); err != nil {
				t.Fatal(err)
			}
			if err := SaveTask(f.task); err != nil {
				t.Fatal(err)
			}
			if status != model.StatusDone {
				pending, err := ReclaimableWorktrees(f.w, "main")
				if err != nil || len(pending) != 0 {
					t.Fatalf("landing prematurely retired nonterminal task: %#v %v", pending, err)
				}
			}
			f.task.Doc.Front.Set("native_landing.current", "malformed")
			if err := SaveTask(f.task); err != nil {
				t.Fatal(err)
			}
			after, err := ReclaimableWorktrees(f.w, "main")
			if err != nil || len(after) != 0 {
				t.Fatalf("merged legacy branch bypassed invalid selected evidence: %#v %v", after, err)
			}
			old := ObserveDeliveryPRs
			ObserveDeliveryPRs = func(string) ([]DeliveryPR, error) {
				return []DeliveryPR{{Number: 1209, DeliveryConfidence: "MERGED", HeadRefName: branch, HeadRefOid: f.source}}, nil
			}
			t.Cleanup(func() { ObserveDeliveryPRs = old })
			plan, err := PlanRepositoryCleanup(f.w, "core", time.Now())
			if err != nil || len(plan.Items) != 1 || !plan.Items[0].Unknown || plan.Items[0].Eligible || plan.Items[0].LandingState != "unknown" {
				t.Fatalf("cleanup did not preserve unknown native ownership: %#v %v", plan.Items, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("read-only cleanup damaged checkout: %v", err)
			}
		})
	}
}
