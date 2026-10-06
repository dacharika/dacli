package nativegovernance

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

// Refresh preserves the separately enrolled reviewer credential and old private
// profile. Exclusive output makes interruption recoverable through exact replay.
func controllerRefreshReview(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "review-config", "request", "out", "json")
	if e != nil {
		return e
	}
	for _, key := range []string{"config", "review-config", "request", "out"} {
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
	var p application.NativeReviewerProfile
	if privateJSON(f.Get("review-config"), &p) != nil || p.SchemaVersion != 1 || p.WorkspaceID != w.ID || p.InstallationID != c.InstallationID {
		return denied()
	}
	otherWorkspace, e := native.ResolveWorkspace(p.Workspace, w.ID)
	if e != nil || otherWorkspace.Root != w.Root {
		return denied()
	}
	for _, path := range []string{c.Authority, p.Authority} {
		info, err := os.Lstat(path)
		if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() {
			return denied()
		}
	}
	authority, e := filepath.EvalSymlinks(c.Authority)
	other, pe := filepath.EvalSymlinks(p.Authority)
	if e != nil || pe != nil || authority != other {
		return denied()
	}
	var request sessions.ControllerReviewRefreshRequest
	if e = nativePolicyRequest(f, &request); e != nil {
		return e
	}
	if request.ProjectID != p.ProjectID || request.TaskID != p.TaskID || request.ExpectedGrantID != p.GrantID || request.ReviewerSessionID != p.ReviewerSessionID || request.WorkerSessionID != p.WorkerSessionID || request.Repository != p.Repository {
		return denied()
	}
	a, policy, e := application.OpenNativeReviewAuthority(w.Root, w.ID, c.Authority, c.InstallationID, p.PolicyFile)
	if e != nil || policy.ProjectID != p.ProjectID || policy.TaskID != p.TaskID {
		return denied()
	}
	token, e := native.ReadCredential(p.CredentialFile)
	if e != nil {
		return denied()
	}
	if _, e = a.Authenticate(token, p.ReviewerSessionID); e != nil {
		return denied()
	}
	file, e := reserve(f.Get("out"))
	if e != nil {
		return e
	}
	issued := false
	defer func() {
		_ = file.Close()
		if !issued {
			_ = os.Remove(f.Get("out"))
		}
	}()
	result, e := a.RefreshControllerReview(owner, request)
	if e != nil {
		return nativePolicyError(e)
	}
	issued = true
	p.GrantID = result.GrantID
	if e = saveReviewProfile(file, p); e != nil {
		fmt.Fprintf(ctx.Stderr, "Review grant refresh %s retained; preserve output and replay the original owner request to a new private file.\n", result.GrantID)
		return e
	}
	return controllerEmit(ctx, result)
}
