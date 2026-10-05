package nativegovernance

import (
	"encoding/json"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"time"
)

func controllerEmit(ctx *clikit.Ctx, value any) error {
	out := struct {
		Schema string `json:"schema"`
		Result any    `json:"result"`
	}{"native-controller/v1", value}
	ctx.Result = out
	if ctx.JSON {
		return clikit.EmitJSON(ctx, out)
	}
	fmt.Fprintln(ctx.Stdout, "Bounded native controller state recorded; external actions require their own authenticated producers.")
	return nil
}
func controllerOwner(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "config", "request", "json")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
	}
	owner, e := application.OpenNativeControllerOwner(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	if kind == "upgrade" {
		if f.Get("request") != "" {
			return clikit.Usagef("upgrade does not accept --request")
		}
		if e = owner.Upgrade(); e != nil {
			return nativePolicyError(e)
		}
		fmt.Fprintln(ctx.Stdout, "Native controller authority upgraded explicitly to schema16.")
		return nil
	}
	switch kind {
	case "configure":
		var r sessions.ControllerPolicy
		if e = nativePolicyRequest(f, &r); e != nil {
			return e
		}
		if e = owner.Configure(r); e != nil {
			return nativePolicyError(e)
		}
		return controllerEmit(ctx, r)
	case "start":
		var r sessions.ControllerStartRequest
		if e = nativePolicyRequest(f, &r); e != nil {
			return e
		}
		out, e := owner.Start(r)
		if e != nil {
			return nativePolicyError(e)
		}
		return controllerEmit(ctx, out)
	}
	return clikit.Usagef("unknown controller owner operation")
}
func controllerCommand(ctx *clikit.Ctx, args []string, kind string) error {
	f, e := flags(args, "connection", "capability-file", "request", "project", "invocation", "json")
	if e != nil {
		return e
	}
	if f.Get("connection") == "" || f.Get("capability-file") == "" {
		return clikit.Usagef("--connection and --capability-file required")
	}
	access, e := application.OpenNativeController(f.Get("connection"), f.Get("capability-file"))
	if e != nil {
		return nativePolicyError(e)
	}
	var out sessions.ControllerStatus
	if kind == "status" {
		if f.Get("project") == "" || f.Get("invocation") == "" || f.Get("request") != "" {
			return clikit.Usagef("status requires --project and --invocation, no --request")
		}
		out, e = access.Status(f.Get("project"), f.Get("invocation"))
	} else {
		if f.Get("project") != "" || f.Get("invocation") != "" {
			return clikit.Usagef("step/reconcile use --request only")
		}
		var r sessions.ControllerStepRequest
		if e = nativePolicyRequest(f, &r); e != nil {
			return e
		}
		if kind == "step" {
			out, e = access.Step(r)
		} else {
			out, e = access.Reconcile(r)
		}
	}
	if out.ID != "" {
		if emitErr := controllerEmit(ctx, out); emitErr != nil {
			return emitErr
		}
	}
	if e != nil {
		return nativePreflightFailure(e, "native controller refused: inspect typed halt and original immutable operation; never retry policy refusal unchanged")
	}
	return nil
}

// Enrollment is owner-only and derives current policy tasks/claims; no caller
// supplies arbitrary actions or acquires a worker, runner or reviewer credential.
func controllerEnroll(ctx *clikit.Ctx, args []string) error {
	return controllerEnrollFiles(ctx, args, writePrivate)
}

// The file writer boundary permits testing real post-issuance filesystem failures.
func controllerEnrollFiles(ctx *clikit.Ctx, args []string, write func(*os.File, []byte) error) error {
	f, e := flags(args, "config", "project", "connection", "credential", "expires")
	if e != nil {
		return e
	}
	for _, k := range []string{"config", "project", "connection", "credential"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s required", k)
		}
	}
	lifetime := 8 * time.Hour
	if f.Get("expires") != "" {
		lifetime, e = time.ParseDuration(f.Get("expires"))
		if e != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most24h")
		}
	}
	owner, e := application.OpenNativeControllerOwner(f.Get("config"))
	if e != nil {
		return nativePolicyError(e)
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	w, e := nativeconfig.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	credential, e := reserve(f.Get("credential"))
	if e != nil {
		return e
	}
	issued := false
	defer func() {
		_ = credential.Close()
		if !issued {
			_ = os.Remove(f.Get("credential"))
		}
	}()
	connection, e := reserve(f.Get("connection"))
	if e != nil {
		return e
	}
	defer func() {
		_ = connection.Close()
		if !issued {
			_ = os.Remove(f.Get("connection"))
		}
	}()
	session, token, e := owner.Enroll(f.Get("project"), time.Now().Add(lifetime))
	if e != nil {
		return nativePolicyError(e)
	}
	issued = true
	completed := false
	defer func() {
		if !completed {
			fmt.Fprintf(ctx.Stderr, "Native bounded controller enrollment %s retained; preserve private connection %s and credential %s for owner reconciliation.\n", session.ID, f.Get("connection"), f.Get("credential"))
		}
	}()
	if e = write(credential, []byte(token+"\n")); e != nil {
		return e
	}
	data, e := json.Marshal(nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: session.ID, CredentialFile: f.Get("credential")})
	if e != nil {
		return e
	}
	if e = write(connection, data); e != nil {
		return e
	}
	completed = true
	fmt.Fprintf(ctx.Stdout, "Native bounded controller %s enrolled with exact owner policy task scope.\n", session.ID)
	return nil
}
