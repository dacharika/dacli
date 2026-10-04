package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestNativeAcceptanceMigrationPreservesHistoriesAndPersistenceFailure(t *testing.T) {
	f := nativeReviewSetup(t)
	p := f.propose(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeSwarms, f.a.UpgradeDispatchIntents, f.a.UpgradeLifecycle, f.a.UpgradeRecovery, f.a.UpgradePreflight, f.a.UpgradeCheckpoints, f.a.UpgradeBudgets} {
		if e := upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	beforeBytes, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	var before map[string]json.RawMessage
	if e = json.Unmarshal(beforeBytes, &before); e != nil {
		t.Fatal(e)
	}
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	if e = f.a.UpgradeNativeAcceptance(f.owner); !errors.Is(e, ErrStorage) {
		t.Fatal("migration persistence failure lost", e)
	}
	current, _ := os.ReadFile(f.a.path)
	if !reflect.DeepEqual(beforeBytes, current) {
		t.Fatal("failed migration changed history")
	}
	f.a.persistOverride = nil
	if e = f.a.UpgradeNativeAcceptance(f.owner); e != nil {
		t.Fatal(e)
	}
	afterBytes, _ := os.ReadFile(f.a.path)
	var after map[string]json.RawMessage
	if e = json.Unmarshal(afterBytes, &after); e != nil {
		t.Fatal(e)
	}
	for key, value := range before {
		if key == "Version" || key == "TaskTerminals" || key == "NativeAcceptanceOperations" {
			continue
		}
		if !reflect.DeepEqual(value, after[key]) {
			t.Fatalf("schema14 changed existing history %s", key)
		}
	}
	for _, key := range []string{"TaskTerminals", "NativeAcceptanceOperations"} {
		if string(before[key]) != "null" || string(after[key]) != "{}" {
			t.Fatalf("new schema14 map %s not initialized exactly: %s -> %s", key, before[key], after[key])
		}
	}
	c, e := f.a.ReadNativeAcceptanceCandidate(f.token, f.reviewer.ID, p.ID)
	if e != nil {
		t.Fatal("review service broken after migration", e)
	}
	r := c.Input
	r.OperationKey = "faulted-close"
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	out, e := f.a.BeginNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (string, error) { return OutputDigest([]byte("canonical body")), nil })
	if !errors.Is(e, ErrStorage) || out.State != "" {
		t.Fatalf("failed closing persistence returned usable retirement: %+v %v", out, e)
	}
	current, _ = os.ReadFile(f.a.path)
	if !reflect.DeepEqual(current, afterBytes) {
		t.Fatal("failed closing persistence changed durable state")
	}
	f.a.persistOverride = nil
	if _, e = f.a.BeginNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (string, error) { return OutputDigest([]byte("canonical body")), nil }); e != nil {
		t.Fatal(e)
	}
	closed := false
	f.a.persistOverride = func([]byte) error { return ErrStorage }
	result, e := f.a.ContinueNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (bool, error) { return closed, nil }, func(NativeAcceptanceRecord) error { closed = true; return nil })
	if !errors.Is(e, ErrStorage) || result.Applied {
		t.Fatalf("canonical success/native persist failure falsely Applied: %+v %v", result, e)
	}
	f.a.persistOverride = nil
	result, e = f.a.ContinueNativeAcceptance(f.owner, r, func(NativeAcceptanceRecord) (bool, error) { return closed, nil }, func(NativeAcceptanceRecord) error { t.Fatal("repair reexecuted canonical close"); return nil })
	if e != nil || !result.Applied {
		t.Fatal("exact historical outcome repair failed", result, e)
	}
}
