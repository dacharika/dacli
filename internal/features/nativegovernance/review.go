package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reviewer authority is newly issued by the owner, never copied from a worker.
// All four outputs are exclusive; after issuance even incomplete files remain
// as recovery evidence, together with safe session/grant identifiers.
func reviewerAdd(ctx *clikit.Ctx, args []string) error {
	allowed := []string{"config", "task", "worker-session", "runner-config", "policy-revision", "policy-out", "review-config", "connection", "credential", "expires"}
	f, e := flags(args, allowed...)
	if e != nil {
		return e
	}
	for _, key := range allowed {
		if key != "expires" && f.Get(key) == "" {
			return clikit.Usagef("--%s is required", key)
		}
	}
	if !application.VerificationID(f.Get("worker-session")) || !application.VerificationID(f.Get("policy-revision")) {
		return denied()
	}
	lifetime, e := verificationLifetime(f)
	if e != nil {
		return e
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
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	task, e := store.FindTask(w, f.Get("task"))
	if e != nil {
		return denied()
	}
	binding, e := a.OwnerTaskBinding(owner, task.ID)
	if e != nil || binding.ProjectID != task.Project {
		return denied()
	}
	var runner application.NativeRunnerProfile
	if privateJSON(f.Get("runner-config"), &runner) != nil || runner.SchemaVersion != 1 || runner.WorkspaceID != w.ID || runner.InstallationID != c.InstallationID || runner.TaskID != task.ID {
		return denied()
	}
	rw, e := native.ResolveWorkspace(runner.Workspace, w.ID)
	if e != nil || rw.Root != w.Root {
		return denied()
	}
	for _, path := range []string{c.Authority, runner.Authority} {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || !filepath.IsAbs(path) {
			return denied()
		}
	}
	authority, e := filepath.EvalSymlinks(c.Authority)
	other, re := filepath.EvalSymlinks(runner.Authority)
	if e != nil || re != nil || authority != other {
		return denied()
	}
	runnerToken, e := native.ReadCredential(runner.CredentialFile)
	if e != nil {
		return denied()
	}
	principal, e := a.Authenticate(runnerToken, runner.RunnerSessionID)
	if e != nil || principal.Parent != "" || len(principal.Scope.Actions) != 1 || principal.Scope.Actions[0] != "verification.run" || len(principal.Scope.Resources) != 1 || principal.Scope.Resources[0] != "task:"+task.ID {
		return denied()
	}
	observation, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil || observation.Repository != binding.Repository || runner.Repository != binding.Repository {
		return denied()
	}
	policy, e := application.NativeReviewPolicyFromChecks(w.ID, task.Project, task.ID, f.Get("policy-revision"), runner.Checks)
	if e != nil {
		return denied()
	}
	observer, e := application.NewNativeReviewObserver(w, policy)
	if e != nil {
		return denied()
	}
	if _, e = observer.ObserveReview(w.ID, task.Project, task.ID); e != nil {
		return denied()
	}
	profile := application.NativeReviewerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: c.Authority, InstallationID: c.InstallationID, ProjectID: task.Project, TaskID: task.ID, Repository: binding.Repository, WorkerSessionID: f.Get("worker-session"), CredentialFile: f.Get("credential"), PolicyFile: f.Get("policy-out")}
	budget := profile
	budget.ReviewerSessionID = strings.Repeat("x", 256)
	budget.GrantID = strings.Repeat("x", 256)
	projected, e := json.Marshal(budget)
	policyBytes, pe := json.Marshal(policy)
	if e != nil || pe != nil || len(projected) > 65536 || len(policyBytes) > 65536 {
		return denied()
	}
	budgetConnection := native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: strings.Repeat("x", 256), CredentialFile: f.Get("credential")}
	connectionBytes, e := json.Marshal(budgetConnection)
	if e != nil || len(connectionBytes) > 16384 {
		return denied()
	}
	paths := []string{f.Get("policy-out"), f.Get("review-config"), f.Get("connection"), f.Get("credential")}
	files := make([]*os.File, 0, len(paths))
	issued := false
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
		if !issued {
			for i := range files {
				_ = os.Remove(paths[i])
			}
		}
	}()
	for _, path := range paths {
		file, e := reserve(path)
		if e != nil {
			return e
		}
		files = append(files, file)
	}
	if e = writePrivate(files[0], policyBytes); e != nil {
		return e
	}
	reviewAuthority, _, e := application.OpenNativeReviewAuthority(w.Root, w.ID, c.Authority, c.InstallationID, paths[0])
	if e != nil {
		return denied()
	}
	expires := time.Now().Add(lifetime)
	reviewer, token, e := reviewAuthority.Enroll(owner, sessions.Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"workspace:" + w.ID, "task:" + task.ID}}, expires)
	if e != nil {
		return denied()
	}
	issued = true
	fmt.Fprintf(ctx.Stderr, "Native reviewer %s issued; preserve private files and authority for owner recovery if setup is interrupted.\n", reviewer.ID)
	profile.ReviewerSessionID = reviewer.ID
	if e = writePrivate(files[3], []byte(token+"\n")); e != nil {
		return e
	}
	if e = saveReviewProfile(files[1], profile); e != nil {
		return e
	}
	connection := native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: reviewer.ID, CredentialFile: paths[3]}
	b, e := json.Marshal(connection)
	if e != nil || len(b) > 16384 {
		return denied()
	}
	if e = writePrivate(files[2], b); e != nil {
		return e
	}
	grant, e := reviewAuthority.GrantReview(owner, sessions.ReviewGrantRequest{ReviewerSession: reviewer.ID, WorkerSession: f.Get("worker-session"), TaskID: task.ID, Repository: binding.Repository, ExpiresAt: expires})
	if e != nil {
		return denied()
	}
	fmt.Fprintf(ctx.Stderr, "Native review grant %s issued.\n", grant.ID)
	profile.GrantID = grant.ID
	if e = saveReviewProfile(files[1], profile); e != nil {
		return e
	}
	fmt.Fprintln(ctx.Stdout, "Native independent reviewer configured; private policy, profile, connection and credential saved.")
	return nil
}
func saveReviewProfile(f *os.File, p application.NativeReviewerProfile) error {
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
