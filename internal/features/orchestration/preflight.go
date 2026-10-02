package orchestration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/verifyroute"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// cyclePreflightRunner is deliberately narrower than runner. Every probe is a
// read-only dacli command; coding agents are never launched by this boundary.
type cyclePreflightRunner interface {
	runPreflight(label string, args ...string) (string, error)
}

type exactLaunchContractRunner interface {
	requiresLaunchContract() bool
}

const (
	preflightPass      = "pass"
	preflightPermanent = "permanent_refusal"
	preflightTransient = "transient_failure"
	preflightUnknown   = "unobserved"
)

type capacityOverride struct {
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

type appliedCapacityOverride struct {
	Task      string    `json:"task"`
	Role      string    `json:"role"`
	Delta     float64   `json:"capacity_delta"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	Scope     string    `json:"scope"`
}

type cyclePreflightResult struct {
	SchemaVersion  int                   `json:"schema_version"`
	Project        string                `json:"project"`
	Cycle          int                   `json:"cycle"`
	Verdict        string                `json:"verdict"`
	Classification string                `json:"classification"`
	GeneratedAt    time.Time             `json:"generated_at"`
	Phases         []cyclePreflightPhase `json:"phases"`
}

type cyclePreflightPhase struct {
	Phase            string                       `json:"phase"`
	Task             string                       `json:"task,omitempty"`
	Role             string                       `json:"role,omitempty"`
	Runtime          string                       `json:"runtime,omitempty"`
	Model            string                       `json:"model,omitempty"`
	Grant            string                       `json:"grant,omitempty"`
	WorkingDirectory string                       `json:"working_directory,omitempty"`
	TokenControl     string                       `json:"token_control,omitempty"`
	OutputContract   string                       `json:"output_contract,omitempty"`
	LaunchContract   *store.RuntimeLaunchContract `json:"launch_contract,omitempty"`
	Claims           []string                     `json:"claims,omitempty"`
	Capacity         *team.TaskCapacityVerdict    `json:"capacity,omitempty"`
	Override         *appliedCapacityOverride     `json:"override,omitempty"`
	Verdict          string                       `json:"verdict"`
	Classification   string                       `json:"classification"`
	Evidence         string                       `json:"evidence,omitempty"`
	Remediation      string                       `json:"remediation,omitempty"`
}

func cyclePreflightFile(w *workspace.Workspace, project string) string {
	return filepath.Join(w.Root, workspace.Dir, "loop", project+"-preflight.json")
}

func (d *driver) emitCyclePreflight(result cyclePreflightResult) {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}
	d.logf("cycle preflight JSON: %s", string(raw))
	if d.cfg.dryRun {
		return
	}
	path := cyclePreflightFile(d.w, d.cfg.project)
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = writeStateFile(path, string(append(raw, '\n')))
	}
}

// preflightCycle resolves the complete cycle boundary before implementation.
// It inventories every phase even after finding a refusal, then returns one
// stable answer: policy/capability mismatches are permanent (exit 3), while
// external observation failures are transient (exit 1). No probe launches a
// coding agent or mutates the repository/GitHub.
func (d *driver) preflightCycle(ready []*store.Task) error {
	result := cyclePreflightResult{SchemaVersion: 2, Project: d.cfg.project, Cycle: d.gov.Cycle() + 1, Verdict: preflightPass, Classification: preflightPass, GeneratedAt: d.now().UTC()}
	var permanent, transient []string
	add := func(phase cyclePreflightPhase) {
		if phase.Classification == "" {
			phase.Classification = preflightPass
		}
		if phase.Verdict == "" {
			phase.Verdict = preflightPass
		}
		result.Phases = append(result.Phases, phase)
		switch phase.Classification {
		case preflightPermanent:
			permanent = append(permanent, phase.Phase+": "+phase.Evidence)
		case preflightTransient:
			transient = append(transient, phase.Phase+": "+phase.Evidence)
		}
	}

	stop := cyclePreflightPhase{Phase: "stop", Evidence: "STOP is clear"}
	if d.gov.StopRequested() {
		stop.Verdict, stop.Classification, stop.Evidence, stop.Remediation = "refuse", preflightPermanent, d.gov.StopReason(), "remove STOP only when the owner intends to resume"
	}
	add(stop)

	rolling := cyclePreflightPhase{Phase: "rolling-budget", Evidence: fmt.Sprintf("spent=%d ceiling=%d window=%s", d.gov.WindowSpent(), d.gov.WindowTokens, d.gov.windowDur())}
	if d.gov.WindowTokens > 0 && d.gov.WindowSpent() >= d.gov.WindowTokens {
		rolling.Verdict, rolling.Classification, rolling.Evidence, rolling.Remediation = "retry", preflightTransient, fmt.Sprintf("rolling token window exhausted (%d/%d)", d.gov.WindowSpent(), d.gov.WindowTokens), "retry after the recorded window reset"
	}
	add(rolling)
	cycleBudget := cyclePreflightPhase{Phase: "cycle-budget", Evidence: fmt.Sprintf("width=%d per-worker-token-ceiling=%d review-reserve=%d recovery-reserve=%d", d.cycleWidth(), d.cfg.perCycleTok, d.tokenBudget.ReviewReservation, d.tokenBudget.RecoveryReserve)}
	if profile, err := loadProfile(d.w, d.cfg.project); err == nil {
		projected := int64(d.cycleWidth())*d.cfg.perCycleTok + d.tokenBudget.ReviewReservation + d.tokenBudget.RecoveryReserve
		cycleBudget.Evidence = fmt.Sprintf("projected-complete-cycle=%d profile-ceiling=%d width=%d", projected, profile.Budgets.PerCycleTokens, d.cycleWidth())
		if projected > 0 && profile.Budgets.PerCycleTokens > 0 && projected > profile.Budgets.PerCycleTokens {
			cycleBudget.Verdict, cycleBudget.Classification, cycleBudget.Remediation = "refuse", preflightPermanent, "reduce width or per-task tokens, or update the operating profile"
		}
	}
	add(cycleBudget)
	schedulingWIP := cyclePreflightPhase{Phase: "cycle-wip", Evidence: fmt.Sprintf("width=%d", d.cycleWidth())}
	if profile, err := loadProfile(d.w, d.cfg.project); err == nil {
		schedulingWIP.Evidence = fmt.Sprintf("width=%d profile-wip=%d", d.cycleWidth(), profile.Scheduling.WIP)
		if profile.Scheduling.WIP > 0 && d.cycleWidth() > profile.Scheduling.WIP {
			schedulingWIP.Verdict, schedulingWIP.Classification, schedulingWIP.Remediation = "refuse", preflightPermanent, "reduce loop width or update the recorded scheduling WIP cap"
		}
	}
	add(schedulingWIP)

	wave := selectClaimCompatibleWave(d.w.Root, ready, d.cycleWidth())
	implementationRoles := map[string]bool{}
	plannedByRole := map[string]int{}
	fallbackRole := d.buildRole()
	for _, task := range wave.Tasks {
		roleName := fallbackRole
		role, found := store.LoadRole(d.w, roleName)
		phase := cyclePreflightPhase{Phase: "implementation", Task: task.ID, Role: roleName, Grant: "rw", Claims: store.ClaimHints(d.w.Root, task), Evidence: "capacity and claims resolved before worker launch"}
		if !found {
			if d.cfg.implRoleExplicit {
				phase.Verdict, phase.Classification = "refuse", preflightPermanent
				phase.Evidence, phase.Remediation = "configured implementation role is missing", "configure the implementation role before starting the loop"
			} else {
				phase.Verdict, phase.Classification, phase.Evidence = "unknown", preflightUnknown, "implicit fallback role is not declared; launch retains legacy resolution"
			}
			add(phase)
			continue
		}
		te, sized := 0.0, false
		if estimate, ok := task.Estimate(); ok {
			te, sized = estimate.Expected(), true
		}
		if !d.cfg.implRoleExplicit {
			all, _ := store.LoadRoles(d.w)
			var candidates []team.Role
			for _, candidate := range all {
				if strings.EqualFold(candidate.Kind, role.Kind) && d.roleAllowedByHarness(candidate) {
					candidates = append(candidates, candidate)
				}
			}
			if selected, ok := team.CheapestCapableForTitled(candidates, role.Kind, te, phase.Claims, task.Title, orchestrationTaskBody(task)); ok {
				role = selected
			}
		}
		phase.Role, phase.Runtime, phase.Model = role.Name, role.Runtime, role.ModelID()
		capacity := team.TaskCapacity(role, te, sized)
		phase.Capacity = &capacity
		if !capacity.Fits {
			validOverride := d.cfg.implRoleExplicit && d.cfg.capacityOverride != nil && d.cfg.capacityOverride.ExpiresAt.After(d.now().UTC())
			if validOverride {
				o := d.cfg.capacityOverride
				phase.Override = &appliedCapacityOverride{Task: task.ID, Role: role.Name, Delta: capacity.Delta, Actor: o.Actor, Reason: o.Reason, ExpiresAt: o.ExpiresAt, Scope: fmt.Sprintf("project=%s cycle=%d", d.cfg.project, result.Cycle)}
				phase.Evidence = capacity.Reason + "; owner accepted the recorded invocation-scoped exception"
			} else {
				phase.Verdict, phase.Classification, phase.Evidence = "refuse", preflightPermanent, capacity.Reason
				phase.Remediation = "assign a capable role, estimate/decompose the task, or supply an owner reason and future expiry"
			}
		}
		add(phase)
		implementationRoles[role.Name] = true

		wip := cyclePreflightPhase{Phase: "implementation-wip", Task: task.ID, Role: role.Name}
		active, err := store.ActiveInRole(d.w, role.Name)
		switch {
		case err != nil:
			wip.Verdict, wip.Classification, wip.Evidence, wip.Remediation = "retry", preflightTransient, err.Error(), "repair the local run/agent record, then retry"
		case role.WIP > 0 && active+plannedByRole[role.Name] >= role.WIP:
			wip.Verdict, wip.Classification, wip.Evidence, wip.Remediation = "refuse", preflightPermanent, fmt.Sprintf("role WIP would be exceeded (active=%d planned-before-this-task=%d cap=%d)", active, plannedByRole[role.Name], role.WIP), "reduce cycle width, finish/retire a live holder, or change the recorded role cap"
		default:
			wip.Evidence = fmt.Sprintf("active=%d planned-before-this-task=%d cap=%d", active, plannedByRole[role.Name], role.WIP)
		}
		add(wip)
		plannedByRole[role.Name]++

		timeout := d.workerTimeout(task)
		add(cyclePreflightPhase{Phase: "implementation-timeout", Task: task.ID, Role: role.Name, Evidence: fmt.Sprintf("worker-timeout=%ds", timeout)})
	}

	roles := append([]string{d.cfg.reviewRole}, sortedKeys(implementationRoles)...)
	grants := append([]string{"ro"}, repeat("rw", len(roles)-1)...)
	for i, roleName := range roles {
		phaseName := "implementation-runtime"
		if i == 0 {
			phaseName = "reviewer-runtime"
		}
		phase := cyclePreflightPhase{Phase: phaseName, Role: roleName, Grant: grants[i], OutputContract: "commit-and-command-result-v1"}
		if i == 0 {
			phase.OutputContract = store.ReviewResultSchema
			phase.Evidence = fmt.Sprintf("reviewer-timeout=%ds", d.workerTimeout(nil))
		}
		role, found := store.LoadRole(d.w, roleName)
		if !found {
			explicit := d.cfg.implRoleExplicit
			if i == 0 {
				explicit = d.cfg.reviewRoleExplicit
			}
			if explicit {
				phase.Verdict, phase.Classification, phase.Evidence, phase.Remediation = "refuse", preflightPermanent, "explicitly selected role is missing", "configure the role and runtime contract"
			} else {
				phase.Verdict, phase.Classification, phase.Evidence = "unknown", preflightUnknown, "implicit role is not declared; launch retains legacy resolution"
			}
			add(phase)
			continue
		}
		phase.Runtime, phase.Model = role.Runtime, role.ModelID()
		if i == 0 && role.Grant != "" && role.Grant != "ro" {
			phase.Verdict, phase.Classification = "refuse", preflightPermanent
			phase.Evidence, phase.Remediation = "independent review role declares grant "+role.Grant+"; reviewer authority must remain read-only", "use a dedicated read-only reviewer role; do not broaden reviewer authority to implement corrections"
			add(phase)
			continue
		}
		phase.TokenControl = "unset"
		if d.cfg.perCycleTok > 0 {
			phase.TokenControl = "unsupported"
			if rt, err := store.LoadRuntime(d.w, role.Runtime); err == nil && rt.TokenLimitFlag != "" {
				phase.TokenControl = "enforced:" + rt.TokenLimitFlag
			} else if d.cfg.allowAdvisoryTokens {
				phase.TokenControl = "advisory"
			}
		}
		if !d.roleAllowedByHarness(role) {
			phase.Verdict, phase.Classification, phase.Evidence, phase.Remediation = "refuse", preflightPermanent, "role runtime is outside the pinned harness policy", "choose a role on the selected harness or explicitly configure hybrid mode"
			add(phase)
			continue
		}
		if i == 0 {
			wip := cyclePreflightPhase{Phase: "reviewer-wip", Role: role.Name}
			active, err := store.ActiveInRole(d.w, role.Name)
			switch {
			case err != nil:
				wip.Verdict, wip.Classification, wip.Evidence, wip.Remediation = "retry", preflightTransient, err.Error(), "repair the local run/agent record, then retry"
			case role.WIP > 0 && active >= role.WIP:
				wip.Verdict, wip.Classification, wip.Evidence, wip.Remediation = "refuse", preflightPermanent, fmt.Sprintf("review role is at WIP limit (%d/%d)", active, role.WIP), "finish or retire the live reviewer before starting implementation"
			default:
				wip.Evidence = fmt.Sprintf("active=%d cap=%d", active, role.WIP)
			}
			add(wip)
		}
		if pr, ok := d.run.(cyclePreflightRunner); ok {
			preflightArgs := []string{"preflight", "--role", roleName, "--grant", grants[i]}
			if i == 0 {
				preflightArgs = append(preflightArgs, "--structured-review-result")
			}
			out, err := pr.runPreflight(phaseName, d.contextExceptionArgs(preflightArgs)...)
			if observed := strings.TrimSpace(out); observed != "" {
				if phase.Evidence != "" {
					phase.Evidence += "; "
				}
				phase.Evidence += observed
			}
			if err != nil {
				phase.Verdict, phase.Remediation = "refuse", "fix the declared runtime contract or select a compatible role"
				if clikit.ExitCode(err) == 3 {
					phase.Classification = preflightPermanent
				} else {
					phase.Verdict, phase.Classification = "retry", preflightTransient
				}
				if !strings.Contains(phase.Evidence, err.Error()) {
					if phase.Evidence != "" {
						phase.Evidence += "; "
					}
					phase.Evidence += err.Error()
				}
			} else if i == 0 {
				contract, contractErr := store.ParseRuntimeLaunchContract(out)
				if contractErr == nil {
					phase.LaunchContract = &contract
					d.reviewLaunchFingerprint = contract.Fingerprint
				} else if exact, required := d.run.(exactLaunchContractRunner); required && exact.requiresLaunchContract() {
					phase.Verdict, phase.Classification, phase.Evidence, phase.Remediation = "refuse", preflightPermanent, contractErr.Error(), "rerun the exact reviewer preflight; do not launch from unbound evidence"
				}
			}
		} else {
			phase.Verdict, phase.Classification, phase.Evidence = "unknown", preflightUnknown, "runner does not expose read-only preflight"
		}
		add(phase)
	}

	base := orDefault(d.cfg.landing.Base, d.trunkBranch)
	landing := cyclePreflightPhase{Phase: "landing-base", WorkingDirectory: d.w.Root, Evidence: fmt.Sprintf("mode=%s base=%s", d.cfg.landing.Mode, clikit.OrDash(base))}
	if base == "" {
		landing.Verdict, landing.Classification = "unknown", preflightUnknown
		landing.Evidence = "landing base is unresolved"
	} else if _, err := gitx.Run(d.w.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+base); err != nil {
		current, currentErr := gitx.Run(d.w.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
		if currentErr != nil || strings.TrimSpace(current) != base {
			landing.Verdict, landing.Classification, landing.Evidence, landing.Remediation = "refuse", preflightPermanent, fmt.Sprintf("landing base %s does not exist locally", base), "fetch or create the configured landing branch"
		} else {
			landing.Evidence += " (unborn branch)"
		}
	}
	add(landing)

	checks := cyclePreflightPhase{Phase: "check-policy", Evidence: fmt.Sprintf("pr=%t auto-merge=%t", d.cfg.pr, d.cfg.autoMerge)}
	if profile, err := loadProfile(d.w, d.cfg.project); err == nil {
		checks.Evidence = fmt.Sprintf("pr=%t checks-required=%t reviews-required=%d auto-merge=%t", d.cfg.pr, profile.Landing.ChecksRequired, profile.Landing.ReviewsRequired, d.cfg.autoMerge)
	}
	add(checks)
	if d.cfg.pr {
		github := cyclePreflightPhase{Phase: "github-observability", Evidence: "GitHub CLI authentication and repository visibility observed"}
		if pr, ok := d.run.(cyclePreflightRunner); ok {
			out, err := pr.runPreflight("github-observability", "github", "doctor")
			if strings.TrimSpace(out) != "" {
				github.Evidence = strings.TrimSpace(out)
			}
			if err != nil {
				github.Verdict, github.Classification, github.Remediation = "retry", preflightTransient, "restore GitHub authentication/connectivity and retry the unchanged preflight"
				if github.Evidence == "" {
					github.Evidence = err.Error()
				}
			}
		} else {
			github.Verdict, github.Classification, github.Evidence = "unknown", preflightUnknown, "runner does not expose GitHub doctor"
		}
		add(github)
	}

	profile, profileErr := loadProfile(d.w, d.cfg.project)
	switch {
	case profileErr == nil:
		if len(profile.Verification.Rules) > 0 {
			if err := verifyroute.Validate(d.w.Root, profile.Verification.Rules, profile.Verification.ContractGroups); err != nil {
				add(cyclePreflightPhase{Phase: "verification-profile", WorkingDirectory: d.w.Root, Verdict: "refuse", Classification: preflightPermanent, Evidence: err.Error(), Remediation: "repair verification.rules path/cwd/argv and contract-group references"})
				break
			}
			for _, rule := range profile.Verification.Rules {
				cwd := filepath.Join(d.w.Root, filepath.FromSlash(rule.Cwd))
				phase := cyclePreflightPhase{Phase: "verification-command", WorkingDirectory: cwd, Evidence: fmt.Sprintf("rule=%s gate=%s argv=%q", rule.ID, rule.Gate, rule.Argv)}
				if err := verificationArgvAvailable(cwd, rule.Argv); err != nil {
					phase.Verdict, phase.Classification, phase.Evidence, phase.Remediation = "refuse", preflightPermanent, err.Error(), "install/configure the executable or update verification.rules"
				}
				add(phase)
			}
			break
		}
		for _, command := range profile.Verification.Commands {
			phase := cyclePreflightPhase{Phase: "verification-command", WorkingDirectory: d.w.Root, Evidence: command + " (legacy command; migrate to structured verification.rules)"}
			if err := verificationCommandAvailable(d.w.Root, command); err != nil {
				phase.Verdict, phase.Classification, phase.Evidence, phase.Remediation = "refuse", preflightPermanent, err.Error(), "install/configure the command or update verification.commands"
			}
			add(phase)
		}
	case errors.Is(profileErr, os.ErrNotExist):
		add(cyclePreflightPhase{Phase: "verification-profile", WorkingDirectory: d.w.Root, Verdict: "unknown", Classification: preflightUnknown, Evidence: "no operating profile; direct loop retains task-level verification"})
	default:
		add(cyclePreflightPhase{Phase: "verification-profile", WorkingDirectory: d.w.Root, Verdict: "refuse", Classification: preflightPermanent, Evidence: profileErr.Error(), Remediation: "repair or regenerate the operating profile"})
	}

	switch {
	case len(permanent) > 0:
		result.Verdict, result.Classification = "refuse", preflightPermanent
	case len(transient) > 0:
		result.Verdict, result.Classification = "retry", preflightTransient
	}
	d.emitCyclePreflight(result)
	if len(permanent) > 0 {
		return clikit.Refusedf("cycle preflight permanently refused before worker launch: %s", strings.Join(permanent, "; "))
	}
	if len(transient) > 0 {
		return fmt.Errorf("cycle preflight transient failure before worker launch: %s", strings.Join(transient, "; "))
	}
	return nil
}

func verificationCommandAvailable(cwd, command string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return fmt.Errorf("empty verification command for cwd %s", cwd)
	}
	program := ""
	for _, field := range fields {
		if !strings.Contains(field, "=") {
			program = field
			break
		}
	}
	if program == "" {
		return fmt.Errorf("verification command has environment assignments but no executable for cwd %s", cwd)
	}
	if slices.Contains([]string{"cd", "test", "[", "true", "false", "export"}, program) {
		return nil
	}
	return verificationArgvAvailable(cwd, []string{program})
}

func verificationArgvAvailable(cwd string, argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("empty verification argv for cwd %s", cwd)
	}
	program := argv[0]
	if slices.Contains([]string{"cd", "test", "[", "true", "false", "export"}, program) {
		return fmt.Errorf("verification executable %s is shell syntax, not structured argv, for cwd %s", program, cwd)
	}
	if strings.ContainsRune(program, os.PathSeparator) {
		path := program
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if st, err := os.Stat(path); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			return fmt.Errorf("verification executable %s is unavailable from cwd %s", program, cwd)
		}
		return nil
	}
	if _, err := exec.LookPath(program); err != nil {
		return fmt.Errorf("verification executable %s is unavailable from cwd %s", program, cwd)
	}
	return nil
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}

func repeat(value string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = value
	}
	return out
}
