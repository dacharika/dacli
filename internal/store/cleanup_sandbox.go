package store

// Claim clones are directories, not worktrees or temporary artifacts. Only
// receipt-exact trees are supported: novel content and Git state stay in place.
import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type CleanupSandboxFile struct {
	Path      string `json:"path"`
	Mode      uint32 `json:"mode"`
	Digest    string `json:"digest"`
	Directory bool   `json:"directory,omitempty"`
}
type CleanupSandbox struct {
	Node     string               `json:"node"`
	Branch   string               `json:"branch"`
	RootMode uint32               `json:"root_mode"`
	Path     string               `json:"path"`
	RunID    string               `json:"run_id"`
	Task     string               `json:"task,omitempty"`
	Base     string               `json:"base,omitempty"`
	Commit   string               `json:"commit,omitempty"`
	Identity string               `json:"identity"`
	Evidence string               `json:"evidence"`
	Source   string               `json:"source"`
	Files    []CleanupSandboxFile `json:"files,omitempty"`
	Bytes    int64                `json:"bytes"`
	Eligible bool                 `json:"eligible"`
	Reason   string               `json:"reason"`
}
type cleanupSandboxManifest struct {
	Schema       string    `json:"schema"`
	RunID        string    `json:"run_id"`
	CanonicalDir string    `json:"canonical_dir"`
	SandboxDir   string    `json:"sandbox_dir"`
	BaseCommit   string    `json:"base_commit"`
	Branch       string    `json:"branch"`
	Claims       []string  `json:"claims"`
	ProjectedAt  time.Time `json:"projected_at"`
	Paths        []string  `json:"projected_paths"`
}
type CleanupSandboxRecovery struct {
	Candidate CleanupSandbox `json:"candidate"`
	Archive   string         `json:"archive"`
	Digest    string         `json:"digest"`
	Bytes     int64          `json:"bytes"`
	State     string         `json:"state"`
}
type cleanupSandboxArchive struct {
	Schema    string            `json:"schema"`
	Candidate CleanupSandbox    `json:"candidate"`
	Deltas    map[string][]byte `json:"deltas"`
}

func sandboxHash(v any) string {
	raw, _ := json.Marshal(v)
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}
func sandboxPath(w *workspace.Workspace, id string) string {
	return filepath.Join(w.WorktreesDir(), ".claim-sandboxes", id)
}
func sandboxEntryPath(p string) bool {
	return p != "" && p != "." && p == filepath.ToSlash(filepath.Clean(p)) && !filepath.IsAbs(p) && p != ".." && !strings.HasPrefix(p, "../") && p != ".git" && !strings.HasPrefix(p, ".git/") && !strings.Contains(p, "\\")
}
func sandboxRead(path string, out any) error {
	i, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !i.Mode().IsRegular() {
		return fmt.Errorf("nonregular evidence")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

// Hash every administrative and working entry, including ignored files. No
// symlink, device, socket or alternate object store is traversed.
func sandboxSnapshot(root string, admin bool) ([]CleanupSandboxFile, int64, error) {
	var files []CleanupSandboxFile
	var size int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("source is not a directory")
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !admin && rel == ".git" {
			return filepath.SkipDir
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			return fmt.Errorf("unsupported filesystem mode %s", rel)
		}
		f := CleanupSandboxFile{Path: rel, Mode: uint32(info.Mode().Perm()), Directory: d.IsDir()}
		if !d.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported filesystem entry %s", rel)
			}
			raw, e := os.ReadFile(path) // #nosec G122 -- cooperative integrity: reject symlinks and recheck complete source before removal; Go 1.22 has no os.Root.
			if e != nil {
				return e
			}
			s := sha256.Sum256(raw)
			f.Digest = hex.EncodeToString(s[:])
			size += info.Size()
		}
		files = append(files, f)
		return nil
	})
	return files, size, err
}
func sandboxGit(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := append([]string{"-C", root, "-c", "core.fsmonitor=false"}, args...)
	return exec.CommandContext(ctx, "git", argv...).Output()
}
func sandboxTree(root, commit string) (map[string]CleanupSandboxFile, error) {
	raw, e := sandboxGit(root, "ls-tree", "-r", "-z", commit)
	if e != nil {
		return nil, e
	}
	out := map[string]CleanupSandboxFile{}
	for _, entry := range bytes.Split(raw, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid tree")
		}
		fields := strings.Fields(string(parts[0]))
		path := string(parts[1])
		if len(fields) != 3 || fields[1] != "blob" || !sandboxEntryPath(path) || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, fmt.Errorf("unsupported receipt tree")
		}
		blob, e := sandboxGit(root, "cat-file", "blob", fields[2])
		if e != nil {
			return nil, e
		}
		s := sha256.Sum256(blob)
		mode := uint32(0644)
		if fields[0] == "100755" {
			mode = 0755
		}
		out[path] = CleanupSandboxFile{Path: path, Mode: mode, Digest: hex.EncodeToString(s[:])}
	}
	return out, nil
}
func observeCleanupSandboxes(w *workspace.Workspace, tasks map[string]*Task, landing string, protect []string) ([]CleanupSandbox, error) {
	entries, e := os.ReadDir(filepath.Join(w.WorktreesDir(), ".claim-sandboxes"))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, fmt.Errorf("observe managed claim sandboxes: %w", e)
	}
	var out []CleanupSandbox
	for _, entry := range entries {
		c := CleanupSandbox{RunID: entry.Name(), Path: sandboxPath(w, entry.Name())}
		c = observeCleanupSandbox(w, c, tasks, landing, protect)
		out = append(out, c)
	}
	return out, nil
}
func observeCleanupSandbox(w *workspace.Workspace, c CleanupSandbox, tasks map[string]*Task, landing string, protect []string) CleanupSandbox {
	refuse := func(e error) CleanupSandbox {
		c.Eligible = false
		c.Reason = e.Error()
		c.Identity = sandboxHash(c)
		return c
	}
	if !workspace.SafeSegment(c.RunID) {
		return refuse(fmt.Errorf("invalid managed run id"))
	}
	for _, p := range append(protect, w.Root) {
		protected, err := filepath.Abs(cleanPath(p))
		if err != nil {
			return refuse(fmt.Errorf("invalid protected path: %w", err))
		}
		source, err := filepath.Abs(cleanPath(c.Path))
		if err != nil {
			return refuse(err)
		}
		rel, err := filepath.Rel(source, protected)
		if err != nil {
			return refuse(err)
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return refuse(fmt.Errorf("protected current sandbox"))
		}
	}
	if e := rejectCleanupSymlinkParents(w.Root, c.Path, false); e != nil {
		return refuse(e)
	}
	rootInfo, e := os.Lstat(c.Path)
	if e != nil {
		return refuse(e)
	}
	c.RootMode = uint32(rootInfo.Mode().Perm())
	c.Node = sandboxNode(rootInfo)
	if c.Node == "unsupported" {
		return refuse(fmt.Errorf("unsupported filesystem identity"))
	}
	all, size, e := sandboxSnapshot(c.Path, true)
	if e != nil {
		return refuse(e)
	}
	c.Source = sandboxHash(all)
	c.Bytes = size
	files, _, e := sandboxSnapshot(c.Path, false)
	if e != nil {
		return refuse(e)
	}
	c.Files = files
	var m cleanupSandboxManifest
	var req ParentCommitRequest
	var receipt ParentCommitReceipt
	var h RootHandoff
	var used parentCommitConsumption
	run := w.RunDir(c.RunID)
	evidenceNames := []string{"claim-sandbox-v1.json", ParentCommitRequestFile, ParentCommitReceiptFile, RootHandoffFile, RootHandoffConsumedFile, "proc.txt", "runtime-exit.txt"}
	var evidence []string
	for _, name := range evidenceNames {
		p := filepath.Join(run, name)
		if e := rejectCleanupSymlinkParents(w.Root, p, false); e != nil {
			return refuse(e)
		}
		i, e := os.Lstat(p)
		if e != nil || !i.Mode().IsRegular() {
			return refuse(fmt.Errorf("missing or nonregular %s", name))
		}
		d, e := cleanupArtifactDigest(p)
		if e != nil {
			return refuse(e)
		}
		evidence = append(evidence, d)
	}
	c.Evidence = sandboxHash(evidence)
	for _, v := range []struct {
		name string
		out  any
	}{{"claim-sandbox-v1.json", &m}, {ParentCommitRequestFile, &req}, {ParentCommitReceiptFile, &receipt}, {RootHandoffFile, &h}, {RootHandoffConsumedFile, &used}} {
		if e := sandboxRead(filepath.Join(run, v.name), v.out); e != nil {
			return refuse(e)
		}
	}
	rec, e := procmon.ReadRecord(filepath.Join(run, "proc.txt"))
	if e != nil {
		return refuse(e)
	}
	c.Task = rec.Task
	c.Base = m.BaseCommit
	c.Branch = m.Branch
	c.Commit = receipt.Commit
	exit, e := os.ReadFile(filepath.Join(run, "runtime-exit.txt"))
	if e != nil || strings.TrimSpace(string(exit)) != "0" || rec.RunID != c.RunID || rec.Outcome == "" || len(rec.Claims) != 0 || procmon.AliveIdentity(rec.PID, rec.PIDStart) {
		return refuse(fmt.Errorf("run is not terminal with released claims and successful runtime exit"))
	}
	task := tasks[rec.Task]
	if task == nil || task.Status != model.StatusDone {
		return refuse(fmt.Errorf("task missing or not done"))
	}
	// Observe all possible matching writers, even a different run/claim record.
	runs, e := os.ReadDir(w.RunsDir())
	if e != nil {
		return refuse(e)
	}
	var ownership []string
	for _, r := range runs {
		if !r.IsDir() || r.Name() == "pr-publications" {
			continue
		}
		path := filepath.Join(w.RunDir(r.Name()), "proc.txt")
		other, err := procmon.ReadRecord(path)
		if err != nil {
			return refuse(fmt.Errorf("unknown writer evidence for run %s", r.Name()))
		}
		d, err := cleanupArtifactDigest(path)
		if err != nil {
			return refuse(err)
		}
		ownership = append(ownership, r.Name()+d)
		var otherManifest cleanupSandboxManifest
		manifestPath := filepath.Join(w.RunDir(r.Name()), "claim-sandbox-v1.json")
		matchPath := false
		if _, err := os.Lstat(manifestPath); err == nil {
			if err := sandboxRead(manifestPath, &otherManifest); err != nil {
				return refuse(fmt.Errorf("unknown writer manifest"))
			}
			d, err := cleanupArtifactDigest(manifestPath)
			if err != nil {
				return refuse(err)
			}
			ownership = append(ownership, r.Name()+d)
			matchPath = otherManifest.SandboxDir == c.Path || otherManifest.CanonicalDir == m.CanonicalDir
		} else if !os.IsNotExist(err) {
			return refuse(err)
		}
		if other.Task == rec.Task || other.Child == rec.Child || matchPath {
			if other.Outcome == "" || len(other.Claims) > 0 || procmon.AliveIdentity(other.PID, other.PIDStart) {
				return refuse(fmt.Errorf("matching current writer exists"))
			}
		}
	}
	c.Evidence = sandboxHash([]string{c.Evidence, sandboxHash(ownership), sandboxHash(task)})
	if m.Schema != "claim-sandbox/v1" || m.RunID != c.RunID || m.SandboxDir != c.Path || m.ProjectedAt.IsZero() || len(m.Paths) == 0 || len(m.Claims) == 0 || m.CanonicalDir != req.Worktree || m.CanonicalDir != w.WorktreePath(task.Project, task.Seq, task.Slug) || m.Branch != TaskBranch(task) || m.Branch != req.Branch {
		return refuse(fmt.Errorf("manifest identity or projection mismatch"))
	}
	if e := validateParentCommitRequest(req); e != nil {
		return refuse(e)
	}
	if req.RunID != c.RunID || req.TaskID != rec.Task || req.ChildID != rec.Child || req.ParentCommit != m.BaseCommit || !reflect.DeepEqual(req.AllowedPaths, m.Claims) || receipt.Schema != ParentCommitReceiptSchema || receipt.RunID != c.RunID || receipt.TaskID != rec.Task || receipt.RequestID != req.RequestID || receipt.TreeOID != req.TreeOID || receipt.Branch != m.Branch || h.Schema != RootHandoffSchema || h.Version != 1 || h.RunID != c.RunID || h.TaskID != rec.Task || h.ChildID != rec.Child || h.Worktree != m.CanonicalDir || h.TreeSHA256 != req.TreeSHA256 || h.DiffSHA256 != req.DiffSHA256 || !reflect.DeepEqual(h.ChangedPaths, req.ChangedPaths) || used.Schema != "root-handoff-consumption/v1" || used.RunID != c.RunID || used.RequestID != req.RequestID || used.Commit != receipt.Commit || used.TreeSHA256 != h.TreeSHA256 {
		return refuse(fmt.Errorf("parent/handoff/consumed receipt identity mismatch"))
	}
	if req.TreeSHA256 != sandboxHandoffDigest(req.ChangedPaths) {
		return refuse(fmt.Errorf("handoff tree digest mismatch"))
	}
	paths := make([]string, 0, len(req.ChangedPaths))
	for _, p := range req.ChangedPaths {
		paths = append(paths, p.Path)
		if !inParentCommitClaim(p.Path, m.Claims) {
			return refuse(fmt.Errorf("receipt outside claims"))
		}
	}
	sort.Strings(paths)
	projected := append([]string(nil), m.Paths...)
	sort.Strings(projected)
	diffPaths, e := sandboxGit(w.Root, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", c.Base, c.Commit)
	if e != nil {
		return refuse(e)
	}
	var committedPaths []string
	for _, path := range bytes.Split(diffPaths, []byte{0}) {
		if len(path) > 0 {
			committedPaths = append(committedPaths, string(path))
		}
	}
	sort.Strings(committedPaths)
	if !reflect.DeepEqual(committedPaths, projected) {
		return refuse(fmt.Errorf("receipt contains unprojected changes"))
	}
	if !reflect.DeepEqual(paths, projected) {
		return refuse(fmt.Errorf("projected paths mismatch"))
	}
	tree, e := gitx.Run(w.Root, "rev-parse", receipt.Commit+"^{tree}")
	if e != nil || tree != req.TreeOID {
		return refuse(fmt.Errorf("receipt commit tree mismatch"))
	}
	parent, e := gitx.Run(w.Root, "rev-parse", receipt.Commit+"^")
	if e != nil || parent != m.BaseCommit {
		return refuse(fmt.Errorf("receipt parent mismatch"))
	}
	contained, e := gitx.IsAncestor(w.Root, receipt.Commit, landing)
	if e != nil || !contained {
		return refuse(fmt.Errorf("receipt commit not contained in landing base"))
	}
	if e := sandboxSupportedGit(w, c); e != nil {
		return refuse(e)
	}
	expected, e := sandboxTree(w.Root, c.Commit)
	if e != nil {
		return refuse(e)
	}
	for _, f := range files {
		if f.Directory {
			continue
		}
		want, ok := expected[f.Path]
		if !ok || want != f {
			return refuse(fmt.Errorf("working content/mode differs from receipt or has extra ignored/untracked file: %s", f.Path))
		}
		delete(expected, f.Path)
	}
	if len(expected) != 0 {
		return refuse(fmt.Errorf("receipt file missing from working tree"))
	}
	for _, p := range req.ChangedPaths {
		f, e := digestFile(c.Path, p.Path)
		if e != nil || f != p {
			return refuse(fmt.Errorf("handoff content mismatch: %s", p.Path))
		}
	}
	c.Eligible = true
	c.Reason = "terminal released run, done task, consumed landed receipt and exact working bytes/modes; recovery archive required"
	c.Identity = sandboxHash(c)
	return c
}
func sandboxSupportedGit(w *workspace.Workspace, c CleanupSandbox) error {
	gitdir := filepath.Join(c.Path, ".git")
	i, e := os.Lstat(gitdir)
	if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsupported Git directory")
	}
	all, _, e := sandboxSnapshot(gitdir, true)
	if e != nil {
		return e
	}
	for _, f := range all {
		p := f.Path
		if f.Directory {
			continue
		}
		allowed := p == "HEAD" || p == "config" || p == "index" || p == "packed-refs" || p == "description" || p == "info/exclude" || strings.HasPrefix(p, "objects/") || strings.HasPrefix(p, "refs/") || strings.HasPrefix(p, "logs/") || (strings.HasPrefix(p, "hooks/") && strings.HasSuffix(p, ".sample"))
		if !allowed || p == "objects/info/alternates" {
			return fmt.Errorf("unsupported Git state: %s", p)
		}
	}
	for _, name := range []string{"description", "info/exclude"} {
		raw, e := os.ReadFile(filepath.Join(gitdir, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		expected := "Unnamed repository; edit this file 'description' to name the repository.\n"
		if name == "info/exclude" {
			expected = "# git ls-files --others --exclude-from=.git/info/exclude\n# Lines that start with '#' are comments.\n# For a project mostly in C, the following would be a good set of\n# exclude patterns (uncomment them if you want to use them):\n# *.[oa]\n# *~\n"
		}
		if string(raw) != expected {
			return fmt.Errorf("nonstandard Git %s", name)
		}
	}
	for _, f := range all {
		if strings.HasPrefix(f.Path, "hooks/") && !f.Directory {
			canonicalPath, e := gitx.Run(w.Root, "rev-parse", "--git-path", f.Path)
			if e != nil {
				return e
			}
			if !filepath.IsAbs(canonicalPath) {
				canonicalPath = filepath.Join(w.Root, canonicalPath)
			}
			digest, e := cleanupArtifactDigest(canonicalPath)
			if e != nil || digest != "sha256:"+f.Digest {
				return fmt.Errorf("nonstandard Git sample hook")
			}
		}
	}
	cfg, e := sandboxGit(c.Path, "config", "--local", "--null", "--list")
	if e != nil {
		return e
	}
	for _, entry := range bytes.Split(cfg, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		parts := strings.SplitN(string(entry), "\n", 2)
		key := parts[0]
		if len(parts) != 2 || (key == "core.bare" && parts[1] != "false") || (key == "core.repositoryformatversion" && parts[1] != "0") {
			return fmt.Errorf("unsupported Git config value")
		}
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode":
		default:
			return fmt.Errorf("unsupported Git config: %s", key)
		}
	}
	staged, e := gitx.Run(c.Path, "diff", "--cached", "--name-only", c.Base, "--")
	if e != nil || staged != "" {
		return fmt.Errorf("unsupported staged Git state")
	}
	head, e := gitx.Run(c.Path, "rev-parse", "HEAD")
	if e != nil || head != c.Base {
		return fmt.Errorf("sandbox HEAD diverged from initial base")
	}
	attached, e := gitx.Run(c.Path, "symbolic-ref", "HEAD")
	if e != nil || attached != "refs/heads/"+c.Branch {
		return fmt.Errorf("unsupported detached or changed sandbox branch")
	}
	refs, e := gitx.Run(c.Path, "for-each-ref", "--format=%(refname) %(objectname)")
	if e != nil {
		return e
	}
	for _, line := range strings.Split(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("invalid sandbox ref")
		}
		if fields[0] == "refs/heads/"+c.Branch && fields[1] == c.Base {
			continue
		}
		known, e := gitx.Run(w.Root, "rev-parse", "--verify", fields[0])
		if e != nil || known != fields[1] {
			return fmt.Errorf("novel or divergent sandbox ref")
		}
	}

	objects, e := gitx.Run(c.Path, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	if e != nil {
		return e
	}
	canonical, e := gitx.Run(w.Root, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for _, line := range strings.Split(canonical, "\n") {
		known[line] = true
	}
	for _, line := range strings.Split(objects, "\n") {
		if line != "" && !known[line] {
			return fmt.Errorf("novel sandbox Git object")
		}
	}

	return nil
}

func sandboxArchivePath(w *workspace.Workspace, planID, identity string) string {
	return filepath.Join(w.Root, workspace.Dir, "quarantine", "cleanup", planID, identity+".sandbox.json.gz")
}

var writeSandboxRecovery = writeSandboxDurable

func writeSandboxDurable(path string, raw []byte, mode fs.FileMode) error {
	if err := writeRootHandoffAtomic(path, raw, mode); err != nil {
		return err
	}
	return syncSandboxDirectory(filepath.Dir(path))
}
func syncSandboxDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	if e := f.Sync(); e != nil {
		return e
	}
	parent := filepath.Dir(path)
	if parent != path {
		return syncSandboxDirectory(parent)
	}
	return nil
}

var removeSandboxSource = os.RemoveAll
var sandboxBeforeRemove = func() {}

func buildSandboxRecovery(w *workspace.Workspace, c CleanupSandbox, path string) (CleanupSandboxRecovery, error) {
	r := CleanupSandboxRecovery{Candidate: c, Archive: path, State: "verified-source-retained"}
	for _, pin := range []struct{ name, oid string }{{"base", c.Base}, {"projection", c.Commit}} {
		if _, e := gitx.Run(w.Root, "update-ref", "refs/dacli/cleanup/"+c.Identity+"/"+pin.name, pin.oid); e != nil {
			return r, e
		}
	}
	for _, name := range []string{"base", "projection"} {
		refpath, e := gitx.Run(w.Root, "rev-parse", "--git-path", "refs/dacli/cleanup/"+c.Identity+"/"+name)
		if e != nil {
			return r, e
		}
		if !filepath.IsAbs(refpath) {
			refpath = filepath.Join(w.Root, refpath)
		}
		f, e := os.Open(refpath)
		if e != nil {
			return r, e
		}
		e = f.Sync()
		closeErr := f.Close()
		if e != nil {
			return r, e
		}
		if closeErr != nil {
			return r, closeErr
		}
		if e := syncSandboxDirectory(filepath.Dir(refpath)); e != nil {
			return r, e
		}
	}
	a := cleanupSandboxArchive{Schema: "claim-sandbox-recovery/v1", Candidate: c, Deltas: map[string][]byte{}}
	base, e := sandboxTree(w.Root, c.Base)
	if e != nil {
		return r, e
	}
	for _, f := range c.Files {
		if f.Directory {
			continue
		}
		if old, ok := base[f.Path]; !ok || old.Digest != f.Digest {
			data, e := os.ReadFile(filepath.Join(c.Path, filepath.FromSlash(f.Path)))
			if e != nil {
				return r, e
			}
			a.Deltas[f.Path] = data
		}
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if e := json.NewEncoder(gz).Encode(a); e != nil {
		return r, e
	}
	if e := gz.Close(); e != nil {
		return r, e
	}
	if e := rejectCleanupSymlinkParents(w.Root, path, true); e != nil {
		return r, e
	}
	if _, e := os.Lstat(path); !os.IsNotExist(e) {
		return r, fmt.Errorf("recovery archive destination exists or is unreadable")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return r, e
	}
	if e := writeSandboxRecovery(path, buf.Bytes(), 0600); e != nil {
		return r, e
	}
	r.Digest, e = cleanupArtifactDigest(path)
	if e != nil {
		return r, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return r, e
	}
	r.Bytes = info.Size()
	a, e = loadSandboxRecovery(r)
	if e != nil {
		return r, e
	}
	temp, e := os.MkdirTemp(filepath.Dir(path), ".verify-sandbox-")
	if e != nil {
		return r, e
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if e := reconstructSandbox(w, a, temp); e != nil {
		return r, e
	}
	observed, _, e := sandboxSnapshot(temp, false)
	if e != nil || !reflect.DeepEqual(observed, c.Files) {
		return r, fmt.Errorf("reconstruction manifest mismatch")
	}
	return r, nil
}

// Apply and restore must validate the same exact persisted artifact, including
// gzip trailers and EOF. A successful writer alone is not recovery evidence.
var sandboxRecoveryDecodedLimit int64 = 1 << 30

func loadSandboxRecovery(r CleanupSandboxRecovery) (cleanupSandboxArchive, error) {
	var a cleanupSandboxArchive
	info, e := os.Lstat(r.Archive)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return a, fmt.Errorf("invalid private recovery archive")
	}
	raw, e := os.ReadFile(r.Archive)
	if e != nil {
		return a, e
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != r.Digest {
		return a, fmt.Errorf("recovery archive digest mismatch")
	}
	gz, e := gzip.NewReader(bytes.NewReader(raw))
	if e != nil {
		return a, e
	}
	decoded, readErr := io.ReadAll(io.LimitReader(gz, sandboxRecoveryDecodedLimit+1))
	closeErr := gz.Close()
	if readErr != nil {
		return a, readErr
	}
	if closeErr != nil {
		return a, closeErr
	}
	if int64(len(decoded)) > sandboxRecoveryDecodedLimit {
		return a, fmt.Errorf("recovery archive decoded size exceeds limit")
	}
	if e := json.Unmarshal(decoded, &a); e != nil {
		return a, e
	}
	if a.Schema != "claim-sandbox-recovery/v1" || !reflect.DeepEqual(a.Candidate, r.Candidate) {
		return a, fmt.Errorf("archive candidate identity mismatch")
	}
	return a, nil
}
func reconstructSandbox(w *workspace.Workspace, a cleanupSandboxArchive, dest string) error {
	if a.Schema != "claim-sandbox-recovery/v1" {
		return fmt.Errorf("unknown recovery schema")
	}
	seen := map[string]bool{}
	fileSet := map[string]bool{}
	for _, f := range a.Candidate.Files {
		if !sandboxEntryPath(f.Path) || seen[f.Path] || f.Mode > 0777 {
			return fmt.Errorf("unsafe recovery entry path/mode")
		}
		seen[f.Path] = true
		if !f.Directory {
			fileSet[f.Path] = true
		}
	}
	for p := range a.Deltas {
		if !fileSet[p] {
			return fmt.Errorf("unexpected recovery delta path")
		}
	}
	// Validate parents before creating any entry, including file/directory conflicts.
	for _, f := range a.Candidate.Files {
		for parent := filepath.ToSlash(filepath.Dir(f.Path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if !seen[parent] || fileSet[parent] {
				return fmt.Errorf("invalid recovery parent")
			}
		}
	}
	for _, f := range a.Candidate.Files {
		target := filepath.Join(dest, filepath.FromSlash(f.Path))
		if f.Directory {
			if e := os.Mkdir(target, 0700); e != nil {
				return e
			}
			continue
		}
		data, ok := a.Deltas[f.Path]
		if !ok {
			var e error
			data, e = sandboxGit(w.Root, "cat-file", "blob", a.Candidate.Base+":"+f.Path)
			if e != nil {
				return e
			}
		}
		s := sha256.Sum256(data)
		if hex.EncodeToString(s[:]) != f.Digest {
			return fmt.Errorf("recovery content mismatch")
		}
		out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = out.Write(data)
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if e := os.Chmod(target, fs.FileMode(f.Mode)); e != nil {
			return e
		}
	}
	for i := len(a.Candidate.Files) - 1; i >= 0; i-- {
		f := a.Candidate.Files[i]
		if f.Directory {
			if e := os.Chmod(filepath.Join(dest, filepath.FromSlash(f.Path)), fs.FileMode(f.Mode)); e != nil {
				return e
			}
		}
	}
	return nil
}
func applyCleanupSandboxes(w *workspace.Workspace, p CleanupPlan, audit *CleanupAudit, protect []string) error {
	for _, c := range p.Sandboxes {
		if !c.Eligible {
			continue
		}
		path := sandboxArchivePath(w, p.ID, c.Identity)
		r, e := buildSandboxRecovery(w, c, path)
		if e != nil {
			return fmt.Errorf("sandbox recovery failed; source retained: %w", e)
		}
		audit.Sandboxes = append(audit.Sandboxes, r)
		if _, e := writeCleanupAudit(w, *audit); e != nil {
			return e
		}
		sandboxBeforeRemove()
		// Re-observe all immutable source and authority evidence after durable archive.
		fresh, e := PlanRepositoryCleanup(w, p.Project, time.Now(), protect...)
		if e != nil {
			return e
		}
		var found *CleanupSandbox
		for i := range fresh.Sandboxes {
			if fresh.Sandboxes[i].Path == c.Path {
				found = &fresh.Sandboxes[i]
			}
		}
		if found == nil || !found.Eligible || found.Identity != c.Identity {
			return fmt.Errorf("sandbox content or ownership changed; source retained with verified archive")
		}
		audit.Sandboxes[len(audit.Sandboxes)-1].State = "removal-started"
		if _, e := writeCleanupAudit(w, *audit); e != nil {
			return e
		}
		if e := removeSandboxSource(c.Path); e != nil {
			return fmt.Errorf("sandbox removal interrupted; verified recovery archive retained: %w", e)
		}
		if e := syncSandboxDirectory(filepath.Dir(c.Path)); e != nil {
			return e
		}
		audit.Sandboxes[len(audit.Sandboxes)-1].State = "removed"
		audit.Operations = append(audit.Operations, CleanupOperationAudit{Operation: "sandbox-remove", Source: c.Path, Target: path, Identity: c.Identity, Digest: r.Digest, CompletedAt: time.Now().UTC()})
		if _, e := writeCleanupAudit(w, *audit); e != nil {
			return e
		}
	}
	return nil
}
func restoreCleanupSandbox(w *workspace.Workspace, audit *CleanupAudit, identity string, now time.Time) (bool, error) {
	for i := range audit.Sandboxes {
		r := &audit.Sandboxes[i]
		if r.Candidate.Identity != identity {
			continue
		}
		c := r.Candidate
		if r.State == "restored" {
			return true, fmt.Errorf("sandbox already restored")
		}
		if !workspace.SafeSegment(c.RunID) || c.Path != sandboxPath(w, c.RunID) || r.Archive != sandboxArchivePath(w, audit.PlanID, identity) {
			return true, fmt.Errorf("invalid managed sandbox recovery paths")
		}
		for _, p := range []string{c.Path, r.Archive} {
			if e := rejectCleanupSymlinkParents(w.Root, p, true); e != nil {
				return true, e
			}
		}
		if _, e := os.Lstat(c.Path); !os.IsNotExist(e) {
			return true, fmt.Errorf("sandbox restore destination exists or is unreadable")
		}
		a, e := loadSandboxRecovery(*r)
		if e != nil {
			return true, e
		}
		if e := os.MkdirAll(filepath.Dir(c.Path), 0700); e != nil {
			return true, e
		}
		temp, e := os.MkdirTemp(filepath.Dir(c.Path), ".restore-sandbox-")
		if e != nil {
			return true, e
		}
		defer func() { _ = os.RemoveAll(temp) }()
		if e := reconstructSandbox(w, a, temp); e != nil {
			return true, e
		}
		files, _, e := sandboxSnapshot(temp, false)
		if e != nil || !reflect.DeepEqual(files, c.Files) {
			return true, fmt.Errorf("restored manifest mismatch")
		}
		// A mkdir reservation prevents rename from replacing an existing directory.
		if e := os.Mkdir(c.Path, 0700); e != nil {
			return true, fmt.Errorf("reserve absent destination: %w", e)
		}
		entries, e := os.ReadDir(temp)
		if e != nil {
			return true, e
		}
		for _, entry := range entries {
			if e := os.Rename(filepath.Join(temp, entry.Name()), filepath.Join(c.Path, entry.Name())); e != nil {
				return true, fmt.Errorf("restore interrupted; archive retained: %w", e)
			}
		}
		if e := os.Chmod(c.Path, fs.FileMode(c.RootMode)); e != nil {
			return true, e
		}
		final, _, e := sandboxSnapshot(c.Path, false)
		if e != nil || !reflect.DeepEqual(final, c.Files) {
			return true, fmt.Errorf("final restore manifest mismatch; archive retained")
		}
		r.State = "restored"
		audit.Operations = append(audit.Operations, CleanupOperationAudit{Operation: "sandbox-restore", Source: r.Archive, Target: c.Path, Identity: identity, Digest: r.Digest, CompletedAt: now.UTC()})
		_, e = writeCleanupAudit(w, *audit)
		return true, e
	}
	return false, nil
}

// Source inode identity detects replacing a directory with byte-identical content.
// Reflection keeps portable builds independent of platform-specific Stat_t types.
func sandboxNode(info os.FileInfo) string {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "unsupported"
	}
	fields := map[string]string{}
	for _, name := range []string{"Dev", "Ino"} {
		f := v.FieldByName(name)
		if !f.IsValid() {
			return "unsupported"
		}
		if f.CanUint() {
			fields[name] = fmt.Sprint(f.Uint())
		} else if f.CanInt() {
			fields[name] = fmt.Sprint(f.Int())
		} else {
			return "unsupported"
		}
	}
	return sandboxHash(fields)
}

func sandboxHandoffDigest(paths []RootHandoffPath) string {
	h := sha256.New()
	for _, p := range paths {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", p.Path, p.Mode, p.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
