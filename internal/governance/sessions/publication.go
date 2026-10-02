package sessions

import (
	"math"
	"reflect"
	"regexp"
	"sort"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/policy"
	"github.com/mlnomadpy/dacli/internal/publication"
)

// PublicationPolicy is owner-selected disclosure policy, not task status or
// evidence that the remote repository has the asserted visibility.
type PublicationPolicy struct {
	TaskID, Repository, Visibility    string
	IncludeInternal, RecordedInternal bool
	Generation                        uint64
	Digest                            string
}
type SetPublicationPolicyRequest struct {
	TaskID, Repository, Visibility    string
	IncludeInternal, RecordedInternal bool
	ExpectedGeneration                uint64
}
type PublicationAction struct {
	Action                           policy.Action
	Repository, Issue, PayloadSHA256 string
	Fields                           []publication.Field
}
type PublicationDecisionRequest struct {
	OperationKey, AttemptID, LeaseID, RevisionID string
	ExpectedFence                                uint64
	ExpiresAt                                    time.Time
	Action                                       PublicationAction
}

// PublicationDecision is an immutable assessment, never a bearer permission.
type PublicationDecision struct {
	ID, SessionID string
	Request       PublicationDecisionRequest
	Policy        PublicationPolicy
	Revision      AttemptRevision
	Assessment    policy.Assessment
}
type ConsumePublicationRequest struct {
	OperationKey, DecisionID string
	Action                   PublicationAction
}

// PublicationConsumption only records a completed local permission transition.
// Historical is always true: even a newly returned receipt cannot publish.
type PublicationConsumption struct {
	DecisionID, OperationKey, ActionDigest, Assurance string
	ConsumedAt                                        time.Time
	Historical                                        bool
}

var publicationRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func initializePublication(s *state) {
	s.PublicationPolicies = map[string][]PublicationPolicy{}
	s.PublicationDecisions = map[string]PublicationDecision{}
	s.PublicationConsumed = map[string]PublicationConsumption{}
}

// UpgradePublication is an explicit owner-authorized schema-5 migration.
func (a *Authority) UpgradePublication(owner string) error {
	old := *a
	old.version = 5
	return old.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		initializePublication(s)
		s.Version = 6
		return nil
	})
}
func (a *Authority) SetPublicationPolicy(owner string, req SetPublicationPolicyRequest) (PublicationPolicy, error) {
	var result PublicationPolicy
	err := a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		if !validID(req.TaskID) || !publicationRepository.MatchString(req.Repository) || (req.Visibility != "public" && req.Visibility != "private" && req.Visibility != "unknown") {
			return ErrDenied
		}
		if _, ok := s.Tasks[req.TaskID]; !ok {
			return ErrDenied
		}
		history := s.PublicationPolicies[req.TaskID]
		if uint64(len(history)) != req.ExpectedGeneration || req.ExpectedGeneration == math.MaxUint64 {
			return ErrDenied
		}
		result = PublicationPolicy{TaskID: req.TaskID, Repository: req.Repository, Visibility: req.Visibility, IncludeInternal: req.IncludeInternal, RecordedInternal: req.RecordedInternal, Generation: req.ExpectedGeneration + 1}
		result.Digest = identity("publication-policy/v1", "", result)
		s.PublicationPolicies[req.TaskID] = append(history, result)
		return nil
	})
	if err != nil {
		return PublicationPolicy{}, err
	}
	return result, nil
}
func normalizePublicationAction(in PublicationAction) (PublicationAction, error) {
	if len(in.Fields) == 0 || len(in.Fields) > 32 {
		return PublicationAction{}, ErrDenied
	}
	in.Fields = append([]publication.Field(nil), in.Fields...)
	sort.Slice(in.Fields, func(i, j int) bool { return in.Fields[i] < in.Fields[j] })
	fields := in.Fields[:0]
	for _, f := range in.Fields {
		if len(fields) == 0 || fields[len(fields)-1] != f {
			fields = append(fields, f)
		}
	}
	in.Fields = fields
	return in, nil
}

// publicationCurrent never accepts a cached authenticated session. Owner callers
// authenticate separately; worker ancestry and scope are reread under this lock.
func publicationCurrent(s *state, session string, req PublicationDecisionRequest) (Attempt, AttemptRevision, PublicationPolicy, error) {
	r, _, err := rootSession(s, session, time.Now())
	if err != nil {
		return Attempt{}, AttemptRevision{}, PublicationPolicy{}, ErrDenied
	}
	at, err := attemptFor(s, req.AttemptID, session)
	if err != nil {
		return Attempt{}, AttemptRevision{}, PublicationPolicy{}, ErrDenied
	}
	b, ok := s.Tasks[at.Request.TaskID]
	l, lok := s.Leases[req.LeaseID]
	history := s.PublicationPolicies[at.Request.TaskID]
	if !ok || !b.AdmissionEnabled || b.Repository != at.Observation.Repository || b.ProjectID != at.ProjectID || len(history) == 0 || !lok || l.SessionID != session || l.AttemptID != at.ID || l.Fence != req.ExpectedFence || !currentLease(l, time.Now()) || !time.Now().Before(req.ExpiresAt) {
		return Attempt{}, AttemptRevision{}, PublicationPolicy{}, ErrDenied
	}
	p := history[len(history)-1]
	if !subset([]string{"publication.evaluate"}, r.Scope.Actions) || !subset([]string{"task:" + at.Request.TaskID, "repository:" + p.Repository}, r.Scope.Resources) || req.Action.Repository != p.Repository {
		return Attempt{}, AttemptRevision{}, PublicationPolicy{}, ErrDenied
	}
	rev, err := currentRevision(s, at, req.RevisionID, req.LeaseID, req.ExpectedFence)
	if err != nil {
		return Attempt{}, AttemptRevision{}, PublicationPolicy{}, ErrDenied
	}
	return at, rev, p, nil
}
func (a *Authority) EvaluatePublication(token, session string, req PublicationDecisionRequest) (PublicationDecision, error) {
	var result PublicationDecision
	action, err := normalizePublicationAction(req.Action)
	if err != nil {
		return result, err
	}
	req.Action = action
	req.ExpiresAt = req.ExpiresAt.UTC()
	err = a.transaction(false, true, func(s *state) error {
		if _, err := authenticate(s, token, session, time.Now()); err != nil {
			return err
		}
		if !validID(req.OperationKey) {
			return ErrDenied
		}
		digest := identity("publication-decision/v1", session, req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest || op.PublicationDecision == nil {
				return ErrDenied
			}
			result = *op.PublicationDecision
			return nil
		}
		at, rev, p, err := publicationCurrent(s, session, req)
		if err != nil {
			return err
		}
		if a.admission == nil || a.admission.AdmitLease(s.Binding.Workspace, at.ProjectID, at.Request.TaskID) != nil {
			return ErrDenied
		}
		if err = observeRevision(at, rev); err != nil {
			return err
		}
		if _, _, _, err = publicationCurrent(s, session, req); err != nil {
			return err
		}
		now := time.Now().UTC()
		if !now.Before(req.ExpiresAt) {
			return ErrDenied
		}
		assessment := policy.EvaluatePublication(policy.Observation{ActorID: session, SessionID: session, AttemptID: at.ID, WorkspaceID: a.binding.Workspace, InstallationID: a.binding.Installation, AuthorityID: identity("publication-authority/v1", "", struct {
			Binding Binding
			Path    string
		}{a.binding, a.path}), Repository: p.Repository, Resource: req.Action.Issue, Action: req.Action.Action, Fields: req.Action.Fields, ProjectedPayloadSHA256: req.Action.PayloadSHA256, PolicyRevision: p.Digest, LeaseID: req.LeaseID, LeaseGeneration: req.ExpectedFence, NotBefore: now, ObservedAt: now, ExpiresAt: req.ExpiresAt, Visibility: p.Visibility, IncludeInternal: p.IncludeInternal, RecordedInternal: p.RecordedInternal})
		// The existing evaluator supports create too; this transport kernel only update.
		if req.Action.Action != policy.IssueUpdate {
			assessment.Verdict = policy.Deny
			assessment.Reasons = append(assessment.Reasons, policy.Reason{Code: policy.UnsupportedAction, Detail: "native decisions support issue-update only"})
		}
		assessment.Assurance = "cooperative-session"
		assessment.ActionDigest = identity("native-publication-decision/v1", session, struct {
			Binding  policy.Observation
			Revision AttemptRevision
		}{assessment.Binding, rev})
		id, err := mint()
		if err != nil {
			return err
		}
		result = PublicationDecision{id, session, req, p, rev, assessment}
		s.PublicationDecisions[id] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, PublicationDecision: &result}
		return nil
	})
	if err != nil {
		return PublicationDecision{}, err
	}
	return result, nil
}
func (a *Authority) ConsumePublication(owner string, req ConsumePublicationRequest) (PublicationConsumption, error) {
	var result PublicationConsumption
	action, err := normalizePublicationAction(req.Action)
	if err != nil {
		return result, err
	}
	req.Action = action
	err = a.transaction(false, true, func(s *state) error {
		if len(owner) != 64 || hash(owner) != s.OwnerHash {
			return ErrDenied
		}
		if !validID(req.OperationKey) || !validID(req.DecisionID) {
			return ErrDenied
		}
		digest := identity("publication-consumption/v1", "owner", req)
		if op, ok := s.Operations[req.OperationKey]; ok {
			if op.Digest != digest || op.PublicationConsumption == nil {
				return ErrDenied
			}
			result = *op.PublicationConsumption
			return nil
		}
		d, ok := s.PublicationDecisions[req.DecisionID]
		if !ok || d.Assessment.Verdict != policy.Allow || d.Request.Action.Action != policy.IssueUpdate || !reflect.DeepEqual(req.Action, d.Request.Action) {
			return ErrDenied
		}
		if _, ok := s.PublicationConsumed[d.ID]; ok {
			return ErrDenied
		}
		at, rev, p, err := publicationCurrent(s, d.SessionID, d.Request)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(p, d.Policy) || !reflect.DeepEqual(rev, d.Revision) {
			return ErrDenied
		}
		if a.admission == nil || a.admission.AdmitLease(s.Binding.Workspace, at.ProjectID, at.Request.TaskID) != nil {
			return ErrDenied
		}
		if err = observeRevision(at, rev); err != nil {
			return err
		}
		if _, _, _, err = publicationCurrent(s, d.SessionID, d.Request); err != nil {
			return err
		}
		result = PublicationConsumption{DecisionID: d.ID, OperationKey: req.OperationKey, ActionDigest: d.Assessment.ActionDigest, Assurance: "cooperative-session", ConsumedAt: time.Now().UTC(), Historical: true}
		s.PublicationConsumed[d.ID] = result
		s.Operations[req.OperationKey] = operation{Digest: digest, PublicationConsumption: &result}
		return nil
	})
	if err != nil {
		return PublicationConsumption{}, err
	}
	return result, nil
}
