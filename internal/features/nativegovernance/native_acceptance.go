package nativegovernance

import (
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func nativeAcceptanceUpgrade(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
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
	a, e := sessions.OpenCurrent(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil || a.UpgradeNativeAcceptance(owner) != nil {
		return denied()
	}
	fmt.Fprintln(ctx.Stdout, "Native canonical acceptance schema upgraded.")
	return nil
}
func nativeAcceptanceCandidate(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection", "review-config", "review")
	if e != nil {
		return e
	}
	for _, k := range []string{"connection", "review-config", "review"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s required", k)
		}
	}
	p, e := application.OpenNativeReviewProfile(f.Get("connection"), f.Get("review-config"))
	if e != nil {
		return denied()
	}
	a, e := p.Authenticate()
	if e != nil {
		return denied()
	}
	candidate, e := a.AcceptanceCandidate(f.Get("review"))
	if e != nil {
		return denied()
	}
	result := struct {
		Schema    string                             `json:"schema"`
		Candidate sessions.NativeAcceptanceCandidate `json:"candidate"`
	}{"native-acceptance-candidate/v1", candidate}
	ctx.Result = result
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintf(ctx.Stdout, "Native acceptance candidate %s for %s; current independent producer eligibility observed.\n", candidate.Input.ReviewID, candidate.Input.TaskID)
	return nil
}
