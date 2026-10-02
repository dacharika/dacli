package orchestration

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestCycleOutcomeDistinguishesHealthyIdleAndFullyLanded(t *testing.T) {
	d := &driver{}
	idle := d.finishCycleOutcome(0, cycleRollup{})
	if idle.Schema != cycleOutcomeSchema || idle.Classification != "healthy-idle" || idle.err() != nil {
		t.Fatalf("idle=%+v err=%v", idle, idle.err())
	}
	landed := d.finishCycleOutcome(2, cycleRollup{Landed: 2})
	if landed.Classification != "healthy" || landed.Landed != 2 || landed.err() != nil {
		t.Fatalf("landed=%+v err=%v", landed, landed.err())
	}
}

func TestCycleOutcomeKeepsPolicyAndOperationalFailuresTyped(t *testing.T) {
	policy := &driver{}
	policy.recordCycleFailure("review", clikit.Refusedf("approval withheld"), "approval withheld")
	policyOutcome := policy.finishCycleOutcome(1, cycleRollup{ProducedNothing: 1})
	if policyOutcome.Classification != "degraded-zero-output" || clikit.ExitCode(policyOutcome.err()) != 3 || policyOutcome.Failures[0].Retryable {
		t.Fatalf("policy outcome=%+v err=%v", policyOutcome, policyOutcome.err())
	}

	operational := &driver{}
	operational.recordCycleFailure("ship", errors.New("remote unavailable"), "remote unavailable")
	operationalOutcome := operational.finishCycleOutcome(1, cycleRollup{Stalled: 1})
	if clikit.ExitCode(operationalOutcome.err()) != 1 || !operationalOutcome.Failures[0].Retryable {
		t.Fatalf("operational outcome=%+v err=%v", operationalOutcome, operationalOutcome.err())
	}
}

type zeroOutputRunner struct {
	fakeRunner
	w *workspace.Workspace
}

func (r *zeroOutputRunner) run(label string, args ...string) (string, error) {
	_, _ = r.fakeRunner.run(label, args...)
	if len(args) > 0 && args[0] == "spawn" {
		task, err := store.FindTask(r.w, argAfter(args, "--task"))
		if err != nil {
			return "", err
		}
		runID := "01ZEROOUTPUT00000000000000"
		dir := r.w.RunDir(runID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := procmon.WriteRecord(filepath.Join(dir, "proc.txt"), procmon.Record{RunID: runID, Task: task.ID, Outcome: "completed", Started: time.Now()}); err != nil {
			return "", err
		}
	}
	return "", nil
}

func TestLoopReturnsOperationalFailureAfterPersistingZeroOutputSpawn(t *testing.T) {
	w := loopEnv(t)
	task, err := store.CreateTask(w, "a-root", "p", "Produces no commit", store.TaskOpts{Accept: []string{"commit exists"}})
	if err != nil {
		t.Fatal(err)
	}
	d := newDriver(w, &zeroOutputRunner{w: w}, &Governor{MaxCycles: 1, NoProgressHalt: 3})
	d.cfg.width = 1
	err = d.loop()
	if clikit.ExitCode(err) != 1 {
		t.Fatalf("zero-output loop err=%v code=%d", err, clikit.ExitCode(err))
	}
	st, readErr := readLoopState(w, "p")
	if readErr != nil || st.Outcome.Classification != "degraded-zero-output" || st.Outcome.ProducedNothing != 1 || len(st.Outcome.Failures) == 0 || st.Outcome.Failures[0].Phase != "commit" {
		t.Fatalf("persisted outcome=%+v readErr=%v", st.Outcome, readErr)
	}
	_ = task
}

func TestLoopStatusJSONExposesVersionedOutcomeAcrossRestart(t *testing.T) {
	w := loopEnv(t)
	d := newDriver(w, &fakeRunner{}, &Governor{})
	d.lastRollup = cycleRollup{Landed: 1}
	d.lastOutcome = d.finishCycleOutcome(1, d.lastRollup)
	if err := d.saveState("proceed", "cycle complete", 0); err != nil {
		t.Fatal(err)
	}
	restarted, err := readLoopState(w, "p")
	if err != nil || restarted.Outcome.Schema != cycleOutcomeSchema || restarted.Outcome.Landed != 1 {
		t.Fatalf("restart outcome=%+v err=%v", restarted.Outcome, err)
	}
	var out bytes.Buffer
	ctx := &clikit.Ctx{Cwd: w.Root, Stdout: &out, Stderr: &bytes.Buffer{}, JSON: true}
	if err := cmdLoopStatus(ctx, []string{"--project", "p"}); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Outcome cycleOutcome `json:"cycle_outcome"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil || payload.Outcome.Schema != cycleOutcomeSchema || payload.Outcome.Classification != "healthy" {
		t.Fatalf("status=%s decode=%v outcome=%+v", out.String(), err, payload.Outcome)
	}
}

func TestLaunchPolicyExitAfterWarningsIsNonretryable(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'warning: advisory token ceiling'; echo 'dacli: undeclared user-config source config.toml' >&2; exit 3")
	out, err := commandresult.Run(cmd, commandresult.RunOptions{Operation: "dacli spawn"})
	d := &driver{}
	d.recordCycleFailure("spawn", err, string(out))
	outcome := d.finishCycleOutcome(1, cycleRollup{ProducedNothing: 1})
	if clikit.ExitCode(outcome.err()) != 3 || outcome.Failures[0].Retryable || !strings.Contains(outcome.Failures[0].Detail, "config.toml") {
		t.Fatalf("lost refusal: %+v", outcome)
	}
	if got := reviewFailure(string(out), err, commandresult.Spawn{}); !strings.Contains(got, "config.toml") {
		t.Fatalf("lost diagnostic: %s", got)
	}
}

func TestLoopContextExceptionIsExplicitAndForwarded(t *testing.T) {
	d := &driver{}
	args := []string{"spawn", "--claim", "internal/features/execution"}
	if got := d.contextExceptionArgs(args); slices.Contains(got, "--allow-user-config") {
		t.Fatal("default grants exception")
	}
	d.cfg.allowUserConfig = true
	for _, operation := range []string{"spawn", "preflight", "supervise"} {
		got := d.contextExceptionArgs([]string{operation, "--grant", "ro"})
		if !slices.Contains(got, "--allow-user-config") || !slices.Contains(got, "ro") {
			t.Fatalf("lost policy or grant: %v", got)
		}
	}
}

func TestLoopContextExceptionReachesBuildAndReview(t *testing.T) {
	w := loopEnv(t)
	task, err := store.CreateTask(w, "a-root", "p", "Feature context", store.TaskOpts{Accept: []string{"a"}, Estimate: "1,2,3"})
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	d := newDriver(w, fr, &Governor{})
	d.cfg.allowUserConfig = true
	d.runCycle([]*store.Task{task})
	for label, call := range map[string][]string{"build": buildSpawnCall(fr), "review": reviewSpawnCall(fr)} {
		if !slices.Contains(call, "--allow-user-config") {
			t.Fatalf("%s lost exception: %v", label, call)
		}
	}
}

type launchRefusalRunner struct{ fakeRunner }

func (r *launchRefusalRunner) run(label string, args ...string) (string, error) {
	_, _ = r.fakeRunner.run(label, args...)
	if args[0] != "spawn" {
		return "", nil
	}
	cmd := exec.Command("sh", "-c", "echo 'warning: advisory token ceiling'; echo 'dacli: undeclared config.toml' >&2; exit 3")
	out, err := commandresult.Run(cmd, commandresult.RunOptions{Operation: "dacli spawn"})
	return string(out), err
}

func TestLoopLaunchRefusalStopsWithoutRetryAndRetainsTail(t *testing.T) {
	w := loopEnv(t)
	_, err := store.CreateTask(w, "a-root", "p", "Refused launch", store.TaskOpts{Accept: []string{"a"}, Estimate: "1,2,3"})
	if err != nil {
		t.Fatal(err)
	}
	r := &launchRefusalRunner{}
	d := newDriver(w, r, &Governor{MaxCycles: 3, NoProgressHalt: 3})
	d.cfg.width = 1
	err = d.loop()
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("loop lost policy diagnostic: %v", err)
	}
	count := 0
	for _, call := range r.calls {
		if call[0] == "spawn" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("refusal retried %d times", count)
	}
}
