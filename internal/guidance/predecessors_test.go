package guidance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testOwner(key string) PlanOwner {
	return PlanOwner{Binding: PlanOwnerBinding{WorkspaceID: "ws-test", InstallationID: "install-test", AuthorityPath: "/tmp/authority"}, Authorize: func() error { return nil }, Seal: func(payload []byte) (string, error) {
		m := hmac.New(sha256.New, []byte(key))
		m.Write(payload)
		return hex.EncodeToString(m.Sum(nil)), nil
	}}
}

func TestReviewedPredecessorFixtureAndManifestArePinned(t *testing.T) {
	var fixture struct {
		Schema string            `json:"schema"`
		Origin string            `json:"origin_commit"`
		Files  map[string]string `json:"files"`
	}
	b, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	if fixture.Schema != "dacli-guidance-test-bundle/v1" || fixture.Origin != "ce282fcb27f6ff7f41152cc4e7ab800900605e8b" || len(fixture.Files) != 13 {
		t.Fatalf("fixture identity/count invalid: %s %s %d", fixture.Schema, fixture.Origin, len(fixture.Files))
	}
	registry, e := reviewedBundles()
	if e != nil {
		t.Fatal(e)
	}
	if len(registry) != 2 || registry[1].digest != "6962ba499547efc4ffffbcc48f86299e1be174bde083a6797cfa56f441c0a0c1" || registry[1].origin != fixture.Origin {
		t.Fatalf("registry mismatch: %+v", registry)
	}
	for name, body := range fixture.Files {
		if digest([]byte(body)) != registry[1].files[name] {
			t.Fatalf("fixture body mismatch: %s", name)
		}
	}
	changed := false
	for name, old := range registry[1].files {
		if old != currentDigests(mustCurrent(t))[name] {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("reviewed predecessor unexpectedly has no changed rows")
	}
	if registry[1].files["capabilities.json"] != currentDigests(mustCurrent(t))["capabilities.json"] {
		t.Fatal("reviewed capabilities row should be unchanged")
	}
}

func TestLegacyV1PlanJSONAndDigestRemainStable(t *testing.T) {
	p := Plan{Schema: Schema, Operation: "reviewed-refresh", Root: "/tmp/root", RootIdentity: "file-id", SourceIdentity: ReviewedSourceID, SourceDigest: strings.Repeat("a", 64), CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), Files: []fileRecord{}, PlanDigest: ""}
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	want := `{"schema":"dacli-guidance/v1","operation":"reviewed-refresh","root":"/tmp/root","root_identity":"file-id","source_identity":"embedded-reviewed-dacli-skill/v1","source_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created_at":"2025-01-02T03:04:05Z","files":[],"plan_digest":""}`
	if string(raw) != want {
		t.Fatalf("v1 JSON changed: %s", raw)
	}
	if got := planDigest(p); got != "cd27a654fefe84236a69cf9edadd4913a2160fe26e1dd0acf1988aa254c6e2cf" {
		t.Fatalf("v1 digest changed: %s", got)
	}
}

func TestReviewedPredecessorManifestParserRejectsMalformedInputs(t *testing.T) {
	good, e := predecessorFiles.ReadFile("assets/predecessors/ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	current, e := embeddedCurrent()
	if e != nil {
		t.Fatal(e)
	}
	target := digestSource(current)
	if _, _, e = parsePredecessorManifest(good, target); e != nil {
		t.Fatal(e)
	}
	var base map[string]any
	if e = json.Unmarshal(good, &base); e != nil {
		t.Fatal(e)
	}
	files := base["files"].([]any)
	mutations := map[string]func(map[string]any){"unknown-field": func(m map[string]any) { m["unexpected"] = true }, "wrong-target": func(m map[string]any) { m["target_bundle_digest"] = strings.Repeat("0", 64) }, "missing-row": func(m map[string]any) { m["files"] = files[:12] }, "extra-duplicate-row": func(m map[string]any) { m["files"] = append(files, files[12]) }, "wrong-manifest-digest": func(m map[string]any) { m["before_bundle_digest"] = strings.Repeat("0", 64) }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			clone := map[string]any{}
			for k, v := range base {
				clone[k] = v
			}
			mutate(clone)
			raw, e := json.Marshal(clone)
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = parsePredecessorManifest(raw, target); e == nil {
				t.Fatalf("malformed %s manifest accepted", name)
			}
		})
	}
	if _, _, e = parsePredecessorManifest(append(append([]byte{}, good...), []byte(" {}")...), target); e == nil {
		t.Fatal("trailing manifest value accepted")
	}
	if e = uniqueJSONKeys([]byte(`{"schema":"a","schema":"b"}`)); e == nil {
		t.Fatal("duplicate JSON key accepted")
	}
}
func mustCurrent(t *testing.T) map[string][]byte {
	t.Helper()
	m, e := embeddedCurrent()
	if e != nil {
		t.Fatal(e)
	}
	return m
}

func TestReviewedPredecessorOwnedRefreshAndReplay(t *testing.T) {
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	b, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(tempDir(t), "old")
	for name, body := range fixture.Files {
		if e := writeFixtureFile(root, name, []byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	marker := filepath.Join(root, "private-adjacent.txt")
	if e = os.WriteFile(marker, []byte("preserve adjacent"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(filepath.Join(root, "SKILL.md"), 0600); e != nil {
		t.Fatal(e)
	}
	d := Diagnose([]string{root}, "", "", "", "")
	if len(d.Copies) != 1 || d.Copies[0].Status != StatusPreviousCurrent || d.Copies[0].BundleIdentity != "embedded-reviewed-dacli-skill/ce282fcb" {
		t.Fatalf("diagnosis=%+v", d)
	}
	owner := testOwner("test-owner-key")
	p, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(tempDir(t), "plan.json")
	if e = WritePlanOwned(path, p, owner); e != nil {
		t.Fatal(e)
	}
	r, e := ApplyOwned(path, owner)
	if e != nil || r.State != "completed" || len(r.Replaced) != 13 || len(r.Backups) != 13 {
		t.Fatalf("apply=%+v err=%v", r, e)
	}
	for _, entry := range p.Files {
		backup, e := os.ReadFile(entry.BackupPath)
		if e != nil || digest(backup) != entry.Before {
			t.Fatalf("backup %s is not exact: %v", entry.Path, e)
		}
		info, e := os.Stat(entry.BackupPath)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup mode %s: %v %v", entry.Path, info, e)
		}
	}
	mode, e := os.Stat(filepath.Join(root, "SKILL.md"))
	if e != nil || mode.Mode().Perm() != 0600 {
		t.Fatalf("target mode was not preserved: %v %v", mode, e)
	}
	adjacent, e := os.ReadFile(marker)
	if e != nil || string(adjacent) != "preserve adjacent" {
		t.Fatalf("adjacent file changed: %v", e)
	}
	second, e := ApplyOwned(path, owner)
	if e != nil || second.State != "completed" || len(second.Replaced) != 13 || len(second.Backups) != 13 {
		t.Fatalf("replay=%+v err=%v", second, e)
	}
	if d.FreshProcessState != "unknown" || d.CachedConnectionState != "unknown; file refresh cannot prove reconnect" {
		t.Fatalf("diagnosis overstated runtime freshness: %+v", d)
	}
}

func TestReviewedPredecessorPlanTamperingAndOldReaderRefusal(t *testing.T) {
	var e error
	var root string
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	b, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	root = filepath.Join(tempDir(t), "predecessor")
	for n, v := range fixture.Files {
		if e = writeFixtureFile(root, n, []byte(v)); e != nil {
			t.Fatal(e)
		}
	}
	owner := testOwner("first-key")
	p, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(tempDir(t), "tampered.json")
	if e = WritePlanOwned(path, p, owner); e != nil {
		t.Fatal(e)
	}
	var decoded Plan
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	decoded.Files[0].Before = "0000000000000000000000000000000000000000000000000000000000000000"
	decoded.PlanDigest = planDigest(decoded)
	raw, e = json.Marshal(decoded)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = ApplyOwned(path, owner); e == nil {
		t.Fatal("tampered but re-digested plan passed owner seal")
	}
	validPlan, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	validPath := filepath.Join(tempDir(t), "valid.json")
	if e = WritePlanOwned(validPath, validPlan, owner); e != nil {
		t.Fatal(e)
	}
	if _, e = ApplyAuthorized(validPath, func() error { return nil }); e == nil {
		t.Fatal("callback-only v1 reader accepted v2 plan")
	}
	if _, e = ApplyOwned(validPath, testOwner("different-key")); e == nil {
		t.Fatal("different owner key adopted v2 plan")
	}
	if _, e = os.Stat(filepath.Join(root, backupDirectoryName)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("refusal created backup state: %v", e)
	}
}

func TestReviewedPredecessorWriterRefusesModifiedPreview(t *testing.T) {
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	raw, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(tempDir(t), "candidate")
	for name, body := range fixture.Files {
		if e = writeFixtureFile(root, name, []byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	owner := testOwner("modified-preview-key")
	p, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	p.CreatedAt = p.CreatedAt.Add(time.Second)
	p.PlanDigest = planDigest(p)
	path := filepath.Join(tempDir(t), "must-not-exist.json")
	if e = WritePlanOwned(path, p, owner); e == nil {
		t.Fatal("writer resealed a modified preview")
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		t.Fatalf("modified preview wrote plan: %v", e)
	}
}

func TestReviewedPredecessorRejectsCustomizedAndMixedBundles(t *testing.T) {
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	b, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	owner := testOwner("bundle-test-key")
	for _, mode := range []string{"customized", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(tempDir(t), "candidate")
			current, e := embeddedCurrent()
			if e != nil {
				t.Fatal(e)
			}
			for name, body := range fixture.Files {
				contents := []byte(body)
				if mode == "customized" && name == "SKILL.md" {
					contents = append(append([]byte{}, contents...), byte('x'))
				}
				if mode == "mixed" && name == "SKILL.md" {
					contents = current[name]
				}
				if e = writeFixtureFile(root, name, contents); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = PreviewOwned(root, ReviewedSourceID, owner); e == nil {
				t.Fatal("unsupported mixed/customized bundle produced an owned plan")
			}
		})
	}
}

func TestReviewedPredecessorRejectsSymlinkAndHardlinkTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			var fixture struct {
				Files map[string]string `json:"files"`
			}
			raw, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(raw, &fixture); e != nil {
				t.Fatal(e)
			}
			root := filepath.Join(tempDir(t), "candidate")
			for name, body := range fixture.Files {
				if e = writeFixtureFile(root, name, []byte(body)); e != nil {
					t.Fatal(e)
				}
			}
			target := filepath.Join(root, "SKILL.md")
			external := filepath.Join(tempDir(t), "outside.md")
			if kind == "symlink" {
				if e = os.WriteFile(external, []byte(fixture.Files["SKILL.md"]), 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(target); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(external, target); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = os.Link(target, external); e != nil {
					t.Skipf("hardlinks unavailable: %v", e)
				}
			}
			if _, e = PreviewOwned(root, ReviewedSourceID, testOwner("unsafe-target-key")); e == nil {
				t.Fatal("unsafe target produced an owned plan")
			}
		})
	}
}

func TestReviewedPredecessorPartialBeforeCapabilitiesDoesNotClaimReplacement(t *testing.T) {
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	raw, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(tempDir(t), "candidate")
	for name, body := range fixture.Files {
		if e = writeFixtureFile(root, name, []byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	owner := testOwner("recovery-test-key")
	p, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if e = WritePlanOwned(plan, p, owner); e != nil {
		t.Fatal(e)
	}
	var unchanged fileRecord
	for _, entry := range p.Files {
		if entry.Path == "capabilities.json" {
			unchanged = entry
			break
		}
	}
	backup := unchanged.BackupPath
	if e = os.MkdirAll(filepath.Dir(backup), 0700); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(filepath.Join(root, "capabilities.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(backup, original, 0600); e != nil {
		t.Fatal(e)
	}
	receipt, e := ApplyOwned(plan, owner)
	if e == nil || receipt.State != "recovery_required" || len(receipt.Replaced) != 0 || len(receipt.Backups) != 1 {
		t.Fatalf("partial-before-capabilities replay=%+v err=%v", receipt, e)
	}
}

func TestReviewedPredecessorPartialRecoveryBoundaries(t *testing.T) {
	for _, scenario := range []string{"before-capabilities-backup", "after-capabilities-backup", "after-capabilities-rename"} {
		t.Run(scenario, func(t *testing.T) {
			var fixture struct {
				Files map[string]string `json:"files"`
			}
			raw, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(raw, &fixture); e != nil {
				t.Fatal(e)
			}
			root := filepath.Join(tempDir(t), "candidate")
			for name, body := range fixture.Files {
				if e = writeFixtureFile(root, name, []byte(body)); e != nil {
					t.Fatal(e)
				}
			}
			key := []byte("partial-recovery-key")
			var p Plan
			armed := false
			triggered := false
			owner := testOwner(string(key))
			owner.Seal = func(payload []byte) (string, error) {
				m := hmac.New(sha256.New, key)
				_, _ = m.Write(payload)
				return hex.EncodeToString(m.Sum(nil)), nil
			}
			owner.Authorize = func() error {
				if !armed || triggered || len(p.Files) == 0 {
					return nil
				}
				changed := 0
				for _, entry := range p.Files {
					contents, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
					if e == nil && digest(contents) == entry.After && entry.Path != "capabilities.json" {
						changed++
					}
				}
				caps := filepath.Join(root, "capabilities.json")
				var capEntry fileRecord
				for _, entry := range p.Files {
					if entry.Path == "capabilities.json" {
						capEntry = entry
						break
					}
				}
				backupExists := false
				if _, e := os.Lstat(capEntry.BackupPath); e == nil {
					backupExists = true
				}
				stages, _ := filepath.Glob(caps + ".dacli-refresh-*")
				capsBytes, _ := os.ReadFile(caps)
				capsChanged := digest(capsBytes) == capEntry.After && fileIDFromPath(t, caps) != capEntry.TargetID
				switch scenario {
				case "before-capabilities-backup":
					if changed == 2 && !backupExists {
						triggered = true
						return errors.New("injected stop before unchanged-row backup")
					}
				case "after-capabilities-backup":
					if backupExists && len(stages) > 0 {
						triggered = true
						return errors.New("injected stop after unchanged-row backup")
					}
				case "after-capabilities-rename":
					if capsChanged && backupExists {
						triggered = true
						return errors.New("injected stop after unchanged-row replacement")
					}
				}
				return nil
			}
			p, e = PreviewOwned(root, ReviewedSourceID, owner)
			if e != nil {
				t.Fatal(e)
			}
			plan := filepath.Join(tempDir(t), "plan.json")
			if e = WritePlanOwned(plan, p, owner); e != nil {
				t.Fatal(e)
			}
			armed = true
			first, e := ApplyOwned(plan, owner)
			if e == nil || !triggered || first.State != "recovery_required" {
				t.Fatalf("injected partial=%+v err=%v triggered=%v", first, e, triggered)
			}
			armed = false
			wantReplaced, wantBackups := 2, 2
			if scenario == "after-capabilities-backup" {
				wantBackups = 3
			}
			if scenario == "after-capabilities-rename" {
				wantReplaced, wantBackups = 3, 3
			}
			if len(first.Replaced) != wantReplaced || len(first.Backups) != wantBackups {
				t.Fatalf("initial exact subset=%+v want replaced/backups=%d/%d", first, wantReplaced, wantBackups)
			}
			before := map[string]string{}
			for _, entry := range p.Files {
				data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
				if e != nil {
					t.Fatal(e)
				}
				before[entry.Path] = digest(data)
			}
			replay, e := ApplyOwned(plan, owner)
			if e == nil || replay.State != "recovery_required" || len(replay.Replaced) != wantReplaced || len(replay.Backups) != wantBackups {
				t.Fatalf("replay broadened exact subset: %+v err=%v", replay, e)
			}
			for _, entry := range p.Files {
				data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
				if e != nil || digest(data) != before[entry.Path] {
					t.Fatalf("replay modified %s: %v", entry.Path, e)
				}
			}
		})
	}
}

func fileIDFromPath(t *testing.T, path string) string {
	t.Helper()
	info, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	return fileIdentity(info)
}

func TestReviewedPredecessorRechecksSameInodeBytesAtFinalBoundary(t *testing.T) {
	var fixture struct {
		Files map[string]string `json:"files"`
	}
	raw, e := os.ReadFile("testdata/previous-current-ce282fcb.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(tempDir(t), "candidate")
	for name, body := range fixture.Files {
		if e = writeFixtureFile(root, name, []byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	key := []byte("final-boundary-key")
	triggered := false
	owner := testOwner(string(key))
	owner.Authorize = func() error {
		target := filepath.Join(root, "SKILL.md")
		stages, _ := filepath.Glob(target + ".dacli-refresh-*")
		if !triggered && len(stages) > 0 {
			f, e := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
			if e != nil {
				return e
			}
			_, e = f.Write([]byte("same-inode drift"))
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
			triggered = true
		}
		return nil
	}
	owner.Seal = func(payload []byte) (string, error) {
		m := hmac.New(sha256.New, key)
		_, _ = m.Write(payload)
		return hex.EncodeToString(m.Sum(nil)), nil
	}
	p, e := PreviewOwned(root, ReviewedSourceID, owner)
	if e != nil {
		t.Fatal(e)
	}
	plan := filepath.Join(tempDir(t), "plan.json")
	if e = WritePlanOwned(plan, p, owner); e != nil {
		t.Fatal(e)
	}
	receipt, e := ApplyOwned(plan, owner)
	if !triggered || e == nil || receipt.State != "recovery_required" || len(receipt.Backups) != 1 || len(receipt.Replaced) != 0 {
		t.Fatalf("final-boundary drift receipt=%+v err=%v triggered=%v", receipt, e, triggered)
	}
	got, e := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if e != nil || string(got) != fixture.Files["SKILL.md"]+"same-inode drift" {
		t.Fatalf("same inode drift overwritten: %v", e)
	}
}
