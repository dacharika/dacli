package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func nativePolicySetup(t *testing.T) (nativeReviewFixture, NativeAcceptanceInput, NativePolicy) {
	t.Helper()
	f := nativeReviewSetup(t)
	p := f.propose(t)
	for _, up := range []func(string) error{f.a.UpgradeSwarms, f.a.UpgradeDispatchIntents, f.a.UpgradeLifecycle, f.a.UpgradeRecovery, f.a.UpgradePreflight, f.a.UpgradeCheckpoints, f.a.UpgradeBudgets, f.a.UpgradeNativeAcceptance} {
		if e := up(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	c, e := f.a.ReadNativeAcceptanceCandidate(f.token, f.reviewer.ID, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	in := c.Input
	in.OperationKey = "native-effect"
	policy := NativePolicy{SchemaVersion: 1, ProjectID: in.ProjectID, TaskID: in.TaskID, Revision: "one", Allow: true, ExpiresAt: time.Now().Add(time.Hour)}
	return f, in, policy
}
func nativePolicyBody(NativeAcceptanceRecord) (string, error) {
	return OutputDigest([]byte("exact test canonical outcome")), nil
}
func TestNativePolicyMigrationAndAtomicPersistence(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if _, e := f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, "unsupported", nativePolicyBody); !errors.Is(e, ErrDenied) {
		t.Fatal("schema14 ignored requested policy", e)
	}
	raw, _ := os.ReadFile(f.a.path)
	var before map[string]json.RawMessage
	_ = json.Unmarshal(raw, &before)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	raw, _ = os.ReadFile(f.a.path)
	var after map[string]json.RawMessage
	_ = json.Unmarshal(raw, &after)
	newMaps := map[string]bool{"NativePolicies": true, "NativePolicyHistory": true, "NativePolicyDecisions": true, "NativePolicyApprovals": true, "NativePolicyConsumptions": true, "NativePolicyOutcomes": true}
	for k, v := range before {
		if k == "Version" {
			continue
		}
		if newMaps[k] {
			if string(v) != "null" || string(after[k]) != "{}" {
				t.Fatal("new schema15 map initialization mismatch", k)
			}
			continue
		}
		if !reflect.DeepEqual(v, after[k]) {
			t.Fatal("schema15 changed existing history", k)
		}
	}
	old := *f.a
	old.version = 14
	if _, e := old.Authenticate(f.workerToken, f.worker.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("old schema14 reader accepted15", e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "decision", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	saved, _ := os.ReadFile(f.a.path)
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	rec, e := f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d.ID, nativePolicyBody)
	if !errors.Is(e, ErrStorage) || rec.State != "" {
		t.Fatal("failed atomic consumption returned effect authority", rec, e)
	}
	now, _ := os.ReadFile(f.a.path)
	if !reflect.DeepEqual(saved, now) {
		t.Fatal("failed consumption persisted partial authority")
	}
	f.a.persistOverride = nil
	if _, e = f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d.ID, nativePolicyBody); e != nil {
		t.Fatal(e)
	}
	status, e := f.a.ReadNativePolicyStatus(f.owner, d.ID)
	if e != nil || status.Consumption == nil || status.Outcome == nil || status.Outcome.State != "unknown" || status.Outcome.ObservedOutcomeDigest != "" || !status.Outcome.ObservedAt.IsZero() {
		t.Fatal("consumption manufactured action outcome", status, e)
	}
	closed := false
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	out, e := f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) { return closed, nil }, func(NativeAcceptanceRecord) error { closed = true; return nil })
	if !errors.Is(e, ErrStorage) || out.Applied {
		t.Fatal("lost final persistence falsely Applied", out, e)
	}
	f.a.persistOverride = nil
	out, e = f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) { return closed, nil }, func(NativeAcceptanceRecord) error { t.Fatal("historical finalization repeated action"); return nil })
	if e != nil || !out.Applied {
		t.Fatal("exact action outcome recovery lost", out, e)
	}
}
func TestNativePolicyHistoricalPolicyCannotReviveAndOutcomeParity(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d1, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "d1", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	d2, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "d2", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d1.ID, nativePolicyBody); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d2.ID, nativePolicyBody); !errors.Is(e, ErrDenied) {
		t.Fatal("different evaluated decision attached to consumed operation", e)
	}
	status, e := f.a.ReadNativePolicyStatus(f.owner, d2.ID)
	if e != nil || status.Outcome != nil || status.Consumption != nil {
		t.Fatal("unconsumed decision inherited another decision outcome", status, e)
	}
	p2 := p
	p2.Revision = "two"
	p2.Stopped = true
	if e = f.a.ConfigureNativePolicy(f.owner, p2); e != nil {
		t.Fatal(e)
	}
	if e = f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	out, e := f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) { return false, nil }, func(NativeAcceptanceRecord) error {
		t.Fatal("historical policy replay revived fresh action")
		return nil
	})
	if !errors.Is(e, ErrDenied) || out.Applied {
		t.Fatal("historical configuration revived old allow", out, e)
	}
}
func TestNativePolicyMigratedClosingHistoricalOnly(t *testing.T) {
	f, in, _ := nativePolicySetup(t)
	if _, e := f.a.BeginNativeAcceptance(f.owner, in, nativePolicyBody); e != nil {
		t.Fatal(e)
	}
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.BeginNativeAcceptance(f.owner, in, nativePolicyBody); e != nil {
		t.Fatal("old closing history unavailable", e)
	}
	if out, e := f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) { return false, nil }, func(NativeAcceptanceRecord) error {
		t.Fatal("migrated unconsumed closing got fresh effect")
		return nil
	}); !errors.Is(e, ErrDenied) || out.Applied {
		t.Fatal("migrated no-policy action permitted", out, e)
	}
	if out, e := f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) { return true, nil }, func(NativeAcceptanceRecord) error { t.Fatal("exact DONE history reexecuted"); return nil }); e != nil || !out.Applied {
		t.Fatal("migrated exact DONE historical repair refused", out, e)
	}
	var state state
	raw, _ := os.ReadFile(f.a.path)
	_ = json.Unmarshal(raw, &state)
	if len(state.NativePolicyConsumptions) != 0 || len(state.NativePolicyOutcomes) != 0 {
		t.Fatal("migration fabricated policy consumption/outcome")
	}
}
func TestNativePolicyFinalSlowObservationExpiry(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	// Leave time for real Git observations under race instrumentation. The second
	// continuation observation still waits until the exact decision expires, so
	// this tests the final effect guard rather than accidental setup expiry.
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "short", Input: in, ExpiresAt: time.Now().Add(5 * time.Second)}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d.ID, nativePolicyBody); e != nil {
		t.Fatal(e)
	}
	n := 0
	out, e := f.a.ContinueNativeAcceptance(f.owner, in, func(NativeAcceptanceRecord) (bool, error) {
		n++
		if n == 2 {
			time.Sleep(time.Until(d.ExpiresAt) + 20*time.Millisecond)
		}
		return false, nil
	}, func(NativeAcceptanceRecord) error {
		t.Fatal("expired policy decision reached canonical effect")
		return nil
	})
	if !errors.Is(e, ErrDenied) || out.Applied {
		t.Fatal("final slow observer bypassed decision expiry", out, e)
	}
}

func TestNativePolicyPreparedBodyGuard(t *testing.T) {
	f, in, p := nativePolicySetup(t)
	if e := f.a.UpgradeNativePolicy(f.owner); e != nil {
		t.Fatal(e)
	}
	if e := f.a.ConfigureNativePolicy(f.owner, p); e != nil {
		t.Fatal(e)
	}
	d, e := f.a.EvaluateNativePolicy(f.owner, NativePolicyEvaluationRequest{OperationKey: "body-decision", Input: in, ExpiresAt: p.ExpiresAt}, nativePolicyBody)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.BeginNativeAcceptanceWithPolicy(f.owner, in, d.ID, func(NativeAcceptanceRecord) (string, error) {
		return OutputDigest([]byte("changed canonical action body")), nil
	})
	if !errors.Is(e, ErrDenied) || out.State != "" {
		t.Fatal("changed prepared body consumed action authority", out, e)
	}
	after, _ := os.ReadFile(f.a.path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("body refusal persisted consumption or retirement")
	}
}
