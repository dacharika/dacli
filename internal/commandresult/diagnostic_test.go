package commandresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunRetainsTypedFailureInsteadOfBareExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "printf 'partial result\\n'; printf 'fatal: index.lock is held\\n' >&2; exit 23")
	cmd.Dir = root
	out, err := Run(cmd, RunOptions{Operation: "git add", WorkspaceRoot: root})
	if err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(string(out), "partial result") || !strings.Contains(string(out), "index.lock") {
		t.Fatalf("compatibility output lost a stream: %q", out)
	}
	diagnostic, ok := AsDiagnostic(fmt.Errorf("feature failed: %w", err))
	if !ok {
		t.Fatalf("wrapped error lost typed diagnostic: %T: %v", err, err)
	}
	if diagnostic.ExitCode == nil || *diagnostic.ExitCode != 23 {
		t.Fatalf("exit code = %v, want 23", diagnostic.ExitCode)
	}
	if diagnostic.StdoutTail != "partial result" || diagnostic.StderrTail != "fatal: index.lock is held" {
		t.Fatalf("stream tails collapsed: %#v", diagnostic)
	}
	if diagnostic.Kind != "contention" || !diagnostic.Retryable || !strings.Contains(diagnostic.NextAction, "stale lock") {
		t.Fatalf("contention classification = %#v", diagnostic)
	}
	if got := err.Error(); !strings.Contains(got, "fatal: index.lock is held") || strings.Contains(got, "exit status 23") {
		t.Fatalf("human error must lead with the actionable cause, not bare status: %q", got)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal("typed diagnostic did not retain the original process cause")
	}
}

func TestSanitizeTailMatchesGovernedCompatibilityBoundary(t *testing.T) {
	const secret = "github_pat_stream_secret_123456"
	t.Setenv("STREAM_API_TOKEN", secret)
	got := SanitizeTail(strings.Repeat("prefix ", 1000)+secret+" /outside/private/file", t.TempDir())
	if len(got) > diagnosticTailBytes+len("[truncated] ") || strings.Contains(got, secret) || strings.Contains(got, "/outside/private/file") {
		t.Fatalf("unsafe stream tail: len=%d value=%q", len(got), got)
	}
}

func TestRunRedactsSecretsAndOutsideWorkspacePathsFromEverySurface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	const secret = "super-secret-token-value"
	t.Setenv("DACLI_TEST_API_TOKEN", secret)
	root := t.TempDir()
	inside := root + "/safe/file.txt"
	outside := "/private/operator/credentials.txt"
	cmd := exec.Command("/bin/sh", "-c", "printf '%s\\n' \"$DACLI_TEST_API_TOKEN\"; printf '%s %s\\n' \"$1\" \"$2\" >&2; exit 9", "fixture", inside, outside)
	cmd.Dir = root
	out, err := Run(cmd, RunOptions{
		Operation:     "provider authenticate " + secret,
		WorkspaceRoot: root,
	})
	if err == nil {
		t.Fatal("expected command failure")
	}
	diagnostic, ok := AsDiagnostic(err)
	if !ok {
		t.Fatalf("missing diagnostic: %v", err)
	}
	encoded, marshalErr := json.Marshal(diagnostic)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for surface, value := range map[string]string{
		"human":  err.Error(),
		"json":   strings.ReplaceAll(strings.ReplaceAll(string(encoded), `\u003c`, "<"), `\u003e`, ">"),
		"output": string(out),
	} {
		if strings.Contains(value, secret) || strings.Contains(value, outside) {
			t.Fatalf("%s surface leaked protected material: %s", surface, value)
		}
		if !strings.Contains(value, "<redacted>") || !strings.Contains(value, "<outside-workspace>") {
			t.Fatalf("%s surface omitted redaction markers: %s", surface, value)
		}
	}
	if !strings.Contains(diagnostic.StderrTail, "<workspace>/safe/file.txt") {
		t.Fatalf("workspace path was not safely scoped: %q", diagnostic.StderrTail)
	}
	if diagnostic.CwdScope != "." {
		t.Fatalf("cwd scope = %q, want .", diagnostic.CwdScope)
	}
}

func TestRunBoundsDiagnosticTails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	cmd := exec.Command("/bin/sh", "-c", "i=0; while [ $i -lt 6000 ]; do printf x >&2; i=$((i+1)); done; printf ' decisive-tail' >&2; exit 1")
	cmd.Dir = t.TempDir()
	_, err := Run(cmd, RunOptions{Operation: "fixture", WorkspaceRoot: cmd.Dir})
	diagnostic, ok := AsDiagnostic(err)
	if !ok {
		t.Fatalf("missing diagnostic: %v", err)
	}
	if len(diagnostic.StderrTail) > diagnosticTailBytes+len("[truncated] ") || !strings.HasSuffix(diagnostic.StderrTail, "decisive-tail") {
		t.Fatalf("stderr tail is not bounded from the actionable end: len=%d suffix=%q", len(diagnostic.StderrTail), actionableLine(diagnostic.StderrTail))
	}
}

func TestRunClassifiesSignalTermination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal fixture")
	}
	cmd := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	cmd.Dir = t.TempDir()
	_, err := Run(cmd, RunOptions{Operation: "worker", WorkspaceRoot: cmd.Dir})
	diagnostic, ok := AsDiagnostic(err)
	if !ok || diagnostic.Kind != "signal" || diagnostic.Signal == "" {
		t.Fatalf("signal diagnostic = %#v, present=%v", diagnostic, ok)
	}
}

func TestCaptureReturnsQuietMutationIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	cmd := exec.Command("/bin/sh", "-c", `printf '{"merged":1,"open":0}' > "$DACLI_COMMAND_RESULT"`)
	cmd.Dir = t.TempDir()
	var result Integration
	out, err := Capture(cmd, &result)
	if err != nil {
		t.Fatalf("quiet mutation capture: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("fixture must be quiet; got presentation output %q", out)
	}
	if result.Merged != 1 || result.Open != 0 {
		t.Fatalf("quiet mutation lost its typed identity: %#v", result)
	}
	if identity, ok := IdentityOf(&result); !ok || identity != "integration merged=1 open=0" {
		t.Fatalf("quiet mutation identity = %q, %v", identity, ok)
	}
}

func TestCaptureRejectsQuietSuccessWithoutInventoriedIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	type untrackedMutation struct {
		Object string `json:"object"`
	}
	cmd := exec.Command("/bin/sh", "-c", `printf '  \n'; printf '{"object":"changed"}' > "$DACLI_COMMAND_RESULT"`)
	cmd.Dir = t.TempDir()
	var result untrackedMutation
	out, err := Capture(cmd, &result)
	if err == nil || !strings.Contains(err.Error(), "no stable result identity") {
		t.Fatalf("uninventoried quiet mutation = output %q, result %#v, error %v", out, result, err)
	}
}

func TestStructuredMutationIdentityInventory(t *testing.T) {
	for name, result := range map[string]any{
		"spawn":       &Spawn{RunID: "01RUN"},
		"integration": &Integration{Merged: 2, Open: 1},
		"wait":        &Wait{Runs: []WaitRun{{RunID: "01RUN"}}},
	} {
		if identity, ok := IdentityOf(result); !ok || strings.TrimSpace(identity) == "" {
			t.Errorf("%s result has no stable identity: %q, %v", name, identity, ok)
		}
	}
}

func TestCapturePreservesTypedCommandFailureWhenResultIsMalformed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	cmd := exec.Command("/bin/sh", "-c", `printf '{malformed' > "$DACLI_COMMAND_RESULT"; printf 'authentication failed decisively\n' >&2; exit 17`)
	cmd.Dir = t.TempDir()
	var result Integration
	_, err := Capture(cmd, &result)
	if err == nil || !strings.Contains(err.Error(), "decode command result") {
		t.Fatalf("Capture error = %v, want malformed-result context", err)
	}
	diagnostic, ok := AsDiagnostic(err)
	if !ok {
		t.Fatalf("malformed result collapsed the governed process failure: %T: %v", err, err)
	}
	if diagnostic.ExitCode == nil || *diagnostic.ExitCode != 17 || diagnostic.Kind != "authentication" {
		t.Fatalf("typed command facts = %#v, want authentication exit 17", diagnostic)
	}
	if !strings.Contains(diagnostic.StderrTail, "authentication failed decisively") {
		t.Fatalf("actionable stderr was lost: %#v", diagnostic)
	}
}

func TestChildEnvironmentSecretsRedacted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, mode := range []string{"Run", "Output", "NewExternalError"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			secret := "synthetic-child-secret-602"
			cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$CHILD_API_TOKEN"; printf 'safe detail %s\n' "$CHILD_API_TOKEN" >&2; exit 17`)
			cmd.Dir = root
			cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
			var out []byte
			var err error
			switch mode {
			case "Run":
				out, err = Run(cmd, RunOptions{Operation: "child fixture", WorkspaceRoot: root})
			case "Output":
				out, err = Output(cmd, RunOptions{Operation: "child fixture", WorkspaceRoot: root})
			default:
				stdout, stderr, cause := output(cmd)
				out = []byte{}
				err = NewExternalError(cmd, RunOptions{Operation: "child fixture", WorkspaceRoot: root}, stdout, stderr, cause, false)
			}
			assertProtectedFailure(t, out, err, secret)
		})
	}
}

func assertProtectedFailure(t *testing.T, out []byte, err error, protected string) {
	t.Helper()
	d, ok := AsDiagnostic(err)
	if !ok || d.ExitCode == nil || *d.ExitCode != 17 {
		t.Fatalf("typed process failure lost: %+v %v", d, err)
	}
	raw, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	for name, s := range map[string]string{"compatibility": string(out), "human": err.Error(), "json": string(raw)} {
		if strings.Contains(s, protected) {
			t.Errorf("protected material leaked on %s: %s", name, s)
		}
	}
	var cause *exec.ExitError
	if !errors.As(err, &cause) {
		t.Fatal("original process cause lost")
	}
}

func TestQuotedPathsRedactedWithoutLosingWorkspaceOrURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	outside := "/private/operator/secret folder/credentials.txt"
	inside := root + "/safe folder/file.txt"
	cmd := exec.Command("/bin/sh", "-c", `printf "safe diagnostic\nfatal: cannot open '%s'\n" "$1" >&2; exit 17`, "fixture", outside)
	cmd.Dir = root
	out, err := Run(cmd, RunOptions{Operation: "git fixture", WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, outside)
	for _, quoted := range []string{"'" + inside + "'", `"` + inside + `"`} {
		got := Redact(quoted, root)
		if !strings.Contains(got, "<workspace>/safe folder/file.txt") {
			t.Fatalf("safe workspace path lost: %q", got)
		}
	}
	for _, quoted := range []string{"'" + outside + "'", `"` + outside + `"`, "'" + outside, `"` + outside} {
		if got := Redact(quoted, root); strings.Contains(got, outside) || !strings.Contains(got, "<outside-workspace>") {
			t.Fatalf("quoted or malformed private path leaked: %q", got)
		}
	}
	if got := Redact("https://fixture:synthetic-url-password@example.invalid/safe/path", root); strings.Contains(got, "synthetic-url-password") || !strings.Contains(got, "example.invalid/safe/path") {
		t.Fatalf("URL credential policy changed: %q", got)
	}
	if got := Redact("see https://example.invalid/safe/path for remediation", root); got != "see https://example.invalid/safe/path for remediation" {
		t.Fatalf("safe URL changed: %q", got)
	}
}

func TestCredentialHelperSecretFieldsRedacted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, key := range []string{"password", "token", "access_token", "oauth_token"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			secret := "synthetic-helper-secret-602"
			cmd := exec.Command("/bin/sh", "-c", `printf 'username=fixture\n%s=%s\nsafe actionable line\n' "$1" "$2" >&2; exit 17`, "fixture", key, secret)
			cmd.Dir = root
			out, err := Run(cmd, RunOptions{Operation: "credential fixture", WorkspaceRoot: root})
			assertProtectedFailure(t, out, err, secret)
			if !strings.Contains(string(out), "username=fixture") || !strings.Contains(err.Error(), "safe actionable line") {
				t.Fatal("safe helper/actionable context lost")
			}
			// Put the helper field last as well: Error() presents that line, so
			// this exercises human redaction rather than hiding behind safe prose.
			cmd = exec.Command("/bin/sh", "-c", `printf 'safe context\n%s=%s\n' "$1" "$2" >&2; exit 17`, "fixture", key, secret)
			cmd.Dir = root
			out, err = Run(cmd, RunOptions{Operation: "credential fixture", WorkspaceRoot: root})
			assertProtectedFailure(t, out, err, secret)
		})
	}
}

func TestCommandRedactionPrecedesTailBoundAndSuccessUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	secret := strings.Repeat("secret-fragment-", 600)
	cmd := exec.Command("/bin/sh", "-c", `printf '%s' "$CHILD_API_TOKEN" >&2; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
	out, err := Run(cmd, RunOptions{Operation: "tail fixture", WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, "secret-fragment-")
	cmd = exec.Command("/bin/sh", "-c", `printf 'successful literal bytes'; printf ' stderr' >&2`)
	cmd.Dir = root
	out, err = Run(cmd, RunOptions{Operation: "success", WorkspaceRoot: root})
	if err != nil || string(out) != "successful literal bytes stderr" {
		t.Fatalf("successful compatibility changed: %q %v", out, err)
	}
}

func TestChildEnvironmentOverlapNeverLeaksSuffix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	parent := "synthetic-prefix-602"
	child := parent + "-distinct-private-suffix"
	t.Setenv("PARENT_API_TOKEN", parent)
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$CHILD_API_TOKEN"; printf '%s\n' "$CHILD_API_TOKEN" >&2; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + child}
	out, err := Run(cmd, RunOptions{Operation: "fixture " + child, WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, "distinct-private-suffix")
	d, _ := AsDiagnostic(err)
	if strings.Contains(d.Operation, "distinct-private-suffix") {
		t.Fatalf("effective child secret fragment leaked in operation: %s", d.Operation)
	}
}

func TestSanitizeCommandTailUsesEffectiveSecretsBeforeBounds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	parent := "synthetic-stream-prefix-602"
	secret := parent + strings.Repeat("-stream-private-fragment", 400)
	t.Setenv("PARENT_API_TOKEN", parent)
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$CHILD_API_TOKEN"; printf "password=synthetic-stream-helper\n'/private/operator/space folder/file'\nfatal: actionable line\n" >&2; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
	stdout, stderr, cause := output(cmd)
	if cause == nil {
		t.Fatal("actual child must fail")
	}
	value := string(stdout) + string(stderr)
	got := SanitizeCommandTail(cmd, value, root)
	for _, protected := range []string{"stream-private-fragment", "synthetic-stream-helper", "/private/operator/space folder/file"} {
		if strings.Contains(got, protected) {
			t.Fatalf("command-aware bounded tail leaked %s: %s", protected, got)
		}
	}
	if !strings.Contains(got, "fatal: actionable line") || len(got) > diagnosticTailBytes+len("[truncated] ") {
		t.Fatalf("command-aware tail lost actionable bound: %s", got)
	}
	_, err := Run(exec.Command("/bin/sh", "-c", "exit 17"), RunOptions{})
	if _, ok := AsDiagnostic(err); !ok {
		t.Fatal("typed failure contract changed")
	}
	if got := SanitizeCommandTail(cmd, strings.Repeat("safe ", 2000)+"decisive safe end", root); len(got) > diagnosticTailBytes+len("[truncated] ") || !strings.HasSuffix(got, "decisive safe end") {
		t.Fatal("streaming bound changed")
	}
}

func TestSanitizeCommandTailProtectsMultilineSecretComponents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	secret := "synthetic-first-private-line\nsynthetic-second-private-line"
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$CHILD_API_TOKEN"; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
	stdout, _, cause := output(cmd)
	if cause == nil {
		t.Fatal("actual child must fail")
	}
	for _, line := range strings.Split(string(stdout), "\n") {
		if line == "" {
			continue
		}
		if got := SanitizeCommandTail(cmd, line, root); strings.Contains(got, line) {
			t.Fatalf("multiline secret component leaked through line sanitizer: %s", got)
		}
	}
	if got := SanitizeCommandTail(cmd, "fatal: safe subsequent line", root); got != "fatal: safe subsequent line" {
		t.Fatalf("safe subsequent line changed: %s", got)
	}
}

func TestKnownSecretsCannotEraseStructuralDisclosure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	cases := []struct{ name, secret, raw, protected string }{
		{"helper", "password", "password=synthetic-distinct-helper-private-value", "synthetic-distinct-helper-private-value"},
		{"url", "https", "https://fixture:synthetic-private-userinfo@example.invalid/safe/path", "synthetic-private-userinfo"},
		{"token", "ghp_", "ghp_synthetic_private_token602", "synthetic_private_token602"},
		{"quoted-path", "/private", "'/private/operator/secret folder/private-file602'", "secret folder/private-file602"},
		{"unquoted-path", "/private", "/private/operator/private-file602", "operator/private-file602"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$1" >&2; exit 17`, "fixture", tc.raw)
			cmd.Dir = root
			cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + tc.secret}
			out, err := Run(cmd, RunOptions{Operation: "structural fixture", WorkspaceRoot: root})
			assertProtectedFailure(t, out, err, tc.protected)
			if got := SanitizeCommandTail(cmd, tc.raw, root); strings.Contains(got, tc.protected) {
				t.Errorf("structural secret leaked through command-tail: %s", got)
			}
		})
	}
}

func TestCommandDerivedMetadataProtectsKnownSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	secret := "synthetic-private-metadata602"
	dir := filepath.Join(root, secret)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "executable-"+secret)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
	out, err := Run(cmd, RunOptions{WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, secret)
	d, _ := AsDiagnostic(err)
	for name, value := range map[string]string{"executable": d.Executable, "empty-operation-fallback": d.Operation, "cwd": d.CwdScope} {
		if strings.Contains(value, secret) {
			t.Errorf("derived metadata secret leaked in %s: %s", name, value)
		}
	}
}

func TestKnownSecretContainingStructureIsProtectedAsWhole(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	root := t.TempDir()
	secret := "synthetic-outer-private-https://fixture:synthetic-user-info@example.invalid/safe-tail"
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' "$CHILD_API_TOKEN" >&2; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + secret}
	out, err := Run(cmd, RunOptions{Operation: "whole secret fixture", WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, "synthetic-outer-private")
	if got := SanitizeCommandTail(cmd, secret, root); strings.Contains(got, "synthetic-outer-private") {
		t.Errorf("known structural secret fragment leaked through command-tail: %s", got)
	}
}

func TestKnownSecretsOverlappingAtDifferentOffsets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	t.Setenv("PARENT_API_TOKEN", "parentprefix")
	child := "prefix-distinct-private-suffix"
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `printf 'parent%s\n' "$CHILD_API_TOKEN" >&2; exit 17`)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "CHILD_API_TOKEN=" + child}
	out, err := Run(cmd, RunOptions{Operation: "overlap offset fixture", WorkspaceRoot: root})
	assertProtectedFailure(t, out, err, "distinct-private-suffix")
}
