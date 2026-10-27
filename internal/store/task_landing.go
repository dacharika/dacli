package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/pathclaims"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const (
	taskLandingSchema      = "task-landing/v1"
	taskLandingSection     = "Landing Evidence"
	maxTaskLandingHistory  = 1 << 20
	maxTaskLandingRecord   = 64 << 10
	maxTaskLandingOutput   = 1 << 20
	taskLandingReadTimeout = 15 * time.Second
	maxTaskLandingRecords  = 256
)

var (
	taskLandingHexOID       = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	taskLandingOperationKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	taskLandingRepoSlug     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// TaskLandingSource binds a published native revision to the source checkout
// and registration base that the owner authenticated.
type TaskLandingSource struct {
	AttemptID  string `json:"attempt_id"`
	RevisionID string `json:"revision_id"`
	Sequence   uint64 `json:"sequence"`
	Repository string `json:"repository"`
	BaseCommit string `json:"base_commit"`
	Commit     string `json:"commit"`
	Tree       string `json:"tree"`
	Checkout   string `json:"checkout"`
}

// TaskLandingObservation is a task-bound historical observation of a merged
// source revision and its exact verified target tree. It is not a signature or
// permission to perform Git or GitHub operations.
type TaskLandingObservation struct {
	Schema               string            `json:"schema"`
	ID                   string            `json:"id"`
	OperationKey         string            `json:"operation_key"`
	PreviousID           string            `json:"previous_id"`
	WorkspaceID          string            `json:"workspace_id"`
	InstallationID       string            `json:"installation_id"`
	Project              string            `json:"project"`
	TaskID               string            `json:"task_id"`
	Generation           int               `json:"generation"`
	DeliveryGeneration   int               `json:"delivery_generation"`
	TaskBindingDigest    string            `json:"task_binding_digest"`
	LandingPolicyDigest  string            `json:"landing_policy_digest"`
	Repository           string            `json:"repository"`
	GitHubRepository     string            `json:"github_repository"`
	Source               TaskLandingSource `json:"source"`
	IntegratedBaseCommit string            `json:"integrated_base_commit"`
	IntegratedCommit     string            `json:"integrated_commit"`
	PRNumber             int               `json:"pr_number"`
	PRURL                string            `json:"pr_url"`
	PRState              string            `json:"pr_state"`
	PRHead               string            `json:"pr_head"`
	PRBase               string            `json:"pr_base"`
	MergeCommit          string            `json:"merge_commit"`
	TargetRef            string            `json:"target_ref"`
	TargetCommit         string            `json:"target_commit"`
	TargetTree           string            `json:"target_tree"`
	VerificationDigest   string            `json:"verification_digest"`
	ObservedAt           time.Time         `json:"observed_at"`
}

// TaskLandingResult describes the same three-valued landing contract as
// LandingState while retaining a stable reason and receipt identity.
type TaskLandingResult struct {
	State     LandingState `json:"-"`
	ReceiptID string       `json:"receipt_id,omitempty"`
	Reason    string       `json:"reason"`
	Source    string       `json:"source"`
}

// TaskLandingID returns the canonical content identity for an observation.
// The ID field is excluded from its own digest.
func TaskLandingID(observation TaskLandingObservation) (string, error) {
	observation.ID = ""
	raw, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// TaskLandingBindingDigest binds source evidence to the task's identity,
// current generations, ordered acceptance text, explicit claims and parent / delivery identity.
func TaskLandingBindingDigest(t *Task) (string, error) {
	if t == nil || t.Doc == nil || t.ID == "" || t.Project == "" {
		return "", errors.New("task landing binding unavailable")
	}
	generation, err := strictTaskGeneration(t.Doc, "generation")
	if err != nil {
		return "", err
	}
	deliveryGeneration, err := strictTaskGeneration(t.Doc, "delivery_generation")
	if err != nil {
		return "", err
	}
	parentGeneration, err := strictTaskGeneration(t.Doc, "parent_generation")
	if err != nil {
		return "", err
	}
	criteria := make([]string, 0, len(t.Acceptance()))
	for _, box := range t.Acceptance() {
		criteria = append(criteria, box.Text)
	}
	identity := struct {
		ID                 string   `json:"id"`
		Project            string   `json:"project"`
		Generation         int      `json:"generation"`
		DeliveryGeneration int      `json:"delivery_generation"`
		Acceptance         []string `json:"acceptance"`
		Claims             []string `json:"claims"`
		Parent             string   `json:"parent"`
		ParentGeneration   int      `json:"parent_generation"`
		DeliverySlice      string   `json:"delivery_slice"`
		DeliveryRequired   string   `json:"delivery_required"`
		DeliveryTerminal   string   `json:"delivery_terminal"`
	}{ID: t.ID, Project: t.Project, Generation: generation, DeliveryGeneration: deliveryGeneration, Acceptance: criteria, Claims: t.Claims()}
	identity.Parent, _ = t.Doc.Front.Get("parent")
	identity.ParentGeneration = parentGeneration
	identity.DeliverySlice, _ = t.Doc.Front.Get("delivery_slice")
	identity.DeliveryRequired, _ = t.Doc.Front.Get("delivery_required")
	identity.DeliveryTerminal, _ = t.Doc.Front.Get("delivery_terminal")
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func strictTaskGeneration(doc *mdstore.Doc, key string) (int, error) {
	raw, ok := doc.Front.Get(key)
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || strconv.Itoa(n) != raw {
		return 0, fmt.Errorf("task landing %s is malformed", key)
	}
	return n, nil
}

// TaskLandingPolicyDigest binds the project GitHub mapping, configured landing
// mode/base, and canonical target repository identity.
func TaskLandingPolicyDigest(w *workspace.Workspace, project string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskLandingReadTimeout)
	defer cancel()
	return TaskLandingPolicyDigestContext(ctx, w, project)
}

// TaskLandingPolicyDigestContext computes the policy identity under the
// caller's shared operation deadline.
func TaskLandingPolicyDigestContext(ctx context.Context, w *workspace.Workspace, project string) (string, error) {
	if ctx == nil {
		return "", errors.New("task landing context required")
	}
	return taskLandingPolicyDigest(ctx, w, project)
}

func taskLandingPolicyDigest(ctx context.Context, w *workspace.Workspace, project string) (string, error) {
	if w == nil || project == "" {
		return "", errors.New("task landing policy unavailable")
	}
	p, err := LoadProject(w, project)
	if err != nil {
		return "", err
	}
	repo, err := targetRepositoryContext(ctx, w.Root)
	if err != nil {
		return "", err
	}
	githubRepo, _ := p.Doc.Front.Get("github_repo")
	githubRepo = strings.TrimSpace(githubRepo)
	if !taskLandingRepoSlug.MatchString(githubRepo) || strings.Contains(githubRepo, "..") {
		return "", errors.New("task landing GitHub repository is not configured")
	}
	mode, ok := p.Doc.Front.Get("landing.mode")
	if !ok || mode != string(model.LandingPR) || p.Landing.Base == "" || !validTaskLandingBase(p.Landing.Base) {
		return "", errors.New("task landing PR policy is not configured")
	}
	policy := struct {
		Project          string `json:"project"`
		Mode             string `json:"mode"`
		Base             string `json:"base"`
		Repository       string `json:"repository"`
		GitHubRepository string `json:"github_repository"`
	}{project, mode, p.Landing.Base, repo, githubRepo}
	raw, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validTaskLandingBase(base string) bool {
	if base == "" || strings.HasPrefix(base, "-") || strings.Contains(base, "..") || strings.ContainsAny(base, " ~^:?*[\\") || strings.HasSuffix(base, ".") || strings.HasSuffix(base, ".lock") || strings.Contains(base, "//") {
		return false
	}
	for _, r := range base {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/", r)) {
			return false
		}
	}
	for _, segment := range strings.Split(base, "/") {
		if segment == "" || segment == "." || strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "-") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

// AppendTaskLanding validates and appends an observation to the canonical task
// document. Callers must hold the stable task lock and save the returned task.
func AppendTaskLanding(t *Task, observation TaskLandingObservation) error {
	if t == nil || t.Doc == nil {
		return errors.New("task landing target unavailable")
	}
	if err := validateTaskLandingEnvelope(t, observation); err != nil {
		return err
	}
	records, present, err := taskLandingRecords(t)
	if err != nil {
		return err
	}
	marker, marked := t.Doc.Front.Get("native_landing.current")
	if err := validateTaskLandingHistory(t, records, present, marker, marked); err != nil {
		return err
	}
	if !present && !marked {
		if observation.PreviousID != "" {
			return errors.New("task landing previous receipt does not match current task")
		}
	} else {
		current := records[len(records)-1]
		if current.OperationKey == observation.OperationKey {
			a, _ := TaskLandingID(current)
			b, _ := TaskLandingID(observation)
			if a == b && current.ID == observation.ID {
				return nil
			}
			return errors.New("task landing operation key was replayed with different content")
		}
		if observation.PreviousID != marker {
			return errors.New("task landing current receipt changed")
		}
	}
	for _, record := range records {
		if record.ID == observation.ID {
			return errors.New("task landing receipt ID already exists in history")
		}
		if record.OperationKey == observation.OperationKey {
			return errors.New("task landing operation key already exists in history")
		}
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	if len(encoded) > maxTaskLandingRecord {
		return errors.New("task landing record exceeds size limit")
	}
	content := ""
	if sec, ok := t.Doc.Section(taskLandingSection); ok {
		content = sec.Content
	}
	if len(content)+len(encoded)+1 > maxTaskLandingHistory {
		return errors.New("task landing history exceeds size limit")
	}
	if strings.TrimSpace(content) != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += string(encoded) + "\n"
	t.Doc.SetSection(taskLandingSection, content)
	t.Doc.Front.Set("native_landing.current", observation.ID)
	return nil
}

func validateTaskLandingEnvelope(t *Task, o TaskLandingObservation) error {
	if o.Schema != taskLandingSchema || o.ID == "" || o.OperationKey == "" || !taskLandingOperationKey.MatchString(o.OperationKey) || o.WorkspaceID == "" || o.InstallationID == "" || o.Project != t.Project || o.TaskID != t.ID || o.Generation < 0 || o.DeliveryGeneration < 0 || o.TaskBindingDigest == "" || o.LandingPolicyDigest == "" || o.Repository == "" || o.GitHubRepository == "" || o.Source.AttemptID == "" || o.Source.RevisionID == "" || o.Source.Sequence == 0 || o.Source.Repository == "" || o.Source.Checkout == "" || o.PRNumber <= 0 || o.PRURL == "" || o.PRState != "MERGED" || o.PRHead == "" || o.PRBase == "" || o.MergeCommit == "" || o.TargetRef == "" || o.TargetCommit == "" || o.TargetTree == "" || o.VerificationDigest == "" || o.ObservedAt.IsZero() {
		return errors.New("task landing observation is incomplete")
	}
	wantID, err := TaskLandingID(o)
	if err != nil || wantID != o.ID {
		return errors.New("task landing observation ID is invalid")
	}
	if !taskLandingHexOID.MatchString(o.Source.BaseCommit) || !taskLandingHexOID.MatchString(o.Source.Commit) || !taskLandingHexOID.MatchString(o.Source.Tree) || !taskLandingHexOID.MatchString(o.IntegratedBaseCommit) || !taskLandingHexOID.MatchString(o.IntegratedCommit) || !taskLandingHexOID.MatchString(o.PRHead) || !taskLandingHexOID.MatchString(o.MergeCommit) || !taskLandingHexOID.MatchString(o.TargetCommit) || !taskLandingHexOID.MatchString(o.TargetTree) {
		return errors.New("task landing observation has an invalid object ID")
	}
	if !filepath.IsAbs(o.Source.Repository) || filepath.Clean(o.Source.Repository) != o.Source.Repository || !filepath.IsAbs(o.Source.Checkout) || filepath.Clean(o.Source.Checkout) != o.Source.Checkout || !filepath.IsAbs(o.Repository) || filepath.Clean(o.Repository) != o.Repository {
		return errors.New("task landing repository path is invalid")
	}
	if !taskLandingRepoSlug.MatchString(o.GitHubRepository) || strings.Contains(o.GitHubRepository, "..") || o.PRBase == "" || !validTaskLandingBase(o.PRBase) || o.TargetRef != "refs/heads/"+o.PRBase {
		return errors.New("task landing repository or ref is invalid")
	}
	if o.PRURL != "https://github.com/"+o.GitHubRepository+"/pull/"+strconv.Itoa(o.PRNumber) {
		return errors.New("task landing pull request URL is not canonical")
	}
	if o.PreviousID != "" && !validTaskLandingDigest(o.PreviousID) {
		return errors.New("task landing previous receipt is invalid")
	}
	for _, digest := range []string{o.TaskBindingDigest, o.LandingPolicyDigest, o.VerificationDigest} {
		if !validTaskLandingDigest(digest) {
			return errors.New("task landing digest is invalid")
		}
	}
	return nil
}

func validTaskLandingDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") && len(s) == len("sha256:")+64 && strings.Trim(s[len("sha256:"):], "0123456789abcdef") == ""
}

// ReadTaskLanding returns (result,true) when any native marker/history exists.
// Invalid present state is unknown and must not fall back to legacy branches.
func ReadTaskLanding(ctx context.Context, w *workspace.Workspace, t *Task, base string) (TaskLandingResult, bool) {
	result := TaskLandingResult{State: LandingUnknown, Source: "native-observation", Reason: "landing_unknown"}
	if t == nil || t.Doc == nil || w == nil {
		return result, false
	}
	records, present, err := taskLandingRecords(t)
	marker, marked := t.Doc.Front.Get("native_landing.current")
	if !present && !marked {
		return TaskLandingResult{}, false
	}
	result.ReceiptID = marker
	if err != nil || validateTaskLandingHistory(t, records, present, marker, marked) != nil {
		result.Reason = "landing_receipt_invalid"
		return result, true
	}
	selected := records[len(records)-1]
	result.ReceiptID = selected.ID
	if err := ValidateTaskLanding(ctx, w, t, selected, base); err != nil {
		if errors.Is(err, errTaskLandingUnlanded) {
			result.State, result.Reason = LandingUnlanded, "landing_not_in_target"
			return result, true
		}
		result.Reason = taskLandingReason(err)
		return result, true
	}
	result.State, result.Reason = LandingLanded, "landing_verified"
	return result, true
}

// ReadTaskLandingOperation returns only the current observation for an exact
// idempotency key. It never reselects an older record from the append-only log.
func ReadTaskLandingOperation(t *Task, operationKey string) (TaskLandingObservation, bool, error) {
	if t == nil || t.Doc == nil || operationKey == "" {
		return TaskLandingObservation{}, false, errors.New("task landing operation lookup is invalid")
	}
	records, present, err := taskLandingRecords(t)
	if err != nil {
		return TaskLandingObservation{}, false, err
	}
	marker, marked := t.Doc.Front.Get("native_landing.current")
	if !present && !marked {
		return TaskLandingObservation{}, false, nil
	}
	if err := validateTaskLandingHistory(t, records, present, marker, marked); err != nil {
		return TaskLandingObservation{}, false, err
	}
	current := records[len(records)-1]
	if current.OperationKey == operationKey {
		return current, true, nil
	}
	for _, record := range records[:len(records)-1] {
		if record.OperationKey == operationKey {
			return TaskLandingObservation{}, false, errors.New("task landing operation is no longer current")
		}
	}
	return TaskLandingObservation{}, false, nil
}

func validateTaskLandingHistory(t *Task, records []TaskLandingObservation, present bool, marker string, marked bool) error {
	if !present && !marked {
		return nil
	}
	if !marked || marker == "" || len(records) == 0 || marker != records[len(records)-1].ID {
		return errors.New("task landing current receipt history is invalid")
	}
	ids, keys := make(map[string]bool, len(records)), make(map[string]bool, len(records))
	for i, record := range records {
		if err := validateTaskLandingEnvelope(t, record); err != nil {
			return err
		}
		if (i == 0 && record.PreviousID != "") || (i > 0 && record.PreviousID != records[i-1].ID) {
			return errors.New("task landing receipt chain is invalid")
		}
		if ids[record.ID] || keys[record.OperationKey] {
			return errors.New("task landing history contains duplicate identity")
		}
		ids[record.ID], keys[record.OperationKey] = true, true
	}
	return nil
}

var errTaskLandingUnlanded = errors.New("task landing commit is not contained by configured target")

func taskLandingReason(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "binding") || strings.Contains(s, "generation") || strings.Contains(s, "claims") || strings.Contains(s, "criteria"):
		return "landing_task_changed"
	case strings.Contains(s, "policy") || strings.Contains(s, "repository") || strings.Contains(s, "ref"):
		return "landing_policy_changed"
	case strings.Contains(s, "verification"):
		return "landing_verification_invalid"
	default:
		return "landing_evidence_unknown"
	}
}

func taskLandingRecords(t *Task) ([]TaskLandingObservation, bool, error) {
	sec, present := t.Doc.Section(taskLandingSection)
	if !present {
		return nil, false, nil
	}
	if len(sec.Content) > maxTaskLandingHistory {
		return nil, true, errors.New("task landing history exceeds size limit")
	}
	records := make([]TaskLandingObservation, 0)
	scanner := bufio.NewScanner(strings.NewReader(sec.Content))
	scanner.Buffer(make([]byte, 4096), maxTaskLandingHistory)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if len(line) > maxTaskLandingRecord {
			return nil, true, errors.New("task landing record exceeds size limit")
		}
		if len(records) >= maxTaskLandingRecords {
			return nil, true, errors.New("task landing history record limit exceeded")
		}
		var record TaskLandingObservation
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&record); err != nil {
			return nil, true, err
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			return nil, true, errors.New("task landing record has trailing JSON")
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, true, err
	}
	return records, true, nil
}

// ValidateTaskLanding checks the current task/policy bindings, exact source
// and integration range deltas, PR/merge content, target ancestry, and stored
// final-tree verification using only bounded local Git reads.
func ValidateTaskLanding(parent context.Context, w *workspace.Workspace, t *Task, o TaskLandingObservation, base string) error {
	if w == nil || t == nil || t.Doc == nil {
		return errors.New("task landing validation target unavailable")
	}
	if err := validateTaskLandingEnvelope(t, o); err != nil {
		return err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, taskLandingReadTimeout)
	defer cancel()
	if o.WorkspaceID != w.ID {
		return errors.New("task landing workspace binding changed")
	}
	generation, err := strictTaskGeneration(t.Doc, "generation")
	if err != nil || generation != o.Generation {
		return errors.New("task landing generation binding changed")
	}
	deliveryGeneration, err := strictTaskGeneration(t.Doc, "delivery_generation")
	if err != nil || deliveryGeneration != o.DeliveryGeneration {
		return errors.New("task landing delivery generation binding changed")
	}
	binding, err := TaskLandingBindingDigest(t)
	if err != nil || binding != o.TaskBindingDigest {
		return errors.New("task landing task binding changed")
	}
	policy, err := taskLandingPolicyDigest(ctx, w, t.Project)
	if err != nil || policy != o.LandingPolicyDigest {
		return errors.New("task landing policy changed")
	}
	project, err := LoadProject(w, t.Project)
	if err != nil {
		return errors.New("task landing project policy unavailable")
	}
	githubRepo, _ := project.Doc.Front.Get("github_repo")
	if strings.TrimSpace(githubRepo) != o.GitHubRepository || project.Landing.Base != base || o.PRBase != base || o.TargetRef != "refs/heads/"+base {
		return errors.New("task landing repository or ref policy changed")
	}
	targetRepo, err := targetRepositoryContext(ctx, w.Root)
	if err != nil || targetRepo != o.Repository {
		return errors.New("task landing target repository binding changed")
	}
	if err := verifyTaskLandingGit(ctx, w.Root, t, o); err != nil {
		return err
	}
	return nil
}

func targetRepositoryContext(ctx context.Context, root string) (string, error) {
	if ctx == nil {
		return "", errors.New("task landing context required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(canonical) || filepath.Clean(canonical) != canonical {
		return "", errors.New("task landing workspace root is unavailable")
	}
	out, err := taskLandingGitRun(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	common, err = filepath.EvalSymlinks(common)
	if err != nil || !filepath.IsAbs(common) || filepath.Clean(common) != common {
		return "", errors.New("task landing common repository unavailable")
	}
	return common, nil
}

// TaskLandingRepository returns the canonical common-dir identity of the
// workspace's local Git repository. It performs no fetch or other network
// operation and is bounded by the landing read deadline.
func TaskLandingRepository(w *workspace.Workspace) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskLandingReadTimeout)
	defer cancel()
	return TaskLandingRepositoryContext(ctx, w)
}

// TaskLandingRepositoryContext observes the workspace common-dir under the
// caller's shared operation deadline.
func TaskLandingRepositoryContext(ctx context.Context, w *workspace.Workspace) (string, error) {
	if ctx == nil {
		return "", errors.New("task landing context required")
	}
	if w == nil {
		return "", errors.New("task landing workspace unavailable")
	}
	return targetRepositoryContext(ctx, w.Root)
}

func verifyTaskLandingGit(ctx context.Context, root string, t *Task, o TaskLandingObservation) error {
	shallow, err := taskLandingGitRun(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(string(shallow)) != "false" {
		return errors.New("task landing repository ancestry is incomplete")
	}
	target, err := taskLandingGitRun(ctx, root, "rev-parse", "--verify", o.TargetRef+"^{commit}")
	if err != nil {
		return errors.New("task landing configured target unavailable")
	}
	actualTarget := strings.TrimSpace(string(target))
	if actualTarget != o.TargetCommit {
		ancestor, checkErr := taskLandingAncestor(ctx, root, o.TargetCommit, actualTarget)
		if checkErr != nil {
			return checkErr
		}
		if !ancestor {
			return errTaskLandingUnlanded
		}
	}
	for _, oid := range []string{o.Source.BaseCommit, o.Source.Commit, o.Source.Tree, o.IntegratedBaseCommit, o.IntegratedCommit, o.PRHead, o.MergeCommit, o.TargetCommit, o.TargetTree} {
		if _, err := taskLandingGitRun(ctx, root, "cat-file", "-e", oid+"^{object}"); err != nil {
			return errors.New("task landing Git object unavailable")
		}
	}
	if err := verifyCommitTree(ctx, root, o.Source.Commit, o.Source.Tree); err != nil {
		return errors.New("task landing source tree differs from genuine commit")
	}
	if err := verifyCommitTree(ctx, root, o.TargetCommit, o.TargetTree); err != nil {
		return errors.New("task landing target tree differs from genuine commit")
	}
	if ancestor, err := taskLandingAncestor(ctx, root, o.Source.BaseCommit, o.Source.Commit); err != nil || !ancestor {
		return errors.New("task landing source base is not ancestor")
	}
	if ancestor, err := taskLandingAncestor(ctx, root, o.IntegratedBaseCommit, o.IntegratedCommit); err != nil || !ancestor {
		return errors.New("task landing integration range is invalid")
	}
	if o.IntegratedBaseCommit == o.IntegratedCommit {
		return errors.New("task landing integration range is empty")
	}
	if ancestor, err := taskLandingAncestor(ctx, root, o.IntegratedCommit, o.PRHead); err != nil || !ancestor {
		return errors.New("task landing integration is not in pull request head")
	}
	if ancestor, err := taskLandingAncestor(ctx, root, o.IntegratedCommit, o.MergeCommit); err != nil || !ancestor {
		return errors.New("task landing integration is not in merge commit")
	}
	if ancestor, err := taskLandingAncestor(ctx, root, o.MergeCommit, o.TargetCommit); err != nil {
		return err
	} else if !ancestor {
		return errTaskLandingUnlanded
	}
	source, err := taskLandingDelta(ctx, root, o.Source.BaseCommit, o.Source.Commit)
	if err != nil || len(source) == 0 {
		return errors.New("task landing source delta unavailable or empty")
	}
	integrated, err := taskLandingDelta(ctx, root, o.IntegratedBaseCommit, o.IntegratedCommit)
	if err != nil {
		return errors.New("task landing integration delta unavailable")
	}
	if err := compareTaskLandingDeltas(source, integrated); err != nil {
		return err
	}
	for _, commit := range []string{o.PRHead, o.MergeCommit} {
		for path, change := range source {
			if err := taskLandingEntryMatches(ctx, root, commit, path, change); err != nil {
				return fmt.Errorf("task landing source content differs at pull request boundary: %w", err)
			}
		}
	}
	if err := taskLandingClaims(t, source); err != nil {
		return err
	}
	if err := taskLandingVerification(t, o); err != nil {
		return err
	}
	// The target ref can move during the preceding Git reads. A fresh local
	// resolve must still identify the same commit or a descendant.
	second, err := taskLandingGitRun(ctx, root, "rev-parse", "--verify", o.TargetRef+"^{commit}")
	if err != nil {
		return errors.New("task landing target changed during validation")
	}
	secondTarget := strings.TrimSpace(string(second))
	if secondTarget != actualTarget {
		ancestor, checkErr := taskLandingAncestor(ctx, root, actualTarget, secondTarget)
		if checkErr != nil || !ancestor {
			return errors.New("task landing target changed during validation")
		}
	}
	if secondTarget != o.TargetCommit {
		ancestor, checkErr := taskLandingAncestor(ctx, root, o.TargetCommit, secondTarget)
		if checkErr != nil {
			return checkErr
		}
		if !ancestor {
			return errTaskLandingUnlanded
		}
	}
	return nil
}

type taskLandingChange struct{ oldMode, newMode, oldOID, newOID, status string }

func taskLandingDelta(ctx context.Context, root, base, tip string) (map[string]taskLandingChange, error) {
	raw, err := taskLandingGitRun(ctx, root, "diff-tree", "--raw", "-r", "--no-commit-id", "--no-renames", "--no-abbrev", "--no-ext-diff", "--no-textconv", "-z", base, tip, "--")
	if err != nil {
		return nil, err
	}
	if len(raw) > maxTaskLandingOutput {
		return nil, errors.New("task landing diff exceeds size limit")
	}
	if len(raw) == 0 {
		return map[string]taskLandingChange{}, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, errors.New("task landing diff is malformed")
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(parts)%2 != 0 || len(parts)/2 > 4096 {
		return nil, errors.New("task landing diff exceeds entry limit")
	}
	out := make(map[string]taskLandingChange, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		fields := strings.Fields(string(parts[i]))
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return nil, errors.New("task landing raw diff header is malformed")
		}
		change := taskLandingChange{oldMode: strings.TrimPrefix(fields[0], ":"), newMode: fields[1], oldOID: fields[2], newOID: fields[3], status: fields[4]}
		path := string(parts[i+1])
		if path == "" || strings.IndexByte(path, 0) >= 0 || len(path) > 4096 || strings.HasPrefix(path, "/") || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || path == ".." {
			return nil, errors.New("task landing diff path is invalid")
		}
		if _, exists := out[path]; exists {
			return nil, errors.New("task landing diff contains duplicate path")
		}
		if change.status != "A" && change.status != "D" && change.status != "M" && change.status != "T" {
			return nil, errors.New("task landing diff has unsupported status")
		}
		if change.oldMode == "160000" || change.newMode == "160000" {
			return nil, errors.New("task landing diff contains unsupported gitlink")
		}
		if (change.oldMode != "000000" && change.oldMode != "100644" && change.oldMode != "100755" && change.oldMode != "120000") || (change.newMode != "000000" && change.newMode != "100644" && change.newMode != "100755" && change.newMode != "120000") {
			return nil, errors.New("task landing diff has unsupported file mode")
		}
		if change.oldMode != "000000" && !taskLandingHexOID.MatchString(change.oldOID) || change.newMode != "000000" && !taskLandingHexOID.MatchString(change.newOID) {
			return nil, errors.New("task landing diff has invalid object ID")
		}
		out[path] = change
	}
	return out, nil
}

func compareTaskLandingDeltas(source, integrated map[string]taskLandingChange) error {
	for path, sourceEntry := range source {
		entry, ok := integrated[path]
		if !ok || sourceEntry != entry {
			return errors.New("task landing integration does not preserve the source delta")
		}
	}
	return nil
}

func taskLandingEntryMatches(ctx context.Context, root, commit, path string, change taskLandingChange) error {
	raw, err := taskLandingGitRun(ctx, root, "ls-tree", "-z", "--full-tree", commit, "--", ":(literal)"+path)
	if err != nil {
		return err
	}
	if change.newMode == "000000" {
		if len(raw) != 0 {
			return errors.New("deleted source path is present")
		}
		return nil
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return errors.New("source path is absent")
	}
	entries := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(entries) != 1 {
		return errors.New("source path resolution is ambiguous")
	}
	entry := string(entries[0])
	sep := strings.IndexByte(entry, '\t')
	if sep < 0 || entry[sep+1:] != path {
		return errors.New("source path resolution differs")
	}
	fields := strings.Fields(entry[:sep])
	if len(fields) != 3 || fields[0] != change.newMode || fields[1] != "blob" || fields[2] != change.newOID {
		return errors.New("source path object differs")
	}
	return nil
}

func taskLandingClaims(t *Task, source map[string]taskLandingChange) error {
	claims := t.Claims()
	if len(claims) == 0 {
		return nil
	}
	valid, err := pathclaims.ValidateClaims(claims)
	if err != nil {
		return errors.New("task landing task claims are invalid")
	}
	for path := range source {
		inside := false
		for _, claim := range valid {
			if path == claim || strings.HasPrefix(path, strings.TrimSuffix(claim, "/")+"/") {
				inside = true
				break
			}
		}
		if !inside {
			return errors.New("task landing source path is outside task claims")
		}
	}
	return nil
}

func taskLandingVerification(t *Task, o TaskLandingObservation) error {
	for _, evidence := range VerificationEvidenceRecords(t) {
		encoded, err := json.Marshal(evidence)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(encoded)
		if "sha256:"+hex.EncodeToString(sum[:]) != o.VerificationDigest {
			continue
		}
		return ValidateFinalTreeVerification(evidence, o.TargetCommit, o.TargetTree)
	}
	return errors.New("task landing final verification evidence is unavailable")
}

func verifyCommitTree(ctx context.Context, root, commit, tree string) error {
	got, err := taskLandingGitRun(ctx, root, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil || strings.TrimSpace(string(got)) != tree {
		return errors.New("commit tree differs")
	}
	return nil
}

func taskLandingAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	_, err := taskLandingGitRun(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func taskLandingGitRun(ctx context.Context, root string, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("task landing context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1"}
	out := &taskLandingCapture{}
	cmd.Stdout, cmd.Stderr = taskLandingStream{capture: out, stdout: true}, taskLandingStream{capture: out}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return append([]byte(nil), out.stdout.Bytes()...), nil
}

type taskLandingCapture struct {
	sync.Mutex
	total  int
	stdout bytes.Buffer
}

type taskLandingStream struct {
	capture *taskLandingCapture
	stdout  bool
}

func (s taskLandingStream) Write(p []byte) (int, error) {
	c := s.capture
	c.Lock()
	defer c.Unlock()
	if c.total+len(p) > maxTaskLandingOutput {
		return 0, errors.New("task landing Git output exceeds limit")
	}
	c.total += len(p)
	if s.stdout {
		return c.stdout.Write(p)
	}
	return len(p), nil
}
