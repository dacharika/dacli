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
	RoutingPolicy json.RawMessage    `json:"routing_policy,omitempty"`
	BaseCommit    string             `json:"base_commit,omitempty"`
}

// Separate declarations forbid a caller from supplying an existing grant ID.
type checkDeclaration struct {
	ID          string   `json:"id"`
	Command     string   `json:"command,omitempty"`
	Argv        []string `json:"argv,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Environment string   `json:"environment,omitempty"`
	Deadline    string   `json:"deadline"`
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
	if e = privateJSON(f.Get("checks-file"), &declarations); e != nil || (declarations.SchemaVersion != 1 && declarations.SchemaVersion != 2) {
		return denied()
	}
	checks := make([]configuredCheck, 0, len(declarations.Checks))
	for _, d := range declarations.Checks {
		checks = append(checks, configuredCheck{ID: d.ID, Command: d.Command, Deadline: d.Deadline, Argv: append([]string(nil), d.Argv...), Cwd: d.Cwd, Environment: d.Environment})
	}
	trusted, e := application.NativeProfileCheckDescriptors(runnerProfile{SchemaVersion: declarations.SchemaVersion, Checks: checks, RoutingPolicy: declarations.RoutingPolicy, BaseCommit: declarations.BaseCommit}, false)
	if e != nil {
		return denied()
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
	budgetProfile := runnerProfile{SchemaVersion: declarations.SchemaVersion, RoutingPolicy: append(json.RawMessage(nil), declarations.RoutingPolicy...), BaseCommit: declarations.BaseCommit, Workspace: w.Root, WorkspaceID: w.ID, Authority: c.Authority, InstallationID: c.InstallationID, TaskID: task.ID, Repository: observation.Repository, RunnerSessionID: strings.Repeat("x", 256), CredentialFile: f.Get("credential"), Checks: append([]configuredCheck(nil), checks...)}
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
	profile := runnerProfile{SchemaVersion: declarations.SchemaVersion, RoutingPolicy: append(json.RawMessage(nil), declarations.RoutingPolicy...), BaseCommit: declarations.BaseCommit, Workspace: w.Root, WorkspaceID: w.ID, Authority: c.Authority, InstallationID: c.InstallationID, TaskID: task.ID, Repository: observation.Repository, RunnerSessionID: runner.ID, CredentialFile: f.Get("credential"), Checks: checks}
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

// VerificationRoutingCommands contributes explicit native replacement entrypoints
// for retired inference/start; inspection cannot issue an execution permit.
func VerificationRoutingCommands() []clikit.Command {
	return []clikit.Command{
		{Path: "native verifier review-policy", Brief: "owner pins complete routed verification policy for independent review", Mutates: true, Usage: "dacli native verifier review-policy --config <owner> --task <ref> --runner-config <profile> --revision <id> --out <new-private-policy>", Run: verifierReviewPolicy},
		{Path: "native verifier inspect", Brief: "inspect exact owner-routed checks without execution", JSON: true, Usage: "dacli native verifier inspect --connection <worker> --runner-config <runner> --request <exact-revision> --json", Run: verifierInspect},
	}
}
func VerificationRoutingAuthenticatesExplicitly(path string) bool {
	return path == "native verifier review-policy" || path == "native verifier inspect"
}
func verifierReviewPolicy(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "task", "runner-config", "revision", "out")
	if e != nil {
		return e
	}
	for _, key := range []string{"config", "task", "runner-config", "revision", "out"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s required", key)
		}
	}
	c, e := config(f.Get("config"))
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
	authority, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	task, e := store.FindTask(w, f.Get("task"))
	if e != nil {
		return denied()
	}
	bound, e := authority.OwnerTaskBinding(owner, task.ID)
	if e != nil || bound.ProjectID != task.Project {
		return denied()
	}
	var runner runnerProfile
	if privateJSON(f.Get("runner-config"), &runner) != nil || runner.Repository != bound.Repository || runner.Authority != c.Authority || runner.InstallationID != c.InstallationID {
		return denied()
	}
	policy, e := application.NativeRoutedReviewPolicyFromProfile(w.ID, task.Project, task.ID, f.Get("revision"), f.Get("runner-config"))
	if e != nil {
		return denied()
	}
	file, e := reserve(f.Get("out"))
	if e != nil {
		return e
	}
	// Retain cleanup on errors; check the successful close before reporting success.
	defer func() { _ = file.Close() }()
	raw, e := json.Marshal(policy)
	if e != nil {
		return denied()
	}
	if e = writePrivate(file, raw); e != nil {
		return e
	}
	if e = file.Close(); e != nil {
		return denied()
	}
	fmt.Fprintln(ctx.Stdout, "Private routed review policy pinned; no check executed or acceptance authorized.")
	return nil
}
func verifierInspect(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection", "runner-config", "request", "json")
	if e != nil {
		return e
	}
	for _, key := range []string{"connection", "runner-config", "request"} {
		if f.Get(key) == "" {
			return clikit.Usagef("--%s required", key)
		}
	}
	var request verificationRequest
	if privateJSON(f.Get("request"), &request) != nil || request.SchemaVersion != 1 {
		return denied()
	}
	profile, e := application.OpenNativeVerificationProfile(f.Get("connection"), f.Get("runner-config"))
	if e != nil {
		return denied()
	}
	access, e := profile.Authenticate()
	if e != nil {
		return denied()
	}
	req := application.NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: access.Worker.ID, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	plan, e := access.InspectRouting(bounded, req)
	if e != nil {
		return denied()
	}
	result := struct {
		Schema              string `json:"schema"`
		Plan                any    `json:"plan"`
		ExecutionAuthorized bool   `json:"execution_authorized"`
	}{"native-verification-routing/v1", plan, false}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "Exact native routing plan %s: %d required checks; execution not authorized by inspection.\n", plan.Digest, len(plan.RequiredGateIDs))
	return nil
}
