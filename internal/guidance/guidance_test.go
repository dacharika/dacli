package guidance

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

//go:embed testdata/known-legacy/**
var knownLegacyFiles embed.FS

func TestKnownLegacyFixtureMatchesHistoricalDigestManifest(t *testing.T) {
	fixture, err := embeddedLegacyFixture()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture) != len(legacyDigests) {
		t.Fatalf("fixture file count=%d manifest count=%d", len(fixture), len(legacyDigests))
	}
}

func embeddedLegacyFixture() (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(knownLegacyFiles, "testdata/known-legacy", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel := strings.TrimPrefix(name, "testdata/known-legacy/")
		b, e := knownLegacyFiles.ReadFile(name)
		if e != nil {
			return e
		}
		if digest(b) != legacyDigests[rel] {
			return fmt.Errorf("fixture digest mismatch for %s", rel)
		}
		out[rel] = b
		return nil
	})
	return out, err
}

func TestEmbeddedCurrentBundleMatchesCanonicalSkillFiles(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	canonical := filepath.Join(repo, "skills", "dacli")
	current, err := embeddedCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != len(legacyDigests) {
		t.Fatalf("bundle file count=%d manifest count=%d", len(current), len(legacyDigests))
	}
	for name, bundled := range current {
		checkedIn, err := os.ReadFile(filepath.Join(canonical, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(bundled, checkedIn) {
			t.Fatalf("embedded replacement %s differs from canonical source: %v", name, err)
		}
	}
}

func TestApplyAuthorizedRechecksOwnerAtFinalReplacementBoundary(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "owner-plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	checks := 0
	result, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks == 3 {
			return errors.New("owner session revoked before replacement")
		}
		return nil
	})
	if err == nil || result.State != "recovery_required" || len(result.Backups) != 1 || len(result.Replaced) != 0 {
		t.Fatalf("final owner revalidation result=%+v err=%v checks=%d", result, err, checks)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("revoked owner callback did not stop replacement")
	}
}

func TestApplyRevalidatesTargetAfterBackupAtFinalReplacementBoundary(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "target-plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "SKILL.md")
	checks := 0
	result, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks == 3 {
			return os.WriteFile(target, []byte("post-backup substitution"), 0o644)
		}
		return nil
	})
	if err == nil || result.State != "recovery_required" || len(result.Backups) != 1 || len(result.Replaced) != 0 {
		t.Fatalf("final target revalidation result=%+v err=%v checks=%d", result, err, checks)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "post-backup substitution" {
		t.Fatal("final revalidation overwrote changed target")
	}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dacli-guidance-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func fixtureTree(t *testing.T, source string) string {
	t.Helper()
	root := filepath.Join(tempDir(t), "dacli")
	legacyFiles, err := embeddedLegacyFixture()
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel string, content []byte) error { return writeFixtureFile(root, rel, content) }
	if source == "current" {
		err = fsWalk(currentFiles, "assets/current/", write)
	} else {
		for rel, content := range legacyFiles {
			if err = write(rel, content); err != nil {
				break
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFixtureFile(root, rel string, content []byte) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func fsWalk(fsys embed.FS, prefix string, fn func(string, []byte) error) error {
	return fs.WalkDir(fsys, strings.TrimSuffix(prefix, "/"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		b, e := fsys.ReadFile(name)
		if e != nil {
			return e
		}
		return fn(strings.TrimPrefix(name, prefix), b)
	})
}

func TestDiagnoseClassifiesKnownLegacyCurrentCustomizedMissingAndConflict(t *testing.T) {
	legacy := fixtureTree(t, "legacy")
	current := fixtureTree(t, "current")
	custom := fixtureTree(t, "legacy")
	p := filepath.Join(custom, "SKILL.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, ' ')
	if err = os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(tempDir(t), "not-installed")
	cases := []struct {
		roots []string
		want  Status
	}{
		{[]string{legacy}, StatusLegacy}, {[]string{current}, StatusCurrent},
		{[]string{custom}, StatusCustomized}, {[]string{missing}, StatusMissing},
		{[]string{legacy, current}, StatusConflict},
	}
	for _, tc := range cases {
		got := Diagnose(tc.roots, "runtime", "sha256:test", "compatible", "ok")
		if got.CombinedStatus != tc.want {
			t.Errorf("roots %v: got %s want %s", tc.roots, got.CombinedStatus, tc.want)
		}
	}
}

func TestPreviewApplyRefreshesOnlyAllowlistedFilesWithPrivateByteExactBackups(t *testing.T) {
	root := fixtureTree(t, "legacy")
	private := filepath.Join(root, "private-profile.json")
	privateBytes := []byte("credential-like-user-data\n")
	if err := os.WriteFile(private, privateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range KnownFiles() {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = b
	}
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "refresh-plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	r, err := apply(plan, nil)
	if err != nil {
		t.Fatalf("apply: %v (%+v)", err, r)
	}
	if r.State != "completed" || len(r.Backups) != len(KnownFiles()) || len(r.Replaced) != len(KnownFiles()) {
		t.Fatalf("receipt=%+v", r)
	}
	current, err := embeddedCurrent()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range KnownFiles() {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, current[name]) {
			t.Fatalf("replacement %s mismatch: %v", name, err)
		}
		backup := filepath.Join(root, backupDirectoryName, digest([]byte(p.Root+"\x00"+p.RootIdentity+"\x00"+p.SourceDigest)), filepath.FromSlash(name))
		b, err := os.ReadFile(backup)
		info, statErr := os.Stat(backup)
		if err != nil || statErr != nil || info.Mode().Perm() != 0o600 || !bytes.Equal(b, before[name]) {
			t.Fatalf("backup %s is not private byte-exact: read=%v stat=%v", name, err, statErr)
		}
	}
	if b, err := os.ReadFile(private); err != nil || !bytes.Equal(b, privateBytes) {
		t.Fatal("adjacent private profile changed")
	}
	replayed, replayErr := apply(plan, nil)
	if replayErr != nil || replayed.State != "completed" || len(replayed.Backups) != len(KnownFiles()) || len(replayed.Replaced) != len(KnownFiles()) {
		t.Fatalf("completed plan replay lost its exact disposition: receipt=%+v err=%v", replayed, replayErr)
	}
	d := Diagnose([]string{root}, "runtime", "digest", "unknown", "no fresh host observation")
	if d.FreshProcessState != "unknown" || !strings.Contains(d.CachedConnectionState, "unknown") {
		t.Fatalf("refresh claimed process freshness: %+v", d)
	}
}

func TestPreviewRejectsOneByteCustomizationAndPlanSourceSubstitution(t *testing.T) {
	root := fixtureTree(t, "legacy")
	path := filepath.Join(root, "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if err = os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(root, ReviewedSourceID); err == nil || !strings.Contains(err.Error(), "customized") {
		t.Fatalf("one-byte custom copy accepted: %v", err)
	}
	if _, err := Preview(root, "caller-supplied-path"); err == nil {
		t.Fatal("caller replacement source was accepted")
	}
}

func TestApplyRejectsRecomputedPlanForCustomizedBeforeBytes(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(p.Files[0].Path))
	custom := []byte("owner customized guidance that is not in the reviewed legacy manifest")
	if err := os.WriteFile(target, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	customBytes, info, err := checkedRegular(target, maxSkillFile)
	if err != nil {
		t.Fatal(err)
	}
	p.Files[0].Before = digest(customBytes)
	p.Files[0].TargetID = fileIdentity(info)
	p.SourceIdentity = "caller-recomputed-source"
	p.PlanDigest = planDigest(p)
	planPath := filepath.Join(tempDir(t), "forged-custom.json")
	if err := writePlan(planPath, p); err != nil {
		t.Fatal(err)
	}
	r, err := apply(planPath, nil)
	got, readErr := os.ReadFile(target)
	if readErr != nil || err == nil || r.State != "refused" || len(r.Replaced) != 0 || !bytes.Equal(got, custom) {
		t.Fatalf("recomputed customized plan was accepted: receipt=%+v err=%v preserved=%v read=%v", r, err, bytes.Equal(got, custom), readErr)
	}
}

func TestApplyRevalidatesPostPreviewTargetAndPlan(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "SKILL.md")
	if err := os.WriteFile(target, []byte("changed after preview"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := apply(plan, nil)
	if err == nil || r.State != "refused" {
		t.Fatalf("target drift was not refused: receipt=%+v err=%v", r, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "changed after preview" {
		t.Fatal("changed target was overwritten")
	}
	var tampered Plan
	raw, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.SourceIdentity = "unreviewed"
	raw, err = json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(tempDir(t), "bad-plan.json")
	if err = os.WriteFile(bad, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(bad, nil); err == nil {
		t.Fatal("plan source substitution accepted")
	}
}

func TestDiagnosisAndPreviewRefuseSymlink(t *testing.T) {
	root := fixtureTree(t, "legacy")
	target := filepath.Join(root, "SKILL.md")
	_ = os.Remove(target)
	if err := os.Symlink(filepath.Join(root, "capabilities.json"), target); err != nil {
		t.Fatal(err)
	}
	d := Diagnose([]string{root}, "", "", "unknown", "")
	if d.CombinedStatus != StatusUnsafe {
		t.Fatalf("diagnosis=%+v", d)
	}
	if _, err := Preview(root, ReviewedSourceID); err == nil {
		t.Fatal("unsafe target previewed")
	}
}

func TestApplyBackupFailureIsRecoveryRequiredAndLeavesOriginalTarget(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	backupParent := filepath.Join(root, backupDirectoryName)
	if err := os.WriteFile(backupParent, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := apply(plan, nil)
	if err == nil || r.State != "refused" {
		t.Fatalf("backup failure result=%+v err=%v", r, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("backup failure altered target")
	}
}

func TestApplyRefusesSubstitutedBackupDestination(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Dir(p.Files[0].BackupPath)
	if err := os.MkdirAll(filepath.Dir(backupRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupRoot, "substituted"), []byte("unrelated backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := apply(plan, nil)
	if err == nil || r.State != "refused" || len(r.Replaced) != 0 {
		t.Fatalf("substituted backup destination accepted: %+v %v", r, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("backup substitution changed target")
	}
}

func TestApplyRefusesBackupParentSymlinkSubstitutionAtCreationBoundary(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "backup-parent-swap.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	outside := tempDir(t)
	backupParent := filepath.Join(root, backupDirectoryName)
	backupID := filepath.Base(filepath.Dir(p.Files[0].BackupPath))
	checks := 0
	r, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks != 2 {
			return nil
		}
		if err := os.Mkdir(filepath.Join(outside, backupID), 0o700); err != nil {
			return err
		}
		if err := os.Rename(backupParent, backupParent+".moved"); err != nil {
			return err
		}
		return os.Symlink(outside, backupParent)
	})
	if err == nil || r.State != "refused" || len(r.Replaced) != 0 {
		t.Fatalf("backup parent substitution was not refused: receipt=%+v err=%v", r, err)
	}
	if _, err := os.Stat(filepath.Join(outside, backupID, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("substituted backup parent received a backup: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("parent substitution altered target bytes")
	}
}

func TestApplyRefusesReviewedPlanParentSubstitutionBeforeBackup(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	planParent := tempDir(t)
	plan := filepath.Join(planParent, "plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	outside := tempDir(t)
	moved := planParent + ".moved"
	checks := 0
	r, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks != 2 {
			return nil
		}
		if err := os.Rename(planParent, moved); err != nil {
			return err
		}
		t.Cleanup(func() { _ = os.RemoveAll(moved) })
		return os.Symlink(outside, planParent)
	})
	if err == nil || r.State != "refused" || len(r.Backups) != 0 || len(r.Replaced) != 0 {
		t.Fatalf("reviewed plan parent substitution escaped its pinned source: receipt=%+v err=%v", r, err)
	}
	if _, err := os.Stat(p.Files[0].BackupPath); !os.IsNotExist(err) {
		t.Fatalf("plan parent substitution created a byte backup: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("plan parent substitution altered target bytes")
	}
}

func TestApplyRefusesLateBackupContentSubstitution(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "backup-content-swap.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	backupPath := p.Files[0].BackupPath
	checks := 0
	r, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks == 3 {
			return os.WriteFile(backupPath, []byte("substituted backup bytes"), 0o600)
		}
		return nil
	})
	if err == nil || r.State != "recovery_required" || len(r.Backups) != 1 || len(r.Replaced) != 0 {
		t.Fatalf("late backup substitution was not recovery-required: receipt=%+v err=%v", r, err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.Files[0].Path)))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("late backup substitution altered target")
	}
	replay, replayErr := ApplyAuthorized(plan, func() error { return nil })
	if replayErr == nil || replay.State != "recovery_required" || replay.RecoveryPath == "" || len(replay.Replaced) != 0 {
		t.Fatalf("corrupt prior backup did not retain recovery boundary: receipt=%+v err=%v", replay, replayErr)
	}
}

func TestApplyRefusesTargetParentSubstitutionAtFinalBoundary(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "target-parent-swap.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	outside := tempDir(t)
	checks := 0
	swapped := false
	r, err := ApplyAuthorized(plan, func() error {
		checks++
		// First three files have completed after seven callbacks; the fourth
		// target is under references/ and is pinned before callback nine.
		if checks == 9 {
			references := filepath.Join(root, "references")
			if err := os.Mkdir(filepath.Join(outside, "references"), 0o755); err != nil {
				return err
			}
			if err := os.Rename(references, references+".moved"); err != nil {
				return err
			}
			if err := os.Symlink(filepath.Join(outside, "references"), references); err != nil {
				return err
			}
			swapped = true
		}
		return nil
	})
	if !swapped || err == nil || r.State != "recovery_required" || len(r.Replaced) != 3 {
		t.Fatalf("target parent substitution was not safely bounded: swapped=%v receipt=%+v err=%v", swapped, r, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "references", "continuous-operations.md.dacli-refresh-"+p.PlanDigest+"-3")); !os.IsNotExist(err) {
		t.Fatalf("outside target parent received a staged replacement: %v", err)
	}
}

func TestPreviewRejectsHardlinkedManagedTarget(t *testing.T) {
	root := fixtureTree(t, "legacy")
	target := filepath.Join(root, "SKILL.md")
	linked := filepath.Join(tempDir(t), "linked-guidance.md")
	if err := os.Link(target, linked); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := Preview(root, ReviewedSourceID); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("hardlinked guidance target was not refused: %v", err)
	}
}

func TestApplyReplacementFailureAfterBackupReturnsRecoveryRequired(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "SKILL.md.dacli-refresh-"+p.PlanDigest+"-0")
	if err := os.WriteFile(stage, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := apply(plan, nil)
	if err == nil || r.State != "recovery_required" || len(r.Backups) != 1 || len(r.Replaced) != 0 {
		t.Fatalf("replacement failure result=%+v err=%v", r, err)
	}
	got, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil || digest(got) != p.Files[0].Before {
		t.Fatal("failed replacement altered original target")
	}
	backup, err := os.ReadFile(r.Backups[0])
	if err != nil || digest(backup) != p.Files[0].Before {
		t.Fatal("recovery backup is missing or not byte-exact")
	}
	replay, replayErr := apply(plan, nil)
	if replayErr == nil || replay.State != "recovery_required" || len(replay.Backups) != 1 || len(replay.Replaced) != 0 || replay.RecoveryPath == "" || !equalStrings(replay.Backups, r.Backups) {
		t.Fatalf("partial replacement replay lost recovery boundary: receipt=%+v original=%+v err=%v", replay, r, replayErr)
	}
}

func TestApplyReplayAfterPartialReplacementRetainsExactRecoverySubset(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "partial-replay-plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	checks := 0
	first, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks == 4 {
			return errors.New("stop after first replacement")
		}
		return nil
	})
	if err == nil || first.State != "recovery_required" || len(first.Replaced) != 1 || len(first.Backups) != 1 {
		t.Fatalf("partial replacement fixture did not stop after one replacement: receipt=%+v err=%v", first, err)
	}
	replay, replayErr := ApplyAuthorized(plan, func() error { return nil })
	if replayErr == nil || replay.State != "recovery_required" || len(replay.Replaced) != 1 || len(replay.Backups) != 1 || replay.RecoveryPath == "" {
		t.Fatalf("partial replay lost verified progress: receipt=%+v err=%v", replay, replayErr)
	}
}

func TestApplyReplayAfterMissingPartialTargetRetainsRecoveryBoundary(t *testing.T) {
	root := fixtureTree(t, "legacy")
	p, err := Preview(root, ReviewedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(tempDir(t), "missing-target-replay-plan.json")
	if err := writePlan(plan, p); err != nil {
		t.Fatal(err)
	}
	checks := 0
	first, err := ApplyAuthorized(plan, func() error {
		checks++
		if checks == 4 {
			return errors.New("stop after first replacement")
		}
		return nil
	})
	if err == nil || first.State != "recovery_required" || len(first.Replaced) != 1 || len(first.Backups) != 1 {
		t.Fatalf("partial replacement fixture did not stop after one replacement: receipt=%+v err=%v", first, err)
	}
	missing := first.Replaced[0]
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	available := filepath.Join(root, filepath.FromSlash(p.Files[1].Path))
	availableBefore, err := os.ReadFile(available)
	if err != nil {
		t.Fatal(err)
	}
	replay, replayErr := ApplyAuthorized(plan, func() error { return nil })
	if replayErr == nil || replay.State != "recovery_required" || len(replay.Backups) != 1 || !equalStrings(replay.Backups, first.Backups) || replay.RecoveryPath != missing {
		t.Fatalf("missing partial target lost its recovery boundary: receipt=%+v first=%+v err=%v", replay, first, replayErr)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("replay replaced or recreated missing target: %v", err)
	}
	availableAfter, err := os.ReadFile(available)
	if err != nil || !bytes.Equal(availableAfter, availableBefore) {
		t.Fatalf("replay changed available neighboring guidance: err=%v", err)
	}
}
