package application

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
)

func TestNativeSignedRoleScenarioAndReservationKeepExactPolicyProvenance(t *testing.T) {
	f := setupRoleContractBoundary(t)
	release, _, _, err := f.registry.Resolve("core", f.role.Name, time.Now().UTC())
	if err != nil {
		t.Fatal("resolve exact installed release:", err)
	}
	policyReferences := append([]rolebundle.Reference(nil), release.Content.References...)
	if len(policyReferences) != 3 || policyReferences[0].ID != "native-budget" || policyReferences[1].ID != "native-preflight" || policyReferences[2].ID != "native-swarm" {
		t.Fatalf("fixture release must pin current native budget, preflight, and swarm policies: %+v", policyReferences)
	}
	for _, scenarioID := range []string{"allowed", "forbidden", "missing-gate", "failed-gate", "exhausted-budget", "unknown-budget"} {
		result, err := rolebundle.EvaluateScenario(release, map[string]ed25519.PublicKey{"test-publisher": f.public}, scenarioID)
		if err != nil || !result.MatchesExpected {
			t.Fatalf("signed scenario %s: result=%+v err=%v", scenarioID, result, err)
		}
		if result.ReleaseVersion != release.Content.Version || result.ReleaseDigest != release.ContentDigest || result.RoleID != f.role.Name || !equalRoleReferences(result.PolicyReferences, policyReferences) {
			t.Fatalf("scenario %s lost exact signed release or policy identity: %+v", scenarioID, result)
		}
	}
	if len(f.dispatch.RoleRegistry) != 1 {
		t.Fatalf("reservation has %d role provenance entries, want exactly one: %+v", len(f.dispatch.RoleRegistry), f.dispatch.RoleRegistry)
	}
	if release.Content.Version != "2.0.0" || release.ContentDigest != f.dispatch.RoleRegistry[0].ContentDigest || f.dispatch.RoleRegistry[0].Version != release.Content.Version || !equalSessionPolicyReferences(f.dispatch.RoleRegistry[0].PolicyReferences, policyReferences) {
		t.Fatalf("reserved run lost exact signed policy references: %+v", f.dispatch.RoleRegistry)
	}

	// The fixture has completed public PlanSwarm and ReserveWave. Reopen the
	// schema-20 authority and cross the public dispatch admission boundary to
	// read the persisted reservation. Scenario labels above are declarative
	// fixture outputs and do not assert host filesystem or tool interception.
	reopened, err := sessions.OpenNativeRoleRegistry(f.path, f.binding, f.admission)
	if err != nil {
		t.Fatal("reopen authority:", err)
	}
	persisted, err := reopened.AdmitDispatch(f.controllerToken, f.controller.ID, sessions.AdmitDispatchRequest{DispatchID: f.dispatch.ID, ExpectedFence: f.dispatch.Reservation.Fence})
	if err != nil {
		t.Fatal("admit persisted dispatch:", err)
	}
	if persisted.ID != f.dispatch.ID || persisted.Reservation.ID != f.dispatch.Reservation.ID || len(persisted.RoleRegistry) != 1 || persisted.RoleRegistry[0].Version != release.Content.Version || persisted.RoleRegistry[0].ContentDigest != release.ContentDigest || !equalSessionPolicyReferences(persisted.RoleRegistry[0].PolicyReferences, policyReferences) {
		t.Fatalf("reopened dispatch lost exact role registry evidence: %+v", persisted)
	}
	if release.Content.Contract == nil {
		t.Fatal("installed release omitted its signed contract")
	}
	if err := installBoundaryRelease(t, f.w, f.registry, f.role, *release.Content.Contract, "2.0.1", f.private, time.Now().UTC().Truncate(time.Second), true, policyReferences...); err != nil {
		t.Fatal("install later signed role upgrade:", err)
	}
	afterUpgrade, err := sessions.OpenNativeRoleRegistry(f.path, f.binding, f.admission)
	if err != nil {
		t.Fatal("reopen authority after role upgrade:", err)
	}
	storage, err := afterUpgrade.ObserveStorage(f.owner)
	if err != nil {
		t.Fatal("observe persisted reservation after role upgrade:", err)
	}
	var persistedState struct {
		Dispatches map[string]sessions.DispatchReservation
	}
	if err := json.Unmarshal(storage.PrivateBytes, &persistedState); err != nil {
		t.Fatal("decode reopened authority snapshot:", err)
	}
	retained, ok := persistedState.Dispatches[f.dispatch.ID]
	if !ok || retained.ID != f.dispatch.ID || len(retained.RoleRegistry) != 1 || retained.RoleRegistry[0].Version != release.Content.Version || retained.RoleRegistry[0].ContentDigest != release.ContentDigest || !equalSessionPolicyReferences(retained.RoleRegistry[0].PolicyReferences, policyReferences) {
		t.Fatalf("later role upgrade rewrote or dropped original dispatch provenance: %+v", retained)
	}

}

func TestNativeRoleBudgetDriftRefusesIntentAndHandoffWithPositiveControls(t *testing.T) {
	for _, boundary := range []string{"intent", "handoff"} {
		t.Run(boundary, func(t *testing.T) {
			control := setupRoleContractBoundary(t)
			if err := exerciseRoleBudgetBoundary(&control, boundary, "role-budget-positive"); err != nil {
				t.Fatalf("unchanged budget positive control: %v", err)
			}

			f := setupRoleContractBoundary(t)
			if len(f.dispatch.RoleRegistry) != 1 || f.dispatch.RoleRegistry[0].Version != "2.0.0" {
				t.Fatalf("negative case must begin with the same selected role release: %+v", f.dispatch.RoleRegistry)
			}
			changedBudget := f.budgetPolicy
			changedBudget.Revision = "role-contract-budget-drift"
			if err := f.a.ConfigureBudget(f.owner, changedBudget); err != nil {
				t.Fatal("configure only changed budget revision:", err)
			}
			if boundary == "intent" {
				intent, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, roleBudgetIntentRequest(&f, "role-budget-positive"))
				if err == nil || intent.HostActionAuthorized || intent.Intent.ID != "" {
					t.Fatalf("changed budget issued host intent: %+v err=%v", intent, err)
				}
				return
			}
			lease, err := f.a.HandoffDispatch(f.workerToken, f.worker.ID, roleBudgetHandoffRequest(&f, "role-budget-positive"))
			if !errors.Is(err, sessions.ErrDenied) || lease.ID != "" {
				t.Fatalf("changed budget issued worker lease: %+v err=%v", lease, err)
			}
		})
	}
}

func exerciseRoleBudgetBoundary(f *roleContractBoundaryFixture, boundary, operationKey string) error {
	switch boundary {
	case "intent":
		result, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, roleBudgetIntentRequest(f, operationKey))
		if err != nil || !result.HostActionAuthorized || result.Intent.ID == "" {
			return errors.New("unchanged budget refused intent")
		}
	case "handoff":
		lease, err := f.a.HandoffDispatch(f.workerToken, f.worker.ID, roleBudgetHandoffRequest(f, operationKey))
		if err != nil || lease.ID == "" {
			return errors.New("unchanged budget refused handoff")
		}
	default:
		return errors.New("unknown role budget boundary")
	}
	return nil
}

func roleBudgetIntentRequest(f *roleContractBoundaryFixture, operationKey string) sessions.RequestDispatchIntent {
	return sessions.RequestDispatchIntent{OperationKey: operationKey, DispatchID: f.dispatch.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: f.dispatch.Reservation.Fence}
}

func roleBudgetHandoffRequest(f *roleContractBoundaryFixture, operationKey string) sessions.HandoffDispatchRequest {
	return sessions.HandoffDispatchRequest{OperationKey: operationKey, DispatchID: f.dispatch.ID, AttemptID: f.attempt.ID, ExpectedFence: f.dispatch.Reservation.Fence}
}

func equalRoleReferences(left, right []rolebundle.Reference) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalSessionPolicyReferences(left []sessions.RolePolicyReference, right []rolebundle.Reference) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Kind != right[i].ID || left[i].Version != right[i].Version || left[i].Digest != right[i].Digest {
			return false
		}
	}
	return true
}
