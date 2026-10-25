package ghmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

// milestonePlan is an opt-in, bounded snapshot for assigning milestones to an
// exact task window. Pinning both the mapped issue and its current milestone
// turns a plan into a reviewable compare-and-set instead of a repo-wide rule.
type milestonePlan struct {
	Schema      string                `json:"schema"`
	Project     string                `json:"project"`
	Repository  string                `json:"repository"`
	Assignments []milestoneAssignment `json:"assignments"`
}

type milestoneAssignment struct {
	TaskID            string   `json:"task_id"`
	TaskDigest        string   `json:"task_digest"`
	Issue             int      `json:"issue"`
	ExpectedMilestone string   `json:"expected_milestone"`
	ExpectedLabels    []string `json:"expected_labels"`
	Milestone         string   `json:"milestone"`
}

const (
	milestonePlanMaxBytes       = 1 << 20
	milestonePlanMaxAssignments = 1000
)

func (a *milestoneAssignment) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{"task_id": true, "task_digest": true, "issue": true, "expected_milestone": true, "expected_labels": true, "milestone": true}
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("unknown assignment field %q", key)
		}
	}
	for _, key := range []string{"task_id", "task_digest", "issue", "expected_milestone", "expected_labels", "milestone"} {
		if raw, ok := fields[key]; !ok || string(raw) == "null" {
			return fmt.Errorf("assignment is missing required field %q", key)
		}
	}
	type plain milestoneAssignment
	return json.Unmarshal(data, (*plain)(a))
}

func readMilestonePlan(path string) (milestonePlan, error) {
	var plan milestonePlan
	f, err := os.Open(path)
	if err != nil {
		return plan, fmt.Errorf("read milestone plan: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, milestonePlanMaxBytes+1))
	if err != nil {
		return plan, fmt.Errorf("read milestone plan: %w", err)
	}
	if len(data) > milestonePlanMaxBytes {
		return plan, clikit.Usagef("invalid milestone plan: input exceeds 1 MiB")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return plan, clikit.Usagef("invalid milestone plan: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return plan, clikit.Usagef("invalid milestone plan: %v", err)
	}
	if plan.Schema != "github-milestone-plan/v1" || strings.TrimSpace(plan.Project) == "" || strings.TrimSpace(plan.Repository) == "" || len(plan.Assignments) == 0 {
		return plan, clikit.Usagef("milestone plan requires schema github-milestone-plan/v1, project, repository, and at least one assignment")
	}
	if len(plan.Assignments) > milestonePlanMaxAssignments {
		return plan, clikit.Usagef("invalid milestone plan: at most %d assignments are allowed", milestonePlanMaxAssignments)
	}
	for _, a := range plan.Assignments {
		if strings.TrimSpace(a.TaskID) == "" || strings.TrimSpace(a.TaskDigest) == "" || a.Issue < 1 || strings.TrimSpace(a.Milestone) == "" || a.ExpectedLabels == nil {
			return plan, clikit.Usagef("milestone plan assignments require task_id, task_digest, positive issue, expected_milestone, expected_labels, and milestone")
		}
	}
	return plan, nil
}

// rejectDuplicateJSONKeys walks every JSON object before decoding into structs.
// encoding/json intentionally accepts duplicate keys with last-value-wins
// semantics, which can make an owner-reviewed plan mean something else to its
// reader. Nested objects are checked too, including fields the plan schema
// would otherwise reject later.
func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(dec); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func scanJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		closing, err := dec.Token()
		if err != nil || closing != json.Delim('}') {
			if err != nil {
				return err
			}
			return fmt.Errorf("malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := scanJSONValue(dec); err != nil {
				return err
			}
		}
		closing, err := dec.Token()
		if err != nil || closing != json.Delim(']') {
			if err != nil {
				return err
			}
			return fmt.Errorf("malformed JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

// milestoneTaskDigest pins the canonical local task facts used to identify a
// plan target and derive its task status label. It intentionally excludes
// mutable link metadata because the mapped issue number is pinned separately.
func milestoneTaskDigest(t *store.Task) string {
	b, _ := json.Marshal(struct {
		ID      string `json:"id"`
		Project string `json:"project"`
		Status  string `json:"status"`
		Title   string `json:"title"`
	}{t.ID, t.Project, string(t.Status), t.Title})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum[:])
}

// validateMilestonePlan requires exact equality with the explicitly selected
// task window. Missing entries never fall back to the legacy project milestone.
func validateMilestonePlan(plan milestonePlan, project, repo string, tasks []*store.Task, idx *markerIndex) (map[string]string, error) {
	if plan.Project != project || !strings.EqualFold(plan.Repository, repo) {
		return nil, clikit.Refusedf("milestone plan targets %s/%s, current push is %s/%s", plan.Project, plan.Repository, project, repo)
	}
	if len(tasks) == 0 || len(plan.Assignments) != len(tasks) {
		return nil, clikit.Refusedf("milestone plan must cover the exact non-empty selected task window (%d task(s), %d assignment(s)); select only the plan's canonical task IDs", len(tasks), len(plan.Assignments))
	}
	selected := make(map[string]*store.Task, len(tasks))
	for _, t := range tasks {
		selected[t.ID] = t
	}
	seenTasks, seenIssues := map[string]bool{}, map[int]bool{}
	wants := make(map[string]string, len(tasks))
	for _, a := range plan.Assignments {
		t, ok := selected[a.TaskID]
		if !ok || seenTasks[a.TaskID] || seenIssues[a.Issue] || strings.TrimSpace(a.Milestone) == "" {
			return nil, clikit.Refusedf("milestone plan has an empty, duplicate, conflicting, or unselected assignment for task %q", a.TaskID)
		}
		seenTasks[a.TaskID], seenIssues[a.Issue] = true, true
		if mappedIssue(t) != a.Issue {
			return nil, clikit.Refusedf("milestone plan is stale for task %s: mapped issue is #%d, plan pins #%d", a.TaskID, mappedIssue(t), a.Issue)
		}
		if milestoneTaskDigest(t) != a.TaskDigest {
			return nil, clikit.Refusedf("milestone plan is stale for task %s: local task content or metadata changed", a.TaskID)
		}
		current, found := idx.issueMilestone(a.Issue)
		if !found || current != a.ExpectedMilestone {
			return nil, clikit.Refusedf("milestone plan is stale for issue #%d: expected current milestone %q, observed %q", a.Issue, a.ExpectedMilestone, current)
		}
		labels, found := idx.issueLabels(a.Issue)
		if !found || !sameLabels(labels, a.ExpectedLabels) {
			return nil, clikit.Refusedf("milestone plan is stale for issue #%d: current labels differ from the planned snapshot", a.Issue)
		}
		wants[t.ID] = strings.TrimSpace(a.Milestone)
	}
	return wants, nil
}

func sameLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func projectedTaxonomyLabels(current []string, status model.Status, area string) []string {
	labels := map[string]bool{}
	stale := map[string]bool{}
	for _, label := range otherStatusLabels(status) {
		stale[label] = true
	}
	for _, label := range current {
		if !stale[label] {
			labels[label] = true
		}
	}
	labels[statusLabel(status)] = true
	labels["type:task"] = true
	if area != "" {
		labels[area] = true
	}
	out := make([]string, 0, len(labels))
	for label := range labels {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func formatLabelSet(labels []string) string {
	copyOf := append([]string(nil), labels...)
	sort.Strings(copyOf)
	quoted := make([]string, len(copyOf))
	for i, label := range copyOf {
		quoted[i] = fmt.Sprintf("%q", label)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
