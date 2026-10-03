package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

// NativePhaseContract is an owner-selected cooperative contract, not a measured
// model restriction, billing counter, unattended integration producer or host API.
// Private declarations can establish only owner-manual assurance.
type NativePhaseContract struct {
	SchemaVersion                                                                   int
	WorkspaceID, ProjectID, TaskID, Harness                                         string
	ObservedAt, ExpiresAt                                                           time.Time
	Models                                                                          []string
	ReviewOutputContract                                                            string
	IntegrationRoute, IntegrationBase, IntegrationPolicyDigest, IntegrationCheckout string
	ExternalStatus                                                                  string // ready, transient, unavailable
	TokenControl, BudgetControl                                                     string // advisory, unavailable; never enforced
}

func safeDigest(v any) string {
	b, _ := json.Marshal(v)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func credentialDigest(path string) (string, error) {
	token, e := nativeconfig.ReadCredential(path)
	if e != nil {
		return "", e
	}
	d := sha256.Sum256([]byte(token))
	return hex.EncodeToString(d[:]), nil
}
func (a *LeaseAdmission) ObservePreflight(workspaceID, project, task string, spec sessions.PreflightTask, placement sessions.SwarmPlacement) (sessions.PreflightObservation, error) {
	o := sessions.PreflightObservation{Harness: "codex", Phases: []sessions.PreflightPhase{}}
	if a == nil || workspaceID != a.workspace.ID {
		return o, sessions.ErrDenied
	}
	phase := func(name, verdict, assurance, remedy string, required bool, evidence string) {
		o.Phases = append(o.Phases, sessions.PreflightPhase{Phase: name, Verdict: verdict, Assurance: assurance, Required: required, EvidenceDigest: evidence, Remediation: remedy})
	}
	canonical, e := store.ReadCheckpointCanonical(&a.workspace)
	if e != nil {
		return o, e
	}
	var found *store.Task
	for i := range canonical.Tasks {
		if canonical.Tasks[i].ID == task && canonical.Tasks[i].Project == project {
			if found != nil {
				return o, sessions.ErrDenied
			}
			found = canonical.Tasks[i]
		}
	}
	if found == nil {
		return o, sessions.ErrDenied
	}
	o.TaskDigest = canonical.Digests[found.ID]
	o.ClaimsDigest = safeDigest(found.Claims())
	for _, claim := range found.Claims() {
		o.ClaimResources = append(o.ClaimResources, "path:"+claim)
	}
	if estimate, sized := found.Estimate(); sized {
		o.Required = estimate.Expected()
		o.Sized = true
	}
	roles, e := store.ReadCheckpointRoles(&a.workspace)
	if e != nil {
		return o, e
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	o.RosterDigest = safeDigest(roles)
	role := func(name string) team.Role {
		for _, r := range roles {
			if r.Name == name {
				return r
			}
		}
		return team.Role{}
	}
	o.Implementation = role(spec.ImplementationRole)
	o.Review = role(spec.ReviewRole)
	o.Recovery = role(spec.RecoveryRole)
	for _, name := range spec.FallbackRoles {
		o.Fallback = append(o.Fallback, role(name))
	}
	if !slices.Equal(o.Implementation.FallbackTo, spec.FallbackRoles) {
		phase("fallback", "policy_refused", "canonical-role", "owner fallback selection must match the complete canonical chain", true, o.RosterDigest)
	}
	for _, r := range append([]team.Role{o.Implementation, o.Recovery}, o.Fallback...) {
		scopeOK := r.Grant == "rw" || r.Grant == ""
		for _, path := range found.Claims() {
			if !r.InScope(path) {
				scopeOK = false
			}
		}
		if !scopeOK {
			phase("roles", "policy_refused", "canonical-role", "every implementation/recovery/fallback role grant and scope must include all canonical claims", true, o.ClaimsDigest)
		}
	}
	var worker nativeconfig.Connection
	if verificationPrivateJSON(spec.WorkerConnection, &worker) != nil || (worker.SchemaVersion != 1 && worker.SchemaVersion != 2) || worker.WorkspaceID != workspaceID {
		phase("implementation", "policy_refused", "private-profile", "provide the exact owner-selected private worker connection", true, "")
	} else {
		w, e := nativeconfig.ResolveWorkspace(worker.Workspace, workspaceID)
		if e != nil || w.Root != a.workspace.Root {
			return o, sessions.ErrDenied
		}
		if info, e := os.Lstat(worker.Authority); e != nil || !info.Mode().IsRegular() {
			phase("implementation", "policy_refused", "private-profile", "worker authority must be the same regular authority file; file aliases are unsupported", true, "")
		} else {
			o.AuthorityPath, _ = filepath.EvalSymlinks(worker.Authority)
		}
		o.InstallationID = worker.InstallationID
		credential, e := credentialDigest(worker.CredentialFile)
		if e != nil {
			phase("implementation", "policy_refused", "private-profile", "worker credential unavailable", true, "")
		} else {
			o.Worker = sessions.PreflightActor{SessionID: worker.SessionID, CredentialHash: credential}
		}
	}
	profileFacts := []any{worker}
	var contract NativePhaseContract
	if verificationPrivateJSON(spec.ContractProfile, &contract) != nil || contract.SchemaVersion != 1 || contract.WorkspaceID != workspaceID || contract.ProjectID != project || contract.TaskID != task || contract.Harness != "codex" || contract.ObservedAt.After(time.Now().Add(time.Minute)) || contract.ObservedAt.Before(time.Now().Add(-time.Hour)) || !time.Now().Before(contract.ExpiresAt) || contract.ExpiresAt.After(contract.ObservedAt.Add(time.Hour)) {
		phase("controls", "policy_refused", "owner-manual", "refresh a bounded owner-selected phase contract", true, "")
	} else {
		o.ExpiresAt = contract.ExpiresAt
	}
	profileFacts = append(profileFacts, contract)
	modelAvailable := func(r team.Role) bool {
		for _, m := range contract.Models {
			if r.ModelID() == m {
				return true
			}
		}
		return false
	}
	modelRoles := []team.Role{o.Implementation, o.Recovery}
	if spec.RequireReview {
		modelRoles = append(modelRoles, o.Review)
	}
	for _, r := range append(modelRoles, o.Fallback...) {
		if !modelAvailable(r) {
			phase("model", "unsupported", "owner-manual", "owner must explicitly attest the resolved model is available in the pinned Codex channel", true, safeDigest(contract.Models))
		}
	}
	phase("implementation", "ready", "owner-manual", "cooperative model selection; no enforced host model override is claimed", true, o.RosterDigest)
	phase("recovery", "ready", "owner-manual", "owner recovery remains explicit; no alternate host launcher", true, o.RosterDigest)
	phase("fallback", "ready", "owner-manual", "only canonical pinned Codex roles may be selected", true, o.RosterDigest)
	if spec.RequireReview {
		for _, path := range found.Claims() {
			if !o.Review.InScope(path) {
				phase("review", "policy_refused", "canonical-role", "reviewer role scope excludes canonical claims", true, o.ClaimsDigest)
			}
		}
		var p NativeReviewerProfile
		if verificationPrivateJSON(spec.ReviewerProfile, &p) != nil || p.SchemaVersion != 1 || p.WorkspaceID != workspaceID || p.ProjectID != project || p.TaskID != task || p.Authority != worker.Authority || p.InstallationID != worker.InstallationID || p.WorkerSessionID != worker.SessionID || p.Workspace != worker.Workspace || contract.ReviewOutputContract != "native-review-v1" || o.Review.Grant != "ro" {
			phase("review", "unsupported", "private-profile", "provision matching independent reviewer, native-review-v1 output and scoped read-only role before implementation", true, "")
		} else {
			credential, e := credentialDigest(p.CredentialFile)
			observer, err := NewNativeReviewObserver(&a.workspace, privateNativeReviewPolicy{path: p.PolicyFile, workspace: workspaceID, project: project, task: task})
			if e != nil || err != nil {
				phase("review", "policy_refused", "private-profile", "review credential or current policy unavailable", true, "")
			} else {
				observation, e := observer.ObserveReview(workspaceID, project, task)
				if e != nil {
					phase("review", "policy_refused", "canonical-policy", "current review checklist unavailable", true, "")
				} else {
					o.ReviewObservation = observation
					o.Reviewer = sessions.PreflightActor{SessionID: p.ReviewerSessionID, GrantID: p.GrantID, CredentialHash: credential}
					phase("review", "ready", "owner-manual", "separately provisioned reviewer output contract", true, safeDigest(observation))
				}
			}
		}
		o.ReviewerRepository = p.Repository
		profileFacts = append(profileFacts, p)
	} else {
		phase("review", "skipped", "owner-policy", "review not required by this owner policy", false, "")
	}
	if spec.RequireVerification || spec.RequireReview {
		var p NativeRunnerProfile
		if verificationPrivateJSON(spec.RunnerProfile, &p) != nil || p.SchemaVersion != 1 || p.WorkspaceID != workspaceID || p.TaskID != task || p.Authority != worker.Authority || p.InstallationID != worker.InstallationID || p.Workspace != worker.Workspace {
			phase("verification", "unsupported", "private-profile", "provision exact trusted runner, workdir and bounded check profile", true, "")
		} else {
			checks, e := NativeCheckDescriptors(p.Checks, true)
			credential, ce := credentialDigest(p.CredentialFile)
			if e != nil || ce != nil {
				phase("verification", "policy_refused", "private-profile", "repair bounded checks and runner credential", true, "")
			} else {
				o.Runner = sessions.PreflightActor{SessionID: p.RunnerSessionID, CredentialHash: credential}
				for _, c := range checks {
					o.VerificationGrantIDs = append(o.VerificationGrantIDs, c.GrantID)
					o.Checks = append(o.Checks, sessions.RequiredCheck{ID: c.Descriptor.ID, CommandDigest: sessions.CheckDigest(c.Descriptor)})
				}
				phase("verification", "ready", "configured-trusted-runner", "exact checks are configured; no execution evidence is invented", true, safeDigest(o.Checks))
			}
		}
		o.RunnerRepository = p.Repository
		if !store.VerificationInterpreterAvailable() {
			phase("verification", "unsupported", "local-interpreter-observation", "configured sh-c interpreter unavailable", true, safeDigest(o.Checks))
		} else {
			phase("verification-execution", "ready", "owner-manual", "exact bounded shell checks configured; command execution availability is cooperative and no checks are run during preflight", true, safeDigest(o.Checks))
		}
		working, workErr := checkout.Observe(placement.Checkout)
		if workErr != nil || working.Repository != p.Repository {
			phase("verification", "unsupported", "local-workdir-observation", "workspace workdir unavailable", true, "")
		}
		profileFacts = append(profileFacts, struct {
			Session, Authority, Installation, Repository string
			GrantIDs                                     []string
			Checks                                       []sessions.RequiredCheck
		}{p.RunnerSessionID, p.Authority, p.InstallationID, p.Repository, o.VerificationGrantIDs, o.Checks})
	} else {
		phase("verification", "skipped", "owner-policy", "verification not required by this owner policy", false, "")
	}
	if spec.RequireIntegration {
		verdict := "unsupported"
		landing, landingErr := checkout.Observe(contract.IntegrationCheckout)
		if spec.OwnerAuthorizedIntegration && contract.IntegrationRoute == "owner-manual-local" && len(contract.IntegrationBase) == 40 && strings.Trim(contract.IntegrationBase, "0123456789abcdef") == "" && len(contract.IntegrationPolicyDigest) == 64 && strings.Trim(contract.IntegrationPolicyDigest, "0123456789abcdef") == "" && landingErr == nil && landing.Commit == contract.IntegrationBase && landing.Repository == func() string {
			working, e := checkout.Observe(placement.Checkout)
			if e != nil {
				return ""
			}
			return working.Repository
		}() {
			switch contract.ExternalStatus {
			case "ready":
				verdict = "ready"
			case "transient":
				verdict = "transient"
			}
		}
		phase("integration", verdict, "owner-manual", "owner must authorize and freshly attest exact base/policy; native integration producer592 remains unavailable", true, safeDigest(struct{ Route, Base, Policy string }{contract.IntegrationRoute, contract.IntegrationBase, contract.IntegrationPolicyDigest}))
	} else {
		phase("integration", "skipped", "owner-policy", "no integration authority inferred from a worker grant", false, "")
	}
	phase("tokens", "unsupported", "advisory", "required enforced token controls cannot be satisfied by an owner declaration", spec.RequireEnforcedTokens, safeDigest(contract.TokenControl))
	phase("budget", "unknown", "unavailable", "accounting552 rolling budgets are unavailable; no billing or enforced spending guarantee", spec.RequireEnforcedBudget, safeDigest(contract.BudgetControl))
	// Profile identities deliberately exclude credential bytes/hashes and runner commands.
	o.ProfileDigest = safeDigest(profileFacts)
	return o, nil
}
