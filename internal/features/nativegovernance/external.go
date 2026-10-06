package nativegovernance

import (
	"context"
	"errors"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ExternalCommands requires independent, private owner selections. A worker
// session never receives an external-action grant through these routes.
func ExternalCommands() []clikit.Command {
	var commands []clikit.Command
	for _, op := range []string{"upgrade", "configure", "inspect", "apply", "reconcile"} {
		operation := op
		commands = append(commands, clikit.Command{Path: "native external " + operation, Brief: "Owner-only durable optional GitHub recovery", Mutates: operation != "inspect", JSON: operation != "upgrade", Usage: "dacli native external " + operation + " --config <private-owner> [--review-policy <private-file> --external-profile <private-file> --request <private-file> --operation <id>]", Run: func(c *clikit.Ctx, args []string) error { return externalCommand(c, args, operation) }})
	}
	return commands
}
func ExternalAuthenticatesExplicitly(path string) bool {
	switch path {
	case "native external upgrade", "native external configure", "native external inspect", "native external apply", "native external reconcile":
		return true
	}
	return false
}
func externalCommand(ctx *clikit.Ctx, args []string, op string) error {
	f, e := flags(args, "config", "review-policy", "external-profile", "request", "operation")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config required")
	}
	if op == "upgrade" {
		if f.Get("review-policy") != "" || f.Get("external-profile") != "" || f.Get("request") != "" || f.Get("operation") != "" {
			return clikit.Usagef("upgrade accepts only --config")
		}
		owner, e := application.OpenNativeRecovery(f.Get("config"))
		if e != nil {
			return denied()
		}
		if e = owner.UpgradeExternal(); e != nil {
			return denied()
		}
		fmt.Fprintln(ctx.Stdout, "Native external authority explicitly upgraded; previous records preserved.")
		return nil
	}
	if f.Get("review-policy") == "" || f.Get("external-profile") == "" {
		return clikit.Usagef("--review-policy and --external-profile required")
	}
	owner, e := application.OpenNativeExternalOwner(f.Get("config"), f.Get("review-policy"), f.Get("external-profile"))
	if e != nil {
		return denied()
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var output any
	switch op {
	case "configure":
		if f.Get("request") != "" || f.Get("operation") != "" {
			return clikit.Usagef("configure accepts only owner selections")
		}
		p, err := owner.Configure(bounded)
		e = err
		output = externalPublicPolicy(p)
	case "inspect":
		if f.Get("request") != "" {
			return clikit.Usagef("inspect takes optional --operation")
		}
		if f.Get("operation") == "" {
			p, err := owner.InspectPolicy()
			e = err
			output = externalPublicPolicy(p)
		} else {
			r, err := owner.Inspect(f.Get("operation"))
			e = err
			output = externalPublicRecord(r, true)
		}
	case "apply", "reconcile":
		if f.Get("request") == "" || f.Get("operation") != "" {
			return clikit.Usagef("--request required")
		}
		var r sessions.NativeExternalInput
		if privateJSON(f.Get("request"), &r) != nil {
			return denied()
		}
		var result sessions.NativeExternalResult
		if op == "apply" {
			result, e = owner.Apply(bounded, r)
		} else {
			result, e = owner.Reconcile(bounded, r)
		}
		output = externalPublicRecord(result.Record, result.Duplicate)
	default:
		return clikit.Usagef("unsupported external operation")
	}
	if errors.Is(e, sessions.ErrReconcile) {
		return clikit.Refusedf("external outcome unknown; inspect/reconcile original operation without relaunch; protection retained")
	}
	if e != nil {
		return denied()
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, output)
	}
	fmt.Fprintln(ctx.Stdout, output)
	return nil
}
func externalPublicPolicy(p sessions.NativeExternalPolicy) any {
	return struct {
		Project, Task, Revision, Head, Commit, Tree, Target, Trunk, Next string
		Generation, SliceGeneration                                      int
		Required                                                         bool
	}{p.ProjectID, p.TaskID, p.Revision, p.SourceBranch, p.Commit, p.Tree, p.TargetBranch, p.ExpectedTrunk, "run trusted verification and separate review after this configuration epoch; external merge is not local integration or acceptance", p.Generation, p.SliceGeneration, p.Required}
}
func externalPublicRecord(r sessions.NativeExternalRecord, duplicate bool) any {
	return struct {
		Operation, Task, Action, State, Commit, Trunk, PRURL, PRState, Class, Next, Receipt, Freshness string
		ObservedAt                                                                                     time.Time
		PRNumber, Generation, SliceGeneration                                                          int
		Duplicate, RequiredChecksSatisfied                                                             bool
	}{r.OperationKey, r.Policy.TaskID, r.Input.Action, r.State, r.Policy.Commit, r.Outcome.Trunk, r.Outcome.PullRequestURL, r.Outcome.PullRequestState, r.Outcome.Diagnosis.Code, r.Outcome.Diagnosis.Next, r.ReceiptDigest, sessions.NativeExternalFreshness(r.Outcome), r.Outcome.ObservedAt, r.Outcome.PullRequestNumber, r.Policy.Generation, r.Policy.SliceGeneration, duplicate, r.Outcome.ChecksSatisfied}
}
