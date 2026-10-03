package nativegovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

// Commands and issuer credentials are private owner configuration. A worker
// supplies only an immutable check name and its fenced, committed revision.
type configuredCheck struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Deadline string `json:"deadline"`
	GrantID  string `json:"grant_id,omitempty"`
}
type checksConfig struct {
	SchemaVersion int                `json:"schema_version"`
	Checks        []checkDeclaration `json:"checks"`
}

// Separate declarations forbid a caller from supplying an existing grant ID.
type checkDeclaration struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Deadline string `json:"deadline"`
}
type runnerProfile struct {
	SchemaVersion   int               `json:"schema_version"`
	Workspace       string            `json:"workspace"`
	WorkspaceID     string            `json:"workspace_id"`
	Authority       string            `json:"authority"`
	InstallationID  string            `json:"installation_id"`
	TaskID          string            `json:"task_id"`
	Repository      string            `json:"repository"`
	RunnerSessionID string            `json:"runner_session_id"`
	CredentialFile  string            `json:"credential_file"`
	Checks          []configuredCheck `json:"checks"`
}
type verificationRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationKey  string `json:"operation_key"`
	AttemptID     string `json:"attempt_id"`
	LeaseID       string `json:"lease_id"`
	RevisionID    string `json:"revision_id"`
	CheckID       string `json:"check_id"`
	ExpectedFence uint64 `json:"expected_fence"`
}

func verificationID(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "/\\\x00\r\n")
}
func privateJSON(path string, value any) error {
	b, e := native.ReadPrivate(path, 65536)
	if e != nil || native.Decode(b, value) != nil {
		return denied()
	}
	return nil
}
func descriptors(checks []configuredCheck, grants bool) ([]application.NativeCheck, error) {
	if len(checks) == 0 || len(checks) > 32 {
		return nil, denied()
	}
	seen := map[string]bool{}
	result := make([]application.NativeCheck, 0, len(checks))
	for _, c := range checks {
		deadline, e := time.ParseDuration(c.Deadline)
		if e != nil || deadline <= 0 || deadline > 10*time.Minute || !verificationID(c.ID) || strings.TrimSpace(c.Command) == "" || seen[c.ID] || (grants && !verificationID(c.GrantID)) {
			return nil, denied()
		}
		seen[c.ID] = true
		result = append(result, application.NativeCheck{Descriptor: sessions.CheckDescriptor{ID: c.ID, Command: c.Command, Deadline: deadline}, GrantID: c.GrantID})
	}
	return result, nil
}
func verificationLifetime(f *clikit.Flags) (time.Duration, error) {
	lifetime := time.Hour
	if f.Get("expires") != "" {
		var e error
		lifetime, e = time.ParseDuration(f.Get("expires"))
		if e != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return 0, clikit.Usagef("--expires must be positive and at most 24h")
		}
	}
	return lifetime, nil
}
func verifierAdd(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "task", "checks-file", "out", "credential", "expires")
	if e != nil {
		return e
	}
	for _, k := range []string{"config", "task", "checks-file", "out", "credential"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s is required", k)
		}
	}
	lifetime, e := verificationLifetime(f)
	if e != nil {
		return e
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	var declarations checksConfig
	if e = privateJSON(f.Get("checks-file"), &declarations); e != nil || declarations.SchemaVersion != 1 {
		return denied()
	}
	checks := make([]configuredCheck, 0, len(declarations.Checks))
	for _, d := range declarations.Checks {
		checks = append(checks, configuredCheck{ID: d.ID, Command: d.Command, Deadline: d.Deadline})
	}
	trusted, e := descriptors(checks, false)
	if e != nil {
		return e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	owner, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	task, e := store.FindTask(w, f.Get("task"))
	if e != nil {
		return denied()
	}
	// This read-only owner preflight must never create or enable task admission.
	binding, e := a.OwnerTaskBinding(owner, task.ID)
	if e != nil || binding.ProjectID != task.Project {
		return denied()
	}
	observation, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil || binding.Repository != observation.Repository {
		return denied()
	}
	// Budget the complete profile before issuing authority. Conservative maximum
	// ID lengths cover all granted fields; setup cannot create an unreadable file.
	budgetProfile := runnerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: c.Authority, InstallationID: c.InstallationID, TaskID: task.ID, Repository: observation.Repository, RunnerSessionID: strings.Repeat("x", 256), CredentialFile: f.Get("credential"), Checks: append([]configuredCheck(nil), checks...)}
	for i := range budgetProfile.Checks {
		budgetProfile.Checks[i].GrantID = strings.Repeat("x", 256)
	}
	projected, e := json.Marshal(budgetProfile)
	if e != nil || len(projected) > 65536 {
		return denied()
	}
	profileFile, e := reserve(f.Get("out"))
	if e != nil {
		return e
	}
	defer func() { _ = profileFile.Close() }()
	credential, e := reserve(f.Get("credential"))
	if e != nil {
		_ = os.Remove(f.Get("out"))
		return e
	}
	defer func() { _ = credential.Close() }()
	issued := false
	defer func() {
		if !issued {
			_ = os.Remove(f.Get("out"))
			_ = os.Remove(f.Get("credential"))
		}
	}()
	expires := time.Now().Add(lifetime)
	runner, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + task.ID}}, expires)
	if e != nil {
		return denied()
	}
	// Issued authority is never rolled back. Persist recovery identifiers before
	// grants; incomplete private profiles fail closed and can be owner-revoked.
	issued = true
	fmt.Fprintf(ctx.Stderr, "Native verification runner %s issued; preserve private files for owner recovery if setup is interrupted.\n", runner.ID)
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	profile := runnerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: c.Authority, InstallationID: c.InstallationID, TaskID: task.ID, Repository: observation.Repository, RunnerSessionID: runner.ID, CredentialFile: f.Get("credential"), Checks: checks}
	if e = saveRunnerProfile(profileFile, profile); e != nil {
		return e
	}
	for i, check := range trusted {
		grant, grantErr := a.GrantVerification(owner, runner.ID, task.ID, observation.Repository, check.Descriptor, expires)
		if grantErr != nil {
			return denied()
		}
		profile.Checks[i].GrantID = grant.ID
		fmt.Fprintf(ctx.Stderr, "Native verification grant %s issued for check %s.\n", grant.ID, check.Descriptor.ID)
		if e = saveRunnerProfile(profileFile, profile); e != nil {
			return e
		}
	}
	fmt.Fprintln(ctx.Stdout, "Native named verification configured; runner profile and credential saved privately.")
	return nil
}

// Rewrite only this invocation's new, exclusively reserved descriptor. A failed
// write leaves issued authority and private recovery files intact, never reset.
func saveRunnerProfile(f *os.File, p runnerProfile) error {
	b, e := json.Marshal(p)
	if e != nil || len(b) > 65536 {
		return denied()
	}
	if e = f.Truncate(0); e != nil {
		return denied()
	}
	if _, e = f.Seek(0, 0); e != nil {
		return denied()
	}
	return writePrivate(f, b)
}
func verify(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection", "runner-config", "request")
	if e != nil {
		return e
	}
	for _, k := range []string{"connection", "runner-config", "request"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s is required", k)
		}
	}
	var c native.Connection
	var p runnerProfile
	var request verificationRequest
	if privateJSON(f.Get("connection"), &c) != nil || privateJSON(f.Get("runner-config"), &p) != nil || privateJSON(f.Get("request"), &request) != nil {
		return denied()
	}
	if c.SchemaVersion != 1 || p.SchemaVersion != 1 || request.SchemaVersion != 1 || c.WorkspaceID == "" || c.InstallationID == "" || !verificationID(c.SessionID) || c.WorkspaceID != p.WorkspaceID || c.InstallationID != p.InstallationID || !verificationID(p.RunnerSessionID) || !verificationID(p.TaskID) || !filepath.IsAbs(p.Repository) {
		return denied()
	}
	// Compare physical authority paths as well as installation/workspace binding;
	// a profile from another local authority cannot be substituted by its IDs.
	for _, path := range []string{c.Authority, p.Authority} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return denied()
		}
	}
	authority, e := filepath.EvalSymlinks(c.Authority)
	runnerAuthority, re := filepath.EvalSymlinks(p.Authority)
	if e != nil || re != nil || !filepath.IsAbs(authority) || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(p.Authority) || authority != runnerAuthority {
		return denied()
	}
	w, e := native.ResolveWorkspace(c.Workspace, c.WorkspaceID)
	if e != nil {
		return denied()
	}
	runnerWorkspace, e := native.ResolveWorkspace(p.Workspace, p.WorkspaceID)
	if e != nil || runnerWorkspace.Root != w.Root {
		return denied()
	}
	trusted, e := descriptors(p.Checks, true)
	if e != nil {
		return e
	}
	if !verificationID(request.OperationKey) || !verificationID(request.AttemptID) || !verificationID(request.LeaseID) || !verificationID(request.RevisionID) || !verificationID(request.CheckID) || request.ExpectedFence == 0 {
		return denied()
	}
	workerToken, e := native.ReadCredential(c.CredentialFile)
	if e != nil {
		return denied()
	}
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID}, admission)
	if e != nil {
		return denied()
	}
	worker, e := a.Authenticate(workerToken, c.SessionID)
	if e != nil {
		return denied()
	}
	runnerToken, e := native.ReadCredential(p.CredentialFile)
	if e != nil {
		return denied()
	}
	runnerSession, e := a.Authenticate(runnerToken, p.RunnerSessionID)
	if e != nil || runnerSession.Parent != "" || len(runnerSession.Scope.Actions) != 1 || runnerSession.Scope.Actions[0] != "verification.run" || len(runnerSession.Scope.Resources) != 1 || runnerSession.Scope.Resources[0] != "task:"+p.TaskID {
		return denied()
	}
	if _, e = a.Authorize(workerToken, worker.ID, w.ID, "verification.request", "task:"+p.TaskID); e != nil {
		return denied()
	}
	repository, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil || repository.Repository != p.Repository {
		return denied()
	}
	runner, e := application.NewNativeVerificationRunner(a, runnerToken, p.RunnerSessionID, trusted)
	if e != nil {
		return denied()
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	evidence, e := runner.Run(bounded, workerToken, application.NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: worker.ID, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence})
	if errors.Is(e, sessions.ErrReconcile) {
		return clikit.Refusedf("verification outcome unknown; preserve operation key and private profile for owner reconciliation")
	}
	if e != nil {
		return denied()
	}
	if evidence.Revision.Observation.Repository != p.Repository || evidence.Revision.AttemptID != request.AttemptID {
		return denied()
	}
	return clikit.EmitJSON(ctx, struct {
		Schema   string            `json:"schema"`
		Evidence sessions.Evidence `json:"evidence"`
	}{"native-verification/v1", evidence})
}
