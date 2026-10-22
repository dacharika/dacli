package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// NativeAcceptanceTask reads a unique bounded canonical record; it never prefers a done alias.
func NativeAcceptanceTask(w *workspace.Workspace, id, project string) (*Task, error) {
	c, e := ReadCheckpointCanonical(w)
	if e != nil {
		return nil, e
	}
	for _, t := range c.Tasks {
		if t.ID == id {
			if t.Project != project {
				return nil, Refusedf("native task project mismatch")
			}
			return t, nil
		}
	}
	return nil, Refusedf("native acceptance task missing")
}

// WithNativeAcceptanceTask keeps the stable task lock through native closing/finalization.
func WithNativeAcceptanceTask(w *workspace.Workspace, id, project string, fn func(*Task) error) error {
	t, e := NativeAcceptanceTask(w, id, project)
	if e != nil {
		return e
	}
	release, e := acquireFileLock(taskLockPath(w, t))
	if e != nil {
		return e
	}
	defer release()
	t, e = NativeAcceptanceTask(w, id, project)
	if e != nil {
		return e
	}
	return fn(t)
}

// NativeAcceptanceOriginalChecklist recognizes only the exact owner marker and its
// check-all effect. Criterion text/prose changes cannot become a continuation exception.
func NativeAcceptanceOriginalChecklist(t *Task, receipt, before string) (string, error) {
	marker, _ := t.Doc.Front.Get("native_acceptance.receipt")
	if marker == "" {
		return AcceptanceChecklistDigest(t)
	}
	if marker != receipt {
		return "", Refusedf("another native acceptance marker")
	}
	original, ok := t.Doc.Front.Get("native_acceptance.original_acceptance")
	if !ok {
		return "", Refusedf("native acceptance original checklist missing")
	}
	raw := mdstore.Render(t.Doc)
	doc, e := mdstore.Parse(raw)
	if e != nil {
		return "", e
	}
	copy := *t
	copy.Doc = doc
	copy.Doc.SetSection("Acceptance", original)
	digest, e := AcceptanceChecklistDigest(&copy)
	if e != nil || digest != before {
		return "", Refusedf("native acceptance original checklist changed")
	}
	CheckAllAcceptance(&copy)
	expected, ok := copy.Doc.Section("Acceptance")
	current, cok := t.Doc.Section("Acceptance")
	if !ok || !cok || current.Content != expected.Content {
		return "", Refusedf("native acceptance partial checklist changed")
	}
	return digest, nil
}

// ValidateNativeAcceptanceClose performs all consequential delivery/landing reads
// before the kernel's final freshness check. Only observed local landing is supported.
func ValidateNativeAcceptanceClose(w *workspace.Workspace, t *Task, repository, commit, tree, base string) error {
	if !HasAcceptanceCriteria(t) {
		return Refusedf("native acceptance requires criteria")
	}
	c, e := ReadCheckpointCanonical(w)
	if e != nil {
		return e
	}
	p, ok := c.Projects[t.Project]
	if !ok {
		return Refusedf("native project missing")
	}
	landing, _, e := model.ResolveLanding(p.Landing, model.LandingOverride{})
	if e != nil {
		return e
	}
	for _, child := range c.Tasks {
		if child.IsDeliverySlice() && child.ParentID() == t.ID {
			return Refusedf("native delivery-slice acceptance requires unavailable producer")
		}
	}
	if t.IsDeliverySlice() {
		return Refusedf("native delivery-slice acceptance unsupported")
	}
	if t.IsAggregate() {
		ids := t.AggregateChildren()
		if len(ids) == 0 {
			return Refusedf("aggregate has no required children")
		}
		for _, id := range ids {
			var child *Task
			for _, candidate := range c.Tasks {
				if candidate.ID == id && candidate.Project == t.Project {
					child = candidate
				}
			}
			if child == nil || child.Status != model.StatusDone || !HasAcceptanceCriteria(child) {
				return Refusedf("required aggregate child incomplete")
			}
			for _, box := range child.Acceptance() {
				if !box.Done {
					return Refusedf("required aggregate child unverified")
				}
			}
		}
	}
	projection, e := CheckpointLocalProjection(w, t.Project, c.Tasks, []string{t.ID})
	if e != nil {
		return e
	}
	for _, finding := range projection.Findings {
		if finding.Severity != "info" {
			return Refusedf("native canonical reconciliation required: %s", finding.Classification)
		}
	}
	if landing.Mode != model.LandingLocal {
		return Refusedf("native acceptance requires supported local landing")
	}
	target := landing.Base
	if target == "" {
		target = "main"
	}
	if base != "" && base != target {
		return Refusedf("native acceptance landing base differs from policy")
	}
	if nativeAcceptanceLanded(w.Root, repository, commit, tree, target) != nil {
		return Refusedf("exact native reviewed commit is not landed")
	}
	return nil
}

// PrepareNativeAcceptanceTask constructs the exact full canonical outcome before effects.
func PrepareNativeAcceptanceTask(t *Task, receipt, reviewer, summary string, at time.Time) (string, string, error) {
	doc, e := mdstore.Parse(mdstore.Render(t.Doc))
	if e != nil {
		return "", "", e
	}
	copy := *t
	copy.Doc = doc
	section, ok := copy.Doc.Section("Acceptance")
	if !ok {
		return "", "", Refusedf("native acceptance criteria missing")
	}
	copy.Doc.Front.Set("native_acceptance.original_acceptance", section.Content)
	copy.Doc.Front.Set("native_acceptance.receipt", receipt)
	copy.Doc.Front.Set("native_acceptance.reviewer", reviewer)
	CheckAllAcceptance(&copy)
	log, _ := copy.Doc.Section("Log")
	copy.Doc.SetSection("Log", log.Content+fmt.Sprintf("- %s native acceptance %s: %s\n- %s completed by native-owner\n", at.UTC().Format(time.RFC3339), receipt, summary, at.UTC().Format(time.RFC3339)))
	copy.Doc.Front.Delete("completion_state")
	copy.Doc.Front.Delete("completion_requested_by")
	copy.Doc.Front.Delete("completion_requested_at")
	body := mdstore.Render(copy.Doc)
	digest := nativeAcceptanceRawDigest(body)
	return body, digest, nil
}

// CloseTaskWithNativeAcceptance saves only the kernel-bound prepared outcome then moves.
// An exact own partial marker can move again; it never checks a changed task anew.
func CloseTaskWithNativeAcceptance(w *workspace.Workspace, t *Task, receipt, body, digest string) error {
	return closeTaskWithNativeAcceptance(w, t, receipt, body, digest, nil)
}

// CloseTaskWithNativeAcceptanceAfterSave exposes the durable-save/move boundary
// to the application crash-recovery fixture. Normal producers should use
// CloseTaskWithNativeAcceptance; this observer cannot alter the saved outcome.
func CloseTaskWithNativeAcceptanceAfterSave(w *workspace.Workspace, t *Task, receipt, body, digest string, afterSave func()) error {
	return closeTaskWithNativeAcceptance(w, t, receipt, body, digest, afterSave)
}

func closeTaskWithNativeAcceptance(w *workspace.Workspace, t *Task, receipt, body, digest string, afterSave func()) error {
	if t.Status == model.StatusDone {
		return Refusedf("native task already done")
	}
	marker, _ := t.Doc.Front.Get("native_acceptance.receipt")
	if marker == "" {
		observed := nativeAcceptanceRawDigest(body)
		if observed != digest {
			return Refusedf("native outcome differs")
		}
		doc, e := mdstore.Parse(body)
		if e != nil {
			return e
		}
		t.Doc = doc
		if e = SaveTask(t); e != nil {
			return e
		}
		if afterSave != nil {
			afterSave()
		}
	} else {
		observed, e := ReadNativeAcceptanceOutcomeDigest(w, t.ID, t.Project)
		if marker != receipt || e != nil || observed != digest {
			return Refusedf("native partial outcome differs")
		}
	}
	return MoveTask(w, t, model.StatusDone)
}

// nativeAcceptanceLanded observes only exact local refs in the canonical repository.
// Ambient Git overrides and config includes cannot select a different proof repository.
func nativeAcceptanceLanded(root, repository, commit, tree, base string) error {
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil || !filepath.IsAbs(root) {
		return Refusedf("native landing checkout unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", canonical}, args...)...)
		cmd.WaitDelay = 250 * time.Millisecond
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "GIT_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		out := nativeAcceptanceOutput{}
		cmd.Stdout = &out
		cmd.Stderr = &out
		if e := cmd.Run(); e != nil {
			return "", e
		}
		return strings.TrimSpace(out.String()), nil
	}
	top, e := run("rev-parse", "--show-toplevel")
	if e != nil {
		return Refusedf("native landing repository unavailable")
	}
	actual, e := filepath.EvalSymlinks(top)
	if e != nil || actual != canonical {
		return Refusedf("native landing checkout binding differs")
	}
	common, e := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return Refusedf("native landing repository unavailable")
	}
	common, e = filepath.EvalSymlinks(common)
	if e != nil || common != repository {
		return Refusedf("native landing repository binding differs")
	}
	if len(commit) != 40 && len(commit) != 64 || strings.Trim(commit, "0123456789abcdef") != "" {
		return Refusedf("native landing commit invalid")
	}
	genuineTree, e := run("rev-parse", "--verify", commit+"^{tree}")
	if e != nil || genuineTree != tree {
		return Refusedf("native reviewed tree differs from genuine landed commit")
	}
	target, e := run("rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if e != nil {
		return Refusedf("native local landing target unavailable")
	}
	if _, e = run("merge-base", "--is-ancestor", commit, target); e != nil {
		return Refusedf("native reviewed commit unlanded")
	}
	return nil
}

type nativeAcceptanceOutput struct{ bytes.Buffer }

func (b *nativeAcceptanceOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, fmt.Errorf("native landing output limit")
	}
	return b.Buffer.Write(p)
}

func nativeAcceptanceRawDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReadNativeAcceptanceOutcomeDigest reuses the bounded unique snapshot raw bytes.
func ReadNativeAcceptanceOutcomeDigest(w *workspace.Workspace, id, project string) (string, error) {
	c, e := ReadCheckpointCanonical(w)
	if e != nil {
		return "", e
	}
	for _, t := range c.Tasks {
		if t.ID == id {
			if t.Project != project {
				return "", Refusedf("native outcome project differs")
			}
			return "sha256:" + c.Digests[id], nil
		}
	}
	return "", Refusedf("native outcome missing")
}
