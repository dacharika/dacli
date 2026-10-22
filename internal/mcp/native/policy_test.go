package native

import (
	"bytes"
	"encoding/json"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"testing"
)

func TestNativePolicyToolStrictProjectionAndFallback(t *testing.T) {
	f := setup(t)
	before, _ := os.ReadFile(f.c.Authority)
	args := policyArgs{version: version{1}, Project: "p", Task: f.task.ID}
	data := call(t, f, "get_policy_status", args).(sessions.NativePolicyObservation)
	if data.State != "unavailable" || data.EffectAuthorized {
		t.Fatal("old schema status became effect authority", data)
	}
	r := result("get_policy_status", data, nil).(map[string]any)
	structured, _ := json.Marshal(r["structuredContent"])
	fallback := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if string(structured) != fallback {
		t.Fatal("policy structured/text mismatch")
	}
	after, _ := os.ReadFile(f.c.Authority)
	if !bytes.Equal(before, after) {
		t.Fatal("policy tool migrated or wrote authority")
	}
	for _, bad := range []map[string]any{{"schema_version": 1, "project": "p"}, {"schema_version": 1, "project": "p", "task": f.task.ID, "command": "override"}, {"schema_version": 1, "project": "p", "task": f.task.ID, "owner_config": "override"}, {"schema_version": 1, "project": "wrong", "task": f.task.ID}, {"schema_version": 1, "project": "p", "task": f.task.ID, "decision_id": "unknown"}} {
		raw, _ := json.Marshal(bad)
		_, e := f.s.call("get_policy_status", raw)
		if e == nil {
			t.Fatal("strict policy override or binding accepted", bad)
		}
		refusal := result("get_policy_status", nil, e).(map[string]any)
		wire, _ := json.Marshal(refusal["structuredContent"])
		if !bytes.Contains(wire, []byte(`"exit_code":3`)) || !validJSON(outputSchema("get_policy_status"), wire) {
			t.Fatal("unstable typed refusal", string(wire))
		}
	}
	data.EffectAuthorized = true
	bad := result("get_policy_status", data, nil).(map[string]any)
	if bad["isError"] != true {
		t.Fatal("effect authorized output escaped schema validator")
	}
}
