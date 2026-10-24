package sessions

import "testing"

func TestNativeRoleQualificationBudgetRevisionRefusesOldReservation(t *testing.T) {
	f, _, policy := budgetSetup(t, 130)
	wave := budgetReserve(t, f, "role-qualification-budget")
	if len(wave.Dispatches) == 0 {
		t.Fatal("positive control did not reserve a dispatch")
	}
	dispatch := wave.Dispatches[0]
	if err := f.a.transaction(false, false, func(s *state) error {
		if !budgetDispatchCurrent(s, dispatch) {
			t.Fatal("unchanged budget rejected its own reservation")
		}
		return nil
	}); err != nil {
		t.Fatal("read unchanged budget state:", err)
	}

	policy.Revision = "role-qualification-budget-drift"
	if err := f.a.ConfigureBudget(f.owner, policy); err != nil {
		t.Fatal("configure changed budget revision:", err)
	}
	if err := f.a.transaction(false, false, func(s *state) error {
		if budgetDispatchCurrent(s, dispatch) {
			t.Fatal("old dispatch remained current after only the budget revision changed")
		}
		return nil
	}); err != nil {
		t.Fatal("read changed budget state:", err)
	}
}
