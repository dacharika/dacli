package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func dashboardReader(t *testing.T, a *Authority, owner string, tasks ...string) (Session, string) {
	t.Helper()
	resources := []string{"workspace:" + a.binding.Workspace, "project:project"}
	for _, task := range tasks {
		resources = append(resources, "task:"+task)
	}
	actor, token, e := a.Enroll(owner, Scope{Actions: []string{"read"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	return actor, token
}
func TestNativeDashboardExactProducerLinksPrivacyAndApproval(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	p.RequireApproval = true
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "dashboard-decision", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	actor, token := dashboardReader(t, f.a, f.owner, "task-a")
	before, _ := os.ReadFile(f.a.path)
	snapshot, e := f.a.ReadDashboard(token, actor.ID, "project", "task-a")
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(f.a.path)
	if !bytes.Equal(before, after) {
		t.Fatal("dashboard read changed authority")
	}
	seen := map[string]DashboardRecord{}
	for _, r := range snapshot.Records {
		seen[r.Kind+":"+r.ID] = r
	}
	if seen["attempt:"+f.at.ID].ActorID != f.worker.ID || seen["lease:"+f.lease.ID].Fence != f.lease.Fence || seen["evidence:"+f.evidence.ID].RevisionID != f.revision.ID || seen["decision:"+d.ID].ReviewerID != f.reviewer.ID {
		t.Fatal("exact revision producer links lost", snapshot)
	}
	if seen["evidence:"+f.evidence.ID].Eligibility != "historical-not-revalidated" {
		t.Fatal("stored evidence promoted to current authority")
	}
	raw, _ := json.Marshal(snapshot)
	for _, secret := range []string{hash(token), hash(f.workerToken), f.at.Observation.Locator, f.check.Command, "credential_file", "CommandDigest", "OwnerHash"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private authority field leaked in dashboard")
		}
	}
	found := false
	for _, item := range snapshot.Attention {
		if item.ID == d.ID && item.Category == "approval-needed" {
			found = true
		}
	}
	if !found {
		t.Fatal("current owner approval obligation absent")
	}
	if _, e = f.a.ApproveNativePolicy(f.owner, NativePolicyApprovalRequest{OperationKey: "dashboard-approval", DecisionID: d.ID, ExpiresAt: d.ExpiresAt}); e != nil {
		t.Fatal(e)
	}
	snapshot, e = f.a.ReadDashboard(token, actor.ID, "project", "task-a")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range snapshot.Attention {
		if item.ID == d.ID {
			t.Fatal("approved historical decision remained actionable")
		}
	}
}
func TestNativeDashboardScopeRevocationAndSchema10(t *testing.T) {
	f := nativeReviewSetup(t)
	f.propose(t)
	for _, up := range []func(string) error{f.a.UpgradeSwarms, f.a.UpgradeDispatchIntents, f.a.UpgradeLifecycle, f.a.UpgradeRecovery} {
		if e := up(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.a.BindTask(f.owner, TaskBinding{TaskID: "task-b", ProjectID: "project", Repository: f.revision.Observation.Repository, AdmissionEnabled: true}); e != nil {
		t.Fatal(e)
	}
	foreign, foreignToken, e := f.a.Enroll(f.owner, Scope{Actions: []string{"attempt.register"}, Resources: []string{"task:task-b", "checkout:"}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	foreignAttempt, e := f.a.RegisterAttempt(foreignToken, foreign.ID, AttemptRequest{RequestID: "foreign-task", TaskID: "task-b", Checkout: f.revision.Observation.Locator})
	if e != nil {
		t.Fatal(e)
	}
	actor, token := dashboardReader(t, f.a, f.owner, "task-a")
	projectView, e := f.a.ReadDashboard(token, actor.ID, "project", "")
	if e != nil {
		t.Fatal(e)
	}
	for _, record := range projectView.Records {
		if record.TaskID == "task-b" || record.ID == foreignAttempt.ID {
			t.Fatal("foreign task record escaped scoped dashboard")
		}
	}
	snapshot, e := f.a.ReadDashboard(token, actor.ID, "project", "task-a")
	if e != nil || snapshot.AuthorityVersion != 10 || snapshot.Services["policy"] != "unsupported" || snapshot.Spend.ProviderTokens != nil || snapshot.Spend.State != "unavailable" {
		t.Fatal("schema10 fabricated unsupported services or spend", e, snapshot)
	}
	if _, e = f.a.ReadDashboard(token, actor.ID, "other-project", "task-a"); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-project native records disclosed")
	}
	if _, e = f.a.ReadDashboard(token, actor.ID, "project", "other-task"); !errors.Is(e, ErrDenied) {
		t.Fatal("cross-task native records disclosed")
	}
	if e = f.a.Revoke(f.owner, actor.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.ReadDashboard(token, actor.ID, "project", "task-a"); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked dashboard reader retained authority")
	}
}
func TestNativeDashboardBoundedDescriptorAndDecoder(t *testing.T) {
	f := nativeReviewSetup(t)
	a, owner := f.a, f.owner
	actor, token := dashboardReader(t, a, owner, "task-a")
	original, _ := os.ReadFile(a.path)
	cases := []string{"fifo", "symlink", "oversized", "malformed-schema", "permissions"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			if e := os.Remove(a.path); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "fifo":
				if e := unix.Mkfifo(a.path, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				p := filepath.Join(t.TempDir(), "private.json")
				if e := os.WriteFile(p, original, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(p, a.path); e != nil {
					t.Fatal(e)
				}
			case "oversized":
				f, e := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				if e := f.Truncate(dashboardAuthorityLimit + 1); e != nil {
					t.Fatal(e)
				}
				if e := f.Close(); e != nil {
					t.Fatal(e)
				}
			case "malformed-schema":
				var s map[string]json.RawMessage
				if e := json.Unmarshal(original, &s); e != nil {
					t.Fatal(e)
				}
				s["Version"] = json.RawMessage("10")
				raw, _ := json.Marshal(s)
				if e := os.WriteFile(a.path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "permissions":
				if e := os.WriteFile(a.path, original, 0644); e != nil {
					t.Fatal(e)
				}
			}
			start := time.Now()
			out, e := a.ReadDashboard(token, actor.ID, "project", "task-a")
			if e == nil || out.Schema != "" || time.Since(start) > time.Second {
				t.Fatal("unsafe authority read was accepted or unbounded", kind, e)
			}
			if e := os.Remove(a.path); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(a.path, original, 0600); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestNativeDashboardHistoricalBudgetScopeAndNullUsage(t *testing.T) {
	f, _, _ := budgetSetup(t, 10000)
	actor, token := dashboardReader(t, f.a, f.owner, "task-a")
	snapshot, e := f.a.ReadDashboard(token, actor.ID, "project", "task-a")
	if e != nil {
		t.Fatal(e)
	}
	if snapshot.Spend.EstimatedSpent != nil || snapshot.Spend.ProviderTokens != nil {
		t.Fatal("partial task scope disclosed project budget")
	}
	if !dashboardBudgetScope(&state{BudgetAllocations: map[string]BudgetAllocation{}}, BudgetPolicy{Tasks: map[string]BudgetTask{"task-a": {}}}, map[string]bool{"task-a": true}) {
		t.Fatal("exact budget scope refused")
	}
	state := state{BudgetAllocations: map[string]BudgetAllocation{"old": {ProjectID: "project", TaskID: "removed-task"}}}
	if dashboardBudgetScope(&state, BudgetPolicy{ProjectID: "project", Tasks: map[string]BudgetTask{"task-a": {}}}, map[string]bool{"task-a": true}) {
		t.Fatal("historical out-of-scope allocation leaked spend")
	}
}

func TestNativeDashboardCurrentPreflightAndSourceFreshness(t *testing.T) {
	now := time.Now()
	s := state{PreflightPolicies: map[string]PreflightPolicy{"project": {Revision: "current"}}, PreflightOperations: map[string]PreflightReceipt{
		"old":     {Report: PreflightReport{ProjectID: "project", TaskID: "task-a", PolicyRevision: "current", ObservedAt: now.Add(-time.Minute), Phases: []PreflightPhase{{Phase: "review", Required: true, Verdict: "unsupported"}}}},
		"ready":   {Report: PreflightReport{ProjectID: "project", TaskID: "task-a", PolicyRevision: "current", ObservedAt: now, Allowed: true}},
		"foreign": {Report: PreflightReport{ProjectID: "project", TaskID: "task-b", PolicyRevision: "current", ObservedAt: now.Add(time.Minute)}},
	}}
	selected := dashboardCurrentPreflight(&s, "project", map[string]bool{"task-a": true})
	if len(selected) != 1 || !selected["task-a"].Allowed {
		t.Fatal("old failed receipt superseded current ready proof", selected)
	}
	source := now.Add(-2 * time.Hour)
	expiry := source.Add(time.Hour)
	if dashboardFreshness(source, &expiry, now) != "stale" {
		t.Fatal("delayed historical source report appeared fresh")
	}
}
