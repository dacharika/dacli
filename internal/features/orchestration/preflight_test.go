package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

type recordingPreflightRunner struct {
	fakeRunner
	failRole       string
	launchContract string
}

func preflightPhase(result cyclePreflightResult, name string) (cyclePreflightPhase, bool) {
	for _, phase := range result.Phases {
		if phase.Phase == name {
			return phase, true
		}
	}
	return cyclePreflightPhase{}, false
}

func readCyclePreflight(t *testing.T, wRoot, project string) cyclePreflightResult {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(wRoot, ".dacli", "loop", project+"-preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result cyclePreflightResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (r *recordingPreflightRunner) runPreflight(label string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if r.failRole != "" && argAfter(args, "--role") == r.failRole {
		return "runtime contract incompatible", clikit.Refusedf("runtime contract incompatible")
	}
	if r.launchContract != "" && contains(args, "--structured-review-result") {
		return "compatible\n" + store.RuntimeLaunchContractMarker + r.launchContract + "\n", nil
	}
	return "compatible", nil
}

func TestReviewerPreflightPersistsExactLaunchFingerprintForReviewSpawn(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "builder", Kind: "implementer", Grant: "rw"},
		{Name: "reviewer", Kind: "reviewer", Grant: "ro"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Build reviewed output", store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
	if err != nil {
		t.Fatal(err)
	}
	contract := store.RuntimeLaunchContract{
		Schema: store.RuntimeLaunchContractSchema, Fingerprint: "sha256:review-contract",
		Harness: "codex", Adapter: store.BehavioralPreflightCodexExecJSONV2,
		SandboxFlags: []string{"--sandbox", "read-only"}, Grant: "ro", Runtime: "codex",
		Model: "review-model", ResultChannel: store.IndependentReviewChannel,
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPreflightRunner{launchContract: string(raw)}
	d := newDriver(w, runner, &Governor{})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.reviewRoleExplicit = "builder", true, "reviewer", true
	if err := d.preflightCycle([]*store.Task{task}); err != nil {
		t.Fatal(err)
	}
	if d.reviewLaunchFingerprint != contract.Fingerprint {
		t.Fatalf("review fingerprint=%q, want %q", d.reviewLaunchFingerprint, contract.Fingerprint)
	}
	phase, ok := preflightPhase(readCyclePreflight(t, w.Root, "p"), "reviewer-runtime")
	if !ok || phase.LaunchContract == nil || !phase.LaunchContract.Equal(contract) {
		t.Fatalf("preflight artifact lost exact launch contract: %+v", phase)
	}
}

func TestExplicitUnderCapacityRoleRefusesBeforeAnyWorkerStarts(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "junior", Kind: "implementer", Grant: "rw", MaxPoints: 3, Summary: "small fixes"},
		{Name: "reviewer", Kind: "reviewer", Grant: "ro", Summary: "reviews"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Implement large change", store.TaskOpts{Accept: []string{"observable"}, Estimate: "8,10,12"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPreflightRunner{}
	d := newDriver(w, runner, &Governor{MaxCycles: 1, NoProgressHalt: 3})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole = "junior", true, "reviewer"
	err = d.loop()
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "above role junior's cap") {
		t.Fatalf("loop error = %v (exit %d), want capacity refusal", err, clikit.ExitCode(err))
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "spawn" {
			t.Fatalf("capacity refusal happened after worker spawn: %v", call)
		}
	}
	raw, err := os.ReadFile(cyclePreflightFile(w, "p"))
	if err != nil {
		t.Fatalf("missing durable preflight result: %v", err)
	}
	var result cyclePreflightResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("preflight result is not JSON: %v", err)
	}
	var implementation cyclePreflightPhase
	for _, phase := range result.Phases {
		if phase.Phase == "implementation" {
			implementation = phase
			break
		}
	}
	if result.SchemaVersion != 2 || result.Verdict != "refuse" || implementation.Task != task.ID || implementation.Capacity == nil || implementation.Capacity.Delta != -7 {
		t.Fatalf("preflight result lost versioned capacity evidence: %+v", result)
	}
}

func TestReviewerPreflightRefusesBeforeImplementationSpawn(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "builder", Kind: "implementer", Grant: "rw", Summary: "builds"},
		{Name: "broken-reviewer", Kind: "reviewer", Grant: "ro", Summary: "reviews"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateTask(w, "a-root", "p", "Implement reviewed change", store.TaskOpts{Accept: []string{"observable"}, Estimate: "2,2,2"}); err != nil {
		t.Fatal(err)
	}
	runner := &recordingPreflightRunner{failRole: "broken-reviewer"}
	d := newDriver(w, runner, &Governor{MaxCycles: 3, NoProgressHalt: 3})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole = "builder", true, "broken-reviewer"
	err := d.loop()
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("loop error = %v (exit %d), want permanent reviewer refusal", err, clikit.ExitCode(err))
	}
	if len(runner.calls) == 0 || runner.calls[0][0] != "preflight" || argAfter(runner.calls[0], "--role") != "broken-reviewer" {
		t.Fatalf("reviewer must be the first probe reached before refusal: %v", runner.calls)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "spawn" {
			t.Fatalf("reviewer refusal happened after worker spawn: %v", runner.calls)
		}
	}
}

func TestReviewerPreflightRequiresROAndStructuredResultBeforeImplementation(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "builder", Kind: "implementer", Grant: "rw"},
		{Name: "unsafe-reviewer", Kind: "reviewer", Grant: "rw"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Build reviewed output", store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingPreflightRunner{}
	d := newDriver(w, runner, &Governor{})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.reviewRoleExplicit, d.cfg.landing.Base = "builder", true, "unsafe-reviewer", true, "main"
	if err := d.preflightCycle([]*store.Task{task}); clikit.ExitCode(err) != 3 {
		t.Fatalf("rw reviewer preflight exit=%d err=%v, want refusal", clikit.ExitCode(err), err)
	}
	phase, ok := preflightPhase(readCyclePreflight(t, w.Root, "p"), "reviewer-runtime")
	if !ok || phase.OutputContract != store.ReviewResultSchema || phase.Grant != "ro" || phase.Classification != preflightPermanent {
		t.Fatalf("reviewer preflight lost contract/authority: %+v", phase)
	}
	for _, call := range runner.calls {
		if len(call) > 0 && call[0] == "spawn" {
			t.Fatalf("unsafe review role reached implementation spawn: %v", call)
		}
	}
}

func TestCyclePreflightPreservesMultiSliceClaimsDespiteIncidentalProse(t *testing.T) {
	w := loopEnv(t)
	for _, path := range []string{
		"internal/features/ship/ship.go",
		"internal/features/vcs/vcs.go",
		"internal/features/acceptance/acceptance.go",
		"internal/features/execution/execution.go",
	} {
		full := filepath.Join(w.Root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []team.Role{{Name: "lifecycle", Kind: "implementer", Grant: "rw", Summary: "lifecycle work"}, {Name: "go-auditor", Kind: "reviewer", Grant: "ro"}} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Fix product lifecycle", store.TaskOpts{
		Estimate: "3,3,3",
		Context:  "The report incidentally mentions internal/features/execution, but the lifecycle crosses slices.",
		Accept: []string{
			"internal/features/ship/ship.go handles landing",
			"internal/features/vcs/vcs.go preserves the branch",
			"internal/features/acceptance/acceptance.go verifies closure",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := newDriver(w, &fakeRunner{}, &Governor{})
	d.cfg.implRole, d.cfg.implRoleExplicit = "lifecycle", true
	d.cfg.landing.Base = "main"
	if err := d.preflightCycle([]*store.Task{task}); err != nil {
		t.Fatal(err)
	}
	claims := d.ctx.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{"internal/features/ship/ship.go", "internal/features/vcs/vcs.go", "internal/features/acceptance/acceptance.go"} {
		if !strings.Contains(claims, want) {
			t.Errorf("versioned preflight collapsed multi-slice claim; missing %s in %s", want, claims)
		}
	}
}

func TestCapacityOverrideIsOwnerReasonedExpiringAndTaskScoped(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "junior", Kind: "implementer", Grant: "rw", MaxPoints: 2},
		{Name: "reviewer", Kind: "reviewer", Grant: "ro"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Oversized but accepted risk", store.TaskOpts{Accept: []string{"done"}, Estimate: "5,5,5"})
	if err != nil {
		t.Fatal(err)
	}
	d := newDriver(w, &recordingPreflightRunner{}, &Governor{})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole = "junior", true, "reviewer"
	d.cfg.landing.Base = "main"
	d.cfg.capacityOverride = &capacityOverride{Actor: "a-root", Reason: "single-cycle incident recovery", ExpiresAt: d.now().Add(time.Hour)}
	if err := d.preflightCycle([]*store.Task{task}); err != nil {
		t.Fatalf("reasoned capacity override refused: %v", err)
	}
	result := readCyclePreflight(t, w.Root, "p")
	phase, ok := preflightPhase(result, "implementation")
	if !ok || phase.Override == nil {
		t.Fatalf("durable override missing: %+v", result)
	}
	got := phase.Override
	if got.Task != task.ID || got.Role != "junior" || got.Delta != -3 || got.Actor != "a-root" || got.Reason != "single-cycle incident recovery" || !strings.Contains(got.Scope, "cycle=1") || !got.ExpiresAt.Equal(d.now().Add(time.Hour)) {
		t.Fatalf("override lost its scope/provenance: %+v", got)
	}
}

func TestExpiredCapacityOverrideAndPlannedWIPFailClosed(t *testing.T) {
	t.Run("expired override", func(t *testing.T) {
		w := loopEnv(t)
		for _, role := range []team.Role{{Name: "junior", Kind: "implementer", Grant: "rw", MaxPoints: 1}, {Name: "reviewer", Kind: "reviewer", Grant: "ro"}} {
			if err := store.CreateRole(w, "a-root", role); err != nil {
				t.Fatal(err)
			}
		}
		task, err := store.CreateTask(w, "a-root", "p", "Too large", store.TaskOpts{Accept: []string{"done"}, Estimate: "3,3,3"})
		if err != nil {
			t.Fatal(err)
		}
		d := newDriver(w, &recordingPreflightRunner{}, &Governor{})
		d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.landing.Base = "junior", true, "reviewer", "main"
		d.cfg.capacityOverride = &capacityOverride{Actor: "a-root", Reason: "stale", ExpiresAt: d.now().Add(-time.Second)}
		if err := d.preflightCycle([]*store.Task{task}); clikit.ExitCode(err) != 3 {
			t.Fatalf("expired override = exit %d, %v", clikit.ExitCode(err), err)
		}
		phase, _ := preflightPhase(readCyclePreflight(t, w.Root, "p"), "implementation")
		if phase.Override != nil || phase.Classification != preflightPermanent {
			t.Fatalf("expired override authorized work: %+v", phase)
		}
	})

	t.Run("planned wave consumes WIP", func(t *testing.T) {
		w := loopEnv(t)
		for _, role := range []team.Role{{Name: "builder", Kind: "implementer", Grant: "rw", WIP: 1}, {Name: "reviewer", Kind: "reviewer", Grant: "ro"}} {
			if err := store.CreateRole(w, "a-root", role); err != nil {
				t.Fatal(err)
			}
		}
		var tasks []*store.Task
		for _, title := range []string{"One", "Two"} {
			task, err := store.CreateTask(w, "a-root", "p", title, store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
			if err != nil {
				t.Fatal(err)
			}
			tasks = append(tasks, task)
		}
		d := newDriver(w, &recordingPreflightRunner{}, &Governor{})
		d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.landing.Base = "builder", true, "reviewer", "main"
		if err := d.preflightCycle(tasks); clikit.ExitCode(err) != 3 {
			t.Fatalf("over-WIP wave = exit %d, %v", clikit.ExitCode(err), err)
		}
		result := readCyclePreflight(t, w.Root, "p")
		var refused bool
		for _, phase := range result.Phases {
			refused = refused || phase.Phase == "implementation-wip" && phase.Classification == preflightPermanent && strings.Contains(phase.Evidence, "planned-before-this-task=1")
		}
		if !refused {
			t.Fatalf("planned WIP was not inventoried: %+v", result.Phases)
		}
	})
}

type transientPreflightRunner struct{ fakeRunner }

func (r *transientPreflightRunner) runPreflight(label string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if label == "github-observability" {
		return "network timeout", fmt.Errorf("network timeout")
	}
	return "compatible", nil
}

func TestCyclePreflightClassifiesExternalFailureAsTransient(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{{Name: "builder", Kind: "implementer", Grant: "rw"}, {Name: "reviewer", Kind: "reviewer", Grant: "ro"}} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Build", store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
	if err != nil {
		t.Fatal(err)
	}
	d := newDriver(w, &transientPreflightRunner{}, &Governor{WindowTokens: 100, WindowDur: time.Hour})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.landing.Base = "builder", true, "reviewer", "main"
	err = d.preflightCycle([]*store.Task{task})
	if err == nil || clikit.ExitCode(err) != 1 {
		t.Fatalf("external failure = exit %d, %v; want retryable exit 1", clikit.ExitCode(err), err)
	}
	result := readCyclePreflight(t, w.Root, "p")
	if result.Classification != preflightTransient {
		t.Fatalf("result classification = %q", result.Classification)
	}
	github, ok := preflightPhase(result, "github-observability")
	if !ok || github.Classification != preflightTransient || github.Verdict != "retry" {
		t.Fatalf("GitHub phase not stably transient: %+v", github)
	}
	for _, want := range []string{"stop", "rolling-budget", "cycle-budget", "cycle-wip", "implementation-wip", "implementation-timeout", "reviewer-runtime", "reviewer-wip", "landing-base", "check-policy", "verification-profile"} {
		if _, ok := preflightPhase(result, want); !ok {
			t.Errorf("complete phase inventory missing %s", want)
		}
	}
}

func TestVerificationPreflightRecordsCwdAndRefusesMissingCommand(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{{Name: "builder", Kind: "implementer", Grant: "rw"}, {Name: "reviewer", Kind: "reviewer", Grant: "ro"}} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	p, err := defaultProfile("p", "loop")
	if err != nil {
		t.Fatal(err)
	}
	p.Verification.Commands = []string{"definitely-no-such-verifier --check"}
	if err := saveProfile(w, p); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "a-root", "p", "Build", store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
	if err != nil {
		t.Fatal(err)
	}
	d := newDriver(w, &recordingPreflightRunner{}, &Governor{})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.landing.Base = "builder", true, "reviewer", "main"
	err = d.preflightCycle([]*store.Task{task})
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("missing verifier = exit %d, %v", clikit.ExitCode(err), err)
	}
	verification, ok := preflightPhase(readCyclePreflight(t, w.Root, "p"), "verification-command")
	if !ok || verification.WorkingDirectory != w.Root || !strings.Contains(verification.Evidence, "unavailable") {
		t.Fatalf("verification cwd/availability evidence = %+v", verification)
	}
}

type sizingBeforePreflightRunner struct{ fakeRunner }

func (r *sizingBeforePreflightRunner) runPreflight(label string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{"probe:" + label}, args...))
	if label == "reviewer-runtime" {
		return "permanent reviewer mismatch", clikit.Refusedf("permanent reviewer mismatch")
	}
	return "compatible", nil
}

func TestAutomaticSizingRunsBeforeCompleteCyclePreflight(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{
		{Name: "builder", Kind: "implementer", Grant: "rw"},
		{Name: "reviewer", Kind: "reviewer", Grant: "ro"},
		{Name: estimatorRole, Kind: "planner", Grant: "rw"},
	} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateTask(w, "a-root", "p", "Unsized", store.TaskOpts{Accept: []string{"done"}}); err != nil {
		t.Fatal(err)
	}
	runner := &sizingBeforePreflightRunner{}
	d := newDriver(w, runner, &Governor{MaxCycles: 1, NoProgressHalt: 3})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole = "builder", true, "reviewer"
	err := d.loop()
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("reviewer refusal = exit %d, %v", clikit.ExitCode(err), err)
	}
	if len(runner.calls) < 2 || runner.calls[0][0] != "spawn" || argAfter(runner.calls[0], "--role") != estimatorRole || !strings.HasPrefix(runner.calls[1][0], "probe:") {
		t.Fatalf("sizing did not precede preflight: %v", runner.calls)
	}
}

func TestLoopContextExceptionReachesReadiness(t *testing.T) {
	w := loopEnv(t)
	for _, role := range []team.Role{{Name: "builder", Kind: "implementer", Grant: "rw"}, {Name: "reviewer", Kind: "reviewer", Grant: "ro"}} {
		if err := store.CreateRole(w, "a-root", role); err != nil {
			t.Fatal(err)
		}
	}
	task, err := store.CreateTask(w, "a-root", "p", "Build context", store.TaskOpts{Accept: []string{"done"}, Estimate: "1,1,1"})
	if err != nil {
		t.Fatal(err)
	}
	r := &transientPreflightRunner{}
	d := newDriver(w, r, &Governor{WindowTokens: 100, WindowDur: time.Hour})
	d.cfg.implRole, d.cfg.implRoleExplicit, d.cfg.reviewRole, d.cfg.landing.Base = "builder", true, "reviewer", "main"
	d.cfg.allowUserConfig = true
	_ = d.preflightCycle([]*store.Task{task})
	count := 0
	for _, call := range r.calls {
		if call[0] == "preflight" {
			count++
			if !contains(call, "--allow-user-config") {
				t.Fatalf("readiness lost exception: %v", call)
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected both readiness calls, got %d", count)
	}
}
