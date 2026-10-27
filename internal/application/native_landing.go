package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

const (
	nativeLandingTimeout    = 15 * time.Second
	nativeLandingMaxRequest = 64 << 10
)

var nativeLandingOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var nativeLandingRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var nativeLandingOperationKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// NativeLandingRequest selects historical published source and exact PR and
// target observations. It cannot assert that any observation succeeded.
type NativeLandingRequest struct {
	OperationKey         string `json:"operation_key"`
	ExpectedPreviousID   string `json:"expected_previous_id"`
	TaskID               string `json:"task_id"`
	AttemptID            string `json:"attempt_id"`
	RevisionID           string `json:"revision_id"`
	GitHubRepository     string `json:"github_repository"`
	PRNumber             int    `json:"pr_number"`
	ExpectedPRHead       string `json:"expected_pr_head"`
	IntegratedBaseCommit string `json:"integrated_base_commit"`
	IntegratedCommit     string `json:"integrated_commit"`
	TargetCommit         string `json:"target_commit"`
	TargetTree           string `json:"target_tree"`
}

// NativeLandingPRObservation is the bounded read-only PR data needed to bind
// a native source publication to the configured merge target.
type NativeLandingPRObservation struct {
	Number         int
	URL            string
	State          string
	HeadRefOID     string
	BaseRefName    string
	MergeCommitOID string
}

// NativeLandingPRObserver supplies PR observations from the caller's trusted
// governance adapter; the application layer never launches provider tools.
type NativeLandingPRObserver func(context.Context, string, int) (NativeLandingPRObservation, error)

// Record binds a historical native publication to exact merged PR content and
// a locally verified target commit. Remote observation is read-only; the only
// write is an append to the canonical task while the owner and task locks are
// held.
func (p *NativeLandingOwner) Record(ctx context.Context, request NativeLandingRequest) (store.TaskLandingObservation, error) {
	if p == nil || ctx == nil || !validNativeLandingRequest(request) {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, nativeLandingTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	authority, owner, err := p.authenticate()
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	project, err := store.LoadProject(p.workspace, p.project)
	if err != nil || project.Landing.Mode != model.LandingPR || project.Landing.Base == "" {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	githubRepository, _ := project.Doc.Front.Get("github_repo")
	githubRepository = strings.TrimSpace(githubRepository)
	if !validNativeLandingRepository(githubRepository) || request.GitHubRepository != githubRepository {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	source, err := authority.OwnerReadNativeLandingSource(owner, p.project, request.TaskID, request.AttemptID, request.RevisionID)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	task, err := store.NativeAcceptanceTask(p.workspace, request.TaskID, p.project)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	verificationDigest, err := nativeLandingVerificationDigest(task, request.TargetCommit, request.TargetTree)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	if p.observePR == nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	pr, err := p.observePR(ctx, githubRepository, request.PRNumber)
	if err != nil || !validNativeLandingPRObservation(pr, githubRepository, project.Landing.Base, request) {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	if err = p.selectedOwnerCurrent(owner); err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	policyDigest, err := store.TaskLandingPolicyDigestContext(ctx, p.workspace, p.project)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	targetRepository, err := store.TaskLandingRepositoryContext(ctx, p.workspace)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	bindingDigest, err := store.TaskLandingBindingDigest(task)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	generation, err := nativeLandingGeneration(task, "generation")
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	deliveryGeneration, err := nativeLandingGeneration(task, "delivery_generation")
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	observation := store.TaskLandingObservation{
		Schema: "task-landing/v1", OperationKey: request.OperationKey, PreviousID: request.ExpectedPreviousID,
		WorkspaceID: p.workspace.ID, InstallationID: p.owner.config.InstallationID,
		Project: p.project, TaskID: request.TaskID, Generation: generation, DeliveryGeneration: deliveryGeneration,
		TaskBindingDigest: bindingDigest, LandingPolicyDigest: policyDigest,
		Repository: targetRepository, GitHubRepository: githubRepository,
		Source: store.TaskLandingSource{
			AttemptID: source.Attempt.ID, RevisionID: source.Revision.ID, Sequence: source.Revision.Sequence,
			Repository: source.Binding.Repository, BaseCommit: source.Attempt.Observation.Commit,
			Commit: source.Revision.Observation.Commit, Tree: source.Revision.Observation.Tree,
			Checkout: source.Attempt.Observation.Locator,
		},
		IntegratedBaseCommit: request.IntegratedBaseCommit, IntegratedCommit: request.IntegratedCommit,
		PRNumber: pr.Number, PRURL: pr.URL, PRState: pr.State, PRHead: pr.HeadRefOID, PRBase: pr.BaseRefName,
		MergeCommit: pr.MergeCommitOID, TargetRef: "refs/heads/" + project.Landing.Base,
		TargetCommit: request.TargetCommit, TargetTree: request.TargetTree,
		VerificationDigest: verificationDigest, ObservedAt: time.Now().UTC(),
	}
	observation.ID, err = store.TaskLandingID(observation)
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	var recorded store.TaskLandingObservation
	err = authority.WithOwnerNativeLandingSource(owner, p.project, request.TaskID, request.AttemptID, request.RevisionID, func(currentSource sessions.NativeLandingSource) error {
		if !sameNativeLandingSource(source, currentSource) || p.selectedOwnerCurrent(owner) != nil {
			return sessions.ErrDenied
		}
		return store.WithNativeAcceptanceTask(p.workspace, request.TaskID, p.project, func(currentTask *store.Task) error {
			if ctx.Err() != nil || currentTask == nil || currentTask.ID != request.TaskID || currentTask.Project != p.project {
				return sessions.ErrDenied
			}
			currentProject, err := store.LoadProject(p.workspace, p.project)
			if err != nil || currentProject.Landing != project.Landing {
				return sessions.ErrDenied
			}
			currentGithubRepo, _ := currentProject.Doc.Front.Get("github_repo")
			if currentGithubRepo != githubRepository {
				return sessions.ErrDenied
			}
			currentPolicyDigest, err := store.TaskLandingPolicyDigestContext(ctx, p.workspace, p.project)
			if err != nil || currentPolicyDigest != observation.LandingPolicyDigest {
				return sessions.ErrDenied
			}
			currentBindingDigest, err := store.TaskLandingBindingDigest(currentTask)
			if err != nil || currentBindingDigest != observation.TaskBindingDigest {
				return sessions.ErrDenied
			}
			previous, exists, err := store.ReadTaskLandingOperation(currentTask, request.OperationKey)
			if err != nil {
				return sessions.ErrDenied
			}
			if exists {
				if !nativeLandingSameRequest(previous, observation) || p.validateTaskLanding(ctx, currentTask, previous, project.Landing.Base) != nil {
					return sessions.ErrDenied
				}
				if p.selectedOwnerCurrent(owner) != nil || ctx.Err() != nil || p.selectedLandingPolicyCurrent(ctx, project.Landing, githubRepository, observation.LandingPolicyDigest) != nil {
					return sessions.ErrDenied
				}
				recorded = previous
				return nil
			}
			currentID, marked := currentTask.Doc.Front.Get("native_landing.current")
			if !marked {
				currentID = ""
			}
			if currentID != request.ExpectedPreviousID {
				return sessions.ErrDenied
			}
			if err := p.validateTaskLanding(ctx, currentTask, observation, project.Landing.Base); err != nil {
				return sessions.ErrDenied
			}
			if p.selectedOwnerCurrent(owner) != nil || ctx.Err() != nil || p.selectedLandingPolicyCurrent(ctx, project.Landing, githubRepository, observation.LandingPolicyDigest) != nil {
				return sessions.ErrDenied
			}
			if err := store.AppendTaskLanding(currentTask, observation); err != nil {
				return sessions.ErrDenied
			}
			if err := store.SaveTask(currentTask); err != nil {
				return sessions.ErrStorage
			}
			recorded = observation
			return nil
		})
	})
	if err != nil {
		return store.TaskLandingObservation{}, sessions.ErrDenied
	}
	return recorded, nil
}

func nativeLandingSameRequest(previous, candidate store.TaskLandingObservation) bool {
	candidate.ID = previous.ID
	candidate.ObservedAt = previous.ObservedAt
	return reflect.DeepEqual(previous, candidate)
}

func validNativeLandingRequest(request NativeLandingRequest) bool {
	if !nativeLandingOperationKey.MatchString(request.OperationKey) || len(request.ExpectedPreviousID) > 80 || len(request.TaskID) > 256 || len(request.AttemptID) > 256 || len(request.RevisionID) > 256 || len(request.GitHubRepository) > 512 || request.PRNumber < 1 || request.PRNumber > 1_000_000_000 || !nativeLandingOID.MatchString(request.ExpectedPRHead) || !nativeLandingOID.MatchString(request.IntegratedBaseCommit) || !nativeLandingOID.MatchString(request.IntegratedCommit) || !nativeLandingOID.MatchString(request.TargetCommit) || !nativeLandingOID.MatchString(request.TargetTree) {
		return false
	}
	for _, value := range []string{request.TaskID, request.AttemptID, request.RevisionID} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\/\x00\r\n") {
			return false
		}
	}
	if request.ExpectedPreviousID != "" && (len(request.ExpectedPreviousID) != 71 || !strings.HasPrefix(request.ExpectedPreviousID, "sha256:") || !nativeLandingOID.MatchString(request.ExpectedPreviousID[7:])) {
		return false
	}
	encoded, err := json.Marshal(request)
	return err == nil && len(encoded) <= nativeLandingMaxRequest
}

func validNativeLandingRepository(repository string) bool {
	return nativeLandingRepo.MatchString(repository) && !strings.Contains(repository, "..") && !strings.HasSuffix(repository, ".")
}

func validNativeLandingPRObservation(pr NativeLandingPRObservation, repository, base string, request NativeLandingRequest) bool {
	if pr.Number != request.PRNumber || pr.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repository, request.PRNumber) || pr.State != "MERGED" || pr.HeadRefOID != request.ExpectedPRHead || pr.BaseRefName != base || !nativeLandingOID.MatchString(pr.MergeCommitOID) {
		return false
	}
	return true
}

func sameNativeLandingSource(left, right sessions.NativeLandingSource) bool {
	return left.Binding == right.Binding && left.Attempt == right.Attempt && left.Revision == right.Revision
}

func nativeLandingGeneration(task *store.Task, key string) (int, error) {
	raw, ok := task.Doc.Front.Get(key)
	if !ok {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || strconv.Itoa(value) != raw {
		return 0, errors.New("task generation is malformed")
	}
	return value, nil
}

func nativeLandingVerificationDigest(task *store.Task, commit, tree string) (string, error) {
	for _, evidence := range store.VerificationEvidenceRecords(task) {
		if store.ValidateFinalTreeVerification(evidence, commit, tree) != nil {
			continue
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(encoded)
		return "sha256:" + hex.EncodeToString(digest[:]), nil
	}
	return "", sessions.ErrDenied
}
