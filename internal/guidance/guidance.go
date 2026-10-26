// Package guidance diagnoses known installed skill copies and provides a
// reviewed, exact-file refresh plan (issue #1213).
package guidance

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	Schema              = "dacli-guidance/v1"
	ReviewedSourceID    = "embedded-reviewed-dacli-skill/v1"
	maxSkillFile        = 1 << 20
	maxPlanFile         = 1 << 20
	backupDirectoryName = ".dacli-guidance-backups"
)

//go:embed assets/current/**
var currentFiles embed.FS

type Status string

const (
	StatusMissing    Status = "missing"
	StatusLegacy     Status = "known_legacy"
	StatusCurrent    Status = "current"
	StatusCustomized Status = "customized_or_unrecognized"
	StatusConflict   Status = "conflicting"
	StatusUnsafe     Status = "unsafe_or_unreadable"
)

type Copy struct {
	Root          string `json:"root"`
	Status        Status `json:"status"`
	Identity      string `json:"identity,omitempty"`
	Digest        string `json:"digest,omitempty"`
	NextAction    string `json:"next_action"`
	SelectedFiles int    `json:"selected_files"`
}

type Diagnosis struct {
	Schema                  string `json:"schema"`
	Copies                  []Copy `json:"copies"`
	CombinedStatus          Status `json:"combined_status"`
	SelectedDiskState       string `json:"selected_disk_state"`
	FreshProcessState       string `json:"fresh_process_state"`
	CachedConnectionState   string `json:"cached_connection_state"`
	SelectedRuntimeIdentity string `json:"selected_runtime_identity"`
	SelectedRuntimeDigest   string `json:"selected_runtime_digest"`
	CapabilityStatus        string `json:"capability_status"`
	CapabilitySummary       string `json:"capability_summary"`
}

type fileRecord struct {
	Path       string   `json:"path"`
	Before     string   `json:"before_sha256"`
	After      string   `json:"after_sha256"`
	BackupPath string   `json:"backup_path"`
	TargetID   string   `json:"target_identity"`
	ParentIDs  []string `json:"parent_identities"`
}

// Plan is immutable: PlanDigest binds the exact target, reviewed source bundle,
// old bytes, replacement bytes, backup paths and target inodes.
type Plan struct {
	Schema         string       `json:"schema"`
	Operation      string       `json:"operation"`
	Root           string       `json:"root"`
	RootIdentity   string       `json:"root_identity"`
	SourceIdentity string       `json:"source_identity"`
	SourceDigest   string       `json:"source_digest"`
	CreatedAt      time.Time    `json:"created_at"`
	Files          []fileRecord `json:"files"`
	PlanDigest     string       `json:"plan_digest"`
}

type Receipt struct {
	Schema       string   `json:"schema"`
	PlanDigest   string   `json:"plan_digest"`
	State        string   `json:"state"`
	Backups      []string `json:"backups"`
	Replaced     []string `json:"replaced"`
	RecoveryPath string   `json:"recovery_path,omitempty"`
	Message      string   `json:"message"`
}

// DefaultRoots lists only the supported global skill locations. An explicit
// root is added verbatim for callers that selected another package location.
func DefaultRoots(home, selected string) []string {
	roots := []string{}
	if filepath.IsAbs(home) {
		roots = append(roots, filepath.Join(home, ".codex", "skills", "dacli"), filepath.Join(home, ".agents", "skills", "dacli"))
	}
	if selected != "" {
		roots = append(roots, selected)
	}
	return roots
}

var legacyDigests = map[string]string{
	"SKILL.md":                               "2713e120eab6367153844edcd77c4fab87f8149a2f3e878c2e64939921497dd5",
	"agents/openai.yaml":                     "c586dc0cdbf0511727855abca8659eda68755b68ff7f753acc51a5ed97195cd5",
	"capabilities.json":                      "a09a8ec568df0c302cfdc97cffc224e4ee855b290b1798d0254ae35539033e22",
	"references/continuous-operations.md":    "0858e4d4f83c2f8e4faf7963c4dc6d97b3c2565ff7da3ef4f516954b3c2ee001",
	"references/critical-path-github.md":     "8a873bee133d7784362db8a22ef1429c05707fcf838031dd68c628855ed0cc65",
	"references/github-landing.md":           "fc4d9006d772195b9ea1b823cd99fc4972d0cec8cc5dbe8a6d5bde0bb9330981",
	"references/model-economics.md":          "c7400fe7eb3722b921bd35731ee925264773bc4acc23f1ea3d4225bdf0bd0417",
	"references/operating-profiles.md":       "08c64e8b83d094723b01be75b4de1f4a77607396847c58fd03b08ca71a959744",
	"references/recovery.md":                 "07a734313256769cdc1f68da11645493378d46ced3f1a7853b7f13a9677925b1",
	"references/roster-design.md":            "140d583ce8858ecb5ee1c13f509c24bc522fb05212f7f292e034bcff5bbf83c5",
	"references/runtimes-models-skills.md":   "8854f258e5a59823e6e2dd9144f17c0a57d4fb3a44cee7dc3e58e32a65d2a1a2",
	"references/swarms-loops.md":             "0645f47f85cceddaec669e5c3b75dd121df8877f4f2ae2970ccdd7075cbf8304",
	"references/workspace-tasks-projects.md": "66c965cb53b19638fece8548ee27f9cf5ebe1ccbca1cba9dca51f04bec1bb46d",
}

// KnownFiles returns the exact files included in the reviewed managed set.
func KnownFiles() []string {
	files := make([]string, 0, len(legacyDigests))
	for name := range legacyDigests {
		files = append(files, name)
	}
	sort.Strings(files)
	return files
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func embeddedCurrent() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(currentFiles, "assets/current", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(name, "assets/current/")
		b, e := currentFiles.ReadFile(name)
		if e != nil {
			return e
		}
		if len(b) > maxSkillFile {
			return fmt.Errorf("bundled guidance file %s exceeds limit", rel)
		}
		out[rel] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) != len(legacyDigests) {
		return nil, fmt.Errorf("reviewed bundle file set differs from manifest")
	}
	return out, nil
}

func fileIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Sprintf("mode:%s;size:%d;mod:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
	}
	return fmt.Sprintf("dev:%d;ino:%d;mode:%o", st.Dev, st.Ino, info.Mode())
}

func checkedRegular(path string, limit int64) ([]byte, os.FileInfo, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 || !li.Mode().IsRegular() || li.Size() < 0 || li.Size() > limit {
		return nil, nil, fmt.Errorf("%s is not a bounded regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(li, opened) || opened.Size() > limit {
		return nil, nil, fmt.Errorf("%s changed during open", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, nil, fmt.Errorf("%s exceeds read limit", path)
	}
	final, err := f.Stat()
	if err != nil || !os.SameFile(opened, final) || final.Size() != int64(len(b)) {
		return nil, nil, fmt.Errorf("%s changed while reading", path)
	}
	return b, opened, nil
}

func safeRoot(root string) (os.FileInfo, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("skill root must be an absolute clean path")
	}
	cur := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(root, cur), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("skill path component %s is not a real directory", cur)
		}
	}
	return os.Stat(root)
}

func safeParents(root, relative string) ([]string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean != filepath.FromSlash(relative) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("managed path is not a clean relative target")
	}
	cur := root
	ids := []string{}
	for _, part := range strings.Split(filepath.Dir(clean), string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("managed parent %s is not a real directory", cur)
		}
		ids = append(ids, fileIdentity(info))
	}
	return ids, nil
}

func currentDigests(files map[string][]byte) map[string]string {
	out := make(map[string]string, len(files))
	for name, content := range files {
		out[name] = digest(content)
	}
	return out
}

func candidateDigest(root string, expectedDigests map[string]string) (string, int, bool, error) {
	parts := make([]string, 0, len(expectedDigests))
	all := true
	for _, rel := range KnownFiles() {
		if _, err := safeParents(root, rel); err != nil {
			return "", 0, false, err
		}
		want, expected := expectedDigests[rel]
		path := filepath.Join(root, filepath.FromSlash(rel))
		got, _, err := checkedRegular(path, maxSkillFile)
		if err != nil {
			if os.IsNotExist(err) {
				all = false
				continue
			}
			return "", 0, false, err
		}
		gotDigest := digest(got)
		if !expected || gotDigest != want {
			all = false
		}
		parts = append(parts, rel+"\x00"+gotDigest)
	}
	sort.Strings(parts)
	return digest([]byte(strings.Join(parts, "\x00"))), len(parts), all, nil
}

// Diagnose inspects only explicitly supplied roots. It neither recurses nor
// chooses among conflicting copies.
func Diagnose(roots []string, runtimeIdentity, runtimeDigest, capabilityStatus, capabilitySummary string) Diagnosis {
	d := Diagnosis{Schema: Schema, Copies: []Copy{}, SelectedDiskState: "selected files inspected only", FreshProcessState: "unknown", CachedConnectionState: "unknown; file refresh cannot prove reconnect", SelectedRuntimeIdentity: runtimeIdentity, SelectedRuntimeDigest: runtimeDigest, CapabilityStatus: capabilityStatus, CapabilitySummary: capabilitySummary}
	current, currentErr := embeddedCurrent()
	if currentErr != nil {
		d.CombinedStatus = StatusUnsafe
		return d
	}
	seen := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		copy := Copy{Root: root, Status: StatusMissing, NextAction: "Install the skill through its supported package manager or select its exact installation root."}
		info, err := safeRoot(root)
		if os.IsNotExist(err) {
			d.Copies = append(d.Copies, copy)
			continue
		}
		if err != nil {
			copy.Status, copy.NextAction = StatusUnsafe, "Ask the owner to inspect this installation path; symlinks and non-directories are not followed."
			d.Copies = append(d.Copies, copy)
			continue
		}
		copy.Identity = fileIdentity(info)
		_, count, isCurrent, err := candidateDigest(root, currentDigests(current))
		if err != nil {
			copy.Status, copy.NextAction = StatusUnsafe, "Ask the owner to inspect a target file; only regular bounded files are inspected."
			d.Copies = append(d.Copies, copy)
			continue
		}
		copy.SelectedFiles = count
		_, legacyCount, isLegacy, err := candidateDigest(root, legacyDigests)
		if err != nil {
			copy.Status, copy.NextAction = StatusUnsafe, "Ask the owner to inspect a target file; only regular bounded files are inspected."
			d.Copies = append(d.Copies, copy)
			continue
		}
		if isCurrent {
			copy.Status, copy.NextAction = StatusCurrent, "Selected skill files match the reviewed current bundle; process and cached connection freshness remain unknown."
		} else if isLegacy {
			copy.Status, copy.SelectedFiles = StatusLegacy, legacyCount
			copy.NextAction = "Review an exact refresh preview; apply only the pinned allowlisted instruction files after owner review."
		} else {
			copy.Status, copy.NextAction = StatusCustomized, "Preserve this copy. Review or manually migrate customized files; no automatic overwrite is available."
		}
		copy.Digest, _, _, _ = candidateDigest(root, currentDigests(current))
		d.Copies = append(d.Copies, copy)
	}
	d.CombinedStatus = StatusMissing
	var first Status
	for _, c := range d.Copies {
		if c.Status == StatusMissing {
			continue
		}
		if c.Status == StatusUnsafe {
			d.CombinedStatus = StatusUnsafe
			break
		}
		if first == "" {
			first = c.Status
		} else if c.Status != first {
			d.CombinedStatus = StatusConflict
			break
		}
		if c.Status == StatusCustomized {
			d.CombinedStatus = StatusCustomized
		}
	}
	if d.CombinedStatus != StatusUnsafe && d.CombinedStatus != StatusConflict && d.CombinedStatus != StatusCustomized {
		if first != "" {
			d.CombinedStatus = first
		}
	}
	if len(d.Copies) > 1 {
		var prior *Copy
		for i := range d.Copies {
			c := &d.Copies[i]
			if c.Status == StatusMissing {
				continue
			}
			if prior == nil {
				prior = c
				continue
			}
			if prior.Status != c.Status || prior.Digest != c.Digest {
				d.CombinedStatus = StatusConflict
				break
			}
		}
	}
	return d
}

func digestSource(files map[string][]byte) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(digest(files[k]))
		b.WriteByte(0)
	}
	return digest(b.Bytes())
}

func planDigest(p Plan) string { p.PlanDigest = ""; b, _ := json.Marshal(p); return digest(b) }

// Preview accepts only an exact known legacy copy. The emitted plan contains
// no replacement text, only digests and exact paths.
func Preview(root, sourceIdentity string) (Plan, error) {
	if sourceIdentity != "" && sourceIdentity != ReviewedSourceID {
		return Plan{}, errors.New("replacement source is not the reviewed embedded bundle")
	}
	root = filepath.Clean(root)
	rinfo, err := safeRoot(root)
	if err != nil {
		return Plan{}, err
	}
	current, err := embeddedCurrent()
	if err != nil {
		return Plan{}, err
	}
	_, _, legacyMatch, err := candidateDigest(root, legacyDigests)
	if err != nil {
		return Plan{}, err
	}
	_, _, currentMatch, err := candidateDigest(root, currentDigests(current))
	if err != nil {
		return Plan{}, err
	}
	if !legacyMatch && !currentMatch {
		return Plan{}, errors.New("selected skill is customized, conflicting or unrecognized; preserve it and review manually")
	}
	if currentMatch {
		return Plan{}, errors.New("selected skill already matches the reviewed current bundle")
	}
	p := Plan{Schema: Schema, Operation: "reviewed-refresh", Root: root, RootIdentity: fileIdentity(rinfo), SourceIdentity: ReviewedSourceID, SourceDigest: digestSource(current), CreatedAt: time.Now().UTC(), Files: []fileRecord{}}
	for _, rel := range KnownFiles() {
		before, info, e := checkedRegular(filepath.Join(root, filepath.FromSlash(rel)), maxSkillFile)
		if e != nil {
			return Plan{}, e
		}
		parents, e := safeParents(root, rel)
		if e != nil {
			return Plan{}, e
		}
		p.Files = append(p.Files, fileRecord{Path: rel, Before: digest(before), After: digest(current[rel]), TargetID: fileIdentity(info), ParentIDs: parents})
	}
	backupID := digest([]byte(p.Root + "\x00" + p.RootIdentity + "\x00" + p.SourceDigest))
	for i := range p.Files {
		p.Files[i].BackupPath = filepath.Join(root, backupDirectoryName, backupID, filepath.FromSlash(p.Files[i].Path))
	}
	p.PlanDigest = planDigest(p)
	return p, nil
}

// WritePlan creates a private immutable preview file and refuses replacement.
func WritePlan(path string, p Plan) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("plan path must be absolute and clean")
	}
	if _, err := safeRoot(filepath.Dir(path)); err != nil {
		return fmt.Errorf("plan parent is unsafe: %w", err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func readPlan(path string) (Plan, error) {
	var p Plan
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return p, errors.New("plan path must be absolute and clean")
	}
	if _, err := safeRoot(filepath.Dir(path)); err != nil {
		return p, fmt.Errorf("plan parent is unsafe: %w", err)
	}
	b, info, err := checkedRegular(path, maxPlanFile)
	if err != nil {
		return p, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return p, errors.New("plan file must be private (0600 or stricter)")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p, errors.New("plan contains trailing data")
	}
	if p.Schema != Schema || p.Operation != "reviewed-refresh" || p.PlanDigest == "" || p.PlanDigest != planDigest(p) {
		return p, errors.New("plan schema or content digest is invalid")
	}
	return p, nil
}

func validatePlan(p Plan) (map[string][]byte, os.FileInfo, error) {
	var zero os.FileInfo
	rootInfo, err := safeRoot(p.Root)
	if err != nil {
		return nil, zero, err
	}
	if fileIdentity(rootInfo) != p.RootIdentity {
		return nil, zero, errors.New("target root identity changed after preview")
	}
	current, err := embeddedCurrent()
	if err != nil {
		return nil, zero, err
	}
	if digestSource(current) != p.SourceDigest {
		return nil, zero, errors.New("reviewed source bundle identity changed")
	}
	if len(p.Files) != len(KnownFiles()) {
		return nil, zero, errors.New("plan file set is incomplete")
	}
	for i, rel := range KnownFiles() {
		entry := p.Files[i]
		if entry.Path != rel || entry.After != digest(current[rel]) || entry.BackupPath != filepath.Join(p.Root, backupDirectoryName, digest([]byte(p.Root+"\x00"+p.RootIdentity+"\x00"+p.SourceDigest)), filepath.FromSlash(rel)) {
			return nil, zero, errors.New("plan target or source binding is invalid")
		}
		parents, parentErr := safeParents(p.Root, rel)
		if parentErr != nil || !equalStrings(parents, entry.ParentIDs) {
			return nil, zero, errors.New("managed target parent identity changed after preview")
		}
	}
	return current, rootInfo, nil
}

// ApplyAuthorized requires a fresh owner authorization callback before backup
// writes and again immediately before each target replacement.
func ApplyAuthorized(path string, authorize func() error) (Receipt, error) {
	if authorize == nil {
		return Receipt{}, errors.New("owner authorization callback is required")
	}
	return apply(path, authorize)
}

// apply revalidates the immutable plan and each opened target immediately
// before making its exclusive byte-exact backup and atomic replacement.
func apply(path string, authorize func() error) (Receipt, error) {
	p, err := readPlan(path)
	if err != nil {
		return Receipt{}, err
	}
	current, rootInfo, err := validatePlan(p)
	if err != nil {
		return Receipt{Schema: Schema, PlanDigest: p.PlanDigest, State: "refused"}, err
	}
	r := Receipt{Schema: Schema, PlanDigest: p.PlanDigest, State: "applying", Backups: []string{}, Replaced: []string{}}
	if authorize != nil {
		if err := authorize(); err != nil {
			return r, recoveryError(&r, "owner authorization expired before refresh", err)
		}
	}
	backupRoot := filepath.Dir(p.Files[0].BackupPath)
	backupParent := filepath.Join(p.Root, backupDirectoryName)
	if err := os.Mkdir(backupParent, 0o700); err != nil && !os.IsExist(err) {
		return r, recoveryError(&r, "backup directory creation failed", err)
	}
	parentInfo, err := os.Lstat(backupParent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o077 != 0 {
		return r, recoveryError(&r, "existing backup directory is not private and regular", coalesce(err, errors.New("unsafe backup directory")))
	}
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		return r, recoveryError(&r, "exclusive backup root creation failed", err)
	}
	for i, entry := range p.Files {
		path := filepath.Join(p.Root, filepath.FromSlash(entry.Path))
		parents, parentErr := safeParents(p.Root, entry.Path)
		if parentErr != nil || !equalStrings(parents, entry.ParentIDs) {
			return r, recoveryError(&r, "target parent changed after preview", coalesce(parentErr, errors.New("parent identity changed")))
		}
		before, opened, e := checkedRegular(path, maxSkillFile)
		if e != nil || digest(before) != entry.Before || fileIdentity(opened) != entry.TargetID {
			return r, recoveryError(&r, "target changed after preview", coalesce(e, errors.New("target identity or bytes changed")))
		}
		// Revalidate plan and root immediately before each backup/replacement.
		_, currentRoot, e := validatePlan(p)
		if e != nil || !os.SameFile(rootInfo, currentRoot) {
			return r, recoveryError(&r, "plan or root changed before write", coalesce(e, errors.New("root identity changed")))
		}
		backupPath := filepath.Join(backupRoot, filepath.FromSlash(entry.Path))
		if filepath.Dir(backupPath) != backupRoot {
			if err := os.Mkdir(filepath.Dir(backupPath), 0o700); err != nil && !os.IsExist(err) {
				return r, recoveryError(&r, "private backup subdirectory creation failed", err)
			}
		}
		if err := verifyPrivateParents(backupRoot, filepath.Dir(backupPath)); err != nil {
			return r, recoveryError(&r, "backup directory creation failed", err)
		}
		if authorize != nil {
			if err := authorize(); err != nil {
				return r, recoveryError(&r, "owner authorization expired before backup", err)
			}
		}
		backup, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return r, recoveryError(&r, "exclusive backup creation failed", err)
		}
		r.Backups = append(r.Backups, backupPath)
		if _, err = backup.Write(before); err == nil {
			err = backup.Sync()
		}
		closeErr := backup.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return r, recoveryError(&r, "backup write failed before target replacement", err)
		}
		backupBytes, backupInfo, err := checkedRegular(backupPath, maxSkillFile)
		if err != nil || backupInfo.Mode().Perm() != 0o600 || !bytes.Equal(backupBytes, before) {
			return r, recoveryError(&r, "backup verification failed before target replacement", coalesce(err, errors.New("backup bytes or permissions differ")))
		}
		// Re-open and revalidate immediately before the rename; this is a
		// cooperative OS-user boundary, not hostile-process containment.
		latest, latestInfo, err := checkedRegular(path, maxSkillFile)
		if err != nil || digest(latest) != entry.Before || fileIdentity(latestInfo) != entry.TargetID {
			return r, recoveryError(&r, "target changed after backup; original bytes are preserved", coalesce(err, errors.New("target drift after backup")))
		}
		tmp, err := os.OpenFile(path+fmt.Sprintf(".dacli-refresh-%s-%d", p.PlanDigest, i), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return r, recoveryError(&r, "replacement staging failed after backup", err)
		}
		_, err = tmp.Write(current[entry.Path])
		if err == nil {
			err = tmp.Chmod(latestInfo.Mode().Perm())
		}
		if err == nil {
			err = tmp.Sync()
		}
		closeErr = tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "replacement staging failed after backup", err)
		}
		staged, stagedInfo, err := checkedRegular(tmp.Name(), maxSkillFile)
		if err != nil || !bytes.Equal(staged, current[entry.Path]) || stagedInfo.Mode().Perm() != latestInfo.Mode().Perm() {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "replacement staging verification failed after backup", coalesce(err, errors.New("staged bytes changed")))
		}
		latest, latestInfo, err = checkedRegular(path, maxSkillFile)
		if err != nil || digest(latest) != entry.Before || fileIdentity(latestInfo) != entry.TargetID {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "target changed before replacement; original bytes are preserved", coalesce(err, errors.New("target drift before rename")))
		}
		parents, parentErr = safeParents(p.Root, entry.Path)
		if parentErr != nil || !equalStrings(parents, entry.ParentIDs) {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "target parent changed before replacement", coalesce(parentErr, errors.New("parent identity changed")))
		}
		if authorize != nil {
			if err := authorize(); err != nil {
				removeStaging(&r, tmp.Name())
				return r, recoveryError(&r, "owner authorization expired at final replacement boundary", err)
			}
		}
		latest, latestInfo, err = checkedRegular(path, maxSkillFile)
		if err != nil || digest(latest) != entry.Before || fileIdentity(latestInfo) != entry.TargetID {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "target changed at final replacement boundary", coalesce(err, errors.New("target drift before rename")))
		}
		parents, parentErr = safeParents(p.Root, entry.Path)
		if parentErr != nil || !equalStrings(parents, entry.ParentIDs) {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "target parent changed at final replacement boundary", coalesce(parentErr, errors.New("parent identity changed")))
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			removeStaging(&r, tmp.Name())
			return r, recoveryError(&r, "replacement failed after backup; restore from recorded backup", err)
		}
		r.Replaced = append(r.Replaced, path)
	}
	r.State = "completed"
	r.Message = "Allowlisted guidance files refreshed from the reviewed embedded bundle. Backups are byte-exact and private; cached host connections and loaded skill text were not refreshed."
	return r, nil
}

func removeStaging(r *Receipt, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		r.RecoveryPath = path
	}
}

func coalesce(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func verifyPrivateParents(root, selected string) error {
	for _, path := range []string{root, selected} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("backup directory %s is not a private real directory", path)
		}
	}
	if selected != root && filepath.Dir(selected) != root {
		return errors.New("backup path escaped its exclusive plan directory")
	}
	return nil
}
func recoveryError(r *Receipt, message string, cause error) error {
	if len(r.Backups) > 0 || len(r.Replaced) > 0 {
		r.State = "recovery_required"
	} else {
		r.State = "refused"
	}
	r.Message = message + ": " + cause.Error()
	if r.RecoveryPath == "" && len(r.Backups) > 0 {
		r.RecoveryPath = r.Backups[0]
	}
	return cause
}
