package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func policyReader(t *testing.T, a *Authority, owner, workspace, task string) (Session, string) {
	t.Helper()
	s, token, e := a.Enroll(owner, Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + workspace, "task:" + task}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	return s, token
}
func TestNativePolicyObservationUnavailableAndExactScope(t *testing.T) {
	a, e := Open(filepath.Join(t.TempDir(), "authority.json"), Binding{Workspace: "workspace", Installation: "install"})
	if e != nil {
		t.Fatal(e)
	}
	owner, e := a.Bootstrap()
	if e != nil {
		t.Fatal(e)
	}
	for _, up := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery} {
		if e = up(owner); e != nil {
			t.Fatal(e)
		}
	}
	if e = a.BindTask(owner, TaskBinding{TaskID: "task-a", ProjectID: "p", Repository: t.TempDir()}); e != nil {
		t.Fatal(e)
	}
	if e = a.BindTask(owner, TaskBinding{TaskID: "task-b", ProjectID: "p", Repository: t.TempDir()}); e != nil {
		t.Fatal(e)
	}
	s, token := policyReader(t, a, owner, "workspace", "task-a")
	before, _ := os.ReadFile(a.path)
	out, e := a.ObserveNativePolicy(token, s.ID, "p", "task-a", "")
	if e != nil || out.State != "unavailable" || out.EffectAuthorized || out.CurrentPolicy != nil || out.Decision != nil {
		t.Fatal("schema10 unavailable became permission", out, e)
	}
	if _, e = a.ObserveNativePolicy(token, s.ID, "wrong-project", "task-a", ""); !errors.Is(e, ErrDenied) {
		t.Fatal("authorized task wrong project disclosed unavailable", e)
	}
	after, _ := os.ReadFile(a.path)
	if !bytes.Equal(before, after) {
		t.Fatal("observation migrated or mutated authority")
	}
	unbound, unboundToken := policyReader(t, a, owner, "workspace", "unbound-task")
	unboundBefore, _ := os.ReadFile(a.path)
	if _, e = a.ObserveNativePolicy(unboundToken, unbound.ID, "p", "unbound-task", ""); !errors.Is(e, ErrDenied) {
		t.Fatal("unbound scoped task disclosed unavailable", e)
	}
	unboundAfter, _ := os.ReadFile(a.path)
	if !bytes.Equal(unboundBefore, unboundAfter) {
		t.Fatal("unbound refusal wrote authority")
	}
	if _, e = a.ObserveNativePolicy(token, s.ID, "p", "task-b", ""); !errors.Is(e, ErrDenied) {
		t.Fatal("wrong task scope disclosed policy", e)
	}
	if _, e = a.ObserveNativePolicy(token, s.ID, "p", "task-a", "unknown"); !errors.Is(e, ErrDenied) {
		t.Fatal("missing decision fabricated history", e)
	}
	if e = a.Revoke(owner, s.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ObserveNativePolicy(token, s.ID, "p", "task-a", ""); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked reader disclosed policy", e)
	}
}
func TestNativePolicyObservationHistoricalAndCurrentRemainSeparate(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "decision", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	s, token := policyReader(t, f.a, f.owner, f.a.binding.Workspace, in.TaskID)
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.ObserveNativePolicy(token, s.ID, in.ProjectID, in.TaskID, d.ID)
	if e != nil || out.State != "allowed" || out.EffectAuthorized || out.Decision == nil || !out.Decision.Historical || !out.Decision.MatchesCurrentPolicy || out.Decision.Consumption != nil || out.Decision.Outcome != nil {
		t.Fatal("policy observation fabricated effect or lost history", out, e)
	}
	encoded, _ := json.Marshal(out)
	if bytes.Contains(encoded, []byte(in.Repository)) || bytes.Contains(encoded, []byte("Prepared")) {
		t.Fatal("projection leaked prepared/private source", string(encoded))
	}
	after, _ := os.ReadFile(f.a.path)
	if !bytes.Equal(before, after) {
		t.Fatal("status wrote decision or outcome")
	}
	if _, e = f.a.ObserveNativePolicy(token, s.ID, "other", in.TaskID, d.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("wrong project disclosed decision", e)
	}
	// A valid scoped current task cannot borrow another task's historic decision.
	if e = f.a.transaction(false, true, func(st *state) error {
		wrong := d
		wrong.Input.TaskID = "other-task"
		st.NativePolicyDecisions[d.ID] = wrong
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.ObserveNativePolicy(token, s.ID, in.ProjectID, in.TaskID, d.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-task decision disclosed history", e)
	}
	if e = f.a.transaction(false, true, func(st *state) error { st.NativePolicyDecisions[d.ID] = d; return nil }); e != nil {
		t.Fatal(e)
	}
	p.Revision = "stopped-two"
	p.Stopped = true
	if e = f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	out, e = f.a.ObserveNativePolicy(token, s.ID, in.ProjectID, in.TaskID, d.ID)
	if e != nil || out.State != "stopped" || out.EffectAuthorized || out.Decision.MatchesCurrentPolicy || out.CurrentPolicy.Revision != p.Revision {
		t.Fatal("historical decision revived current stopped policy", out, e)
	}
}
func TestNativePolicyObservationApprovalConsumptionAndOutcomeAreDiagnostics(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	p.RequireApproval = true
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "decision", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.ApproveNativePolicy(f.owner, NativePolicyApprovalRequest{OperationKey: "approval", DecisionID: d.ID, ExpiresAt: d.ExpiresAt}); e != nil {
		t.Fatal(e)
	}
	s, token := policyReader(t, f.a, f.owner, f.a.binding.Workspace, in.TaskID)
	out, e := f.a.ObserveNativePolicy(token, s.ID, in.ProjectID, in.TaskID, d.ID)
	if e != nil || out.EffectAuthorized || out.Decision.Approval == nil || !out.Decision.Approval.Current {
		t.Fatal("approval observation mistaken for effect permission", out, e)
	}
	if _, e = f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d.ID, nativePolicyBody); e != nil {
		t.Fatal(e)
	}
	out, e = f.a.ObserveNativePolicy(token, s.ID, in.ProjectID, in.TaskID, d.ID)
	if e != nil || out.EffectAuthorized || out.Decision.Consumption == nil || out.Decision.Outcome == nil || out.Decision.Outcome.State != "unknown" || !out.Decision.Outcome.ObservedAt.IsZero() {
		t.Fatal("consumption manufactured completed outcome", out, e)
	}
}
