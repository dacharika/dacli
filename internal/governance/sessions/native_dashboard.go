package sessions

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
)

const dashboardAuthorityLimit = 16 << 20
const dashboardRecordLimit = 512

// DashboardRecord is an explicit public historical DTO, never an authority record.
// No credential hash, command, profile path, prompt or transcript belongs here.
type DashboardRecord struct {
	Kind        string     `json:"kind"`
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	AttemptID   string     `json:"attempt_id,omitempty"`
	LeaseID     string     `json:"lease_id,omitempty"`
	RevisionID  string     `json:"revision_id,omitempty"`
	ReviewerID  string     `json:"reviewer_id,omitempty"`
	ActorID     string     `json:"actor_id,omitempty"`
	DecisionID  string     `json:"decision_id,omitempty"`
	EvidenceIDs []string   `json:"evidence_ids,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	Tree        string     `json:"tree,omitempty"`
	CheckID     string     `json:"check_id,omitempty"`
	Provenance  string     `json:"provenance,omitempty"`
	State       string     `json:"state"`
	Freshness   string     `json:"freshness"`
	ObservedAt  time.Time  `json:"observed_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Sequence    uint64     `json:"sequence,omitempty"`
	Fence       uint64     `json:"fence,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Claims      []string   `json:"claims,omitempty"`
	Eligibility string     `json:"eligibility,omitempty"`
}
type DashboardAttention struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	Category   string     `json:"category"`
	Summary    string     `json:"summary"`
	Freshness  string     `json:"freshness"`
	ObservedAt time.Time  `json:"observed_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}
type DashboardSpend struct {
	State             string   `json:"state"`
	Provenance        string   `json:"provenance"`
	Enforcement       string   `json:"enforcement"`
	Unit              string   `json:"unit"`
	EstimatedSpent    *int64   `json:"estimated_spent"`
	EstimatedReserved *int64   `json:"estimated_reserved"`
	EstimatedLimit    *int64   `json:"estimated_limit"`
	ProviderTokens    *int64   `json:"provider_tokens"`
	ProviderCost      *float64 `json:"provider_cost_usd"`
	UnknownRetained   bool     `json:"unknown_retained"`
}
type DashboardSnapshot struct {
	Schema           string               `json:"schema"`
	State            string               `json:"state"`
	Project          string               `json:"project"`
	ObservedAt       time.Time            `json:"observed_at"`
	AuthorityVersion int                  `json:"authority_version"`
	Records          []DashboardRecord    `json:"records"`
	Attention        []DashboardAttention `json:"attention"`
	Spend            DashboardSpend       `json:"spend"`
	Services         map[string]string    `json:"services"`
	Limited          bool                 `json:"limited"`
}

// ReadDashboard uses one descriptor-bounded immutable authority snapshot. It does
// not acquire/create a lock or invoke observers, producers, migrations or writes.
func (a *Authority) ReadDashboard(token, session, project, task string) (DashboardSnapshot, error) {
	out := DashboardSnapshot{Schema: "native-dashboard/v1", State: "ready", Project: project, ObservedAt: time.Now().UTC(), Records: []DashboardRecord{}, Attention: []DashboardAttention{}, Services: map[string]string{}, Spend: DashboardSpend{State: "unavailable", Provenance: "unavailable", Enforcement: "unavailable", Unit: "owner-estimated tokens"}}
	if !validID(project) || task != "" && !validID(task) {
		return DashboardSnapshot{}, ErrDenied
	}
	raw, e := nativeconfig.ReadPrivate(a.path, dashboardAuthorityLimit)
	if e != nil {
		return DashboardSnapshot{}, ErrStorage
	}
	s, e := decodeAuthorityState(raw, a.binding, 6)
	if e != nil {
		return DashboardSnapshot{}, e
	}
	actor, e := authenticate(&s, token, session, time.Now())
	if e != nil || !subset([]string{"read"}, actor.Scope.Actions) || !subset([]string{"workspace:" + s.Binding.Workspace}, actor.Scope.Resources) || task == "" && !subset([]string{"project:" + project}, actor.Scope.Resources) {
		return DashboardSnapshot{}, ErrDenied
	}
	allowed := map[string]bool{}
	for id, b := range s.Tasks {
		if b.ProjectID == project && (task == "" || id == task) && subset([]string{"task:" + id}, actor.Scope.Resources) {
			allowed[id] = true
		}
	}
	if len(allowed) == 0 || task != "" && !allowed[task] {
		return DashboardSnapshot{}, ErrDenied
	}
	out.AuthorityVersion = s.Version
	for name, version := range map[string]int{"sessions": 6, "lifecycle": 9, "preflight": 11, "budget": 13, "policy": 15, "controller": 16} {
		out.Services[name] = "supported"
		if s.Version < version {
			out.Services[name] = "unsupported"
		}
	}
	appendRecord := func(r DashboardRecord) { out.Records = append(out.Records, r) }
	attention := func(id, task, category, summary string, at time.Time, expiry *time.Time) {
		out.Attention = append(out.Attention, DashboardAttention{id, task, category, summary, dashboardFreshness(at, expiry, out.ObservedAt), at, expiry})
	}
	related := map[string]string{}
	relate := func(actor, task string) {
		if old, ok := related[actor]; !ok || task < old {
			related[actor] = task
		}
	}
	attempts := map[string]Attempt{}
	for _, at := range s.Attempts {
		if !allowed[at.Request.TaskID] || at.ProjectID != project {
			continue
		}
		attempts[at.ID] = at
		relate(at.SessionID, at.Request.TaskID)
		appendRecord(DashboardRecord{Kind: "attempt", ID: at.ID, TaskID: at.Request.TaskID, ActorID: at.SessionID, State: "registered", ObservedAt: at.RegisteredAt, Freshness: "historical"})
		for _, rev := range s.Revisions[at.ID] {
			if rev.AttemptID != at.ID || rev.SessionID != at.SessionID {
				continue
			}
			appendRecord(DashboardRecord{Kind: "revision", ID: rev.ID, TaskID: at.Request.TaskID, AttemptID: at.ID, LeaseID: rev.LeaseID, ActorID: rev.SessionID, Commit: rev.Observation.Commit, Tree: rev.Observation.Tree, Sequence: rev.Sequence, Fence: rev.Fence, State: "published", ObservedAt: rev.PublishedAt, Freshness: "historical"})
		}
	}
	for _, l := range s.Leases {
		if !allowed[l.TaskID] || s.Tasks[l.TaskID].ProjectID != project {
			continue
		}
		state := "retained"
		if l.Released {
			state = "released"
		}
		expiry := l.ExpiresAt
		claims := []string{}
		for _, r := range l.Resources {
			if r.Kind == "path" || r.Kind == "task" {
				claims = append(claims, r.Kind+":"+r.Path)
			}
		}
		relate(l.SessionID, l.TaskID)
		appendRecord(DashboardRecord{Kind: "lease", ID: l.ID, TaskID: l.TaskID, AttemptID: l.AttemptID, ActorID: l.SessionID, Fence: l.Fence, State: state, Freshness: dashboardFreshness(time.Time{}, &expiry, out.ObservedAt), ExpiresAt: &expiry, Claims: claims})
		if !l.Released && !out.ObservedAt.Before(l.ExpiresAt) {
			attention(l.ID, l.TaskID, "blocked", "Retained lease protects declared resources; expiry does not release it.", time.Time{}, &expiry)
		}
	}
	for _, ev := range s.Evidence {
		at, ok := attempts[ev.Revision.AttemptID]
		if !ok || ev.Revision.SessionID != at.SessionID {
			continue
		}
		relate(ev.IssuerSession, at.Request.TaskID)
		code := ev.ExitCode
		appendRecord(DashboardRecord{Kind: "evidence", ID: ev.ID, TaskID: at.Request.TaskID, AttemptID: at.ID, LeaseID: ev.Revision.LeaseID, RevisionID: ev.Revision.ID, ActorID: ev.IssuerSession, CheckID: ev.CheckID, Provenance: ev.Provenance, ExitCode: &code, State: "recorded", ObservedAt: ev.RecordedAt, Freshness: "historical", Eligibility: "historical-not-revalidated"})
	}
	for _, r := range s.Reviews {
		at, ok := attempts[r.Request.AttemptID]
		if !ok || r.Revision.AttemptID != at.ID {
			continue
		}
		ids := []string{}
		for _, id := range r.Request.EvidenceIDs {
			if ev, ok := s.Evidence[id]; ok && ev.Revision.AttemptID == at.ID && ev.Revision.ID == r.Revision.ID {
				ids = append(ids, id)
			}
		}
		relate(r.IssuerSession, at.Request.TaskID)
		appendRecord(DashboardRecord{Kind: "review", ID: r.ID, TaskID: at.Request.TaskID, AttemptID: at.ID, LeaseID: r.Request.LeaseID, RevisionID: r.Revision.ID, ReviewerID: r.IssuerSession, EvidenceIDs: ids, State: r.Request.Outcome, ObservedAt: r.CreatedAt, Freshness: "historical", Eligibility: "historical-not-revalidated"})
	}
	for _, d := range s.NativePolicyDecisions {
		if d.Input.ProjectID != project || !allowed[d.Input.TaskID] {
			continue
		}
		expiry := d.ExpiresAt
		state := string(d.Assessment.Verdict)
		if dashboardApprovalNeeded(&s, d, out.ObservedAt) {
			attention(d.ID, d.Input.TaskID, "approval-needed", "Recorded policy decision requires owner approval; no effect is authorized here.", d.CreatedAt, &expiry)
		}
		appendRecord(DashboardRecord{Kind: "decision", ID: d.ID, DecisionID: d.ID, TaskID: d.Input.TaskID, AttemptID: d.Input.AttemptID, LeaseID: d.Input.LeaseID, RevisionID: d.Input.RevisionID, ReviewerID: s.Reviews[d.Input.ReviewID].IssuerSession, ActorID: d.ActorID, State: state, ObservedAt: d.CreatedAt, ExpiresAt: &expiry, Freshness: dashboardFreshness(d.CreatedAt, &expiry, out.ObservedAt), Eligibility: "historical-not-revalidated"})
	}
	for _, report := range dashboardCurrentPreflight(&s, project, allowed) {
		expiry := report.ExpiresAt
		if !out.ObservedAt.Before(expiry) {
			attention(report.TaskID+":preflight", report.TaskID, "disconnected", "Recorded preflight proof has expired; current capability is unknown.", report.ObservedAt, &expiry)
			continue
		}
		for _, phase := range report.Phases {
			if phase.Required && phase.Verdict != "ready" && phase.Verdict != "skipped" {
				category := "blocked"
				if phase.Verdict == "unsupported" || phase.Verdict == "unknown" {
					category = "unsupported-capability"
				}
				attention(report.TaskID+":"+phase.Phase, report.TaskID, category, "Required native capability has no current ready proof.", report.ObservedAt, &expiry)
			}
		}
	}

	for id, taskID := range related {
		r, ok := s.Sessions[id]
		if !ok {
			continue
		}
		expiry := r.ExpiresAt
		state := "enrolled"
		if r.Revoked {
			state = "revoked"
		}
		appendRecord(DashboardRecord{Kind: "session", ID: id, TaskID: taskID, State: state, ObservedAt: time.Time{}, ExpiresAt: &expiry, Freshness: dashboardFreshness(time.Time{}, &expiry, out.ObservedAt)})
	}
	for id, d := range s.Dispatches {
		if d.ProjectID != project || !allowed[d.Reservation.TaskID] {
			continue
		}
		history := s.LifecycleObservations[id]
		if len(history) == 0 {
			attention(id, d.Reservation.TaskID, "disconnected", "No authenticated lifecycle observation is recorded; host state is unknown.", time.Time{}, nil)
			continue
		}
		last := history[len(history)-1]
		expiry := last.SourceTime.Add(s.LifecyclePolicies[project].Freshness)
		if out.ObservedAt.After(expiry) {
			attention(id, d.Reservation.TaskID, "disconnected", "Recorded cooperative lifecycle observation is stale.", last.SourceTime, &expiry)
		}
	}
	if policy, ok := s.BudgetPolicies[project]; ok && dashboardBudgetScope(&s, policy, allowed) {
		status, e := budgetStatus(&s, policy, out.ObservedAt)
		if e == nil {
			spent, reserved, limit := status.Window.Spent, status.Window.Reserved, status.Window.Limit
			out.Spend = DashboardSpend{State: "estimated", Provenance: "owner-estimate", Enforcement: "local allocation admission only; provider spending unavailable", Unit: "owner-estimated tokens", EstimatedSpent: &spent, EstimatedReserved: &reserved, EstimatedLimit: &limit, ProviderTokens: status.ProviderUsage}
			for _, alloc := range status.Allocations {
				for _, phase := range alloc.Phases {
					if strings.Contains(phase.State, "unknown") {
						out.Spend.UnknownRetained = true
					}
				}
			}
		}
	}
	sort.Slice(out.Records, func(i, j int) bool {
		return out.Records[i].Kind+out.Records[i].ID < out.Records[j].Kind+out.Records[j].ID
	})
	sort.Slice(out.Attention, func(i, j int) bool {
		return out.Attention[i].ID+out.Attention[i].Category < out.Attention[j].ID+out.Attention[j].Category
	})
	unique := out.Attention[:0]
	for _, item := range out.Attention {
		if len(unique) == 0 || unique[len(unique)-1].ID != item.ID {
			unique = append(unique, item)
		}
	}
	out.Attention = unique
	if len(out.Records) > dashboardRecordLimit {
		out.Records = out.Records[:dashboardRecordLimit]
		out.Limited = true
		out.State = "limited"
	}
	if len(out.Attention) > dashboardRecordLimit {
		out.Attention = out.Attention[:dashboardRecordLimit]
		out.Limited = true
		out.State = "limited"
	}
	if _, e := authenticate(&s, token, session, time.Now()); e != nil {
		return DashboardSnapshot{}, e
	}
	return out, nil
}
func dashboardFreshness(observed time.Time, expires *time.Time, now time.Time) string {
	if expires != nil && !now.Before(*expires) {
		return "stale"
	}
	if observed.IsZero() {
		return "unknown"
	}
	return "historical"
}

// OpenDashboardAuthority canonicalizes the private authority path and selects no
// observer or writer. Unsupported future schemas fail through the shared decoder.
func OpenDashboardAuthority(path string, binding Binding) (*Authority, error) {
	if !filepath.IsAbs(path) || binding.Workspace == "" || binding.Installation == "" {
		return nil, ErrDenied
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return nil, ErrStorage
	}
	return &Authority{path: filepath.Join(parent, filepath.Base(path)), binding: binding, version: 6}, nil
}

func dashboardBudgetScope(s *state, p BudgetPolicy, allowed map[string]bool) bool {
	for _, allocation := range s.BudgetAllocations {
		if allocation.ProjectID == p.ProjectID && !allowed[allocation.TaskID] {
			return false
		}
	}
	for id := range p.Tasks {
		if !allowed[id] {
			return false
		}
	}
	return true
}

func dashboardApprovalNeeded(s *state, d NativePolicyDecision, now time.Time) bool {
	p, ok := s.NativePolicies[d.Input.TaskID]
	if !ok || !reflect.DeepEqual(p, d.Policy) || d.PolicyDigest != identity("native-policy", "", p) || !p.Allow || p.Stopped || !p.RequireApproval || !now.Before(p.ExpiresAt) || !now.Before(d.ExpiresAt) {
		return false
	}
	if _, used := s.NativePolicyConsumptions[d.ID]; used {
		return false
	}
	ap, ok := s.NativePolicyApprovals[d.ID]
	return !ok || ap.DecisionID != d.ID || ap.ActionDigest != d.ActionDigest || !now.Before(ap.ExpiresAt)
}

func dashboardCurrentPreflight(s *state, project string, allowed map[string]bool) map[string]PreflightReport {
	latest := map[string]PreflightReport{}
	current, ok := s.PreflightPolicies[project]
	if !ok {
		return latest
	}
	for _, receipt := range s.PreflightOperations {
		r := receipt.Report
		if r.ProjectID != project || !allowed[r.TaskID] || r.PolicyRevision != current.Revision {
			continue
		}
		old, ok := latest[r.TaskID]
		if !ok || r.ObservedAt.After(old.ObservedAt) || r.ObservedAt.Equal(old.ObservedAt) && r.ProofDigest > old.ProofDigest {
			latest[r.TaskID] = r
		}
	}
	return latest
}
