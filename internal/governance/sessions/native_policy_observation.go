package sessions

import (
	"reflect"
	"time"
)

// NativePolicyObservation is diagnostic knowledge, never permission to perform
// an effect (dacli 577). It excludes prepared canonical bodies and private paths.
type NativePolicyObservation struct {
	ProjectID        string                    `json:"project"`
	TaskID           string                    `json:"task"`
	State            string                    `json:"state"`
	EffectAuthorized bool                      `json:"effect_authorized"`
	ObservedAt       time.Time                 `json:"observed_at"`
	CurrentPolicy    *NativePolicyView         `json:"current_policy"`
	Decision         *NativePolicyDecisionView `json:"decision"`
}
type NativePolicyView struct {
	Revision        string    `json:"revision"`
	Digest          string    `json:"digest"`
	Allow           bool      `json:"allow"`
	RequireApproval bool      `json:"require_approval"`
	Stopped         bool      `json:"stopped"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type NativePolicyDecisionView struct {
	ID                   string                       `json:"id"`
	ActionDigest         string                       `json:"action_digest"`
	PolicyDigest         string                       `json:"policy_digest"`
	Historical           bool                         `json:"historical"`
	MatchesCurrentPolicy bool                         `json:"matches_current_policy"`
	ExpiresAt            time.Time                    `json:"expires_at"`
	Approval             *NativePolicyApprovalView    `json:"approval"`
	Consumption          *NativePolicyConsumptionView `json:"consumption"`
	Outcome              *NativePolicyOutcomeView     `json:"outcome"`
}
type NativePolicyApprovalView struct {
	ExpiresAt time.Time `json:"expires_at"`
	Current   bool      `json:"current"`
}
type NativePolicyConsumptionView struct {
	OperationKey           string    `json:"operation_key"`
	ConsumedAt             time.Time `json:"consumed_at"`
	CanonicalOutcomeDigest string    `json:"canonical_outcome_digest"`
}
type NativePolicyOutcomeView struct {
	State                 string    `json:"state"`
	ExpectedOutcomeDigest string    `json:"expected_outcome_digest"`
	ObservedOutcomeDigest string    `json:"observed_outcome_digest"`
	ObservedAt            time.Time `json:"observed_at"`
}

// ObserveNativePolicy authenticates and narrows to one task in the same read
// transaction as projection. Old schemas remain readable without migration.
func (a *Authority) ObserveNativePolicy(token, session, project, task, decision string) (NativePolicyObservation, error) {
	var out NativePolicyObservation
	e := a.transaction(false, false, func(s *state) error {
		now := time.Now().UTC()
		actor, e := authenticate(s, token, session, now)
		if e != nil || !validID(project) || !validID(task) || !subset([]string{"read"}, actor.Scope.Actions) || !subset([]string{"workspace:" + a.binding.Workspace, "task:" + task}, actor.Scope.Resources) {
			return ErrDenied
		}
		binding, bound := s.Tasks[task]
		if !bound || binding.TaskID != task || binding.ProjectID != project {
			return ErrDenied
		}
		out = NativePolicyObservation{ProjectID: project, TaskID: task, State: "unavailable", ObservedAt: now}
		p, exists := s.NativePolicies[task]
		if exists {
			if p.TaskID != task || p.ProjectID != project {
				return ErrDenied
			}
			out.CurrentPolicy = &NativePolicyView{p.Revision, identity("native-policy", "", p), p.Allow, p.RequireApproval, p.Stopped, p.ExpiresAt}
			switch {
			case p.Stopped:
				out.State = "stopped"
			case !now.Before(p.ExpiresAt):
				out.State = "expired"
			case !p.Allow:
				out.State = "denied"
			case p.RequireApproval:
				out.State = "approval_required"
			default:
				out.State = "allowed"
			}
		}
		if decision == "" {
			return nil
		}
		d, ok := s.NativePolicyDecisions[decision]
		if !ok || d.ID != decision || d.Input.WorkspaceID != a.binding.Workspace || d.Input.ProjectID != project || d.Input.TaskID != task {
			return ErrDenied
		}
		view := &NativePolicyDecisionView{ID: d.ID, ActionDigest: d.ActionDigest, PolicyDigest: d.PolicyDigest, Historical: true, MatchesCurrentPolicy: exists && reflect.DeepEqual(p, d.Policy) && d.PolicyDigest == identity("native-policy", "", p), ExpiresAt: d.ExpiresAt}
		if ap, ok := s.NativePolicyApprovals[d.ID]; ok && ap.DecisionID == d.ID && ap.ActionDigest == d.ActionDigest {
			view.Approval = &NativePolicyApprovalView{ap.ExpiresAt, view.MatchesCurrentPolicy && now.Before(ap.ExpiresAt) && now.Before(d.ExpiresAt) && now.Before(p.ExpiresAt) && p.Allow && !p.Stopped}
		}
		if c, ok := s.NativePolicyConsumptions[d.ID]; ok && c.DecisionID == d.ID && c.ActionDigest == d.ActionDigest {
			view.Consumption = &NativePolicyConsumptionView{c.OperationKey, c.ConsumedAt, c.CanonicalOutcomeDigest}
			if v, ok := s.NativePolicyOutcomes[c.OperationKey]; ok && v.DecisionID == d.ID && v.OperationKey == c.OperationKey {
				view.Outcome = &NativePolicyOutcomeView{v.State, v.ExpectedOutcomeDigest, v.ObservedOutcomeDigest, v.ObservedAt}
			}
		}
		out.Decision = view
		return nil
	})
	if e != nil {
		return NativePolicyObservation{}, e
	}
	return out, nil
}
