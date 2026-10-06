// Package nativegovernance adapts explicit native authority; never legacy ambient identity.
package nativegovernance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
)

var Commands = append(append(append(append([]clikit.Command{
	{Path: "native controller refresh-review", Brief: "Owner refresh exact independent review grant after checklist progress", Mutates: true, JSON: true, Usage: "dacli native controller refresh-review --config <owner> --review-config <private-profile> --request <private-file> --out <new-private-profile> --json", Run: controllerRefreshReview},
	{Path: "native controller upgrade", Brief: "upgrade durable bounded native controller", Mutates: true, JSON: false, Usage: "dacli native controller upgrade --config <owner>", Run: func(c *clikit.Ctx, a []string) error { return controllerOwner(c, a, "upgrade") }},
	{Path: "native controller configure", Brief: "configure durable bounded native controller", Mutates: true, JSON: true, Usage: "dacli native controller configure --config <owner> --request <private-file>", Run: func(c *clikit.Ctx, a []string) error { return controllerOwner(c, a, "configure") }},
	{Path: "native controller start", Brief: "start durable bounded native controller", Mutates: true, JSON: true, Usage: "dacli native controller start --config <owner> --request <private-file>", Run: func(c *clikit.Ctx, a []string) error { return controllerOwner(c, a, "start") }},
	{Path: "native controller step", Brief: "step durable bounded native controller", Mutates: true, JSON: true, Usage: "dacli native controller step --connection <private-file> --capability-file <private-file> --request <private-file>", Run: func(c *clikit.Ctx, a []string) error { return controllerCommand(c, a, "step") }},
	{Path: "native controller reconcile", Brief: "reconcile durable bounded native controller", Mutates: true, JSON: true, Usage: "dacli native controller reconcile --connection <private-file> --capability-file <private-file> --request <private-file>", Run: func(c *clikit.Ctx, a []string) error { return controllerCommand(c, a, "reconcile") }},
	{Path: "native controller status", Brief: "status durable bounded native controller", Mutates: false, JSON: true, Usage: "dacli native controller status --connection <private-file> --capability-file <private-file> --project <id> --invocation <id>", Run: func(c *clikit.Ctx, a []string) error { return controllerCommand(c, a, "status") }},
	{Path: "native storage observe", Brief: "Owner-only native authority inventory", JSON: true, Usage: "dacli native storage observe --config <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return storageCommand(c, a, "observe") }},
	{Path: "native storage experiment", Brief: "Import an isolated non-authoritative storage projection", Mutates: true, JSON: true, Usage: "dacli native storage experiment --config <private-file> --destination <separate-private-dir> --mode projection-only --json", Run: func(c *clikit.Ctx, a []string) error { return storageCommand(c, a, "experiment") }},
	{Path: "native storage retention", Brief: "Dry-run exact artifacts with native evidence and protection holds", JSON: true, Usage: "dacli native storage retention --config <private-file> --project <id> --json", Run: func(c *clikit.Ctx, a []string) error { return storageCommand(c, a, "retention") }},
	{Path: "native controller enroll", Brief: "enroll durable bounded native controller", Mutates: true, JSON: false, Usage: "dacli native controller enroll --config <owner> --project <id> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: controllerEnroll},
	{Path: "native policy upgrade", Brief: "Explicit owner upgrades native policy", Mutates: true, Usage: "dacli native policy upgrade --config <owner>", Run: nativePolicyUpgrade},
	{Path: "native policy configure", Brief: "Owner configures exact native action policy", Mutates: true, Usage: "dacli native policy configure --config <owner> --request <policy>", Run: nativePolicyConfigure},
	{Path: "native policy evaluate", Brief: "Owner evaluates exact native acceptance without canonical effect", Mutates: true, JSON: true, Usage: "dacli native policy evaluate --config <owner> --review-policy <policy> --request <evaluation>", Run: nativePolicyEvaluate},
	{Path: "native policy approve", Brief: "Owner records exact decision approval", Mutates: true, JSON: true, Usage: "dacli native policy approve --config <owner> --request <approval>", Run: nativePolicyApprove},
	{Path: "native policy status", Brief: "Owner observes historical decision and outcome", JSON: true, Usage: "dacli native policy status --config <owner> --decision <id>", Run: nativePolicyStatus},
	{Path: "native acceptance upgrade", Brief: "Explicit owner upgrades durable canonical acceptance", Mutates: true, Usage: "dacli native acceptance upgrade --config <owner>", Run: nativeAcceptanceUpgrade},
	{Path: "native acceptance candidate", Brief: "Independent authenticated reviewer exports exact eligible producers", JSON: true, Usage: "dacli native acceptance candidate --connection <reviewer> --review-config <profile> --review <id>", Run: nativeAcceptanceCandidate},
	{Path: "native budget upgrade", Brief: "upgrade owner-estimated native budget policy", Mutates: true, JSON: false, Usage: "dacli native budget upgrade --config <private-file>", Run: func(c *clikit.Ctx, a []string) error { return budgetOwner(c, a, "upgrade") }},
	{Path: "native budget configure", Brief: "configure owner-estimated native budget policy", Mutates: true, JSON: true, Usage: "dacli native budget configure --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return budgetOwner(c, a, "configure") }},
	{Path: "native budget quote", Brief: "quote owner-estimated native budget allocations", Mutates: false, JSON: true, Usage: "dacli native budget quote --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return budgetCommand(c, a, "quote") }},
	{Path: "native budget status", Brief: "status owner-estimated native budget allocations", Mutates: false, JSON: true, Usage: "dacli native budget status --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return budgetCommand(c, a, "status") }},
	{Path: "native budget settle", Brief: "settle owner-estimated native budget allocations", Mutates: true, JSON: true, Usage: "dacli native budget settle --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return budgetCommand(c, a, "settle") }},
	{Path: "native budget controller add", Brief: "owner enroll scoped budget controller", Mutates: true, Usage: "dacli native budget controller add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: budgetControllerAdd},
	{Path: "native checkpoint upgrade", Brief: "upgrade native durable checkpoint policy", Mutates: true, JSON: false, Usage: "dacli native checkpoint upgrade --config <private-file>", Run: func(c *clikit.Ctx, a []string) error { return checkpointOwner(c, a, "upgrade") }},
	{Path: "native checkpoint configure", Brief: "configure native durable checkpoint policy", Mutates: true, JSON: true, Usage: "dacli native checkpoint configure --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return checkpointOwner(c, a, "configure") }},
	{Path: "native checkpoint record", Brief: "record durable bounded checkpoint", Mutates: true, JSON: true, Usage: "dacli native checkpoint record --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return checkpointCommand(c, a, "record") }},
	{Path: "native checkpoint reconcile", Brief: "reconcile durable bounded checkpoint", Mutates: true, JSON: true, Usage: "dacli native checkpoint reconcile --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return checkpointCommand(c, a, "reconcile") }},
	{Path: "native checkpoint status", Brief: "status durable bounded checkpoint", Mutates: false, JSON: true, Usage: "dacli native checkpoint status --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return checkpointCommand(c, a, "status") }},
	{Path: "native checkpoint controller add", Brief: "controller add native checkpoint session", Mutates: true, Usage: "dacli native checkpoint controller add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: checkpointControllerAdd},
	{Path: "native preflight upgrade", Brief: "upgrade complete-loop native preflight", Mutates: true, JSON: false, Usage: "dacli native preflight upgrade --config <private-file>", Run: func(c *clikit.Ctx, a []string) error { return preflightOwner(c, a, "upgrade") }},
	{Path: "native preflight configure", Brief: "configure complete-loop native preflight", Mutates: true, JSON: true, Usage: "dacli native preflight configure --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return preflightOwner(c, a, "configure") }},
	{Path: "native preflight override", Brief: "override complete-loop native preflight", Mutates: true, JSON: true, Usage: "dacli native preflight override --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return preflightOwner(c, a, "override") }},
	{Path: "native preflight check", Brief: "check complete-loop native preflight", Mutates: true, JSON: true, Usage: "dacli native preflight check --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: preflightCheck},
	{Path: "native recovery upgrade", Brief: "upgrade fenced owner native recovery", Mutates: true, JSON: false, Usage: "dacli native recovery upgrade --config <private-file>", Run: func(c *clikit.Ctx, a []string) error { return recoveryOperation(c, a, "upgrade") }},
	{Path: "native recovery configure", Brief: "configure fenced owner native recovery", Mutates: true, JSON: true, Usage: "dacli native recovery configure --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return recoveryOperation(c, a, "configure") }},
	{Path: "native recovery preview", Brief: "preview fenced owner native recovery", Mutates: false, JSON: true, Usage: "dacli native recovery preview --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return recoveryOperation(c, a, "preview") }},
	{Path: "native recovery apply", Brief: "apply fenced owner native recovery", Mutates: true, JSON: true, Usage: "dacli native recovery apply --config <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return recoveryOperation(c, a, "apply") }},
	{Path: "native lifecycle upgrade", Brief: "upgrade cooperative native lifecycle", Mutates: true, JSON: false, Usage: "dacli native lifecycle upgrade --config <private-file>", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOwner(c, a, true) }},
	{Path: "native lifecycle configure", Brief: "configure cooperative native lifecycle", Mutates: true, JSON: false, Usage: "dacli native lifecycle configure --config <private-file> --policy-file <private-file>", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOwner(c, a, false) }},
	{Path: "native lifecycle controller add", Brief: "controller add cooperative native lifecycle", Mutates: true, JSON: false, Usage: "dacli native lifecycle controller add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: func(c *clikit.Ctx, a []string) error { return swarmScopedActorAdd(c, a, false, true) }},
	{Path: "native lifecycle bridge add", Brief: "bridge add cooperative native lifecycle", Mutates: true, JSON: false, Usage: "dacli native lifecycle bridge add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: func(c *clikit.Ctx, a []string) error { return swarmScopedActorAdd(c, a, true, true) }},
	{Path: "native lifecycle observe", Brief: "observe cooperative native lifecycle", Mutates: true, JSON: true, Usage: "dacli native lifecycle observe --connection <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOperation(c, a, "observe") }},
	{Path: "native lifecycle cancel", Brief: "cancel cooperative native lifecycle", Mutates: true, JSON: true, Usage: "dacli native lifecycle cancel --connection <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOperation(c, a, "cancel") }},
	{Path: "native lifecycle cancel-ack", Brief: "cancel-ack cooperative native lifecycle", Mutates: true, JSON: true, Usage: "dacli native lifecycle cancel-ack --connection <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOperation(c, a, "cancel-ack") }},
	{Path: "native lifecycle status", Brief: "status cooperative native lifecycle", Mutates: false, JSON: true, Usage: "dacli native lifecycle status --connection <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return lifecycleOperation(c, a, "status") }},

	{Path: "native dispatch upgrade", Brief: "Owner-upgrade authority schema7 to8 preserving history", Mutates: true, Usage: "dacli native dispatch upgrade --config <private-file>", Run: dispatchUpgrade},
	{Path: "native dispatch bridge add", Brief: "Owner-enroll separate correlation-only native bridge", Mutates: true, Usage: "dacli native dispatch bridge add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: dispatchBridgeAdd},
	{Path: "native dispatch request", Brief: "request cooperative native dispatch intent", Mutates: true, JSON: true, Usage: "dacli native dispatch request --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "dispatch-request") }},
	{Path: "native dispatch correlate", Brief: "correlate cooperative native dispatch intent", Mutates: true, JSON: true, Usage: "dacli native dispatch correlate --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "dispatch-correlate") }},
	{Path: "native dispatch acknowledge", Brief: "acknowledge cooperative native dispatch intent", Mutates: true, JSON: true, Usage: "dacli native dispatch acknowledge --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "dispatch-acknowledge") }},
	{Path: "native dispatch status", Brief: "status cooperative native dispatch intent", Mutates: false, JSON: true, Usage: "dacli native dispatch status --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "dispatch-status") }},
	{Path: "native swarm upgrade", Brief: "Owner-upgrade native authority from schema 6 to 7 preserving history", Mutates: true, Usage: "dacli native swarm upgrade --config <private-file>", Run: swarmUpgrade},
	{Path: "native swarm configure", Brief: "Owner-configure pinned native swarm placements and capacity", Mutates: true, Usage: "dacli native swarm configure --config <private-file> --policy-file <private-file>", Run: swarmConfigure},
	{Path: "native swarm controller add", Brief: "Owner-enroll a separate native swarm controller", Mutates: true, Usage: "dacli native swarm controller add --config <private-file> --project <project> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: swarmControllerAdd},
	{Path: "native swarm plan", Brief: "Plan scoped native work using fresh owner-attested manual handoff capability", JSON: true, Usage: "dacli native swarm plan --connection <private-file> --capability-file <private-file> --project <project> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "plan") }},
	{Path: "native swarm reserve", Brief: "Atomically reserve a fresh native swarm plan", Mutates: true, JSON: true, Usage: "dacli native swarm reserve --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "reserve") }},
	{Path: "native swarm admit", Brief: "Freshly admit an exact reserved manual native dispatch", JSON: true, Usage: "dacli native swarm admit --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "admit") }},
	{Path: "native swarm handoff", Brief: "Convert an admitted reservation to an independently enrolled worker lease", Mutates: true, JSON: true, Usage: "dacli native swarm handoff --connection <private-file> --capability-file <private-file> --request <private-file> --json", Run: func(c *clikit.Ctx, a []string) error { return swarmOperation(c, a, "handoff") }},
	{Path: "native reviewer add", Brief: "Owner-configure a separate private native reviewer and derived named-check policy", Mutates: true, Usage: "dacli native reviewer add --config <private-file> --task <ref> --worker-session <id> --runner-config <private-file> --policy-revision <id> --policy-out <new-private-file> --review-config <new-private-file> --connection <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: reviewerAdd},
	{Path: "native verifier add", Brief: "Owner-configure private named checks with separately scoped verification authority", Mutates: true, Usage: "dacli native verifier add --config <private-file> --task <ref> --checks-file <private-file> --out <new-private-file> --credential <new-private-file> [--expires <duration>]", Run: verifierAdd},
	{Path: "native verify", Brief: "Run an owner-configured named check for a fenced native revision", Mutates: true, JSON: true, Usage: "dacli native verify --connection <private-file> --runner-config <private-file> --request <private-file> --json", Run: verify},
	{Path: "native upgrade", Brief: "Owner-upgrade native authority from schema 5 to 6 without replacing credentials", Mutates: true, Usage: "dacli native upgrade --config <private-file>", Run: upgrade},
	{Path: "native revoke", Brief: "Owner-revoke an exact native session using a private explicit owner configuration", Mutates: true, Usage: "dacli native revoke --config <private-file> --session <session-id>", Run: revoke},
	{Path: "native serve", Brief: "Serve scoped native governance MCP using a private explicit session connection", Usage: "dacli native serve --connection <private-file> [--runner-config <private-file> | --review-config <private-file> | --orchestration-config <private-file>]", Run: serve},
	{Path: "native bootstrap", Brief: "Initialize an absent native authority and privately save its owner credential", Mutates: true, Usage: "dacli native bootstrap --config <private-file>", Run: bootstrap},
	{Path: "native enroll", Brief: "Owner-enroll a scoped native session for an existing canonical task", Mutates: true, Usage: "dacli native enroll --config <private-file> --task <ref> --connection <new-private-file> --credential <new-private-file> [--path <relative-path>]... [--checkout <absolute-checkout>]... [--expires <duration>] [--verification]", Run: enroll},
}, ModelRoutingCommands()...), IntegrationCommands()...), VerificationRoutingCommands()...), ExternalCommands()...)

// AuthenticatesExplicitly lists exactly the handlers exempt from the legacy grant gate.
func AuthenticatesExplicitly(path string) bool {
	if ExternalAuthenticatesExplicitly(path) || ModelRoutingAuthenticatesExplicitly(path) || IntegrationAuthenticatesExplicitly(path) || VerificationRoutingAuthenticatesExplicitly(path) {
		return true
	}
	switch path {
	case "native storage observe", "native storage experiment", "native storage retention":
		return true
	case "native controller upgrade", "native controller configure", "native controller start", "native controller step", "native controller reconcile", "native controller status", "native controller enroll", "native policy upgrade", "native policy configure", "native policy evaluate", "native policy approve", "native policy status", "accept apply-native", "native acceptance upgrade", "native acceptance candidate":
		return true
	case "native budget upgrade", "native budget configure", "native budget quote", "native budget status", "native budget settle", "native budget controller add":
		return true
	case "native checkpoint upgrade", "native checkpoint configure", "native checkpoint record", "native checkpoint reconcile", "native checkpoint status", "native checkpoint controller add", "native preflight upgrade", "native preflight configure", "native preflight override", "native preflight check":
		return true
	case "native recovery upgrade", "native recovery configure", "native recovery preview", "native recovery apply":
		return true
	case "native lifecycle upgrade", "native lifecycle configure", "native lifecycle controller add", "native lifecycle bridge add", "native lifecycle observe", "native lifecycle cancel", "native lifecycle cancel-ack", "native lifecycle status":
		return true
	case "native dispatch upgrade", "native dispatch bridge add", "native dispatch request", "native dispatch correlate", "native dispatch acknowledge", "native dispatch status", "native swarm upgrade", "native swarm configure", "native swarm controller add", "native swarm plan", "native swarm reserve", "native swarm admit", "native swarm handoff", "native bootstrap", "native enroll", "native revoke", "native upgrade", "native verifier add", "native verify", "native reviewer add":
		return true
	}
	return false
}
func flags(args []string, allowed ...string) (*clikit.Flags, error) {
	f, parseErr := clikit.ParseFlags(args)
	if parseErr != nil {
		return nil, parseErr
	}
	if e := f.Reject(allowed...); e != nil {
		return nil, e
	}
	if len(f.Pos) != 0 {
		return nil, clikit.Usagef("unexpected positional arguments")
	}
	for _, k := range allowed {
		if k != "path" && len(f.All(k)) > 1 {
			return nil, clikit.Usagef("flag --%s must appear once", k)
		}
	}
	return f, nil
}
func denied() error {
	return clikit.Refusedf("native authority unavailable or refused; verify private configuration and credentials")
}
func serve(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "connection", "runner-config", "review-config", "orchestration-config")
	if e != nil {
		return e
	}
	if f.Get("connection") == "" {
		return clikit.Usagef("--connection is required")
	}
	if (f.Get("runner-config") != "" && f.Get("review-config") != "") || (f.Get("orchestration-config") != "" && (f.Get("runner-config") != "" || f.Get("review-config") != "")) {
		return clikit.Usagef("--runner-config, --review-config and --orchestration-config are mutually exclusive")
	}
	var s *native.Server
	if f.Get("orchestration-config") != "" {
		s, e = native.OpenWithOrchestration(f.Get("connection"), f.Get("orchestration-config"))
	} else {
		s, e = native.OpenWithProfiles(f.Get("connection"), f.Get("runner-config"), f.Get("review-config"))
	}
	if e != nil {
		return denied()
	}
	bounded, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = s.ServeContext(bounded, os.Stdin, ctx.Stdout); e != nil {
		return denied()
	}
	return nil
}

type ownerConfig struct {
	SchemaVersion       int    `json:"schema_version"`
	Workspace           string `json:"workspace"`
	Authority           string `json:"authority"`
	InstallationID      string `json:"installation_id"`
	OwnerCredentialFile string `json:"owner_credential_file"`
}

func config(path string) (ownerConfig, error) {
	var c ownerConfig
	b, e := native.ReadPrivate(path, 16384)
	if e != nil || native.Decode(b, &c) != nil || c.SchemaVersion != 1 || c.InstallationID == "" || !filepath.IsAbs(c.Authority) || !filepath.IsAbs(c.OwnerCredentialFile) {
		return c, denied()
	}
	return c, nil
}

// reserve never truncates an existing credential. Failed issuance leaves authority
// history intact; no recovery path resets or deletes the authority aggregate.
func reserve(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, denied()
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, denied()
	}
	return f, nil
}
func writePrivate(f *os.File, data []byte) error {
	if _, e := f.Write(data); e != nil {
		return denied()
	}
	if e := f.Sync(); e != nil {
		return denied()
	}
	return nil
}
func bootstrap(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
	}
	c, e := config(f.Get("config"))
	if e != nil {
		return e
	}
	w, e := native.ResolveWorkspace(c.Workspace, "")
	if e != nil {
		return denied()
	}
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	credential, e := reserve(c.OwnerCredentialFile)
	if e != nil {
		return e
	}
	defer func() { _ = credential.Close() }()
	token, e := a.Bootstrap()
	if e != nil {
		_ = os.Remove(c.OwnerCredentialFile)
		return denied()
	}
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	fmt.Fprintln(ctx.Stdout, "Native authority initialized; owner credential saved privately.")
	return nil
}
func enroll(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "task", "connection", "credential", "path", "expires", "verification", "checkout")
	if e != nil {
		return e
	}
	for _, k := range []string{"config", "task", "connection", "credential"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s is required", k)
		}
	}
	if len(f.All("verification")) > 0 && f.Get("verification") != "true" {
		return clikit.Usagef("--verification must be a bare boolean flag")
	}
	lifetime := time.Hour
	if f.Get("expires") != "" {
		lifetime, e = time.ParseDuration(f.Get("expires"))
		if e != nil || lifetime <= 0 || lifetime > 24*time.Hour {
			return clikit.Usagef("--expires must be positive and at most 24h")
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
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	if a.AuthenticateOwner(owner) != nil {
		return denied()
	}
	task, e := store.FindTask(w, f.Get("task"))
	if e != nil {
		return denied()
	}
	obs, e := checkout.ObserveRepositoryIdentity(w.Root)
	if e != nil {
		return denied()
	}
	paths := []string{"workspace:" + w.ID, "task:" + task.ID}
	for _, p := range f.All("path") {
		if p == "" || filepath.IsAbs(p) || filepath.ToSlash(filepath.Clean(p)) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return denied()
		}
		paths = append(paths, "path:"+p)
	}
	for _, path := range f.All("checkout") {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(path) || canonical != path {
			return denied()
		}
		selected, err := checkout.Observe(path)
		if err != nil || selected.Repository != obs.Repository {
			return denied()
		}
		paths = append(paths, "checkout:"+path)
	}
	credential, e := reserve(f.Get("credential"))
	if e != nil {
		return e
	}
	defer func() { _ = credential.Close() }()
	connection, e := reserve(f.Get("connection"))
	if e != nil {
		_ = os.Remove(f.Get("credential"))
		return e
	}
	defer func() { _ = connection.Close() }()
	issued := false
	defer func() {
		if !issued {
			_ = os.Remove(f.Get("credential"))
			_ = os.Remove(f.Get("connection"))
		}
	}()
	if e = a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: task.Project, Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
		return denied()
	}
	actions := []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit"}
	if len(f.All("verification")) > 0 {
		actions = append(actions, "verification.request")
	}
	s, token, e := a.Enroll(owner, sessions.Scope{Actions: actions, Resources: paths}, time.Now().Add(lifetime))
	if e != nil {
		return denied()
	}
	issued = true
	if e = writePrivate(credential, []byte(token+"\n")); e != nil {
		return e
	}
	b, e := json.Marshal(native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: c.Authority, WorkspaceID: w.ID, InstallationID: c.InstallationID, SessionID: s.ID, CredentialFile: f.Get("credential")})
	if e != nil {
		return denied()
	}
	if e = writePrivate(connection, b); e != nil {
		return e
	}
	fmt.Fprintf(ctx.Stdout, "Native session %s enrolled; connection and credential saved privately.\n", s.ID)
	return nil
}

// Revocation changes authority, not files held by a client. Live transports recheck
// that authority on every request; never replace or delete session credentials here.
func revoke(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config", "session")
	if e != nil {
		return e
	}
	for _, k := range []string{"config", "session"} {
		if f.Get(k) == "" {
			return clikit.Usagef("--%s is required", k)
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
	a, e := sessions.Open(c.Authority, sessions.Binding{Workspace: w.ID, Installation: c.InstallationID})
	if e != nil {
		return denied()
	}
	if e = a.Revoke(owner, f.Get("session")); e != nil {
		return denied()
	}
	fmt.Fprintln(ctx.Stdout, "Native session revoked.")
	return nil
}

// Upgrade is deliberately exact, never startup migration, reset or downgrade.
func upgrade(ctx *clikit.Ctx, args []string) error {
	f, e := flags(args, "config")
	if e != nil {
		return e
	}
	if f.Get("config") == "" {
		return clikit.Usagef("--config is required")
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
	if e = a.UpgradePublication(owner); e != nil {
		return clikit.Refusedf("native upgrade requires schema 5 and valid private owner authority; preserve storage for owner reconciliation")
	}
	fmt.Fprintln(ctx.Stdout, "Native authority upgraded from schema 5 to 6; existing credentials preserved.")
	return nil
}
