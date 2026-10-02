// Package delivery derives one read-only delivery-state projection from the
// workspace, repository, run, event, loop, and GitHub evidence. It deliberately
// lives below feature slices so diagnosis and recovery views can share the same
// classifications without importing each other (issue #856).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/eventdisp"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/prci"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const DeliverySchemaVersion = "delivery-state/v1"

type DeliveryConfidence string

const (
	DeliveryKnown   DeliveryConfidence = "known"
	DeliveryUnknown DeliveryConfidence = "unknown"
)

type DeliveryFinding struct {
	ID             string             `json:"id"`
	Classification string             `json:"classification"`
	ObjectID       string             `json:"object_id"`
	Source         string             `json:"observed_source"`
	ObservedAt     time.Time          `json:"observed_at"`
	Severity       string             `json:"severity"`
	Confidence     DeliveryConfidence `json:"confidence"`
	Detail         string             `json:"detail"`
	NextAction     string             `json:"suggested_next_action"`
	DiagnosisCode  string             `json:"diagnosis_code,omitempty"`
	Retryable      bool               `json:"retryable"`
	RelatedRefs    []DeliveryRef      `json:"related_refs,omitempty"`
}

type DeliveryRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type DeliveryProjection struct {
	Schema     string            `json:"schema"`
	Version    int               `json:"version"`
	Project    string            `json:"project"`
	ObservedAt time.Time         `json:"observed_at"`
	Reconciled bool              `json:"reconciled"`
	Findings   []DeliveryFinding `json:"findings"`
}

type DeliveryPR struct {
	Number             int    `json:"number"`
	DeliveryConfidence string `json:"state"`
	URL                string `json:"url"`
	HeadRefName        string `json:"headRefName"`
	BaseRefName        string `json:"baseRefName"`
	HeadRefOid         string `json:"headRefOid"`
	BaseRefOid         string `json:"baseRefOid"`
	MergeCommit        *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	StatusCheckRollup []struct {
		DeliveryConfidence string `json:"state"`
		Conclusion         string `json:"conclusion"`
		Name               string `json:"name"`
	} `json:"statusCheckRollup"`
}

// ObserveDeliveryPRs is replaceable by deterministic outage/auth fixtures. The
// production command is a read-only gh query; no repair path is present here.
var ObserveDeliveryPRs = func(root string) ([]DeliveryPR, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--state", "all", "--limit", "1000", "--json", "number,state,url,headRefName,baseRefName,headRefOid,baseRefOid,mergeCommit,statusCheckRollup")
	cmd.Dir = root
	raw, err := commandresult.Run(cmd, commandresult.RunOptions{
		Operation:     "gh pr list",
		WorkspaceRoot: root,
		TimedOut: func() bool {
			return ctx.Err() == context.DeadlineExceeded
		},
	})
	if err != nil {
		return nil, fmt.Errorf("GitHub observation failed: %w", err)
	}
	var prs []DeliveryPR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("decode GitHub observation: %w", err)
	}
	return prs, nil
}

func add(p *DeliveryProjection, class, object, source, severity string, confidence DeliveryConfidence, detail, action string) {
	p.Findings = append(p.Findings, DeliveryFinding{ID: class + ":" + object, Classification: class, ObjectID: object, Source: source, ObservedAt: p.ObservedAt, Severity: severity, Confidence: confidence, Detail: detail, NextAction: action})
}

// LocalDeliveryProjection derives all evidence that does not require GitHub. Doctor uses this
// entry point so a network outage cannot erase local inconsistencies.
func LocalDeliveryProjection(w *workspace.Workspace, project string, now time.Time) (DeliveryProjection, error) {
	p := DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: project, ObservedAt: now.UTC(), Reconciled: true, Findings: []DeliveryFinding{}}
	tasks, err := ListTasks(w, project, "")
	if err != nil {
		return p, err
	}
	byID := map[string]*Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}

	runs, err := os.ReadDir(w.RunsDir())
	if err != nil && !os.IsNotExist(err) {
		return p, fmt.Errorf("read runs: %w", err)
	}
	liveTask := map[string]bool{}
	for _, ent := range runs {
		if !ent.IsDir() {
			continue
		}
		runID := ent.Name()
		rec, e := procmon.ReadRecord(filepath.Join(w.RunDir(runID), "proc.txt"))
		if e != nil {
			continue
		}
		if len(rec.Claims) > 0 {
			add(&p, "run-path-claims", runID, "run/proc", "info", DeliveryKnown, "claimed_paths="+strings.Join(rec.Claims, ","), "compare claims with the task worktree before landing")
		}
		if _, statErr := os.Stat(RootHandoffPathForRun(w, runID)); statErr == nil {
			_, handoffErr := LoadRootHandoff(w, runID)
			if handoffErr != nil {
				if byID[rec.Task] != nil {
					add(&p, "handoff-state-unknown", runID, "run/root-handoff", "major", DeliveryUnknown, handoffErr.Error(), "preserve the worktree and repair or regenerate the structured handoff before publication")
				}
			} else if _, consumedErr := os.Stat(filepath.Join(w.RunDir(runID), RootHandoffConsumedFile)); os.IsNotExist(consumedErr) {
				if byID[rec.Task] != nil {
					add(&p, "handoff-required", runID, "run/root-handoff", "major", DeliveryKnown, "worker preserved exact changed paths and lifecycle failure evidence for root", "root runs dacli handoff show "+runID+" then dacli handoff consume "+runID+" before publishing")
				}
			}
		}
		if rec.Outcome == "" && procmon.AliveIdentity(rec.PID, rec.PIDStart) {
			liveTask[rec.Task] = true
		}
		if rec.Outcome == "" && !procmon.AliveIdentity(rec.PID, rec.PIDStart) {
			raw, _ := os.ReadFile(filepath.Join(w.RunDir(runID), "outcome.md"))
			if strings.HasPrefix(string(raw), "outcome: running (detached)") {
				add(&p, "finished-unfinalized-run", runID, "run/proc", "major", DeliveryKnown, "recorded process is gone but outcome remains running", "inspect the historical run and native host outcome; use audited owner recovery before changing ownership")
			}
		}
	}

	wts, wtErr := gitx.ListWorktrees(w.Root)
	if wtErr != nil {
		add(&p, "worktree-state-unknown", project, "git", "major", DeliveryUnknown, wtErr.Error(), "restore local git observability")
	}
	wtByBranch := map[string]gitx.Worktree{}
	for _, wt := range wts {
		wtByBranch[wt.Branch] = wt
	}
	for _, t := range tasks {
		if t.CompletionState() == "implemented-unlanded" {
			add(&p, "implemented-unlanded", t.ID, "task/completion", "info", DeliveryKnown,
				"implementation completion was requested but canonical landing and acceptance are not recorded",
				"run dacli ship --tasks "+fmt.Sprintf("%d", t.Seq)+" --pr --verify \"<cmd>\" or dacli accept after the exact head lands")
		}
		if t.Status == model.StatusActive && t.Owner() != "" && t.Owner() != agentid.RootID && !liveTask[t.ID] {
			leased, leaseErr := OwnerTaskHasRecoveryLease(w, t.Owner(), t.ID)
			if leaseErr != nil {
				add(&p, "task-agent-state-unknown", t.ID, "task/run", "major", DeliveryUnknown, leaseErr.Error(), "repair unreadable run evidence")
			} else if !leased {
				add(&p, "orphaned-active-task", t.ID, "task/run", "major", DeliveryKnown, "active task owner has no live or recoverable run", "take over or reassign the task")
			}
		}
		branch := TaskBranch(t)
		_, hasWT := wtByBranch[branch]
		if gitx.BranchExists(w.Root, branch) || hasWT {
			detail := fmt.Sprintf("branch=%t worktree=%t", gitx.BranchExists(w.Root, branch), hasWT)
			if wt, ok := wtByBranch[branch]; ok {
				detail += fmt.Sprintf(" clean=%t", gitx.IsClean(wt.Path))
			}
			add(&p, "task-delivery-artifacts", t.ID, "git/worktree", "info", DeliveryKnown, detail, "inspect before cleanup or landing")
		}
	}

	for _, problem := range ReadyFrontier(tasks).Problems {
		add(&p, "unresolved-dependency", project+":"+strconv.Itoa(len(p.Findings)), "task/dependency", "major", DeliveryKnown, fmt.Sprint(problem), "correct the dependency reference")
	}

	// eventlog imports store for the shared not-found type, so the store-level
	// projection reads the append-only documents directly rather than creating
	// an entity import cycle. The pending predicate remains the durable literal
	// `applied: false`, matching eventlog.ListReport.
	var eventPaths []string
	dismissedEvents := eventdisp.DismissedIDs(w.EventsDir())
	_ = filepath.WalkDir(w.EventsDir(), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			add(&p, "event-state-unknown", filepath.Base(path), "event-log", "major", DeliveryUnknown, walkErr.Error(), "repair or restore the event record")
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			eventPaths = append(eventPaths, path)
		}
		return nil
	})
	for _, path := range eventPaths {
		doc, readErr := mdstore.ReadFile(path)
		if readErr != nil {
			add(&p, "event-state-unknown", filepath.Base(path), "event-log", "major", DeliveryUnknown, readErr.Error(), "repair or restore the event record")
			continue
		}
		applied, _ := doc.Front.Get("applied")
		if applied != "false" {
			continue
		}
		id, _ := doc.Front.Get("id")
		if dismissedEvents[id] {
			continue
		}
		about, _ := doc.Front.Get("about")
		about = strings.TrimSuffix(strings.TrimPrefix(about, "[["), "]]")
		eventKind, _ := doc.Front.Get("event_kind")
		var eventBody strings.Builder
		for _, section := range doc.Sections {
			eventBody.WriteString(section.Content)
		}
		t := byID[about]
		switch {
		case t == nil && strings.HasPrefix(about, "t-"):
			add(&p, "event-target-missing", id, "event-log", "major", DeliveryKnown, "pending event targets missing record "+about, "dismiss or retarget the event")
		case t == nil:
			add(&p, "pending-event", id, "event-log", "moderate", DeliveryKnown, "event remains unapplied for "+about, "owner runs dacli sync")
		case t.Status == model.StatusDone && model.EventKind(eventKind) == model.EventComment && currentTaskPREvent(t, eventBody.String()):
			add(&p, "terminal-task-pr-event", id, "event-log", "major", DeliveryKnown, "pending PR-open comment targets accepted task "+t.ID, "observe the exact merged PR and append an audited dismissal")
		case t.Status == model.StatusDone:
			add(&p, "terminal-task-event", id, "event-log", "major", DeliveryKnown, "pending event targets terminal task "+t.ID, "review and dismiss or reopen")
		default:
			add(&p, "pending-event", id, "event-log", "moderate", DeliveryKnown, "event remains unapplied", "owner runs dacli sync")
		}
	}

	loopPath := filepath.Join(w.Root, workspace.Dir, "loop", project+".txt")
	if raw, e := os.ReadFile(loopPath); e == nil {
		marker := field(string(raw), "trunk_marker")
		base := "main"
		if prj, pe := LoadProject(w, project); pe == nil && prj.Landing.Base != "" {
			base = prj.Landing.Base
		}
		// Match orchestration.trunkMarker's progress definition: commits that
		// changed product state, excluding dacli's own loop bookkeeping.
		fresh, ge := gitx.Run(w.Root, "rev-list", "--count", base, "--", ":(exclude).dacli")
		if ge != nil {
			add(&p, "trunk-state-unknown", project, "loop/git", "major", DeliveryUnknown, ge.Error(), "restore configured base observation")
		} else if marker != strings.TrimSpace(fresh) {
			add(&p, "stale-loop-trunk-marker", project, "loop/git", "major", DeliveryKnown, "checkpoint="+marker+" observed="+strings.TrimSpace(fresh), "recover loop from the fresh configured base")
		}
	}
	sort.Slice(p.Findings, func(i, j int) bool { return p.Findings[i].ID < p.Findings[j].ID })
	return p, nil
}

func currentTaskPREvent(t *Task, body string) bool {
	if !acceptanceCompleteForDelivery(t) {
		return false
	}
	const prefix = "PR opened: "
	body = strings.TrimSpace(body)
	rest := strings.TrimPrefix(body, prefix)
	if rest == body || strings.Contains(rest, " ") || rest == "" {
		return false
	}
	return strings.HasPrefix(rest, "https://")
}

func acceptanceCompleteForDelivery(t *Task) bool {
	boxes := t.Acceptance()
	if len(boxes) == 0 {
		return false
	}
	for _, box := range boxes {
		if !box.Done {
			return false
		}
	}
	return true
}

func field(raw, key string) string {
	for _, line := range strings.Split(raw, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ReconcileDelivery(w *workspace.Workspace, project string, now time.Time) (DeliveryProjection, error) {
	p, err := LocalDeliveryProjection(w, project, now)
	if err != nil {
		return p, err
	}
	prs, ghErr := ObserveDeliveryPRs(w.Root)
	if ghErr != nil {
		p.Reconciled = false
		diagnosis := prci.Diagnose(prci.Input{AccessFailure: &prci.AccessFailure{Operation: "observe delivery pull requests", Message: ghErr.Error()}, Now: now})
		add(&p, "github-state-unknown", project, "github", "major", DeliveryUnknown, diagnosis.Summary+": "+ghErr.Error(), diagnosis.Next)
		p.Findings[len(p.Findings)-1].DiagnosisCode = diagnosis.Code
		p.Findings[len(p.Findings)-1].Retryable = diagnosis.Retryable
		return p, ghErr
	}
	branchTask := map[string]*Task{}
	tasks, _ := ListTasks(w, project, "")
	for _, t := range tasks {
		// A delivery projection diagnoses work that can still require action.
		// Historical completed PRs are evidence, not current findings.
		if t.Status == model.StatusDone {
			continue
		}
		branchTask[TaskBranch(t)] = t
	}
	for _, pr := range prs {
		t := branchTask[pr.HeadRefName]
		if t == nil {
			continue
		}
		input := prci.Input{CanonicalHead: pr.HeadRefName, CanonicalHeadOID: pr.HeadRefOid, Now: now}
		input.PullRequests = []prci.PullRequest{{Number: pr.Number, URL: pr.URL, State: pr.DeliveryConfidence, Head: pr.HeadRefName, HeadOID: pr.HeadRefOid}}
		for _, c := range pr.StatusCheckRollup {
			input.Checks = append(input.Checks, prci.Check{Name: c.Name, Status: c.DeliveryConfidence, Conclusion: c.Conclusion})
		}
		diagnosis := prci.Diagnose(input)
		checks := "diagnosis=" + diagnosis.Code
		detail := fmt.Sprintf("pr=%s state=%s head=%s base=%s %s", pr.URL, strings.ToLower(pr.DeliveryConfidence), pr.HeadRefOid, pr.BaseRefOid, checks)
		class, sev, action := "canonical-pr", "info", "continue observing required checks"
		switch {
		case strings.EqualFold(pr.DeliveryConfidence, "CLOSED"):
			class, sev, action = "closed-unmerged-pr", "major", "reopen or create the canonical DeliveryPR"
		case strings.EqualFold(pr.DeliveryConfidence, "MERGED") && t.Generation() > 0:
			class, sev, action = "historical-merged-pr", "info", "do not use a prior generation as evidence for the reopened task"
		case strings.EqualFold(pr.DeliveryConfidence, "MERGED"):
			class, sev, action = "merged-pr-task-nonterminal", "major", "verify the exact merged head on fresh trunk before accepting the task"
		}
		add(&p, class, t.ID, "github", sev, DeliveryKnown, detail, action)
		finding := &p.Findings[len(p.Findings)-1]
		finding.DiagnosisCode = diagnosis.Code
		finding.Retryable = diagnosis.Retryable
		finding.RelatedRefs = append(finding.RelatedRefs, DeliveryRef{Kind: "branch", ID: pr.HeadRefName})
		if t.IsDeliverySlice() {
			finding.RelatedRefs = append(finding.RelatedRefs,
				DeliveryRef{Kind: "parent_task", ID: t.ParentID()},
				DeliveryRef{Kind: "delivery_generation", ID: strconv.Itoa(t.DeliveryGeneration())},
			)
		}
		if pr.Number > 0 {
			finding.RelatedRefs = append(finding.RelatedRefs, DeliveryRef{Kind: "pull_request", ID: fmt.Sprintf("#%d", pr.Number)})
		}
		for _, check := range pr.StatusCheckRollup {
			if check.Name != "" {
				finding.RelatedRefs = append(finding.RelatedRefs, DeliveryRef{Kind: "check", ID: check.Name})
			}
		}
		if class == "canonical-pr" {
			finding.NextAction = diagnosis.Next
		}
	}
	sort.Slice(p.Findings, func(i, j int) bool { return p.Findings[i].ID < p.Findings[j].ID })
	return p, nil
}
