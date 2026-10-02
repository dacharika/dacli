package store

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestRunVerificationCapturesCompleteProvenance(t *testing.T) {
	w, err := workspace.Init(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "evidence-branch"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if out, err := gitx.Run(w.Root, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(w.Root+"/artifact.txt", []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed"}} {
		if out, err := gitx.Run(w.Root, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	ev, out, err := RunAcceptanceVerification(w.Root, "a-verifier", "printf artifact")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "artifact" || ev.Command != "printf artifact" || ev.ExitCode != 0 || ev.DurationMS < 0 || ev.ArtifactHash == "" || ev.Verifier != "a-verifier" || ev.Branch != "evidence-branch" || ev.CommitSHA == "" || ev.TreeSHA == "" || !ev.Clean || len(ev.Argv) != 3 || ev.WorkingDirectory != w.Root || len(ev.RuntimeVersions) == 0 || len(ev.ToolVersions) == 0 {
		t.Fatalf("incomplete verification provenance: output=%q evidence=%#v", out, ev)
	}
	if err := ValidateFinalTreeVerification(ev, ev.CommitSHA, ev.TreeSHA); err != nil {
		t.Fatalf("fresh immutable evidence rejected: %v", err)
	}
}

func TestObserveGitHubExternalVerificationBindsEveryRecordToExactHead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := `#!/bin/sh
if [ "$1" = api ]; then
  printf '%s\n' '{"check_runs":[{"id":11,"name":"unit","status":"completed","conclusion":"success","details_url":"https://checks/11","head_sha":"abc"},{"id":12,"name":"stale","status":"completed","conclusion":"success","head_sha":"old"}]}'
else
  printf '%s\n' '[{"databaseId":21,"name":"ci","status":"in_progress","conclusion":"","url":"https://runs/21","headSha":"abc"}]'
fi
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got, err := observeGitHubExternalVerification(t.TempDir(), "owner/repo", "abc", at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("external evidence = %+v", got)
	}
	for _, evidence := range got {
		if evidence.HeadSHA != "abc" || evidence.ObservedAt != at || evidence.Name == "stale" {
			t.Fatalf("non-exact evidence escaped: %+v", evidence)
		}
		if err := AttachExternalVerification(&VerificationEvidence{}, evidence); err != nil {
			t.Fatalf("valid external evidence rejected: %v", err)
		}
	}
	states := map[string]bool{}
	for _, evidence := range got {
		states[evidence.State] = true
	}
	if !states["observed"] || !states["pending"] {
		t.Fatalf("external states = %+v", got)
	}
}

func TestRunVerificationKeepsUnknownProvenanceOutsideGit(t *testing.T) {
	dir := t.TempDir()
	ev, out, err := RunAdvisoryVerification(dir, "a-verifier", "printf artifact")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "artifact" || ev.Branch != "" || ev.CommitSHA != "" || ev.ArtifactHash == "" {
		t.Fatalf("non-git verification fabricated provenance: output=%q evidence=%#v", out, ev)
	}
}

func TestVerificationArtifactIsBoundedWithExplicitTruncationMarker(t *testing.T) {
	out := bytes.Repeat([]byte("x"), maxVerificationArtifact*2)
	bounded := boundVerificationArtifact(out)
	if len(bounded) != maxVerificationArtifact || !strings.HasSuffix(string(bounded), "[verification output truncated at 65536 bytes]\n") {
		t.Fatalf("bounded verification artifact = %d bytes, suffix %q", len(bounded), bounded[len(bounded)-64:])
	}
}

func TestRunVerificationRetainsTypedSanitizedFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GITHUB_TOKEN", "ghp_verify_secret_123456")
	ev, out, err := RunAdvisoryVerification(dir, "a-verifier", "printf 'bad ghp_verify_secret_123456 /private/outside/path' >&2; exit 19")
	if err == nil {
		t.Fatal("failed verification returned nil error")
	}
	diagnostic, ok := commandresult.AsDiagnostic(err)
	if !ok || diagnostic.ExitCode == nil || *diagnostic.ExitCode != 19 || diagnostic.Operation != "verification command" {
		t.Fatalf("diagnostic = %#v, %v", diagnostic, err)
	}
	if ev.ExitCode != 19 || strings.Contains(string(out), "ghp_verify_secret_123456") || strings.Contains(string(out), "/private/outside/path") {
		t.Fatalf("verification failure leaked or lost exit identity: evidence=%#v output=%q", ev, out)
	}
}

func TestVerificationEvidenceRoundTripAndLegacyMigration(t *testing.T) {
	doc := &mdstore.Doc{}
	tk := &Task{Doc: doc}
	want := VerificationEvidence{
		Command:      "go test ./...",
		ExitCode:     0,
		DurationMS:   17,
		ArtifactHash: "sha256:abc123",
		Verifier:     "a-reviewer",
		Branch:       "dacli/432-evidence",
		CommitSHA:    "deadbeef",
	}
	if err := AppendVerificationEvidence(tk, want); err != nil {
		t.Fatal(err)
	}
	got := VerificationEvidenceRecords(tk)
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}

	legacy := &Task{Doc: &mdstore.Doc{Sections: []mdstore.Section{{Level: 2, Title: "Log", Content: "- 2026-08-13T10:00:00Z verified by `go test ./...` (exit 0) in branch old at abc123\n"}}}}
	records := VerificationEvidenceRecords(legacy)
	if len(records) != 1 {
		t.Fatalf("legacy records = %#v, want one readable record", records)
	}
	if records[0].Legacy == "" || records[0].Command != "" || records[0].ArtifactHash != "" || records[0].Verifier != "" || records[0].Branch != "" || records[0].CommitSHA != "" || records[0].DurationMS != 0 {
		t.Fatalf("legacy parser fabricated structured fields: %#v", records[0])
	}
}

func TestAcceptanceVerificationRefusesDirtyParentTreeBeforeRunning(t *testing.T) {
	dir := verificationRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "command-ran")
	ev, _, err := RunAcceptanceVerification(dir, "reviewer", "touch command-ran")
	if err == nil || !IsVerificationPolicyError(err) || !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("dirty tree result = %#v, %v; want policy refusal naming dirty tree", ev, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("verification command ran despite dirty preflight: %v", statErr)
	}
	if ev.Clean {
		t.Fatalf("dirty parent-SHA evidence asserted clean: %#v", ev)
	}
}

func TestAcceptanceVerificationRejectsTreeMutationDuringCommand(t *testing.T) {
	dir := verificationRepo(t)
	ev, _, err := RunAcceptanceVerification(dir, "reviewer", "printf mutation >> source.txt")
	if err == nil || !IsVerificationPolicyError(err) {
		t.Fatalf("mutating verification result = %#v, %v; want policy refusal", ev, err)
	}
	for _, want := range []string{"changed during execution", "before commit", "after commit", "clean=true", "clean=false"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("mutation refusal %q missing %q", err, want)
		}
	}
	if err := ValidateFinalTreeVerification(ev, ev.CommitSHA, ev.TreeSHA); err != nil {
		t.Fatalf("captured before-state should itself be structurally complete: %v", err)
	}
}

func TestFinalTreeGateRejectsHistoricalAndStaleEvidence(t *testing.T) {
	legacy := VerificationEvidence{Legacy: "- verified by old text"}
	if err := ValidateFinalTreeVerification(legacy, "reviewed", "tree"); err == nil || !strings.Contains(err.Error(), "not acceptance-grade") {
		t.Fatalf("legacy gate = %v, want explicit non-acceptance-grade rejection", err)
	}
	stale := VerificationEvidence{CommitSHA: "old", TreeSHA: "old-tree", Clean: true}
	if err := ValidateFinalTreeVerification(stale, "reviewed", "reviewed-tree"); err == nil || !strings.Contains(err.Error(), "rerun verification on the reviewed head") {
		t.Fatalf("stale gate = %v, want actionable reviewed-head rejection", err)
	}
}

func TestExternalVerificationCannotTreatSkippedAsGreen(t *testing.T) {
	var ev VerificationEvidence
	err := AttachExternalVerification(&ev, ExternalVerificationEvidence{Provider: "github", WorkflowRunID: "123", State: "skipped", Conclusion: "success", SkipReason: "not triggered"})
	if err == nil || !strings.Contains(err.Error(), "cannot carry conclusion") || len(ev.External) != 0 {
		t.Fatalf("skipped green attachment = %#v, %v; want rejection without append", ev.External, err)
	}
	if err := AttachExternalVerification(&ev, ExternalVerificationEvidence{Provider: "github", WorkflowRunID: "123", CheckRunID: "456", State: "observed", Conclusion: "success"}); err != nil {
		t.Fatalf("observed GitHub IDs rejected: %v", err)
	}
	if err := AttachExternalVerification(&ev, ExternalVerificationEvidence{Provider: "github", State: "unobservable", SkipReason: "API unavailable"}); err != nil {
		t.Fatalf("explicitly unobservable check rejected: %v", err)
	}
}

func TestExternalVerificationPolicyRequiresExactGreenChecksAndArtifacts(t *testing.T) {
	policy := ExternalVerificationPolicy{HeadSHA: "head", RequiredChecks: []string{"ci"}, RequiredArtifacts: []string{"binary"}}
	good := VerificationEvidence{External: []ExternalVerificationEvidence{{Provider: "github-actions", WorkflowRunID: "22", HeadSHA: "head", Name: "ci", State: "observed", Conclusion: "success", Artifacts: []ExternalArtifactEvidence{{ID: "31", Name: "binary", Digest: "sha256:abc"}}}}}
	if err := ValidateExternalVerification(good, policy); err != nil {
		t.Fatalf("exact green evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*VerificationEvidence){
		"stale head": func(ev *VerificationEvidence) { ev.External[0].HeadSHA = "old" },
		"skipped": func(ev *VerificationEvidence) {
			ev.External[0].State, ev.External[0].Conclusion, ev.External[0].SkipReason = "skipped", "", "not run"
		},
		"superseded": func(ev *VerificationEvidence) {
			ev.External[0].State, ev.External[0].Conclusion, ev.External[0].SkipReason = "superseded", "", "rerun"
		},
		"expired artifact":  func(ev *VerificationEvidence) { ev.External[0].Artifacts[0].Expired = true },
		"artifact mismatch": func(ev *VerificationEvidence) { ev.External[0].Artifacts[0].Name = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			ev := good
			ev.External = append([]ExternalVerificationEvidence(nil), good.External...)
			ev.External[0].Artifacts = append([]ExternalArtifactEvidence(nil), good.External[0].Artifacts...)
			mutate(&ev)
			if err := ValidateExternalVerification(ev, policy); err == nil {
				t.Fatalf("%s evidence became green: %+v", name, ev)
			}
		})
	}
}

func verificationRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if out, err := gitx.Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "source.txt"}, {"commit", "-m", "seed"}} {
		if out, err := gitx.Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func TestCommandCriterionRequiresCompleteVerificationEvidence(t *testing.T) {
	tk := &Task{Doc: &mdstore.Doc{Sections: []mdstore.Section{{Level: 2, Title: "Acceptance", Content: "- [ ] `go test ./...` exits zero\n"}}}}
	if !AcceptanceRequiresCommandVerification(tk, 1) {
		t.Fatal("inline command criterion was not recognized as command verification")
	}
	for _, evidence := range []VerificationEvidence{
		{Command: "go test ./...", ExitCode: 0, Verifier: "a-reviewer"},
		{Command: "go test ./...", ExitCode: 0, ArtifactHash: "sha256:abc"},
	} {
		if err := ValidateCommandVerification(evidence); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("incomplete evidence %#v accepted: %v", evidence, err)
		}
	}
}

func TestNativeAcceptanceContextDeadlineAndInheritedPipe(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = VerificationEnvironment()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	for _, command := range []string{"sleep 4", "sleep 4 & printf done"} {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		started := time.Now()
		ev, _, err := RunAcceptanceVerificationContext(ctx, dir, "runner", command)
		cancel()
		if err == nil || ev.ExitCode == 0 || time.Since(started) > 2*time.Second {
			t.Fatalf("deadline/pipe bound %q: %+v %v %s", command, ev, err, time.Since(started))
		}
	}
}

func TestNativeAcceptanceContextRefusesFilterBeforeStatus(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "filter-executed")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = VerificationEnvironment()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "tracked.x"), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.x filter=poison\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	run("config", "filter.poison.clean", "touch '"+marker+"'; cat")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := RunAcceptanceVerificationContext(ctx, dir, "runner", "printf harmless")
	if !IsVerificationPolicyError(err) {
		t.Fatal("executable filter accepted", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("filter executed during contextual observation", err)
	}
}

func TestNativeAcceptanceContextBoundsOutputAndValidatesFinalTree(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = VerificationEnvironment()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ev, out, err := RunAcceptanceVerificationContext(ctx, dir, "runner", "head -c 200000 /dev/zero")
	if err != nil || len(out) != maxVerificationArtifact || !bytes.HasSuffix(out, []byte("[verification output truncated at 65536 bytes]\n")) {
		t.Fatal("unbounded output", len(out), err)
	}
	if err = ValidateFinalTreeVerification(ev, ev.CommitSHA, ev.TreeSHA); err != nil {
		t.Fatal("not acceptance grade", err)
	}
}

func TestNativeAcceptanceContextRefusesWrongRevisionBeforeCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = VerificationEnvironment()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	run("init")
	run("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	baseline, _, err := RunAcceptanceVerificationContext(ctx, dir, "runner", "printf baseline")
	if err != nil {
		t.Fatal(err)
	}
	run("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "advanced")
	_, _, err = RunAcceptanceVerificationRevisionContext(ctx, dir, "runner", "touch '"+marker+"'", baseline.CommitSHA, baseline.TreeSHA)
	if !IsVerificationPolicyError(err) {
		t.Fatal("wrong revision command permitted", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command executed on wrong revision", err)
	}
}
