package planning

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func spawnPlanningAgent(t *testing.T, w *workspace.Workspace, role string, grant model.Grant) (string, string) {
	t.Helper()
	id, token, err := agentid.Spawn(w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW}, role, grant)
	if err != nil {
		t.Fatal(err)
	}
	return id, token
}

func runAsPlanningAgent(t *testing.T, token string) {
	t.Helper()
	t.Setenv(agentid.EnvVar, token)
}

func closedTask(t *testing.T, w *workspace.Workspace, title string) *store.Task {
	t.Helper()
	tk, err := store.CreateTask(w, "a-root", "p", title, store.TaskOpts{Accept: []string{"one", "two"}})
	if err != nil {
		t.Fatal(err)
	}
	store.CheckAllAcceptance(tk)
	if err := store.CloseTask(w, tk, "a-root"); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindTask(w, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// Closing was a one-way door: a task force-accepted by mistake could only be
// corrected by editing the markdown store by hand — which is exactly what
// happened to tasks 336 and 339 when `accept --force` ran over a batch nobody
// read. The tool's product is a record and it had no command to fix the record
// (dacli 340).
func TestTaskReopenClearsTheBoxesAndRecordsWhy(t *testing.T) {
	w, ctx := taskAddEnv(t)
	tk := closedTask(t, w, "A task closed by mistake")
	if tk.Status != model.StatusDone {
		t.Fatalf("fixture is %s, not done — this test would measure nothing", tk.Status)
	}

	if err := cmdTaskReopen(ctx, []string{tk.ID, "--reason", "the work was never done"}); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	got, err := store.FindTask(w, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusOpen {
		t.Errorf("status = %s, want open", got.Status)
	}
	if got.Generation() != 1 {
		t.Errorf("generation = %d, want 1 after public reopen command", got.Generation())
	}
	// The boxes are a claim that the work was verified; a reopen says that
	// claim was wrong, so leaving them checked keeps the false record.
	sec, _ := got.Doc.Section("Acceptance")
	if strings.Contains(strings.ToLower(sec.Content), "- [x]") {
		t.Errorf("a checked acceptance box survived the reopen:\n%s", sec.Content)
	}
	// And the reason is in the log, or the next reader has a mystery.
	logSec, _ := got.Doc.Section("Log")
	if !strings.Contains(logSec.Content, "the work was never done") {
		t.Errorf("the reopen reason is not in the log:\n%s", logSec.Content)
	}
	if !strings.Contains(logSec.Content, "reopened by") {
		t.Errorf("the log does not record the reopen:\n%s", logSec.Content)
	}
}

// A reopen with no reason is a mystery later, and an already-open task has
// nothing to reopen — both refuse rather than doing something plausible.
func TestTaskReopenRefusesWithoutAReasonOrOnAnOpenTask(t *testing.T) {
	w, ctx := taskAddEnv(t)
	tk := closedTask(t, w, "Another closed task")

	err := cmdTaskReopen(ctx, []string{tk.ID})
	if err == nil || clikit.ExitCode(err) != 2 {
		t.Errorf("a reopen with no --reason must be a usage error, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "reason") {
		t.Errorf("the refusal must name what is missing: %v", err)
	}

	if err := cmdTaskReopen(ctx, []string{tk.ID, "--reason", "ok"}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// Second time: already open.
	again := cmdTaskReopen(ctx, []string{tk.ID, "--reason", "ok"})
	if again == nil {
		t.Error("reopening an already-open task must refuse, not silently succeed")
	}
}

// Removal is for a task whose EXISTENCE was the mistake — a probe, a
// duplicate. It must not become a way to erase work that happened, and must
// not leave a dangling reference behind.
func TestTaskRmRefusesDoneWorkAndReferencedTasks(t *testing.T) {
	w, ctx := taskAddEnv(t)

	probe, err := store.CreateTask(w, "a-root", "p", "A probe that should not exist", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdTaskRm(ctx, []string{probe.ID}); err != nil {
		t.Fatalf("removing an open, unreferenced task should work: %v", err)
	}
	if _, err := store.FindTask(w, probe.ID); err == nil {
		t.Error("the task survived removal")
	}

	// A done task carries a record of work that happened.
	done := closedTask(t, w, "Real work that landed")
	err = cmdTaskRm(ctx, []string{done.ID})
	if err == nil || clikit.ExitCode(err) != 3 {
		t.Errorf("removing a DONE task must be refused (exit 3), got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "reopen") {
		t.Errorf("the refusal must point at the right tool: %v", err)
	}

	// A dangling dependency fails far from the deletion that caused it.
	base, err := store.CreateTask(w, "a-root", "p", "A base task", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(w, "a-root", "p", "A dependent task", store.TaskOpts{
		Accept: []string{"x"}, DependsOn: []string{base.ID},
	}); err != nil {
		t.Fatal(err)
	}
	err = cmdTaskRm(ctx, []string{base.ID, "--force"})
	if err == nil {
		t.Error("removing a task something depends on must refuse, even with --force")
	} else if !strings.Contains(err.Error(), "referenced") {
		t.Errorf("the refusal must say what still points at it: %v", err)
	}
}

// A LIVE agent holding a task outranks every other removal check. Removing it
// leaves that agent working a ref that resolves to nothing — or worse, to a
// DIFFERENT task, because a freed seq is handed out again.
//
// Reported from inside the failure: an estimator mid-investigation watched
// `dacli task show 344` start returning someone else's finished task, with no
// signal that its own had been deleted (issue #433). The removal checks
// searched the RECORD — events and notes — and never .dacli/runs, so a task
// referenced by a running process read as unreferenced.
func TestTaskRmRefusesWhileALiveAgentHoldsTheTask(t *testing.T) {
	w, ctx := taskAddEnv(t)
	task, err := store.CreateTask(w, "a-root", "p", "A task an agent is working", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}

	// A run record for a process that IS alive: this test's own pid, which is
	// the only pid it can be sure about.
	runID := "01LIVERUN0000000000000000"
	dir := filepath.Join(w.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	start, _ := procmon.ProcStart(self)
	rec := procmon.Record{
		RunID: runID, Child: "a-estimator-live", Task: task.ID,
		PID: self, PGID: self, PIDStart: start,
	}
	if err := procmon.WriteRecord(filepath.Join(dir, "proc.txt"), rec); err != nil {
		t.Fatal(err)
	}

	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if err == nil {
		t.Fatal("a task held by a LIVE agent was removed — that agent's ref now resolves to nothing")
	}
	// --force must not get past this one: the alternative is a run that cannot
	// be made correct.
	if !strings.Contains(err.Error(), "a-estimator-live") {
		t.Errorf("the refusal must name the agent holding it: %v", err)
	}
	if _, ferr := store.FindTask(w, task.ID); ferr != nil {
		t.Errorf("the task was deleted despite the refusal: %v", ferr)
	}

	// With the run gone, removal works again — the guard is about liveness,
	// not about ever having been claimed.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := cmdTaskRm(ctx, []string{task.ID}); err != nil {
		t.Errorf("removal should succeed once no live agent holds it: %v", err)
	}
}

// Root needs a recovery path for duplicate tasks left behind by finished child
// agents. The ordinary ownership gate deliberately has no root exception, so
// task rm must make this exception locally without widening every mutation.
func TestTaskRmLetsRootRemoveRetiredChildOwnedTask(t *testing.T) {
	w, ctx := taskAddEnv(t)
	child, _ := spawnPlanningAgent(t, w, "retired-worker", model.GrantRW)
	task, err := store.CreateTask(w, child, "p", "A duplicate filed by a retired child", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	store.AppendLog(task, "claimed by "+child)
	if err := store.SaveTask(task); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireAgent(w, child); err != nil {
		t.Fatal(err)
	}

	dependent, err := store.CreateTask(w, agentid.RootID, "p", "A task referring to the duplicate", store.TaskOpts{Accept: []string{"x"}, DependsOn: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdTaskRm(ctx, []string{task.ID, "--force"}); err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("forced orphan removal while referenced = %v, want canonical reference refusal", err)
	}
	if err := cmdTaskRm(ctx, []string{dependent.ID}); err != nil {
		t.Fatalf("remove dependent fixture: %v", err)
	}
	if err := store.MoveTask(w, task, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	if err := cmdTaskRm(ctx, []string{task.ID}); err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("done/history-bearing orphan removal without force = %v, want explicit-force refusal", err)
	}
	if err := cmdTaskRm(ctx, []string{task.ID, "--force"}); err != nil {
		t.Fatalf("root could not remove retired child-owned task: %v", err)
	}
	if _, err := store.FindTask(w, task.ID); err == nil {
		t.Fatal("retired child-owned task survived root removal")
	}
}

func TestTaskRmFailsClosedOnUnreadableRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*testing.T, *workspace.Workspace)
	}{
		{
			name: "runs directory unreadable",
			corrupt: func(t *testing.T, w *workspace.Workspace) {
				if err := os.RemoveAll(w.RunsDir()); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(w.RunsDir(), []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed proc record",
			corrupt: func(t *testing.T, w *workspace.Workspace) {
				runDir := w.RunDir("01MALFORMEDREMOVAL")
				if err := os.MkdirAll(runDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runDir, "proc.txt"), []byte("not a process record\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, ctx := taskAddEnv(t)
			owner, _ := spawnPlanningAgent(t, w, "retired-owner", model.GrantRW)
			task, err := store.CreateTask(w, owner, "p", "Preserve when recovery evidence is unknown", store.TaskOpts{Accept: []string{"x"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RetireAgent(w, owner); err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, w)

			err = cmdTaskRm(ctx, []string{task.ID, "--force"})
			if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "evidence is unreadable") {
				t.Fatalf("task rm = %v (exit %d), want unreadable-evidence refusal", err, clikit.ExitCode(err))
			}
			if _, err := os.Stat(task.Path); err != nil {
				t.Fatalf("refused removal lost original task: %v", err)
			}
			if _, err := os.Stat(filepath.Join(w.TombstonesDir("p"), "001-preserve-when-recovery-evidence-is-unknown.md")); !os.IsNotExist(err) {
				t.Fatalf("refused removal wrote tombstone: %v", err)
			}
		})
	}
}

func TestTaskRmRootOrphanOverridePreservesOwnershipAndLiveSafety(t *testing.T) {
	w, ctx := taskAddEnv(t)
	owner, _ := spawnPlanningAgent(t, w, "owner", model.GrantRW)
	task, err := store.CreateTask(w, owner, "p", "A child-owned task", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}

	// The owner's live process blocks root even when that run record happens to
	// point at another task: this is owner lifecycle authorization, not merely
	// RemoveTask's independent live-claim reference check.
	runID := "01LIVEOWNER00000000000000"
	dir := filepath.Join(w.RunsDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	start, _ := procmon.ProcStart(self)
	if err := procmon.WriteRecord(filepath.Join(dir, "proc.txt"), procmon.Record{RunID: runID, Child: owner, Task: "another-task", PID: self, PGID: self, PIDStart: start}); err != nil {
		t.Fatal(err)
	}
	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), owner) || !strings.Contains(err.Error(), "live") {
		t.Fatalf("root removal while owner live = %v, want named live-owner refusal", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	_, siblingToken := spawnPlanningAgent(t, w, "sibling", model.GrantRW)
	runAsPlanningAgent(t, siblingToken)
	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if err == nil || clikit.ExitCode(err) != 3 {
		t.Fatalf("sibling removal = %v, want ownership refusal", err)
	}
	if _, err := store.FindTask(w, task.ID); err != nil {
		t.Fatalf("task disappeared after sibling refusal: %v", err)
	}
}

func TestTaskRmReadOnlyRootCannotUseOrphanOverride(t *testing.T) {
	w, ctx := taskAddEnv(t)
	owner, _ := spawnPlanningAgent(t, w, "retired-owner", model.GrantRW)
	task, err := store.CreateTask(w, owner, "p", "Another orphan", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetireAgent(w, owner); err != nil {
		t.Fatal(err)
	}

	rootFileID, rootToken := spawnPlanningAgent(t, w, "root-shaped-read-only", model.GrantRO)
	doc, err := mdstore.ReadFile(w.AgentPath(rootFileID))
	if err != nil {
		t.Fatal(err)
	}
	doc.Front.Set("id", agentid.RootID)
	if err := mdstore.WriteFile(w.AgentPath(rootFileID), doc); err != nil {
		t.Fatal(err)
	}
	runAsPlanningAgent(t, rootToken)
	ctx.Stdout = &bytes.Buffer{}
	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only root removal = %v, want grant refusal", err)
	}
	if _, err := store.FindTask(w, task.ID); err != nil {
		t.Fatalf("task disappeared after read-only refusal: %v", err)
	}
}

func TestTaskRmRootRefusesMalformedPIDRecoveryEvidence(t *testing.T) {
	w, ctx := taskAddEnv(t)
	owner, _ := spawnPlanningAgent(t, w, "retained-owner", model.GrantRW)
	task, err := store.CreateTask(w, owner, "p", "Preserve malformed PID", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	dir := w.RunDir("retained")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "child: " + owner + "\ntask: " + task.ID + "\npid: not-a-pid\n"
	if err := os.WriteFile(filepath.Join(dir, "proc.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "evidence is unreadable") {
		t.Fatalf("malformed PID recovery evidence must refuse with exit3: %v (exit %d)", err, clikit.ExitCode(err))
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused removal changed original task: %v", err)
	}
	entries, err := os.ReadDir(w.TombstonesDir("p"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused removal wrote tombstone: %v", entries)
	}
}

func TestTaskRmRootRefusesActiveZeroRecoveryEvidence(t *testing.T) {
	w, ctx := taskAddEnv(t)
	owner, _ := spawnPlanningAgent(t, w, "retained-owner", model.GrantRW)
	task, err := store.CreateTask(w, owner, "p", "Preserve unknown zero PID", store.TaskOpts{Accept: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(task.Path)
	if err != nil {
		t.Fatal(err)
	}
	dir := w.RunDir("retained")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := "child: " + owner + "\ntask: " + task.ID + "\npid: 0\n"
	if err := os.WriteFile(filepath.Join(dir, "proc.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	err = cmdTaskRm(ctx, []string{task.ID, "--force"})
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "evidence is unreadable") {
		t.Fatalf("active zero recovery evidence must refuse with exit3: %v (exit %d)", err, clikit.ExitCode(err))
	}
	after, err := os.ReadFile(task.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused removal changed original task: %v", err)
	}
	entries, err := os.ReadDir(w.TombstonesDir("p"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("refused removal wrote tombstone: %v", entries)
	}
}
