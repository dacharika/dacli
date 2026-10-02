package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const loopOperationSchema = "loop-operation/v1"

type loopOperationResponse struct {
	Schema    string                 `json:"schema"`
	Generated string                 `json:"generated"`
	Project   string                 `json:"project"`
	State     loopOperationState     `json:"state"`
	Wave      loopOperationWave      `json:"wave"`
	Budget    loopTokenBudget        `json:"budget"`
	Tasks     []loopOperationTask    `json:"tasks"`
	Runs      []loopOperationRun     `json:"active_runs"`
	Routing   []loopOperationRouting `json:"routing"`
	Preflight []loopPreflightPhase   `json:"preflight"`
	Harness   loopHarnessPolicy      `json:"harness"`
	Warnings  []string               `json:"warnings"`
}

type loopOperationState struct {
	Value          string `json:"value"`
	Freshness      string `json:"freshness"`
	Source         string `json:"source"`
	ObservedAt     string `json:"observed_at,omitempty"`
	Cycle          int    `json:"cycle"`
	Generation     int    `json:"generation"`
	Phase          string `json:"phase,omitempty"`
	Checkpoint     string `json:"checkpoint,omitempty"`
	Retryable      *bool  `json:"retryable,omitempty"`
	HaltClass      string `json:"halt_class,omitempty"`
	Reason         string `json:"reason,omitempty"`
	NextAction     string `json:"next_action"`
	LastCheckpoint string `json:"last_checkpoint,omitempty"`
}

type loopOperationWave struct {
	Requested int `json:"requested_width"`
	Allocated int `json:"allocated_width"`
	Live      int `json:"live_width"`
}

type loopTokenAmount struct {
	Unit      string `json:"unit"`
	Limit     int64  `json:"limit"`
	Spent     int64  `json:"spent"`
	Reserved  int64  `json:"reserved"`
	Remaining *int64 `json:"remaining"`
}

type loopRunReservation struct {
	Task       string `json:"task"`
	RunID      string `json:"run_id,omitempty"`
	Tokens     int64  `json:"tokens"`
	State      string `json:"state"`
	Outcome    string `json:"outcome,omitempty"`
	Usage      *int64 `json:"usage,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
}

type loopTokenBudget struct {
	Mode               string               `json:"mode"`
	ObservedAt         string               `json:"observed_at,omitempty"`
	WindowResetAt      string               `json:"window_reset_at,omitempty"`
	Cycle              loopTokenAmount      `json:"cycle"`
	Rolling            loopTokenAmount      `json:"rolling"`
	Runs               []loopRunReservation `json:"runs"`
	ReviewReservation  int64                `json:"review_reservation"`
	RecoveryReserve    int64                `json:"recovery_reserve"`
	Unallocated        *int64               `json:"unallocated"`
	UnknownUsageRuns   []string             `json:"unknown_usage_runs"`
	AccountingBoundary string               `json:"accounting_boundary"`
}

type loopOperationTask struct {
	Task       string  `json:"task"`
	RunID      string  `json:"run_id,omitempty"`
	Phase      string  `json:"phase"`
	Generation int     `json:"generation"`
	UpdatedAt  string  `json:"updated_at,omitempty"`
	Role       string  `json:"role,omitempty"`
	Runtime    string  `json:"runtime,omitempty"`
	Model      string  `json:"model,omitempty"`
	Grant      string  `json:"grant,omitempty"`
	ClaimCount int     `json:"claim_count"`
	Capacity   *bool   `json:"capacity_fit,omitempty"`
	TaskPoints float64 `json:"task_points,omitempty"`
	RoleLimit  float64 `json:"role_limit,omitempty"`
	Override   string  `json:"override,omitempty"`
	VerifyCWD  string  `json:"verification_cwd,omitempty"`
	VerifyArgv string  `json:"verification_argv,omitempty"`
}

type loopOperationRun struct {
	RunID   string `json:"run_id"`
	AgentID string `json:"agent_id"`
	Task    string `json:"task"`
	Role    string `json:"role,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	State   string `json:"state"`
}

type loopOperationRouting struct {
	Task       string                      `json:"task"`
	Selected   loopRouteSelection          `json:"selected"`
	Candidates []team.CandidateExplanation `json:"candidates"`
	Source     string                      `json:"source"`
	Uplift     string                      `json:"uplift,omitempty"`
	Freshness  string                      `json:"freshness"`
}

type loopRouteSelection struct {
	Role    string `json:"role,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
}

type loopPreflightPhase struct {
	Phase          string `json:"phase"`
	Task           string `json:"task,omitempty"`
	Role           string `json:"role,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	Model          string `json:"model,omitempty"`
	Grant          string `json:"grant,omitempty"`
	Verdict        string `json:"verdict"`
	Classification string `json:"classification"`
	Evidence       string `json:"evidence,omitempty"`
	Remediation    string `json:"remediation,omitempty"`
	TokenControl   string `json:"token_control,omitempty"`
	OutputContract string `json:"output_contract,omitempty"`
}

type loopHarnessPolicy struct {
	Mode    string   `json:"mode"`
	Allowed []string `json:"allowed"`
	Source  string   `json:"source"`
}

type persistedLoopState struct {
	Project        string
	Cycle, Backlog int
	WindowTokens   int64
	Status         string
	Reason         string
	Recovery       string
	UpdatedAt      time.Time
}

type persistedRecovery struct {
	Schema     string    `json:"schema"`
	Version    int       `json:"version"`
	Project    string    `json:"project"`
	Cycle      int       `json:"cycle"`
	Checkpoint string    `json:"checkpoint"`
	HaltClass  string    `json:"halt_class"`
	Retryable  bool      `json:"retryable"`
	NextAction string    `json:"next_action"`
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
}

type persistedPhaseJournal struct {
	Schema  string `json:"schema"`
	Version int    `json:"version"`
	Project string `json:"project"`
	Cycle   int    `json:"cycle"`
	Tasks   []struct {
		TaskID     string    `json:"task_id"`
		Sequence   int       `json:"sequence"`
		Generation int       `json:"generation"`
		Branch     string    `json:"branch"`
		RunID      string    `json:"run_id"`
		Phase      string    `json:"phase"`
		UpdatedAt  time.Time `json:"updated_at"`
	} `json:"tasks"`
}

type persistedCapacity struct {
	Fits     bool    `json:"fits"`
	Required float64 `json:"required_points"`
	Limit    float64 `json:"limit_points"`
}

type persistedPreflight struct {
	SchemaVersion  int                  `json:"schema_version"`
	Project        string               `json:"project"`
	Cycle          int                  `json:"cycle"`
	Verdict        string               `json:"verdict"`
	Classification string               `json:"classification"`
	GeneratedAt    time.Time            `json:"generated_at"`
	Phases         []persistedPhaseItem `json:"phases"`
}

type persistedPhaseItem struct {
	Phase, Task, Role, Runtime, Model, Grant string
	WorkingDirectory, TokenControl           string
	OutputContract, Verdict, Classification  string
	Evidence, Remediation                    string
	Claims                                   []string
	Capacity                                 *persistedCapacity
	Override                                 *struct {
		Reason    string    `json:"reason"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"override"`
}

func (p *persistedPhaseItem) UnmarshalJSON(raw []byte) error {
	type wire struct {
		Phase, Task, Role, Runtime, Model, Grant string
		WorkingDirectory                         string `json:"working_directory"`
		TokenControl                             string `json:"token_control"`
		OutputContract                           string `json:"output_contract"`
		Verdict, Classification                  string
		Evidence, Remediation                    string
		Claims                                   []string
		Capacity                                 *persistedCapacity
		Override                                 *struct {
			Reason    string    `json:"reason"`
			ExpiresAt time.Time `json:"expires_at"`
		}
	}
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	*p = persistedPhaseItem(w)
	return nil
}

type persistedTokenBudget struct {
	Schema             string               `json:"schema"`
	Version            int                  `json:"version"`
	Project            string               `json:"project"`
	Cycle              int                  `json:"cycle"`
	Mode               string               `json:"mode"`
	ObservedAt         time.Time            `json:"observed_at"`
	WindowResetAt      *time.Time           `json:"window_reset_at"`
	CycleBudget        loopTokenAmount      `json:"cycle_budget"`
	RollingBudget      loopTokenAmount      `json:"rolling_window"`
	Runs               []loopRunReservation `json:"live_run_reservations"`
	ReviewReservation  int64                `json:"review_reservation"`
	RecoveryReserve    int64                `json:"integration_recovery_reserve"`
	Unallocated        *int64               `json:"unallocated"`
	RequestedWidth     int                  `json:"requested_width"`
	AllocatedWidth     int                  `json:"allocated_width"`
	UnknownUsageRuns   []string             `json:"unknown_usage_runs"`
	AccountingBoundary string               `json:"accounting_boundary"`
}

type persistedProfile struct {
	Version int    `json:"version"`
	Project string `json:"project"`
	Routing struct {
		HarnessMode      string   `json:"harness_mode"`
		AllowedHarnesses []string `json:"allowed_harnesses"`
	} `json:"routing"`
}

func buildLoopOperation(w *workspace.Workspace, project string, now time.Time) loopOperationResponse {
	v := loopOperationResponse{Schema: loopOperationSchema, Generated: now.UTC().Format(time.RFC3339), Project: project}
	v.State = loopOperationState{Value: "not-started", Freshness: "missing", Source: "loop-status", NextAction: "no historical loop record; use the native host and scoped governance tools for new work"}
	v.Tasks, v.Runs, v.Routing, v.Preflight, v.Warnings = []loopOperationTask{}, []loopOperationRun{}, []loopOperationRouting{}, []loopPreflightPhase{}, []string{}
	v.Budget.Runs, v.Budget.UnknownUsageRuns = []loopRunReservation{}, []string{}
	v.Harness = loopHarnessPolicy{Mode: "unknown", Allowed: []string{}, Source: "operating-profile"}

	loopDir := filepath.Join(w.Root, workspace.Dir, "loop")
	state, stateErr := readDashboardLoopState(filepath.Join(loopDir, project+".txt"))
	if stateErr == nil {
		if state.Project != project {
			markLoopCorrupt(&v, "loop status", errors.New("project identity mismatch"))
		} else {
			v.State.Cycle, v.State.ObservedAt, v.State.Reason = state.Cycle, state.UpdatedAt.UTC().Format(time.RFC3339), safeDashboardText(state.Reason)
			v.State.Source = "loop-status+durable-records"
			v.State.Freshness = freshness(now, state.UpdatedAt)
			v.State.Value, v.State.NextAction = mapLoopState(state)
		}
	} else if !errors.Is(stateErr, fs.ErrNotExist) {
		markLoopCorrupt(&v, "loop status", stateErr)
	}

	var recovery persistedRecovery
	if err := readLoopJSON(filepath.Join(loopDir, project+"-recovery.json"), &recovery); err == nil {
		if recovery.Schema != "loop-recovery/v1" || recovery.Version != 1 || recovery.Project != project || recovery.Cycle < 0 || recovery.Checkpoint == "" || recovery.HaltClass == "" || recovery.ObservedAt.IsZero() {
			markLoopCorrupt(&v, "recovery checkpoint", errors.New("identity or schema mismatch"))
		} else {
			v.State.Checkpoint, v.State.HaltClass = recovery.Checkpoint, recovery.HaltClass
			v.State.Retryable, v.State.NextAction = &recovery.Retryable, safeDashboardText(recovery.NextAction)
			v.State.LastCheckpoint = recovery.ObservedAt.UTC().Format(time.RFC3339)
			if recovery.Cycle > v.State.Cycle {
				v.State.Cycle = recovery.Cycle
			}
			applyRecoveryState(&v.State, recovery)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		markLoopCorrupt(&v, "recovery checkpoint", err)
	}

	var journal persistedPhaseJournal
	if err := readLoopJSON(filepath.Join(loopDir, project+"-phases.json"), &journal); err == nil {
		if journal.Schema != "loop-phase-journal/v1" || journal.Version != 1 || journal.Project != project {
			markLoopCorrupt(&v, "phase journal", errors.New("identity or schema mismatch"))
		} else {
			if journal.Cycle > v.State.Cycle {
				v.State.Cycle = journal.Cycle
			}
			for _, task := range journal.Tasks {
				if task.TaskID == "" || task.Sequence <= 0 || task.Branch == "" || !validLoopPhase(task.Phase) {
					markLoopCorrupt(&v, "phase journal", errors.New("task identity, branch, sequence, or phase is invalid"))
					break
				}
				v.Tasks = append(v.Tasks, loopOperationTask{Task: task.TaskID, RunID: task.RunID, Phase: task.Phase, Generation: task.Generation, UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339)})
				if task.Generation > v.State.Generation {
					v.State.Generation = task.Generation
				}
				if task.UpdatedAt.After(parseTime(v.State.LastCheckpoint)) {
					v.State.Phase, v.State.LastCheckpoint = task.Phase, task.UpdatedAt.UTC().Format(time.RFC3339)
				}
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		markLoopCorrupt(&v, "phase journal", err)
	}

	var preflight persistedPreflight
	if err := readLoopJSON(filepath.Join(loopDir, project+"-preflight.json"), &preflight); err == nil {
		if preflight.SchemaVersion != 2 || preflight.Project != project || preflight.Cycle < 0 || preflight.Verdict == "" || preflight.Classification == "" || preflight.GeneratedAt.IsZero() {
			markLoopCorrupt(&v, "cycle preflight", errors.New("identity or schema mismatch"))
		} else {
			applyPreflight(w, &v, preflight)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		markLoopCorrupt(&v, "cycle preflight", err)
	}

	var budget persistedTokenBudget
	if err := readLoopJSON(filepath.Join(loopDir, project+"-tokens.json"), &budget); err == nil {
		if !validBudgetSnapshot(budget, project) {
			markLoopCorrupt(&v, "token reservation ledger", errors.New("identity, schema, or mode mismatch"))
		} else {
			v.Wave.Requested, v.Wave.Allocated = budget.RequestedWidth, budget.AllocatedWidth
			v.Budget = loopTokenBudget{Mode: budget.Mode, ObservedAt: budget.ObservedAt.UTC().Format(time.RFC3339), Cycle: budget.CycleBudget, Rolling: budget.RollingBudget, Runs: budget.Runs, ReviewReservation: budget.ReviewReservation, RecoveryReserve: budget.RecoveryReserve, Unallocated: budget.Unallocated, UnknownUsageRuns: append([]string{}, budget.UnknownUsageRuns...), AccountingBoundary: safeDashboardText(budget.AccountingBoundary)}
			if budget.Mode != "enforceable" {
				v.Budget.Cycle.Remaining, v.Budget.Rolling.Remaining, v.Budget.Unallocated = nil, nil, nil
			}
			if budget.WindowResetAt != nil {
				v.Budget.WindowResetAt = budget.WindowResetAt.UTC().Format(time.RFC3339)
			}
			for _, run := range budget.Runs {
				if run.State == "live" {
					v.Wave.Live++
				}
			}
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		v.Budget.Mode = "unknown"
		v.Budget.AccountingBoundary = "no durable reservation ledger; remaining budget is unknown, not zero or unlimited"
	} else {
		markLoopCorrupt(&v, "token reservation ledger", err)
	}

	readHarnessPolicy(w, project, &v)
	enrichLoopRunsAndRouting(w, project, now, &v)
	applyPhaseState(&v.State)
	sort.Slice(v.Tasks, func(i, j int) bool { return v.Tasks[i].Task < v.Tasks[j].Task })
	sort.Slice(v.Routing, func(i, j int) bool { return v.Routing[i].Task < v.Routing[j].Task })
	if stateErr == nil && state.Backlog == 0 && v.Wave.Live == 0 && v.State.Value != "corrupt" && v.State.Value != "externally-unknown" {
		v.State.Value, v.State.NextAction = "completed", "observe retained delivery and acceptance evidence; assign new work through the native host"
	}
	return v
}

func applyPhaseState(state *loopOperationState) {
	if state.Value == "corrupt" || state.Value == "halted-policy" || state.Value == "externally-unknown" || state.Value == "sleeping-budget" || state.Value == "waiting-owner" {
		return
	}
	switch state.Phase {
	case "review-pending", "correction-pending", "re-review-pending":
		state.Value, state.NextAction = "waiting-review", "observe the independent review or bounded correction checkpoint"
	case "ci-pending":
		state.Value, state.NextAction = "waiting-ci", "observe the exact-head required checks; do not refresh GitHub from the dashboard"
	case "merged":
		state.Value, state.NextAction = "waiting-owner", "complete exact-tree acceptance and durable record reconciliation"
	}
}

func readDashboardLoopState(path string) (persistedLoopState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return persistedLoopState{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return persistedLoopState{}, fmt.Errorf("malformed line")
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	required := []string{"project", "cycle", "window_tokens", "backlog", "status", "updated_at"}
	for _, key := range required {
		if values[key] == "" {
			return persistedLoopState{}, fmt.Errorf("missing %s", key)
		}
	}
	cycle, e1 := strconv.Atoi(values["cycle"])
	window, e2 := strconv.ParseInt(values["window_tokens"], 10, 64)
	backlog, e3 := strconv.Atoi(values["backlog"])
	updated, e4 := time.Parse(time.RFC3339, values["updated_at"])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || cycle < 0 || window < 0 || backlog < 0 {
		return persistedLoopState{}, fmt.Errorf("invalid numeric or timestamp field")
	}
	return persistedLoopState{Project: values["project"], Cycle: cycle, WindowTokens: window, Backlog: backlog, Status: values["status"], Reason: values["reason"], Recovery: values["recovery"], UpdatedAt: updated}, nil
}

func readLoopJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func freshness(now, observed time.Time) string {
	if observed.IsZero() {
		return "missing"
	}
	if now.Sub(observed) > 2*time.Minute {
		return "stale"
	}
	return "fresh"
}

func mapLoopState(state persistedLoopState) (string, string) {
	switch state.Status {
	case "proceed":
		return "running", "observe the current wave and its next durable checkpoint"
	case "idle":
		return "idle", "wait for ready work or resolve the recorded backlog blockers"
	case "sleep-window":
		return "sleeping-budget", "wait for the recorded rolling token window reset"
	case "halt":
		return "halted-policy", "follow the durable halt reason before starting another cycle"
	default:
		return "externally-unknown", "inspect the unsupported historical loop status; do not infer native execution authority"
	}
}

func applyRecoveryState(state *loopOperationState, recovery persistedRecovery) {
	state.Reason = safeDashboardText(recovery.Reason)
	switch recovery.HaltClass {
	case "none":
		return
	case "transient-infrastructure-failure":
		state.Value = "externally-unknown"
	case "external-blocker":
		if strings.Contains(strings.ToLower(recovery.NextAction+recovery.Reason), "review") {
			state.Value = "waiting-review"
		} else if strings.Contains(strings.ToLower(recovery.NextAction+recovery.Reason), "ci") {
			state.Value = "waiting-ci"
		} else {
			state.Value = "waiting-owner"
		}
	case "handoff-required":
		state.Value = "waiting-owner"
	case "policy-refusal":
		state.Value = "halted-policy"
	default:
		state.Value = "halted-policy"
	}
}

func applyPreflight(w *workspace.Workspace, v *loopOperationResponse, preflight persistedPreflight) {
	byTask := map[string]int{}
	for i := range v.Tasks {
		byTask[v.Tasks[i].Task] = i
	}
	for _, phase := range preflight.Phases {
		item := loopPreflightPhase{Phase: phase.Phase, Task: phase.Task, Role: phase.Role, Runtime: phase.Runtime, Model: phase.Model, Grant: phase.Grant, Verdict: phase.Verdict, Classification: phase.Classification, Evidence: safeDashboardText(phase.Evidence), Remediation: safeDashboardText(phase.Remediation), TokenControl: safeDashboardText(phase.TokenControl), OutputContract: safeDashboardText(phase.OutputContract)}
		v.Preflight = append(v.Preflight, item)
		if phase.Task == "" {
			continue
		}
		index, found := byTask[phase.Task]
		if !found {
			v.Tasks = append(v.Tasks, loopOperationTask{Task: phase.Task, Phase: "planned"})
			index = len(v.Tasks) - 1
			byTask[phase.Task] = index
		}
		task := &v.Tasks[index]
		if phase.Role != "" {
			task.Role, task.Runtime, task.Model, task.Grant = phase.Role, phase.Runtime, phase.Model, phase.Grant
			task.ClaimCount = len(phase.Claims)
		}
		if phase.Capacity != nil {
			fit := phase.Capacity.Fits
			task.Capacity, task.TaskPoints, task.RoleLimit = &fit, phase.Capacity.Required, phase.Capacity.Limit
		}
		if phase.Override != nil {
			task.Override = fmt.Sprintf("owner reason recorded until %s", phase.Override.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if phase.WorkingDirectory != "" {
			task.VerifyCWD = repositoryRelative(w.Root, phase.WorkingDirectory)
		}
		if phase.Phase == "verification-command" {
			task.VerifyArgv = safeDashboardText(phase.Evidence)
		}
	}
	if preflight.Classification == "permanent_refusal" && v.State.Value != "corrupt" {
		v.State.Value, v.State.HaltClass, v.State.Retryable = "halted-policy", preflight.Classification, boolPtr(false)
		v.State.NextAction = "inspect the historical preflight refusal; new execution belongs to the native host"
	}
}

func repositoryRelative(root, candidate string) string {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "<outside-workspace>"
	}
	if rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

func readHarnessPolicy(w *workspace.Workspace, project string, v *loopOperationResponse) {
	var profile persistedProfile
	path := filepath.Join(w.Root, workspace.Dir, "profiles", project+".json")
	if err := readLoopJSON(path, &profile); err == nil {
		if profile.Version != 1 || profile.Project != project {
			markLoopCorrupt(v, "operating profile", errors.New("identity or version mismatch"))
			return
		}
		v.Harness.Mode, v.Harness.Allowed = profile.Routing.HarnessMode, append([]string{}, profile.Routing.AllowedHarnesses...)
		if v.Harness.Mode == "single" && len(v.Harness.Allowed) > 1 {
			markLoopCorrupt(v, "operating profile", errors.New("single harness mode has multiple allowed harnesses"))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		markLoopCorrupt(v, "operating profile", err)
	}
}

func enrichLoopRunsAndRouting(w *workspace.Workspace, project string, now time.Time, v *loopOperationResponse) {
	delivery, err := store.LocalDeliveryProjection(w, project, now)
	if err != nil {
		v.Warnings = append(v.Warnings, "local delivery evidence is unreadable")
		markLoopPartial(v)
		return
	}
	explain, err := store.BuildProgressExplain(w, project, delivery, now)
	if err != nil {
		v.Warnings = append(v.Warnings, "routing and worker evidence is unreadable")
		markLoopPartial(v)
		return
	}
	taskSet := map[string]bool{}
	for _, task := range v.Tasks {
		taskSet[task.Task] = true
	}
	for _, task := range explain.Tasks {
		if !taskSet[task.ID.Value] {
			continue
		}
		r := task.RoleRouting.Value
		r.Candidates = allowedRoutingCandidates(w, v.Harness, r.Candidates)
		if !candidatePresent(r.Candidates, r.Selected.Role) {
			r.Selected = team.RouteSelection{}
		}
		for _, planned := range v.Tasks {
			if planned.Task == task.ID.Value && planned.Role != "" {
				r.Selected = team.RouteSelection{Role: planned.Role, Runtime: planned.Runtime, Model: planned.Model}
				break
			}
		}
		v.Routing = append(v.Routing, loopOperationRouting{Task: task.ID.Value, Selected: loopRouteSelection{Role: r.Selected.Role, Runtime: r.Selected.Runtime, Model: r.Selected.Model}, Candidates: append([]team.CandidateExplanation{}, r.Candidates...), Source: r.Source + "; bounded by recorded harness policy", Uplift: r.Uplift, Freshness: observedFreshness(now, task.RoleRouting.ObservedAt, task.RoleRouting.Stale)})
	}
	for _, worker := range explain.Workers {
		if worker.State.Value != "live" && worker.State.Value != "handoff-required" && worker.State.Value != "finished-unfinalized" {
			continue
		}
		v.Runs = append(v.Runs, loopOperationRun{RunID: worker.RunID.Value, AgentID: worker.AgentID.Value, Task: worker.TaskID.Value, Role: worker.Role.Value, Runtime: worker.Runtime.Value, State: worker.State.Value})
	}
	if len(v.Runs) > v.Wave.Live {
		v.Wave.Live = len(v.Runs)
	}
}

func allowedRoutingCandidates(w *workspace.Workspace, policy loopHarnessPolicy, candidates []team.CandidateExplanation) []team.CandidateExplanation {
	if len(policy.Allowed) == 0 {
		return append([]team.CandidateExplanation{}, candidates...)
	}
	allowed := map[string]bool{}
	for _, harness := range policy.Allowed {
		allowed[strings.ToLower(harness)] = true
	}
	out := make([]team.CandidateExplanation, 0, len(candidates))
	for _, candidate := range candidates {
		runtime, err := store.LoadRuntime(w, candidate.Runtime)
		if err == nil && allowed[strings.ToLower(runtime.Harness)] {
			out = append(out, candidate)
		}
	}
	return out
}

func candidatePresent(candidates []team.CandidateExplanation, role string) bool {
	for _, candidate := range candidates {
		if candidate.Role == role {
			return true
		}
	}
	return false
}

func observedFreshness(now, observed time.Time, stale bool) string {
	if stale {
		return "stale"
	}
	return freshness(now, observed)
}

func markLoopCorrupt(v *loopOperationResponse, source string, err error) {
	v.State.Value, v.State.Freshness, v.State.HaltClass = "corrupt", "corrupt", "corrupt_record"
	v.State.Retryable = boolPtr(false)
	v.State.NextAction = "repair or restore the named historical record before relying on its evidence"
	v.Warnings = append(v.Warnings, source+": "+safeDashboardText(err.Error()))
}

func markLoopPartial(v *loopOperationResponse) {
	if v.State.Freshness != "corrupt" {
		v.State.Freshness = "partial"
	}
}

func validLoopPhase(phase string) bool {
	switch phase {
	case "spawned", "waited", "verified", "review-pending", "correction-pending", "re-review-pending", "reviewed", "pushed", "pr-created", "ci-pending", "merged", "record-accepted":
		return true
	default:
		return false
	}
}

func validBudgetMode(mode string) bool {
	return mode == "enforceable" || mode == "advisory" || mode == "unknown"
}

func validBudgetSnapshot(budget persistedTokenBudget, project string) bool {
	if budget.Schema != "loop-token-reservations/v1" || budget.Version != 1 || budget.Project != project || !validBudgetMode(budget.Mode) || budget.Cycle < 0 || budget.ObservedAt.IsZero() || budget.RequestedWidth < 0 || budget.AllocatedWidth < 0 || budget.AllocatedWidth > budget.RequestedWidth || budget.ReviewReservation < 0 || budget.RecoveryReserve < 0 {
		return false
	}
	validAmount := func(amount loopTokenAmount) bool {
		return amount.Limit >= 0 && amount.Spent >= 0 && amount.Reserved >= 0 && (amount.Remaining == nil || *amount.Remaining >= 0)
	}
	if !validAmount(budget.CycleBudget) || !validAmount(budget.RollingBudget) || (budget.Unallocated != nil && *budget.Unallocated < 0) {
		return false
	}
	for _, run := range budget.Runs {
		if run.Task == "" || run.Tokens < 0 || (run.Usage != nil && *run.Usage < 0) {
			return false
		}
		switch run.State {
		case "planned", "live", "settled", "released":
		default:
			return false
		}
	}
	return true
}
func boolPtr(v bool) *bool { return &v }
func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339, value)
	return t
}
