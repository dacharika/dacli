package store

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func sandboxFixture(t *testing.T) (*workspace.Workspace, CleanupSandbox, func()) {
	t.Helper()
	w, task, canonical, done := cleanupFixture(t)
	id := "01M14CLEANUP0000000000001"
	path := sandboxPath(w, id)
	base, e := gitx.Run(w.Root, "rev-parse", TaskBranch(task)+"^")
	if e != nil {
		t.Fatal(e)
	}
	commit, e := gitx.Run(w.Root, "rev-parse", TaskBranch(task))
	if e != nil {
		t.Fatal(e)
	}
	tree, e := gitx.Run(w.Root, "rev-parse", commit+"^{tree}")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	worktreeGit(t, w.Root, "clone", "-q", "--no-hardlinks", "--no-checkout", w.Root, path)
	worktreeGit(t, path, "remote", "remove", "origin")
	worktreeGit(t, path, "checkout", "-q", "--detach", base)
	refs, e := gitx.Run(path, "for-each-ref", "--format=%(refname)")
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range strings.Fields(refs) {
		worktreeGit(t, path, "update-ref", "-d", r)
	}
	worktreeGit(t, path, "checkout", "-qb", TaskBranch(task))
	if e := os.WriteFile(filepath.Join(path, "product"), []byte("landed\n"), 0644); e != nil {
		t.Fatal(e)
	}
	changed, e := digestFile(path, "product")
	if e != nil {
		t.Fatal(e)
	}
	m := cleanupSandboxManifest{Schema: "claim-sandbox/v1", RunID: id, CanonicalDir: canonical, SandboxDir: path, BaseCommit: base, Branch: TaskBranch(task), Claims: []string{"product"}, ProjectedAt: time.Unix(1, 0), Paths: []string{"product"}}
	h := RootHandoff{Schema: RootHandoffSchema, Version: 1, RunID: id, TaskID: task.ID, ChildID: "a-worker", Worktree: canonical, ChangedPaths: []RootHandoffPath{changed}, TreeSHA256: sandboxHandoffDigest([]RootHandoffPath{changed}), DiffSHA256: "diff"}
	req := ParentCommitRequest{Schema: ParentCommitRequestSchema, RunID: id, TaskID: task.ID, ChildID: "a-worker", Worktree: canonical, Branch: m.Branch, ParentCommit: base, TreeOID: tree, AllowedPaths: m.Claims, ChangedPaths: h.ChangedPaths, TreeSHA256: h.TreeSHA256, DiffSHA256: h.DiffSHA256, AuthorName: "worker", AuthorEmail: "worker@example.test", Message: "projection", Verification: []RootHandoffVerification{{Command: "test", ExitCode: 0}}, RequestedAt: time.Unix(1, 0)}
	req.RequestID, e = parentCommitID(req)
	if e != nil {
		t.Fatal(e)
	}
	receipt := ParentCommitReceipt{Schema: ParentCommitReceiptSchema, RequestID: req.RequestID, RunID: id, TaskID: task.ID, Branch: m.Branch, Commit: commit, TreeOID: tree}
	consumed := parentCommitConsumption{Schema: "root-handoff-consumption/v1", RunID: id, TreeSHA256: h.TreeSHA256, RequestID: req.RequestID, Commit: commit}
	for _, v := range []struct {
		name  string
		value any
	}{{"claim-sandbox-v1.json", m}, {RootHandoffFile, h}, {ParentCommitRequestFile, req}, {ParentCommitReceiptFile, receipt}, {RootHandoffConsumedFile, consumed}} {
		writeSandboxJSON(t, filepath.Join(w.RunDir(id), v.name), v.value)
	}
	if e := os.WriteFile(filepath.Join(w.RunDir(id), "runtime-exit.txt"), []byte("0\n"), 0644); e != nil {
		t.Fatal(e)
	}
	p, e := PlanRepositoryCleanup(w, "core", time.Unix(1, 0))
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Sandboxes) != 1 || !p.Sandboxes[0].Eligible {
		t.Fatalf("dirty projected fixture must qualify: %+v", p.Sandboxes)
	}
	return w, p.Sandboxes[0], done
}
func writeSandboxJSON(t *testing.T, p string, v any) {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestCleanupSandboxDirtyProjectionRoundtripReclaimsBytes(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	_ = os.Remove(filepath.Join(w.RunDir(c.RunID), "capture.tmp"))
	beforeTotal := sandboxRetainedBytes(t, w.Root)
	p, e := PlanRepositoryCleanup(w, "core", time.Unix(2, 0), mustSandboxCanonical(t, w, c.RunID))
	if e != nil {
		t.Fatal(e)
	}
	evidence, _, e := sandboxSnapshot(w.RunDir(c.RunID), true)
	if e != nil {
		t.Fatal(e)
	}
	audit, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Unix(3, 0), mustSandboxCanonical(t, w, c.RunID))
	if e != nil {
		t.Fatal(e)
	}
	if len(audit.Sandboxes) != 1 || audit.Sandboxes[0].State != "removed" {
		t.Fatalf("missing recovery audit: %+v", audit)
	}
	afterTotal := sandboxRetainedBytes(t, w.Root)
	if afterTotal >= beforeTotal {
		t.Fatalf("total retained bytes did not fall: %d -> %d", beforeTotal, afterTotal)
	}
	t.Logf("actual total retained bytes including canonical objects, pins, audits and archives: %d -> %d", beforeTotal, afterTotal)
	r := audit.Sandboxes[0]
	if r.Bytes >= c.Bytes {
		t.Fatalf("archive does not reclaim bytes: before %d after %d", c.Bytes, r.Bytes)
	}
	t.Logf("actual source files %d bytes; retained archive %d bytes; reclaimed %d bytes (pins reference preexisting canonical objects)", c.Bytes, r.Bytes, c.Bytes-r.Bytes)
	if _, e := os.Stat(c.Path); !os.IsNotExist(e) {
		t.Fatalf("source still exists: %v", e)
	}
	after, _, e := sandboxSnapshot(w.RunDir(c.RunID), true)
	if e != nil || sandboxHash(after) != sandboxHash(evidence) {
		t.Fatal("durable run evidence changed")
	}
	restored, e := RestoreRepositoryCleanupArtifact(w, "core", p.ID, c.Identity, time.Unix(4, 0))
	if e != nil {
		t.Fatal(e)
	}
	if restored.Sandboxes[0].State != "restored" {
		t.Fatal("restore not recorded")
	}
	files, _, e := sandboxSnapshot(c.Path, false)
	if e != nil || sandboxHash(files) != sandboxHash(c.Files) {
		t.Fatal("complete working manifest did not roundtrip")
	}
	if _, e := RestoreRepositoryCleanupArtifact(w, "core", p.ID, c.Identity, time.Now()); e == nil {
		t.Fatal("restore overwrote existing destination")
	}
}
func TestCleanupSandboxContentGuard(t *testing.T) {
	for _, kind := range []string{"modified", "untracked", "ignored", "mode", "missing", "symlink", "novel-ref", "config", "receipt", "description", "sample-hook", "exclude"} {
		t.Run(kind, func(t *testing.T) {
			w, c, done := sandboxFixture(t)
			defer done()
			switch kind {
			case "modified":
				_ = os.WriteFile(filepath.Join(c.Path, "product"), []byte("different"), 0644)
			case "untracked":
				_ = os.WriteFile(filepath.Join(c.Path, "extra"), []byte("private"), 0600)
			case "ignored":
				_ = os.WriteFile(filepath.Join(c.Path, ".git", "info", "exclude"), []byte("private\n"), 0644)
				_ = os.WriteFile(filepath.Join(c.Path, "private"), []byte("private"), 0600)
			case "mode":
				_ = os.Chmod(filepath.Join(c.Path, "product"), 0600)
			case "missing":
				_ = os.Remove(filepath.Join(c.Path, "product"))
			case "symlink":
				_ = os.Symlink("product", filepath.Join(c.Path, "alias"))
			case "novel-ref":
				worktreeGit(t, c.Path, "update-ref", "refs/heads/novel", c.Base)
			case "description":
				_ = os.WriteFile(filepath.Join(c.Path, ".git", "description"), []byte("private"), 0600)
			case "sample-hook":
				_ = os.WriteFile(filepath.Join(c.Path, ".git", "hooks", "commit-msg.sample"), []byte("private"), 0600)
			case "exclude":
				_ = os.WriteFile(filepath.Join(c.Path, ".git", "info", "exclude"), []byte("private"), 0600)
			case "config":
				worktreeGit(t, c.Path, "config", "filter.evil.clean", "false")
			case "receipt":
				var r ParentCommitReceipt
				if e := sandboxRead(ParentCommitReceiptPath(w, c.RunID), &r); e != nil {
					t.Fatal(e)
				}
				r.TaskID = "different"
				writeSandboxJSON(t, ParentCommitReceiptPath(w, c.RunID), r)
			}
			p, e := PlanRepositoryCleanup(w, "core", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			if len(p.Sandboxes) != 1 || p.Sandboxes[0].Eligible {
				t.Fatalf("content guard accepted %s", kind)
			}
		})
	}
}
func TestCleanupSandboxOwnershipGuard(t *testing.T) {
	for _, kind := range []string{"claims", "live", "unknown", "mismatched", "failed-exit", "unprojected", "task"} {
		t.Run(kind, func(t *testing.T) {
			w, c, done := sandboxFixture(t)
			defer done()
			path := filepath.Join(w.RunDir(c.RunID), "proc.txt")
			r, e := procmon.ReadRecord(path)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "claims":
				r.Claims = []string{"product"}
			case "live":
				r.PID = os.Getpid()
			case "unknown":
				r.Outcome = ""
			case "mismatched":
				r.RunID = "other"
			case "failed-exit":
				_ = os.Remove(filepath.Join(w.RunDir(c.RunID), "runtime-exit.txt"))
			case "unprojected":
				var m cleanupSandboxManifest
				_ = sandboxRead(filepath.Join(w.RunDir(c.RunID), "claim-sandbox-v1.json"), &m)
				m.ProjectedAt = time.Time{}
				writeSandboxJSON(t, filepath.Join(w.RunDir(c.RunID), "claim-sandbox-v1.json"), m)
			case "task":
				r.Task = "missing"
			}
			if e := procmon.WriteRecord(path, r); e != nil {
				t.Fatal(e)
			}
			p, e := PlanRepositoryCleanup(w, "core", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			if p.Sandboxes[0].Eligible {
				t.Fatalf("ownership guard accepted %s", kind)
			}
		})
	}
}
func TestCleanupSandboxStalePlanRefuses(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	p, e := PlanRepositoryCleanup(w, "core", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(filepath.Join(c.Path, "product"), []byte("changed"), 0644)
	if _, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Now()); e == nil {
		t.Fatal("stale source plan applied")
	}
	if _, e := os.Stat(c.Path); e != nil {
		t.Fatal("source removed")
	}
}
func TestCleanupSandboxRecoveryFailuresRetainSourceOrArchive(t *testing.T) {
	for _, kind := range []string{"archive", "changed-after-archive", "interrupted", "before-remove"} {
		t.Run(kind, func(t *testing.T) {
			w, c, done := sandboxFixture(t)
			defer done()
			oldWrite, oldRemove, oldHook := writeSandboxRecovery, removeSandboxSource, sandboxBeforeRemove
			defer func() { writeSandboxRecovery, removeSandboxSource, sandboxBeforeRemove = oldWrite, oldRemove, oldHook }()
			switch kind {
			case "archive":
				writeSandboxRecovery = func(string, []byte, os.FileMode) error { return os.ErrPermission }
			case "changed-after-archive":
				sandboxBeforeRemove = func() { _ = os.WriteFile(filepath.Join(c.Path, "product"), []byte("changed"), 0644) }
			case "before-remove":
				removeSandboxSource = func(string) error { return os.ErrPermission }
			case "interrupted":
				removeSandboxSource = func(path string) error { _ = os.Remove(filepath.Join(path, "product")); return os.ErrPermission }
			}
			p, e := PlanRepositoryCleanup(w, "core", time.Now())
			if e != nil {
				t.Fatal(e)
			}
			a, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Now())
			if e == nil {
				t.Fatal("injected failure accepted")
			}
			if kind != "interrupted" {
				if _, e := os.Stat(filepath.Join(c.Path, "product")); e != nil {
					t.Fatal("source lost")
				}
			}
			if kind == "archive" {
				if len(a.Sandboxes) != 0 {
					t.Fatal("false verified archive")
				}
				return
			}
			if len(a.Sandboxes) != 1 {
				t.Fatal("verified recovery missing")
			}
			if kind == "interrupted" && a.Sandboxes[0].State != "removal-started" {
				t.Fatal("untruthful interrupted audit")
			}
			if kind == "before-remove" {
				if _, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Now()); e == nil {
					t.Fatal("repeated apply overwrote recovery audit")
				}
				var persisted CleanupAudit
				if e := sandboxRead(filepath.Join(w.Root, workspace.Dir, "audit", "cleanup", p.ID+".json"), &persisted); e != nil {
					t.Fatal(e)
				}
				if len(persisted.Sandboxes) != 1 || persisted.Sandboxes[0].State != "removal-started" {
					t.Fatal("repeated apply lost recovery ledger")
				}
			}
			_ = os.RemoveAll(c.Path)
			if _, e := RestoreRepositoryCleanupArtifact(w, "core", p.ID, c.Identity, time.Now()); e != nil {
				t.Fatalf("verified archive cannot restore after failure: %v", e)
			}
		})
	}
}
func TestCleanupSandboxRestoreRejectsTraversalAndFailure(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	a := cleanupSandboxArchive{Schema: "claim-sandbox-recovery/v1", Candidate: c, Deltas: map[string][]byte{}}
	a.Candidate.Files = []CleanupSandboxFile{{Path: "../escape", Mode: 0644}}
	if e := reconstructSandbox(w, a, t.TempDir()); e == nil {
		t.Fatal("traversal accepted")
	}
	p, e := PlanRepositoryCleanup(w, "core", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	audit, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(audit.Sandboxes[0].Archive, []byte("corrupt"), 0600)
	if _, e := RestoreRepositoryCleanupArtifact(w, "core", p.ID, c.Identity, time.Now()); e == nil {
		t.Fatal("corrupt recovery archive restored")
	}
	if _, e := os.Stat(c.Path); !os.IsNotExist(e) {
		t.Fatal("failed restore created destination")
	}
}

func mustSandboxCanonical(t *testing.T, w *workspace.Workspace, id string) string {
	t.Helper()
	var m cleanupSandboxManifest
	if e := sandboxRead(filepath.Join(w.RunDir(id), "claim-sandbox-v1.json"), &m); e != nil {
		t.Fatal(e)
	}
	return m.CanonicalDir
}
func sandboxRetainedBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	e := filepath.Walk(root, func(_ string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode().IsRegular() {
			total += i.Size()
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return total
}

func TestCleanupSandboxReplacedSourceIdentityRefuses(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	p, e := PlanRepositoryCleanup(w, "core", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	entries, _, e := sandboxSnapshot(c.Path, true)
	if e != nil {
		t.Fatal(e)
	}
	original := filepath.Join(t.TempDir(), "original")
	if e := os.Rename(c.Path, original); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(c.Path, os.FileMode(c.RootMode)); e != nil {
		t.Fatal(e)
	}
	for _, f := range entries {
		dest := filepath.Join(c.Path, filepath.FromSlash(f.Path))
		if f.Directory {
			if e := os.Mkdir(dest, os.FileMode(f.Mode)); e != nil {
				t.Fatal(e)
			}
			continue
		}
		data, e := os.ReadFile(filepath.Join(original, filepath.FromSlash(f.Path)))
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(dest, data, os.FileMode(f.Mode)); e != nil {
			t.Fatal(e)
		}
	}
	fresh, e := PlanRepositoryCleanup(w, "core", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	replacement := fresh.Sandboxes[0]
	if !replacement.Eligible || replacement.Source != c.Source || replacement.Node == c.Node {
		t.Fatalf("fixture must replace only source inode identity: %+v", replacement)
	}
	if _, e := ApplyRepositoryCleanup(w, "core", p.ID, time.Now()); e == nil {
		t.Fatal("replaced source accepted")
	}
	if _, e := os.Stat(c.Path); e != nil {
		t.Fatal("replacement source removed")
	}
}

func TestReviewPersistedArchiveMustBeVerified(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	old := writeSandboxRecovery
	defer func() { writeSandboxRecovery = old }()
	writeSandboxRecovery = func(path string, raw []byte, mode os.FileMode) error {
		return writeSandboxDurable(path, []byte("corrupt saved archive"), mode)
	}
	p, e := PlanRepositoryCleanup(w, "core", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	_, e = ApplyRepositoryCleanup(w, "core", p.ID, time.Now())
	if e == nil {
		t.Fatal("apply removed source after verifying only memory, accepting corrupt persisted archive")
	}
	if _, e = os.Stat(c.Path); e != nil {
		t.Fatal("source lost", e)
	}
}
func TestReviewProtectNestedCurrentSandbox(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	nested := filepath.Join(c.Path, "nested")
	if e := os.Mkdir(nested, 0755); e != nil {
		t.Fatal(e)
	}
	p, e := PlanRepositoryCleanup(w, "core", time.Now(), nested)
	if e != nil {
		t.Fatal(e)
	}
	if p.Sandboxes[0].Eligible {
		t.Fatal("current directory inside sandbox does not protect sandbox")
	}
}

func TestCleanupSandboxProtectSiblingPrefix(t *testing.T) {
	w, c, done := sandboxFixture(t)
	defer done()
	p, e := PlanRepositoryCleanup(w, "core", time.Now(), c.Path+"-sibling")
	if e != nil {
		t.Fatal(e)
	}
	if !p.Sandboxes[0].Eligible {
		t.Fatalf("sibling prefix protected sandbox: %s", p.Sandboxes[0].Reason)
	}
}

func TestCleanupSandboxPersistedArchiveGuards(t *testing.T) {
	for _, kind := range []string{"checksum", "truncated", "trailing-json", "oversized", "candidate"} {
		t.Run(kind, func(t *testing.T) {
			w, c, done := sandboxFixture(t)
			defer done()
			oldWrite, oldLimit := writeSandboxRecovery, sandboxRecoveryDecodedLimit
			defer func() { writeSandboxRecovery, sandboxRecoveryDecodedLimit = oldWrite, oldLimit }()
			if kind == "oversized" {
				sandboxRecoveryDecodedLimit = 64
			}
			writeSandboxRecovery = func(path string, raw []byte, mode os.FileMode) error {
				switch kind {
				case "checksum":
					raw[len(raw)-8] ^= 1
				case "truncated":
					raw = raw[:len(raw)-4]
				case "trailing-json", "candidate":
					gz, err := gzip.NewReader(bytes.NewReader(raw))
					if err != nil {
						return err
					}
					decoded, err := io.ReadAll(gz)
					if err != nil {
						return err
					}
					if err := gz.Close(); err != nil {
						return err
					}
					if kind == "candidate" {
						var a cleanupSandboxArchive
						if err := json.Unmarshal(decoded, &a); err != nil {
							return err
						}
						a.Candidate.RunID = "other"
						decoded, err = json.Marshal(a)
						if err != nil {
							return err
						}
					} else {
						decoded = append(decoded, []byte("{}")...)
					}
					var buf bytes.Buffer
					writer := gzip.NewWriter(&buf)
					if _, err := writer.Write(decoded); err != nil {
						return err
					}
					if err := writer.Close(); err != nil {
						return err
					}
					raw = buf.Bytes()
				}
				return writeSandboxDurable(path, raw, mode)
			}
			p, err := PlanRepositoryCleanup(w, "core", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			audit, err := ApplyRepositoryCleanup(w, "core", p.ID, time.Now())
			if err == nil {
				t.Fatalf("%s persisted archive accepted", kind)
			}
			if len(audit.Sandboxes) != 0 {
				t.Fatal("invalid recovery marked verified")
			}
			if _, err := os.Stat(filepath.Join(c.Path, "product")); err != nil {
				t.Fatalf("source lost: %v", err)
			}
		})
	}
}
