package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/pathclaims"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const taskPathClaimsSchema = "task-path-claims/v1"

// TaskPathClaimsPlan is a digest-bound owner proposal. It has no authority on
// its own; native owner authentication is required again when applying it.
type TaskPathClaimsPlan struct {
	Schema     string    `json:"schema"`
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	Project    string    `json:"project"`
	Status     string    `json:"status"`
	TaskDigest string    `json:"task_digest"`
	OldClaims  []string  `json:"old_claims"`
	NewClaims  []string  `json:"new_claims"`
	CreatedAt  time.Time `json:"created_at"`
	AppliedAt  time.Time `json:"applied_at,omitempty"`
}

func validateOwnerTaskClaims(root string, claims []string) ([]string, error) {
	validated, err := pathclaims.ValidateClaims(claims)
	if err != nil {
		return nil, err
	}
	for _, claim := range validated {
		// `.dacli` is workspace metadata, outside user-authored task scope.
		for _, segment := range strings.Split(claim, "/") {
			if segment == ".dacli" {
				return nil, fmt.Errorf("workspace metadata cannot be a task path claim")
			}
		}
		p := root
		for _, segment := range strings.Split(claim, "/") {
			p = filepath.Join(p, segment)
			info, statErr := os.Lstat(p)
			if os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("unsafe symlink or unreadable path claim %q", claim)
			}
		}
	}
	return validated, nil
}

func taskPathClaimsID(plan TaskPathClaimsPlan) (string, error) {
	plan.ID = ""
	plan.AppliedAt = time.Time{}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func taskFileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// PlanTaskPathClaims snapshots exact current task bytes and validates the
// replacement set before producing a plan for owner review.
func PlanTaskPathClaims(w *workspace.Workspace, task *Task, requested []string, now time.Time) (TaskPathClaimsPlan, error) {
	var plan TaskPathClaimsPlan
	newClaims, err := validateOwnerTaskClaims(w.Root, requested)
	if err != nil {
		return plan, err
	}
	oldClaims := task.Claims()
	if len(oldClaims) > 0 {
		oldClaims, err = validateOwnerTaskClaims(w.Root, oldClaims)
		if err != nil {
			return plan, fmt.Errorf("current task claims are invalid: %w", err)
		}
	}
	digest, err := taskFileDigest(task.Path)
	if err != nil {
		return plan, err
	}
	plan = TaskPathClaimsPlan{Schema: taskPathClaimsSchema, TaskID: task.ID, Project: task.Project, Status: string(task.Status), TaskDigest: digest, OldClaims: oldClaims, NewClaims: newClaims, CreatedAt: now.UTC()}
	plan.ID, err = taskPathClaimsID(plan)
	return plan, err
}

// ApplyTaskPathClaims is a compare-and-swap on the full task file. Native
// authority protection is held by the caller around this operation.
func ApplyTaskPathClaims(w *workspace.Workspace, task *Task, plan TaskPathClaimsPlan, now time.Time) (TaskPathClaimsPlan, error) {
	want, err := taskPathClaimsID(plan)
	if err != nil || plan.Schema != taskPathClaimsSchema || plan.ID != want || plan.TaskID != task.ID || plan.Project != task.Project || plan.Status != string(task.Status) || plan.TaskDigest == "" || plan.CreatedAt.IsZero() {
		return TaskPathClaimsPlan{}, fmt.Errorf("invalid task path claims plan")
	}
	newClaims, err := validateOwnerTaskClaims(w.Root, plan.NewClaims)
	if err != nil || !slices.Equal(newClaims, plan.NewClaims) {
		return TaskPathClaimsPlan{}, fmt.Errorf("plan contains invalid or noncanonical claims")
	}
	oldClaims := plan.OldClaims
	if len(oldClaims) > 0 {
		oldClaims, err = validateOwnerTaskClaims(w.Root, oldClaims)
		if err != nil || !slices.Equal(oldClaims, plan.OldClaims) {
			return TaskPathClaimsPlan{}, fmt.Errorf("plan contains invalid prior claims")
		}
	}
	if slices.Equal(oldClaims, newClaims) {
		return TaskPathClaimsPlan{}, fmt.Errorf("task path claims are unchanged")
	}
	err = WithTask(w, task, func(fresh *Task) error {
		if fresh.ID != plan.TaskID || fresh.Project != plan.Project || string(fresh.Status) != plan.Status {
			return fmt.Errorf("task changed since path claims preview")
		}
		digest, digestErr := taskFileDigest(fresh.Path)
		if digestErr != nil || digest != plan.TaskDigest {
			return fmt.Errorf("task changed since path claims preview")
		}
		current := fresh.Claims()
		if len(current) > 0 {
			current, digestErr = validateOwnerTaskClaims(w.Root, current)
			if digestErr != nil {
				return fmt.Errorf("current task claims are invalid: %w", digestErr)
			}
		}
		if !slices.Equal(current, oldClaims) {
			return fmt.Errorf("task claims changed since path claims preview")
		}
		protected, protectionErr := liveClaimants(w, fresh)
		if protectionErr != nil {
			return protectionErr
		}
		if len(protected) > 0 {
			return fmt.Errorf("task has a live local claimant: %s", protected[0])
		}
		fresh.Doc.Front.SetList("claims", newClaims)
		return SaveTask(fresh)
	})
	if err != nil {
		return TaskPathClaimsPlan{}, err
	}
	plan.NewClaims = append([]string(nil), newClaims...)
	plan.AppliedAt = now.UTC()
	return plan, nil
}
