package sessions

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestVerificationInterruptionIsDurableNonEvidenceAndNeverReexecutes(t *testing.T) {
	for _, reason := range []string{"cancelled_before_start", "deadline_before_start", "cancelled_owned_group_cleanup", "deadline_owned_group_cleanup"} {
		t.Run(reason, func(t *testing.T) {
			f := evidenceSetup(t)
			f.begin(t)
			interrupted, err := f.a.InterruptVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check), reason)
			if err != nil || interrupted.Reason != reason || interrupted.Request != f.request || interrupted.RunnerSession != f.runner.ID || interrupted.Assurance != "cooperative-native-executor" {
				t.Fatal("exact runner interruption not recorded", interrupted, err)
			}
			before, err := os.ReadFile(f.a.path)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(f.a.path, f.a.binding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.LookupVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
			if err != nil || got.State != VerificationInterrupted || got.Evidence != nil || got.Interruption == nil || !reflect.DeepEqual(*got.Interruption, interrupted) {
				t.Fatal("reopen lost non-evidence interruption", got, err)
			}
			replay, err := reopened.InterruptVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check), reason)
			if err != nil || !reflect.DeepEqual(replay, interrupted) {
				t.Fatal("interruption replay changed identity", replay, err)
			}
			if _, _, err = reopened.BeginVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check)); !errors.Is(err, ErrReconcile) {
				t.Fatal("interrupted command reauthorized", err)
			}
			if _, err = reopened.CompleteVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, f.result()); !errors.Is(err, ErrDenied) {
				t.Fatal("late completion replaced interruption", err)
			}
			after, err := os.ReadFile(f.a.path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("replay/refused restart mutated authority", err)
			}
			if err = reopened.transaction(false, false, func(s *state) error {
				op := s.Operations[f.request.OperationKey]
				if op.Pending || op.Evidence != nil || len(s.Evidence) != 0 || op.Digest != identity("run:"+CheckDigest(f.check), f.runner.ID, f.request) || op.ResultDigest != interrupted.ResultDigest || s.Leases[f.lease.ID].Released {
					t.Fatal("interruption changed original binding, evidence, or protection")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerificationInterruptionRefusesWrongActorBindingAndUnknownCancellation(t *testing.T) {
	for _, mismatch := range []string{"worker as runner", "operation", "attempt", "lease", "revision", "fence", "check", "grant", "descriptor", "reason", "changed terminal reason"} {
		t.Run(mismatch, func(t *testing.T) {
			f := evidenceSetup(t)
			f.begin(t)
			req, token, runner, descriptor, reason := f.request, f.runnerToken, f.runner.ID, CheckDigest(f.check), "cancelled_before_start"
			switch mismatch {
			case "worker as runner":
				token, runner = f.workerToken, f.worker.ID
			case "operation":
				req.OperationKey = "other"
			case "attempt":
				req.AttemptID = "other"
			case "lease":
				req.LeaseID = "other"
			case "revision":
				req.RevisionID = "other"
			case "fence":
				req.ExpectedFence++
			case "check":
				req.CheckID = "other"
			case "grant":
				req.GrantID = "other"
			case "descriptor":
				descriptor = "other"
			case "reason":
				reason = "cancellation_requested"
			case "changed terminal reason":
				if _, err := f.a.InterruptVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, descriptor, "cancelled_owned_group_cleanup"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(f.a.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.InterruptVerification(f.workerToken, token, runner, req, descriptor, reason); !errors.Is(err, ErrDenied) {
				t.Fatal("mismatched interruption accepted", err)
			}
			after, err := os.ReadFile(f.a.path)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed protected operation", err)
			}
		})
	}
	f := evidenceSetup(t)
	f.begin(t)
	reopened, err := Open(f.a.path, f.a.binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.LookupVerification(f.workerToken, f.runnerToken, f.runner.ID, f.request, CheckDigest(f.check))
	if err != nil || got.State != VerificationPending || got.Interruption != nil || got.Evidence != nil {
		t.Fatal("unknown restart manufactured interruption", got, err)
	}
}
