package nativegovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
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
type configuredCheck = application.ConfiguredCheck
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
type runnerProfile = application.NativeRunnerProfile
type verificationRequest struct {
	SchemaVersion int    `json:"schema_version"`
	OperationKey  string `json:"operation_key"`
	AttemptID     string `json:"attempt_id"`
	LeaseID       string `json:"lease_id"`
	RevisionID    string `json:"revision_id"`
	CheckID       string `json:"check_id"`
	ExpectedFence uint64 `json:"expected_fence"`
}

func privateJSON(path string, value any) error {
	b, e := native.ReadPrivate(path, 65536)
	if e != nil || native.Decode(b, value) != nil {
		return denied()
	}
	return nil
}
func descriptors(checks []configuredCheck, grants bool) ([]application.NativeCheck, error) {
	trusted, e := application.NativeCheckDescriptors(checks, grants)
	if e != nil {
		return nil, denied()
	}
	return trusted, nil
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
	var request verificationRequest
	if privateJSON(f.Get("request"), &request) != nil || request.SchemaVersion != 1 {
		return denied()
	}
	selected, e := application.OpenNativeVerificationProfile(f.Get("connection"), f.Get("runner-config"))
	if e != nil {
		return denied()
	}
	access, e := selected.Authenticate()
	if e != nil {
		return denied()
	}
	req := application.NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: access.Worker.ID, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence}
	if !application.ValidNativeVerificationRequest(req) {
		return denied()
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	evidence, e := access.Run(bounded, req)
	if errors.Is(e, sessions.ErrReconcile) {
		return clikit.Refusedf("verification outcome unknown; preserve operation key and private profile for owner reconciliation")
	}
	if e != nil {
		return denied()
	}
	if evidence.Revision.AttemptID != request.AttemptID {
		return denied()
	}
	return clikit.EmitJSON(ctx, struct {
		Schema   string            `json:"schema"`
		Evidence sessions.Evidence `json:"evidence"`
	}{"native-verification/v1", evidence})
}
