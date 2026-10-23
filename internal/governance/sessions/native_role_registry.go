package sessions

import (
	"reflect"
	"time"
)

// RolePolicyDigest returns the domain-separated digest used by signed role
// bundles to bind one exact current native owner policy revision.
func RolePolicyDigest(kind string, policy any) string {
	return "sha256:" + identity("dacli-role-policy/"+kind+"/v1", "", policy)
}

func resolveRolePolicyReferences(s *state, project string, roles []RoleRegistryProvenance, now time.Time) error {
	for _, role := range roles {
		seen := map[string]bool{}
		for _, ref := range role.PolicyReferences {
			if ref.Version == "" || ref.Digest == "" || seen[ref.Kind] {
				return ErrDenied
			}
			seen[ref.Kind] = true
			var revision, digest string
			switch ref.Kind {
			case "native-swarm":
				policy, ok := s.SwarmPolicies[project]
				if !ok || policy.Stopped || policy.TTL <= 0 {
					return ErrDenied
				}
				revision, digest = policy.Revision, RolePolicyDigest(ref.Kind, policy)
			case "native-preflight":
				policy, ok := s.PreflightPolicies[project]
				if !ok || !now.Before(policy.ExpiresAt) {
					return ErrDenied
				}
				revision, digest = policy.Revision, RolePolicyDigest(ref.Kind, policy)
			case "native-budget":
				policy, ok := s.BudgetPolicies[project]
				if !ok || !now.Before(policy.ExpiresAt) {
					return ErrDenied
				}
				revision, digest = policy.Revision, RolePolicyDigest(ref.Kind, policy)
			case "native-model-routing":
				policy, ok := s.ModelRoutingPolicies[project]
				if !ok || !now.Before(policy.ExpiresAt) {
					return ErrDenied
				}
				revision, digest = policy.Revision, RolePolicyDigest(ref.Kind, policy)
			default:
				return ErrDenied
			}
			if revision != ref.Version || digest != ref.Digest {
				return ErrDenied
			}
		}
	}
	return nil
}

// currentRoleRegistryMatches is the final authorization check before an
// external dispatch effect. It repeats the observation after capability
// callbacks, which may take time or change the selected local pin.
func (a *Authority) currentRoleRegistryMatches(s *state, dispatch DispatchReservation) error {
	observer, ok := a.admission.(SwarmAdmission)
	if !ok {
		return ErrDenied
	}
	snapshot, err := observer.ObserveSwarm(s.Binding.Workspace, dispatch.ProjectID)
	if err != nil || !reflect.DeepEqual(dispatch.RoleRegistry, snapshot.RoleRegistry) {
		return ErrDenied
	}
	now := time.Now().UTC()
	if resolveRolePolicyReferences(s, dispatch.ProjectID, snapshot.RoleRegistry, now) != nil {
		return ErrDenied
	}
	for _, role := range snapshot.RoleRegistry {
		if role.LifecycleExpiresAt.IsZero() || !now.Before(role.LifecycleExpiresAt) {
			return ErrDenied
		}
	}
	return nil
}
