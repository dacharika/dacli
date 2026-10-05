package application

import (
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/policy"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func nativePolicyFixture(t *testing.T, approval bool) (acceptanceFixture, sessions.NativePolicy, sessions.NativePolicyDecision) {
	t.Helper()
	f := acceptanceSetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	p := sessions.NativePolicy{SchemaVersion: 1, ProjectID: "p", TaskID: f.task.ID, Revision: "native-one", Allow: true, RequireApproval: approval, ExpiresAt: time.Now().Add(10 * time.Minute)}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.task.Path)
	d, e := f.access.EvaluatePolicy(sessions.NativePolicyEvaluationRequest{OperationKey: "decision-one", Input: f.input, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(f.task.Path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("evaluation changed canonical task")
	}
	status, e := f.a.ReadNativePolicyStatus(f.owner, d.ID)
	if e != nil || status.Consumption != nil || status.Outcome != nil || !status.Historical {
		t.Fatal("permission fabricated consumed action/outcome", status, e)
	}
	return f, p, d
}
func TestNativePolicyActualConsumerDecisionAndSeparateOutcome(t *testing.T) {
	f, _, d := nativePolicyFixture(t, false)
	if d.Assessment.Verdict != policy.Allow || d.Assessment.Assurance != "cooperative-session" {
		t.Fatal("authenticated typed assessment missing", d.Assessment)
	}
	second, e := f.access.EvaluatePolicy(sessions.NativePolicyEvaluationRequest{OperationKey: "second-same-action", Input: f.input, ExpiresAt: d.ExpiresAt})
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.task.Path)
	if out, e := f.access.Apply(f.input, ""); !errors.Is(e, sessions.ErrDenied) || out.Applied {
		t.Fatal("schema15 no-decision actual close bypass", out, e)
	}
	after, _ := os.ReadFile(f.task.Path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("no-decision refusal changed task")
	}
	out, e := f.access.ApplyWithPolicy(f.input, "", d.ID)
	if e != nil || !out.Applied {
		t.Fatal("policy-bound actual canonical close failed", out, e)
	}
	task, e := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
	if e != nil || task.Status != model.StatusDone || !store.LogHasStamp(task, "completed by ") {
		t.Fatal("policy consumer not actual DONE", e)
	}
	status, e := f.a.ReadNativePolicyStatus(f.owner, d.ID)
	if e != nil || status.Consumption == nil || status.Outcome == nil || status.Outcome.State != "closed" || status.Consumption.CanonicalOutcomeDigest != d.Prepared.CanonicalOutcomeDigest || status.Outcome.ObservedOutcomeDigest != d.Prepared.CanonicalOutcomeDigest {
		t.Fatal("separate consumed permission/observed outcome missing", status, e)
	}
	if l, e := f.a.RenewLease(f.workerToken, f.worker.ID, sessions.RenewLeaseRequest{OperationKey: "retired-renew", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpiresAt: f.lease.ExpiresAt}); !errors.Is(e, sessions.ErrDenied) || l.ID != "" {
		t.Fatal("retired worker received current permit", l, e)
	}
	if out, e = f.access.ApplyWithPolicy(f.input, "", second.ID); !errors.Is(e, sessions.ErrDenied) || out.Applied {
		t.Fatal("different decision attached to historical operation", out, e)
	}
	again, e := f.access.ApplyWithPolicy(f.input, "", d.ID)
	if e != nil || !again.Duplicate || !again.Applied {
		t.Fatal("exact historical outcome unavailable", again, e)
	}
}
func TestNativePolicyRecordedApprovalAndCurrentRevision(t *testing.T) {
	f, p, d := nativePolicyFixture(t, true)
	if d.Assessment.Verdict != policy.NeedsApproval {
		t.Fatal("missing exact approval not typed", d.Assessment)
	}
	if out, e := f.access.ApplyWithPolicy(f.input, "", d.ID); !errors.Is(e, sessions.ErrDenied) || out.Applied {
		t.Fatal("missing approval actual close bypass", out, e)
	}
	if _, e := f.a.ApproveNativePolicy(f.workerToken, sessions.NativePolicyApprovalRequest{OperationKey: "approve", DecisionID: d.ID, ExpiresAt: d.ExpiresAt}); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("nonowner forged approval", e)
	}
	if _, e := f.a.ApproveNativePolicy(f.owner, sessions.NativePolicyApprovalRequest{OperationKey: "approve", DecisionID: d.ID, ExpiresAt: d.ExpiresAt}); e != nil {
		t.Fatal(e)
	}
	p.Revision = "native-two"
	p.Allow = false
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.task.Path)
	if out, e := f.access.ApplyWithPolicy(f.input, "", d.ID); !errors.Is(e, sessions.ErrDenied) || out.Applied {
		t.Fatal("changed owner policy actual close bypass", out, e)
	}
	after, _ := os.ReadFile(f.task.Path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed policy refusal mutated canonical task")
	}
}
func TestNativePolicyApprovalActualClose(t *testing.T) {
	f, _, d := nativePolicyFixture(t, true)
	if _, e := f.a.ApproveNativePolicy(f.owner, sessions.NativePolicyApprovalRequest{OperationKey: "approval", DecisionID: d.ID, ExpiresAt: d.ExpiresAt}); e != nil {
		t.Fatal(e)
	}
	if out, e := f.access.ApplyWithPolicy(f.input, "", d.ID); e != nil || !out.Applied {
		t.Fatal("exact recorded approval failed actual close", out, e)
	}
}
func TestNativePolicyActualConsumerChangedBinding(t *testing.T) {
	for _, x := range []string{"action", "revision", "fence", "tree", "canonical-body", "worker-revoked", "reviewer-revoked"} {
		t.Run(x, func(t *testing.T) {
			f, _, d := nativePolicyFixture(t, false)
			in := f.input
			switch x {
			case "action":
				in.OperationKey = "another-action"
			case "revision":
				in.RevisionID = "another-revision"
			case "fence":
				in.ExpectedFence++
			case "tree":
				in.Tree = "another-tree"
			case "canonical-body":
				store.AppendLog(f.task, "changed semantic action payload after evaluation")
				if e := store.SaveTask(f.task); e != nil {
					t.Fatal(e)
				}
			case "worker-revoked":
				if e := f.a.Revoke(f.owner, f.worker.ID); e != nil {
					t.Fatal(e)
				}
			case "reviewer-revoked":
				if e := f.a.Revoke(f.owner, f.reviewer.ID); e != nil {
					t.Fatal(e)
				}
			}
			before, _ := os.ReadFile(f.task.Path)
			authorityBefore, _ := os.ReadFile(f.access.owner.config.Authority)
			if out, e := f.access.ApplyWithPolicy(in, "", d.ID); !errors.Is(e, sessions.ErrDenied) || out.Applied {
				t.Fatal("changed exact policy binding actual close bypass", out, e)
			}
			after, _ := os.ReadFile(f.task.Path)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("binding refusal changed canonical task")
			}
			authorityAfter, _ := os.ReadFile(f.access.owner.config.Authority)
			if !reflect.DeepEqual(authorityBefore, authorityAfter) {
				t.Fatal("binding refusal consumed or retired authority")
			}
		})
	}
}

func TestNativePolicyActualClosingCrashAndPartialRecovery(t *testing.T) {
	for _, kind := range []string{"before-save", "ignored-partial", "tracked-partial", "after-done"} {
		t.Run(kind, func(t *testing.T) {
			f := acceptanceSetupMode(t, kind == "tracked-partial")
			if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
				t.Fatal(e)
			}
			p := sessions.NativePolicy{SchemaVersion: 1, ProjectID: "p", TaskID: f.task.ID, Revision: "one", Allow: true, ExpiresAt: time.Now().Add(time.Hour)}
			if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
				t.Fatal(e)
			}
			d, e := f.access.EvaluatePolicy(sessions.NativePolicyEvaluationRequest{OperationKey: "decision", Input: f.input, ExpiresAt: p.ExpiresAt})
			if e != nil {
				t.Fatal(e)
			}
			a, o, observer, e := f.access.authenticate()
			if e != nil {
				t.Fatal(e)
			}
			var body string
			rec, e := a.BeginNativeAcceptanceWithPolicy(o, f.input, d.ID, func(rec sessions.NativeAcceptanceRecord) (string, error) {
				var digest string
				body, digest, e = store.PrepareNativeAcceptanceTask(f.task, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(f.input, rec.Candidate), rec.CreatedAt)
				return digest, e
			})
			if e != nil {
				t.Fatal(e)
			}
			observer.receipt, observer.before = rec.CanonicalReceiptDigest, f.input.ChecklistDigest
			destination := filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path))
			if kind != "before-save" {
				out, e := a.ContinueNativeAcceptance(o, f.input, func(sessions.NativeAcceptanceRecord) (bool, error) { return false, nil }, func(rec sessions.NativeAcceptanceRecord) error {
					if kind != "after-done" {
						if e := os.MkdirAll(destination, 0700); e != nil {
							return e
						}
					}
					if e := store.CloseTaskWithNativeAcceptance(f.w, f.task, rec.CanonicalReceiptDigest, body, rec.CanonicalOutcomeDigest); e != nil {
						return e
					}
					return errors.New("injected lost final reply before authority outcome")
				})
				if e == nil || out.Applied {
					t.Fatal("actual partial effect falsely Applied", out, e)
				}
				if kind != "after-done" {
					if e := os.Remove(destination); e != nil {
						t.Fatal(e)
					}
				}
			}
			status, e := a.ReadNativePolicyStatus(o, d.ID)
			if e != nil || status.Outcome.State != "unknown" || status.Outcome.ObservedOutcomeDigest != "" {
				t.Fatal("partial/crashed effect manufactured observed outcome", status, e)
			}
			out, e := f.access.ApplyWithPolicy(f.input, "", d.ID)
			if kind == "tracked-partial" {
				if !errors.Is(e, sessions.ErrDenied) || !errors.Is(e, sessions.ErrReconcile) || out.Applied {
					t.Fatal("tracked policy partial requires explicit reconciliation", out, e)
				}
				return
			}
			if e != nil || !out.Applied {
				t.Fatal("original policy operation recovery failed", out, e)
			}
			status, e = a.ReadNativePolicyStatus(o, d.ID)
			if e != nil || status.Outcome.State != "closed" || status.Outcome.ObservedOutcomeDigest != d.Prepared.CanonicalOutcomeDigest {
				t.Fatal("actual finalization lost observed raw outcome", status, e)
			}
			var durable struct{ Leases map[string]sessions.Lease }
			raw, _ := os.ReadFile(f.access.owner.config.Authority)
			if json.Unmarshal(raw, &durable) != nil || durable.Leases[f.lease.ID].Released {
				t.Fatal("policy recovery released protection")
			}
		})
	}
}
func TestNativePolicyActualTwoProcessSingleConsumption(t *testing.T) {
	if path := os.Getenv("DACLI578_POLICY_CHILD"); path != "" {
		var r struct {
			Owner, ReviewPolicy, Decision, Result string
			Input                                 sessions.NativeAcceptanceInput
		}
		raw, e := os.ReadFile(path)
		if e != nil || json.Unmarshal(raw, &r) != nil {
			t.Fatal("child input unavailable")
		}
		p, e := OpenNativeAcceptanceOwner(r.Owner, r.ReviewPolicy)
		if e != nil {
			t.Fatal("child owner unavailable")
		}
		out, e := p.ApplyWithPolicy(r.Input, "", r.Decision)
		b, _ := json.Marshal(struct{ Applied, Denied bool }{out.Applied, errors.Is(e, sessions.ErrDenied)})
		if e := os.WriteFile(r.Result, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	f, p, d1 := nativePolicyFixture(t, false)
	in2 := f.input
	in2.OperationKey = "second-action"
	d2, e := f.access.EvaluatePolicy(sessions.NativePolicyEvaluationRequest{OperationKey: "decision-two", Input: in2, ExpiresAt: p.ExpiresAt})
	if e != nil {
		t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	commands := []*exec.Cmd{}
	for i, d := range []sessions.NativePolicyDecision{d1, d2} {
		name := filepath.Join(dir, string(rune('a'+i)))
		acceptanceJSON(t, name, struct {
			Owner, ReviewPolicy, Decision, Result string
			Input                                 sessions.NativeAcceptanceInput
		}{f.access.owner.path, f.access.policyPath, d.ID, name + "-result", d.Input})
		c := exec.Command(exe, "-test.run=^TestNativePolicyActualTwoProcessSingleConsumption$")
		c.Env = append(store.VerificationEnvironment(), "DACLI578_POLICY_CHILD="+name)
		commands = append(commands, c)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, c := range commands {
		wg.Add(1)
		go func(i int, c *exec.Cmd) { defer wg.Done(); errs[i] = c.Run() }(i, c)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal("child process failed", e)
		}
	}
	applied, denied := 0, 0
	for i := 0; i < 2; i++ {
		var r struct{ Applied, Denied bool }
		raw, _ := os.ReadFile(filepath.Join(dir, string(rune('a'+i))) + "-result")
		if json.Unmarshal(raw, &r) != nil {
			t.Fatal("child actual result absent")
		}
		if r.Applied {
			applied++
		}
		if r.Denied {
			denied++
		}
	}
	if applied != 1 || denied != 1 {
		t.Fatalf("concurrent actual consumers admitted duplicate action: applied=%d denied=%d", applied, denied)
	}
	var durable struct {
		NativePolicyConsumptions map[string]sessions.NativePolicyConsumption
		NativePolicyOutcomes     map[string]sessions.NativePolicyOutcome
		Leases                   map[string]sessions.Lease
	}
	raw, _ := os.ReadFile(f.access.owner.config.Authority)
	if json.Unmarshal(raw, &durable) != nil || len(durable.NativePolicyConsumptions) != 1 || len(durable.NativePolicyOutcomes) != 1 || durable.Leases[f.lease.ID].Released {
		t.Fatal("concurrent action authority/outcome/protection mismatch")
	}
	task, e := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
	if e != nil || task.Status != model.StatusDone {
		t.Fatal("winning process did not actually close task", e)
	}
}

func TestNativePolicyActualMigratedClosingNoFreshEffect(t *testing.T) {
	f := acceptanceSetup(t)
	a, o, _, e := f.access.authenticate()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.BeginNativeAcceptance(o, f.input, func(rec sessions.NativeAcceptanceRecord) (string, error) {
		_, d, e := store.PrepareNativeAcceptanceTask(f.task, rec.CanonicalReceiptDigest, rec.Candidate.ReviewerSessionID, nativeAcceptanceSummary(f.input, rec.Candidate), rec.CreatedAt)
		return d, e
	}); e != nil {
		t.Fatal(e)
	}
	if e = a.UpgradeNativePolicy(o); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.task.Path)
	authorityBefore, _ := os.ReadFile(f.access.owner.config.Authority)
	if out, e := f.access.Apply(f.input, ""); !errors.Is(e, sessions.ErrDenied) || out.Applied {
		t.Fatal("migrated unconsumed closing performed actual new effect", out, e)
	}
	after, _ := os.ReadFile(f.task.Path)
	authorityAfter, _ := os.ReadFile(f.access.owner.config.Authority)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(authorityBefore, authorityAfter) {
		t.Fatal("migrated closing refusal mutated effect or consumption")
	}
}
