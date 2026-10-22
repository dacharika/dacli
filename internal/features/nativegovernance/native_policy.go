package nativegovernance

import (
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
)

func nativePolicyAuthority(path string) (*sessions.Authority, string, error) {
	c, e := config(path)
	if e != nil {
		return nil, "", e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	owner, e := native.ReadCredential(c.OwnerCredentialFile)
	if e != nil {
		return nil, "", sessions.ErrDenied
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return nil, "", e
	}
	if e = a.AuthenticateOwner(owner); e != nil {
		return nil, "", e
	}
	return a, owner, nil
}
func nativePolicyError(e error) error {
	if errors.Is(e, sessions.ErrDenied) {
		return denied()
	}
	return e
}
func nativePolicyFlags(args []string, allowed ...string) (*clikit.Flags, error) {
	f, e := flags(args, allowed...)
	if e != nil {
		return nil, e
	}
	if f.Get("config") == "" {
		return nil, clikit.Usagef("--config required")
	}
	return f, nil
}
func nativePolicyUpgrade(ctx *clikit.Ctx, args []string) error {
	f, e := nativePolicyFlags(args, "config")
	if e != nil {
		return e
	}
	a, o, e := nativePolicyAuthority(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	if e = a.UpgradeNativePolicy(o); e != nil {
		return nativePolicyError(e)
	}
	fmt.Fprintln(ctx.Stdout, "Native policy schema upgraded.")
	return nil
}
func nativePolicyRequest(f *clikit.Flags, out any) error {
	if f.Get("request") == "" {
		return clikit.Usagef("--request required")
	}
	raw, e := nativeconfig.ReadPrivate(f.Get("request"), 65536)
	if e != nil || nativeconfig.Decode(raw, out) != nil {
		return denied()
	}
	return nil
}
func nativePolicyConfigure(ctx *clikit.Ctx, args []string) error {
	f, e := nativePolicyFlags(args, "config", "request")
	if e != nil {
		return e
	}
	var p sessions.NativePolicy
	if e = nativePolicyRequest(f, &p); e != nil {
		return e
	}
	a, o, e := nativePolicyAuthority(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	if e = a.ConfigureNativePolicy(o, p); e != nil {
		return nativePolicyError(e)
	}
	fmt.Fprintln(ctx.Stdout, "Native policy configured.")
	return nil
}
func nativePolicyEmit(ctx *clikit.Ctx, value any) error {
	out := struct {
		Schema string `json:"schema"`
		Result any    `json:"result"`
	}{"native-policy/v1", value}
	ctx.Result = out
	if ctx.JSON {
		return clikit.EmitJSON(ctx, out)
	}
	fmt.Fprintln(ctx.Stdout, "Native policy history recorded/observed; no bearer action authority.")
	return nil
}
func nativePolicyEvaluate(ctx *clikit.Ctx, args []string) error {
	f, e := nativePolicyFlags(args, "config", "review-policy", "request")
	if e != nil {
		return e
	}
	if f.Get("review-policy") == "" {
		return clikit.Usagef("--review-policy required")
	}
	var r sessions.NativePolicyEvaluationRequest
	if e = nativePolicyRequest(f, &r); e != nil {
		return e
	}
	p, e := application.OpenNativeAcceptanceOwner(f.Get("config"), f.Get("review-policy"))
	if e != nil {
		return nativePolicyError(e)
	}
	out, e := p.EvaluatePolicy(r)
	if e != nil {
		return nativePolicyError(e)
	}
	return nativePolicyEmit(ctx, out)
}
func nativePolicyApprove(ctx *clikit.Ctx, args []string) error {
	f, e := nativePolicyFlags(args, "config", "request")
	if e != nil {
		return e
	}
	var r sessions.NativePolicyApprovalRequest
	if e = nativePolicyRequest(f, &r); e != nil {
		return e
	}
	a, o, e := nativePolicyAuthority(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	out, e := a.ApproveNativePolicy(o, r)
	if e != nil {
		return nativePolicyError(e)
	}
	return nativePolicyEmit(ctx, out)
}
func nativePolicyStatus(ctx *clikit.Ctx, args []string) error {
	f, e := nativePolicyFlags(args, "config", "decision")
	if e != nil {
		return e
	}
	if f.Get("decision") == "" {
		return clikit.Usagef("--decision required")
	}
	a, o, e := nativePolicyAuthority(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	out, e := a.ReadNativePolicyStatus(o, f.Get("decision"))
	if e != nil {
		return nativePolicyError(e)
	}
	return nativePolicyEmit(ctx, out)
}
