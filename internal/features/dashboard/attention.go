package dashboard

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const operatorAttentionSchema = "operator-attention/v1"

type operatorAttentionResponse struct {
	Native    []sessions.DashboardSnapshot `json:"native,omitempty"`
	Schema    string                       `json:"schema"`
	Generated string                       `json:"generated"`
	Project   string                       `json:"project,omitempty"`
	Alerts    []operatorAttentionItem      `json:"alerts"`
	Rule      string                       `json:"ranking_rule"`
}

type attentionAffected struct {
	Project string `json:"project"`
	Task    string `json:"task,omitempty"`
	Run     string `json:"run,omitempty"`
	PR      string `json:"pr,omitempty"`
}

type attentionEvidence struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	URL        string `json:"url"`
	ObservedAt string `json:"observed_at"`
	Confidence string `json:"confidence"`
}

type operatorAttentionItem struct {
	ID              string              `json:"id"`
	Code            string              `json:"code"`
	Severity        string              `json:"severity"`
	Affected        attentionAffected   `json:"affected"`
	FirstObserved   string              `json:"first_observed"`
	LastObserved    string              `json:"last_observed"`
	Freshness       string              `json:"freshness"`
	Retryable       bool                `json:"retryable"`
	Summary         string              `json:"summary"`
	NextAction      string              `json:"next_action"`
	Link            string              `json:"link"`
	Evidence        []attentionEvidence `json:"evidence"`
	Occurrences     int                 `json:"occurrences"`
	DurationSeconds int64               `json:"duration_seconds"`
	CriticalPath    bool                `json:"critical_path"`
	Confidence      string              `json:"confidence"`
	Rank            int                 `json:"rank"`
	RankReason      string              `json:"rank_reason"`
	first, last     time.Time
}

func buildOperatorAttention(w *workspace.Workspace, project string, now time.Time) (operatorAttentionResponse, error) {
	response := operatorAttentionResponse{
		Schema: operatorAttentionSchema, Generated: now.UTC().Format(time.RFC3339), Project: project, Alerts: []operatorAttentionItem{},
		Rule: "severity (critical>high>medium>low), then critical-path impact, oldest first observation, evidence confidence, stable alert identity",
	}
	projects, err := store.ListProjects(w)
	if err != nil {
		return response, err
	}
	var candidates []operatorAttentionItem
	for _, candidateProject := range projects {
		if project != "" && candidateProject.Slug != project {
			continue
		}
		operation := buildLoopOperation(w, candidateProject.Slug, now)
		candidates = append(candidates, loopAttentionCandidates(operation, now)...)
		tasks, listErr := store.ListTasks(w, candidateProject.Slug, "")
		if listErr != nil {
			return response, listErr
		}
		graph, graphErr := buildGraph(w, candidateProject.Slug)
		if graphErr != nil {
			return response, graphErr
		}
		candidates = append(candidates, graphAttentionCandidates(graph, tasks, now)...)
		for _, task := range tasks {
			if task.Status == model.StatusDone {
				continue
			}
			candidates = append(candidates, taskAttentionCandidates(w, task, now)...)
		}
	}
	response.Alerts = collapseAttentionCandidates(candidates, now)
	return response, nil
}

func loopAttentionCandidates(operation loopOperationResponse, now time.Time) []operatorAttentionItem {
	observed := parseTime(operation.State.ObservedAt)
	if observed.IsZero() {
		observed = parseTime(operation.State.LastCheckpoint)
	}
	if observed.IsZero() {
		observed = now
	}
	base := func(code, severity, summary, next string, retryable bool) operatorAttentionItem {
		return attentionCandidate(code, severity, attentionAffected{Project: operation.Project}, observed, retryable, summary, next, "#/agents?project="+url.QueryEscape(operation.Project), attentionEvidence{Kind: "loop", ID: operation.State.Source, URL: "#/agents?project=" + url.QueryEscape(operation.Project), ObservedAt: observed.UTC().Format(time.RFC3339), Confidence: "high"})
	}
	var out []operatorAttentionItem
	lower := strings.ToLower(operation.State.HaltClass + " " + operation.State.Reason + " " + operation.State.NextAction)
	switch operation.State.Value {
	case "corrupt":
		out = append(out, base("recovery_state_corrupt", "critical", "Durable loop or recovery evidence is corrupt.", operation.State.NextAction, false))
	case "externally-unknown":
		out = append(out, base("github_state_unknown", "critical", "External delivery state is unknown; absence is not healthy.", operation.State.NextAction, true))
	case "waiting-owner":
		code := "owner_handoff"
		if !strings.Contains(lower, "handoff") {
			code = "acceptance_owner_required"
		}
		out = append(out, base(code, "high", "The governed loop requires an owner action.", operation.State.NextAction, false))
	case "waiting-review":
		out = append(out, base("review_blocked", "high", "Delivery is waiting on an independent review outcome.", operation.State.NextAction, true))
	case "waiting-ci":
		out = append(out, base("ci_blocked", "high", "Delivery is waiting on exact-head required checks.", operation.State.NextAction, true))
	case "halted-policy":
		code := "policy_refusal"
		if strings.Contains(lower, "zero") && strings.Contains(lower, "progress") {
			code = "zero_progress"
		} else if strings.Contains(lower, "capacity") || strings.Contains(lower, "wip") {
			code = "wip_capacity_limit"
		}
		out = append(out, base(code, "high", "The loop stopped at a durable policy boundary.", operation.State.NextAction, false))
	}
	if operation.Budget.Mode == "enforceable" {
		exhausted := operation.Budget.Cycle.Remaining != nil && *operation.Budget.Cycle.Remaining <= 0 || operation.Budget.Rolling.Remaining != nil && *operation.Budget.Rolling.Remaining <= 0
		if exhausted {
			at := parseTime(operation.Budget.ObservedAt)
			if at.IsZero() {
				at = observed
			}
			out = append(out, attentionCandidate("token_reservation_exhausted", "high", attentionAffected{Project: operation.Project}, at, true, "The enforceable token ledger has no allocatable capacity.", "wait for the recorded reset or reduce the bounded wave before retrying", "#/agents?project="+url.QueryEscape(operation.Project), attentionEvidence{Kind: "token-ledger", ID: fmt.Sprintf("cycle-%d", operation.State.Cycle), URL: "#/agents?project=" + url.QueryEscape(operation.Project), ObservedAt: at.UTC().Format(time.RFC3339), Confidence: "high"}))
		}
	}
	for _, phase := range operation.Preflight {
		if phase.Verdict != "refuse" {
			continue
		}
		text := strings.ToLower(phase.Phase + " " + phase.Evidence + " " + phase.Remediation)
		if !strings.Contains(text, "capacity") && !strings.Contains(text, "wip") {
			continue
		}
		affected := attentionAffected{Project: operation.Project, Task: phase.Task}
		link := "#/agents?project=" + url.QueryEscape(operation.Project)
		if phase.Task != "" {
			link = deliveryTaskLink(operation.Project, phase.Task)
		}
		out = append(out, attentionCandidate("wip_capacity_limit", "high", affected, observed, false, "A planned task exceeds the configured WIP or role capacity.", phase.Remediation, link, attentionEvidence{Kind: "preflight", ID: phase.Phase, URL: link, ObservedAt: observed.UTC().Format(time.RFC3339), Confidence: "high"}))
	}
	for _, run := range operation.Runs {
		if run.State != "handoff-required" {
			continue
		}
		link := "/api/agents/transcript?run=" + url.QueryEscape(run.RunID)
		out = append(out, attentionCandidate("owner_handoff", "high", attentionAffected{Project: operation.Project, Task: run.Task, Run: run.RunID}, observed, false, "A worker reached a root-only operation and recorded an owner handoff.", "inspect and consume the exact root handoff after re-observing its hashes", link, attentionEvidence{Kind: "run", ID: run.RunID, URL: link, ObservedAt: observed.UTC().Format(time.RFC3339), Confidence: "high"}))
	}
	return out
}

func taskAttentionCandidates(w *workspace.Workspace, task *store.Task, now time.Time) []operatorAttentionItem {
	observed := latestTaskObservation(task)
	if observed.IsZero() {
		observed = taskULIDTime(task.ID)
	}
	if observed.IsZero() {
		observed = now
	}
	link := deliveryTaskLink(task.Project, task.ID)
	var out []operatorAttentionItem
	generation := generationStart(task)
	tx, txErr := store.ReadReviewTransaction(w, task.Project, task.ID)
	txCurrent := txErr == nil && (generation.IsZero() || !tx.UpdatedAt.Before(generation))
	if txCurrent && tx.State != store.ReviewApproved {
		out = append(out, attentionCandidate("review_blocked", "high", attentionAffected{Project: task.Project, Task: task.ID, Run: tx.ReviewRunID}, tx.UpdatedAt, tx.State != store.ReviewHalted, "Independent review is "+safeDashboardText(string(tx.State))+" for the current task generation.", "open the delivery timeline and resolve the recorded review state", link, attentionEvidence{Kind: "review", ID: string(tx.State), URL: link, ObservedAt: tx.UpdatedAt.UTC().Format(time.RFC3339), Confidence: "high"}))
	}
	evidence := store.VerificationEvidenceRecords(task)
	if len(evidence) > 0 {
		latest := evidence[len(evidence)-1]
		currentPR := ""
		if len(latest.External) > 0 {
			if prs := deliveryPullRequests(w, task); len(prs) > 0 {
				currentPR = prs[len(prs)-1].URL
			}
		}
		staleGeneration := task.Generation() > 0 && !(txCurrent && tx.State == store.ReviewApproved && tx.CurrentCommit == latest.CommitSHA && tx.CurrentTree == latest.TreeSHA)
		stale := latest.Legacy != "" || !latest.Clean || latest.CommitSHA == "" || latest.TreeSHA == "" || latest.Branch != store.TaskBranch(task) || staleGeneration
		if stale {
			out = append(out, attentionCandidate("verification_stale", "high", attentionAffected{Project: task.Project, Task: task.ID}, observed, false, "Verification evidence is not acceptance-grade for the current task branch and tree.", "run the configured verification against a clean committed current tree", link, attentionEvidence{Kind: "verification", ID: latest.ArtifactHash, URL: link, ObservedAt: observed.UTC().Format(time.RFC3339), Confidence: "high"}))
		}
		for _, external := range latest.External {
			if task.Generation() > 0 && !external.ObservedAt.IsZero() && external.ObservedAt.Before(generation) {
				continue // an older PR/check generation is evidence for the stale-tree alert, not a current GitHub alert
			}
			if external.State == "observed" && strings.EqualFold(external.Conclusion, "success") && external.HeadSHA == latest.CommitSHA && !external.ObservedAt.IsZero() {
				continue
			}
			at := external.ObservedAt
			if at.IsZero() {
				at = observed
			}
			text := strings.ToLower(external.Name + " " + external.Conclusion + " " + external.SkipReason)
			code, severity, retryable := "github_state_unknown", "critical", true
			if external.State == "observed" && external.Conclusion != "" {
				code, severity = "ci_blocked", "high"
			}
			if strings.Contains(text, "billing") || strings.Contains(text, "quota") || strings.Contains(text, "account") {
				code, severity, retryable = "billing_blocked", "critical", false
			}
			out = append(out, attentionCandidate(code, severity, attentionAffected{Project: task.Project, Task: task.ID, PR: currentPR}, at, retryable, "Required external verification is "+safeDashboardText(external.State+conclusionSuffix(external.Conclusion))+".", "inspect the exact-head diagnosis and re-observe GitHub only after the named condition changes", link, attentionEvidence{Kind: "github-check", ID: firstNonEmpty(external.CheckRunID, external.WorkflowRunID, external.Name), URL: firstNonEmpty(external.URL, link), ObservedAt: at.UTC().Format(time.RFC3339), Confidence: confidenceForExternal(external)}))
		}
	}
	return out
}

func graphAttentionCandidates(graph graphView, tasks []*store.Task, now time.Time) []operatorAttentionItem {
	nodes := map[string]graphNode{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	blockedImpact := map[string]bool{}
	for _, edge := range graph.Edges {
		from, to := nodes[edge.From], nodes[edge.To]
		if from.Status == string(model.StatusBlocked) && (to.Status == string(model.StatusOpen) || to.Status == string(model.StatusActive)) {
			blockedImpact[edge.From] = true
		}
	}
	byID := map[string]*store.Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	var out []operatorAttentionItem
	for taskID := range blockedImpact {
		task := byID[taskID]
		at := now
		if task != nil {
			at = latestTaskObservation(task)
			if at.IsZero() {
				at = taskULIDTime(task.ID)
			}
		}
		link := "#/delivery?project=" + url.QueryEscape(graph.Project) + "&task=" + url.QueryEscape(taskID)
		candidate := attentionCandidate("critical_path_stalled", "high", attentionAffected{Project: graph.Project, Task: taskID}, at, false, "A blocked prerequisite stalls downstream open work.", "inspect the dependency edge and resolve the canonical blocker before widening the wave", link, attentionEvidence{Kind: "dependency-graph", ID: taskID, URL: link, ObservedAt: at.UTC().Format(time.RFC3339), Confidence: "high"})
		candidate.CriticalPath = true
		out = append(out, candidate)
	}
	return out
}

func attentionCandidate(code, severity string, affected attentionAffected, observed time.Time, retryable bool, summary, next, link string, evidence attentionEvidence) operatorAttentionItem {
	if observed.IsZero() {
		observed = time.Unix(0, 0).UTC()
	}
	id := strings.Join([]string{code, affected.Project, affected.Task, affected.Run, affected.PR}, "/")
	return operatorAttentionItem{ID: id, Code: code, Severity: severity, Affected: affected, Retryable: retryable, Summary: safeDashboardText(summary), NextAction: safeDashboardText(next), Link: link, Evidence: []attentionEvidence{evidence}, Occurrences: 1, Confidence: evidence.Confidence, first: observed.UTC(), last: observed.UTC()}
}

func collapseAttentionCandidates(candidates []operatorAttentionItem, now time.Time) []operatorAttentionItem {
	byID := map[string]*operatorAttentionItem{}
	for _, candidate := range candidates {
		if existing := byID[candidate.ID]; existing != nil {
			existing.Occurrences += candidate.Occurrences
			existing.Evidence = append(existing.Evidence, candidate.Evidence...)
			if candidate.first.Before(existing.first) {
				existing.first = candidate.first
			}
			if candidate.last.After(existing.last) {
				existing.last = candidate.last
				existing.Summary, existing.NextAction = candidate.Summary, candidate.NextAction
			}
			if confidenceRank(candidate.Confidence) > confidenceRank(existing.Confidence) {
				existing.Confidence = candidate.Confidence
			}
			existing.CriticalPath = existing.CriticalPath || candidate.CriticalPath
			continue
		}
		copy := candidate
		byID[candidate.ID] = &copy
	}
	out := make([]operatorAttentionItem, 0, len(byID))
	for _, alert := range byID {
		sort.SliceStable(alert.Evidence, func(i, j int) bool {
			if alert.Evidence[i].ObservedAt != alert.Evidence[j].ObservedAt {
				return alert.Evidence[i].ObservedAt < alert.Evidence[j].ObservedAt
			}
			return alert.Evidence[i].Kind+alert.Evidence[i].ID < alert.Evidence[j].Kind+alert.Evidence[j].ID
		})
		alert.FirstObserved, alert.LastObserved = alert.first.Format(time.RFC3339), alert.last.Format(time.RFC3339)
		alert.Freshness = freshness(now, alert.last)
		alert.DurationSeconds = max64(0, int64(alert.last.Sub(alert.first).Seconds()))
		alert.RankReason = fmt.Sprintf("severity=%s; critical_path=%t; age=%s; confidence=%s", alert.Severity, alert.CriticalPath, now.Sub(alert.first).Round(time.Second), alert.Confidence)
		out = append(out, *alert)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if severityRank(out[i].Severity) != severityRank(out[j].Severity) {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if out[i].CriticalPath != out[j].CriticalPath {
			return out[i].CriticalPath
		}
		if !out[i].first.Equal(out[j].first) {
			return out[i].first.Before(out[j].first)
		}
		if confidenceRank(out[i].Confidence) != confidenceRank(out[j].Confidence) {
			return confidenceRank(out[i].Confidence) > confidenceRank(out[j].Confidence)
		}
		return out[i].ID < out[j].ID
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func latestTaskObservation(task *store.Task) time.Time { return lastLogStamp(task, "") }
func deliveryTaskLink(project, task string) string {
	return "#/delivery?project=" + url.QueryEscape(project) + "&task=" + url.QueryEscape(task)
}
func confidenceForExternal(e store.ExternalVerificationEvidence) string {
	if e.ObservedAt.IsZero() || e.HeadSHA == "" {
		return "low"
	}
	return "high"
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return safeDashboardText(value)
		}
	}
	return "unknown"
}
func severityRank(value string) int {
	return map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}[value]
}
func confidenceRank(value string) int { return map[string]int{"low": 1, "medium": 2, "high": 3}[value] }
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
