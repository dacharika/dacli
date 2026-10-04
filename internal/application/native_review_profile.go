package application

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// NativeReviewPolicy is private owner configuration, never review request data.
// Checklist truth is freshly read from canonical tasks, not copied into this file.
type NativeReviewPolicy struct {
	SchemaVersion int                      `json:"schema_version"`
	WorkspaceID   string                   `json:"workspace_id"`
	ProjectID     string                   `json:"project_id"`
	TaskID        string                   `json:"task_id"`
	Revision      string                   `json:"revision"`
	Checks        []sessions.RequiredCheck `json:"checks"`
}

// ReviewPolicy supports owner preflight of a validated configuration snapshot.
// Live review authorities use privateNativeReviewPolicy to reread the file.
func (p NativeReviewPolicy) ReviewPolicy(workspace, project, task string) (string, []sessions.RequiredCheck, error) {
	if validateNativeReviewPolicy(p) != nil || p.WorkspaceID != workspace || p.ProjectID != project || p.TaskID != task {
		return "", nil, sessions.ErrDenied
	}
	return p.Revision, append([]sessions.RequiredCheck(nil), p.Checks...), nil
}

type NativeReviewerProfile struct {
	SchemaVersion     int    `json:"schema_version"`
	Workspace         string `json:"workspace"`
	WorkspaceID       string `json:"workspace_id"`
	Authority         string `json:"authority"`
	InstallationID    string `json:"installation_id"`
	ProjectID         string `json:"project_id"`
	TaskID            string `json:"task_id"`
	Repository        string `json:"repository"`
	ReviewerSessionID string `json:"reviewer_session_id"`
	WorkerSessionID   string `json:"worker_session_id"`
	GrantID           string `json:"grant_id"`
	CredentialFile    string `json:"credential_file"`
	PolicyFile        string `json:"policy_file"`
}

type NativeReviewRequest struct {
	OperationKey  string   `json:"operation_key"`
	AttemptID     string   `json:"attempt_id"`
	LeaseID       string   `json:"lease_id"`
	RevisionID    string   `json:"revision_id"`
	ExpectedFence uint64   `json:"expected_fence"`
	Outcome       string   `json:"outcome"`
	Rationale     string   `json:"rationale"`
	EvidenceIDs   []string `json:"evidence_ids"`
}

func validateNativeReviewPolicy(p NativeReviewPolicy) error {
	if p.SchemaVersion != 1 || !VerificationID(p.WorkspaceID) || !VerificationID(p.ProjectID) || !VerificationID(p.TaskID) || !VerificationID(p.Revision) || len(p.Checks) == 0 || len(p.Checks) > 32 {
		return sessions.ErrDenied
	}
	seen := map[string]bool{}
	for _, c := range p.Checks {
		if !VerificationID(c.ID) || seen[c.ID] || len(c.CommandDigest) != 64 || strings.Trim(c.CommandDigest, "0123456789abcdef") != "" {
			return sessions.ErrDenied
		}
		seen[c.ID] = true
	}
	return nil
}

// NativeReviewPolicyFromChecks binds the named command bytes and deadlines of
// validated owner-configured runner checks; no producer-supplied digest is trusted.
func NativeReviewPolicyFromChecks(workspaceID, projectID, taskID, revision string, checks []ConfiguredCheck) (NativeReviewPolicy, error) {
	descriptors, err := NativeCheckDescriptors(checks, true)
	if err != nil {
		return NativeReviewPolicy{}, err
	}
	p := NativeReviewPolicy{SchemaVersion: 1, WorkspaceID: workspaceID, ProjectID: projectID, TaskID: taskID, Revision: revision}
	for _, c := range descriptors {
		p.Checks = append(p.Checks, sessions.RequiredCheck{ID: c.Descriptor.ID, CommandDigest: sessions.CheckDigest(c.Descriptor)})
	}
	sort.Slice(p.Checks, func(i, j int) bool { return p.Checks[i].ID < p.Checks[j].ID })
	if err = validateNativeReviewPolicy(p); err != nil {
		return NativeReviewPolicy{}, err
	}
	return p, nil
}

type privateNativeReviewPolicy struct{ path, workspace, project, task string }

func (p privateNativeReviewPolicy) ReviewPolicy(workspace, project, task string) (string, []sessions.RequiredCheck, error) {
	if workspace != p.workspace || project != p.project || task != p.task {
		return "", nil, sessions.ErrDenied
	}
	var current NativeReviewPolicy
	if verificationPrivateJSON(p.path, &current) != nil || validateNativeReviewPolicy(current) != nil || current.WorkspaceID != workspace || current.ProjectID != project || current.TaskID != task {
		return "", nil, sessions.ErrDenied
	}
	return current.Revision, append([]sessions.RequiredCheck(nil), current.Checks...), nil
}

// OpenNativeReviewAuthority installs a fresh canonical observer and private policy
// source. It neither enrolls a session nor creates or widens any task binding.
func OpenNativeReviewAuthority(workspacePath, workspaceID, authorityPath, installationID, policyPath string) (*sessions.Authority, NativeReviewPolicy, error) {
	var p NativeReviewPolicy
	if verificationPrivateJSON(policyPath, &p) != nil || validateNativeReviewPolicy(p) != nil || p.WorkspaceID != workspaceID || !VerificationID(installationID) {
		return nil, NativeReviewPolicy{}, sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(workspacePath, workspaceID)
	if e != nil {
		return nil, NativeReviewPolicy{}, sessions.ErrDenied
	}
	observer, e := NewNativeReviewObserver(w, privateNativeReviewPolicy{policyPath, p.WorkspaceID, p.ProjectID, p.TaskID})
	if e != nil {
		return nil, NativeReviewPolicy{}, e
	}
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		return nil, NativeReviewPolicy{}, e
	}
	a, e := sessions.OpenReviewAuthority(authorityPath, sessions.Binding{Workspace: workspaceID, Installation: installationID}, observer, admission)
	if e != nil {
		return nil, NativeReviewPolicy{}, e
	}
	return a, p, nil
}

type NativeReviewProfile struct {
	connectionPath, profilePath string
	connection                  nativeconfig.Connection
	profile                     NativeReviewerProfile
}
type NativeReviewAccess struct {
	authority             *sessions.Authority
	token, session, grant string
}

func (NativeReviewAccess) String() string   { return "NativeReviewAccess" }
func (NativeReviewAccess) GoString() string { return "NativeReviewAccess" }

func OpenNativeReviewProfile(connectionPath, profilePath string) (*NativeReviewProfile, error) {
	var c nativeconfig.Connection
	var p NativeReviewerProfile
	if verificationPrivateJSON(connectionPath, &c) != nil || verificationPrivateJSON(profilePath, &p) != nil {
		return nil, sessions.ErrDenied
	}
	selected := &NativeReviewProfile{connectionPath, profilePath, c, p}
	if _, e := selected.Authenticate(); e != nil {
		return nil, e
	}
	return selected, nil
}
func (selected *NativeReviewProfile) Authenticate() (*NativeReviewAccess, error) {
	if selected == nil {
		return nil, sessions.ErrDenied
	}
	var c nativeconfig.Connection
	var p NativeReviewerProfile
	if verificationPrivateJSON(selected.connectionPath, &c) != nil || verificationPrivateJSON(selected.profilePath, &p) != nil || c != selected.connection || !reflect.DeepEqual(p, selected.profile) {
		return nil, sessions.ErrDenied
	}
	if c.SchemaVersion != 1 || p.SchemaVersion != 1 || c.WorkspaceID != p.WorkspaceID || c.InstallationID != p.InstallationID || c.SessionID != p.ReviewerSessionID || c.CredentialFile != p.CredentialFile || !VerificationID(p.ReviewerSessionID) || !VerificationID(p.WorkerSessionID) || p.ReviewerSessionID == p.WorkerSessionID || !VerificationID(p.GrantID) || !filepath.IsAbs(p.Repository) {
		return nil, sessions.ErrDenied
	}
	for _, path := range []string{c.Authority, p.Authority} {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || !filepath.IsAbs(path) {
			return nil, sessions.ErrDenied
		}
	}
	authority, e := filepath.EvalSymlinks(c.Authority)
	other, re := filepath.EvalSymlinks(p.Authority)
	if e != nil || re != nil || authority != other {
		return nil, sessions.ErrDenied
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	pw, e := nativeconfig.ResolveWorkspace(p.Workspace, p.WorkspaceID)
	if e != nil || pw.Root != w.Root {
		return nil, sessions.ErrDenied
	}
	a, policy, e := OpenNativeReviewAuthority(w.Root, w.ID, authority, p.InstallationID, p.PolicyFile)
	if e != nil || policy.TaskID != p.TaskID || policy.ProjectID != p.ProjectID {
		return nil, sessions.ErrDenied
	}
	token, e := nativeconfig.ReadCredential(c.CredentialFile)
	if e != nil {
		return nil, sessions.ErrDenied
	}
	principal, e := a.Authenticate(token, c.SessionID)
	if e != nil || principal.Parent != "" {
		return nil, sessions.ErrDenied
	}
	actions := map[string]bool{}
	for _, action := range principal.Scope.Actions {
		if action != "read" && action != "review.propose" && action != "review.evaluate" {
			return nil, sessions.ErrDenied
		}
		actions[action] = true
	}
	if !actions["review.propose"] || !actions["review.evaluate"] {
		return nil, sessions.ErrDenied
	}
	taskScope := false
	for _, resource := range principal.Scope.Resources {
		if resource == "task:"+p.TaskID {
			taskScope = true
		} else if resource != "workspace:"+w.ID {
			return nil, sessions.ErrDenied
		}
	}
	if !taskScope {
		return nil, sessions.ErrDenied
	}
	observation, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil || observation.Repository != p.Repository {
		return nil, sessions.ErrDenied
	}
	return &NativeReviewAccess{a, token, principal.ID, p.GrantID}, nil
}
func (a *NativeReviewAccess) Propose(req NativeReviewRequest) (sessions.Review, error) {
	if a == nil {
		return sessions.Review{}, sessions.ErrDenied
	}
	return a.authority.ProposeReview(a.token, a.session, sessions.ReviewRequest{OperationKey: req.OperationKey, GrantID: a.grant, AttemptID: req.AttemptID, LeaseID: req.LeaseID, RevisionID: req.RevisionID, ExpectedFence: req.ExpectedFence, Outcome: req.Outcome, Rationale: req.Rationale, EvidenceIDs: append([]string(nil), req.EvidenceIDs...)})
}
func (a *NativeReviewAccess) Evaluate(id string) (sessions.ReviewEligibility, error) {
	if a == nil || !VerificationID(id) {
		return sessions.ReviewEligibility{}, sessions.ErrDenied
	}
	return a.authority.EvaluateReview(a.token, a.session, id)
}

// AcceptanceCandidate exports only a freshly eligible issuer's stored native facts.
func (a *NativeReviewAccess) AcceptanceCandidate(reviewID string) (sessions.NativeAcceptanceCandidate, error) {
	if a == nil {
		return sessions.NativeAcceptanceCandidate{}, sessions.ErrDenied
	}
	return a.authority.ReadNativeAcceptanceCandidate(a.token, a.session, reviewID)
}
