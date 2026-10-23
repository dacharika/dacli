package sessions

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/team"
)

// ErrPreflightTransient permits only a bounded preflight retry, never a host launch.
var ErrPreflightTransient = errors.New("native complete-loop preflight transient failure")

// PreflightTask selects private inputs at the owner boundary. Controllers cannot
// substitute profiles, credentials or roles through a dispatch request.
type PreflightTask struct {
	ImplementationRole, ReviewRole, RecoveryRole                                                         string
	FallbackRoles                                                                                        []string
	WorkerConnection, ReviewerProfile, RunnerProfile, ContractProfile                                    string
	RequireReview, RequireVerification, RequireIntegration, RequireEnforcedTokens, RequireEnforcedBudget bool
	OwnerAuthorizedIntegration                                                                           bool
}
type PreflightPolicy struct {
	SchemaVersion                           int
	ProjectID, Revision, Harness, Rationale string
	AcceptManualAssurance                   bool
	ExpiresAt                               time.Time
	TransientRetries                        int
	Tasks                                   map[string]PreflightTask
}
type PreflightActor struct {
	SessionID, GrantID string
	CredentialHash     string `json:"-"`
}
type PreflightPhase struct {
	Phase, Verdict, Assurance, EvidenceDigest, Remediation string
	Required                                               bool
}

// RequiredVerificationSet contains sanitized producer bindings, never argv or private paths.
type RequiredVerificationSet struct {
	PolicyDigest, PolicyBytesDigest, BaseCommit, RevisionID, RevisionCommit, RevisionTree, PlanDigest string
	Checks                                                                                            []RequiredCheck
	GrantIDs                                                                                          []string
}
type RequiredVerificationObserver interface {
	ObserveRequiredVerification(workspace, project, task string, revision AttemptRevision) (*RequiredVerificationSet, error)
}
type PreflightObservation struct {
	RequiredSet       *RequiredVerificationSet `json:",omitempty"`
	AvailableChecks   []RequiredCheck          `json:",omitempty"`
	AvailableGrantIDs []string                 `json:",omitempty"`

	ReviewerRepository, RunnerRepository string

	TaskDigest, RosterDigest, ClaimsDigest, ProfileDigest, Harness string
	AuthorityPath, InstallationID                                  string
	ClaimResources                                                 []string
	VerificationGrantIDs                                           []string
	RoleRegistry                                                   []RoleRegistryProvenance
	Required                                                       float64
	Sized                                                          bool
	Implementation, Review, Recovery                               team.Role
	Fallback                                                       []team.Role
	Worker, Reviewer, Runner                                       PreflightActor
	ReviewObservation                                              ReviewObservation
	Checks                                                         []RequiredCheck
	Phases                                                         []PreflightPhase
	ExpiresAt                                                      time.Time
}

// RoleRegistryProvenance is durable proof of the exact signed role release
// and current lifecycle/pin generations admitted for this project.
type RoleRegistryProvenance struct {
	RoleID, Version, ContentDigest, PublisherKeyID, LifecycleDigest string
	LifecycleExpiresAt                                              time.Time
	LifecycleGeneration, PinGeneration, ProjectGeneration           uint64
	PolicyReferences                                                []RolePolicyReference `json:",omitempty"`
}
type RolePolicyReference struct {
	Kind, Version, Digest string
}

// PreflightObserver performs bounded pure observations only: no authentication,
// authority transactions, model turns or host launches inside this callback.
type PreflightObserver interface {
	ObservePreflight(workspace, project, task string, spec PreflightTask, placement SwarmPlacement) (PreflightObservation, error)
}
type PreflightRoleCapacity struct {
	Phase, RoleID, RoleDigest, OverrideID string
	Capacity                              team.TaskCapacityVerdict
}
type PreflightReport struct {
	RequiredSet                                                                                                                           *RequiredVerificationSet `json:",omitempty"`
	RoutingPolicyDigest                                                                                                                   string                   `json:",omitempty"`
	RoutingRole                                                                                                                           string                   `json:",omitempty"`
	AuthorityDigest                                                                                                                       string
	RoleCapacities                                                                                                                        []PreflightRoleCapacity
	RoleRegistry                                                                                                                          []RoleRegistryProvenance `json:",omitempty"`
	SchemaVersion                                                                                                                         int
	Workspace, ProjectID, TaskID, PolicyRevision, Harness, TaskDigest, RoleDigest, RosterDigest, ClaimsDigest, ProfileDigest, ProofDigest string
	Capacity                                                                                                                              team.TaskCapacityVerdict
	OverrideID                                                                                                                            string
	Allowed                                                                                                                               bool
	FailureClass                                                                                                                          string
	Phases                                                                                                                                []PreflightPhase
	ObservedAt, ExpiresAt                                                                                                                 time.Time
}
type CapacityOverride struct {
	ID, Workspace, ProjectID, TaskID, PolicyRevision, Harness, TaskDigest, RoleDigest, RoleID, Reason, Actor string
	Required, Delta                                                                                          float64
	ExpiresAt                                                                                                time.Time
}
type CapacityOverrideRequest struct {
	ID, ProjectID, TaskID, RoleID, Reason string
	ExpiresAt                             time.Time
}
type PreflightReceipt struct {
	RequestDigest     string
	Report            PreflightReport
	TransientAttempts int
}

func (a *Authority) UpgradePreflight(owner string) error {
	old := *a
	old.version = 10
	return old.transaction(false, true, func(s *state) error {
		if s.Version != 10 || len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		s.PreflightPolicies = map[string]PreflightPolicy{}
		s.PreflightPolicyHistory = map[string]map[string]PreflightPolicy{}
		s.CapacityOverrides = map[string]CapacityOverride{}
		s.PreflightOperations = map[string]PreflightReceipt{}
		s.PreflightRetryBudgets = map[string]int{}
		s.Version = 11
		return nil
	})
}
func validPreflightPolicy(p PreflightPolicy) bool {
	if p.SchemaVersion != 1 || !validID(p.ProjectID) || !validID(p.Revision) || p.Harness != "codex" || strings.TrimSpace(p.Rationale) == "" || len(p.Rationale) > 8192 || !time.Now().Before(p.ExpiresAt) || p.ExpiresAt.After(time.Now().Add(24*time.Hour)) || p.TransientRetries < 0 || p.TransientRetries > 3 || len(p.Tasks) == 0 || len(p.Tasks) > 256 {
		return false
	}
	for id, t := range p.Tasks {
		if !validID(id) || !validID(t.ImplementationRole) || !validID(t.ReviewRole) || !validID(t.RecoveryRole) || !filepath.IsAbs(t.WorkerConnection) || !filepath.IsAbs(t.ContractProfile) || t.RequireReview && !filepath.IsAbs(t.ReviewerProfile) || (t.RequireVerification || t.RequireReview) && !filepath.IsAbs(t.RunnerProfile) || t.RequireIntegration && !t.OwnerAuthorizedIntegration {
			return false
		}
		for _, r := range t.FallbackRoles {
			if !validID(r) {
				return false
			}
		}
	}
	return true
}
func (a *Authority) ConfigurePreflight(owner string, p PreflightPolicy) error {
	p.ExpiresAt = p.ExpiresAt.UTC()
	return a.transaction(false, true, func(s *state) error {
		if s.Version < 11 || len(owner) != 64 || hash(owner) != s.OwnerHash || !validPreflightPolicy(p) {
			return ErrDenied
		}
		swarm, ok := s.SwarmPolicies[p.ProjectID]
		if !ok || swarm.Harness != p.Harness {
			return ErrDenied
		}
		for task := range p.Tasks {
			if _, ok := swarm.Placements[task]; !ok {
				return ErrDenied
			}
		}
		if old, ok := s.PreflightPolicyHistory[p.ProjectID][p.Revision]; ok {
			if !reflect.DeepEqual(old, p) {
				return ErrDenied
			}
			return nil
		}
		if s.PreflightPolicyHistory[p.ProjectID] == nil {
			s.PreflightPolicyHistory[p.ProjectID] = map[string]PreflightPolicy{}
		}
		s.PreflightPolicyHistory[p.ProjectID][p.Revision] = p
		s.PreflightPolicies[p.ProjectID] = p
		return nil
	})
}
func finiteNonnegative(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }
func roleDigest(r team.Role) string    { return identity("preflight.role", "", r) }
func (a *Authority) preflight(s *state, project, task, impl, review, worker string) (PreflightReport, error) {
	p := s.PreflightPolicies[project]
	spec, present := routingPreflightSpec(s, project, task, impl)
	return a.preflightSpec(s, project, task, impl, review, worker, p, spec, present)
}
func (a *Authority) preflightSpec(s *state, project, task, impl, review, worker string, p PreflightPolicy, spec PreflightTask, present bool) (PreflightReport, error) {
	_, ok := s.PreflightPolicies[project]
	report := PreflightReport{SchemaVersion: 1, Workspace: s.Binding.Workspace, ProjectID: project, TaskID: task, PolicyRevision: p.Revision, Harness: p.Harness, ObservedAt: time.Now().UTC(), ExpiresAt: p.ExpiresAt.UTC(), Phases: []PreflightPhase{}}
	if _, enabled := s.ModelRoutingPolicies[project]; enabled {
		report.RoutingRole = impl
		p := s.ModelRoutingPolicies[project]
		report.RoutingPolicyDigest = identity("model.routing.policy", "", p)
		if p.ExpiresAt.Before(report.ExpiresAt) {
			report.ExpiresAt = p.ExpiresAt
		}
		for _, candidate := range p.Tasks[task].Candidates {
			if candidate.RoleID == impl && candidate.ExpiresAt.Before(report.ExpiresAt) {
				report.ExpiresAt = candidate.ExpiresAt
			}
		}
	}
	fail := func(phase, reason, remedy string) {
		report.Phases = append(report.Phases, PreflightPhase{Phase: phase, Verdict: reason, Required: true, Assurance: "cooperative-current-authority", Remediation: remedy})
	}
	if _, retired := s.TaskTerminals[task]; retired {
		fail("policy", "policy_refused", "task execution retired by native acceptance; retained resources require separate owner cleanup")
		report.FailureClass = "permanent"
		completePreflightPhases(&report)
		report.ProofDigest = preflightDigest(report)
		return report, nil
	}
	observer, available := a.admission.(PreflightObserver)
	if s.Version < 11 || !ok || !present || !time.Now().Before(p.ExpiresAt) || !available {
		fail("policy", "policy_refused", "owner must configure a fresh complete-loop preflight policy")
		report.FailureClass = "permanent"
		completePreflightPhases(&report)
		report.ProofDigest = preflightDigest(report)
		return report, nil
	}
	obs, e := observer.ObservePreflight(s.Binding.Workspace, project, task, spec, s.SwarmPolicies[project].Placements[task])
	if e != nil {
		fail("observation", "policy_refused", "canonical or owner-selected profile observation unavailable; reconcile instead of unchanged retry")
		report.FailureClass = "permanent"
		completePreflightPhases(&report)
		report.ProofDigest = preflightDigest(report)
		return report, nil //nolint:nilerr // Observation failures are durable typed refusal reports, not lost transaction errors.
	}
	if !obs.Sized {
		fail("implementation", "policy_refused", "canonical PERT estimate is required before normal assignment or native allocation")
	}
	if len(obs.RoleRegistry) > 0 && s.Version < 20 {
		fail("schema", "policy_refused", "owner must explicitly migrate the authority to schema 20 before using governed role pins")
		report.FailureClass = "permanent"
		completePreflightPhases(&report)
		report.ProofDigest = preflightDigest(report)
		return report, nil
	}
	report.RequiredSet = obs.RequiredSet
	report.TaskDigest = obs.TaskDigest
	report.RoleDigest = roleDigest(obs.Implementation)
	report.RosterDigest = obs.RosterDigest
	report.RoleRegistry = append([]RoleRegistryProvenance(nil), obs.RoleRegistry...)
	for _, provenance := range report.RoleRegistry {
		if provenance.LifecycleExpiresAt.IsZero() {
			fail("role-registry", "policy_refused", "refresh the signed lifecycle statement for the pinned role")
		} else if provenance.LifecycleExpiresAt.Before(report.ExpiresAt) {
			report.ExpiresAt = provenance.LifecycleExpiresAt.UTC()
		}
	}
	if resolveRolePolicyReferences(s, project, report.RoleRegistry, time.Now().UTC()) != nil {
		fail("role-policy", "policy_refused", "refresh or explicitly re-pin the exact native policy referenced by the signed role release")
	}
	report.ClaimsDigest = obs.ClaimsDigest
	report.ProfileDigest = obs.ProfileDigest
	if obs.ExpiresAt.Before(report.ExpiresAt) {
		report.ExpiresAt = obs.ExpiresAt.UTC()
	}
	report.Phases = append(report.Phases, obs.Phases...)
	if !time.Now().Before(report.ExpiresAt) || obs.AuthorityPath != a.path || obs.InstallationID != s.Binding.Installation || obs.Harness != p.Harness || p.Harness != "codex" || len(obs.TaskDigest) != 64 || len(obs.RosterDigest) != 64 || len(obs.ClaimsDigest) != 64 || len(obs.ProfileDigest) != 64 {
		fail("freshness", "policy_refused", "refresh owner-selected canonical and private-profile observations")
	}
	validRole := func(r team.Role, name string) bool {
		return r.Name == name && r.Runtime == p.Harness && r.ModelID() != "" && finiteNonnegative(r.MaxPoints) && finiteNonnegative(r.Profile.MaxTaskPoints) && r.Profile.ContextLimit >= 0
	}
	if !implementationCharter(obs.Implementation.Kind) || spec.RequireReview && !reviewCharter(obs.Review.Kind) || !implementationCharter(obs.Recovery.Kind) {
		fail("roles", "policy_refused", "canonical role charter must match implementation, review and recovery phases")
	}
	if !validRole(obs.Implementation, spec.ImplementationRole) || impl != spec.ImplementationRole || !validRole(obs.Review, spec.ReviewRole) || review != spec.ReviewRole || !validRole(obs.Recovery, spec.RecoveryRole) {
		fail("roles", "policy_refused", "resolve exact owner-selected Codex roster roles and models")
	}
	for i, r := range obs.Fallback {
		if i >= len(spec.FallbackRoles) || !implementationCharter(r.Kind) || !validRole(r, spec.FallbackRoles[i]) {
			fail("fallback", "policy_refused", "pin every fallback to the same Codex harness")
		}
	}
	if len(obs.Fallback) != len(spec.FallbackRoles) {
		fail("fallback", "policy_refused", "resolve the complete fallback chain")
	}
	evaluateCapacity := func(role team.Role, phase string) {
		capacity := team.TaskCapacity(role, obs.Required, obs.Sized)
		entry := PreflightRoleCapacity{Phase: phase, RoleID: role.Name, RoleDigest: roleDigest(role), Capacity: capacity}
		if !finiteNonnegative(obs.Required) || !finiteNonnegative(role.MaxPoints) || !finiteNonnegative(role.Profile.MaxTaskPoints) || !finiteNonnegative(capacity.Limit) {
			fail(phase, "policy_refused", "repair finite canonical task estimate and role task capacity")
			entry.Capacity = team.TaskCapacityVerdict{Fits: false, Sized: obs.Sized, Reason: "invalid nonfinite or negative capacity input"}
		} else if !capacity.Fits {
			ids := make([]string, 0, len(s.CapacityOverrides))
			for id := range s.CapacityOverrides {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool {
				a, b := s.CapacityOverrides[ids[i]], s.CapacityOverrides[ids[j]]
				if !a.ExpiresAt.Equal(b.ExpiresAt) {
					return a.ExpiresAt.After(b.ExpiresAt)
				}
				return a.ID < b.ID
			})
			for _, id := range ids {
				o := s.CapacityOverrides[id]
				if o.Workspace == s.Binding.Workspace && o.ProjectID == project && o.TaskID == task && o.PolicyRevision == p.Revision && o.Harness == p.Harness && o.TaskDigest == obs.TaskDigest && o.RoleID == role.Name && o.RoleDigest == entry.RoleDigest && o.Required == obs.Required && o.Delta == capacity.Delta && time.Now().Before(o.ExpiresAt) {
					entry.OverrideID = id
					if o.ExpiresAt.Before(report.ExpiresAt) {
						report.ExpiresAt = o.ExpiresAt.UTC()
					}
					break
				}
			}
			if entry.OverrideID == "" {
				fail(phase, "policy_refused", capacity.Reason+"; owner may record an exact role-specific expiring size-only override")
			}
		}
		report.RoleCapacities = append(report.RoleCapacities, entry)
		if phase == "implementation" {
			report.Capacity = entry.Capacity
			report.OverrideID = entry.OverrideID
		}
	}
	evaluateCapacity(obs.Implementation, "implementation")
	if spec.RequireReview {
		evaluateCapacity(obs.Review, "review")
	}
	evaluateCapacity(obs.Recovery, "recovery")
	for _, role := range obs.Fallback {
		evaluateCapacity(role, "fallback")
	}
	authorityFacts := []any{}
	bindExpiry := func(expires time.Time) {
		if expires.Before(report.ExpiresAt) {
			report.ExpiresAt = expires.UTC()
		}
	}
	currentActor := func(actor PreflightActor, actions []string) (record, string, bool) {
		r, root, e := rootSession(s, actor.SessionID, time.Now())
		authorityFacts = append(authorityFacts, r.Session)
		bindExpiry(r.ExpiresAt)
		return r, root, e == nil && r.Hash == actor.CredentialHash && subset(actions, r.Scope.Actions) && subset([]string{"task:" + task}, r.Scope.Resources) && !workerTaskWithdrawn(s, r.ID, task)
	}
	actions := []string{"attempt.register", "lease.acquire", "lease.renew", "lease.release"}
	if spec.RequireReview || spec.RequireVerification {
		actions = append(actions, "attempt.publish")
	}
	if spec.RequireVerification || spec.RequireReview {
		actions = append(actions, "verification.request")
	}
	wr, root, valid := currentActor(obs.Worker, actions)
	if !valid || !subset(append(append([]string{}, obs.ClaimResources...), "checkout:"+s.SwarmPolicies[project].Placements[task].Checkout), wr.Scope.Resources) || wr.Parent != "" || worker != "" && worker != wr.ID {
		fail("implementation", "policy_refused", "provision the exact current independent worker root and scoped credential")
	}
	if spec.RequireReview {
		rr, reviewRoot, valid := currentActor(obs.Reviewer, []string{"review.propose", "review.evaluate"})
		g, found := s.ReviewGrants[obs.Reviewer.GrantID]
		authorityFacts = append(authorityFacts, g)
		bindExpiry(g.ExpiresAt)
		_, independence := reviewIndependence(s, rr.ID, wr.ID, task, time.Now())
		if !valid || independence != nil || obs.ReviewerRepository != s.Tasks[task].Repository || rr.Parent != "" || reviewRoot == root || !reviewOnly(rr, task) || !found || g.Revoked || !time.Now().Before(g.ExpiresAt) || g.ReviewerSession != rr.ID || g.WorkerRoot != root || g.TaskID != task || g.Repository != s.Tasks[task].Repository || !samePreflightReviewPolicy(g.Observation, obs.ReviewObservation) {
			fail("review", "policy_refused", "owner must provision an independent reviewer, matching current grant, policy and private connection before implementation")
		}
	}
	if spec.RequireVerification || spec.RequireReview {
		if spec.RequireReview {
			reviewChecks := obs.ReviewObservation.Checks
			if obs.RequiredSet != nil {
				for _, selected := range obs.RequiredSet.Checks {
					found := false
					for _, allowed := range obs.ReviewObservation.Checks {
						if selected == allowed {
							found = true
						}
					}
					if !found {
						fail("verification", "policy_refused", "every routed required check must be in the full owner review policy")
					}
				}
				reviewChecks = obs.RequiredSet.Checks
			}
			for _, required := range reviewChecks {
				found := false
				for _, configured := range obs.Checks {
					if required == configured {
						found = true
					}
				}
				if !found {
					fail("verification", "policy_refused", "required reviewer checks must be present in the exact trusted runner profile")
				}
			}
		}
		rr, runnerRoot, valid := currentActor(obs.Runner, []string{"verification.run"})
		if !valid || obs.RunnerRepository != s.Tasks[task].Repository || rr.Parent != "" || runnerRoot == root || len(obs.Checks) == 0 || len(obs.Checks) != len(obs.VerificationGrantIDs) {
			fail("verification", "policy_refused", "provision a current trusted runner and bounded check profile")
		}
		for index, c := range obs.Checks {
			if index < len(obs.VerificationGrantIDs) {
				g := s.Grants[obs.VerificationGrantIDs[index]]
				authorityFacts = append(authorityFacts, g)
				bindExpiry(g.ExpiresAt)
			}
			found := false
			for _, g := range s.Grants {
				if index < len(obs.VerificationGrantIDs) && g.ID == obs.VerificationGrantIDs[index] && !g.Revoked && time.Now().Before(g.ExpiresAt) && g.RunnerSession == rr.ID && g.TaskID == task && g.Repository == s.Tasks[task].Repository && g.CheckID == c.ID && g.CommandDigest == c.CommandDigest {
					found = true
				}
			}
			if !found {
				fail("verification", "policy_refused", "provision exact current check grants before implementation")
			}
		}
	}
	report.AuthorityDigest = identity("preflight.authority", "", authorityFacts)
	if !time.Now().Before(report.ExpiresAt) {
		fail("freshness", "policy_refused", "owner policy, capability contract or size override expired during observation")
	}
	budget := project + ":" + p.Revision + ":" + task
	if s.PreflightRetryBudgets[budget] > p.TransientRetries {
		fail("retry", "policy_refused", "owner must reconcile the exhausted durable transient retry budget")
	}
	completePreflightPhases(&report)
	// A transient report is retryable only when no permanent phase is blocked.
	permanent, transient := false, false
	for _, f := range report.Phases {
		if f.Required && (f.Verdict != "ready" || f.Assurance == "owner-manual" && !p.AcceptManualAssurance) {
			if f.Verdict == "transient" {
				transient = true
			} else {
				permanent = true
			}
		}
	}
	report.Allowed = !permanent && !transient
	report.FailureClass = ""
	if permanent {
		report.FailureClass = "permanent"
	} else if transient {
		report.FailureClass = "transient"
	}
	completePreflightPhases(&report)
	report.ProofDigest = preflightDigest(report)
	return report, nil
}
func (a *Authority) OverrideCapacity(owner string, req CapacityOverrideRequest) (CapacityOverride, error) {
	var result CapacityOverride
	e := a.transaction(false, true, func(s *state) error {
		if s.Version < 11 || len(owner) != 64 || hash(owner) != s.OwnerHash || !validID(req.ID) || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 8192 || !time.Now().Before(req.ExpiresAt) {
			return ErrDenied
		}
		p := s.PreflightPolicies[req.ProjectID]
		spec := p.Tasks[req.TaskID]
		report, e := a.preflight(s, req.ProjectID, req.TaskID, spec.ImplementationRole, spec.ReviewRole, "")
		roleID := req.RoleID
		if roleID == "" {
			roleID = spec.ImplementationRole
		}
		var selected PreflightRoleCapacity
		for _, c := range report.RoleCapacities {
			if c.RoleID == roleID {
				selected = c
				break
			}
		}
		if e != nil || !time.Now().Before(req.ExpiresAt) || selected.RoleID == "" || selected.Capacity.Fits || selected.Capacity.Delta >= 0 || req.ExpiresAt.After(p.ExpiresAt) {
			return ErrDenied
		}
		result = CapacityOverride{ID: req.ID, Workspace: s.Binding.Workspace, ProjectID: req.ProjectID, TaskID: req.TaskID, PolicyRevision: p.Revision, Harness: p.Harness, TaskDigest: report.TaskDigest, RoleDigest: selected.RoleDigest, RoleID: roleID, Reason: req.Reason, Actor: "owner:" + s.Binding.Installation, Required: selected.Capacity.Required, Delta: selected.Capacity.Delta, ExpiresAt: req.ExpiresAt.UTC()}
		if old, ok := s.CapacityOverrides[req.ID]; ok {
			if !reflect.DeepEqual(old, result) {
				return ErrDenied
			}
			result = old
			return nil
		}
		s.CapacityOverrides[req.ID] = result
		return nil
	})
	if e != nil {
		return CapacityOverride{}, e
	}
	return result, nil
}
func (a *Authority) CheckPreflight(token, session, project, task, key string) (PreflightReceipt, error) {
	var result PreflightReceipt
	e := a.transaction(false, true, func(s *state) error {
		if actor, e := controller(s, token, session, project, "swarm.plan"); s.Version < 11 || e != nil || !validID(key) || !validID(task) || !subset([]string{"task:" + task}, actor.Scope.Resources) {
			return ErrDenied
		}
		digest := identity("preflight.check", session, struct{ Project, Task string }{project, task})
		if old, ok := s.PreflightOperations[key]; ok {
			if old.RequestDigest != digest {
				return ErrDenied
			}
			result = old
			return nil
		}
		p := s.PreflightPolicies[project]
		spec := p.Tasks[task]
		report, e := a.preflight(s, project, task, spec.ImplementationRole, spec.ReviewRole, "")
		if e != nil {
			return e
		}
		budget := project + ":" + p.Revision + ":" + task
		if report.FailureClass == "transient" {
			s.PreflightRetryBudgets[budget]++
			if s.PreflightRetryBudgets[budget] > p.TransientRetries {
				report.FailureClass = "permanent"
				report.Phases = append(report.Phases, PreflightPhase{Phase: "retry", Required: true, Verdict: "policy_refused", Assurance: "durable-bounded-policy", Remediation: "owner must reconcile exhausted transient retry budget"})
			}
		}
		if actor, e := controller(s, token, session, project, "swarm.plan"); e != nil || !subset([]string{"task:" + task}, actor.Scope.Resources) {
			return ErrDenied
		}
		report.ProofDigest = preflightDigest(report)
		result = PreflightReceipt{RequestDigest: digest, Report: report, TransientAttempts: s.PreflightRetryBudgets[budget]}
		s.PreflightOperations[key] = result
		return nil
	})
	if e != nil {
		return PreflightReceipt{}, e
	}
	return result, nil
}
func (a *Authority) dispatchPreflight(s *state, d DispatchReservation, worker string) error {
	if s.Version < 11 {
		return nil
	}
	report, e := a.preflight(s, d.ProjectID, d.Reservation.TaskID, d.ImplementationRole, d.ReviewRole, worker)
	if e != nil {
		return e
	}
	if !report.Allowed {
		return preflightReportError(report)
	}
	if d.PreflightDigest == "" || d.PreflightDigest != report.ProofDigest {
		return ErrDenied
	}
	if !reflect.DeepEqual(d.RoleRegistry, report.RoleRegistry) {
		return ErrDenied
	}
	return nil
}

func completePreflightPhases(r *PreflightReport) {
	for _, name := range []string{"implementation", "review", "verification", "integration", "recovery", "fallback", "tokens", "budget"} {
		seen := false
		for _, p := range r.Phases {
			if p.Phase == name {
				seen = true
			}
		}
		if !seen {
			r.Phases = append(r.Phases, PreflightPhase{Phase: name, Verdict: "not_evaluated", Required: true, Assurance: "unavailable", Remediation: "resolve preceding owner-policy or canonical-profile refusal"})
		}
	}
}
func preflightDigest(r PreflightReport) string {
	r.ObservedAt = time.Time{}
	r.ProofDigest = ""
	b, _ := json.Marshal(r)
	return hash(string(b))
}

func implementationCharter(kind string) bool { return kind == "implement" || kind == "implementer" }
func reviewCharter(kind string) bool         { return kind == "review" || kind == "reviewer" }

func preflightBoundCurrent(s *state, d DispatchReservation) bool {
	return modelRoutingCurrent(s, d) && (s.Version < 11 || d.PreflightDigest != "" && time.Now().Before(d.PreflightExpiresAt))
}

func preflightReportError(report PreflightReport) error {
	if report.FailureClass == "transient" {
		return ErrPreflightTransient
	}
	return ErrDenied
}
func preflightPlanError(fresh, old SwarmPlan) error {
	selected := map[string]bool{}
	for _, v := range old.Verdicts {
		if v.Selected {
			selected[v.TaskID] = true
		}
	}
	transient := false
	for _, v := range fresh.Verdicts {
		if len(selected) != 0 && !selected[v.TaskID] {
			continue
		}
		if !v.Selected {
			if v.Preflight == nil || v.Preflight.FailureClass != "transient" {
				return ErrDenied
			}
			transient = true
		}
	}
	if transient {
		return ErrPreflightTransient
	}
	return ErrDenied
}

func samePreflightReviewPolicy(grant, current ReviewObservation) bool {
	current.RequiredSet = nil
	return reflect.DeepEqual(grant, current)
}
func (o checkpointReviewObservation) ObserveRequiredVerification(workspace, project, task string, rev AttemptRevision) (*RequiredVerificationSet, error) {
	set := o.observation.RequiredSet
	if set == nil {
		return nil, nil
	}
	if set.RevisionCommit != rev.Observation.Commit || set.RevisionTree != rev.Observation.Tree || len(set.Checks) == 0 {
		return nil, ErrDenied
	}
	result := *set
	result.RevisionID = rev.ID
	return &result, nil
}
