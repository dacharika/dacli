package orchestration

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestEstimatorForwardsExplicitContextException(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		w := loopEnv(t)
		if err := store.CreateRole(w, "a-root", team.Role{Name: estimatorRole, Grant: "ro"}); err != nil {
			t.Fatal(err)
		}
		task, err := store.CreateTask(w, "a-root", "p", "Unsized", store.TaskOpts{Accept: []string{"done"}})
		if err != nil {
			t.Fatal(err)
		}
		r := &fakeRunner{}
		d := newDriver(w, r, &Governor{})
		d.cfg.allowUserConfig = allowed
		d.sizeUnestimated([]*store.Task{task})
		if len(r.calls) != 1 || contains(r.calls[0], "--allow-user-config") != allowed {
			t.Fatalf("estimator exception=%t: calls=%v", allowed, r.calls)
		}
	}
}

func TestEstimatorPolicyRefusalStopsLoopBeforeBuild(t *testing.T) {
	w := loopEnv(t)
	if err := store.CreateRole(w, "a-root", team.Role{Name: estimatorRole, Grant: "ro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(w, "a-root", "p", "Unsized", store.TaskOpts{Accept: []string{"done"}}); err != nil {
		t.Fatal(err)
	}
	r := &launchRefusalRunner{}
	d := newDriver(w, r, &Governor{MaxCycles: 3, NoProgressHalt: 3})
	d.cfg.width = 1
	err := d.loop()
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("estimator lost policy refusal: %v", err)
	}
	spawns := 0
	for _, call := range r.calls {
		if call[0] == "spawn" {
			spawns++
			if argAfter(call, "--role") != estimatorRole {
				t.Fatalf("loop launched build after estimator refusal: %v", call)
			}
		}
	}
	if spawns != 1 {
		t.Fatalf("estimator refusal launched %d agents", spawns)
	}
	state, err := readLoopState(w, "p")
	if err != nil || len(state.Outcome.Failures) == 0 || state.Outcome.Failures[0].Retryable || !strings.Contains(state.Outcome.Failures[0].Detail, "config.toml") {
		t.Fatalf("estimator refusal not durably recorded: %+v, %v", state.Outcome, err)
	}
}

type correctionRefusalRunner struct {
	reviewTransactionRunner
	beforeRefusal func()
}

func (r *correctionRefusalRunner) run(label string, args ...string) (string, error) {
	if label != "review-correction" {
		return r.reviewTransactionRunner.run(label, args...)
	}
	_, _ = r.fakeRunner.run(label, args...)
	if r.beforeRefusal != nil {
		r.beforeRefusal()
	}
	cmd := exec.Command("sh", "-c", "echo 'warning: advisory token ceiling'; echo 'dacli: undeclared correction config.toml' >&2; exit 3")
	out, err := commandresult.Run(cmd, commandresult.RunOptions{Operation: "dacli spawn"})
	return string(out), err
}

// Replace an existing transaction file only when the launch is about to
// refuse, after the driver has read it. Atomic rename cannot replace a directory.
func blockFailureRecord(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectionRefusalSurvivesHaltPersistenceFailure(t *testing.T) {
	w, task := reviewedTaskFixture(t)
	r := &correctionRefusalRunner{reviewTransactionRunner: reviewTransactionRunner{w: w, task: task}}
	r.beforeRefusal = func() { blockFailureRecord(t, store.ReviewTransactionPath(w, "p", task.ID)) }
	d := newDriver(w, r, &Governor{})
	d.phases = cyclePhaseJournal{Project: "p"}
	if d.reviewDeliveryTask(task) {
		t.Fatal("refused correction approved delivery")
	}
	if clikit.ExitCode(d.phaseErr) != 3 || !strings.Contains(d.phaseErr.Error(), "config.toml") || !strings.Contains(d.phaseErr.Error(), "persist correction failure") {
		t.Fatalf("storage error replaced correction policy refusal: code=%d err=%v", clikit.ExitCode(d.phaseErr), d.phaseErr)
	}
}

type estimatorStorageFailureRunner struct {
	launchRefusalRunner
	beforeRefusal func()
}

func (r *estimatorStorageFailureRunner) run(label string, args ...string) (string, error) {
	if label == "estimate" {
		r.beforeRefusal()
	}
	return r.launchRefusalRunner.run(label, args...)
}

func TestEstimatorRefusalSurvivesStatePersistenceFailure(t *testing.T) {
	w := loopEnv(t)
	if err := store.CreateRole(w, "a-root", team.Role{Name: estimatorRole, Grant: "ro"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(w, "a-root", "p", "Unsized", store.TaskOpts{Accept: []string{"done"}}); err != nil {
		t.Fatal(err)
	}
	r := &estimatorStorageFailureRunner{beforeRefusal: func() { blockFailureRecord(t, loopStateFile(w, "p")) }}
	d := newDriver(w, r, &Governor{MaxCycles: 3, NoProgressHalt: 3})
	d.cfg.width = 1
	err := d.loop()
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "config.toml") || !strings.Contains(err.Error(), "persist estimator refusal") {
		t.Fatalf("storage error replaced estimator policy refusal: code=%d err=%v", clikit.ExitCode(err), err)
	}
}

func TestCorrectionPolicyRefusalIsTerminalAcrossPartialDeliveryAndRestart(t *testing.T) {
	w, task := reviewedTaskFixture(t)
	r := &correctionRefusalRunner{reviewTransactionRunner: reviewTransactionRunner{w: w, task: task}}
	d := newDriver(w, r, &Governor{})
	d.phases = cyclePhaseJournal{Project: "p"}
	if d.reviewDeliveryTask(task) {
		t.Fatal("refused correction approved delivery")
	}
	// Another task's delivery must not erase the policy stop. The loop consumes
	// phaseErr independently of the zero-output outcome classifier.
	outcome := d.finishCycleOutcome(2, cycleRollup{Landed: 1, Blocked: 1})
	if clikit.ExitCode(d.phaseErr) != 3 || !strings.Contains(d.phaseErr.Error(), "config.toml") {
		t.Fatalf("partial delivery swallowed correction policy stop: err=%v outcome=%+v", d.phaseErr, outcome)
	}
	if len(outcome.Failures) != 1 || outcome.Failures[0].Retryable || !strings.Contains(outcome.Failures[0].Detail, "warning:") || !strings.Contains(outcome.Failures[0].Detail, "config.toml") {
		t.Fatalf("correction diagnostic lost: %+v", outcome)
	}
	tx, err := store.ReadReviewTransaction(w, "p", task.ID)
	if err != nil || tx.State != store.ReviewHalted || tx.CorrectionTurns != 1 || len(tx.FindingIDs) == 0 || tx.PriorTree == "" {
		t.Fatalf("correction authority not preserved as halted: %+v, %v", tx, err)
	}
	before := len(r.calls)
	restarted := newDriver(w, r, &Governor{})
	if restarted.reviewDeliveryTask(task) || len(r.calls) != before {
		t.Fatalf("restart retried refused correction: calls=%v", r.calls[before:])
	}
}
