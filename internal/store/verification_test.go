package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func nativeDiagnosticGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = VerificationEnvironment()
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("Git setup: %v %s", e, out)
		}
	}
	return dir
}
func assertNativeArtifactDigest(t *testing.T, ev VerificationEvidence, out []byte) {
	t.Helper()
	sum := sha256.Sum256(out)
	if ev.ArtifactHash != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("raw evidence digest changed")
	}
}

func TestNativeVerificationCaptureSanitizesBoundedWholeLineTails(t *testing.T) {
	secret := "capture-secret-component-4921"
	cmd := exec.Command("sh")
	cmd.Env = append(os.Environ(), "SERVICE_TOKEN="+secret)
	capture := nativeVerificationCapture{command: cmd, workspace: t.TempDir()}
	filler := []byte(strings.Repeat("harmless filler line with enough bytes\n", 3000))
	stdout := append(append([]byte(nil), filler...), []byte("SERVICE_TOKEN="+secret+"\nfatal: stdout actionable condition\n")...)
	stderr := append(append([]byte(nil), filler...), []byte("fatal: stderr actionable condition\n")...)
	if _, err := (nativeVerificationStream{capture: &capture}).Write(stdout); err != nil {
		t.Fatal(err)
	}
	if _, err := (nativeVerificationStream{capture: &capture, stderr: true}).Write(stderr); err != nil {
		t.Fatal(err)
	}
	safeOut, safeErr := capture.diagnosticStreams()
	if !strings.Contains(string(safeOut), "fatal: stdout actionable condition") || !strings.Contains(string(safeErr), "fatal: stderr actionable condition") {
		t.Fatalf("bounded raw-tail sanitization lost late actionable lines: stdout=%q stderr=%q", safeOut, safeErr)
	}
	if strings.Contains(string(safeOut), secret) || len(safeOut) > 4110 || len(safeErr) > 4110 {
		t.Fatalf("tail redaction or size bound changed: stdout=%q stderr=%q", safeOut, safeErr)
	}
	if len(capture.stdout.rawTail) > maxVerificationArtifact || len(capture.stderr.rawTail) > maxVerificationArtifact || !bytes.HasPrefix(capture.stdout.rawTail, []byte("harmless filler line")) {
		t.Fatal("diagnostic raw retention exceeded its bound or evicted a partial line")
	}
	if len(capture.output) != maxVerificationArtifact+1 {
		t.Fatalf("merged raw artifact capture bound changed: %d", len(capture.output))
	}
}

func TestNativeVerificationPostflightContextSurvivesCommandCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	postflight, cancelPostflight := nativeVerificationPostflightContext(parent)
	defer cancelPostflight()
	if postflight.Err() != nil {
		t.Fatalf("postflight inherited command cancellation: %v", postflight.Err())
	}
	deadline, ok := postflight.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > nativeVerificationPostflightTimeout {
		t.Fatalf("postflight must have a fresh bounded deadline: %v %t", deadline, ok)
	}
}

func TestNativeVerificationCancellationDuringPostflightCannotCertifySuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("sh")
	capture := nativeVerificationCapture{command: cmd, workspace: t.TempDir()}
	_, _ = (nativeVerificationStream{capture: &capture}).Write([]byte("safe late command output\n"))
	ev := VerificationEvidence{ExitCode: 0}
	err := nativeVerificationCancellationFailure(ctx, &ev, cmd, &capture, capture.workspace, nil)
	var policy *VerificationPolicyError
	diagnostic, ok := commandresult.AsDiagnostic(err)
	if ev.ExitCode == 0 || !errors.As(err, &policy) || !errors.Is(err, context.Canceled) || !ok || diagnostic.Timeout || diagnostic.ExitCode != nil || !strings.Contains(diagnostic.StdoutTail, "safe late command output") {
		t.Fatalf("postflight cancellation certified success or lost diagnostic: evidence=%+v diagnostic=%+v error=%v", ev, diagnostic, err)
	}
	if err.Error() != policy.Error() || strings.Contains(err.Error(), "safe late command output") {
		t.Fatalf("cancellation policy message exposed command output: %v", err)
	}
}

func TestNativeVerificationCleanupPolicyRemainsOpaqueWithDiagnostic(t *testing.T) {
	const secret = "cleanup-secret-component-8251"
	cmd := exec.Command("sh")
	cmd.Env = append(os.Environ(), "SERVICE_TOKEN="+secret)
	workspace := t.TempDir()
	capture := nativeVerificationCapture{command: cmd, workspace: workspace}
	_, _ = (nativeVerificationStream{capture: &capture, stderr: true}).Write([]byte("safe cleanup detail " + secret + "\n"))
	policy := &VerificationPolicyError{Reason: "native verification process cleanup unavailable"}
	err := nativeVerificationCommandError(context.Background(), cmd, &capture, workspace, policy)
	var gotPolicy *VerificationPolicyError
	diagnostic, ok := commandresult.AsDiagnostic(err)
	if !errors.As(err, &gotPolicy) || gotPolicy != policy || !ok || !strings.Contains(diagnostic.StderrTail, "safe cleanup detail") || strings.Contains(diagnostic.StderrTail, secret) {
		t.Fatalf("cleanup refusal lost its safe typed diagnostic: diagnostic=%+v error=%v", diagnostic, err)
	}
	if err.Error() != policy.Error() || strings.Contains(err.Error(), "safe cleanup detail") || strings.Contains(err.Error(), secret) {
		t.Fatalf("cleanup policy message became operational output: %v", err)
	}
}

func TestNativeVerificationTypedExitAndSeparateStreams(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf 'stdout-first\\nstdout-last\\n'; printf 'stderr-first\\nstderr-last ghp_example_secret_123456\\n' >&2; exit 17")
	var diagnostic *commandresult.ExternalError
	var exit *exec.ExitError
	if !errors.As(e, &diagnostic) || !errors.As(e, &exit) {
		t.Fatalf("native exit must retain typed diagnostic and process cause: %v", e)
	}
	d := diagnostic.Diagnostic
	if ev.ExitCode != 17 || d.ExitCode == nil || *d.ExitCode != 17 || d.Executable != "sh" || d.Operation != "verification command" || d.CwdScope != "." || d.NextAction == "" {
		t.Fatalf("native exit metadata lost: %+v %+v", ev, d)
	}
	if !strings.Contains(d.StdoutTail, "stdout-last") || strings.Contains(d.StdoutTail, "stderr") || !strings.Contains(d.StderrTail, "stderr-last") || strings.Contains(d.StderrTail, "stdout") || strings.Contains(e.Error(), "ghp_example_secret_123456") {
		t.Fatalf("separate safe streams lost: %+v", d)
	}
	if !strings.Contains(string(out), "stdout-first") || !strings.Contains(string(out), "stderr-first") {
		t.Fatal("merged raw evidence lost a stream")
	}
	assertNativeArtifactDigest(t, ev, out)
}
func TestNativeVerificationTypedSignal(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf before-signal >&2; kill -TERM $$")
	d, ok := commandresult.AsDiagnostic(e)
	if !ok || d.Kind != "signal" || d.Signal == "" || d.Timeout || ev.ExitCode == 0 {
		t.Fatalf("native signal diagnostic lost: %+v %v", d, e)
	}
	assertNativeArtifactDigest(t, ev, out)
}
func TestNativeVerificationTypedTimeoutAndInheritedPipe(t *testing.T) {
	for _, command := range []string{"sleep 4", "sleep 4 & printf inherited-pipe"} {
		t.Run(command, func(t *testing.T) {
			dir := nativeDiagnosticGit(t)
			deadline := 500 * time.Millisecond
			if strings.Contains(command, "&") {
				deadline = 3 * time.Second
			} // Pipe draining must expire before the fixture context.
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			start := time.Now()
			ev, out, e := RunAcceptanceVerificationContext(ctx, dir, "runner", command)
			d, ok := commandresult.AsDiagnostic(e)
			if !ok || ev.ExitCode == 0 || time.Since(start) > 2*time.Second {
				t.Fatalf("native bounded process diagnostic lost: %+v %v", d, e)
			}
			if command == "sleep 4" {
				var policy *VerificationPolicyError
				if !errors.As(e, &policy) || e.Error() != policy.Error() || !errors.Is(e, context.DeadlineExceeded) || !d.Timeout || d.Kind != "timeout" {
					t.Fatalf("native timeout policy/cause lost: %+v %v", d, e)
				}
			} else if d.Timeout || !errors.Is(e, exec.ErrWaitDelay) {
				t.Fatalf("inherited pipe WaitDelay must remain distinct from deadline: %+v %v", d, e)
			}
			assertNativeArtifactDigest(t, ev, out)
		})
	}
}
func TestNativeVerificationBoundedTailsAndSuccessIdentity(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "head -c 100000 /dev/zero; printf '\\nstdout-end\\n'; head -c 100000 /dev/zero >&2; printf '\\nstderr-end\\n' >&2; exit 17")
	d, ok := commandresult.AsDiagnostic(e)
	if !ok || !strings.Contains(d.StdoutTail, "stdout-end") || !strings.Contains(d.StderrTail, "stderr-end") || len(d.StdoutTail) > 4110 || len(d.StderrTail) > 4110 {
		t.Fatalf("native separate bounded tails lost: %+v %v", d, e)
	}
	if len(out) != maxVerificationArtifact || !strings.Contains(string(out), "[verification output truncated") {
		t.Fatal("merged artifact bound changed")
	}
	assertNativeArtifactDigest(t, ev, out)
	ev, out, e = RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf exact-success")
	if e != nil || ev.ExitCode != 0 || string(out) != "exact-success" {
		t.Fatalf("native success output changed: %+v %v", ev, e)
	}
	if e = ValidateFinalTreeVerification(ev, ev.CommitSHA, ev.TreeSHA); e != nil {
		t.Fatal(e)
	}
	assertNativeArtifactDigest(t, ev, out)
}

func TestNativeVerificationCancellationAndMissingExecutable(t *testing.T) {
	t.Run("cancelled-command", func(t *testing.T) {
		dir := nativeDiagnosticGit(t)
		marker := filepath.Join(t.TempDir(), "started")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, e := os.Stat(marker); e == nil {
					cancel()
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
		}()
		ev, out, e := RunAcceptanceVerificationContext(ctx, dir, "runner", "printf started > '"+marker+"'; sleep 4")
		<-done
		d, ok := commandresult.AsDiagnostic(e)
		if !ok || d.Timeout || !errors.Is(e, context.Canceled) || !IsVerificationPolicyError(e) || ev.ExitCode == 0 {
			t.Fatalf("cancelled command diagnostic/policy lost: %+v %v", d, e)
		}
		assertNativeArtifactDigest(t, ev, out)
	})
	t.Run("missing-shell", func(t *testing.T) {
		dir := nativeDiagnosticGit(t)
		git, e := exec.LookPath("git")
		if e != nil {
			t.Fatal(e)
		}
		bin := t.TempDir()
		if e = os.Symlink(git, filepath.Join(bin, "git")); e != nil {
			t.Fatal(e)
		}
		t.Setenv("PATH", bin)
		ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf never")
		d, ok := commandresult.AsDiagnostic(e)
		if !ok || d.Kind != "missing_executable" || IsVerificationPolicyError(e) || ev.ExitCode == 0 {
			t.Fatalf("actual missing shell diagnostic lost: %+v %v", d, e)
		}
		assertNativeArtifactDigest(t, ev, out)
	})
}
func TestNativeVerificationPolicyRemainsOpaque(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	ev, _, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf baseline")
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(dir, "dirty")
	if e = os.WriteFile(marker, []byte("dirty"), 0600); e != nil {
		t.Fatal(e)
	}
	_, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf must-not-run")
	if !IsVerificationPolicyError(e) || len(out) != 0 {
		t.Fatalf("dirty policy refusal changed: %v", e)
	}
	if _, ok := commandresult.AsDiagnostic(e); ok {
		t.Fatal("preflight policy acquired operational diagnostic")
	}
	if e = os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	_, _, e = RunAcceptanceVerificationRevisionContext(context.Background(), dir, "runner", "printf must-not-run", "wrong", ev.TreeSHA)
	if !IsVerificationPolicyError(e) {
		t.Fatalf("stale policy refusal changed: %v", e)
	}
	if _, ok := commandresult.AsDiagnostic(e); ok {
		t.Fatal("stale policy acquired operational diagnostic")
	}
	failed, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "printf dirty > dirty; printf safe-error >&2; exit 17")
	var policy *VerificationPolicyError
	d, ok := commandresult.AsDiagnostic(e)
	if !errors.As(e, &policy) || e.Error() != policy.Error() || !ok || d.ExitCode == nil || *d.ExitCode != 17 || failed.ExitCode == 0 || strings.Contains(e.Error(), "safe-error") {
		t.Fatalf("final policy precedence or executed cause lost: %+v %v", d, e)
	}
	assertNativeArtifactDigest(t, failed, out)
}

func TestNativeVerificationTruncationCannotCutRedactionContext(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	input := filepath.Join(t.TempDir(), "stream")
	// Both a token and a quoted outside path cross the old raw-tail cut. The
	// entire oversized diagnostic is withheld; raw artifact bytes stay evidence.
	secret := "ghp_" + strings.Repeat("s", 120)
	outside := "/private/" + strings.Repeat("directory/", 20) + "sensitive file"
	raw := strings.Repeat("x", 100) + secret + " '" + outside + "' " + strings.Repeat("z", maxVerificationArtifact-150)
	if e := os.WriteFile(input, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", "cat '"+input+"'; cat '"+input+"' >&2; exit 17")
	d, ok := commandresult.AsDiagnostic(e)
	if !ok || d.StdoutTail != nativeDiagnosticOmission || d.StderrTail != nativeDiagnosticOmission || strings.Contains(e.Error(), "ssss") || strings.Contains(e.Error(), "directory/") {
		t.Fatalf("truncation exposed ambiguous raw fragments: %+v %v", d, e)
	}
	if len(out) != maxVerificationArtifact || !strings.Contains(string(out), secret) {
		t.Fatal("raw merged evidence prefix was redacted or replaced")
	}
	assertNativeArtifactDigest(t, ev, out)
}

func TestNativeVerificationRetainsSafeActionableLineAfterLargeOutput(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	command := `awk 'BEGIN { for(i=0;i<3000;i++) print "harmless filler line with enough bytes"; print "fatal: stdout actionable condition" }'; awk 'BEGIN { for(i=0;i<3000;i++) print "harmless filler line with enough bytes"; print "fatal: stderr actionable condition" }' >&2; exit 17`
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", command)
	d, ok := commandresult.AsDiagnostic(e)
	if !ok || !strings.Contains(d.StdoutTail, "fatal: stdout actionable condition") || !strings.Contains(d.StderrTail, "fatal: stderr actionable condition") || !strings.Contains(e.Error(), "fatal: stderr actionable condition") {
		t.Fatalf("large output lost later safe actionable diagnostic lines: %+v %v", d, e)
	}
	assertNativeArtifactDigest(t, ev, out)
}

func TestNativeVerificationMultilineKnownSecretIsSanitizedPerLine(t *testing.T) {
	dir := nativeDiagnosticGit(t)
	t.Setenv("SERVICE_PASSWORD", "first-sensitive-component-4921\nsecond-sensitive-component-9853")
	ev, out, e := RunAcceptanceVerificationContext(context.Background(), dir, "runner", `printf '%s\n' "$SERVICE_PASSWORD"; printf 'fatal: safe condition\n' >&2; exit 17`)
	d, ok := commandresult.AsDiagnostic(e)
	if !ok || strings.Contains(d.StdoutTail, "sensitive-component") || strings.Contains(e.Error(), "sensitive-component") || !strings.Contains(d.StderrTail, "fatal: safe condition") {
		t.Fatalf("per-line known secret leaked or safe action lost: %+v %v", d, e)
	}
	if !strings.Contains(string(out), "first-sensitive-component-4921") {
		t.Fatal("raw evidence compatibility changed")
	}
	assertNativeArtifactDigest(t, ev, out)
}
