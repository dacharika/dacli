package sessions

import "testing"

func TestNativeControllerKnownVerificationInterruptionRetainsProtection(t *testing.T) {
	f, st := controllerCompletedWave(t)
	action := st.Actions[0]
	route := f.start.Routes[action.TaskID]
	req := action.Verification
	var grant VerificationGrant
	if err := f.a.transaction(false, false, func(s *state) error { grant = s.Grants[req.GrantID]; return nil }); err != nil {
		t.Fatal(err)
	}
	workerToken, runnerToken := f.observer.tokens[action.TaskID], f.runnerTokens[route.RunnerSession]
	if _, _, err := f.a.BeginVerification(workerToken, runnerToken, route.RunnerSession, req, grant.CommandDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.InterruptVerification(workerToken, runnerToken, route.RunnerSession, req, grant.CommandDigest, "cancelled_owned_group_cleanup"); err != nil {
		t.Fatal(err)
	}
	halted := controllerStep(t, f, st, "observe-interruption")
	if !halted.Terminal || halted.HaltClass != "verification_interrupted" || halted.HostActionAuthorized || halted.RetainedConcurrency != 2 || halted.Corrections != 0 {
		t.Fatal("interruption granted retry/correction or lost protection", halted)
	}
	found := false
	for _, finding := range halted.Checkpoint.Findings {
		if finding.Class == "verification_interrupted" && finding.Knowledge == "known" {
			found = true
		}
	}
	if !found {
		t.Fatal("checkpoint hid known non-evidence interruption")
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if s.Leases[req.LeaseID].Released || len(s.Evidence) != 0 || len(s.Reviews) != 0 || len(s.Operations) == 0 || verificationInterruption(s.Operations[req.OperationKey], route.RunnerSession, req) == nil {
			t.Fatal("controller fabricated producers or released original protection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
