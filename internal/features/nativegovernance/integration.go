package nativegovernance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

// IntegrationCommands exposes separately owner-authorized local operations.
// These routes never provide a worker MCP merge, push, deploy or release grant.
func IntegrationCommands() []clikit.Command {
	var result []clikit.Command
	for _, operation := range []string{"upgrade", "configure", "inspect", "apply", "reconcile"} {
		op := operation
		result = append(result, clikit.Command{Path: "native integration " + op, Brief: "Owner-only durable local fast-forward integration", Mutates: op != "inspect", JSON: op != "upgrade", Usage: "dacli native integration " + op + " --config <private-owner> [--review-policy <private-file> --integration-policy <private-file> --request <private-file> --operation <id>]", Run: func(ctx *clikit.Ctx, args []string) error { return integrationCommand(ctx, args, op) }})
	}
	return result
}
func IntegrationAuthenticatesExplicitly(path string) bool {
	switch path {
	case "native integration upgrade", "native integration configure", "native integration inspect", "native integration apply", "native integration reconcile":
		return true
	}
	return false
}
func integrationCommand(ctx *clikit.Ctx, args []string, op string) error {
	f, e := flags(args, "config", "review-policy", "integration-policy", "request", "operation")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
	}
	if op == "upgrade" {
		if f.Get("review-policy") != "" || f.Get("integration-policy") != "" || f.Get("request") != "" || f.Get("operation") != "" {
			return clikit.Usagef("upgrade accepts only --config")
		}
		access, e := application.OpenNativeRecovery(f.Get("config"))
		if e != nil {
			return denied()
		}
		if e = access.UpgradeIntegration(); e != nil {
			return denied()
		}
		fmt.Fprintln(ctx.Stdout, "Native integration authority upgraded explicitly; prior records preserved.")
		return nil
	}
	if f.Get("review-policy") == "" || f.Get("integration-policy") == "" {
		return clikit.Usagef("--review-policy and --integration-policy are required")
	}
	owner, e := application.OpenNativeIntegrationOwner(f.Get("config"), f.Get("review-policy"), f.Get("integration-policy"))
	if e != nil {
		return denied()
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var result any
	switch op {
	case "configure":
		if f.Get("request") != "" || f.Get("operation") != "" {
			return clikit.Usagef("configure takes only owner/review/integration selections")
		}
		configured, err := owner.Configure(bounded)
		e = err
		result = integrationPolicyRequirements(configured)
	case "inspect":
		if f.Get("request") != "" {
			return clikit.Usagef("inspect accepts an optional --operation, not --request")
		}
		if f.Get("operation") == "" {
			policy, err := owner.InspectPolicy()
			e = err
			result = integrationPolicyRequirements(policy)
		} else {
			record, err := owner.Inspect(f.Get("operation"))
			e = err
			result = integrationPublicResult(record, false)
		}
	case "apply", "reconcile":
		if f.Get("request") == "" || f.Get("operation") != "" {
			return clikit.Usagef("--request is required")
		}
		var input sessions.NativeIntegrationInput
		if privateJSON(f.Get("request"), &input) != nil {
			return denied()
		}
		var outcome sessions.NativeIntegrationResult
		if op == "apply" {
			outcome, e = owner.Apply(bounded, input)
		} else {
			outcome, e = owner.Reconcile(bounded, input)
		}
		result = integrationPublicResult(outcome.Record, outcome.Duplicate)
	default:
		return clikit.Usagef("unsupported integration operation")
	}
	if errors.Is(e, sessions.ErrReconcile) {
		return clikit.Refusedf("native integration outcome unknown; protection retained; inspect/reconcile the exact operation without rerunning its effect")
	}
	if e != nil {
		return denied()
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, result)
	}
	fmt.Fprintln(ctx.Stdout, result)
	return nil
}
func integrationPublicResult(r sessions.NativeIntegrationRecord, duplicate bool) any {
	return struct {
		Operation, Task, State, Commit, Tree, Receipt string
		Duplicate                                     bool
	}{r.OperationKey, r.Input.Candidate.TaskID, r.State, r.Outcome.Commit, r.Outcome.Tree, r.ReceiptDigest, duplicate}
}

func integrationPolicyRequirements(p sessions.NativeIntegrationPolicy) any {
	return struct {
		State, Project, Task, Revision, ExpectedTrunkCommit, ExpectedTrunkTree, NativePolicyRevision, NextAction string
		ProducersAfter, ExpiresAt                                                                                time.Time
	}{"configured", p.ProjectID, p.TaskID, p.Revision, p.ExpectedCommit, p.ExpectedTree, p.NativePolicyRevision, "run trusted verification and independent review after this epoch for the exact resulting committed tree; supply their native acceptance candidate to owner apply", p.ConfiguredAt, p.ExpiresAt}
}
