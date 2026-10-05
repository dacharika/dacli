package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRetentionReferencesHoldsAndExactIdentity(t *testing.T) {
	w, task, _, restore := cleanupFixture(t)
	defer restore()
	native := NativeRetentionAuthority{Complete: true, Digest: strings.Repeat("a", 64)}
	baseline, e := PlanNativeRetention(w, "core", native)
	if e != nil {
		t.Fatal(e)
	}
	var generated NativeRetentionItem
	for _, item := range baseline.Items {
		if filepath.Base(item.Path) == "capture.tmp" {
			generated = item
		}
	}
	if !generated.Removable || generated.Identity == "" || generated.Digest == "" || generated.Size <= 0 {
		t.Fatal("native retention lost exact removable artifact identity")
	}
	before, e := os.ReadFile(generated.Path)
	if e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		name  string
		refs  []NativeRetentionReference
		holds []NativeRetentionHold
	}{
		{"referenced", []NativeRetentionReference{{TaskID: task.ID, Digest: generated.Digest, Kind: "accepted verification"}}, nil},
		{"copied referenced", []NativeRetentionReference{{TaskID: "other-task", Digest: generated.Digest, Kind: "accepted verification"}}, nil},
		{"unavailable output digest", []NativeRetentionReference{{TaskID: task.ID, Digest: "unavailable", Kind: "verification"}}, nil},
		{"expired retained lease", nil, []NativeRetentionHold{{TaskID: task.ID, Reason: "retained native lease"}}},
		{"global unknown", nil, []NativeRetentionHold{{Reason: "unknown native outcome"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := native
			changed.References = test.refs
			changed.Holds = test.holds
			plan, e := PlanNativeRetention(w, "core", changed)
			if e != nil {
				t.Fatal(e)
			}
			for _, item := range plan.Items {
				if item.Path == generated.Path && item.Removable {
					t.Fatal("native retention removed referenced evidence or hold")
				}
			}
		})
	}
	native.Complete = false
	if _, e = PlanNativeRetention(w, "core", native); e == nil {
		t.Fatal("native retention accepted incomplete authority")
	}
	after, _ := os.ReadFile(generated.Path)
	if string(before) != string(after) {
		t.Fatal("dry-run retention deleted or changed artifact")
	}
	for _, item := range baseline.Items {
		if filepath.Base(item.Path) == "transcript.log" && item.Removable {
			t.Fatal("native retention removed durable transcript")
		}
	}
}
