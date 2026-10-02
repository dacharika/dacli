package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/eventlog"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

const defaultReviewCorrections = 2

func (d *driver) structuredReviewRequired() bool {
	profile, err := loadProfile(d.w, d.cfg.project)
	return err == nil && (profile.Verification.IndependentReviews > 0 || profile.Landing.ReviewsRequired > 0)
}

func (d *driver) reviewCorrectionLimit() int {
	profile, err := loadProfile(d.w, d.cfg.project)
	if err == nil && profile.Verification.CorrectionTurns > 0 {
		return profile.Verification.CorrectionTurns
	}
	return defaultReviewCorrections
}

func (d *driver) observeTaskBranch(t *store.Task) (string, string, error) {
	commit, err := d.git("rev-parse", "--verify", taskBranch(t))
	if err != nil {
		return "", "", err
	}
	commit = strings.TrimSpace(commit)
	tree, err := d.git("rev-parse", "--verify", commit+"^{tree}")
	return commit, strings.TrimSpace(tree), err
}

func (d *driver) reviewEventIDs(taskID string) (map[string]bool, error) {
	events, err := eventlog.List(d.w, eventlog.Query{About: taskID, Kinds: []model.EventKind{model.EventReview}})
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.ID] = true
	}
	return seen, err
}

func (d *driver) spawnReviewResult(t *store.Task) (store.IndependentReviewResult, string, error) {
	before, err := d.reviewEventIDs(t.ID)
	if err != nil {
		return store.IndependentReviewResult{}, "", err
	}
	args := []string{"spawn", "--task", t.ID, "--role", d.cfg.reviewRole, "--review", "--structured-review-result"}
	if d.reviewLaunchFingerprint != "" {
		args = append(args, "--preflight-fingerprint", d.reviewLaunchFingerprint)
	}
	for _, harness := range d.cfg.allowedHarnesses {
		args = append(args, "--harness", harness)
	}
	if d.cfg.perCycleTok > 0 {
		args = append(args, "--max-tokens", fmt.Sprint(d.cfg.perCycleTok))
		if d.cfg.allowAdvisoryTokens {
			args = append(args, "--allow-advisory-tokens")
		}
	}
	args = d.contextExceptionArgs(args)
	args = append(args, "--timeout", fmt.Sprint(d.workerTimeout(t)))
	var spawn commandresult.Spawn
	var runErr error
	if rr, ok := d.run.(resultRunner); ok {
		_, runErr = rr.runResult("delivery-review", &spawn, args...)
	} else {
		_, runErr = d.run.run("delivery-review", args...)
	}
	if runErr != nil {
		return store.IndependentReviewResult{}, spawn.RunID, runErr
	}
	events, err := eventlog.List(d.w, eventlog.Query{About: t.ID, Kinds: []model.EventKind{model.EventReview}})
	if err != nil {
		return store.IndependentReviewResult{}, spawn.RunID, err
	}
	for _, event := range events {
		if before[event.ID] {
			continue
		}
		var result store.IndependentReviewResult
		if err := json.Unmarshal([]byte(event.Body), &result); err != nil {
			return result, spawn.RunID, fmt.Errorf("decode review event %s: %w", event.ID, err)
		}
		return result, spawn.RunID, nil
	}
	return store.IndependentReviewResult{}, spawn.RunID, fmt.Errorf("review run %s returned no %s output", spawn.RunID, store.ReviewResultSchema)
}

func (d *driver) runReviewCorrection(t *store.Task, tx *store.ReviewTransaction) error {
	currentCommit, currentTree, err := d.observeTaskBranch(t)
	if err != nil {
		return err
	}
	if currentTree != tx.PriorTree {
		return tx.MarkCorrected(currentCommit, currentTree, d.now())
	}
	store.AppendLog(t, fmt.Sprintf("independent review correction turn %d/%d binds findings %s to prior commit/tree %s/%s; correction must preserve branch %s and claims", tx.CorrectionTurns, tx.MaxCorrections, strings.Join(tx.FindingIDs, ","), tx.PriorCommit, tx.PriorTree, tx.Branch))
	if err := store.SaveTask(t); err != nil {
		return err
	}
	args := []string{"spawn", "--task", t.ID, "--role", d.buildRole(), "--worktree"}
	for _, harness := range d.cfg.allowedHarnesses {
		args = append(args, "--harness", harness)
	}
	if claim := strings.Join(store.ClaimHints(d.w.Root, t), ","); claim != "" {
		args = append(args, "--claim", claim)
	}
	if d.cfg.perCycleTok > 0 {
		args = append(args, "--max-tokens", fmt.Sprint(d.cfg.perCycleTok))
		if d.cfg.allowAdvisoryTokens {
			args = append(args, "--allow-advisory-tokens")
		}
	}
	args = d.contextExceptionArgs(args)
	args = append(args, "--timeout", fmt.Sprint(d.workerTimeout(t)))
	if out, err := d.run.run("review-correction", args...); err != nil {
		return fmt.Errorf("correction launch: %s: %w", strings.TrimSpace(out), err)
	}
	if _, err := d.run.run("review-correction-sync", "sync"); err != nil {
		return err
	}
	commit, tree, err := d.observeTaskBranch(t)
	if err != nil {
		return err
	}
	return tx.MarkCorrected(commit, tree, d.now())
}

// reviewDeliveryTask is a bounded, crash-resumable transaction. Only an exact
// approve on the current tree reaches landing; silence, stale output, refusal,
// infrastructure failure, and exhausted corrections all leave the task open.
func (d *driver) reviewDeliveryTask(t *store.Task) bool {
	commit, tree, err := d.observeTaskBranch(t)
	if err != nil {
		d.logf("    %03d review: cannot observe branch: %v", t.Seq, err)
		return false
	}
	tx, err := store.ReadReviewTransaction(d.w, d.cfg.project, t.ID)
	if os.IsNotExist(err) {
		tx = store.ReviewTransaction{Project: d.cfg.project, TaskID: t.ID, Branch: taskBranch(t), State: store.ReviewAwaiting, MaxCorrections: d.reviewCorrectionLimit(), CurrentCommit: commit, CurrentTree: tree, UpdatedAt: d.now().UTC()}
	} else if err != nil {
		d.logf("    %03d review: corrupt recovery checkpoint: %v", t.Seq, err)
		return false
	}
	if tx.State == store.ReviewApproved && tx.CurrentCommit == commit && tx.CurrentTree == tree {
		return true
	}
	for tx.CorrectionTurns <= tx.MaxCorrections {
		switch tx.State {
		case store.ReviewCorrection:
			if !d.checkpointTaskPhase(t, phaseCorrectionPending) {
				return false
			}
			if err := d.runReviewCorrection(t, &tx); err != nil {
				d.logf("    %03d review correction: %v", t.Seq, err)
				if policyLaunchRefusal(err) {
					// A partially delivered wave must not turn exit 3 into another
					// correction attempt. Halt durably and retain findings/revision.
					tx.State, tx.UpdatedAt = store.ReviewHalted, d.now().UTC()
					d.recordCycleFailure("review-correction", err, err.Error())
					d.phaseErr = clikit.Refusedf("correction policy refusal: %v", err)
				}
				if writeErr := store.WriteReviewTransaction(d.w, tx); writeErr != nil {
					if policyLaunchRefusal(err) {
						d.phaseErr = clikit.Refusedf("%v; persist correction failure: %v", d.phaseErr, writeErr)
					} else {
						d.phaseErr = fmt.Errorf("persist correction failure: %w", writeErr)
					}
				}
				return false
			}
			if err := store.WriteReviewTransaction(d.w, tx); err != nil {
				d.logf("    %03d review checkpoint: %v", t.Seq, err)
				return false
			}
			if !d.checkpointTaskPhase(t, phaseRereviewPending) {
				return false
			}
			continue
		case store.ReviewAwaiting, store.ReviewAwaitingRereview:
			if !d.checkpointTaskPhase(t, phaseReviewPending) {
				return false
			}
			result, runID, reviewErr := d.spawnReviewResult(t)
			tx.ReviewRunID = runID
			if reviewErr != nil {
				tx.State = store.ReviewHalted
				tx.UpdatedAt = d.now().UTC()
				_ = store.WriteReviewTransaction(d.w, tx)
				d.logf("    %03d review: %v", t.Seq, reviewErr)
				return false
			}
			commit, tree, err = d.observeTaskBranch(t)
			if err != nil {
				return false
			}
			if err := tx.Apply(result, commit, tree, d.now()); err != nil {
				_ = store.WriteReviewTransaction(d.w, tx)
				d.logf("    %03d review verdict %s: %v", t.Seq, result.Verdict, err)
				return false
			}
			if err := store.WriteReviewTransaction(d.w, tx); err != nil {
				return false
			}
			if tx.State == store.ReviewApproved {
				if !d.checkpointTaskPhase(t, phaseReviewed) {
					return false
				}
				d.logf("    %03d review approved exact tree %s", t.Seq, tree)
				return true
			}
			continue
		default:
			d.logf("    %03d review halted in state %s", t.Seq, tx.State)
			return false
		}
	}
	return false
}
