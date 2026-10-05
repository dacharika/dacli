package policy

import (
	"reflect"
	"testing"
)

func TestNativePolicyAssessmentNoEffect(t *testing.T) {
	b := NativeAcceptanceObservation{ActorID: "owner", Action: "native-task-accept", TaskID: "task", ActionDigest: "digest", PolicyRevision: "one", Allowed: true}
	for _, x := range []struct {
		name   string
		mutate func(*NativeAcceptanceObservation)
		want   Verdict
	}{{"allow", func(*NativeAcceptanceObservation) {}, Allow}, {"deny", func(b *NativeAcceptanceObservation) { b.Allowed = false }, Deny}, {"approval", func(b *NativeAcceptanceObservation) { b.ApprovalRequired = true }, NeedsApproval}, {"approved", func(b *NativeAcceptanceObservation) { b.ApprovalRequired = true; b.Approved = true }, Allow}, {"stop", func(b *NativeAcceptanceObservation) { b.Stopped = true }, Deny}} {
		t.Run(x.name, func(t *testing.T) {
			in := b
			x.mutate(&in)
			before := in
			out := EvaluateNativeAcceptance(in)
			if out.Verdict != x.want || out.Assurance != "observed-advisory" || len(out.Reasons) != 1 || len(out.Obligations) != 2 || !reflect.DeepEqual(before, in) {
				t.Fatalf("typed side-effect-free assessment lost: %+v", out)
			}
		})
	}
}
