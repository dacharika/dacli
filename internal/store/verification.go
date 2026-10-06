package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const verificationSection = "Verification Evidence"
const maxVerificationArtifact = 64 << 10

// VerificationEvidence is the machine-readable provenance of one command
// verification. Legacy is populated only for pre-schema rendered Log lines;
// their unavailable provenance fields deliberately remain zero-valued.
type VerificationEvidence struct {
	Command          string                         `json:"command"`
	Argv             []string                       `json:"argv,omitempty"`
	WorkingDirectory string                         `json:"working_directory,omitempty"`
	ExitCode         int                            `json:"exit_code"`
	DurationMS       int64                          `json:"duration_ms"`
	ArtifactHash     string                         `json:"artifact_hash"`
	Verifier         string                         `json:"verifier"`
	Branch           string                         `json:"branch"`
	CommitSHA        string                         `json:"commit_sha"`
	TreeSHA          string                         `json:"tree_sha,omitempty"`
	Clean            bool                           `json:"clean,omitempty"`
	RuntimeVersions  map[string]string              `json:"runtime_versions,omitempty"`
	ToolVersions     map[string]string              `json:"tool_versions,omitempty"`
	External         []ExternalVerificationEvidence `json:"external,omitempty"`
	ExternalPolicy   *GitHubRequiredCheckPolicy     `json:"external_policy,omitempty"`
	Legacy           string                         `json:"legacy,omitempty"`
}

// ExternalVerificationEvidence is a typed attachment to evidence observed by
// another system. A skipped or unobservable check cannot carry a successful
// conclusion: absence of evidence is never silently promoted to green.
type ExternalVerificationEvidence struct {
	Provider      string                     `json:"provider"`
	WorkflowRunID string                     `json:"workflow_run_id,omitempty"`
	CheckRunID    string                     `json:"check_run_id,omitempty"`
	HeadSHA       string                     `json:"head_sha,omitempty"`
	URL           string                     `json:"url,omitempty"`
	Name          string                     `json:"name,omitempty"`
	ObservedAt    time.Time                  `json:"observed_at,omitempty"`
	State         string                     `json:"state"` // observed, pending, skipped, or unobservable
	Conclusion    string                     `json:"conclusion,omitempty"`
	SkipReason    string                     `json:"skip_reason,omitempty"`
	Artifacts     []ExternalArtifactEvidence `json:"artifacts,omitempty"`
}

type ExternalArtifactEvidence struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Expired   bool   `json:"expired"`
}

type ExternalVerificationPolicy struct {
	HeadSHA           string   `json:"head_sha"`
	RequiredChecks    []string `json:"required_checks,omitempty"`
	RequiredArtifacts []string `json:"required_artifacts,omitempty"`
}

// VerificationPolicyError means the requested command did not run against a
// provably immutable tree. Retrying the identical command cannot fix it.
type VerificationPolicyError struct{ Reason string }

func (e *VerificationPolicyError) Error() string { return e.Reason }

func IsVerificationPolicyError(err error) bool {
	var target *VerificationPolicyError
	return errors.As(err, &target)
}

type verificationTree struct {
	Branch string
	Commit string
	Tree   string
	Clean  bool
}

// A final-tree refusal stays the public policy error even when an executed
// command also failed. Only its sanitized operational cause is retained.
type verificationCommandPolicyError struct {
	policy  *VerificationPolicyError
	command error
}

func (e *verificationCommandPolicyError) Error() string   { return e.policy.Error() }
func (e *verificationCommandPolicyError) Unwrap() []error { return []error{e.policy, e.command} }
func verificationFinalPolicyError(policy *VerificationPolicyError, command error) error {
	if _, ok := commandresult.AsDiagnostic(command); ok {
		return &verificationCommandPolicyError{policy: policy, command: command}
	}
	return policy
}

// RunVerification is the legacy advisory entry point. It remains for source
// compatibility; acceptance decisions must call RunAcceptanceVerification.
func RunVerification(dir, verifier, command string) (VerificationEvidence, []byte, error) {
	return RunAdvisoryVerification(dir, verifier, command)
}

// RunAdvisoryVerification captures diagnostic evidence from any working tree.
// Its explicit name distinguishes it from evidence allowed to close work.
func RunAdvisoryVerification(dir, verifier, command string) (VerificationEvidence, []byte, error) {
	return runVerification(dir, verifier, command, false)
}

// RunAcceptanceVerification runs a command only against a clean, committed
// git tree and proves that HEAD and the worktree did not change while it ran.
func RunAcceptanceVerification(dir, verifier, command string) (VerificationEvidence, []byte, error) {
	return runVerification(dir, verifier, command, true)
}

// VerificationSummary is the compact, stable presentation shared by commands.
// The complete bounded command output remains available to callers and is only
// rendered when they explicitly request it.
func VerificationSummary(ev VerificationEvidence) string {
	result := "passed"
	if ev.ExitCode != 0 {
		result = "failed"
	}
	return fmt.Sprintf("verification %s: exit=%d duration=%dms verifier=%s artifact=%s", result, ev.ExitCode, ev.DurationMS, ev.Verifier, ev.ArtifactHash)
}

func VerificationOutput(out []byte, full bool) string {
	if !full || len(out) == 0 {
		return ""
	}
	return string(out)
}

// PersistVerificationArtifact retains the exact bounded bytes named by the
// evidence digest, including failures that cannot be appended to a task.
func PersistVerificationArtifact(w *workspace.Workspace, ev VerificationEvidence, out []byte) error {
	hash := strings.TrimPrefix(ev.ArtifactHash, "sha256:")
	decoded, decodeErr := hex.DecodeString(hash)
	if decodeErr != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid verification artifact hash %q", ev.ArtifactHash)
	}
	dir := filepath.Join(w.Root, workspace.Dir, "verification-artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, hash+".log"), out)
}

// RunAcceptanceVerificationContext is the bounded native-runner entry point.
// The caller selects the command and deadline from trusted configuration.
func RunAcceptanceVerificationContext(ctx context.Context, dir, verifier, command string) (VerificationEvidence, []byte, error) {
	if ctx == nil {
		return VerificationEvidence{ExitCode: 1}, nil, &VerificationPolicyError{Reason: "verification context required"}
	}
	return runVerificationWithContext(ctx, dir, verifier, command, true, true, "", "")
}

// RunAcceptanceVerificationRevisionContext additionally refuses a mismatched
// expected commit/tree before executing the configured command.
func RunAcceptanceVerificationRevisionContext(ctx context.Context, dir, verifier, command, commit, tree string) (VerificationEvidence, []byte, error) {
	if ctx == nil || commit == "" || tree == "" {
		return VerificationEvidence{ExitCode: 1}, nil, &VerificationPolicyError{Reason: "verification context and exact revision required"}
	}
	return runVerificationWithContext(ctx, dir, verifier, command, true, true, commit, tree)
}

// RunAcceptanceVerificationArgvRevisionContext runs literal owner argv in a
// contained cwd. Evidence and before/after observations always bind the entire
// checkout, independently of the command working directory.
func RunAcceptanceVerificationArgvRevisionContext(ctx context.Context, dir, verifier string, argv []string, cwd, commit, tree string) (VerificationEvidence, []byte, error) {
	if ctx == nil || commit == "" || tree == "" || len(argv) == 0 || len(argv) > 128 || strings.TrimSpace(argv[0]) == "" {
		return VerificationEvidence{ExitCode: 1}, nil, &VerificationPolicyError{Reason: "structured verification descriptor required"}
	}
	total := 0
	for _, arg := range argv {
		total += len(arg)
		if strings.ContainsAny(arg, "\x00\r\n") {
			return VerificationEvidence{ExitCode: 1}, nil, &VerificationPolicyError{Reason: "invalid verification argument"}
		}
	}
	if total > 16384 {
		return VerificationEvidence{ExitCode: 1}, nil, &VerificationPolicyError{Reason: "verification argument limit exceeded"}
	}
	if _, err := verificationExecutionDirectory(dir, cwd); err != nil {
		return VerificationEvidence{ExitCode: 1}, nil, err
	}
	return runVerificationExecution(ctx, dir, verifier, "", true, true, commit, tree, append([]string(nil), argv...), cwd)
}
func verificationExecutionDirectory(root, relative string) (string, error) {
	denied := func() (string, error) {
		return "", &VerificationPolicyError{Reason: "verification cwd must be a real directory inside the checkout"}
	}
	if relative == "" || len(relative) > 4096 || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") || strings.ContainsAny(relative, "\\\x00\r\n") {
		return denied()
	}
	absolute, e := filepath.Abs(root)
	if e != nil {
		return denied()
	}
	physical, e := filepath.EvalSymlinks(absolute)
	if e != nil || physical != absolute {
		return denied()
	}
	target := filepath.Join(absolute, relative)
	actual, e := filepath.EvalSymlinks(target)
	if e != nil || actual != target {
		return denied()
	}
	info, e := os.Lstat(target)
	if e != nil || !info.IsDir() {
		return denied()
	}
	rel, e := filepath.Rel(absolute, target)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return denied()
	}
	return target, nil
}

// VerificationEnvironment excludes authority/session secrets and Git overrides.
// This is cooperative hygiene, not isolation from a hostile local OS user.
func VerificationEnvironment() []string {
	var env []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "DACLI_") || strings.HasPrefix(upper, "MCP_") || strings.HasPrefix(upper, "GIT_") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "CREDENTIAL") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
}

// Native evidence retains its merged raw prefix. Diagnostic streams instead
// sanitize bounded complete lines before keeping their safe trailing text.
type nativeVerificationCapture struct {
	mu             sync.Mutex
	output         []byte
	command        *exec.Cmd
	workspace      string
	stdout, stderr nativeDiagnosticStream
}
type nativeDiagnosticStream struct {
	pending  []byte
	safeTail string
	overlong bool
}

const nativeDiagnosticOmission = "[diagnostic line omitted: bounded capture exceeded]"
const nativeDiagnosticTailLimit = 4096

func (s *nativeDiagnosticStream) flush(cmd *exec.Cmd, workspace string) {
	var safe string
	if s.overlong {
		safe = nativeDiagnosticOmission
	} else if len(s.pending) > 0 {
		safe = commandresult.SanitizeCommandTail(cmd, string(s.pending), workspace)
	}
	if safe != "" {
		s.safeTail += safe + "\n"
		if len(s.safeTail) > nativeDiagnosticTailLimit {
			s.safeTail = s.safeTail[len(s.safeTail)-nativeDiagnosticTailLimit:]
		}
	}
	s.pending = nil
	s.overlong = false
}
func (s *nativeDiagnosticStream) write(p []byte, cmd *exec.Cmd, workspace string) {
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		complete := end >= 0
		if !complete {
			end = len(p)
		} else {
			end++
		}
		part := p[:end]
		p = p[end:]
		if !s.overlong {
			if len(s.pending)+len(part) > maxVerificationArtifact {
				s.pending = nil
				s.overlong = true
			} else {
				s.pending = append(s.pending, part...)
			}
		}
		if complete {
			s.flush(cmd, workspace)
		}
	}
}

type nativeVerificationStream struct {
	capture *nativeVerificationCapture
	stderr  bool
}

func (w nativeVerificationStream) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	remaining := maxVerificationArtifact + 1 - len(c.output)
	if remaining > 0 {
		part := p
		if len(part) > remaining {
			part = part[:remaining]
		}
		c.output = append(c.output, part...)
	}
	stream := &c.stdout
	if w.stderr {
		stream = &c.stderr
	}
	stream.write(p, c.command, c.workspace)
	return n, nil
}
func (c *nativeVerificationCapture) diagnosticStreams() ([]byte, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stdout.flush(c.command, c.workspace)
	c.stderr.flush(c.command, c.workspace)
	return []byte(strings.TrimSpace(c.stdout.safeTail)), []byte(strings.TrimSpace(c.stderr.safeTail))
}
func runVerification(dir, verifier, command string, acceptanceGrade bool) (VerificationEvidence, []byte, error) {
	return runVerificationWithContext(context.Background(), dir, verifier, command, acceptanceGrade, false, "", "")
}
func runVerificationWithContext(ctx context.Context, dir, verifier, command string, acceptanceGrade, native bool, expectedCommit, expectedTree string) (VerificationEvidence, []byte, error) {
	return runVerificationExecution(ctx, dir, verifier, command, acceptanceGrade, native, expectedCommit, expectedTree, nil, "")
}
func runVerificationExecution(ctx context.Context, dir, verifier, command string, acceptanceGrade, native bool, expectedCommit, expectedTree string, argv []string, cwd string) (VerificationEvidence, []byte, error) {
	absDir, _ := filepath.Abs(dir)
	// core/597: Git observation may fail before execution. Default to failure
	// until a real command completes, so an unstarted check cannot appear green.
	ev := VerificationEvidence{
		Command: command, Argv: []string{"sh", "-c", command}, WorkingDirectory: absDir,
		Verifier: verifier, ExitCode: 1,
		RuntimeVersions: map[string]string{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH},
		ToolVersions:    map[string]string{},
	}
	if argv != nil {
		ev.Argv = append([]string(nil), argv...)
	}
	readTree := readVerificationTree
	if !native {
		ev.ToolVersions = verificationToolVersions(dir)
	} else {
		readTree = func(dir string) (verificationTree, error) { return readVerificationTreeContext(ctx, dir) }
		version, err := runVerificationGit(ctx, dir, "--version")
		if err != nil {
			return ev, nil, &VerificationPolicyError{Reason: "git version observation unavailable"}
		}
		ev.ToolVersions["git"] = strings.TrimSpace(version)
	}
	before, beforeErr := readTree(dir)
	if beforeErr == nil {
		ev.Branch, ev.CommitSHA, ev.TreeSHA, ev.Clean = before.Branch, before.Commit, before.Tree, before.Clean
	}
	if acceptanceGrade {
		if beforeErr != nil {
			return ev, nil, &VerificationPolicyError{Reason: fmt.Sprintf("acceptance-grade verification requires a committed git tree: %v", beforeErr)}
		}
		if expectedCommit != "" && (before.Commit != expectedCommit || before.Tree != expectedTree) {
			return ev, nil, &VerificationPolicyError{Reason: "verification revision changed before execution"}
		}
		if !before.Clean {
			return ev, nil, &VerificationPolicyError{Reason: "acceptance-grade verification refused: working tree is dirty; commit or discard the changes, or use advisory verification explicitly"}
		}
	}
	started := time.Now()
	c := exec.Command("sh", "-c", command)
	c.Dir = dir
	var out []byte
	var err error
	var capture nativeVerificationCapture
	var processCause error
	if !native {
		out, err = commandresult.Run(c, commandresult.RunOptions{Operation: "verification command", WorkspaceRoot: dir})
	} else {
		if argv != nil {
			executionDir, e := verificationExecutionDirectory(dir, cwd)
			if e != nil {
				return ev, nil, e
			}
			c = exec.CommandContext(ctx, argv[0], argv[1:]...)
			c.Dir = executionDir
		} else {
			c = exec.CommandContext(ctx, "sh", "-c", command)
			c.Dir = dir
		}
		c.Env = VerificationEnvironment()
		c.WaitDelay = 250 * time.Millisecond
		capture.command = c
		capture.workspace = dir
		c.Stdout = nativeVerificationStream{capture: &capture}
		c.Stderr = nativeVerificationStream{capture: &capture, stderr: true}
		err = runNativeVerificationCommand(c)
		out = capture.output
		processCause = err
		if ctx.Err() != nil && !IsVerificationPolicyError(processCause) {
			err = ctx.Err()
		}
	}
	out = boundVerificationArtifact(out)
	ev.DurationMS = time.Since(started).Milliseconds()
	sum := sha256.Sum256(out)
	ev.ArtifactHash = "sha256:" + hex.EncodeToString(sum[:])
	if err == nil {
		ev.ExitCode = 0
	} else {
		ev.ExitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ev.ExitCode = ee.ExitCode()
		}
	}
	if native && err != nil && !IsVerificationPolicyError(processCause) {
		cause := processCause
		if ctx.Err() != nil {
			cause = errors.Join(cause, ctx.Err())
		}
		stdout, stderr := capture.diagnosticStreams()
		err = commandresult.NewExternalError(c, commandresult.RunOptions{Operation: "verification command", WorkspaceRoot: dir}, stdout, stderr, cause, errors.Is(ctx.Err(), context.DeadlineExceeded))
	}
	if acceptanceGrade {
		after, afterErr := readTree(dir)
		if afterErr != nil {
			return ev, out, verificationFinalPolicyError(&VerificationPolicyError{Reason: fmt.Sprintf("verification tree changed or became unreadable after execution: before commit %s tree %s clean=%t; after: %v", before.Commit, before.Tree, before.Clean, afterErr)}, err)
		}
		if after != before {
			return ev, out, verificationFinalPolicyError(&VerificationPolicyError{Reason: fmt.Sprintf("verification tree changed during execution: before commit %s tree %s clean=%t; after commit %s tree %s clean=%t", before.Commit, before.Tree, before.Clean, after.Commit, after.Tree, after.Clean)}, err)
		}
	}
	return ev, out, err
}

func boundVerificationArtifact(out []byte) []byte {
	if len(out) <= maxVerificationArtifact {
		return out
	}
	marker := []byte("\n[verification output truncated at 65536 bytes]\n")
	bounded := append([]byte(nil), out[:maxVerificationArtifact-len(marker)]...)
	return append(bounded, marker...)
}

func readVerificationTree(dir string) (verificationTree, error) {
	var got verificationTree
	if !gitx.Available() {
		return got, errors.New("git is unavailable")
	}
	var err error
	if got.Branch, err = gitx.Run(dir, "branch", "--show-current"); err != nil {
		return got, fmt.Errorf("read branch: %w", err)
	}
	if got.Commit, err = gitx.Run(dir, "rev-parse", "HEAD"); err != nil {
		return got, fmt.Errorf("read HEAD: %w", err)
	}
	if got.Tree, err = gitx.Run(dir, "rev-parse", "HEAD^{tree}"); err != nil {
		return got, fmt.Errorf("read HEAD tree: %w", err)
	}
	status, err := gitx.Run(dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return got, fmt.Errorf("read worktree status: %w", err)
	}
	got.Branch = strings.TrimSpace(got.Branch)
	got.Commit = strings.TrimSpace(got.Commit)
	got.Tree = strings.TrimSpace(got.Tree)
	got.Clean = strings.TrimSpace(status) == ""
	return got, nil
}

func verificationToolVersions(dir string) map[string]string {
	versions := map[string]string{}
	if path, err := exec.LookPath("sh"); err == nil {
		version := "version unavailable"
		c := exec.Command(path, "-c", `printf '%s' "${BASH_VERSION:-${ZSH_VERSION:-version unavailable}}"`)
		if out, err := c.CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
			version = strings.TrimSpace(string(out))
		}
		versions["shell"] = path + " (" + version + ")"
	}
	if out, err := gitx.Run(dir, "--version"); err == nil {
		versions["git"] = strings.TrimSpace(out)
	}
	return versions
}

// ValidateFinalTreeVerification rejects historical or stale evidence that was
// not bound to the exact reviewed/landing artifact. Legacy records remain
// readable, but cannot satisfy this gate.
func ValidateFinalTreeVerification(ev VerificationEvidence, commitSHA, treeSHA string) error {
	if ev.Legacy != "" || ev.CommitSHA == "" || ev.TreeSHA == "" || !ev.Clean {
		return fmt.Errorf("verification evidence is not acceptance-grade: exact commit, tree SHA, and clean-state assertion are required; rerun verification on the reviewed head")
	}
	if err := ValidateCommandVerification(ev); err != nil || len(ev.Argv) == 0 || strings.TrimSpace(ev.WorkingDirectory) == "" || len(ev.RuntimeVersions) == 0 || len(ev.ToolVersions) == 0 || ev.ExitCode != 0 {
		return fmt.Errorf("verification evidence is not acceptance-grade: successful structured command/cwd, verifier, runtime/tool versions, and output digest are required; rerun verification on the reviewed head")
	}
	if ev.CommitSHA != commitSHA || ev.TreeSHA != treeSHA {
		return fmt.Errorf("verification evidence is stale: verified commit %s tree %s, but reviewed head is commit %s tree %s; rerun verification on the reviewed head", ev.CommitSHA, ev.TreeSHA, commitSHA, treeSHA)
	}
	return nil
}

// AttachExternalVerification appends a typed external check without allowing a
// skipped or unobservable check to masquerade as a successful one.
func AttachExternalVerification(ev *VerificationEvidence, external ExternalVerificationEvidence) error {
	if strings.TrimSpace(external.Provider) == "" {
		return errors.New("external verification missing provider")
	}
	switch external.State {
	case "observed":
		if external.WorkflowRunID == "" && external.CheckRunID == "" {
			return errors.New("observed external verification missing workflow run or check ID")
		}
		if strings.TrimSpace(external.Conclusion) == "" {
			return errors.New("observed external verification missing conclusion")
		}
	case "pending":
		if external.WorkflowRunID == "" && external.CheckRunID == "" {
			return errors.New("pending external verification missing workflow run or check ID")
		}
		if strings.TrimSpace(external.Conclusion) != "" {
			return fmt.Errorf("pending external verification cannot carry conclusion %q", external.Conclusion)
		}
	case "skipped", "superseded", "unobservable":
		if strings.TrimSpace(external.SkipReason) == "" {
			return fmt.Errorf("%s external verification missing reason", external.State)
		}
		if strings.TrimSpace(external.Conclusion) != "" {
			return fmt.Errorf("%s external verification cannot carry conclusion %q", external.State, external.Conclusion)
		}
	default:
		return fmt.Errorf("external verification has unknown state %q", external.State)
	}
	for _, artifact := range external.Artifacts {
		if strings.TrimSpace(artifact.ID) == "" || strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Digest) == "" {
			return errors.New("external verification artifact requires id, name, and digest")
		}
	}
	ev.External = append(ev.External, external)
	return nil
}

// ValidateExternalVerification applies configured names to one exact-head
// evidence record. It never treats an unconfigured check as authority, and a
// skipped, pending, superseded, expired, stale-head, missing, or unobservable
// observation cannot satisfy a requirement.
func ValidateExternalVerification(ev VerificationEvidence, policy ExternalVerificationPolicy) error {
	checks := map[string]ExternalVerificationEvidence{}
	artifacts := map[string]ExternalArtifactEvidence{}
	for _, external := range ev.External {
		if policy.HeadSHA != "" && external.HeadSHA != policy.HeadSHA {
			continue
		}
		if external.State == "observed" && strings.EqualFold(external.Conclusion, "success") {
			checks[external.Name] = external
			for _, artifact := range external.Artifacts {
				if !artifact.Expired {
					artifacts[artifact.Name] = artifact
				}
			}
		}
	}
	var missing []string
	for _, name := range policy.RequiredChecks {
		if _, ok := checks[name]; !ok {
			missing = append(missing, "check "+name)
		}
	}
	for _, name := range policy.RequiredArtifacts {
		if _, ok := artifacts[name]; !ok {
			missing = append(missing, "artifact "+name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("external verification requirements are not green for head %s: missing or non-successful %s", policy.HeadSHA, strings.Join(missing, ", "))
	}
	return nil
}

// ObserveGitHubExternalVerification reads check and workflow identities for one
// exact commit. The commit is repeated in every attachment so evidence cannot
// be replayed onto a different reviewed tree (issue #898).
var ObserveGitHubExternalVerification = observeGitHubExternalVerification

func observeGitHubExternalVerification(root, repo, commit string, now time.Time) ([]ExternalVerificationEvidence, error) {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(commit) == "" {
		return nil, errors.New("GitHub verification observation requires repository and exact commit")
	}
	run := func(operation string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Dir = root
		return commandresult.Run(cmd, commandresult.RunOptions{Operation: operation, WorkspaceRoot: root, TimedOut: func() bool { return ctx.Err() == context.DeadlineExceeded }})
	}
	checksRaw, err := run("observe exact-commit GitHub checks", "api", "repos/"+repo+"/commits/"+commit+"/check-runs")
	if err != nil {
		return nil, err
	}
	var checks struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"details_url"`
			HeadSHA    string `json:"head_sha"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(checksRaw, &checks); err != nil {
		return nil, fmt.Errorf("decode exact-commit GitHub checks: %w", err)
	}
	out := make([]ExternalVerificationEvidence, 0, len(checks.CheckRuns))
	for _, check := range checks.CheckRuns {
		if check.HeadSHA != "" && check.HeadSHA != commit {
			continue
		}
		state, conclusion, reason := "pending", "", ""
		if strings.EqualFold(check.Status, "completed") && strings.TrimSpace(check.Conclusion) != "" {
			state, conclusion = "observed", strings.ToLower(check.Conclusion)
			if strings.EqualFold(check.Conclusion, "skipped") {
				state, conclusion, reason = "skipped", "", "GitHub reported skipped"
			}
		}
		out = append(out, ExternalVerificationEvidence{Provider: "github-check", CheckRunID: strconv.FormatInt(check.ID, 10), HeadSHA: commit, URL: check.DetailsURL, Name: check.Name, ObservedAt: now.UTC(), State: state, Conclusion: conclusion, SkipReason: reason})
	}
	runsRaw, err := run("observe exact-commit GitHub workflows", "run", "list", "--repo", repo, "--commit", commit, "--limit", "100", "--json", "databaseId,name,status,conclusion,url,headSha")
	if err != nil {
		return nil, err
	}
	var runs []struct {
		DatabaseID int64  `json:"databaseId"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		URL        string `json:"url"`
		HeadSHA    string `json:"headSha"`
	}
	if err := json.Unmarshal(runsRaw, &runs); err != nil {
		return nil, fmt.Errorf("decode exact-commit GitHub workflows: %w", err)
	}
	for _, workflow := range runs {
		if workflow.HeadSHA != "" && workflow.HeadSHA != commit {
			continue
		}
		state, conclusion, reason := "pending", "", ""
		if strings.EqualFold(workflow.Status, "completed") && strings.TrimSpace(workflow.Conclusion) != "" {
			state, conclusion = "observed", strings.ToLower(workflow.Conclusion)
			if strings.EqualFold(workflow.Conclusion, "skipped") {
				state, conclusion, reason = "skipped", "", "GitHub reported skipped"
			}
		}
		out = append(out, ExternalVerificationEvidence{Provider: "github-actions", WorkflowRunID: strconv.FormatInt(workflow.DatabaseID, 10), HeadSHA: commit, URL: workflow.URL, Name: workflow.Name, ObservedAt: now.UTC(), State: state, Conclusion: conclusion, SkipReason: reason})
	}
	// A single bounded artifact query avoids an N+1 request per workflow. Only
	// artifacts whose workflow-run identity is already bound to this exact head
	// are attached; missing digests are intentionally omitted as unverifiable.
	artifactsRaw, artifactErr := run("observe exact-commit GitHub artifacts", "api", "repos/"+repo+"/actions/artifacts?per_page=100")
	if artifactErr != nil {
		return nil, artifactErr
	}
	var artifactPage struct {
		Artifacts []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Size        int64  `json:"size_in_bytes"`
			URL         string `json:"archive_download_url"`
			Expired     bool   `json:"expired"`
			Digest      string `json:"digest"`
			WorkflowRun struct {
				ID      int64  `json:"id"`
				HeadSHA string `json:"head_sha"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(artifactsRaw, &artifactPage); err != nil {
		return nil, fmt.Errorf("decode exact-commit GitHub artifacts: %w", err)
	}
	byWorkflow := map[string]int{}
	for i := range out {
		if out[i].WorkflowRunID != "" {
			byWorkflow[out[i].WorkflowRunID] = i
		}
	}
	for _, artifact := range artifactPage.Artifacts {
		index, ok := byWorkflow[strconv.FormatInt(artifact.WorkflowRun.ID, 10)]
		if !ok || artifact.WorkflowRun.HeadSHA != "" && artifact.WorkflowRun.HeadSHA != commit || strings.TrimSpace(artifact.Digest) == "" {
			continue
		}
		out[index].Artifacts = append(out[index].Artifacts, ExternalArtifactEvidence{ID: strconv.FormatInt(artifact.ID, 10), Name: artifact.Name, Digest: artifact.Digest, URL: artifact.URL, SizeBytes: artifact.Size, Expired: artifact.Expired})
	}
	// Only the newest identity for a repeated check/workflow name can be
	// authoritative. Older reruns remain visible but are explicitly superseded.
	latest := map[string]int{}
	for i := range out {
		key := out[i].Provider + "\x00" + out[i].Name
		if prior, ok := latest[key]; ok {
			priorID, _ := strconv.ParseInt(out[prior].CheckRunID+out[prior].WorkflowRunID, 10, 64)
			currentID, _ := strconv.ParseInt(out[i].CheckRunID+out[i].WorkflowRunID, 10, 64)
			if currentID > priorID {
				out[prior].State, out[prior].Conclusion, out[prior].SkipReason = "superseded", "", "newer rerun observed"
				latest[key] = i
			} else {
				out[i].State, out[i].Conclusion, out[i].SkipReason = "superseded", "", "newer rerun observed"
			}
		} else {
			latest[key] = i
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Provider+out[i].Name+out[i].CheckRunID+out[i].WorkflowRunID < out[j].Provider+out[j].Name+out[j].CheckRunID+out[j].WorkflowRunID
	})
	return out, nil
}

// ValidateCommandVerification enforces the provenance fields whose absence
// makes it impossible to attribute or bind command evidence to an artifact.
func ValidateCommandVerification(ev VerificationEvidence) error {
	var missing []string
	if strings.TrimSpace(ev.Command) == "" {
		missing = append(missing, "command")
	}
	if strings.TrimSpace(ev.ArtifactHash) == "" {
		missing = append(missing, "artifact hash")
	}
	if strings.TrimSpace(ev.Verifier) == "" {
		missing = append(missing, "verifier identity")
	}
	if len(missing) > 0 {
		return fmt.Errorf("verification evidence missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// AppendVerificationEvidence adds one JSON record without replacing legacy
// task content. JSON-lines keeps appends and mechanical migration simple.
func AppendVerificationEvidence(t *Task, ev VerificationEvidence) error {
	if err := ValidateCommandVerification(ev); err != nil {
		return err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	sec, _ := t.Doc.Section(verificationSection)
	t.Doc.SetSection(verificationSection, sec.Content+string(b)+"\n")
	return nil
}

var legacyVerificationLine = regexp.MustCompile(`(?m)^- .* verified by .+$`)

// VerificationEvidenceRecords reads structured records and also surfaces old
// string-only evidence as Legacy records. It never guesses fields that the old
// rendered sentence did not reliably encode.
func VerificationEvidenceRecords(t *Task) []VerificationEvidence {
	var records []VerificationEvidence
	if sec, ok := t.Doc.Section(verificationSection); ok {
		for _, line := range strings.Split(sec.Content, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var ev VerificationEvidence
			if json.Unmarshal([]byte(line), &ev) == nil {
				records = append(records, ev)
			}
		}
	}
	if len(records) > 0 {
		return records
	}
	if log, ok := t.Doc.Section("Log"); ok {
		for _, line := range legacyVerificationLine.FindAllString(log.Content, -1) {
			records = append(records, VerificationEvidence{Legacy: strings.TrimSpace(line)})
		}
	}
	return records
}

// AcceptanceRequiresCommandVerification recognizes the task format's explicit
// inline-code convention: a criterion containing a backticked command asks the
// checker to run and provenance that command, not merely assert its outcome.
func AcceptanceRequiresCommandVerification(t *Task, criterion int) bool {
	boxes := t.Acceptance()
	if criterion < 1 || criterion > len(boxes) {
		return false
	}
	text := boxes[criterion-1].Text
	start := strings.IndexByte(text, '`')
	return start >= 0 && strings.IndexByte(text[start+1:], '`') >= 0
}

// Contextual observations strip the same credential/Git environment as the
// verifier and disable fsmonitor. Inspect executable repository configuration
// before status so a caller cannot turn observation into filter execution.
func readVerificationTreeContext(ctx context.Context, dir string) (verificationTree, error) {
	run := func(args ...string) (string, error) { return runVerificationGit(ctx, dir, args...) }
	keys, err := run("config", "--null", "--name-only", "--list")
	if err != nil {
		return verificationTree{}, err
	}
	for _, key := range strings.Split(keys, "\x00") {
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process")) {
			return verificationTree{}, errors.New("executable clean filter refused")
		}
	}
	entries, err := run("ls-files", "--stage")
	if err != nil {
		return verificationTree{}, err
	}
	for _, entry := range strings.Split(entries, "\n") {
		if strings.HasPrefix(entry, "160000 ") {
			return verificationTree{}, errors.New("indexed submodule refused")
		}
	}
	var got verificationTree
	if got.Branch, err = run("branch", "--show-current"); err != nil {
		return got, err
	}
	if got.Commit, err = run("rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return got, err
	}
	if got.Tree, err = run("rev-parse", "--verify", "HEAD^{tree}"); err != nil {
		return got, err
	}
	status, err := run("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return got, err
	}
	again, err := run("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || again != got.Commit {
		return got, errors.New("HEAD changed during observation")
	}
	got.Branch = strings.TrimSpace(got.Branch)
	got.Commit = strings.TrimSpace(got.Commit)
	got.Tree = strings.TrimSpace(got.Tree)
	got.Clean = strings.TrimSpace(status) == ""
	return got, nil
}

type verificationGitBuffer struct{ strings.Builder }

func (b *verificationGitBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("git observation output limit exceeded")
	}
	return b.Builder.Write(p)
}

func runVerificationGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = VerificationEnvironment()
	cmd.WaitDelay = 250 * time.Millisecond
	var output verificationGitBuffer
	cmd.Stdout = &output
	if err := runNativeVerificationCommand(cmd); err != nil {
		return "", err
	}
	return output.String(), ctx.Err()
}

// VerificationInterpreterAvailable reports whether the executor's sh-c interpreter
// can be resolved. It neither launches the interpreter nor verifies check commands.
func VerificationInterpreterAvailable() bool {
	_, err := exec.LookPath("sh")
	return err == nil
}
